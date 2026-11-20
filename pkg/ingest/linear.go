package ingest

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/linear"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// The queries sent to Linear. They are files so they can be checked against
// Linear's published schema as they are.
var (
	//go:embed queries/linear_org.graphql
	linearOrgQuery string
	//go:embed queries/linear_teams.graphql
	linearTeamsQuery string
	//go:embed queries/linear_states.graphql
	linearStatesQuery string
	//go:embed queries/linear_issues.graphql
	linearIssuesQuery string
	//go:embed queries/linear_history.graphql
	linearHistoryQuery string
)

const (
	// linearPage is the number of issues on a page. It is the number written
	// in the issues query.
	linearPage = 50
	// linearPageMark is where the issues query says its page size.
	linearPageMark = "issues(first: 50,"
	// linearSpans is how many state changes of an issue are read. It is the
	// number written in the issues query.
	linearSpans = 20
	// linearSlice is the stretch of update times one read covers. Linear is
	// read a slice at a time, and the place a sync has reached is saved after
	// each, so a sync that stops goes on from the last finished slice.
	linearSlice = 7 * 24 * time.Hour
	// linearOverlap is read again at the start of each sync, for tickets
	// Linear stamped a little before it showed them.
	linearOverlap = 10 * time.Minute
)

// SourceResult is the outcome of a ticket source, such as a Linear workspace.
type SourceResult struct {
	// Source is the kind of source, and Name the workspace it read.
	Source string
	Name   string
	// Tickets are the tickets of the workspace. Links tie changes to the
	// tickets the source says they belong to.
	Tickets Count
	Links   Count
	// From is the update time the store holds the tickets from.
	From time.Time
	// First is set when this was the first finished sync of the workspace.
	First bool
	// Again is set when the stored tickets were dropped and read again,
	// because the list of teams changed.
	Again bool
	// Keys are the key prefixes of the tickets in the store, such as ENG. A
	// pull request that names a key with one of them is tied to its ticket.
	Keys []string
}

// storedKeys reads the key prefixes of the tickets a source has in the store.
func storedKeys(s *store.Store, source, name string) ([]string, error) {
	var all []record.Ticket
	err := store.Tickets.Each(s, source, name, time.Time{}, time.Time{}, func(_ string, rows []record.Ticket) error {
		all = append(all, rows...)
		return nil
	})
	return record.KeyPrefixes(all), err
}

// Linear syncs the tickets of the Linear workspace a key belongs to into a
// store. A ticket in Linear is called an issue.
type Linear struct {
	C   *linear.Client
	S   *store.Store
	Opt Options
	// Teams limits the read to the teams with these keys, such as ENG. Empty
	// reads every team the key can see.
	Teams []string
}

// linearState is what a sync keeps of a workspace between runs.
type linearState struct {
	ticketWindow
	// Teams are the team keys the read is limited to, sorted.
	Teams []string `json:"teams"`
}

type linearSync struct {
	l      *Linear
	o      Options
	name   string
	sc     store.Scope
	st     linearState
	size   int
	states map[string]string // workflow state id -> type
	res    SourceResult
	t      tracker
}

func (r *linearSync) logf(format string, a ...any) {
	if r.o.Logf != nil {
		r.o.Logf("linear %s: "+format, append([]any{r.name}, a...)...)
	}
}

// Sync reads the workspace into the store. What was read before an error is
// stored, and the next sync goes on from there.
func (l *Linear) Sync(ctx context.Context) (SourceResult, error) {
	r := &linearSync{l: l, o: l.Opt.withDefaults(), size: linearPage}
	r.res.Source = record.Linear
	r.t = tracker{o: r.o, source: record.Linear, name: func() string { return r.name }}
	began := time.Now()
	err := r.sync(ctx, began)
	r.res.Name, r.res.From = r.name, r.st.Floor
	if r.name != "" {
		if n, cerr := l.S.Count(store.Tickets.Name, record.Linear, r.name); cerr == nil {
			r.res.Tickets.Total = n
		}
		if n, cerr := l.S.Count(store.Links.Name, record.Linear, r.name); cerr == nil {
			r.res.Links.Total = n
		}
		if keys, kerr := storedKeys(l.S, record.Linear, r.name); kerr == nil {
			r.res.Keys = keys
		}
	}
	if r.t.step != "" {
		r.t.done(r.res.Tickets.Added, r.res.Tickets.Changed, err)
	}
	res := r.res
	r.t.emit(Event{Kind: ScopeDone, Tickets: &res, Took: time.Since(began), Err: err})
	if cerr := ctx.Err(); cerr != nil {
		return r.res, &StoppedError{Cause: cerr}
	}
	if err != nil {
		return r.res, named(record.Linear, err)
	}
	return r.res, nil
}

