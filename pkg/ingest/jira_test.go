package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/jira"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// This file plays Jira Cloud for the sync tests. It writes times the way Jira
// does, with no colon in the offset. It reads a day in a search in the time
// zone of the account, as Jira does. It pages newest first whatever order the
// search asks for, so a test fails if the sync leans on the order of a page.
// An account it does not know gets an empty search and no error, as Jira
// answers anyone who is not signed in.

const (
	jiraEmail = "ada@example.com"
	jiraToken = "jira_fake_token_that_must_not_leak"
	jiraSite  = "127.0.0.1"
)

type jStatus struct{ id, name, category string }

var jiraFakeStatuses = []jStatus{
	{"1", "To Do", "new"}, {"2", "Backlog", "new"},
	{"3", "In Progress", "indeterminate"}, {"4", "In Review", "indeterminate"},
	{"5", "Done", "done"}, {"6", "Won't Do", "done"}, {"8", "Closed", "done"},
	{"7", "Odd", "undefined"},
}

type jIssue struct {
	id, key          string
	level            int
	created, updated time.Time
	status           string
	resolution       string
	resolved         *time.Time
	labels           []string
	creator          obj
	changes          []obj // oldest first
}

func (i *jIssue) project() string { return i.key[:strings.Index(i.key, "-")] }

type jCall struct {
	path string
	q    map[string][]string
}

type fakeJira struct {
	t        *testing.T
	mu       sync.Mutex
	zone     *time.Location
	projects []string
	issues   []*jIssue
	calls    []jCall
	next     int
	// embed is the number of changes a search carries with an issue, the
	// newest ones. Zero is 100. noChangelog leaves the list out.
	embed       int
	noChangelog bool
	// historyCap is the most changes a page of the history of an issue holds.
	historyCap int
	// noTotal leaves out of a page of the history how many changes there
	// are and whether it is the last page.
	noTotal bool
	// gone are the ids of the issues whose history Jira no longer finds.
	gone map[string]bool
	// cap is the most issues a page of a search holds, whatever was asked.
	cap int
	// failAbove makes a search for a bigger page fail on the side of Jira.
	failAbove int
	// loop makes every page of a search name the same next page.
	loop bool
	// myself is the answer about the account. Nil is a real one.
	myself   obj
	noMyself bool
	// onSearch runs before a search is answered, with the number of searches
	// asked for so far.
	onSearch func(n int, r *http.Request)
	searches int
	srv      *httptest.Server
}

