package measure

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// item is a ticket or a change a report can point at.
type item struct {
	kind, key, url, title string
	class                 int8
}

// repoInfo is what the report says about one repository.
type repoInfo struct {
	name             string
	private          bool
	from, synced     time.Time
	changes, merged  int
	cut              int // changes with more commits than were read
	ciRuns, ciJoined int
	// ciWait says the runs tell when a runner picked them up.
	ciWait      bool
	prodDeploys int
	viaRelease  bool
	// ships counts the production deploys that can have shipped a change, and
	// shipsByCommit the ones that are of the merge commit of a stored change.
	ships, shipsByCommit int
	verifyJoined         int
	environments         []string
	notes                []string
	unfinished           bool
}

// Line holds every observation of a set of repositories and ticket projects.
// It is built once and can be reported over any window.
type Line struct {
	obs    map[string][]obs
	stays  map[string][]stay
	seen   map[string][]int64 // record kind -> creation times, unix seconds
	items  []item
	names  []string // repositories and ticket projects, by key.repo
	agents []string
	repos  []repoInfo
	// sources holds where the records of each kind came from.
	sources map[string]map[string]bool
	// flows names the CI workflows, and flowRuns holds every finished run of
	// one on a change, oldest first.
	flows    []flow
	flowRuns []flowRun
	// tickets holds the ticket systems that are not repositories.
	tickets []ticketSource

	obsPart  map[string]*parts[obs]
	stayPart map[string]*parts[stay]
}

// ticketSource is what the report says about a ticket system that is not a
// repository, such as a Linear workspace.
type ticketSource struct {
	source string
	name   string
	keys   []string
	// tickets counts the tickets of the system, and linked those a change
	// of the report is linked to.
	tickets, linked int
	synced          time.Time
	notes           []string
	unfinished      bool
	// linkedOnly says only the tickets a change links to were counted.
	linkedOnly bool
	// skipped counts the tickets left out for that reason.
	skipped int
}

// firstLink is the first change linked to a ticket: when it was opened, and
// in which repository.
type firstLink struct {
	at   time.Time
	repo int32
}

// flow is a CI workflow of a repository.
type flow struct {
	repo int32 // Line.names
	name string
}

// flowRun is one finished run of a workflow on a change.
type flowRun struct {
	at     time.Time // when it finished
	flow   int32     // Line.flows
	failed bool
}

// saw counts a record for the coverage of a report.
func (l *Line) saw(kind, source string, at time.Time) {
	l.seen[kind] = append(l.seen[kind], at.Unix())
	if l.sources[kind] == nil {
		l.sources[kind] = map[string]bool{}
	}
	l.sources[kind][source] = true
}

func (l *Line) add(stream string, at time.Time, v float64, k key, it int32) {
	l.obs[stream] = append(l.obs[stream], obs{at: at, v: v, k: k, item: it})
}

func (l *Line) hit(stream string, at time.Time, yes bool, k key, it int32) {
	v := 0.0
	if yes {
		v = 1
	}
	l.add(stream, at, v, k, it)
}

// deployItem adds a production deploy to the items, so a number drawn from
// deploys can name them.
func (l *Line) deployItem(d *record.Deploy) int32 {
	it := item{kind: record.KindDeploy, class: classNone}
	short := d.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	switch {
	case d.Via == "release":
		it.key, it.title = d.Repo+" "+d.Ref, "Release "+d.Ref
		if d.Ref != "" {
			it.url = "https://github.com/" + d.Repo + "/releases/tag/" + url.PathEscape(d.Ref)
		}
	default:
		it.key, it.title = d.Repo+" "+short, "Deploy to "+d.Environment
		if d.SHA != "" {
			it.url = "https://github.com/" + d.Repo + "/commit/" + d.SHA
		}
	}
	l.items = append(l.items, it)
	return int32(len(l.items) - 1)
}

// stand records an item standing in a queue from one moment to another. A
// zero to means it still stands. A stay that ends before it starts is dropped.
func (l *Line) stand(queue string, from, to time.Time, k key, it int32) {
	if from.IsZero() || (!to.IsZero() && !to.After(from)) {
		return
	}
	l.stays[queue] = append(l.stays[queue], stay{from: from, to: to, k: k, item: it})
}

