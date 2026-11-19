// Package ingest fills a store from a source and keeps it up to date.
//
// The first sync of a repository reads its history back to a floor, 26 weeks
// by default. Every later sync reads only what changed since the one before.
// A sync that is stopped half way keeps what it stored and goes on from there
// the next time.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marshal-org/factorymeter/pkg/collector"
	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// DefaultHistory is how far back a first sync reads.
const DefaultHistory = 26 * 7 * 24 * time.Hour

const (
	// A CI run reports its result some time after it was created, and a
	// deployment gets its statuses later still. Each sync reads this much
	// again so a record stored while it was running gets its ending.
	ciOverlap     = 3 * 24 * time.Hour
	deployOverlap = 7 * 24 * time.Hour
)

// Options tune a sync.
type Options struct {
	// Since is how far back to read. The zero value means DefaultHistory
	// before Now. A repository keeps the floor of its first sync: a later
	// Since only counts when it is earlier, and then the sync reads the part
	// that is missing.
	Since time.Time
	// Now is the time of the sync. The zero value means the current time.
	Now time.Time
	// Workers is how many repositories of an owner are read at once.
	Workers int
	// PageSize is the number of pull requests per page. It is halved when
	// GitHub times out on a page.
	PageSize int
	// Checkpoint is the number of pages between two saves during a first sync.
	Checkpoint int
	// MaxRuns caps the workflow runs read per repository in one sync. The
	// next sync goes on below the cap.
	MaxRuns int
	// SkipCI and SkipDeploys leave those records out.
	SkipCI      bool
	SkipDeploys bool
	// IncludeForks and IncludeArchived widen an owner sync.
	IncludeForks    bool
	IncludeArchived bool
	Logf            func(format string, a ...any)
}

func (o Options) withDefaults() Options {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	o.Now = o.Now.UTC().Truncate(time.Second)
	if o.Since.IsZero() {
		o.Since = o.Now.Add(-DefaultHistory).Truncate(24 * time.Hour)
	}
	o.Since = o.Since.UTC()
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.PageSize <= 0 {
		o.PageSize = 25
	}
	if o.Checkpoint <= 0 {
		o.Checkpoint = 8
	}
	if o.MaxRuns <= 0 {
		o.MaxRuns = 100000
	}
	return o
}

// Count says what a sync did to one kind of record.
type Count struct {
	// Added is the number of records the store did not have.
	Added int
	// Changed is the number of stored records that got new content.
	Changed int
	// Total is the number of records the store holds after the sync.
	Total int64
}

// RepoResult is the outcome of one repository.
type RepoResult struct {
	Repo    string
	Changes Count
	CIRuns  Count
	Deploys Count
	// From is the time the store holds the repository's changes from.
	From time.Time
	// First is set when this was the repository's first finished sync.
	First bool
	// Notes are limits the sync ran into. The records it could read are stored.
	Notes []string
	// Err is set when the repository could not be synced. What was read
	// before the error is stored, and the next sync goes on from there.
	Err error
}

// Result is the outcome of a sync.
type Result struct {
	Repos []RepoResult
}

// GitHub syncs repositories from GitHub into a store.
type GitHub struct {
	C   *github.Client
	S   *store.Store
	Opt Options
}

// state is what a repository's next sync needs to read only what changed. It
// is saved in the scope's State.
type state struct {
	// Target is the floor the repository is read back to.
	Target  time.Time `json:"target"`
	Changes struct {
		// High is the update time of the newest change the store has seen.
		// The next sync reads from the top down to it.
		High time.Time `json:"high"`
		// Floor is the time the scan from the top has reached. Zero while the
		// first sync has not finished.
		Floor time.Time `json:"floor"`
		// Cursor is where the scan toward Target stands.
		Cursor string `json:"cursor"`
		// Open is set once the open changes last touched before Floor are stored.
		Open bool `json:"open"`
		// Cut is set once a change had more commits than one read returns.
		Cut bool `json:"cut"`
	} `json:"changes"`
	CI struct {
		// Mode is which runs carry the repository's CI: "pull_request" when
		// runs of pull request events do, "all" when only runs of other
		// events land on change commits. Empty while that is unknown.
		Mode string `json:"mode"`
		// From and Through are the ends of the time range whose runs are stored.
		From    time.Time `json:"from"`
		Through time.Time `json:"through"`
	} `json:"ci"`
	Deploys struct {
		From    time.Time `json:"from"`
		Through time.Time `json:"through"`
	} `json:"deploys"`
}