func newJira(t *testing.T) *fakeJira {
	f := &fakeJira{t: t, zone: time.FixedZone("", 3*3600), projects: []string{"ENG", "OPS"}, next: 10000, gone: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJira) client() *jira.Client {
	c, err := jira.New(f.srv.URL, jiraEmail, jiraToken)
	if err != nil {
		f.t.Fatal(err)
	}
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

// jts writes a time as Jira does, in the time zone of the account.
func (f *fakeJira) jts(t time.Time) string {
	return t.In(f.zone).Format("2006-01-02T15:04:05.000-0700")
}

func (f *fakeJira) status(id string) jStatus {
	for _, s := range jiraFakeStatuses {
		if s.id == id {
			return s
		}
	}
	f.t.Fatalf("the fake has no status %q", id)
	return jStatus{}
}

// add keeps an issue. Its project is the first part of its key.
func (f *fakeJira) add(key, status string, created, updated time.Time) *jIssue {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	i := &jIssue{id: strconv.Itoa(f.next), key: key, created: created, updated: updated, status: status, labels: []string{},
		creator: obj{"accountId": "a1", "displayName": "Ada", "accountType": "atlassian", "emailAddress": "grace@example.com"}}
	if f.status(status).category == "done" {
		i.resolution, i.resolved = "Done", &updated
	}
	f.issues = append(f.issues, i)
	return i
}

func (f *fakeJira) change(i *jIssue, at time.Time, items ...obj) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i.changes = append(i.changes, obj{"id": strconv.Itoa(len(i.changes) + 1), "created": f.jts(at), "items": items})
	i.updated = at
}

// move takes an issue to a status at a time, and writes the change to its
// history.
func (f *fakeJira) move(i *jIssue, to string, at time.Time) {
	from, next := f.status(i.status), f.status(to)
	f.change(i, at, obj{"field": "status", "fieldtype": "jira", "fieldId": "status", "from": from.id, "fromString": from.name, "to": next.id, "toString": next.name})
	f.mu.Lock()
	defer f.mu.Unlock()
	i.status, i.resolution, i.resolved = to, "", nil
	if next.category == "done" {
		i.resolution, i.resolved = "Done", &at
		if next.name == "Won't Do" {
			i.resolution = "Won't Do"
		}
	}
}

func (f *fakeJira) take() []jCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func jOf(calls []jCall, path string) []jCall {
	var out []jCall
	for _, c := range calls {
		if c.path == path {
			out = append(out, c)
		}
	}
	return out
}

func (c jCall) get(k string) string {
	if v := c.q[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func jqls(calls []jCall) []string {
	var out []string
	for _, c := range jOf(calls, jiraSearch) {
		out = append(out, c.get("jql"))
	}
	return out
}

func jErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(obj{"errorMessages": []string{msg}, "errors": obj{}})
}

func (f *fakeJira) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		f.t.Errorf("the sync sent a %s to %s. It must only read", r.Method, r.URL.Path)
		jErr(w, http.StatusMethodNotAllowed, "read only")
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, jCall{path: r.URL.Path, q: r.URL.Query()})
	hook, n := f.onSearch, 0
	if r.URL.Path == jiraSearch {
		f.searches++
		n = f.searches
	}
	f.mu.Unlock()
	if hook != nil && n > 0 {
		hook(n, r)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	user, pass, ok := r.BasicAuth()
	known := ok && user == jiraEmail && pass == jiraToken
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/json")
	switch p := r.URL.Path; {
	case p == jiraMyself:
		switch {
		case f.noMyself:
			jErr(w, http.StatusNotFound, "no such page")
		case !known:
			jErr(w, http.StatusUnauthorized, "Client must be authenticated to access this resource.")
		case f.myself != nil:
			json.NewEncoder(w).Encode(f.myself)
		default:
			json.NewEncoder(w).Encode(obj{"accountId": "a1", "displayName": "Ada", "emailAddress": jiraEmail})
		}
	case p == jiraProjects:
		if len(q["keys"]) > 50 {
			f.t.Errorf("%d project keys in one request. Jira takes 50", len(q["keys"]))
			jErr(w, http.StatusBadRequest, "too many keys")
			return
		}
		values := []obj{}
		for _, k := range f.projects {
			if known && (len(q["keys"]) == 0 || among2(q["keys"], k)) {
				values = append(values, obj{"id": "1", "key": k, "name": k})
			}
		}
		json.NewEncoder(w).Encode(obj{"values": values, "isLast": true, "total": len(values)})
	case p == jiraStatuses:
		out := []obj{}
		for _, s := range jiraFakeStatuses {
			// Closed is a status the list leaves out. It is learned from an
			// issue that is in it.
			if s.name != "Closed" {
				out = append(out, obj{"id": s.id, "name": s.name, "statusCategory": obj{"id": 1, "key": s.category, "name": s.category}})
			}
		}
		json.NewEncoder(w).Encode(out)
	case p == jiraSearch:
		f.search(w, q, known)
	case strings.HasPrefix(p, "/rest/api/3/issue/") && strings.HasSuffix(p, "/changelog"):
		f.history(w, strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/3/issue/"), "/changelog"), q)
	default:
		f.t.Errorf("the sync asked for %s", p)
		jErr(w, http.StatusNotFound, "no such page")
	}
}

func among2(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

var (
	jqlProject = regexp.MustCompile(`^project in \(("[A-Z][A-Z0-9_]*"(, "[A-Z][A-Z0-9_]*")*)\)$`)
	jqlFrom    = regexp.MustCompile(`^updated >= "([0-9]{4}-[0-9]{2}-[0-9]{2})"$`)
	jqlTo      = regexp.MustCompile(`^updated < "([0-9]{4}-[0-9]{2}-[0-9]{2})"$`)
)

type jCond struct {
	projects []string
	from, to time.Time
	open     bool
}

// parseJQL reads the searches the sync is known to send, and refuses any
// other. A day is the start of that day in the time zone of the account.
func (f *fakeJira) parseJQL(jql string) (jCond, error) {
	var c jCond
	body, order, ok := strings.Cut(jql, " ORDER BY ")
	if !ok || order != "updated ASC" {
		return c, fmt.Errorf("the search %q does not end in ORDER BY updated ASC", jql)
	}
	if strings.TrimSpace(body) == "" {
		return c, errors.New("Unbounded JQL queries are not allowed here. Please add a search restriction to your query.")
	}
	for _, clause := range strings.Split(body, " AND ") {
		switch {
		case jqlProject.MatchString(clause):
			for _, k := range strings.Split(jqlProject.FindStringSubmatch(clause)[1], ", ") {
				c.projects = append(c.projects, strings.Trim(k, `"`))
			}
		case jqlFrom.MatchString(clause):
			at, err := time.ParseInLocation("2006-01-02", jqlFrom.FindStringSubmatch(clause)[1], f.zone)
			if err != nil {
				return c, err
			}
			c.from = at
		case jqlTo.MatchString(clause):
			at, err := time.ParseInLocation("2006-01-02", jqlTo.FindStringSubmatch(clause)[1], f.zone)
			if err != nil {
				return c, err
			}
			c.to = at
		case clause == "statusCategory != Done":
			c.open = true
		default:
			return c, fmt.Errorf("the fake cannot read the clause %q", clause)
		}
	}
	return c, nil
}

func (f *fakeJira) search(w http.ResponseWriter, q map[string][]string, known bool) {
	get := func(k string) string { return jCall{q: q}.get(k) }
	c, err := f.parseJQL(get("jql"))
	if err != nil {
		f.t.Errorf("search: %v", err)
		jErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if get("fields") != jiraFields || get("expand") != "changelog" {
		f.t.Errorf("search asked for the fields %q and to expand %q", get("fields"), get("expand"))
	}
	size, err := strconv.Atoi(get("maxResults"))
	if err != nil || size < 1 {
		f.t.Errorf("search asked for %q results", get("maxResults"))
		jErr(w, http.StatusBadRequest, "maxResults")
		return
	}
	if f.failAbove > 0 && size > f.failAbove {
		jErr(w, http.StatusGatewayTimeout, "the search took too long")
		return
	}
	if f.cap > 0 && size > f.cap {
		size = f.cap
	}
	var hit []*jIssue
	for _, i := range f.issues {
		switch {
		case !known:
		case len(c.projects) > 0 && !among2(c.projects, i.project()):
		case !c.from.IsZero() && i.updated.Before(c.from):
		case !c.to.IsZero() && !i.updated.Before(c.to):
		case c.open && f.status(i.status).category == "done":
		default:
			hit = append(hit, i)
		}
	}
	sort.SliceStable(hit, func(a, b int) bool { return hit[a].updated.After(hit[b].updated) })
	start := 0
	if tok := get("nextPageToken"); tok != "" && !f.loop {
		if start, err = strconv.Atoi(strings.TrimPrefix(tok, "p")); err != nil || !strings.HasPrefix(tok, "p") {
			f.t.Errorf("search sent the page token %q", tok)
		}
	}
	end := min(start+size, len(hit))
	issues := []obj{}
	for _, i := range hit[min(start, len(hit)):end] {
		issues = append(issues, f.node(i))
	}
	out := obj{"issues": issues, "isLast": end >= len(hit)}
	if end < len(hit) {
		out["nextPageToken"] = "p" + strconv.Itoa(end)
	}
	if f.loop {
		out["nextPageToken"], out["isLast"] = "same", false
	}
	json.NewEncoder(w).Encode(out)
}

func (f *fakeJira) node(i *jIssue) obj {
	st := f.status(i.status)
	fields := obj{
		"summary": "Title of " + i.key, "created": f.jts(i.created), "updated": f.jts(i.updated),
		"resolutiondate": nil, "resolution": nil,
		"status":    obj{"id": st.id, "name": st.name, "statusCategory": obj{"id": 1, "key": st.category, "name": st.category}},
		"labels":    i.labels,
		"issuetype": obj{"id": "10001", "name": "Task", "hierarchyLevel": i.level, "subtask": i.level < 0},
		"project":   obj{"id": "10000", "key": i.project()},
		"creator":   i.creator,
	}
	if i.resolution != "" {
		fields["resolution"] = obj{"id": "1", "name": i.resolution}
	}
	if i.resolved != nil {
		fields["resolutiondate"] = f.jts(*i.resolved)
	}
	n := obj{"id": i.id, "key": i.key, "self": f.srv.URL + "/rest/api/3/issue/" + i.id, "fields": fields}
	if !f.noChangelog {
		embed := f.embed
		if embed == 0 {
			embed = 100
		}
		// Newest first, and cut, as Jira does.
		newest := []obj{}
		for k := len(i.changes) - 1; k >= 0 && len(newest) < embed; k-- {
			newest = append(newest, i.changes[k])
		}
		n["changelog"] = obj{"startAt": 0, "maxResults": len(newest), "total": len(i.changes), "histories": newest}
	}
	return n
}

func (f *fakeJira) history(w http.ResponseWriter, id string, q map[string][]string) {
	var found *jIssue
	for _, i := range f.issues {
		if i.id == id {
			found = i
		}
	}
	if found == nil || f.gone[id] {
		jErr(w, http.StatusNotFound, "Issue does not exist or you do not have permission to see it.")
		return
	}
	get := func(k string) string { return jCall{q: q}.get(k) }
	start, _ := strconv.Atoi(get("startAt"))
	size, _ := strconv.Atoi(get("maxResults"))
	if size < 1 {
		f.t.Errorf("history asked for %q results", get("maxResults"))
		size = 100
	}
	if f.historyCap > 0 && size > f.historyCap {
		size = f.historyCap
	}
	end := min(start+size, len(found.changes))
	values := []obj{}
	if start < end {
		values = found.changes[start:end]
	}
	page := obj{"startAt": start, "maxResults": size, "values": values}
	if !f.noTotal {
		page["total"], page["isLast"] = len(found.changes), end >= len(found.changes)
	}
	json.NewEncoder(w).Encode(page)
}

func jiraSyncer(f *fakeJira, s *store.Store, opt Options, projects ...string) *Jira {
	if opt.Now.IsZero() {
		opt.Now = now
	}
	if opt.Since.IsZero() {
		opt.Since = floor
	}
	return &Jira{C: f.client(), S: s, Opt: opt, Projects: projects}
}

func jiraTickets(t *testing.T, s *store.Store) map[string]record.Ticket {
	t.Helper()
	out := map[string]record.Ticket{}
	err := store.Tickets.Each(s, record.Jira, jiraSite, time.Time{}, time.Time{}, func(_ string, rows []record.Ticket) error {
		for _, r := range rows {
			if _, twice := out[r.Key]; twice {
				t.Errorf("the store holds %s twice", r.Key)
			}
			out[r.Key] = r
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func jiraSaved(t *testing.T, s *store.Store) (store.Scope, jiraState) {
	t.Helper()
	sc, ok := s.Scope(record.Jira, jiraSite)
	if !ok {
		t.Fatal("the store has no jira scope")
	}
	var st jiraState
	if err := json.Unmarshal(sc.State, &st); err != nil {
		t.Fatal(err)
	}
	return sc, st
}

func eqTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func TestJiraFirstSyncStoresTickets(t *testing.T) {
	f := newJira(t)
	// Updated inside the window.
	one := f.add("ENG-1", "1", day(1), day(1))
	one.labels = []string{"bug", "ui"}
	f.move(one, "3", day(2))
	f.move(one, "5", day(3))
	two := f.add("ENG-2", "1", day(5), day(5))
	f.move(two, "3", day(40))
	bot := f.add("OPS-1", "1", day(60), day(60))
	bot.creator = obj{"accountId": "b1", "displayName": "Automation", "accountType": "app"}
	f.move(bot, "6", day(61))
	// A subtask is a piece of work. An epic is not.
	f.add("ENG-6", "2", day(50), day(50)).level = -1
	f.add("ENG-5", "3", day(2), day(3)).level = 1
	// Last updated before the window: one still open, one closed.
	f.add("ENG-3", "1", day(-80), day(-60))
	f.add("ENG-4", "5", day(-80), day(-60))
	// Updated just before the sync started, and while it ran.
	f.add("ENG-7", "1", day(90), now.Add(-time.Second))
	f.add("ENG-8", "1", day(90), now.Add(30*time.Second))

	dir := t.TempDir()
	s := openStore(t, dir)
	res, err := jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != record.Jira || res.Name != jiraSite || !res.First || res.Again || !res.From.Equal(floor) {
		t.Errorf("result = %+v", res)
	}
	if res.Tickets.Added != 7 || res.Tickets.Changed != 0 || res.Tickets.Total != 7 || res.Links != (Count{}) {
		t.Errorf("tickets = %+v, links = %+v, want 7 new tickets and no links", res.Tickets, res.Links)
	}
	got := jiraTickets(t, s)
	if k := lKeys(got); !reflect.DeepEqual(k, []string{"ENG-1", "ENG-2", "ENG-3", "ENG-6", "ENG-7", "ENG-8", "OPS-1"}) {
		t.Fatalf("stored %v", k)
	}

	d1, d2, d3 := day(1), day(2), day(3)
	want := record.Ticket{
		Source: record.Jira, ID: "jira:127.0.0.1/" + one.id, Key: "ENG-1", Aliases: []string{}, Project: "ENG",
		URL: f.srv.URL + "/browse/ENG-1", Title: "Title of ENG-1", Labels: []string{"bug", "ui"},
		Author: record.Actor{Login: "Ada", Kind: record.ActorUser}, State: record.TicketDone, RawState: "Done",
		CreatedAt: day(1), UpdatedAt: day(3), ReadyAt: &d1, StartedAt: &d2, DoneAt: &d3, ClosedAt: &d3,
	}
	if g := got["ENG-1"]; !reflect.DeepEqual(g, want) {
		t.Errorf("ENG-1 = %+v\nwant    %+v", g, want)
	}
	if g := got["ENG-2"]; g.State != record.TicketStarted || !eqTime(g.StartedAt, ptr(day(40))) || g.DoneAt != nil || g.ClosedAt != nil {
		t.Errorf("ENG-2 = %+v", g)
	}
	if g := got["OPS-1"]; g.State != record.TicketCancelled || g.DoneAt != nil || !eqTime(g.ClosedAt, ptr(day(61))) || g.Author != (record.Actor{Login: "Automation", Kind: record.ActorBot}) || g.Project != "OPS" || g.RawState != "Won't Do" {
		t.Errorf("OPS-1 = %+v", g)
	}
	if g := got["ENG-6"]; g.State != record.TicketBacklog || g.StartedAt != nil {
		t.Errorf("ENG-6 = %+v", g)
	}
	if g := got["ENG-3"]; g.State != record.TicketUnstarted || !g.UpdatedAt.Equal(day(-60)) {
		t.Errorf("ENG-3 = %+v", g)
	}

	sc, st := jiraSaved(t, s)
	if !sc.Private || !sc.From.Equal(floor) || !sc.SyncedAt.Equal(now) || len(sc.Notes) != 0 {
		t.Errorf("scope = %+v", sc)
	}
	if !st.Floor.Equal(floor) || !st.High.Equal(now) || !st.Open || len(st.Projects) != 0 {
		t.Errorf("state = %+v", st)
	}
	if string(sc.State) != `{"floor":"2026-08-01T00:00:00Z","high":"2026-11-02T12:00:00Z","open":true,"rev":1,"projects":[]}` {
		t.Errorf("saved state = %s", sc.State)
	}

	// The account is checked first, then the statuses are read, then seven
	// slices of 14 days and the open tickets from before the window. Each
	// search asks for whole days, a day wider below and two wider above.
	calls := f.take()
	var paths []string
	for _, c := range calls[:3] {
		paths = append(paths, c.path)
	}
	if !reflect.DeepEqual(paths, []string{jiraMyself, jiraStatuses, jiraSearch}) || len(jOf(calls, jiraProjects)) != 0 {
		t.Errorf("the first requests = %v", paths)
	}
	wantJQL := []string{
		`updated >= "2026-07-31" AND updated < "2026-08-17" ORDER BY updated ASC`,
		`updated >= "2026-08-14" AND updated < "2026-08-31" ORDER BY updated ASC`,
		`updated >= "2026-08-28" AND updated < "2026-09-14" ORDER BY updated ASC`,
		`updated >= "2026-09-11" AND updated < "2026-09-28" ORDER BY updated ASC`,
		`updated >= "2026-09-25" AND updated < "2026-10-12" ORDER BY updated ASC`,
		`updated >= "2026-10-09" AND updated < "2026-10-26" ORDER BY updated ASC`,
		`updated >= "2026-10-23" ORDER BY updated ASC`,
		`updated < "2026-08-03" AND statusCategory != Done ORDER BY updated ASC`,
	}
	if got := jqls(calls); !reflect.DeepEqual(got, wantJQL) {
		t.Errorf("searches:\n%s", strings.Join(got, "\n"))
	}
	if len(calls) != 2+len(wantJQL) {
		t.Errorf("%d requests, want %d", len(calls), 2+len(wantJQL))
	}
	if c := jOf(calls, jiraSearch)[0]; c.get("maxResults") != "100" {
		t.Errorf("page size = %s", c.get("maxResults"))
	}

	// Neither the token nor an email is written to the store.
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Error(rerr)
		}
		for _, secret := range []string{jiraToken, jiraEmail, "grace@example.com"} {
			if bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s holds %q", path, secret)
			}
		}
		return nil
	})
}

func ptr(t time.Time) *time.Time { return &t }

// Jira reads a day in a search in the time zone of the account. Whatever the
// zone, every ticket updated in the window is stored.
func TestJiraNoTicketIsMissedInAnyTimeZone(t *testing.T) {
	zones := map[string]int{"UTC-12": -12 * 3600, "UTC-8": -8 * 3600, "UTC": 0, "UTC+5:30": 5*3600 + 1800, "UTC+14": 14 * 3600}
	for name, offset := range zones {
		t.Run(name, func(t *testing.T) {
			f := newJira(t)
			f.zone = time.FixedZone("", offset)
			var want []string
			// Done tickets, so the read of open tickets cannot be what finds them.
			for n, at := range []time.Time{
				floor, floor.Add(time.Second), floor.Add(time.Hour), floor.Add(11 * time.Hour), floor.Add(13 * time.Hour), floor.Add(25 * time.Hour),
				day(14).Add(-time.Second), day(14), day(14).Add(time.Second), day(14).Add(13 * time.Hour), day(15.99),
				day(28).Add(-13 * time.Hour), day(84).Add(-time.Second), day(84), day(92),
				now.Add(-25 * time.Hour), now.Add(-13 * time.Hour), now.Add(-time.Second), now,
			} {
				key := fmt.Sprintf("ENG-%d", n+1)
				f.add(key, "5", day(-100), at)
				want = append(want, key)
			}
			// Open tickets from before the window, up to its edge.
			for n, at := range []time.Time{floor.Add(-time.Second), floor.Add(-13 * time.Hour), floor.Add(-25 * time.Hour), day(-400)} {
				key := fmt.Sprintf("OPS-%d", n+1)
				f.add(key, "3", day(-500), at)
				want = append(want, key)
			}
			// Done long before the window: not wanted.
			f.add("ENG-900", "5", day(-100), day(-30))
			sort.Strings(want)

			s := openStore(t, t.TempDir())
			if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, want) {
				t.Fatalf("stored %v\nwant   %v", k, want)
			}

			// Later syncs, each a little after the last. Tickets are done at
			// awkward times in between.
			at := now
			for round, steps := range [][]time.Duration{
				{time.Second, 3 * time.Hour},
				{-9 * time.Minute, 11 * time.Hour, 13 * time.Hour, 36 * time.Hour},
				{time.Minute},
			} {
				for n, d := range steps {
					key := fmt.Sprintf("ENG-%d", 100*(round+1)+n)
					f.add(key, "5", day(-100), at.Add(d))
					want = append(want, key)
				}
				at = at.Add(steps[len(steps)-1] + time.Minute)
				if _, err := jiraSyncer(f, s, Options{Now: at}).Sync(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			sort.Strings(want)
			if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, want) {
				t.Errorf("after later syncs stored %v\nwant %v", k, want)
			}
		})
	}
}

func TestJiraNextSyncReadsOnlyWhatChanged(t *testing.T) {
	f := newJira(t)
	one := f.add("ENG-1", "1", day(1), day(1))
	f.move(one, "3", day(2))
	// Updated on the day of the sync, so the next search reads it again. It
	// did not change, and must not count as changed.
	f.add("ENG-2", "1", day(5), now.Add(-2*time.Hour))
	f.add("ENG-3", "1", day(-80), day(-60))
	dir := t.TempDir()
	s := openStore(t, dir)
	if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.take()

	f.move(one, "5", now.Add(time.Hour))
	f.add("ENG-9", "1", now.Add(time.Hour), now.Add(time.Hour))
	// Closed before the window opened, and updated since: an open ticket
	// from before the window that is now done.
	f.move(f.issues[2], "5", now.Add(90*time.Minute))
	later := now.Add(2 * time.Hour)
	res, err := jiraSyncer(f, s, Options{Now: later}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.First || res.Again || res.Tickets.Added != 1 || res.Tickets.Changed != 2 || res.Tickets.Total != 4 {
		t.Errorf("result = %+v", res)
	}
	calls := f.take()
	// One search from ten minutes before the last sync. The open tickets from
	// before the window are not read again.
	if got := jqls(calls); !reflect.DeepEqual(got, []string{`updated >= "2026-11-01" ORDER BY updated ASC`}) {
		t.Errorf("searches = %q", got)
	}
	if len(calls) != 3 {
		t.Errorf("%d requests, want 3", len(calls))
	}
	got := jiraTickets(t, s)
	if g := got["ENG-1"]; g.State != record.TicketDone || !eqTime(g.DoneAt, ptr(now.Add(time.Hour))) || !eqTime(g.StartedAt, ptr(day(2))) {
		t.Errorf("ENG-1 = %+v", g)
	}
	if g := got["ENG-3"]; g.State != record.TicketDone {
		t.Errorf("ENG-3 = %+v", g)
	}
	sc, st := jiraSaved(t, s)
	if !sc.SyncedAt.Equal(later) || !st.High.Equal(later) || !st.Floor.Equal(floor) {
		t.Errorf("synced = %v, state = %+v", sc.SyncedAt, st)
	}

	// Nothing changed: nothing is written.
	res, err = jiraSyncer(f, s, Options{Now: later.Add(time.Hour)}).Sync(context.Background())
	if err != nil || res.Tickets.Added != 0 || res.Tickets.Changed != 0 || res.Tickets.Total != 4 {
		t.Errorf("result = %+v, error = %v", res, err)
	}
}

func TestJiraAnEarlierFloorReadsTheStretchBelow(t *testing.T) {
	f := newJira(t)
	f.add("ENG-1", "5", day(1), day(3))
	f.add("ENG-2", "5", day(-40), day(-20))
	f.add("ENG-3", "5", day(-40), day(-1))
	f.add("ENG-4", "5", day(-90), day(-50))
	s := openStore(t, t.TempDir())
	if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The day before the floor is read with it, and that is fine.
	if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1", "ENG-3"}) {
		t.Fatalf("stored %v", k)
	}
	f.take()

	res, err := jiraSyncer(f, s, Options{Since: day(-30)}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tickets.Added != 1 || res.Tickets.Total != 3 || !res.From.Equal(day(-30)) || res.First {
		t.Errorf("result = %+v", res)
	}
	want := []string{
		// Three slices below the old floor. The last one ends at it.
		`updated >= "2026-07-01" AND updated < "2026-07-18" ORDER BY updated ASC`,
		`updated >= "2026-07-15" AND updated < "2026-08-01" ORDER BY updated ASC`,
		`updated >= "2026-07-29" AND updated < "2026-08-03" ORDER BY updated ASC`,
		// Then what changed since the last sync.
		`updated >= "2026-11-01" ORDER BY updated ASC`,
	}
	if got := jqls(f.take()); !reflect.DeepEqual(got, want) {
		t.Errorf("searches:\n%s", strings.Join(got, "\n"))
	}
	sc, st := jiraSaved(t, s)
	if !sc.From.Equal(day(-30)) || !st.Floor.Equal(day(-30)) {
		t.Errorf("from = %v, floor = %v", sc.From, st.Floor)
	}

	// A later floor does not move it.
	res, err = jiraSyncer(f, s, Options{Since: day(10)}).Sync(context.Background())
	if err != nil || !res.From.Equal(day(-30)) || len(jqls(f.take())) != 1 {
		t.Errorf("result = %+v, error = %v", res, err)
	}
}

func TestJiraProjectsLimitTheReadAndAChangeReadsAgain(t *testing.T) {
	f := newJira(t)
	f.add("ENG-1", "3", day(1), day(3))
	f.add("OPS-1", "3", day(1), day(4))
	f.add("OPS-2", "1", day(-80), day(-60))
	dir := t.TempDir()
	s := openStore(t, dir)

	res, err := jiraSyncer(f, s, Options{}, " eng ", "ENG").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Again || res.Tickets.Total != 1 {
		t.Errorf("result = %+v", res)
	}
	if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1"}) {
		t.Errorf("stored %v", k)
	}
	calls := f.take()
	if p := jOf(calls, jiraProjects); len(p) != 1 || !reflect.DeepEqual(p[0].q["keys"], []string{"ENG"}) {
		t.Errorf("project requests = %+v", p)
	}
	all := jqls(calls)
	if all[0] != `project in ("ENG") AND updated >= "2026-07-31" AND updated < "2026-08-17" ORDER BY updated ASC` ||
		all[len(all)-1] != `project in ("ENG") AND updated < "2026-08-03" AND statusCategory != Done ORDER BY updated ASC` {
		t.Errorf("searches:\n%s", strings.Join(all, "\n"))
	}
	if _, st := jiraSaved(t, s); !reflect.DeepEqual(st.Projects, []string{"ENG"}) {
		t.Errorf("projects = %v", st.Projects)
	}

	// The same projects in another order and case: nothing is read again.
	res, err = jiraSyncer(f, s, Options{}, "ENG").Sync(context.Background())
	if err != nil || res.Again || len(jqls(f.take())) != 1 {
		t.Errorf("result = %+v, error = %v", res, err)
	}

	// One more project: the tickets are dropped and read again.
	var lines []string
	opt := Options{Logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	res, err = jiraSyncer(f, s, opt, "OPS", "ENG").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Again || !res.First || res.Tickets.Added != 3 || res.Tickets.Total != 3 {
		t.Errorf("result = %+v", res)
	}
	if all := jqls(f.take()); len(all) != 8 || !strings.HasPrefix(all[0], `project in ("ENG", "OPS") AND updated >= "2026-07-31"`) {
		t.Errorf("searches:\n%s", strings.Join(all, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "jira 127.0.0.1: the list of projects changed. The stored tickets are dropped and read again") {
		t.Errorf("log = %v", lines)
	}

	// Down to one project: the tickets of the other are gone from the store.
	res, err = jiraSyncer(f, s, Options{}, "OPS").Sync(context.Background())
	if err != nil || !res.Again || res.Tickets.Total != 2 {
		t.Errorf("result = %+v, error = %v", res, err)
	}
	if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, []string{"OPS-1", "OPS-2"}) {
		t.Errorf("stored %v", k)
	}
	f.take()

	// No project at all reads every project, and is a change too.
	res, err = jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err != nil || !res.Again || res.Tickets.Total != 3 {
		t.Errorf("result = %+v, error = %v", res, err)
	}
	if len(jOf(f.take(), jiraProjects)) != 0 {
		t.Error("the projects were asked for with no project to check")
	}
}

func TestJiraAProjectKeyThatNamesNoProjectIsAnError(t *testing.T) {
	f := newJira(t)
	f.add("ENG-1", "3", day(1), day(3))
	s := openStore(t, t.TempDir())
	_, err := jiraSyncer(f, s, Options{}, "ENG", "nope", "ZED").Sync(context.Background())
	if err == nil || err.Error() != "jira: the site 127.0.0.1 has no project with the key NOPE, ZED that this account can read" {
		t.Fatalf("error = %v", err)
	}
	if _, ok := s.Scope(record.Jira, jiraSite); ok || len(jqls(f.take())) != 0 {
		t.Error("a sync with a wrong project key wrote a scope or searched")
	}

	// A key that is not shaped like one never reaches a search.
	for _, bad := range []string{`ENG") OR project in ("OPS`, "1ENG", "EN G", "ENG-1"} {
		_, err = jiraSyncer(f, s, Options{}, bad).Sync(context.Background())
		if err == nil || !strings.Contains(err.Error(), "is not the key of a Jira project. A key looks like ENG") {
			t.Errorf("%q: error = %v", bad, err)
		}
		if calls := f.take(); len(calls) != 1 || calls[0].path != jiraMyself {
			t.Errorf("%q: requests = %+v", bad, calls)
		}
	}
}

func TestJiraChecksManyProjectsFiftyAtATime(t *testing.T) {
	f := newJira(t)
	var keys []string
	for i := 1; i <= 120; i++ {
		keys = append(keys, fmt.Sprintf("P%d", i))
	}
	f.projects = keys
	f.add("P7-1", "3", day(1), day(3))
	s := openStore(t, t.TempDir())
	res, err := jiraSyncer(f, s, Options{}, keys...).Sync(context.Background())
	if err != nil || res.Tickets.Total != 1 {
		t.Fatalf("result = %+v, error = %v", res, err)
	}
	var sizes []int
	for _, c := range jOf(f.take(), jiraProjects) {
		sizes = append(sizes, len(c.q["keys"]))
	}
	if !reflect.DeepEqual(sizes, []int{50, 50, 20}) {
		t.Errorf("keys per request = %v", sizes)
	}
	// The last key of the last group is checked too.
	f.projects = keys[:119]
	_, err = jiraSyncer(f, s, Options{}, keys...).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no project with the key P120 that") {
		t.Errorf("error = %v", err)
	}
}

// Jira answers a search with a wrong token as it answers anyone: with what
// the public may see, which is nothing here. Without the check of the account
// a wrong token would look like a site with no tickets.
func TestJiraAWrongTokenIsAnErrorThatDoesNotShowIt(t *testing.T) {
	f := newJira(t)
	f.add("ENG-1", "3", day(1), day(3))
	s := openStore(t, t.TempDir())
	c, err := jira.New(f.srv.URL, jiraEmail, jiraToken+"x")
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Jira{C: c, S: s, Opt: Options{Now: now, Since: floor}}).Sync(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "jira rejected the email and the API token (401). Set JIRA_EMAIL and JIRA_API_TOKEN") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), jiraToken) || strings.Contains(err.Error(), jiraEmail) {
		t.Errorf("the error shows a secret: %v", err)
	}
	if res.Name != "" || res.Tickets.Total != 0 {
		t.Errorf("result = %+v", res)
	}
	if _, ok := s.Scope(record.Jira, jiraSite); ok {
		t.Error("a sync with a wrong token wrote a scope")
	}
	if calls := f.take(); len(calls) != 1 || calls[0].path != jiraMyself {
		t.Errorf("requests = %+v", calls)
	}
}

func TestJiraASiteThatDoesNotAnswerAsJiraIsAnError(t *testing.T) {
	f := newJira(t)
	s := openStore(t, t.TempDir())
	f.myself = obj{"displayName": "anonymous"}
	_, err := jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err == nil || err.Error() != "jira: 127.0.0.1 does not answer as a Jira Cloud site does" {
		t.Errorf("error = %v", err)
	}
	f.noMyself = true
	_, err = jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "jira: 127.0.0.1 does not answer as a Jira Cloud site does: ") || !errors.Is(err, jira.ErrNotFound) {
		t.Errorf("error = %v", err)
	}
	if _, ok := s.Scope(record.Jira, jiraSite); ok || len(f.take()) != 2 {
		t.Error("a sync of a site that is not Jira wrote a scope or went on")
	}
}

func TestJiraReadsTheWholeHistoryWhenTheSearchCutsIt(t *testing.T) {
	f := newJira(t)
	f.embed, f.historyCap = 2, 2
	long := f.add("ENG-1", "1", day(1), day(1))
	f.move(long, "3", day(2))
	f.move(long, "1", day(3))
	f.move(long, "3", day(4))
	f.move(long, "5", day(5))
	f.move(long, "3", day(6))
	short := f.add("ENG-2", "1", day(1), day(1))
	f.move(short, "3", day(7))
	f.move(short, "5", day(8))
	// Its history is gone by the time it is asked for: what the search
	// carried is used.
	lost := f.add("ENG-3", "1", day(1), day(1))
	f.move(lost, "3", day(2))
	f.move(lost, "4", day(3))
	f.move(lost, "5", day(9))
	f.gone[lost.id] = true

	s := openStore(t, t.TempDir())
	if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := jiraTickets(t, s)
	// The two newest changes alone would say work started on day 6.
	if g := got["ENG-1"]; !eqTime(g.StartedAt, ptr(day(2))) || g.Reopened != 1 || g.State != record.TicketStarted || g.DoneAt != nil {
		t.Errorf("ENG-1 = %+v", g)
	}
	if g := got["ENG-2"]; !eqTime(g.StartedAt, ptr(day(7))) || !eqTime(g.DoneAt, ptr(day(8))) {
		t.Errorf("ENG-2 = %+v", g)
	}
	// Its first start is not in what the search carried. It left a started
	// status it was never seen to move into, so it counts as made in it.
	if g := got["ENG-3"]; !eqTime(g.StartedAt, ptr(day(1))) || !eqTime(g.DoneAt, ptr(day(9))) {
		t.Errorf("ENG-3 = %+v", g)
	}
	var asked []string
	for _, c := range f.take() {
		if strings.HasSuffix(c.path, "/changelog") {
			asked = append(asked, strings.TrimPrefix(c.path, "/rest/api/3/issue/")+"?"+c.get("startAt")+"&"+c.get("maxResults"))
		}
	}
	want := []string{long.id + "/changelog?0&100", long.id + "/changelog?2&100", long.id + "/changelog?4&100", lost.id + "/changelog?0&100"}
	sort.Strings(asked)
	sort.Strings(want)
	if !reflect.DeepEqual(asked, want) {
		t.Errorf("history requests = %v\nwant %v", asked, want)
	}
}

func TestJiraReadsTheHistoryToAnEmptyPageWhenJiraDoesNotSayHowLongItIs(t *testing.T) {
	f := newJira(t)
	f.embed, f.historyCap, f.noTotal = 2, 2, true
	long := f.add("ENG-1", "1", day(1), day(1))
	f.move(long, "3", day(2))
	f.move(long, "1", day(3))
	f.move(long, "3", day(4))
	f.move(long, "5", day(5))
	f.move(long, "3", day(6))
	s := openStore(t, t.TempDir())
	if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The first page alone would say work started on day 2 and never stopped.
	if g := jiraTickets(t, s)["ENG-1"]; !eqTime(g.StartedAt, ptr(day(2))) || g.Reopened != 1 || g.State != record.TicketStarted || g.DoneAt != nil {
		t.Errorf("ENG-1 = %+v", g)
	}
	var asked []string
	for _, c := range f.take() {
		if strings.HasSuffix(c.path, "/changelog") {
			asked = append(asked, c.get("startAt"))
		}
	}
	// Three pages of two changes, the last short, and then an empty one.
	if !reflect.DeepEqual(asked, []string{"0", "2", "4", "5"}) {
		t.Errorf("history requests start at %v", asked)
	}
}

func TestJiraReadsTheHistoryOfEachIssueWhenTheSearchCarriesNone(t *testing.T) {
	f := newJira(t)
	f.noChangelog = true
	one := f.add("ENG-1", "1", day(1), day(1))
	f.move(one, "3", day(2))
	f.move(one, "5", day(5))
	f.add("ENG-2", "1", day(1), day(1))
	lost := f.add("ENG-3", "3", day(1), day(1))
	f.gone[lost.id] = true
	s := openStore(t, t.TempDir())
	if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := jiraTickets(t, s)
	if g := got["ENG-1"]; !eqTime(g.StartedAt, ptr(day(2))) || !eqTime(g.DoneAt, ptr(day(5))) {
		t.Errorf("ENG-1 = %+v", g)
	}
	if g := got["ENG-3"]; g.State != record.TicketStarted || !eqTime(g.StartedAt, ptr(day(1))) {
		t.Errorf("ENG-3 = %+v", g)
	}
	if len(got) != 3 {
		t.Errorf("stored %v", lKeys(got))
	}
}

func TestJiraDoesNotLeanOnThePageSizeItAskedFor(t *testing.T) {
	f := newJira(t)
	f.cap = 3
	var want []string
	for i := 1; i <= 10; i++ {
		f.add(fmt.Sprintf("ENG-%d", i), "3", day(1), day(1).Add(time.Duration(i)*time.Minute))
		want = append(want, fmt.Sprintf("ENG-%d", i))
	}
	sort.Strings(want)
	s := openStore(t, t.TempDir())
	res, err := jiraSyncer(f, s, Options{Checkpoint: 2}).Sync(context.Background())
	if err != nil || res.Tickets.Added != 10 {
		t.Fatalf("result = %+v, error = %v", res, err)
	}
	if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, want) {
		t.Errorf("stored %v", k)
	}
	var tokens []string
	for _, c := range jOf(f.take(), jiraSearch)[:4] {
		tokens = append(tokens, c.get("nextPageToken"))
	}
	if !reflect.DeepEqual(tokens, []string{"", "p3", "p6", "p9"}) {
		t.Errorf("page tokens = %q", tokens)
	}
}

