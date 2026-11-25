// Package synth is a seeded, synthetic organization that serves records
// through the measure.Source interface. It holds no real data: every name is
// invented, and the same Config always gives the same records in the same
// order.
//
// The organization is made for tests, benchmarks and scale checks of code that
// builds a line from records. It covers the shapes a builder has to handle:
// changes that merged, were closed unmerged, are open, or are drafts; reviews
// by people and by agents; commits including merge commits and fixes pushed
// after the first review; CI runs with and without a runner wait, re-runs and
// merge queue runs; production deploys by deployment record and by release;
// reverts; and authors that are people, coding agents and automation.
//
// A repository is generated from its own seeded random stream, so its records
// do not depend on any other repository, and only one repository is held in
// memory at a time.
package synth

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/record"
)

// Config sizes a synthetic organization.
type Config struct {
	// Seed picks the organization. The same Config gives the same records.
	Seed int64
	// Repos is how many repositories the organization has.
	Repos int
	// ChangesPerWeek is about how many changes the whole organization opens
	// in a week. Each repository gets its own share, so repositories differ.
	ChangesPerWeek int
	// Weeks is how many weeks of history the organization has.
	Weeks int
	// Start is when the history begins. A zero Start means 5 January 2026.
	Start time.Time
}

// Default is the organization of the scale check: 300 repositories, about
// 1,500 changes a week over the whole organization, and 26 weeks.
func Default() Config {
	return Config{Seed: 1, Repos: 300, ChangesPerWeek: 1500, Weeks: 26, Start: defaultStart}
}

var defaultStart = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

// Source is the synthetic organization. It implements measure.Source. It is
// safe for concurrent use.
type Source struct {
	cfg        Config
	start, end time.Time

	mu    sync.Mutex
	idx   int
	cache *repoData
}

var _ measure.Source = (*Source)(nil)

// New makes the source of a configuration. Values below one are raised to
// one, except ChangesPerWeek, which may be zero.
func New(c Config) *Source {
	if c.Repos < 1 {
		c.Repos = 1
	}
	if c.Weeks < 1 {
		c.Weeks = 1
	}
	if c.ChangesPerWeek < 0 {
		c.ChangesPerWeek = 0
	}
	if c.Start.IsZero() {
		c.Start = defaultStart
	}
	c.Start = c.Start.UTC()
	return &Source{cfg: c, start: c.Start, end: c.Start.Add(time.Duration(c.Weeks) * 7 * 24 * time.Hour), idx: -1}
}

// Config is the configuration the source was made with, after defaults.
func (s *Source) Config() Config { return s.cfg }

// End is the end of the history: the time the last read of every repository
// finished.
func (s *Source) End() time.Time { return s.end }

// RepoName is the name of the i-th repository, counting from zero.
func RepoName(i int) string {
	return fmt.Sprintf("acme-%03d/svc-%03d", i/10, i)
}

func repoIndex(name string, n int) (int, bool) {
	_, svc, ok := strings.Cut(name, "/svc-")
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(svc)
	if err != nil || i < 0 || i >= n || RepoName(i) != name {
		return 0, false
	}
	return i, true
}

// Scopes lists the repositories, in order.
func (s *Source) Scopes() []measure.Scope {
	out := make([]measure.Scope, s.cfg.Repos)
	for i := range out {
		out[i] = measure.Scope{Source: record.GitHub, Name: RepoName(i), Private: i%7 == 3, From: s.start, SyncedAt: s.end}
	}
	return out
}

