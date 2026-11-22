package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/jira"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

const (
	// jiraPage is the number of issues asked for on a page. Jira may answer
	// with fewer, and the read does not lean on the number.
	jiraPage = 100
	// jiraHistoryPage is the number of changes of one issue on a page.
	jiraHistoryPage = 100
	// jiraSlice is the stretch of update times one read covers. It is longer
	// than Linear's, because each slice of Jira is read some days wider than
	// it is. See jiraDay.
	jiraSlice = 14 * 24 * time.Hour
	// jiraOverlap is read again at the start of each sync. Jira says a search
	// may not show the newest updates at once.
	jiraOverlap = 10 * time.Minute
	// jiraFields are the fields of an issue the search asks for.
	jiraFields = "summary,status,issuetype,project,creator,labels,created,updated,resolution,resolutiondate"

	jiraMyself   = "/rest/api/3/myself"
	jiraProjects = "/rest/api/3/project/search"
	jiraStatuses = "/rest/api/3/status"
	jiraSearch   = "/rest/api/3/search/jql"
)

// The keys of the status categories of Jira. Every status is in one.
const (
	jiraNew     = "new"
	jiraStarted = "indeterminate"
	jiraDone    = "done"
)

// Jira syncs the tickets of a Jira Cloud site into a store. A ticket in Jira
// is called an issue.
type Jira struct {
	C   *jira.Client
	S   *store.Store
	Opt Options
	// Projects limits the read to the projects with these keys, such as ENG.
	// Empty reads every project the account can see.
	Projects []string
}

// jiraState is what a sync keeps of a site between runs.
type jiraState struct {
	ticketWindow
	// Projects are the project keys the read is limited to, sorted.
	Projects []string `json:"projects"`
}

// jiraStatus is what the adapter knows of a status of the site.
type jiraStatus struct {
	category string
	name     string
}

// jiraBacklog says whether a status is a backlog: work nobody has said is
// next. A "to do" status named backlog is one, and so is a category this
// version does not know.
func jiraBacklog(s jiraStatus) bool {
	switch s.category {
	case jiraNew:
		return strings.Contains(strings.ToLower(s.name), "backlog")
	case jiraStarted, jiraDone:
		return false
	}
	return true
}

type jiraSync struct {
	j        *Jira
	o        Options
	name     string
	sc       store.Scope
	st       jiraState
	size     int
	statuses map[string]jiraStatus // status id -> status
	res      SourceResult
	t        tracker
}

func (r *jiraSync) logf(format string, a ...any) {
	if r.o.Logf != nil {
		r.o.Logf("jira %s: "+format, append([]any{r.name}, a...)...)
	}
}