func TestJiraAPageThatComesTwiceIsAnError(t *testing.T) {
	f := newJira(t)
	f.loop = true
	f.add("ENG-1", "3", day(1), day(3))
	s := openStore(t, t.TempDir())
	res, err := jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err == nil || err.Error() != "jira answered with the same page twice" {
		t.Fatalf("error = %v", err)
	}
	// What was read is kept, and the sync is not marked as finished.
	if res.Tickets.Total != 1 || res.First {
		t.Errorf("result = %+v", res)
	}
	if sc, st := jiraSaved(t, s); !sc.SyncedAt.IsZero() || !st.High.Equal(floor) {
		t.Errorf("synced = %v, high = %v", sc.SyncedAt, st.High)
	}
	if n := len(jqls(f.take())); n != 2 {
		t.Errorf("%d searches, want 2", n)
	}
}

func TestJiraHalvesThePageWhenJiraCannotAnswer(t *testing.T) {
	f := newJira(t)
	f.failAbove = 30
	var want []string
	for i := 1; i <= 40; i++ {
		f.add(fmt.Sprintf("ENG-%d", i), "3", day(1), day(1).Add(time.Duration(i)*time.Minute))
		want = append(want, fmt.Sprintf("ENG-%d", i))
	}
	sort.Strings(want)
	s := openStore(t, t.TempDir())
	var lines []string
	opt := Options{Logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	res, err := jiraSyncer(f, s, opt).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, want) || res.Tickets.Added != 40 {
		t.Errorf("stored %d tickets, added %d", len(k), res.Tickets.Added)
	}
	// 100 and 50 fail, five tries each. 25 is answered, and the size stays.
	var sizes []string
	for _, c := range jOf(f.take(), jiraSearch) {
		if n := len(sizes); n == 0 || sizes[n-1] != c.get("maxResults") {
			sizes = append(sizes, c.get("maxResults"))
		}
	}
	if !reflect.DeepEqual(sizes, []string{"100", "50", "25"}) {
		t.Errorf("page sizes = %v", sizes)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "retrying with 50 per page") {
		t.Errorf("log = %v", lines)
	}
}