const (
	modePR  = "pull_request"
	modeAll = "all"
)

func (g *GitHub) logf(format string, a ...any) {
	if g.Opt.Logf != nil {
		g.Opt.Logf(format, a...)
	}
}

// Sync reads a repository ("owner/name") or every active repository of an
// owner ("owner") into the store. One repository failing does not stop the
// others: the error names how many failed and the result has each one's error.
func (g *GitHub) Sync(ctx context.Context, target string) (Result, error) {
	o := g.Opt.withDefaults()
	owner, _, _ := strings.Cut(target, "/")
	if !strings.EqualFold(owner, g.S.Owner()) {
		return Result{}, fmt.Errorf("the store holds %s, not %s", g.S.Owner(), owner)
	}
	k := &collector.Collector{C: g.C, Opt: collector.Options{
		Since: o.Since, IncludeForks: o.IncludeForks, IncludeArchived: o.IncludeArchived, Logf: o.Logf,
	}}
	repos := []string{target}
	if !strings.Contains(target, "/") {
		var err error
		if repos, err = k.Repos(ctx, target); err != nil {
			return Result{}, err
		}
		if len(repos) == 0 {
			return Result{}, fmt.Errorf("%s has no repository with a push since %s that this token can read", target, o.Since.Format("2006-01-02"))
		}
		g.logf("%s: %d repositories pushed to since %s", target, len(repos), o.Since.Format("2006-01-02"))
	} else if _, _, err := collector.SplitRepo(target); err != nil {
		return Result{}, err
	}

	res := Result{Repos: make([]RepoResult, len(repos))}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(o.Workers, len(repos)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r := &repoSync{g: g, k: k, o: o, name: repos[i]}
				res.Repos[i] = r.run(ctx)
			}
		}()
	}
	for i := range repos {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("stopped. What was read is stored, and the next sync goes on from there: %w", err)
	}
	var failed []string
	var first error
	for _, r := range res.Repos {
		if r.Err != nil {
			failed = append(failed, r.Repo)
			if first == nil {
				first = r.Err
			}
		}
	}
	switch {
	case len(failed) == 1 && len(repos) == 1:
		return res, first
	case len(failed) > 0:
		return res, fmt.Errorf("%d of %d repositories could not be synced (%s). The first error: %w", len(failed), len(repos), strings.Join(failed, ", "), first)
	}
	return res, nil
}

// repoSync is one repository's sync. It runs on one goroutine.
type repoSync struct {
	g *GitHub
	k *collector.Collector
	o Options

	// name is the repository as asked for, then as GitHub spells it.
	name string
	sc   store.Scope
	st   state
	// target is the floor this sync reads back to.
	target time.Time
	size   int
	res    RepoResult
	notes  []string
}

func (r *repoSync) logf(format string, a ...any) {
	r.g.logf("%s: "+format, append([]any{r.name}, a...)...)
}

func (r *repoSync) save() error {
	b, err := json.Marshal(r.st)
	if err != nil {
		return err
	}
	r.sc.Source, r.sc.Name, r.sc.State = record.GitHub, r.name, b
	r.sc.From = r.st.Changes.Floor
	return r.g.S.PutScope(r.sc)
}

func (r *repoSync) run(ctx context.Context) RepoResult {
	r.res.Repo = r.name
	err := r.sync(ctx)
	r.res.Repo, r.res.From, r.res.Notes, r.res.Err = r.name, r.st.Changes.Floor, r.notes, err
	for _, t := range []struct {
		name string
		c    *Count
	}{{store.Changes.Name, &r.res.Changes}, {store.CIRuns.Name, &r.res.CIRuns}, {store.Deploys.Name, &r.res.Deploys}} {
		if n, cerr := r.g.S.Count(t.name, record.GitHub, r.name); cerr == nil {
			t.c.Total = n
		}
	}
	if err != nil && ctx.Err() == nil {
		r.logf("failed: %v", err)
	}
	return r.res
}