func teamKeys(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range in {
		k = strings.ToUpper(strings.TrimSpace(k))
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *linearSync) save() error {
	b, err := json.Marshal(r.st)
	if err != nil {
		return err
	}
	r.sc.Source, r.sc.Name, r.sc.Private, r.sc.State = record.Linear, r.name, true, b
	r.sc.From = r.st.Floor
	return r.l.S.PutScope(r.sc)
}

func (r *linearSync) sync(ctx context.Context, began time.Time) error {
	var org struct {
		Organization struct {
			URLKey string `json:"urlKey"`
		} `json:"organization"`
	}
	if err := r.l.C.Query(ctx, linearOrgQuery, nil, &org); err != nil {
		return err
	}
	r.name = org.Organization.URLKey
	teams := teamKeys(r.l.Teams)
	if err := r.checkTeams(ctx, teams); err != nil {
		return err
	}

	sc, known := r.l.S.Scope(record.Linear, r.name)
	if known && len(sc.State) > 0 {
		if err := json.Unmarshal(sc.State, &r.st); err != nil {
			return fmt.Errorf("the saved place of the sync cannot be read: %w", err)
		}
	}
	if known && !sameKeys(teamKeys(r.st.Teams), teams) {
		// The tickets of a team that is no longer read would stay as they
		// were at the last sync for good, open ones as open. So the
		// workspace is read again from nothing.
		r.logf("the list of teams changed. The stored tickets are dropped and read again")
		r.t.note("the list of teams changed. The stored tickets are dropped and read again")
		r.res.Again, known = true, false
	}
	if !known {
		// This also clears what a drop that was cut short left behind.
		if err := r.l.S.DropScope(record.Linear, r.name); err != nil {
			return fmt.Errorf("%q, the URL key of the Linear workspace, cannot name a scope in the store: %w", r.name, err)
		}
		sc, r.st = store.Scope{}, linearState{}
	}
	r.sc, r.st.Teams = sc, teams
	first := r.sc.SyncedAt.IsZero()
	r.t.emit(Event{Kind: ScopeStarted, First: first, Last: time.Duration(r.sc.Took * float64(time.Second)), LastFull: r.sc.TookFull, LastAt: r.sc.SyncedAt})
	r.t.start(StepTickets)

	if err := r.loadStates(ctx); err != nil {
		return err
	}

	read := windowRead{o: r.o, w: &r.st.ticketWindow, slice: linearSlice, overlap: linearOverlap, save: r.save,
		told: func(share float64, at time.Time) { r.t.advance(0, share, at) },
		span: func(ctx context.Context, from, to time.Time) error { return r.span(ctx, from, to, false) }}
	if err := read.updated(ctx); err != nil {
		return err
	}
	if c := r.res.Tickets; c.Added+c.Changed > 0 {
		r.logf("%s new, %d updated", count(c.Added, "ticket", "tickets"), c.Changed)
	}

	if !r.st.Open {
		before := r.res.Tickets.Added
		if err := r.span(ctx, time.Time{}, r.st.Floor, true); err != nil {
			return err
		}
		if n := r.res.Tickets.Added - before; n > 0 {
			r.logf("%d open tickets last touched before %s", n, r.st.Floor.Format("2006-01-02"))
		}
		r.st.Open = true
	}
	r.sc.SyncedAt, r.sc.Notes = r.o.Now, nil
	r.sc.Took, r.sc.TookFull = time.Since(began).Seconds(), first
	r.res.First = first
	return r.save()
}

// checkTeams fails when a team key that was asked for names no team. Without
// the check a wrong key gives a report with no tickets and no reason.
func (r *linearSync) checkTeams(ctx context.Context, teams []string) error {
	if len(teams) == 0 {
		return nil
	}
	var out struct {
		Teams struct {
			Nodes []struct {
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	vars := map[string]any{"filter": map[string]any{"key": map[string]any{"in": teams}}}
	if err := r.l.C.Query(ctx, linearTeamsQuery, vars, &out); err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range out.Teams.Nodes {
		have[strings.ToUpper(t.Key)] = true
	}
	var missing []string
	for _, k := range teams {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the workspace %s has no team with the key %s that this Linear key can read", r.name, strings.Join(missing, ", "))
	}
	return nil
}

func (r *linearSync) loadStates(ctx context.Context) error {
	r.states = map[string]string{}
	after := ""
	for {
		var out struct {
			WorkflowStates struct {
				Nodes []struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"nodes"`
				PageInfo linearPageInfo `json:"pageInfo"`
			} `json:"workflowStates"`
		}
		if err := r.l.C.Query(ctx, linearStatesQuery, map[string]any{"after": cursorVar(after)}, &out); err != nil {
			return err
		}
		for _, s := range out.WorkflowStates.Nodes {
			r.states[s.ID] = s.Type
		}
		if !out.WorkflowStates.PageInfo.HasNextPage || out.WorkflowStates.PageInfo.EndCursor == "" {
			return nil
		}
		after = out.WorkflowStates.PageInfo.EndCursor
	}
}

type linearPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// cursorVar is the value of an $after variable: null for the first page.
func cursorVar(after string) any {
	if after == "" {
		return nil
	}
	return after
}

// linearOpenTypes are the types of a workflow state that an open ticket has.
var linearOpenTypes = []string{"triage", "backlog", "unstarted", "started"}

func (r *linearSync) filter(from, to time.Time, open bool) map[string]any {
	updated := map[string]any{}
	if !from.IsZero() {
		updated["gte"] = from.UTC().Format(time.RFC3339)
	}
	if !to.IsZero() {
		updated["lt"] = to.UTC().Format(time.RFC3339)
	}
	f := map[string]any{"updatedAt": updated}
	if len(r.st.Teams) > 0 {
		f["team"] = map[string]any{"key": map[string]any{"in": r.st.Teams}}
	}
	if open {
		f["state"] = map[string]any{"type": map[string]any{"in": linearOpenTypes}}
	}
	return f
}

// span reads every ticket updated in [from, to) and stores it. A zero bound
// is no bound. With open set it reads only the tickets in an open state. It
// does not lean on the order Linear returns a page in.
func (r *linearSync) span(ctx context.Context, from, to time.Time, open bool) error {
	var tickets []record.Ticket
	var links []record.Link
	flush := func() error {
		added, changed, err := store.Tickets.Put(r.l.S, record.Linear, r.name, tickets)
		r.res.Tickets.Added += added
		r.res.Tickets.Changed += changed
		if err != nil {
			return err
		}
		added, changed, err = store.Links.Put(r.l.S, record.Linear, r.name, links)
		r.res.Links.Added += added
		r.res.Links.Changed += changed
		tickets, links = tickets[:0], links[:0]
		return err
	}
	vars := map[string]any{"filter": r.filter(from, to, open)}
	after := ""
	for pages := 1; ; pages++ {
		var out struct {
			Issues struct {
				Nodes    []linearIssue  `json:"nodes"`
				PageInfo linearPageInfo `json:"pageInfo"`
			} `json:"issues"`
		}
		vars["after"] = cursorVar(after)
		query := strings.Replace(linearIssuesQuery, linearPageMark, "issues(first: "+strconv.Itoa(r.size)+",", 1)
		err := r.l.C.Query(ctx, query, vars, &out)
		if (errors.Is(err, linear.ErrTooComplex) || errors.Is(err, linear.ErrServer)) && r.size > 1 && ctx.Err() == nil {
			r.size = (r.size + 1) / 2
			r.logf("linear could not answer a page of tickets, retrying with %d per page", r.size)
			r.t.note(fmt.Sprintf("Linear could not answer a page of tickets, retrying with %d per page", r.size))
			pages--
			continue
		}
		if err != nil {
			// The tickets read so far are good, so keep them. The slice is
			// read again by the next sync, and they are found unchanged.
			if ferr := flush(); ferr != nil && ctx.Err() == nil {
				return ferr
			}
			return err
		}
		r.t.advance(len(out.Issues.Nodes), 0, time.Time{})
		for i := range out.Issues.Nodes {
			if err := r.history(ctx, &out.Issues.Nodes[i]); err != nil {
				if ferr := flush(); ferr != nil && ctx.Err() == nil {
					return ferr
				}
				return err
			}
			t, ls := linearRecord(&out.Issues.Nodes[i], r.states)
			tickets = append(tickets, t)
			links = append(links, ls...)
		}
		if !out.Issues.PageInfo.HasNextPage || out.Issues.PageInfo.EndCursor == "" {
			return flush()
		}
		after = out.Issues.PageInfo.EndCursor
		if pages%r.o.Checkpoint == 0 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

// linearSpan is a stretch of time an issue spent in one state.
type linearSpan struct {
	StartedAt time.Time `json:"startedAt"`
	StateID   string    `json:"stateId"`
}

// history reads the rest of the state history of an issue, when the issues
// query carried only the start of it. The end matters most: it says when the
// issue was last done, and whether it was opened again.
func (r *linearSync) history(ctx context.Context, n *linearIssue) error {
	info := n.StateHistory.PageInfo
	for info.HasNextPage && info.EndCursor != "" {
		var out struct {
			Issue struct {
				StateHistory struct {
					Nodes    []linearSpan   `json:"nodes"`
					PageInfo linearPageInfo `json:"pageInfo"`
				} `json:"stateHistory"`
			} `json:"issue"`
		}
		if err := r.l.C.Query(ctx, linearHistoryQuery, map[string]any{"id": n.ID, "after": info.EndCursor}, &out); err != nil {
			return err
		}
		h := out.Issue.StateHistory
		if h.PageInfo.HasNextPage && h.PageInfo.EndCursor == info.EndCursor {
			return errors.New("linear answered with the same page of a state history twice")
		}
		n.StateHistory.Nodes = append(n.StateHistory.Nodes, h.Nodes...)
		info = h.PageInfo
	}
	return nil
}

// linearIssue is an issue as the issues query returns it.
type linearIssue struct {
	ID                  string     `json:"id"`
	Identifier          string     `json:"identifier"`
	PreviousIdentifiers []string   `json:"previousIdentifiers"`
	Title               string     `json:"title"`
	URL                 string     `json:"url"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	StartedAt           *time.Time `json:"startedAt"`
	CompletedAt         *time.Time `json:"completedAt"`
	CanceledAt          *time.Time `json:"canceledAt"`
	ArchivedAt          *time.Time `json:"archivedAt"`
	Trashed             *bool      `json:"trashed"`
	State               struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Team struct {
		Key string `json:"key"`
	} `json:"team"`
	Creator *struct {
		DisplayName string `json:"displayName"`
		App         bool   `json:"app"`
	} `json:"creator"`
	BotActor *struct {
		Name *string `json:"name"`
	} `json:"botActor"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Attachments struct {
		Nodes []struct {
			URL string `json:"url"`
		} `json:"nodes"`
	} `json:"attachments"`
	StateHistory struct {
		Nodes    []linearSpan   `json:"nodes"`
		PageInfo linearPageInfo `json:"pageInfo"`
	} `json:"stateHistory"`
}

// linearTicketState maps the type of a Linear workflow state onto the five
// states of a ticket. A duplicate is closed without being done, the same as a
// cancelled ticket.
func linearTicketState(typ string) string {
	switch typ {
	case "unstarted":
		return record.TicketUnstarted
	case "started":
		return record.TicketStarted
	case "completed":
		return record.TicketDone
	case "canceled", "duplicate":
		return record.TicketCancelled
	}
	// Triage, backlog, and a type this version does not know.
	return record.TicketBacklog
}

func utc(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// linearRecord turns an issue into a ticket, and its pull request attachments
// into links.
func linearRecord(n *linearIssue, states map[string]string) (record.Ticket, []record.Link) {
	t := record.Ticket{
		Source: record.Linear, ID: record.TicketID(record.Linear, n.ID), Key: n.Identifier,
		Aliases: []string{}, Project: n.Team.Key, URL: n.URL, Title: n.Title, Labels: []string{},
		Author: record.Actor{Kind: record.ActorUser}, State: linearTicketState(n.State.Type), RawState: n.State.Name,
		CreatedAt: n.CreatedAt.UTC(), UpdatedAt: n.UpdatedAt.UTC(),
	}
	for _, a := range n.PreviousIdentifiers {
		if a != "" && a != n.Identifier {
			t.Aliases = append(t.Aliases, a)
		}
	}
	for _, l := range n.Labels.Nodes {
		t.Labels = append(t.Labels, l.Name)
	}
	// The display name is the short name people see in Linear. No email is
	// read. An issue with no person behind it was made by an integration.
	switch {
	case n.Creator != nil:
		t.Author.Login = n.Creator.DisplayName
		if n.Creator.App {
			t.Author.Kind = record.ActorBot
		}
	case n.BotActor != nil:
		t.Author.Kind = record.ActorBot
		if n.BotActor.Name != nil {
			t.Author.Login = *n.BotActor.Name
		}
	}

	// The history lists the stretches the issue spent in each state, oldest
	// first. It says when work first started, also on an issue that went
	// back to the backlog since, and how often a done issue was opened again.
	started := utc(n.StartedAt)
	wasDone := false
	// last is the stretch the issue is in now, as far as the history knows.
	// Linear keeps the time an issue was first done in completedAt, also after
	// it was opened again and done once more. The history has the last time.
	var last *time.Time
	lastTyp := ""
	for _, s := range n.StateHistory.Nodes {
		typ, ok := states[s.StateID]
		if !ok {
			continue
		}
		at := s.StartedAt.UTC()
		last, lastTyp = &at, typ
		open := typ != "completed" && typ != "canceled" && typ != "duplicate"
		if wasDone && open {
			t.Reopened++
		}
		wasDone = typ == "completed"
		if at := s.StartedAt.UTC(); typ == "started" && (started == nil || at.Before(*started)) {
			started = &at
		}
	}
	t.StartedAt = started

	// An issue in the trash is on its way to being deleted. It counts as
	// cancelled, whatever state it was left in.
	if n.Trashed != nil && *n.Trashed {
		t.State = record.TicketCancelled
	}
	switch t.State {
	case record.TicketDone:
		t.DoneAt = utc(n.CompletedAt)
		if lastTyp == "completed" {
			t.DoneAt = last
		}
		if t.DoneAt == nil {
			t.DoneAt = &t.UpdatedAt
		}
		t.ClosedAt = t.DoneAt
	case record.TicketCancelled:
		closedNow := lastTyp == "canceled" || lastTyp == "duplicate"
		if closedNow && !(n.Trashed != nil && *n.Trashed) {
			t.ClosedAt = last
			break
		}
		for _, at := range []*time.Time{n.CanceledAt, n.ArchivedAt, &t.UpdatedAt} {
			if t.ClosedAt = utc(at); t.ClosedAt != nil {
				break
			}
		}
	}

	var links []record.Link
	seen := map[string]bool{}
	for _, a := range n.Attachments.Nodes {
		from, ok := pullRequestOf(a.URL)
		if !ok || seen[from] {
			continue
		}
		seen[from] = true
		links = append(links, record.Link{Kind: record.LinkMentions, From: from, FromKind: record.KindChange,
			To: t.ID, ToKind: record.KindTicket, Found: "source"})
	}
	return t, links
}

var pullPath = regexp.MustCompile(`^/([A-Za-z0-9][A-Za-z0-9._-]*)/([A-Za-z0-9._-]+)/pull/([0-9]{1,9})(?:/.*)?$`)

// pullRequestOf returns the id of the change a pull request URL points at.
// Linear's GitHub integration attaches the pull requests of an issue to it,
// each with the URL of the pull request.
func pullRequestOf(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	m := pullPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", false
	}
	return record.ChangeID(record.GitHub, m[1]+"/"+m[2], n), true
}