func TestJiraAStoppedSyncGoesOnFromTheLastSlice(t *testing.T) {
	f := newJira(t)
	f.add("ENG-1", "3", day(1), day(3))
	f.add("ENG-2", "3", day(1), day(40))
	f.add("ENG-3", "1", day(-80), day(-60))
	s := openStore(t, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	f.onSearch = func(n int, r *http.Request) {
		if n == 3 {
			cancel()
			<-r.Context().Done()
		}
	}
	res, err := jiraSyncer(f, s, Options{}).Sync(ctx)
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("error = %v, want a stopped sync", err)
	}
	if res.First || res.Tickets.Total != 1 || res.Name != jiraSite {
		t.Errorf("result = %+v", res)
	}
	// Two slices finished: 28 days are held.
	sc, st := jiraSaved(t, s)
	if !st.High.Equal(day(28)) || st.Open || !sc.SyncedAt.IsZero() {
		t.Errorf("high = %v, open = %v, synced = %v", st.High, st.Open, sc.SyncedAt)
	}
	f.take()
	f.mu.Lock()
	f.onSearch = nil
	f.mu.Unlock()

	res, err = jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.First || res.Tickets.Added != 2 || res.Tickets.Total != 3 {
		t.Errorf("result = %+v", res)
	}
	// It starts ten minutes before the place it had reached.
	if all := jqls(f.take()); all[0] != `updated >= "2026-08-27" AND updated < "2026-09-13" ORDER BY updated ASC` {
		t.Errorf("the next sync starts with %s", all[0])
	}
	if k := lKeys(jiraTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1", "ENG-2", "ENG-3"}) {
		t.Errorf("stored %v", k)
	}
	if _, st := jiraSaved(t, s); !st.Open || !st.High.Equal(now) {
		t.Errorf("state = %+v", st)
	}
}