func (r *repoSync) sync(ctx context.Context) error {
	sc, known := r.g.S.Scope(record.GitHub, r.name)
	r.sc = sc
	if known {
		r.name = sc.Name
		if err := json.Unmarshal(sc.State, &r.st); err != nil {
			return fmt.Errorf("the store's state for %s is damaged: %w", r.name, err)
		}
	}
	// The floor is fixed by the first sync. It only moves back, never forward:
	// records that are stored stay stored.
	r.target = r.o.Since
	if !r.st.Target.IsZero() && r.st.Target.Before(r.target) {
		r.target = r.st.Target
	}
	r.st.Target = r.target
	r.size = r.o.PageSize
	first := sc.SyncedAt.IsZero()

	if err := r.changes(ctx); err != nil {
		return fmt.Errorf("pull requests: %w", err)
	}
	if r.st.Changes.Cut {
		r.notes = append(r.notes, "Some pull requests have more than 100 commits. Only the first 100 were read.")
	}
	// CI runs and deploys are extra depth. A repository without Actions, or a
	// token that cannot read them, still gets its changes stored. The state
	// does not move, so the next sync tries again.
	if !r.o.SkipCI {
		if err := r.ci(ctx); err != nil {
			if ctx.Err() != nil {
				return err
			}
			r.notes = append(r.notes, "CI runs could not be read: "+err.Error())
		}
	}
	if !r.o.SkipDeploys {
		if err := r.deploys(ctx); err != nil {
			if ctx.Err() != nil {
				return err
			}
			r.notes = append(r.notes, "Deploys could not be read: "+err.Error())
		}
	}
	r.sc.SyncedAt, r.sc.Notes = r.o.Now, r.notes
	r.res.First = first
	return r.save()
}

// page reads one page of changes. A page of big pull requests can time out on
// GitHub's side, and a smaller page from the same cursor usually goes through.
func (r *repoSync) page(ctx context.Context, q collector.PageQuery) (collector.Page, error) {
	q.Repo = r.name
	for {
		q.Size = r.size
		p, err := r.k.ChangesPage(ctx, q)
		if errors.Is(err, github.ErrServer) && r.size > 1 && ctx.Err() == nil {
			r.size = (r.size + 1) / 2
			r.logf("github timed out on a page, retrying with %d per page", r.size)
			continue
		}
		if err != nil {
			return p, err
		}
		if p.Repo != r.name {
			// Records carry the name as GitHub spells it, whatever was typed.
			r.name = p.Repo
		}
		r.sc.Private = p.Private
		if p.Cut {
			r.st.Changes.Cut = true
		}
		return p, nil
	}
}

func (r *repoSync) put(rows []record.Change) error {
	added, changed, err := store.Changes.Put(r.g.S, record.GitHub, r.name, rows)
	r.res.Changes.Added += added
	r.res.Changes.Changed += changed
	return err
}

func (r *repoSync) changes(ctx context.Context) error {
	c := &r.st.Changes
	if c.Floor.IsZero() || r.target.Before(c.Floor) {
		if err := r.scan(ctx); err != nil {
			return err
		}
	}
	if err := r.head(ctx); err != nil {
		return err
	}
	if !c.Open {
		if err := r.open(ctx); err != nil {
			return err
		}
	}
	return nil
}