// Sync reads the site into the store. What was read before an error is
// stored, and the next sync goes on from there.
func (j *Jira) Sync(ctx context.Context) (SourceResult, error) {
	r := &jiraSync{j: j, o: j.Opt.withDefaults(), size: jiraPage}
	r.res.Source = record.Jira
	r.t = tracker{o: r.o, source: record.Jira, name: func() string { return r.name }}
	began := time.Now()
	err := r.sync(ctx, began)
	r.res.Name, r.res.From = r.name, r.st.Floor
	if r.name != "" {
		if n, cerr := j.S.Count(store.Tickets.Name, record.Jira, r.name); cerr == nil {
			r.res.Tickets.Total = n
		}
		if keys, kerr := storedKeys(j.S, record.Jira, r.name); kerr == nil {
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
		return r.res, named(record.Jira, err)
	}
	return r.res, nil
}

func (r *jiraSync) save() error {
	b, err := json.Marshal(r.st)
	if err != nil {
		return err
	}
	r.sc.Source, r.sc.Name, r.sc.Private, r.sc.State = record.Jira, r.name, true, b
	r.sc.From = r.st.Floor
	return r.j.S.PutScope(r.sc)
}

// count writes a number with the word for it: 1 ticket, 2 tickets.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// named puts the name of a source before an error, unless the error starts
// with it already, as the errors of its client do.
func named(source string, err error) error {
	if strings.HasPrefix(strings.ToLower(err.Error()), source+" ") {
		return err
	}
	return fmt.Errorf("%s: %w", source, err)
}

var jiraProjectKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func (r *jiraSync) sync(ctx context.Context, began time.Time) error {
	// A search of Jira answers a wrong token with what anyone may see, which
	// is often nothing. So the account is checked first, on a path that says
	// no to a wrong token.
	var me struct {
		AccountID string `json:"accountId"`
	}
	if err := r.j.C.Get(ctx, jiraMyself, nil, &me); err != nil {
		if errors.Is(err, jira.ErrNotFound) {
			return fmt.Errorf("%s does not answer as a Jira Cloud site does: %w", r.j.C.Site(), err)
		}
		return err
	}
	if me.AccountID == "" {
		return fmt.Errorf("%s does not answer as a Jira Cloud site does", r.j.C.Site())
	}
	r.name = r.j.C.Site()
	projects := teamKeys(r.j.Projects)
	if err := r.checkProjects(ctx, projects); err != nil {
		return err
	}

	sc, known := r.j.S.Scope(record.Jira, r.name)
	if known && len(sc.State) > 0 {
		if err := json.Unmarshal(sc.State, &r.st); err != nil {
			return fmt.Errorf("the saved place of the sync cannot be read: %w", err)
		}
	}
	if known && !sameKeys(teamKeys(r.st.Projects), projects) {
		// The tickets of a project that is no longer read would stay as they
		// were at the last sync for good, open ones as open. So the site is
		// read again from nothing.
		r.logf("the list of projects changed. The stored tickets are dropped and read again")
		r.t.note("the list of projects changed. The stored tickets are dropped and read again")
		r.res.Again, known = true, false
	}
	if !known {
		// This also clears what a drop that was cut short left behind.
		if err := r.j.S.DropScope(record.Jira, r.name); err != nil {
			return fmt.Errorf("%q, the host of the Jira site, cannot name a scope in the store: %w", r.name, err)
		}
		sc, r.st = store.Scope{}, jiraState{}
	}
	r.sc, r.st.Projects = sc, projects
	first := r.sc.SyncedAt.IsZero()
	r.t.emit(Event{Kind: ScopeStarted, First: first, Last: time.Duration(r.sc.Took * float64(time.Second)), LastFull: r.sc.TookFull, LastAt: r.sc.SyncedAt})
	r.t.start(StepTickets)

	if err := r.loadStatuses(ctx); err != nil {
		return err
	}

	read := windowRead{o: r.o, w: &r.st.ticketWindow, slice: jiraSlice, overlap: jiraOverlap, save: r.save,
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

// checkProjects fails when a project key that was asked for names no project.
// Without the check a wrong key gives a report with no tickets and no reason.
func (r *jiraSync) checkProjects(ctx context.Context, projects []string) error {
	for _, k := range projects {
		if !jiraProjectKey.MatchString(k) {
			return fmt.Errorf("%q is not the key of a Jira project. A key looks like ENG", k)
		}
	}
	have := map[string]bool{}
	// Jira takes up to 50 keys in one request.
	for i := 0; i < len(projects); i += 50 {
		q := url.Values{"maxResults": {"50"}}
		for _, k := range projects[i:min(i+50, len(projects))] {
			q.Add("keys", k)
		}
		var out struct {
			Values []struct {
				Key string `json:"key"`
			} `json:"values"`
		}
		if err := r.j.C.Get(ctx, jiraProjects, q, &out); err != nil {
			return err
		}
		for _, p := range out.Values {
			have[strings.ToUpper(p.Key)] = true
		}
	}
	var missing []string
	for _, k := range projects {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the site %s has no project with the key %s that this account can read", r.name, strings.Join(missing, ", "))
	}
	return nil
}

// jiraStatusNode is a status as Jira sends it, on its own and on an issue.
type jiraStatusNode struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

func (r *jiraSync) learn(s jiraStatusNode) {
	if s.ID != "" && s.StatusCategory.Key != "" {
		r.statuses[s.ID] = jiraStatus{category: s.StatusCategory.Key, name: s.Name}
	}
}

// loadStatuses reads the statuses of the site. The history of an issue names
// a status by its id, and the category of the status says what a move into it
// means. The status an issue is in is learned from the issue as well, for a
// status this list leaves out.
func (r *jiraSync) loadStatuses(ctx context.Context) error {
	r.statuses = map[string]jiraStatus{}
	var out []jiraStatusNode
	if err := r.j.C.Get(ctx, jiraStatuses, nil, &out); err != nil {
		return err
	}
	for _, s := range out {
		r.learn(s)
	}
	return nil
}

// jiraDay writes a time as a day, some days off. Jira reads a time in a
// search in the time zone of the account, which a client cannot see. So a
// read asks for whole days: from a day before its first, up to two days after
// its last. That holds the stretch in every time zone, and what is read
// beyond it is stored as it is.
func jiraDay(t time.Time, days int) string {
	return t.UTC().Truncate(24*time.Hour).AddDate(0, 0, days).Format("2006-01-02")
}

// jql is the search for the issues updated in [from, to). A zero bound is no
// bound. A search of Jira must have a bound, and every search here has one.
func (r *jiraSync) jql(from, to time.Time, open bool) string {
	var parts []string
	if len(r.st.Projects) > 0 {
		parts = append(parts, `project in ("`+strings.Join(r.st.Projects, `", "`)+`")`)
	}
	if !from.IsZero() {
		parts = append(parts, `updated >= "`+jiraDay(from, -1)+`"`)
	}
	if !to.IsZero() {
		parts = append(parts, `updated < "`+jiraDay(to, 2)+`"`)
	}
	if open {
		parts = append(parts, "statusCategory != Done")
	}
	return strings.Join(parts, " AND ") + " ORDER BY updated ASC"
}

// span reads every ticket updated in [from, to) and stores it. A zero bound
// is no bound. With open set it reads only the tickets that are not done. It
// does not lean on the order Jira returns a page in.
func (r *jiraSync) span(ctx context.Context, from, to time.Time, open bool) error {
	var tickets []record.Ticket
	flush := func() error {
		added, changed, err := store.Tickets.Put(r.j.S, record.Jira, r.name, tickets)
		r.res.Tickets.Added += added
		r.res.Tickets.Changed += changed
		tickets = tickets[:0]
		return err
	}
	fail := func(err error) error {
		// The tickets read so far are good, so keep them. The slice is read
		// again by the next sync, and they are found unchanged.
		if ferr := flush(); ferr != nil && ctx.Err() == nil {
			return ferr
		}
		return err
	}
	q := url.Values{"jql": {r.jql(from, to, open)}, "fields": {jiraFields}, "expand": {"changelog"}}
	token := ""
	for pages := 1; ; pages++ {
		var out struct {
			Issues        []jiraIssue `json:"issues"`
			NextPageToken string      `json:"nextPageToken"`
			IsLast        bool        `json:"isLast"`
		}
		q.Set("maxResults", strconv.Itoa(r.size))
		err := r.j.C.Get(ctx, jiraSearch, q, &out)
		if errors.Is(err, jira.ErrServer) && r.size > 1 && ctx.Err() == nil {
			r.size = (r.size + 1) / 2
			r.logf("jira could not answer a page of tickets, retrying with %d per page", r.size)
			r.t.note(fmt.Sprintf("Jira could not answer a page of tickets, retrying with %d per page", r.size))
			pages--
			continue
		}
		if err != nil {
			return fail(err)
		}
		r.t.advance(len(out.Issues), 0, time.Time{})
		for i := range out.Issues {
			r.learn(out.Issues[i].Fields.Status)
		}
		for i := range out.Issues {
			n := &out.Issues[i]
			// An epic holds other tickets and stays open while they are
			// worked on. It is not a piece of work, so it is left out.
			if n.Fields.IssueType.HierarchyLevel > 0 {
				continue
			}
			history, err := r.history(ctx, n)
			if err != nil {
				return fail(err)
			}
			tickets = append(tickets, jiraRecord(r.j.C, n, history, r.statuses))
		}
		switch next := out.NextPageToken; {
		case next == "" || out.IsLast:
			return flush()
		case next == token:
			return fail(errors.New("jira answered with the same page twice"))
		default:
			token = next
			q.Set("nextPageToken", token)
		}
		if pages%r.o.Checkpoint == 0 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

var jiraIssueID = regexp.MustCompile(`^[0-9]{1,18}$`)

// history returns every change of an issue. A search carries the newest
// changes of each issue. When an issue has more than the search carried, all
// of them are read from the issue, a page at a time.
func (r *jiraSync) history(ctx context.Context, n *jiraIssue) ([]jiraChange, error) {
	if !jiraIssueID.MatchString(n.ID) {
		return nil, fmt.Errorf("jira sent an issue with the id %q, which is not a number", n.ID)
	}
	if c := n.Changelog; c != nil && c.Total <= len(c.Histories) {
		return c.Histories, nil
	}
	var all []jiraChange
	for {
		var page struct {
			Values []jiraChange `json:"values"`
			Total  int          `json:"total"`
			IsLast bool         `json:"isLast"`
		}
		q := url.Values{"startAt": {strconv.Itoa(len(all))}, "maxResults": {strconv.Itoa(jiraHistoryPage)}}
		err := r.j.C.Get(ctx, "/rest/api/3/issue/"+n.ID+"/changelog", q, &page)
		if errors.Is(err, jira.ErrNotFound) {
			// The issue went away between the two reads. What the search
			// carried is what there is.
			if n.Changelog != nil {
				return n.Changelog.Histories, nil
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		all = append(all, page.Values...)
		// A page that does not say how many there are is not the last one
		// for that reason alone.
		if len(page.Values) == 0 || page.IsLast || (page.Total > 0 && len(all) >= page.Total) {
			return all, nil
		}
	}
}

// jiraTime is a time as Jira writes it: 2026-11-01T10:00:00.000+0000, with no
// colon in the offset.
type jiraTime struct{ time.Time }

var jiraTimeLayouts = []string{"2006-01-02T15:04:05.999999999Z0700", time.RFC3339Nano}

func (t *jiraTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	for _, layout := range jiraTimeLayouts {
		if at, err := time.Parse(layout, s); err == nil {
			t.Time = at.UTC()
			return nil
		}
	}
	return fmt.Errorf("%q is not a time as Jira writes one", s)
}

// jiraChange is one entry of the history of an issue: the fields that changed
// at one moment.
type jiraChange struct {
	ID      string   `json:"id"`
	Created jiraTime `json:"created"`
	Items   []struct {
		Field      string `json:"field"`
		FieldType  string `json:"fieldtype"`
		From       string `json:"from"`
		FromString string `json:"fromString"`
		To         string `json:"to"`
	} `json:"items"`
}

// jiraIssue is an issue as the search returns it. Jira sends the email of an
// account next to its name when the account allows it. It is not read.
type jiraIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary        string         `json:"summary"`
		Created        jiraTime       `json:"created"`
		Updated        jiraTime       `json:"updated"`
		ResolutionDate *jiraTime      `json:"resolutiondate"`
		Status         jiraStatusNode `json:"status"`
		Labels         []string       `json:"labels"`
		IssueType      struct {
			HierarchyLevel int `json:"hierarchyLevel"`
		} `json:"issuetype"`
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
		Creator *struct {
			DisplayName string `json:"displayName"`
			AccountType string `json:"accountType"`
		} `json:"creator"`
		Resolution *struct {
			Name string `json:"name"`
		} `json:"resolution"`
	} `json:"fields"`
	Changelog *struct {
		Total     int          `json:"total"`
		Histories []jiraChange `json:"histories"`
	} `json:"changelog"`
}

// jiraNotDone are the names of the resolutions and statuses that close a
// ticket without the work being done. Jira files them under done, and a site
// can add its own, which then count as done.
var jiraNotDone = map[string]bool{
	"wont do": true, "wont fix": true, "duplicate": true, "cannot reproduce": true, "cant reproduce": true,
	"declined": true, "cancelled": true, "canceled": true, "rejected": true, "invalid": true, "obsolete": true,
	"not a bug": true, "incomplete": true, "abandoned": true, "not planned": true,
}

// notDone says whether a resolution or a status of the done category means
// the ticket was dropped. Won't Do and WONT DO read the same.
func notDone(name string) bool {
	name = strings.NewReplacer("'", "", "’", "").Replace(strings.ToLower(strings.TrimSpace(name)))
	return jiraNotDone[strings.Join(strings.Fields(name), " ")]
}

var jiraKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// jiraRecord turns an issue and its history into a ticket.
func jiraRecord(c *jira.Client, n *jiraIssue, history []jiraChange, statuses map[string]jiraStatus) record.Ticket {
	f := &n.Fields
	t := record.Ticket{
		// The id of an issue is a number that is only unique on its site.
		Source: record.Jira, ID: record.TicketID(record.Jira, c.Site()+"/"+n.ID), Key: n.Key,
		Aliases: []string{}, Project: f.Project.Key, URL: c.Browse(n.Key), Title: f.Summary, Labels: []string{},
		Author: record.Actor{Kind: record.ActorUser}, RawState: f.Status.Name,
		CreatedAt: f.Created.UTC(), UpdatedAt: f.Updated.UTC(),
	}
	t.Labels = append(t.Labels, f.Labels...)
	// The display name is the name people see in Jira. An account of the
	// type app belongs to an integration.
	if f.Creator != nil {
		t.Author.Login = f.Creator.DisplayName
		if f.Creator.AccountType == "app" {
			t.Author.Kind = record.ActorBot
		}
	}

	// The history says when work first started, also on an issue that went
	// back since, how often a done issue was opened again, when it was done,
	// and the keys it had before it moved to another project.
	sort.SliceStable(history, func(i, k int) bool { return history[i].Created.Before(history[k].Created.Time) })
	var started, done, ready *time.Time
	// inStatus says whether the history has shown a status yet: the first
	// change of status names the one the issue was made in.
	inStatus := false
	seen := map[string]bool{strings.ToUpper(n.Key): true}
	for _, h := range history {
		at := h.Created.UTC()
		for _, it := range h.Items {
			if it.FieldType == "custom" {
				continue
			}
			switch strings.ToLower(it.Field) {
			case "key":
				if k := strings.ToUpper(it.FromString); jiraKey.MatchString(k) && !seen[k] {
					seen[k] = true
					t.Aliases = append(t.Aliases, it.FromString)
				}
			case "status":
				was, wasKnown := statuses[it.From]
				now, known := statuses[it.To]
				if !known {
					continue
				}
				if !inStatus && wasKnown && !jiraBacklog(was) {
					ready = &t.CreatedAt
				}
				inStatus = true
				if ready == nil && !jiraBacklog(now) {
					r := at
					ready = &r
				}
				wasDone := wasKnown && was.category == jiraDone
				// An issue that leaves a started status it never moved into
				// was made in it.
				if wasKnown && was.category == jiraStarted && started == nil {
					started = &t.CreatedAt
				}
				switch {
				case now.category == jiraDone && !wasDone:
					done = &at
				case now.category != jiraDone:
					done = nil
					if wasDone && !notDone(was.name) {
						t.Reopened++
					}
				}
				if now.category == jiraStarted && started == nil {
					started = &at
				}
			}
		}
	}

	switch f.Status.StatusCategory.Key {
	case jiraStarted:
		t.State = record.TicketStarted
		// An issue that is being worked on and never moved was made that way.
		if started == nil {
			started = &t.CreatedAt
		}
	case jiraDone:
		t.State = record.TicketDone
		if (f.Resolution != nil && notDone(f.Resolution.Name)) || notDone(f.Status.Name) {
			t.State = record.TicketCancelled
		}
	case jiraNew:
		t.State = record.TicketUnstarted
		if strings.Contains(strings.ToLower(f.Status.Name), "backlog") {
			t.State = record.TicketBacklog
		}
	default:
		// A category this version does not know.
		t.State = record.TicketBacklog
	}
	t.StartedAt = started
	// An issue whose status never changed was made in the one it is in.
	if ready == nil && !inStatus && t.State != record.TicketBacklog {
		ready = &t.CreatedAt
	}
	if ready == nil && started != nil {
		ready = started
	}
	t.ReadyAt = ready

	if t.State == record.TicketDone || t.State == record.TicketCancelled {
		closed := &t.UpdatedAt
		switch {
		case done != nil:
			closed = done
		case f.ResolutionDate != nil && !f.ResolutionDate.IsZero():
			at := f.ResolutionDate.UTC()
			closed = &at
		}
		t.ClosedAt = closed
		if t.State == record.TicketDone {
			t.DoneAt = closed
		}
	}
	return t
}