func TestJiraAMovedIssueKeepsItsPlaceAndItsOldKey(t *testing.T) {
	f := newJira(t)
	i := f.add("ENG-5", "3", day(1), day(3))
	s := openStore(t, t.TempDir())
	if _, err := jiraSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := jiraTickets(t, s)["ENG-5"]

	f.change(i, now.Add(time.Hour),
		obj{"field": "project", "fieldtype": "jira", "from": "10000", "fromString": "Engineering", "to": "10001", "toString": "Operations"},
		obj{"field": "Key", "fieldtype": "jira", "from": nil, "fromString": "ENG-5", "to": nil, "toString": "OPS-9"})
	i.key = "OPS-9"
	res, err := jiraSyncer(f, s, Options{Now: now.Add(2 * time.Hour)}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tickets.Added != 0 || res.Tickets.Changed != 1 || res.Tickets.Total != 1 {
		t.Errorf("result = %+v", res)
	}
	got := jiraTickets(t, s)
	g, ok := got["OPS-9"]
	if !ok || len(got) != 1 {
		t.Fatalf("stored %v", lKeys(got))
	}
	if g.ID != before.ID || !reflect.DeepEqual(g.Aliases, []string{"ENG-5"}) || g.Project != "OPS" || g.URL != f.srv.URL+"/browse/OPS-9" {
		t.Errorf("moved ticket = %+v", g)
	}
}

func TestJiraAnIssueIDThatIsNotANumberIsAnError(t *testing.T) {
	f := newJira(t)
	f.add("ENG-1", "3", day(1), day(3)).id = "1/../../x"
	s := openStore(t, t.TempDir())
	_, err := jiraSyncer(f, s, Options{}).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), `jira sent an issue with the id "1/../../x", which is not a number`) {
		t.Errorf("error = %v", err)
	}
}