// scan reads changes from the most recently updated down to the target, the
// way a first sync does. It saves its place every few pages. GitHub's cursors
// hold a position by value, an update time and an id, so a saved cursor still
// points at the same place in the list days later.
func (r *repoSync) scan(ctx context.Context) error {
	c := &r.st.Changes
	after, resumed := c.Cursor, c.Cursor != ""
	if resumed {
		r.logf("going on from where the last sync stopped")
	}
	var buf []record.Change
	flush := func(cursor string) error {
		if err := r.put(buf); err != nil {
			return err
		}
		buf = buf[:0]
		c.Cursor = cursor
		return r.save()
	}
	read := 0
	for pages := 1; ; pages++ {
		p, err := r.page(ctx, collector.PageQuery{After: after})
		if errors.Is(err, github.ErrBadCursor) && resumed && pages == 1 {
			// The saved place is no longer accepted. Start from the top:
			// rows that are already stored are only compared, not rewritten.
			r.logf("the saved place is no longer valid, reading from the top")
			after, resumed, pages = "", false, 0
			c.Cursor = ""
			continue
		}
		if err != nil {
			// Keep what was read since the last save. The cursor of the page
			// that failed is where the next sync starts. With nothing read,
			// nothing is written: a repository that cannot be read leaves no
			// trace in the store.
			if len(buf) > 0 {
				if ferr := flush(after); ferr != nil && ctx.Err() == nil {
					return ferr
				}
			}
			return err
		}
		if c.High.IsZero() {
			// The scan starts at the top, so its first row is the newest
			// change there is. Anything updated after this moment is newer
			// still, and the read from the top finds it.
			c.High = r.target
			if len(p.Changes) > 0 {
				c.High = p.Changes[0].UpdatedAt
			}
		}
		done := false
		for _, ch := range p.Changes {
			if ch.UpdatedAt.Before(r.target) {
				// Pages run from the most recently updated change down.
				// Nothing below this one was touched after the target.
				done = true
				break
			}
			buf = append(buf, ch)
		}
		read += len(p.Changes)
		if done || !p.More {
			// Keep the cursor of this last page: a later sync that asks for
			// an earlier floor goes on from here.
			c.Floor = r.target
			return flush(after)
		}
		after = p.End
		if pages%r.o.Checkpoint == 0 {
			if err := flush(after); err != nil {
				return err
			}
			r.logf("%d pull requests read", read)
		}
	}
}