func (s *Source) data(sc measure.Scope) (*repoData, error) {
	i, ok := repoIndex(sc.Name, s.cfg.Repos)
	if !ok || sc.Source != record.GitHub {
		return nil, fmt.Errorf("synth: no such scope %s:%s", sc.Source, sc.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil || s.idx != i {
		s.cache, s.idx = s.generate(i), i
	}
	return s.cache, nil
}

func in(w measure.Window, t time.Time) bool {
	return (w.From.IsZero() || !t.Before(w.From)) && (w.To.IsZero() || !t.After(w.To))
}

// Changes reads the changes opened inside the window.
func (s *Source) Changes(sc measure.Scope, w measure.Window, fn func(*record.Change)) error {
	d, err := s.data(sc)
	if err != nil {
		return err
	}
	for i := range d.changes {
		if in(w, d.changes[i].CreatedAt) {
			fn(&d.changes[i])
		}
	}
	return nil
}

// CIRuns reads the runs created inside the window.
func (s *Source) CIRuns(sc measure.Scope, w measure.Window, fn func(*record.CIRun)) error {
	d, err := s.data(sc)
	if err != nil {
		return err
	}
	for i := range d.runs {
		if in(w, d.runs[i].CreatedAt) {
			fn(&d.runs[i])
		}
	}
	return nil
}

// Deploys reads the deploys created inside the window.
func (s *Source) Deploys(sc measure.Scope, w measure.Window, fn func(*record.Deploy)) error {
	d, err := s.data(sc)
	if err != nil {
		return err
	}
	for i := range d.deploys {
		if in(w, d.deploys[i].CreatedAt) {
			fn(&d.deploys[i])
		}
	}
	return nil
}

// Tickets reads nothing: the organization has no ticket source.
func (s *Source) Tickets(measure.Scope, measure.Window, func(*record.Ticket)) error { return nil }

// Links reads the revert links of a repository. A link has no time, so the
// window does not cut them, as in the store.
func (s *Source) Links(sc measure.Scope, _ measure.Window, fn func(*record.Link)) error {
	d, err := s.data(sc)
	if err != nil {
		return err
	}
	for i := range d.links {
		fn(&d.links[i])
	}
	return nil
}

// repoData is everything one repository holds.
type repoData struct {
	changes []record.Change
	runs    []record.CIRun
	deploys []record.Deploy
	links   []record.Link
}

// mix is the splitmix64 step. It spreads a seed and an index into two
// independent stream words.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// gen holds the state of generating one repository.
type gen struct {
	r          *rand.Rand
	cfg        Config
	end        time.Time
	repo       string
	idx        int
	deployMode bool // production deploys are deployment records, else releases
	ciWait     bool // the source reports when a runner picked the run up
	queue      bool // merges go through a merge queue
	mixedBase  bool // some changes go to a release branch

	out      repoData
	runNo    int64
	deployNo int
	merged   []mergedChange
}

type mergedChange struct {
	num  int
	at   time.Time
	sha  string
	base string
}

var (
	people  = 40
	bots    = []string{"copilot-pull-request-reviewer[bot]", "gemini-code-assist[bot]", "coderabbitai[bot]", "claude[bot]", "cursor[bot]", "chatgpt-codex-connector[bot]"}
	autoBot = []string{"dependabot[bot]", "renovate[bot]", "release-please[bot]", "github-actions[bot]", "acme-sync[bot]"}
	words   = []string{"cache", "retry", "queue", "index", "parser", "billing", "auth", "export", "limits", "search", "upload", "audit", "config", "metrics", "router"}
	verbs   = []string{"Add", "Fix", "Speed up", "Remove", "Rename", "Handle", "Document", "Split", "Guard", "Tidy"}
)

// outcomes of a change.
const (
	oMerged = iota
	oClosed
	oOpen
	oDraftOpen
	oDraftReady
	nOutcomes
)

// authors of a change.
const (
	aHuman = iota
	aAgent
	aAutomation
	aHumanAgentBranch // a person on a branch named like an agent's: a low signature only
	nAuthors
)

// d is a duration between lo and hi with a random nanosecond part.
func (g *gen) d(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(g.r.Int64N(int64(hi-lo)))
}

func (g *gen) chance(p float64) bool { return g.r.Float64() < p }

func (g *gen) sha() string {
	return fmt.Sprintf("%016x%016x%08x", g.r.Uint64(), g.r.Uint64(), g.r.Uint32())
}

func (g *gen) person() string { return fmt.Sprintf("dev-%02d", g.r.IntN(people)) }

func (s *Source) generate(i int) *repoData {
	c := s.cfg
	seed := mix(uint64(c.Seed) ^ mix(uint64(i)+1))
	g := &gen{r: rand.New(rand.NewPCG(seed, mix(seed^0x5851f42d4c957f2d))), cfg: c, end: s.end, repo: RepoName(i), idx: i}
	g.deployMode = g.r.IntN(10) < 6
	g.ciWait = g.r.IntN(2) == 0
	g.queue = g.r.IntN(3) == 0
	g.mixedBase = g.r.IntN(4) == 0

	avg := float64(c.ChangesPerWeek) / float64(c.Repos)
	share := 0.25 + 1.5*g.r.Float64()
	n := int(math.Round(avg * float64(c.Weeks) * share))
	if n < 1 && c.ChangesPerWeek > 0 {
		n = 1
	}

	span := s.end.Sub(s.start)
	at := make([]time.Time, n)
	for j := range at {
		at[j] = s.start.Add(time.Duration(g.r.Int64N(int64(span))))
	}
	sort.Slice(at, func(a, b int) bool { return at[a].Before(at[b]) })
	for j := 1; j < n; j++ {
		if g.chance(0.04) { // an exact tie in the time two changes opened
			at[j] = at[j-1]
		}
	}
	for j, t := range at {
		g.change(j+1, t)
	}
	g.shipping()
	g.noise()

	sort.SliceStable(g.out.runs, func(a, b int) bool { return g.out.runs[a].CreatedAt.Before(g.out.runs[b].CreatedAt) })
	sort.SliceStable(g.out.deploys, func(a, b int) bool { return g.out.deploys[a].CreatedAt.Before(g.out.deploys[b].CreatedAt) })
	return &g.out
}

func tp(t time.Time) *time.Time { return &t }

func (g *gen) agentIdentity() (login, kind string, branch string, trailer bool, footer bool, email string) {
	switch g.r.IntN(6) {
	case 0:
		return "claude[bot]", record.ActorBot, "claude/", false, false, ""
	case 1:
		return g.person(), record.ActorUser, "feature/", true, true, "noreply@anthropic.com"
	case 2:
		return "Copilot", record.ActorBot, "copilot/", false, false, ""
	case 3:
		return "cursor[bot]", record.ActorBot, "cursor/", false, false, ""
	case 4:
		return "devin-ai-integration[bot]", record.ActorBot, "devin/", false, false, ""
	}
	return g.person(), record.ActorUser, "codex/", true, false, "codex@openai.com"
}

// change generates one change with its commits, reviews, runs and deploys.
func (g *gen) change(num int, created time.Time) {
	r := g.r
	forced := num <= nOutcomes*nAuthors
	outcome, authorKind := 0, 0
	if forced {
		outcome, authorKind = (num-1)%nOutcomes, (num-1)%nAuthors
	} else {
		outcome = pickWeighted(r, []float64{58, 12, 14, 6, 10})
		authorKind = pickWeighted(r, []float64{60, 28, 8, 4})
	}
	flag := func(every int, p float64) bool { return (forced && num%every == 0) || g.chance(p) }
	tie, doRevert, mergeCommit, fix, viaQueue := flag(6, 0.04), flag(11, 0.04), flag(5, 0.15), flag(3, 0.35), flag(4, 0.4)

	id := record.ChangeID(record.GitHub, g.repo, num)
	ch := record.Change{Source: record.GitHub, ID: id, Repo: g.repo, Number: num,
		URL:     fmt.Sprintf("https://code.example.invalid/%s/pull/%d", g.repo, num),
		BaseRef: "main", CreatedAt: created}
	if g.mixedBase && g.chance(0.2) {
		ch.BaseRef = "release/1." + strconv.Itoa(1+r.IntN(3))
	}
	word := words[r.IntN(len(words))]
	ch.Title = fmt.Sprintf("%s %s %d", verbs[r.IntN(len(verbs))], word, num)
	ch.Body = "Changes the " + word + " path."

	// Who wrote it.
	author := record.Actor{Login: g.person(), Kind: record.ActorUser}
	commitLogin, commitEmail := author.Login, ""
	var trailer, footer bool
	branch := "feature/"
	switch authorKind {
	case aAgent:
		var login, kind string
		login, kind, branch, trailer, footer, commitEmail = g.agentIdentity()
		author = record.Actor{Login: login, Kind: kind}
		commitLogin = login
		if kind == record.ActorUser {
			commitLogin = author.Login
		}
	case aAutomation:
		bot := autoBot[r.IntN(len(autoBot))]
		author = record.Actor{Login: bot, Kind: record.ActorBot}
		commitLogin = bot
		branch = strings.TrimSuffix(bot, "[bot]") + "/"
	case aHumanAgentBranch:
		branch = []string{"claude/", "codex/", "cursor/", "copilot/"}[r.IntN(4)]
	}
	ch.Author = author
	ch.HeadRef = fmt.Sprintf("%s%s-%d", branch, word, num)
	if footer {
		ch.Body += "\n\nGenerated with [Claude Code](https://claude.com/claude-code)"
	}
	if authorKind == aAutomation {
		ch.Title = "chore(deps): bump " + word + " to 1." + strconv.Itoa(num%9)
	}

	draft := outcome == oDraftOpen || outcome == oDraftReady
	ready := created
	if outcome == oDraftReady {
		ready = created.Add(g.d(10*time.Minute, 40*time.Hour))
	}
	if outcome != oDraftOpen {
		ch.ReadyAt = tp(ready)
	}
	ch.Draft = outcome == oDraftOpen

	// Reviews.
	nrev := r.IntN(4)
	if authorKind == aAutomation {
		nrev = r.IntN(2)
	}
	if draft && outcome == oDraftOpen {
		nrev = r.IntN(2)
	}
	revFrom := ready
	if outcome == oDraftOpen {
		revFrom = created
	}
	var reviews []record.Review
	t := revFrom.Add(g.d(5*time.Minute, 30*time.Hour))
	for k := 0; k < nrev; k++ {
		if k > 0 {
			t = t.Add(g.d(2*time.Minute, 20*time.Hour))
		}
		reviews = append(reviews, g.review(t, author.Login, k == 0, k == nrev-1))
	}
	firstReview := t
	if nrev > 0 {
		firstReview = reviews[0].SubmittedAt
	}

	// Commits: some before the change opened, some after the first review.
	var times []time.Time
	first := created
	switch {
	case g.chance(0.5):
		first = created.Add(-g.d(time.Minute, 5*time.Hour))
	case g.chance(0.04):
		first = created.Add(-g.d(40*time.Hour, 100*time.Hour))
	case g.chance(0.01):
		first = created.Add(-g.d(100*24*time.Hour, 140*24*time.Hour)) // older than the early commit limit
	}
	nc := 1 + r.IntN(4)
	if authorKind == aAutomation {
		nc = 1
	}
	times = append(times, first.Add(g.d(0, 20*time.Minute)))
	for k := 1; k < nc; k++ {
		times = append(times, created.Add(g.d(0, revFrom.Sub(created)+time.Hour)))
	}
	if nrev > 0 && fix {
		for k := r.IntN(2) + 1; k > 0; k-- {
			times = append(times, firstReview.Add(g.d(time.Minute, 6*time.Hour)))
		}
	}
	sort.Slice(times, func(a, b int) bool { return times[a].Before(times[b]) })
	if len(times) > 1 && g.chance(0.05) { // two commits pushed at the same instant
		times[1] = times[0]
	}
	mergeAt := -1
	if mergeCommit && len(times) > 1 {
		mergeAt = 1 + r.IntN(len(times)-1)
	}
	for k, ct := range times {
		cm := record.Commit{SHA: g.sha(), CommittedAt: ct, Parents: 1}
		cm.AuthoredAt = ct.Add(-g.d(0, 3*time.Hour))
		if k == 0 {
			cm.AuthoredAt = first
		}
		if cm.AuthoredAt.After(ct) {
			cm.AuthoredAt = ct
		}
		if k == mergeAt {
			cm.Parents = 2
		}
		cm.Authors = []record.CommitAuthor{{Login: commitLogin, Email: commitLogin + "@example.invalid", Name: commitLogin}}
		if commitEmail != "" && !trailer {
			cm.Authors[0].Email = commitEmail
		}
		if trailer {
			name, mail := "Claude", "noreply@anthropic.com"
			if commitEmail == "codex@openai.com" {
				name, mail = "Codex", commitEmail
			}
			cm.Trailers = []record.Trailer{{Key: "Co-Authored-By", Value: fmt.Sprintf("%s <%s>", name, mail)}}
			cm.Authors = append(cm.Authors, record.CommitAuthor{Email: mail, Name: name})
		}
		ch.Commits = append(ch.Commits, cm)
	}
	ch.CommitCount = len(ch.Commits)
	if g.chance(0.02) {
		ch.CommitCount += 100 + r.IntN(100) // the list holds the first 100
	}

	// Exact ties between instants the builder compares.
	last := times[len(times)-1]
	if tie && nrev >= 1 {
		reviews[nrev-1].SubmittedAt = last
		if nrev >= 2 {
			reviews[nrev-1].SubmittedAt = reviews[nrev-2].SubmittedAt
		}
	}
	sort.SliceStable(reviews, func(a, b int) bool { return reviews[a].SubmittedAt.Before(reviews[b].SubmittedAt) })

	// Size.
	add := int(math.Exp(r.NormFloat64()*1.1 + 4))
	ch.Additions, ch.Deletions = add, int(float64(add)*r.Float64())
	ch.ChangedFiles = 1 + (ch.Additions+ch.Deletions)/40 + r.IntN(3)

	// How it ends.
	end := last
	for _, rv := range reviews {
		if rv.SubmittedAt.After(end) {
			end = rv.SubmittedAt
		}
	}
	if ready.After(end) {
		end = ready
	}
	ch.UpdatedAt = end
	switch outcome {
	case oMerged, oDraftReady:
		m := end.Add(g.d(time.Minute, 26*time.Hour))
		if tie && g.chance(0.5) {
			m = end // merged at the instant of the last activity
		}
		ch.State = record.ChangeMerged
		ch.MergedAt, ch.ClosedAt, ch.UpdatedAt = tp(m), tp(m), m
		ch.MergeSHA = g.sha()
		if g.chance(0.2) {
			ch.MergeSHA = ch.Commits[len(ch.Commits)-1].SHA
		}
	case oClosed:
		c := end.Add(g.d(time.Hour, 72*time.Hour))
		ch.State = record.ChangeClosed
		ch.ClosedAt, ch.UpdatedAt = tp(c), c
	default:
		ch.State = record.ChangeOpen
	}
	if !ch.UpdatedAt.Before(g.end) && ch.State != record.ChangeOpen {
		// It would end after the history does: it is still open at the end.
		ch.State, ch.MergedAt, ch.ClosedAt, ch.MergeSHA = record.ChangeOpen, nil, nil, ""
		ch.UpdatedAt = end
	}
	// A review or a commit written after the end of history has not happened.
	ch.Commits = cutCommits(ch.Commits, g.end)
	ch.Reviews = cutReviews(reviews, g.end)
	if ch.State != record.ChangeOpen && g.chance(0.1) {
		// A review that lands after the change closed, which counts for nothing.
		late := record.Review{Author: record.Actor{Login: g.person(), Kind: record.ActorUser}, State: record.ReviewCommented,
			SubmittedAt: ch.UpdatedAt.Add(g.d(time.Minute, 5*time.Hour)), Via: record.ReviewViaReview, Body: "Late note."}
		if late.SubmittedAt.Before(g.end) {
			ch.Reviews = append(ch.Reviews, late)
		}
	}

	// A revert of an earlier merged change.
	if doRevert && ch.State == record.ChangeMerged {
		for k := len(g.merged) - 1; k >= 0; k-- {
			m := g.merged[k]
			if m.at.Before(created) {
				form := "Reverts " + g.repo + "#" + strconv.Itoa(m.num)
				if num%2 == 0 {
					form = "Reverts #" + strconv.Itoa(m.num)
				}
				ch.Title = "Revert: " + ch.Title
				ch.Body += "\n\n" + form
				g.out.links = append(g.out.links, record.Link{Kind: record.LinkReverts, From: id, FromKind: record.KindChange,
					To: record.ChangeID(record.GitHub, g.repo, m.num), ToKind: record.KindChange, Found: "body"})
				break
			}
		}
	}
	if ch.State == record.ChangeMerged {
		g.merged = append(g.merged, mergedChange{num: num, at: *ch.MergedAt, sha: ch.MergeSHA, base: ch.BaseRef})
	}

	g.out.changes = append(g.out.changes, ch)
	g.runs(&ch, tie, viaQueue)
	g.preview(&ch)
}

func cutCommits(cs []record.Commit, end time.Time) []record.Commit {
	out := cs[:0:0]
	for _, c := range cs {
		if len(out) == 0 || c.CommittedAt.Before(end) {
			out = append(out, c)
		}
	}
	return out
}

func cutReviews(rs []record.Review, end time.Time) []record.Review {
	out := rs[:0:0]
	for _, r := range rs {
		if r.SubmittedAt.Before(end) {
			out = append(out, r)
		}
	}
	return out
}

func pickWeighted(r *rand.Rand, w []float64) int {
	var sum float64
	for _, x := range w {
		sum += x
	}
	v := r.Float64() * sum
	for i, x := range w {
		if v < x {
			return i
		}
		v -= x
	}
	return len(w) - 1
}

// review makes one review, or one comment, at a time.
func (g *gen) review(at time.Time, prAuthor string, first, last bool) record.Review {
	r := g.r
	rv := record.Review{SubmittedAt: at, Via: record.ReviewViaReview, State: record.ReviewCommented}
	if g.chance(0.03) {
		rv.Via = "" // a store written before comments were read
	}
	state := func() string {
		switch {
		case g.chance(0.03):
			return record.ReviewDismissed
		case first && g.chance(0.3):
			return record.ReviewChangesRequested
		case last && g.chance(0.7):
			return record.ReviewApproved
		case g.chance(0.5):
			return record.ReviewApproved
		}
		return record.ReviewCommented
	}
	person := func() record.Actor {
		for {
			l := g.person()
			if l != prAuthor {
				return record.Actor{Login: l, Kind: record.ActorUser}
			}
		}
	}
	switch pickWeighted(r, []float64{55, 20, 7, 8, 6, 4}) {
	case 0: // a person reviews
		rv.Author, rv.State, rv.Body = person(), state(), "Looks reasonable."
		if g.chance(0.3) {
			rv.Body = ""
		}
	case 1: // an agent account reviews
		rv.Author = record.Actor{Login: bots[r.IntN(len(bots))], Kind: record.ActorBot}
		rv.State, rv.Body = state(), "Automated review of this change."
	case 2: // an agent writes through a person's account, in a comment with its footer
		rv.Author, rv.Via, rv.State = person(), record.ReviewViaComment, record.ReviewCommented
		rv.Body = "Two small findings.\n\nGenerated with [Claude Code](https://claude.com/claude-code)"
	case 3: // a plain comment: it is not a review
		rv.Author, rv.Via, rv.State, rv.Body = person(), record.ReviewViaComment, record.ReviewCommented, "Thanks, taking a look."
	case 4: // the author's own account, with text
		rv.Author, rv.State, rv.Body = record.Actor{Login: prAuthor, Kind: record.ActorUser}, record.ReviewCommented, "Findings from my agent run."
	default: // a reply to a review thread, no text
		rv.Author, rv.State = record.Actor{Login: prAuthor, Kind: record.ActorUser}, record.ReviewCommented
	}
	if rv.Author.Login == "" {
		rv.Author = person()
	}
	return rv
}

// runs makes the CI runs of a change.
func (g *gen) runs(ch *record.Change, tie, viaQueue bool) {
	lastIdx := len(ch.Commits) - 1
	for k := range ch.Commits {
		cm := &ch.Commits[k]
		if cm.Parents >= 2 || (k != lastIdx && g.chance(0.3)) {
			continue
		}
		flows := []string{"CI"}
		if g.chance(0.4) {
			flows = append(flows, "Lint")
		}
		if g.chance(0.15) {
			flows = append(flows, "Security")
		}
		for _, wf := range flows {
			g.run(ch, cm.SHA, wf, "pull_request", ch.HeadRef, cm.CommittedAt, k == lastIdx, tie && k == lastIdx)
		}
	}
	if ch.State != record.ChangeMerged {
		return
	}
	m := *ch.MergedAt
	if g.queue && viaQueue {
		q := g.sha()
		branch := fmt.Sprintf("gh-readonly-queue/%s/pr-%d-%s", ch.BaseRef, ch.Number, q[:12])
		g.run(ch, q, "CI", "merge_group", branch, m.Add(-g.d(2*time.Minute, 40*time.Minute)), true, false)
	}
	if g.chance(0.6) {
		g.run(ch, ch.MergeSHA, "CI", "push", ch.BaseRef, m.Add(g.d(time.Second, time.Minute)), true, false)
	}
}

// run makes one run, and sometimes its re-run.
func (g *gen) run(ch *record.Change, sha, workflow, event, branch string, after time.Time, final, tie bool) {
	r := g.r
	g.runNo++
	created := after.Add(g.d(0, 40*time.Second))
	if tie {
		created = after // the run is created at the instant of the commit
	}
	started := created
	if g.ciWait && !g.chance(0.1) {
		started = created.Add(g.d(time.Second, 8*time.Minute))
	}
	dur := g.d(30*time.Second, 25*time.Minute)
	x := record.CIRun{Source: record.GitHub, Repo: g.repo, Workflow: workflow, Event: event, SHA: sha, Branch: branch,
		Attempt: 1, Status: "completed", CreatedAt: created, StartedAt: started, UpdatedAt: started.Add(dur)}
	x.ID = record.CIRunID(record.GitHub, g.repo, int64(g.idx)*1_000_000+g.runNo, 1)
	switch v := r.Float64(); {
	case final && ch.State == record.ChangeOpen && !ch.Draft && g.chance(0.08):
		x.Status, x.Conclusion, x.UpdatedAt = "in_progress", "", created
	case v < 0.04:
		x.Conclusion = "cancelled"
	case v < 0.07:
		x.Conclusion = "skipped"
	case v < 0.08:
		x.Conclusion = "timed_out"
	case !final && v < 0.4, final && v < 0.2:
		x.Conclusion = "failure"
	default:
		x.Conclusion = "success"
	}
	g.out.runs = append(g.out.runs, x)
	if x.Conclusion == "failure" && g.chance(0.3) {
		g.runNo++
		again := x
		again.Attempt = 2
		again.ID = record.CIRunID(record.GitHub, g.repo, int64(g.idx)*1_000_000+g.runNo, 2)
		again.CreatedAt = x.UpdatedAt.Add(g.d(time.Minute, 2*time.Hour))
		again.StartedAt = again.CreatedAt
		if g.ciWait {
			again.StartedAt = again.CreatedAt.Add(g.d(time.Second, 5*time.Minute))
		}
		again.UpdatedAt = again.StartedAt.Add(g.d(30*time.Second, 20*time.Minute))
		again.Conclusion = "success"
		g.out.runs = append(g.out.runs, again)
	}
}

func (g *gen) deploy(via, env string, prod bool, ref, sha string, at time.Time, outcome string) {
	g.deployNo++
	name := strconv.Itoa(g.deployNo)
	if via == "release" {
		name = ref
	}
	d := record.Deploy{Source: record.GitHub, ID: record.DeployID(record.GitHub, g.repo, via, name), Repo: g.repo, Via: via,
		Environment: env, Production: prod, Ref: ref, SHA: sha, CreatedAt: at, Outcome: outcome}
	if outcome == record.DeploySuccess || outcome == record.DeployFailure {
		d.FinishedAt = tp(at.Add(g.d(20*time.Second, 15*time.Minute)))
	}
	g.out.deploys = append(g.out.deploys, d)
}

// preview makes a deployment of a change's own commit before it merged.
func (g *gen) preview(ch *record.Change) {
	if !g.deployMode || len(ch.Commits) == 0 {
		return
	}
	cm := ch.Commits[len(ch.Commits)-1]
	at := cm.CommittedAt.Add(g.d(time.Minute, 30*time.Minute))
	if ch.State == record.ChangeMerged && !at.Before(*ch.MergedAt) {
		return
	}
	switch {
	case g.chance(0.25):
		g.deploy("deployment", "preview", false, ch.HeadRef, cm.SHA, at, record.DeploySuccess)
	case g.chance(0.03):
		g.deploy("deployment", "github-pages", false, ch.HeadRef, cm.SHA, at, record.DeploySuccess)
	}
}

func (g *gen) outcome() string {
	switch v := g.r.Float64(); {
	case v < 0.84:
		return record.DeploySuccess
	case v < 0.91:
		return record.DeployFailure
	case v < 0.95:
		return record.DeployRunning
	}
	return record.DeployUnknown
}

// shipping makes the production deploys that carry merged changes: by
// deployment record, or by release when the repository has no deployments.
func (g *gen) shipping() {
	if g.deployMode {
		for _, m := range g.merged {
			if m.base != "main" || !g.chance(0.45) {
				continue
			}
			at := m.at.Add(g.d(2*time.Minute, 5*time.Hour))
			if g.chance(0.05) {
				at = m.at // a deploy at the very instant of the merge
			}
			g.deploy("deployment", "production", true, "main", m.sha, at, g.outcome())
			if g.chance(0.3) {
				g.deploy("deployment", "staging", false, "main", m.sha, m.at.Add(g.d(10*time.Second, time.Hour)), record.DeploySuccess)
			}
			if g.chance(0.03) && len(g.merged) > 3 { // a rollback to an older commit
				old := g.merged[g.r.IntN(len(g.merged)/2+1)]
				g.deploy("deployment", "production", true, "main", old.sha, at.Add(g.d(time.Hour, 6*time.Hour)), record.DeploySuccess)
			}
		}
		return
	}
	week := map[int]mergedChange{}
	var weeks []int
	for _, m := range g.merged {
		w := int(m.at.Sub(g.cfg.Start) / (7 * 24 * time.Hour))
		if m.base != "main" {
			continue
		}
		if _, ok := week[w]; !ok {
			weeks = append(weeks, w)
		}
		if prev, ok := week[w]; !ok || m.at.After(prev.at) {
			week[w] = m
		}
	}
	sort.Ints(weeks)
	for k, w := range weeks {
		m := week[w]
		out := record.DeploySuccess
		if g.chance(0.1) {
			out = record.DeployUnknown
		}
		tag := fmt.Sprintf("v1.%d.0", k+1)
		g.deploy("release", "", true, tag, m.sha, m.at.Add(g.d(10*time.Minute, 6*time.Hour)), out)
	}
}

// noise adds runs and deploys that belong to no change: pushes to the base
// branch and deploys of commits the repository does not list.
func (g *gen) noise() {
	span := g.end.Sub(g.cfg.Start)
	for k := g.r.IntN(5); k > 0; k-- {
		at := g.cfg.Start.Add(time.Duration(g.r.Int64N(int64(span))))
		g.run(&record.Change{State: record.ChangeMerged}, g.sha(), "CI", "push", "main", at, true, false)
	}
	if g.deployMode {
		for at := g.cfg.Start.Add(g.d(24*time.Hour, 5*24*time.Hour)); at.Before(g.end); at = at.Add(g.d(3*24*time.Hour, 9*24*time.Hour)) {
			g.deploy("deployment", "production", true, "main", g.sha(), at, g.outcome())
		}
	}
}