func TestJiraJQL(t *testing.T) {
	r := &jiraSync{}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		projects []string
		from, to time.Time
		open     bool
		want     string
	}{
		{nil, at("2026-08-01T00:00:00Z"), time.Time{}, false, `updated >= "2026-07-31" ORDER BY updated ASC`},
		{nil, at("2026-08-01T23:59:59Z"), at("2026-08-15T23:59:59Z"), false, `updated >= "2026-07-31" AND updated < "2026-08-17" ORDER BY updated ASC`},
		{nil, at("2026-03-01T00:00:00Z"), at("2026-12-31T00:00:00Z"), false, `updated >= "2026-02-28" AND updated < "2027-01-02" ORDER BY updated ASC`},
		// A time with an offset is a day in UTC.
		{nil, at("2026-08-01T01:00:00+03:00"), time.Time{}, false, `updated >= "2026-07-30" ORDER BY updated ASC`},
		{[]string{"ENG"}, time.Time{}, at("2026-08-01T00:00:00Z"), true, `project in ("ENG") AND updated < "2026-08-03" AND statusCategory != Done ORDER BY updated ASC`},
		{[]string{"ENG", "OPS_2"}, at("2026-08-01T00:00:00Z"), at("2026-08-15T00:00:00Z"), false, `project in ("ENG", "OPS_2") AND updated >= "2026-07-31" AND updated < "2026-08-17" ORDER BY updated ASC`},
	}
	for _, c := range cases {
		r.st.Projects = c.projects
		if got := r.jql(c.from, c.to, c.open); got != c.want {
			t.Errorf("jql = %s\nwant  %s", got, c.want)
		}
	}
}