// Builder turns records into a Line. Repositories first, one at a time, then
// tickets. It keeps one repository in memory at a time, so the size of the
// largest repository bounds what it needs, not the size of the organization.
type Builder struct {
	reg      *signature.Registry
	l        *Line
	nameIdx  map[string]int32
	agentIdx map[string]int16
	// linked is the first change linked to each ticket.
	linked  map[string]firstLink
	flowIdx map[flow]int32
}

// NewBuilder starts an empty line. reg tells agents from people.
func NewBuilder(reg *signature.Registry) *Builder {
	return &Builder{
		reg: reg,
		l: &Line{obs: map[string][]obs{}, stays: map[string][]stay{}, seen: map[string][]int64{},
			sources: map[string]map[string]bool{}},
		nameIdx: map[string]int32{}, agentIdx: map[string]int16{}, linked: map[string]firstLink{},
		flowIdx: map[flow]int32{},
	}
}

func (b *Builder) flow(repo int32, name string) int32 {
	f := flow{repo: repo, name: name}
	if i, ok := b.flowIdx[f]; ok {
		return i
	}
	i := int32(len(b.l.flows))
	b.flowIdx[f] = i
	b.l.flows = append(b.l.flows, f)
	return i
}

func (b *Builder) name(n string) int32 {
	k := strings.ToLower(n)
	if i, ok := b.nameIdx[k]; ok {
		return i
	}
	i := int32(len(b.l.names))
	b.l.names = append(b.l.names, n)
	b.nameIdx[k] = i
	return i
}

func (b *Builder) agent(id string) int16 {
	if i, ok := b.agentIdx[id]; ok {
		return i
	}
	i := int16(len(b.l.agents))
	b.l.agents = append(b.l.agents, id)
	b.agentIdx[id] = i
	return i
}