// head reads the changes updated since the last sync: from the top down to the
// newest update time the store has seen.
func (r *repoSync) head(ctx context.Context) error {
	c := &r.st.Changes
	high, top, after := c.High, c.High, ""
	var buf []record.Change
	for pages := 1; ; pages++ {
		p, err := r.page(ctx, collector.PageQuery{After: after})
		if err != nil {
			// The rows are good, so keep them. High stays where it was and
			// the next sync reads this stretch again.
			if perr := r.put(buf); perr != nil && ctx.Err() == nil {
				return perr
			}
			return err
		}
		if pages == 1 && len(p.Changes) > 0 && p.Changes[0].UpdatedAt.After(top) {
			top = p.Changes[0].UpdatedAt
		}
		done := false
		for _, ch := range p.Changes {
			if ch.UpdatedAt.Before(high) {
				done = true
				break
			}
			buf = append(buf, ch)
		}
		if done || !p.More {
			break
		}
		after = p.End
		if pages%r.o.Checkpoint == 0 {
			if err := r.put(buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	if err := r.put(buf); err != nil {
		return err
	}
	c.High = top
	return r.save()
}

// open stores the open changes nobody touched since before the floor. The
// scan from the top never reaches them, and without them the queue in front of
// a station would miss its oldest items. It runs once: after it, a stale change
// that gets touched shows up in the read from the top.
func (r *repoSync) open(ctx context.Context) error {
	c := &r.st.Changes
	after := ""
	var buf []record.Change
	for {
		p, err := r.page(ctx, collector.PageQuery{After: after, OpenOnly: true, OldestFirst: true})
		if err != nil {
			return err
		}
		done := false
		for _, ch := range p.Changes {
			if !ch.UpdatedAt.Before(c.Floor) {
				done = true
				break
			}
			buf = append(buf, ch)
		}
		if done || !p.More {
			break
		}
		after = p.End
	}
	if err := r.put(buf); err != nil {
		return err
	}
	if len(buf) > 0 {
		r.logf("%d open pull requests last touched before %s", len(buf), c.Floor.Format("2006-01-02"))
	}
	c.Open = true
	return r.save()
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ci stores workflow runs. It reads the newest stretch again for runs that
// were still going, then works down in calendar months toward the target and
// saves after each month.
func (r *repoSync) ci(ctx context.Context) error {
	c := &r.st.CI
	type span struct {
		from, to time.Time
		head     bool
	}
	var spans []span
	top := r.o.Now
	if !c.Through.IsZero() {
		from := c.Through.Add(-ciOverlap)
		if from.Before(c.From) {
			from = c.From
		}
		spans = append(spans, span{from, r.o.Now, true})
		top = c.From
	}
	for top.After(r.target) {
		from := monthStart(top.Add(-time.Second))
		if from.Before(r.target) {
			from = r.target
		}
		spans = append(spans, span{from, top, false})
		top = from
	}

	var shas map[string]bool
	joined := func(runs []record.CIRun) bool {
		if shas == nil {
			shas = map[string]bool{}
			store.Changes.Each(r.g.S, record.GitHub, r.name, time.Time{}, time.Time{}, func(_ string, rows []record.Change) error {
				for _, ch := range rows {
					for _, k := range ch.Commits {
						shas[k.SHA] = true
					}
				}
				return nil
			})
		}
		for _, run := range runs {
			if shas[run.SHA] {
				return true
			}
		}
		return false
	}

	capped := func() error {
		r.notes = append(r.notes, fmt.Sprintf("The sync stopped after %d workflow runs. Run sync again to read the rest.", r.o.MaxRuns))
		return r.save()
	}
	budget := r.o.MaxRuns
	for _, sp := range spans {
		if budget <= 0 {
			return capped()
		}
		var buf []record.CIRun
		// lo is how far down this stretch is read. It starts at the bottom of
		// the stretch and rises when the cap stops a read. Runs come newest
		// first, so the oldest run read marks where that read stopped. A read
		// after a stopped one covers only the part above the mark, with an
		// allowance of its own, so every event is whole from lo up.
		lo, cut := sp.from, false
		fetch := func(event string) error {
			start, allow := len(buf), budget
			if allow <= 0 {
				allow = r.o.MaxRuns
			}
			n, stopped, err := r.k.Runs(ctx, r.name, event, lo, sp.to, allow, func(runs []record.CIRun) { buf = append(buf, runs...) })
			budget -= n
			if stopped {
				cut = true
				oldest := sp.to
				for _, run := range buf[start:] {
					if run.CreatedAt.Before(oldest) {
						oldest = run.CreatedAt
					}
				}
				lo = oldest
			}
			return err
		}
		var err error
		switch c.Mode {
		case modeAll:
			err = fetch("")
		default:
			if err = fetch("pull_request"); err == nil {
				err = fetch("pull_request_target")
			}
			if err != nil || c.Mode == modePR {
				break
			}
			// Runs of pull request events are the usual CI. A repository
			// that only builds on push has none on its change commits, so
			// look at every event before deciding.
			if joined(buf) {
				c.Mode = modePR
				break
			}
			n := len(buf)
			if err = fetch(""); err != nil {
				break
			}
			if joined(buf[n:]) {
				c.Mode = modeAll
			} else {
				buf = buf[:n]
			}
		}
		if err != nil {
			return err
		}
		added, changed, err := store.CIRuns.Put(r.g.S, record.GitHub, r.name, buf)
		r.res.CIRuns.Added += added
		r.res.CIRuns.Changed += changed
		if err != nil {
			return err
		}
		if cut {
			// The cap left the oldest runs of this stretch unread. Record how
			// far down the stored runs go, and let the next sync read on. A
			// cut in the newest stretch moves nothing: the next sync reads
			// that stretch again.
			if !sp.head {
				c.From = lo
				if c.Through.IsZero() {
					c.Through = r.o.Now
				}
			}
			return capped()
		}
		if sp.head || c.Through.IsZero() {
			c.Through = r.o.Now
		}
		if !sp.head {
			c.From = sp.from
		}
		if err := r.save(); err != nil {
			return err
		}
	}
	if n := r.res.CIRuns.Added + r.res.CIRuns.Changed; n > 0 {
		r.logf("%d workflow runs stored", n)
	}
	return nil
}

// deploys stores deployments and releases. Deployments are read back over the
// last week, since they get their statuses after they are created. Releases
// are few, and a release can be published long after its draft was created,
// so they are read back to the floor every time.
func (r *repoSync) deploys(ctx context.Context) error {
	d := &r.st.Deploys
	since := r.target
	if !d.Through.IsZero() && !r.target.Before(d.From) {
		if since = d.Through.Add(-deployOverlap); since.Before(r.target) {
			since = r.target
		}
	}
	rows, err := r.k.Deploys(ctx, r.name, since, r.target)
	if err != nil {
		return err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	added, changed, err := store.Deploys.Put(r.g.S, record.GitHub, r.name, rows)
	r.res.Deploys.Added += added
	r.res.Deploys.Changed += changed
	if err != nil {
		return err
	}
	d.From, d.Through = r.target, r.o.Now
	return r.save()
}