func TestJiraTime(t *testing.T) {
	want := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for in, at := range map[string]time.Time{
		`"2026-11-01T10:00:00.000+0000"`:   want,
		`"2026-11-01T15:30:00.000+0530"`:   want,
		`"2026-10-31T22:00:00.000-1200"`:   want,
		`"2026-11-01T10:00:00Z"`:           want,
		`"2026-11-01T12:00:00+02:00"`:      want,
		`"2026-11-01T10:00:00.123+0000"`:   want.Add(123 * time.Millisecond),
		`"2026-11-01T10:00:00.123456789Z"`: want.Add(123456789 * time.Nanosecond),
		`"2026-11-01T10:00:00+0000"`:       want,
		`""`:                               {},
		`null`:                             {},
	} {
		var got jiraTime
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !got.Equal(at) || got.Location() != time.UTC {
			t.Errorf("%s = %v, want %v", in, got.Time, at)
		}
	}
	for _, in := range []string{`"yesterday"`, `"2026-11-01"`, `1793527200`, `"2026-11-01 10:00:00"`} {
		var got jiraTime
		if err := json.Unmarshal([]byte(in), &got); err == nil {
			t.Errorf("%s was read as %v", in, got.Time)
		}
	}
	// A time that is left out stays unset.
	var n jiraIssue
	if err := json.Unmarshal([]byte(`{"fields":{"resolutiondate":null}}`), &n); err != nil || n.Fields.ResolutionDate != nil || !n.Fields.Created.IsZero() {
		t.Errorf("issue = %+v, error = %v", n, err)
	}
}

func TestNotDone(t *testing.T) {
	for _, name := range []string{"Won't Do", "WONT DO", "won’t fix", " Duplicate ", "Cannot Reproduce", "Can't  Reproduce", "Declined", "Cancelled", "canceled", "Rejected", "Invalid", "Obsolete", "Not a bug", "Incomplete", "Abandoned", "Not Planned"} {
		if !notDone(name) {
			t.Errorf("%q should mean the ticket was dropped", name)
		}
	}
	for _, name := range []string{"Done", "Fixed", "Resolved", "Closed", "Released", "", "Won't Do Yet", "Cancel"} {
		if notDone(name) {
			t.Errorf("%q should not mean the ticket was dropped", name)
		}
	}
}

func jiraTestStatuses() map[string]jiraStatus {
	out := map[string]jiraStatus{}
	for _, s := range jiraFakeStatuses {
		out[s.id] = jiraStatus{category: s.category, name: s.name}
	}
	return out
}

