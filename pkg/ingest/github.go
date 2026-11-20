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
	// SkipCI, SkipDeploys and SkipTickets leave those records out.
	SkipCI      bool
	SkipDeploys bool
	SkipTickets bool
	// IncludeForks and IncludeArchived widen an owner sync.
	IncludeForks    bool
	IncludeArchived bool
	Logf            func(format string, a ...any)
	// Progress is told each step of the sync, for a display. It is called
	// from several goroutines at once. May be nil.
	Progress func(Event)
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
	// Tickets are the repository's issues. Links tie its changes to the
	// tickets they close.
	Tickets Count
	Links   Count
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
		// Links is set once the stored changes carry the tickets they close.
		// A store written before links were read has it unset, and the next
		// sync reads its window once more.
		Links bool `json:"links"`
		// Rev is the version of the fields the stored changes carry. A store
		// written by a version that read fewer has a lower one, and the next
		// sync reads its window once more.
		Rev int `json:"rev"`
	} `json:"changes"`
	Tickets struct {
		// Floor is the update time the stored tickets go back to.
		Floor time.Time `json:"floor"`
		// High is the update time the read has reached. The next sync reads
		// the tickets updated since.
		High time.Time `json:"high"`
		// Open is set once the open tickets last touched before Floor are stored.
		Open bool `json:"open"`
	} `json:"tickets"`
	CI struct {
		// Mode is which runs carry the repository's CI: "pull_request" when
		// runs of pull request events do, "all" when only runs of other
		// events land on change commits. Empty while that is unknown.
		Mode string `json:"mode"`
		// From and Through are the ends of the time range whose runs are stored.
		From    time.Time `json:"from"`
		Through time.Time `json:"through"`
		// Queue is set once that range holds the runs of the merge queue,
		// with the branch that ties each to its change.
		Queue bool `json:"queue"`
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

// changeRev is the version of the fields a change is read with. 1 added the
// commit a merge leaves on the base branch.
const changeRev = 1

// queueEvent is the event of a run a merge queue starts.
const queueEvent = "merge_group"

func (g *GitHub) logf(format string, a ...any) {
	if g.Opt.Logf != nil {
		g.Opt.Logf(format, a...)
	}
}

// StoppedError is returned when a sync was stopped before its end, by an
// interrupt or a deadline. Nothing is lost: the store keeps what was read.
type StoppedError struct{ Cause error }

func (e *StoppedError) Error() string {
	return "stopped before the end. What was read is stored, and the next sync goes on from there"
}

func (e *StoppedError) Unwrap() error { return e.Cause }

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

	o.emit(Event{Kind: Listed, Source: record.GitHub, Scope: target, Total: len(repos), Names: append([]string(nil), repos...)})
	res := Result{Repos: make([]RepoResult, len(repos))}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(o.Workers, len(repos)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r := &repoSync{g: g, k: k, o: o, name: repos[i]}
				r.t = tracker{o: o, source: record.GitHub, slot: i, name: func() string { return r.name }}
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
		return res, &StoppedError{Cause: err}
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
	// issues is the page size of the issue reads. It shrinks the same way.
	issues int
	res    RepoResult
	notes  []string
	// links are the ticket links of the changes read and not stored yet, by
	// the id of the change.
	links map[string][]record.Link
	// t tells a display where the sync stands.
	t tracker
	// full is set once this sync reads a whole window, not only what changed.
	full bool
}

func (r *repoSync) logf(format string, a ...any) {
	r.g.logf("%s: "+format, append([]any{r.name}, a...)...)
}

// notef is logf for a sentence a display shows too.
func (r *repoSync) notef(format string, a ...any) {
	r.logf(format, a...)
	r.t.note(fmt.Sprintf(format, a...))
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
	began := time.Now()
	err := r.sync(ctx, began)
	r.res.Repo, r.res.From, r.res.Notes, r.res.Err = r.name, r.st.Changes.Floor, r.notes, err
	for _, t := range []struct {
		name string
		c    *Count
	}{{store.Changes.Name, &r.res.Changes}, {store.CIRuns.Name, &r.res.CIRuns}, {store.Deploys.Name, &r.res.Deploys},
		{store.Tickets.Name, &r.res.Tickets}, {store.Links.Name, &r.res.Links}} {
		if n, cerr := r.g.S.Count(t.name, record.GitHub, r.name); cerr == nil {
			t.c.Total = n
		}
	}
	if err != nil && ctx.Err() == nil {
		r.logf("failed: %v", err)
	}
	res := r.res
	r.t.emit(Event{Kind: ScopeDone, Repo: &res, Took: time.Since(began), Err: err})
	return r.res
}

// step runs one step of the sync and tells a display how it went. c is the
// count the step adds to.
func (r *repoSync) step(ctx context.Context, s Step, c *Count, fn func(context.Context) error) error {
	before := *c
	r.t.start(s)
	err := fn(ctx)
	r.t.done(c.Added-before.Added, c.Changed-before.Changed, err)
	return err
}

func (r *repoSync) sync(ctx context.Context, began time.Time) error {
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
	r.t.emit(Event{Kind: ScopeStarted, First: first, Last: time.Duration(sc.Took * float64(time.Second)), LastFull: sc.TookFull, LastAt: sc.SyncedAt})

	if err := r.step(ctx, StepChanges, &r.res.Changes, r.changes); err != nil {
		if errors.Is(err, github.ErrForbidden) && !r.o.SkipTickets {
			// The query asks for the issues each pull request closes.
			return fmt.Errorf("pull requests: %w. If the token cannot read issues, give it read-only access to Issues, or pass --skip-tickets", err)
		}
		return fmt.Errorf("pull requests: %w", err)
	}
	if r.st.Changes.Cut {
		r.notes = append(r.notes, "Some pull requests have more than 100 commits. Only the first 100 were read.")
	}
	// Tickets, CI runs and deploys are extra depth. A repository without
	// Actions, or a token that cannot read them, still gets its changes
	// stored. The state does not move, so the next sync tries again.
	if !r.o.SkipTickets {
		if err := r.step(ctx, StepIssues, &r.res.Tickets, r.tickets); err != nil {
			if ctx.Err() != nil {
				return err
			}
			r.notes = append(r.notes, "Issues could not be read: "+err.Error())
		}
	}
	if !r.o.SkipCI {
		if err := r.step(ctx, StepCI, &r.res.CIRuns, r.ci); err != nil {
			if ctx.Err() != nil {
				return err
			}
			r.notes = append(r.notes, "CI runs could not be read: "+err.Error())
		}
	}
	if !r.o.SkipDeploys {
		if err := r.step(ctx, StepDeploys, &r.res.Deploys, r.deploys); err != nil {
			if ctx.Err() != nil {
				return err
			}
			r.notes = append(r.notes, "Deploys could not be read: "+err.Error())
		}
	}
	r.sc.SyncedAt, r.sc.Notes = r.o.Now, r.notes
	r.sc.Took, r.sc.TookFull = time.Since(began).Seconds(), first || r.full
	r.res.First = first
	return r.save()
}

// page reads one page of changes. A page of big pull requests can time out on
// GitHub's side, and a smaller page from the same cursor usually goes through.
func (r *repoSync) page(ctx context.Context, q collector.PageQuery) (collector.Page, error) {
	q.Repo, q.NoLinks = r.name, r.o.SkipTickets
	for {
		q.Size = r.size
		p, err := r.k.ChangesPage(ctx, q)
		if errors.Is(err, github.ErrServer) && r.size > 1 && ctx.Err() == nil {
			r.size = (r.size + 1) / 2
			r.notef("github timed out on a page, retrying with %d per page", r.size)
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
		if len(p.Links) > 0 && r.links == nil {
			r.links = map[string][]record.Link{}
		}
		for _, l := range p.Links {
			r.links[l.From] = append(r.links[l.From], l)
		}
		return p, nil
	}
}

// put stores changes and, with them, the links to the tickets they close. A
// link is never stored without its change.
func (r *repoSync) put(rows []record.Change) error {
	added, changed, err := store.Changes.Put(r.g.S, record.GitHub, r.name, rows)
	r.res.Changes.Added += added
	r.res.Changes.Changed += changed
	if err != nil {
		return err
	}
	var links []record.Link
	for i := range rows {
		links = append(links, r.links[rows[i].ID]...)
		delete(r.links, rows[i].ID)
	}
	added, changed, err = store.Links.Put(r.g.S, record.GitHub, r.name, links)
	r.res.Links.Added += added
	r.res.Links.Changed += changed
	return err
}

func (r *repoSync) changes(ctx context.Context) error {
	c := &r.st.Changes
	// What is left over belongs to changes outside the window.
	defer func() { r.links = nil }()
	fresh := c.Floor.IsZero() && c.Cursor == "" && c.High.IsZero()
	if fresh {
		c.Rev = changeRev
	}
	switch {
	case r.o.SkipTickets:
		// This sync stores changes without their links. The next one that
		// reads tickets reads the window once more for them.
		c.Links = false
	case fresh:
		// Nothing is stored yet, so every change comes with its links.
		c.Links = true
	case !c.Links:
		// The stale open changes were stored without links too.
		c.Open = false
	}
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

// told tells a display that a page of changes was read on the way from top
// down to floor. Pages run from the most recently updated change down, so the
// last change of the page is where the read stands.
//
// The share of the read that is done is taken from the time it has reached.
// A repository whose pull requests all sit in the last weeks of the window
// would stand near zero that way until its last page. So the share is at
// least the part of all pull requests that were read: the read ends at the
// floor or at the last pull request, whichever comes first.
func (r *repoSync) told(p collector.Page, top, floor time.Time) {
	if len(p.Changes) == 0 {
		r.t.advance(0, 0, time.Time{})
		return
	}
	at := p.Changes[len(p.Changes)-1].UpdatedAt
	if at.Before(floor) {
		at = floor
	}
	share := part(top, floor, at)
	if p.Total > 0 {
		share = max(share, min(1, float64(r.t.read+len(p.Changes))/float64(p.Total)))
	}
	r.t.advance(len(p.Changes), share, at)
}

// scan reads changes from the most recently updated down to the target, the
// way a first sync does. It saves its place every few pages. GitHub's cursors
// hold a position by value, an update time and an id, so a saved cursor still
// points at the same place in the list days later.
func (r *repoSync) scan(ctx context.Context) error {
	c := &r.st.Changes
	after, resumed := c.Cursor, c.Cursor != ""
	r.full = true
	if resumed {
		r.notef("going on from where the last sync stopped")
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
			r.notef("the saved place is no longer valid, reading from the top")
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
		r.told(p, r.o.Now, r.target)
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
// newest update time the store has seen. A store whose changes were written
// before links were read, or by a version that read fewer fields, gets its
// whole window read once more. The changes are compared with what is stored,
// and only what differs is written.
func (r *repoSync) head(ctx context.Context) error {
	c := &r.st.Changes
	high, top, after := c.High, c.High, ""
	var missing []string
	if !c.Links && !r.o.SkipTickets {
		missing = append(missing, "the tickets its pull requests close")
	}
	if c.Rev < changeRev {
		missing = append(missing, "the commit each merge left")
	}
	if len(missing) > 0 {
		high = c.Floor
		r.full = true
		r.notef("reading the window once more for %s", strings.Join(missing, " and "))
	}
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
		r.told(p, r.o.Now, high)
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
	c.High, c.Links, c.Rev = top, !r.o.SkipTickets, changeRev
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
		r.t.advance(len(p.Changes), 0, time.Time{})
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

// tickets stores the repository's issues: the ones updated since the floor,
// and the open ones nobody touched since before it, which the queue in front
// of the spec station needs. Issues are read from the least recently updated
// up, so the place a read has reached is an update time. A read that stops
// goes on from that time, and so does the next sync.
func (r *repoSync) tickets(ctx context.Context) error {
	t := &r.st.Tickets
	reached := func(at time.Time) error {
		if at.After(t.High) {
			t.High = at
		}
		return r.save()
	}
	switch {
	case t.Floor.IsZero():
		t.Floor, t.High = r.target, r.target
	case r.target.Before(t.Floor):
		// An earlier floor was asked for. The issues updated since the old
		// floor are stored, so read the stretch below it.
		if _, err := r.ticketRange(ctx, r.target, t.Floor, false, nil); err != nil {
			return err
		}
		t.Floor = r.target
		if err := r.save(); err != nil {
			return err
		}
	}
	if _, err := r.ticketRange(ctx, t.High, time.Time{}, false, reached); err != nil {
		return err
	}
	if c := r.res.Tickets; c.Added+c.Changed > 0 {
		r.logf("%s new, %d updated", count(c.Added, "issue", "issues"), c.Changed)
	}
	if !t.Open {
		n, err := r.ticketRange(ctx, time.Time{}, t.Floor, true, nil)
		if err != nil {
			return err
		}
		if n > 0 {
			r.logf("%d open issues last touched before %s", n, t.Floor.Format("2006-01-02"))
		}
		t.Open = true
	}
	return r.save()
}

// ticketRange reads issues from the least recently updated up, starting at
// since, and stores them. With before set it stops at the first issue updated
// at or after it. reached is told the update time the stored issues go up to,
// every few pages and at the end. It reports how many issues it stored.
func (r *repoSync) ticketRange(ctx context.Context, since, before time.Time, openOnly bool, reached func(time.Time) error) (int, error) {
	if r.issues == 0 {
		// An issue is light next to a pull request with its commits and reviews.
		r.issues = min(4*r.o.PageSize, 100)
	}
	after, n := "", 0
	var buf []record.Ticket
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		added, changed, err := store.Tickets.Put(r.g.S, record.GitHub, r.name, buf)
		r.res.Tickets.Added += added
		r.res.Tickets.Changed += changed
		if err != nil {
			return err
		}
		last := buf[len(buf)-1].UpdatedAt
		n += len(buf)
		buf = buf[:0]
		if reached != nil {
			return reached(last)
		}
		return nil
	}
	for pages := 1; ; pages++ {
		p, err := r.k.TicketsPage(ctx, collector.TicketQuery{Repo: r.name, After: after, Size: r.issues, Since: since, OpenOnly: openOnly})
		if errors.Is(err, github.ErrServer) && r.issues > 1 && ctx.Err() == nil {
			r.issues = (r.issues + 1) / 2
			r.notef("github timed out on a page of issues, retrying with %d per page", r.issues)
			pages--
			continue
		}
		if err != nil {
			// The issues read so far are good, so keep them. The next sync
			// goes on from the update time they reach.
			if ferr := flush(); ferr != nil && ctx.Err() == nil {
				return n, ferr
			}
			return n, err
		}
		done := false
		for _, t := range p.Tickets {
			if !before.IsZero() && !t.UpdatedAt.Before(before) {
				done = true
				break
			}
			buf = append(buf, t)
		}
		if n := len(p.Tickets); n > 0 && !openOnly && before.IsZero() {
			// The read up to now is the one that walks the window.
			at := p.Tickets[n-1].UpdatedAt
			r.t.advance(n, part(since, r.o.Now, at), at)
		} else {
			r.t.advance(n, 0, time.Time{})
		}
		if done || !p.More {
			return n, flush()
		}
		after = p.End
		if pages%r.o.Checkpoint == 0 {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
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
	if c.Through.IsZero() {
		// Nothing is stored yet, so every stretch is read with them.
		c.Queue = true
	}
	if !c.Queue {
		if err := r.queueRuns(ctx); err != nil {
			return err
		}
	}
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
	for i, sp := range spans {
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
			got := 0
			start, allow := len(buf), budget
			if allow <= 0 {
				allow = r.o.MaxRuns
			}
			n, stopped, err := r.k.Runs(ctx, r.name, event, lo, sp.to, allow, func(runs []record.CIRun, total int) {
				buf = append(buf, runs...)
				got += len(runs)
				// The stretches before this one are done. Inside one, GitHub
				// says how many runs the event has, so the runs read and the
				// runs still to come tell how far the stretch is. A later
				// event can bring more runs than the ones before it, and
				// then the share goes back.
				var at time.Time
				if len(runs) > 0 {
					at = runs[len(runs)-1].CreatedAt
				}
				inside, left := 0.0, max(0, min(total, allow)-got)
				if len(buf)+left > 0 {
					inside = min(0.97, float64(len(buf))/float64(len(buf)+left))
				}
				r.t.revise(len(runs), (float64(i)+inside)/float64(len(spans)), at)
			})
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
			for _, event := range []string{"pull_request", "pull_request_target", queueEvent} {
				if err = fetch(event); err != nil {
					break
				}
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
		r.t.advance(0, float64(i+1)/float64(len(spans)), sp.from)
	}
	if n := r.res.CIRuns.Added + r.res.CIRuns.Changed; n > 0 {
		r.logf("%d workflow runs stored", n)
	}
	return nil
}

// queueRuns reads the runs of the merge queue over the range a version that
// did not read them has stored. A repository with no merge queue costs one
// request. A store that keeps the runs of every event has these runs, but
// without the branch that ties each one to its change, so it is read too.
func (r *repoSync) queueRuns(ctx context.Context) error {
	c := &r.st.CI
	var buf []record.CIRun
	_, stopped, err := r.k.Runs(ctx, r.name, queueEvent, c.From, c.Through, r.o.MaxRuns, func(runs []record.CIRun, _ int) { buf = append(buf, runs...) })
	if err != nil {
		return err
	}
	added, changed, err := store.CIRuns.Put(r.g.S, record.GitHub, r.name, buf)
	r.res.CIRuns.Added += added
	r.res.CIRuns.Changed += changed
	if err != nil {
		return err
	}
	if len(buf) > 0 {
		r.logf("%d runs of the merge queue read", len(buf))
	}
	// A read the cap stopped is done again by the next sync.
	c.Queue = !stopped
	return r.save()
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