// Line finishes the build.
func (b *Builder) Line() *Line {
	l := b.l
	for _, list := range l.obs {
		sort.SliceStable(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	}
	for _, list := range l.seen {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	}
	sort.SliceStable(l.flowRuns, func(i, j int) bool { return l.flowRuns[i].at.Before(l.flowRuns[j].at) })
	l.obsPart, l.stayPart = map[string]*parts[obs]{}, map[string]*parts[stay]{}
	return l
}

// RepoScope is what the store knows about a repository.
type RepoScope struct {
	Name    string
	Private bool
	// From is the time the store holds the repository's changes from, and
	// SyncedAt when its last sync finished. A zero SyncedAt means the first
	// sync has not finished.
	From, SyncedAt time.Time
	Notes          []string
}

type commitLite struct {
	committed time.Time
	merge     bool
}

const (
	revCommented int8 = iota
	revApproved
	revChanges
)

type revLite struct {
	at    time.Time
	state int8
}

type runLite struct {
	created, started, updated time.Time
	attempt                   int
	failed                    bool
}

// round is every run on one commit of a change.
type round struct {
	runs []runLite
	// open is set while a run on the commit has not finished.
	open bool
}

// vdep is a deployment of a commit of a change to an environment that is not
// production.
type vdep struct {
	committed, created, finished time.Time
	outcome                      string
}

// chg is a change, cut down to what the stations read.
type chg struct {
	item    int32
	k       key
	created time.Time
	// ready, merged and closed are zero when they did not happen.
	ready, merged, closed time.Time
	// start is the first commit or the opening, whichever is earlier.
	start    time.Time
	size     int
	base     string
	commits  []commitLite
	reviews  []revLite // by another person, oldest first
	reverted bool
	rounds   map[string]*round
	vdeps    []vdep
}

type shaRef struct{ chg, commit int32 }

type prodDeploy struct {
	created, finished time.Time
	outcome, via, sha string
	// item is the deploy in Line.items.
	item int32
	// cut and base are set on a deploy of the commit a merge left: when that
	// change merged, and into which branch. Such a deploy holds the changes
	// merged into the branch up to then.
	cut  time.Time
	base string
}

// RepoBuilder takes the records of one repository: every change first, then
// CI runs, deploys and links in any order, then Done.
type RepoBuilder struct {
	b        *Builder
	sc       RepoScope
	idx      int32
	info     repoInfo
	chgs     []chg
	sha      map[string]shaRef
	byNumber map[int]int32
	// merges finds a merged change by the commit its merge left.
	merges  map[string]int32
	reverts []int
	prod    []prodDeploy
	envs    map[string]bool
	// firstRuns counts the first attempts of CI runs, and waitedRuns the ones
	// that started later than they were created.
	firstRuns, waitedRuns int
}

// Repo starts a repository.
func (b *Builder) Repo(sc RepoScope) *RepoBuilder {
	return &RepoBuilder{
		b: b, sc: sc, idx: b.name(sc.Name),
		info: repoInfo{name: sc.Name, private: sc.Private, from: sc.From, synced: sc.SyncedAt, notes: sc.Notes,
			unfinished: sc.SyncedAt.IsZero()},
		sha: map[string]shaRef{}, byNumber: map[int]int32{}, merges: map[string]int32{}, envs: map[string]bool{},
	}
}

// toPR gives the classifier the fields it reads, in the shape it reads them.
func toPR(c *record.Change) model.PR {
	pr := model.PR{Repo: c.Repo, Number: c.Number, Author: model.Actor{Login: c.Author.Login, Type: "User"},
		HeadRef: c.HeadRef, Body: c.Body}
	if c.Author.Kind == record.ActorBot {
		pr.Author.Type = "Bot"
	}
	pr.Commits = make([]model.Commit, len(c.Commits))
	for i, k := range c.Commits {
		m := model.Commit{SHA: k.SHA}
		for _, a := range k.Authors {
			m.Authors = append(m.Authors, model.CommitAuthor{Login: a.Login, Email: a.Email, Name: a.Name})
		}
		for _, t := range k.Trailers {
			m.Trailers = append(m.Trailers, t.Key+": "+t.Value)
		}
		pr.Commits[i] = m
	}
	return pr
}

// Classify says who wrote a change: the class and, for an agent, which one.
func Classify(reg *signature.Registry, c *record.Change) (class string, agent string) {
	res := reg.Classify(toPR(c))
	if res.Class == signature.ClassAgent && len(res.Agents) > 0 {
		agent = res.Agents[0]
	}
	return string(res.Class), agent
}

// reverts reads the line the code host writes into a change that reverts
// another: "Reverts owner/repo#12".
var reverts = regexp.MustCompile(`(?mi)^\s*Reverts\s+(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+))?#(\d+)\b`)

// another reports whether a review was written by a person other than the
// author.
func another(r *record.Review, author string) bool {
	login := strings.ToLower(r.Author.Login)
	return r.Author.Kind != record.ActorBot && login != "" && login != "ghost" && login != strings.ToLower(author)
}

func val(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Change adds a change.
func (r *RepoBuilder) Change(c *record.Change) {
	b := r.b
	class, agent := Classify(b.reg, c)
	k := key{class: classHuman, agent: -1, repo: r.idx}
	switch class {
	case string(signature.ClassAgent):
		k.class = classAgent
		if agent != "" {
			k.agent = b.agent(agent)
		}
	case string(signature.ClassAutomation):
		k.class = classAutomation
	}
	x := chg{
		base: c.BaseRef,
		item: int32(len(b.l.items)), k: k, created: c.CreatedAt,
		ready: val(c.ReadyAt), merged: val(c.MergedAt), closed: val(c.ClosedAt),
		start: c.CreatedAt, size: c.Additions + c.Deletions,
	}
	b.l.items = append(b.l.items, item{kind: record.KindChange, key: c.Repo + "#" + strconv.Itoa(c.Number), url: c.URL, title: c.Title, class: k.class})
	if c.State != record.ChangeMerged {
		x.merged = time.Time{}
	}
	n := int32(len(r.chgs))
	x.commits = make([]commitLite, len(c.Commits))
	for i := range c.Commits {
		k := &c.Commits[i]
		x.commits[i] = commitLite{committed: k.CommittedAt, merge: k.Parents >= 2}
		// A merge commit brings in work done elsewhere, and a commit much
		// older than the change is history it carries or a wrong date: such
		// a commit would count years of writing.
		if !k.AuthoredAt.IsZero() && k.AuthoredAt.Before(x.start) && k.Parents < 2 &&
			!k.AuthoredAt.Before(c.CreatedAt.Add(-line.EarlyCommitDays*24*time.Hour)) {
			x.start = k.AuthoredAt
		}
		if _, taken := r.sha[k.SHA]; !taken && k.SHA != "" {
			r.sha[k.SHA] = shaRef{chg: n, commit: int32(i)}
		}
	}
	for i := range c.Reviews {
		v := &c.Reviews[i]
		if !another(v, c.Author.Login) {
			continue
		}
		// A review after the merge or the close says nothing about the line.
		if end := val(c.ClosedAt); !end.IsZero() && v.SubmittedAt.After(end) {
			continue
		}
		st := revCommented
		switch v.State {
		case record.ReviewApproved:
			st = revApproved
		case record.ReviewChangesRequested:
			st = revChanges
		}
		x.reviews = append(x.reviews, revLite{at: v.SubmittedAt, state: st})
	}
	sort.SliceStable(x.reviews, func(i, j int) bool { return x.reviews[i].at.Before(x.reviews[j].at) })
	if !x.merged.IsZero() {
		for _, m := range reverts.FindAllStringSubmatch(c.Body, -1) {
			if m[1] != "" && !strings.EqualFold(m[1], c.Repo) {
				continue
			}
			if num, err := strconv.Atoi(m[2]); err == nil && num != c.Number {
				r.reverts = append(r.reverts, num)
			}
		}
		r.info.merged++
	}
	if c.CommitCount > len(c.Commits) {
		r.info.cut++
	}
	if c.MergeSHA != "" && !x.merged.IsZero() {
		r.merges[c.MergeSHA] = n
	}
	r.info.changes++
	r.byNumber[c.Number] = n
	r.chgs = append(r.chgs, x)
	b.l.saw(record.KindChange, c.Source, c.CreatedAt)
}

// skipped holds the conclusions of runs that never ran the checks.
var skipped = map[string]bool{"skipped": true, "cancelled": true, "stale": true, "action_required": true, "neutral": true, "": true}

// queueBranch is the prefix of the branches a GitHub merge queue makes:
// gh-readonly-queue/<base>/pr-<number>-<sha>.
const queueBranch = "gh-readonly-queue/"

// queuedFor gives the number of the pull request a merge queue branch was made
// for. The base branch can hold a slash, so the name is read from its end.
func queuedFor(branch string) (int, bool) {
	rest, ok := strings.CutPrefix(branch, queueBranch)
	if !ok {
		return 0, false
	}
	i := strings.LastIndex(rest, "/pr-")
	if i < 0 {
		return 0, false
	}
	num, _, ok := strings.Cut(rest[i+len("/pr-"):], "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil && n > 0
}

// CIRun adds a CI run. A run on a commit of no change is counted and left out.
// A run of a merge queue is on a commit the queue made, so it is tied to its
// change by the name of its branch, and it is a round of its own.
func (r *RepoBuilder) CIRun(x *record.CIRun) {
	r.info.ciRuns++
	r.b.l.saw(record.KindCIRun, x.Source, x.CreatedAt)
	var at int32
	if ref, ok := r.sha[x.SHA]; ok {
		at = ref.chg
	} else {
		num, ok := queuedFor(x.Branch)
		if !ok || x.Event != "merge_group" {
			return
		}
		if at, ok = r.byNumber[num]; !ok {
			return
		}
	}
	r.info.ciJoined++
	c := &r.chgs[at]
	if c.rounds == nil {
		c.rounds = map[string]*round{}
	}
	rd := c.rounds[x.SHA]
	if rd == nil {
		rd = &round{}
		c.rounds[x.SHA] = rd
	}
	if x.Status != "completed" {
		rd.open = true
		return
	}
	if skipped[x.Conclusion] {
		return
	}
	attempt := max(x.Attempt, 1)
	if attempt == 1 {
		r.firstRuns++
		if x.StartedAt.After(x.CreatedAt) {
			r.waitedRuns++
		}
	}
	failed := x.Conclusion == "failure" || x.Conclusion == "timed_out" || attempt > 1
	rd.runs = append(rd.runs, runLite{created: x.CreatedAt, started: x.StartedAt, updated: x.UpdatedAt, attempt: attempt, failed: failed})
	if x.Workflow != "" {
		r.b.l.flowRuns = append(r.b.l.flowRuns, flowRun{at: x.UpdatedAt, flow: r.b.flow(r.idx, x.Workflow), failed: failed})
	}
}

// Deploy adds a deployment or a release.
func (r *RepoBuilder) Deploy(d *record.Deploy) {
	r.b.l.saw(record.KindDeploy, d.Source, d.CreatedAt)
	if d.Production {
		r.prod = append(r.prod, prodDeploy{created: d.CreatedAt, finished: val(d.FinishedAt), outcome: d.Outcome, via: d.Via, sha: d.SHA,
			item: r.b.l.deployItem(d)})
		if d.Via == "deployment" && d.Environment != "" {
			r.envs[d.Environment] = true
		}
		return
	}
	ref, ok := r.sha[d.SHA]
	if !ok || d.Via != "deployment" {
		return
	}
	c := &r.chgs[ref.chg]
	// A proof before the merge is a deployment of the change's own branch.
	// Three things look like one and are not: a deployment made after the
	// commit landed, a deployment of the branch the change merges into, and
	// a site published by GitHub Pages.
	if (!c.merged.IsZero() && !d.CreatedAt.Before(c.merged)) || (d.Ref != "" && d.Ref == c.base) || strings.EqualFold(d.Environment, "github-pages") {
		return
	}
	r.info.verifyJoined++
	c.vdeps = append(c.vdeps, vdep{committed: c.commits[ref.commit].committed, created: d.CreatedAt, finished: val(d.FinishedAt), outcome: d.Outcome})
}

// Link adds a link from a change of this repository to a ticket.
func (r *RepoBuilder) Link(l *record.Link) {
	if l.ToKind != record.KindTicket || l.Kind == record.LinkReverts {
		return
	}
	_, rest, _ := strings.Cut(l.From, "#")
	num, err := strconv.Atoi(rest)
	if err != nil {
		return
	}
	r.linkTo(num, l.To)
}

// linkTo ties a change of this repository, by its number, to a ticket.
func (r *RepoBuilder) linkTo(num int, ticket string) {
	i, ok := r.byNumber[num]
	if !ok {
		return
	}
	at := r.chgs[i].created
	if old, ok := r.b.linked[ticket]; !ok || at.Before(old.at) {
		r.b.linked[ticket] = firstLink{at: at, repo: r.idx}
	}
}

// Done emits the observations of the repository and lets go of its changes.
func (r *RepoBuilder) Done() {
	l := r.b.l
	// Some sources stamp a run as started the moment it is created. Then the
	// wait for a runner cannot be seen, and it is left out instead of
	// reported as zero.
	r.info.ciWait = r.firstRuns > 0 && r.waitedRuns*20 >= r.firstRuns
	for _, num := range r.reverts {
		if i, ok := r.byNumber[num]; ok {
			r.chgs[i].reverted = true
		}
	}

	// The production deploys of a repository are its deployments to a
	// production environment. Only a repository without any falls back to
	// its published releases.
	deployments := false
	for _, d := range r.prod {
		if d.via == "deployment" {
			deployments = true
			break
		}
	}
	var prod []prodDeploy
	for _, d := range r.prod {
		if deployments == (d.via == "deployment") {
			prod = append(prod, d)
		}
	}
	sort.SliceStable(prod, func(i, j int) bool { return prod[i].created.Before(prod[j].created) })
	r.info.prodDeploys, r.info.viaRelease = len(prod), len(prod) > 0 && !deployments
	for e := range r.envs {
		r.info.environments = append(r.info.environments, e)
	}
	sort.Strings(r.info.environments)

	repoKey := key{class: classNone, agent: -1, repo: r.idx}
	// The deploys that can have shipped a change. A deploy of the commit a
	// merge left is known to hold that change and what merged into the same
	// branch before it. Of any other deploy only its time is known.
	sh := shipping{bases: map[string]bool{}}
	for _, d := range prod {
		l.add("release.deploys", d.created, 1, repoKey, d.item)
		if d.outcome == record.DeploySuccess || d.outcome == record.DeployUnknown {
			if i, ok := r.merges[d.sha]; ok && d.sha != "" {
				d.cut, d.base = r.chgs[i].merged, r.chgs[i].base
				sh.bases[d.base] = true
				r.info.shipsByCommit++
			}
			sh.list = append(sh.list, d)
		}
		if d.via != "deployment" || d.finished.IsZero() {
			continue
		}
		switch d.outcome {
		case record.DeploySuccess:
			l.add("release.work", d.finished, hours(d.finished.Sub(d.created)), repoKey, d.item)
			l.hit("release.rework", d.finished, false, repoKey, d.item)
		case record.DeployFailure:
			l.hit("release.rework", d.finished, true, repoKey, d.item)
		}
	}

	r.info.ships = len(sh.list)
	for i := range r.chgs {
		r.emit(&r.chgs[i], sh)
	}
	l.repos = append(l.repos, r.info)
	r.chgs, r.sha, r.byNumber, r.merges = nil, nil, nil, nil
}

// shipping is the production deploys that can have shipped a change, oldest
// first, and the branches that deploys matched by commit were made from.
type shipping struct {
	list  []prodDeploy
	bases map[string]bool
}

// of finds the deploy that shipped a change merged at a given time into a
// given branch. A deploy that started before the merge cannot hold it. A
// deploy of the commit a merge left holds the changes merged into the same
// branch up to that merge, so a deploy of an older commit, a rollback, is
// passed over. A change merged into a branch that no such deploy was made
// from, a branch that is merged on later, is taken to ship with the first
// deploy whose commit merged after it.
func (s shipping) of(merged time.Time, base string) (prodDeploy, bool) {
	i := sort.Search(len(s.list), func(i int) bool { return !s.list[i].created.Before(merged) })
	for ; i < len(s.list); i++ {
		d := s.list[i]
		if d.cut.IsZero() || (!merged.After(d.cut) && (base == d.base || !s.bases[base])) {
			return d, true
		}
	}
	return prodDeploy{}, false
}

// unseenAtRelease is the stream of changes that merged before the first
// production deploy in the store. No metric reads it. The report counts it
// to say what was left out.
const unseenAtRelease = "release.unseen"

// emit writes down what one change did at every station.
func (r *RepoBuilder) emit(c *chg, sh shipping) {
	ships := sh.list
	l, k := r.b.l, c.k

	// CI first: the other stations take the time a run was in flight out of
	// their own.
	var busy spans
	for _, rd := range c.rounds {
		if len(rd.runs) == 0 {
			continue
		}
		var mine, flight spans
		var finish time.Time
		var first *runLite
		failed := false
		for i := range rd.runs {
			x := &rd.runs[i]
			from := x.started
			if x.attempt == 1 {
				from = x.created
				if first == nil || x.created.Before(first.created) {
					first = x
				}
			}
			mine = append(mine, span{from, x.updated})
			flight = append(flight, span{x.started, x.updated})
			if x.updated.After(finish) {
				finish = x.updated
			}
			failed = failed || x.failed
		}
		busy = append(busy, mine...)
		if rd.open {
			continue
		}
		l.add("ci.flow", finish, 1, k, c.item)
		if first != nil && r.info.ciWait {
			l.add("ci.wait", finish, hours(first.started.Sub(first.created)), k, c.item)
		}
		l.add("ci.work", finish, flight.merge().total(), k, c.item)
		l.hit("ci.rework", finish, failed, k, c.item)
		l.add("ci.held", finish, mine.merge().total(), k, c.item)
	}
	busy = busy.merge()
	// stood is the hours between two moments with no CI run in flight.
	stood := func(from, to time.Time) float64 {
		if !to.After(from) {
			return 0
		}
		return to.Sub(from).Hours() - busy.within(from, to)
	}

	// Verify.
	sort.SliceStable(c.vdeps, func(i, j int) bool { return c.vdeps[i].created.Before(c.vdeps[j].created) })
	for i, d := range c.vdeps {
		l.add("verify.wait", d.created, hours(d.created.Sub(d.committed)), k, c.item)
		if d.finished.IsZero() {
			continue
		}
		switch d.outcome {
		case record.DeploySuccess:
			l.add("verify.work", d.finished, hours(d.finished.Sub(d.created)), k, c.item)
			l.add("verify.held", d.finished, hours(d.finished.Sub(d.committed)), k, c.item)
			l.hit("verify.rework", d.finished, false, k, c.item)
			if i == 0 {
				l.add("verify.flow", d.finished, 1, k, c.item)
			}
		case record.DeployFailure:
			l.hit("verify.rework", d.finished, true, k, c.item)
		}
	}

	// Code.
	if !c.ready.IsZero() {
		l.add("code.flow", c.ready, 1, k, c.item)
		l.add("code.work", c.ready, hours(c.ready.Sub(c.start)), k, c.item)
		l.add("code.held", c.ready, stood(c.start, c.ready), k, c.item)
	}
	if !c.closed.IsZero() {
		l.hit("code.rework", c.closed, c.merged.IsZero(), k, c.item)
	}
	draftEnd := c.ready
	if draftEnd.IsZero() {
		draftEnd = c.closed
	}
	l.stand("code.queue", c.created, draftEnd, k, c.item)

	// Review.
	end := c.closed // zero while the change is open
	reviewed := len(c.reviews) > 0
	var first, leave time.Time
	if reviewed {
		first = c.reviews[0].at
		wait := 0.0
		if !c.ready.IsZero() {
			wait = hours(first.Sub(c.ready))
		}
		l.add("review.wait", first, wait, k, c.item)
	}
	if !c.ready.IsZero() {
		until := end
		if reviewed {
			until = first
		}
		l.stand("review.queue", c.ready, until, k, c.item)
	}
	arrive := c.ready
	if arrive.IsZero() {
		arrive = c.created
	}
	if !c.merged.IsZero() && reviewed {
		leave = c.merged
		sentBack := false
		for _, v := range c.reviews {
			if v.state == revApproved {
				leave = v.at
			}
			sentBack = sentBack || v.state == revChanges
		}
		for _, m := range c.commits {
			sentBack = sentBack || (!m.merge && m.committed.After(first))
		}
		l.add("review.flow", leave, 1, k, c.item)
		l.add("review.work", leave, hours(leave.Sub(first)), k, c.item)
		l.hit("review.rework", leave, sentBack, k, c.item)
		l.add("review.held", leave, stood(arrive, leave), k, c.item)
	}

	// Merge. Every approval opens a stay in the queue that lasts until the
	// next approval, a request for changes or the end of the change.
	for i, v := range c.reviews {
		if v.state != revApproved {
			continue
		}
		until := end
		for _, w := range c.reviews[i+1:] {
			if w.state != revCommented {
				until = w.at
				break
			}
		}
		l.stand("merge.queue", v.at, until, k, c.item)
	}
	if !c.merged.IsZero() {
		at := arrive
		if reviewed {
			at = leave
		}
		if at.After(c.merged) {
			at = c.merged
		}
		l.add("merge.flow", c.merged, 1, k, c.item)
		l.add("merge.wait", c.merged, hours(c.merged.Sub(at)), k, c.item)
		l.hit("merge.rework", c.merged, c.reverted, k, c.item)
		l.add("merge.held", c.merged, stood(at, c.merged), k, c.item)
		l.hit("review.skipped", c.merged, !reviewed, k, c.item)

		l.add("line.flow", c.merged, 1, k, c.item)
		l.add("line.lead", c.merged, hours(c.merged.Sub(c.created)), k, c.item)
		l.add("line.size", c.merged, float64(c.size), k, c.item)
		l.hit("line.agent_share", c.merged, k.class == classAgent, k, c.item)
	}

	// Release. A change merged before the store's deploys begin cannot be
	// matched to the deploy that shipped it.
	if c.merged.IsZero() || len(ships) == 0 || c.merged.Before(r.sc.From) {
		return
	}
	// The station is measured from its first record. A change merged before
	// the first production deploy in the store is not counted: the store
	// cannot tell a line that did not release from one whose releases were
	// not recorded yet.
	if c.merged.Before(ships[0].created) {
		l.add(unseenAtRelease, c.merged, 1, k, c.item)
		return
	}
	d, ok := sh.of(c.merged, c.base)
	if !ok {
		l.stand("release.queue", c.merged, time.Time{}, k, c.item)
		return
	}
	reach := d.finished
	if reach.Before(d.created) {
		reach = d.created
	}
	l.add("release.flow", reach, 1, k, c.item)
	l.add("release.wait", reach, hours(d.created.Sub(c.merged)), k, c.item)
	l.add("release.held", reach, hours(reach.Sub(c.merged)), k, c.item)
	l.add("line.delivery", reach, hours(reach.Sub(c.start)), k, c.item)
	l.stand("release.queue", c.merged, reach, k, c.item)
}

// SourceScope is what the store knows about a ticket system that is not a
// repository.
type SourceScope struct {
	// Source is the system, such as "linear", and Name the workspace or site.
	Source string
	Name   string
	// Keys are the key prefixes of its tickets, such as ENG.
	Keys     []string
	SyncedAt time.Time
	Notes    []string
	// LinkedOnly says the report is of one repository, so only the tickets
	// that a change of it links to are counted.
	LinkedOnly bool
}

// SpecStart is when the spec station takes a ticket to start: the first time
// it moved to a started state, or when the first pull request tied to it was
// opened, whichever is earlier. firstLink is zero when no pull request is
// tied to it. A pull request opened after the ticket was closed did not start
// it: it only names it, as in "a duplicate of ENG-12". A ticket written up
// after the work began did not wait, so it starts when it was created. Zero
// is a ticket that never started.
func SpecStart(t *record.Ticket, firstLink time.Time) time.Time {
	start := val(t.StartedAt)
	closed := val(t.ClosedAt)
	if !firstLink.IsZero() && (start.IsZero() || firstLink.Before(start)) && (closed.IsZero() || !firstLink.After(closed)) {
		start = firstLink
	}
	if !start.IsZero() && start.Before(t.CreatedAt) {
		start = t.CreatedAt
	}
	return start
}

// TicketSource starts a ticket system that is not a repository. Call it after
// every repository, then pass its tickets to the Ticket it returns.
func (b *Builder) TicketSource(sc SourceScope) func(*record.Ticket) {
	l := b.l
	i := len(l.tickets)
	l.tickets = append(l.tickets, ticketSource{source: sc.Source, name: sc.Name, keys: sc.Keys, synced: sc.SyncedAt, notes: sc.Notes,
		unfinished: sc.SyncedAt.IsZero(), linkedOnly: sc.LinkedOnly})
	return func(t *record.Ticket) {
		ts := &l.tickets[i]
		ts.tickets++
		_, linked := b.linked[t.ID]
		if linked {
			ts.linked++
		}
		if !linked && sc.LinkedOnly {
			ts.skipped++
			return
		}
		b.Ticket(t)
	}
}

// Ticket adds a ticket. Call it after every repository, so the links from
// changes are known.
//
// An issue of a repository belongs to that repository. A ticket of another
// system belongs to the repository of the first change linked to it, and to
// none while no change is.
func (b *Builder) Ticket(t *record.Ticket) {
	l := b.l
	l.saw(record.KindTicket, t.Source, t.CreatedAt)
	first, linked := b.linked[t.ID]
	k := key{class: classNone, agent: -1, repo: -1}
	switch {
	case t.Source == record.GitHub:
		k.repo = b.name(t.Project)
	case linked:
		k.repo = first.repo
	}
	it := int32(len(l.items))
	l.items = append(l.items, item{kind: record.KindTicket, key: t.Key, url: t.URL, title: t.Title, class: classNone})

	var firstAt time.Time
	if linked {
		firstAt = first.at
	}
	start := SpecStart(t, firstAt)
	if !start.IsZero() {
		l.add("spec.flow", start, 1, k, it)
		l.add("spec.wait", start, hours(start.Sub(t.CreatedAt)), k, it)
	}
	if done := val(t.DoneAt); !done.IsZero() {
		l.hit("spec.rework", done, t.Reopened > 0, k, it)
	}
	until := start
	if until.IsZero() {
		// A ticket nobody started and nobody will, such as a duplicate, was
		// never work waiting for the line.
		if t.State == record.TicketCancelled {
			return
		}
		until = val(t.ClosedAt)
	}
	l.stand("spec.queue", t.CreatedAt, until, k, it)
}