func jiraTestClient(t *testing.T) *jira.Client {
	t.Helper()
	c, err := jira.New("Acme.atlassian.net", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// jts0 writes a time as Jira does, in a time zone that is not UTC.
func jts0(t time.Time) string {
	return t.In(time.FixedZone("", -7*3600)).Format("2006-01-02T15:04:05.000-0700")
}

func TestJiraRecord(t *testing.T) {
	statuses := jiraTestStatuses()
	at := func(n float64) *time.Time { d := day(n); return &d }
	move := func(from, to string, n float64) string {
		return fmt.Sprintf(`{"id":"%d","created":%q,"items":[{"field":"assignee","fieldtype":"jira","from":"a","to":"b"},{"field":"status","fieldtype":"jira","from":%q,"to":%q}]}`, int(n*100), jts0(day(n)), from, to)
	}
	base := func(status string, fields string, changes ...string) string {
		st := statuses[status]
		return fmt.Sprintf(`{"id":"10009","key":"ENG-9","fields":{"summary":"T","created":%q,"updated":%q,
			"status":{"id":%q,"name":%q,"statusCategory":{"key":%q}},"labels":[],"issuetype":{"hierarchyLevel":0},"project":{"key":"ENG"},
			"creator":{"displayName":"Ada","accountType":"atlassian"}%s},"changelog":{"total":%d,"histories":[%s]}}`,
			jts0(day(1)), jts0(day(9)), status, st.name, st.category, fields, len(changes), strings.Join(changes, ","))
	}
	resolved := func(name string, n float64) string {
		return fmt.Sprintf(`,"resolution":{"name":%q},"resolutiondate":%q`, name, jts0(day(n)))
	}
	cases := []struct {
		name     string
		json     string
		state    string
		started  *time.Time
		done     *time.Time
		closed   *time.Time
		reopened int
	}{
		{"to do is unstarted", base("1", ""), record.TicketUnstarted, nil, nil, nil, 0},
		{"a status named backlog is backlog", base("2", ""), record.TicketBacklog, nil, nil, nil, 0},
		{"a category this version does not know is backlog", base("7", ""), record.TicketBacklog, nil, nil, nil, 0},
		{"in progress with no history started when it was made", base("3", ""), record.TicketStarted, at(1), nil, nil, 0},
		{"in progress takes the time of the move", base("3", "", move("1", "3", 4)), record.TicketStarted, at(4), nil, nil, 0},
		// Work started on day 2, went back, and started again on day 6.
		{"the first start in the history wins", base("3", "", move("1", "3", 2), move("3", "1", 3), move("1", "3", 6)), record.TicketStarted, at(2), nil, nil, 0},
		{"a move between two started statuses is not a new start", base("4", "", move("1", "3", 2), move("3", "4", 5)), record.TicketStarted, at(2), nil, nil, 0},
		{"a ticket back in to do keeps its first start", base("1", "", move("1", "3", 2), move("3", "1", 3)), record.TicketUnstarted, at(2), nil, nil, 0},
		{"done takes the time of the move, not of the resolution", base("5", resolved("Done", 8.5), move("1", "3", 2), move("3", "5", 8)), record.TicketDone, at(2), at(8), at(8), 0},
		{"done with no history takes the resolution date", base("5", resolved("Fixed", 7)), record.TicketDone, nil, at(7), at(7), 0},
		{"done with neither takes the update time", base("5", ""), record.TicketDone, nil, at(9), at(9), 0},
		{"made in a started status and done from it", base("5", "", move("3", "5", 6)), record.TicketDone, at(1), at(6), at(6), 0},
		{"done straight from to do never started", base("5", "", move("1", "5", 6)), record.TicketDone, nil, at(6), at(6), 0},
		// Done on day 3, opened again on day 4, done on day 5, opened on day 6, done on day 8.
		{"opened again twice", base("5", resolved("Done", 8), move("1", "3", 2), move("3", "5", 3), move("5", "3", 4), move("3", "5", 5), move("5", "1", 6), move("1", "5", 8)),
			record.TicketDone, at(2), at(8), at(8), 2},
		{"opened again and still open", base("3", "", move("1", "3", 2), move("3", "5", 3), move("5", "3", 4)), record.TicketStarted, at(2), nil, nil, 1},
		// Done, then closed after a check: it was done at the first of the two.
		{"a move between two done statuses keeps the first time", base("8", resolved("Done", 4), move("3", "5", 4), move("5", "8", 7)), record.TicketDone, at(1), at(4), at(4), 0},
		{"a resolution that drops the ticket is cancelled", base("5", resolved("Won't Do", 7), move("1", "5", 7)), record.TicketCancelled, nil, nil, at(7), 0},
		{"a duplicate is cancelled", base("5", resolved("Duplicate", 7)), record.TicketCancelled, nil, nil, at(7), 0},
		{"a status that drops the ticket is cancelled", base("6", "", move("3", "6", 5)), record.TicketCancelled, at(1), nil, at(5), 0},
		{"cancelled with no time takes the update time", base("6", ""), record.TicketCancelled, nil, nil, at(9), 0},
		{"dropped then opened is not opened again: it was never done", base("3", "", move("1", "6", 3), move("6", "3", 5)), record.TicketStarted, at(5), nil, nil, 0},
		{"done then dropped closed at the first of the two", base("6", "", move("3", "5", 3), move("5", "6", 5)), record.TicketCancelled, at(1), nil, at(3), 0},
		{"a status the site no longer has is skipped", base("3", "", move("gone", "99", 1.5), move("1", "3", 3)), record.TicketStarted, at(3), nil, nil, 0},
		{"a move out of a status the site no longer has counts", base("5", "", move("99", "5", 4)), record.TicketDone, nil, at(4), at(4), 0},
		{"a move into a status the site no longer has changes nothing", base("5", "", move("3", "5", 4), move("5", "99", 5), move("99", "5", 6)), record.TicketDone, at(1), at(6), at(6), 0},
		{"a custom field named status is not the status", base("1", "", `{"created":"`+jts0(day(2))+`","items":[{"field":"Status","fieldtype":"custom","from":"1","to":"3"}]}`), record.TicketUnstarted, nil, nil, nil, 0},
		// Newest first, as a search sends them.
		{"changes are put in order", base("5", "", move("3", "5", 5), move("5", "3", 4), move("3", "5", 3), move("1", "3", 2)), record.TicketDone, at(2), at(5), at(5), 1},
	}
	c := jiraTestClient(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var n jiraIssue
			if err := json.Unmarshal([]byte(tc.json), &n); err != nil {
				t.Fatal(err)
			}
			got := jiraRecord(c, &n, n.Changelog.Histories, statuses)
			if got.State != tc.state || !eqTime(got.StartedAt, tc.started) || !eqTime(got.DoneAt, tc.done) || !eqTime(got.ClosedAt, tc.closed) || got.Reopened != tc.reopened {
				t.Errorf("state %s, started %v, done %v, closed %v, reopened %d", got.State, got.StartedAt, got.DoneAt, got.ClosedAt, got.Reopened)
			}
			// The lists are never nil: the store reads an empty list back as
			// an empty one, and a nil here would count as a change each sync.
			if got.Aliases == nil || got.Labels == nil || len(got.Aliases) != 0 {
				t.Errorf("aliases = %#v, labels = %#v", got.Aliases, got.Labels)
			}
			if got.ID != "jira:acme.atlassian.net/10009" || got.URL != "https://acme.atlassian.net/browse/ENG-9" || got.Source != record.Jira {
				t.Errorf("id %q, url %q", got.ID, got.URL)
			}
			if !got.CreatedAt.Equal(day(1)) || !got.UpdatedAt.Equal(day(9)) || got.CreatedAt.Location() != time.UTC {
				t.Errorf("created %v, updated %v", got.CreatedAt, got.UpdatedAt)
			}
		})
	}
}

func TestJiraRecordNamesAndAuthors(t *testing.T) {
	c := jiraTestClient(t)
	decode := func(fields, changes string) record.Ticket {
		t.Helper()
		var n jiraIssue
		js := `{"id":"7","key":"ENG-9","fields":{"created":"2026-08-02T00:00:00.000+0000","updated":"2026-08-03T00:00:00.000+0000",
			"status":{"id":"1","name":"To Do","statusCategory":{"key":"new"}},"project":{"key":"ENG"}` + fields + `},"changelog":{"histories":[` + changes + `]}}`
		if err := json.Unmarshal([]byte(js), &n); err != nil {
			t.Fatal(err)
		}
		return jiraRecord(c, &n, n.Changelog.Histories, jiraTestStatuses())
	}
	key := func(n int, typ, from string) string {
		return fmt.Sprintf(`{"created":"2026-08-0%dT00:00:00.000+0000","items":[{"field":"Key","fieldtype":%q,"fromString":%q,"toString":"x"}]}`, n, typ, from)
	}
	cases := []struct {
		name    string
		fields  string
		changes string
		author  record.Actor
		aliases []string
		labels  []string
	}{
		{"no creator", `,"creator":null`, "", record.Actor{Kind: record.ActorUser}, []string{}, []string{}},
		{"a person", `,"creator":{"displayName":"Ada L","accountType":"atlassian","emailAddress":"ada@example.com"},"labels":["b","a"]`, "", record.Actor{Login: "Ada L", Kind: record.ActorUser}, []string{}, []string{"b", "a"}},
		{"an integration", `,"creator":{"displayName":"Deploy Bot","accountType":"app"}`, "", record.Actor{Login: "Deploy Bot", Kind: record.ActorBot}, []string{}, []string{}},
		{"a customer of a service desk", `,"creator":{"displayName":"Cy","accountType":"customer"},"labels":null`, "", record.Actor{Login: "Cy", Kind: record.ActorUser}, []string{}, []string{}},
		// Moved twice. The oldest key comes first.
		{"old keys in order", "", key(4, "jira", "BBB-2") + "," + key(3, "jira", "AAA-1"), record.Actor{Kind: record.ActorUser}, []string{"AAA-1", "BBB-2"}, []string{}},
		// Moved away and back: its own key is not an old key, and a key is kept once.
		{"its own key is not an old one", "", key(3, "jira", "ENG-9") + "," + key(4, "jira", "OPS-1") + "," + key(5, "jira", "eng-9") + "," + key(6, "jira", "ops-1"), record.Actor{Kind: record.ActorUser}, []string{"OPS-1"}, []string{}},
		{"a custom field named key is not the key", "", key(3, "custom", "OLD-1"), record.Actor{Kind: record.ActorUser}, []string{}, []string{}},
		{"a value that is not a key is skipped", "", key(3, "jira", "not a key") + "," + key(4, "jira", ""), record.Actor{Kind: record.ActorUser}, []string{}, []string{}},
	}
	for _, tc := range cases {
		got := decode(tc.fields, tc.changes)
		if got.Author != tc.author || !reflect.DeepEqual(got.Aliases, tc.aliases) || !reflect.DeepEqual(got.Labels, tc.labels) {
			t.Errorf("%s: author %+v, aliases %#v, labels %#v", tc.name, got.Author, got.Aliases, got.Labels)
		}
	}
}
