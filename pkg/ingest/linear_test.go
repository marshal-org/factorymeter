package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/linear"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// This file plays Linear for the sync tests. It keeps issues, filters them the
// way the issues query asks, and pages them newest first, so a test fails if
// the sync leans on the order of a page.

const linearKey = "lin_api_fake_key"

type lIssue struct {
	updated time.Time
	team    string
	typ     string
	node    obj
}

type lCall struct {
	kind   string // org, teams, states or issues
	size   int
	after  string
	filter map[string]any
}

type fakeLinear struct {
	t      *testing.T
	mu     sync.Mutex
	org    string
	teams  []string
	issues []*lIssue
	calls  []lCall
	// maxPage makes Linear find a bigger page of issues too complex. Zero is no limit.
	maxPage int
	// onIssues runs before a page of issues is answered, with the number of
	// such pages asked for so far.
	onIssues func(n int, r *http.Request)
	nIssues  int
	// more holds the rest of the state history of an issue, by its id, a page
	// at a time. The issues query carries only the start of it.
	more map[string][][]obj
	srv  *httptest.Server
}

func newLinear(t *testing.T) *fakeLinear {
	f := &fakeLinear{t: t, org: "acme", teams: []string{"ENG", "OPS"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLinear) client() *linear.Client {
	c := linear.New(linearKey)
	c.URL = f.srv.URL
	return c
}

// add keeps an issue. Its team is the first part of its key, and its state is
// named after the type of the state. more sets or replaces fields of the node.
func (f *fakeLinear) add(id, key, typ string, created, updated time.Time, more obj) *lIssue {
	team := key[:strings.Index(key, "-")]
	node := obj{
		"id": id, "identifier": key, "previousIdentifiers": []string{}, "title": "Ticket " + key,
		"url": "https://linear.app/acme/issue/" + key, "createdAt": ts(created), "updatedAt": ts(updated),
		"startedAt": nil, "completedAt": nil, "canceledAt": nil, "archivedAt": nil, "trashed": nil,
		"state": obj{"name": strings.ToUpper(typ[:1]) + typ[1:], "type": typ}, "team": obj{"key": team},
		"creator": obj{"displayName": "ada", "app": false}, "botActor": nil,
		"labels": obj{"nodes": []obj{}}, "attachments": obj{"nodes": []obj{}}, "stateHistory": obj{"nodes": []obj{}},
	}
	for k, v := range more {
		node[k] = v
	}
	i := &lIssue{updated: updated, team: team, typ: typ, node: node}
	f.mu.Lock()
	f.issues = append(f.issues, i)
	f.mu.Unlock()
	return i
}

// touch moves an issue to another state at a time.
func (f *fakeLinear) touch(i *lIssue, typ string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i.typ, i.updated = typ, at
	i.node["updatedAt"] = ts(at)
	i.node["state"] = obj{"name": strings.ToUpper(typ[:1]) + typ[1:], "type": typ}
}

func (f *fakeLinear) take() []lCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func lOf(calls []lCall, kind string) []lCall {
	var out []lCall
	for _, c := range calls {
		if c.kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func attachments(urls ...string) obj {
	nodes := []obj{}
	for _, u := range urls {
		nodes = append(nodes, obj{"url": u})
	}
	return obj{"nodes": nodes}
}

// history lists state spans: pairs of a state type and the day it started.
func history(pairs ...any) obj {
	nodes := []obj{}
	for i := 0; i+1 < len(pairs); i += 2 {
		nodes = append(nodes, obj{"stateId": "s-" + pairs[i].(string), "startedAt": ts(pairs[i+1].(time.Time))})
	}
	return obj{"nodes": nodes}
}

var linearStateTypes = []string{"triage", "backlog", "unstarted", "started", "completed", "canceled", "duplicate"}

var pageSize = regexp.MustCompile(`issues\(first: (\d+),`)

func lErr(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(obj{"errors": []obj{{"message": msg, "extensions": obj{"code": code}}}})
}

func (f *fakeLinear) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != linearKey {
		lErr(w, 401, "AUTHENTICATION_ERROR", "Authentication required, not authenticated")
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &req); err != nil {
		f.t.Errorf("linear body: %v", err)
	}
	filter, _ := req.Variables["filter"].(map[string]any)
	c := lCall{after: str(req.Variables["after"]), filter: filter}
	switch {
	case strings.Contains(req.Query, "issues("):
		c.kind = "issues"
		if m := pageSize.FindStringSubmatch(req.Query); m != nil {
			c.size, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(req.Query, "issue(id:"):
		c.kind = "history"
	case strings.Contains(req.Query, "workflowStates("):
		c.kind = "states"
	case strings.Contains(req.Query, "teams("):
		c.kind = "teams"
	case strings.Contains(req.Query, "organization"):
		c.kind = "org"
	default:
		f.t.Errorf("unknown linear query: %s", req.Query)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	hook, n := f.onIssues, 0
	if c.kind == "issues" {
		f.nIssues++
		n = f.nIssues
	}
	f.mu.Unlock()

	switch c.kind {
	case "org":
		json.NewEncoder(w).Encode(obj{"data": obj{"organization": obj{"urlKey": f.org, "name": "Acme"}}})
	case "teams":
		nodes := []obj{}
		for _, k := range f.teams {
			if among(nested(filter, "key", "in"), k) {
				nodes = append(nodes, obj{"key": k})
			}
		}
		json.NewEncoder(w).Encode(obj{"data": obj{"teams": obj{"nodes": nodes}}})
	case "states":
		// Two pages, so the sync has to follow the cursor.
		half := linearStateTypes[:4]
		info := obj{"hasNextPage": true, "endCursor": "more"}
		if c.after == "more" {
			half, info = linearStateTypes[4:], obj{"hasNextPage": false, "endCursor": nil}
		}
		nodes := []obj{}
		for _, typ := range half {
			nodes = append(nodes, obj{"id": "s-" + typ, "type": typ})
		}
		json.NewEncoder(w).Encode(obj{"data": obj{"workflowStates": obj{"nodes": nodes, "pageInfo": info}}})
	case "history":
		f.mu.Lock()
		pages := f.more[str(req.Variables["id"])]
		f.mu.Unlock()
		// Cursor hN asks for page N, counted from 1.
		n, _ := strconv.Atoi(strings.TrimPrefix(c.after, "h"))
		if n < 1 || n > len(pages) {
			lErr(w, 400, "INPUT_ERROR", "bad cursor")
			return
		}
		info := obj{"hasNextPage": n < len(pages), "endCursor": fmt.Sprintf("h%d", n+1)}
		json.NewEncoder(w).Encode(obj{"data": obj{"issue": obj{"stateHistory": obj{"nodes": pages[n-1], "pageInfo": info}}}})
	case "issues":
		if hook != nil {
			hook(n, r)
		}
		if f.maxPage > 0 && c.size > f.maxPage {
			lErr(w, 400, "INPUT_ERROR", "Query too complex")
			return
		}
		json.NewEncoder(w).Encode(obj{"data": obj{"issues": f.page(c)}})
	}
}

// nested walks a filter down the given keys and returns what is there.
func nested(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

// among reports whether a list holds a value. A filter that is not there
// lets everything through.
func among(list any, v string) bool {
	l, ok := list.([]any)
	if !ok {
		return list == nil
	}
	for _, x := range l {
		if str(x) == v {
			return true
		}
	}
	return false
}

func (f *fakeLinear) page(c lCall) obj {
	f.mu.Lock()
	defer f.mu.Unlock()
	var hit []*lIssue
	for _, i := range f.issues {
		if s := str(nested(c.filter, "updatedAt", "gte")); s != "" {
			if at, _ := time.Parse(time.RFC3339, s); i.updated.Before(at) {
				continue
			}
		}
		if s := str(nested(c.filter, "updatedAt", "lt")); s != "" {
			if at, _ := time.Parse(time.RFC3339, s); !i.updated.Before(at) {
				continue
			}
		}
		if !among(nested(c.filter, "team", "key", "in"), i.team) || !among(nested(c.filter, "state", "type", "in"), i.typ) {
			continue
		}
		hit = append(hit, i)
	}
	// Newest first: the sync must not need them oldest first.
	sort.SliceStable(hit, func(a, b int) bool { return hit[a].updated.After(hit[b].updated) })
	off, _ := strconv.Atoi(c.after)
	end := off + c.size
	if end > len(hit) {
		end = len(hit)
	}
	nodes := []obj{}
	for _, i := range hit[off:end] {
		nodes = append(nodes, i.node)
	}
	info := obj{"hasNextPage": end < len(hit), "endCursor": strconv.Itoa(end)}
	return obj{"nodes": nodes, "pageInfo": info}
}

func linearSyncer(f *fakeLinear, s *store.Store, opt Options, teams ...string) *Linear {
	if opt.Now.IsZero() {
		opt.Now = now
	}
	if opt.Since.IsZero() {
		opt.Since = floor
	}
	return &Linear{C: f.client(), S: s, Opt: opt, Teams: teams}
}

func linearTickets(t *testing.T, s *store.Store) map[string]record.Ticket {
	t.Helper()
	out := map[string]record.Ticket{}
	err := store.Tickets.Each(s, record.Linear, "acme", time.Time{}, time.Time{}, func(_ string, rows []record.Ticket) error {
		for _, r := range rows {
			out[r.Key] = r
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func linearLinks(t *testing.T, s *store.Store) []string {
	t.Helper()
	var out []string
	err := store.Links.Each(s, record.Linear, "acme", time.Time{}, time.Time{}, func(_ string, rows []record.Link) error {
		for _, l := range rows {
			out = append(out, l.From+" "+l.Kind+" "+l.To+" "+l.Found)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func linearSaved(t *testing.T, s *store.Store) (store.Scope, linearState) {
	t.Helper()
	sc, ok := s.Scope(record.Linear, "acme")
	if !ok {
		t.Fatal("the store has no linear scope")
	}
	var st linearState
	if err := json.Unmarshal(sc.State, &st); err != nil {
		t.Fatal(err)
	}
	return sc, st
}

func lKeys(m map[string]record.Ticket) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestLinearQueriesAreTheFilesAndNeverWrite(t *testing.T) {
	for name, q := range map[string]string{"org": linearOrgQuery, "teams": linearTeamsQuery, "states": linearStatesQuery, "issues": linearIssuesQuery, "history": linearHistoryQuery} {
		if strings.Contains(q, "mutation") || strings.Contains(q, "subscription") {
			t.Errorf("the %s query can write", name)
		}
		if !strings.Contains(q, "query") {
			t.Errorf("the %s query is empty", name)
		}
	}
	// The sync writes another page size over this text. It must be there once.
	if n := strings.Count(linearIssuesQuery, linearPageMark); n != 1 {
		t.Errorf("the issues query names its page size %d times, want 1", n)
	}
	if want := fmt.Sprintf("issues(first: %d,", linearPage); linearPageMark != want {
		t.Errorf("page mark = %q, want %q", linearPageMark, want)
	}
	if want := fmt.Sprintf("stateHistory(first: %d)", linearSpans); !strings.Contains(linearIssuesQuery, want) {
		t.Errorf("the issues query does not say %q", want)
	}
}

func TestLinearFirstSyncStoresTicketsAndLinks(t *testing.T) {
	f := newLinear(t)
	// Updated inside the window.
	f.add("u1", "ENG-1", "completed", day(10), day(20), obj{
		"startedAt": ts(day(12)), "completedAt": ts(day(20)),
		"attachments":  attachments("https://github.com/acme/web/pull/7", "https://github.com/acme/web/pull/7/files", "https://example.com/acme/web/pull/x"),
		"stateHistory": history("backlog", day(10), "started", day(12), "completed", day(20)),
		"labels":       obj{"nodes": []obj{{"name": "bug"}}},
	})
	f.add("u2", "ENG-2", "started", day(30), day(40), obj{"startedAt": ts(day(35))})
	f.add("u3", "OPS-1", "canceled", day(50), day(60), obj{"canceledAt": ts(day(60))})
	// Last updated before the window: one still open, one closed.
	f.add("u4", "ENG-3", "backlog", day(-90), day(-40), nil)
	f.add("u5", "ENG-4", "completed", day(-90), day(-50), obj{"completedAt": ts(day(-50))})

	s := openStore(t, t.TempDir())
	res, err := linearSyncer(f, s, Options{}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != record.Linear || res.Name != "acme" || !res.First || res.Again || !res.From.Equal(floor) {
		t.Errorf("result = %+v", res)
	}
	// Three in the window and the open one from before it. The closed one from
	// before the window is not read.
	if res.Tickets.Added != 4 || res.Tickets.Changed != 0 || res.Tickets.Total != 4 {
		t.Errorf("tickets = %+v, want 4 new", res.Tickets)
	}
	if res.Links.Added != 1 || res.Links.Total != 1 {
		t.Errorf("links = %+v, want 1 new", res.Links)
	}
	got := linearTickets(t, s)
	if k := lKeys(got); !reflect.DeepEqual(k, []string{"ENG-1", "ENG-2", "ENG-3", "OPS-1"}) {
		t.Fatalf("stored %v", k)
	}
	e1 := got["ENG-1"]
	if e1.ID != "linear:u1" || e1.Source != record.Linear || e1.Project != "ENG" || e1.State != record.TicketDone || e1.RawState != "Completed" ||
		e1.URL != "https://linear.app/acme/issue/ENG-1" || e1.Author.Login != "ada" || e1.Author.Kind != record.ActorUser ||
		!reflect.DeepEqual(e1.Labels, []string{"bug"}) || len(e1.Aliases) != 0 {
		t.Errorf("ENG-1 = %+v", e1)
	}
	if e1.StartedAt == nil || !e1.StartedAt.Equal(day(12)) || e1.DoneAt == nil || !e1.DoneAt.Equal(day(20)) || e1.ClosedAt == nil || !e1.ClosedAt.Equal(day(20)) {
		t.Errorf("ENG-1 times = %v %v %v", e1.StartedAt, e1.DoneAt, e1.ClosedAt)
	}
	if o := got["OPS-1"]; o.State != record.TicketCancelled || o.DoneAt != nil || o.ClosedAt == nil || !o.ClosedAt.Equal(day(60)) {
		t.Errorf("OPS-1 = %+v", o)
	}
	if o := got["ENG-3"]; o.State != record.TicketBacklog || o.ClosedAt != nil {
		t.Errorf("ENG-3 = %+v", o)
	}
	// The same pull request attached twice is one link. A URL that is not a
	// pull request is none.
	if l := linearLinks(t, s); !reflect.DeepEqual(l, []string{"github:acme/web#7 mentions linear:u1 source"}) {
		t.Errorf("links = %v", l)
	}

	sc, st := linearSaved(t, s)
	if !sc.Private || !sc.SyncedAt.Equal(now) || !sc.From.Equal(floor) {
		t.Errorf("scope = %+v", sc)
	}
	if !st.Floor.Equal(floor) || !st.High.Equal(now) || !st.Open || len(st.Teams) != 0 {
		t.Errorf("state = %+v", st)
	}

	// The window is 93.5 days: 14 slices of 7 days, then the open tickets.
	calls := f.take()
	issues := lOf(calls, "issues")
	if len(lOf(calls, "org")) != 1 || len(lOf(calls, "states")) != 2 || len(lOf(calls, "teams")) != 0 || len(issues) != 15 {
		t.Fatalf("calls: %d org, %d states, %d teams, %d issues", len(lOf(calls, "org")), len(lOf(calls, "states")), len(lOf(calls, "teams")), len(issues))
	}
	for i, c := range issues[:13] {
		want := map[string]any{"updatedAt": map[string]any{"gte": ts(day(float64(7 * i))), "lt": ts(day(float64(7 * (i + 1))))}}
		if !reflect.DeepEqual(c.filter, want) || c.size != 50 {
			t.Errorf("slice %d: filter %v, size %d", i, c.filter, c.size)
		}
	}
	// The last slice has no upper bound.
	if want := map[string]any{"updatedAt": map[string]any{"gte": ts(day(91))}}; !reflect.DeepEqual(issues[13].filter, want) {
		t.Errorf("last slice: %v", issues[13].filter)
	}
	wantOpen := map[string]any{"updatedAt": map[string]any{"lt": ts(floor)}, "state": map[string]any{"type": map[string]any{"in": []any{"triage", "backlog", "unstarted", "started"}}}}
	if !reflect.DeepEqual(issues[14].filter, wantOpen) {
		t.Errorf("open pass: %v", issues[14].filter)
	}
}

func TestLinearNextSyncReadsOnlyWhatChanged(t *testing.T) {
	f := newLinear(t)
	a := f.add("u1", "ENG-1", "started", day(10), day(20), obj{"startedAt": ts(day(12))})
	f.add("u2", "ENG-2", "backlog", day(30), day(40), nil)
	dir := t.TempDir()
	s := openStore(t, dir)
	if _, err := linearSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.take()

	// Nothing changed: one slice from ten minutes before the last sync, no
	// open pass, and not one ticket written again.
	later := now.Add(time.Hour)
	res, err := linearSyncer(f, s, Options{Now: later}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.First || res.Tickets.Added != 0 || res.Tickets.Changed != 0 || res.Tickets.Total != 2 {
		t.Errorf("result = %+v", res)
	}
	issues := lOf(f.take(), "issues")
	if len(issues) != 1 {
		t.Fatalf("%d issue pages, want 1", len(issues))
	}
	if want := map[string]any{"updatedAt": map[string]any{"gte": ts(now.Add(-10 * time.Minute))}}; !reflect.DeepEqual(issues[0].filter, want) {
		t.Errorf("filter = %v", issues[0].filter)
	}

	// One ticket is done and one is new.
	doneAt := later.Add(30 * time.Minute)
	f.touch(a, "completed", doneAt)
	a.node["completedAt"] = ts(doneAt)
	f.add("u3", "ENG-3", "unstarted", doneAt, doneAt, nil)
	res, err = linearSyncer(f, s, Options{Now: later.Add(time.Hour)}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tickets.Added != 1 || res.Tickets.Changed != 1 || res.Tickets.Total != 3 {
		t.Errorf("tickets = %+v, want 1 new and 1 updated", res.Tickets)
	}
	got := linearTickets(t, s)
	if e := got["ENG-1"]; e.State != record.TicketDone || e.DoneAt == nil || !e.DoneAt.Equal(doneAt) {
		t.Errorf("ENG-1 = %+v", e)
	}
	if e := got["ENG-3"]; e.State != record.TicketUnstarted {
		t.Errorf("ENG-3 = %+v", e)
	}
	if _, st := linearSaved(t, s); !st.High.Equal(later.Add(time.Hour)) {
		t.Errorf("high = %v", st.High)
	}
}

func TestLinearAnEarlierFloorReadsTheStretchBelow(t *testing.T) {
	f := newLinear(t)
	f.add("u1", "ENG-1", "completed", day(10), day(20), obj{"completedAt": ts(day(20))})
	// Closed before the first floor, so the first sync leaves it out.
	f.add("u2", "ENG-2", "completed", day(-30), day(-10), obj{"completedAt": ts(day(-10))})
	s := openStore(t, t.TempDir())
	if _, err := linearSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if k := lKeys(linearTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1"}) {
		t.Fatalf("stored %v", k)
	}
	f.take()

	earlier := day(-14)
	res, err := linearSyncer(f, s, Options{Since: earlier, Now: now.Add(time.Hour)}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Tickets.Added != 1 || !res.From.Equal(earlier) {
		t.Errorf("result = %+v", res)
	}
	if k := lKeys(linearTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1", "ENG-2"}) {
		t.Errorf("stored %v", k)
	}
	issues := lOf(f.take(), "issues")
	// Two slices below the old floor, then the one from the last sync on.
	want := []map[string]any{
		{"updatedAt": map[string]any{"gte": ts(day(-14)), "lt": ts(day(-7))}},
		{"updatedAt": map[string]any{"gte": ts(day(-7)), "lt": ts(floor)}},
		{"updatedAt": map[string]any{"gte": ts(now.Add(-10 * time.Minute))}},
	}
	if len(issues) != len(want) {
		t.Fatalf("%d issue pages, want %d", len(issues), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(issues[i].filter, want[i]) {
			t.Errorf("page %d: %v, want %v", i, issues[i].filter, want[i])
		}
	}
	// A later floor changes nothing.
	res, err = linearSyncer(f, s, Options{Since: day(30), Now: now.Add(2 * time.Hour)}).Sync(context.Background())
	if err != nil || !res.From.Equal(earlier) {
		t.Errorf("from = %v, err = %v", res.From, err)
	}
}

func TestLinearTeamsLimitTheReadAndAChangeReadsAgain(t *testing.T) {
	f := newLinear(t)
	f.add("u1", "ENG-1", "started", day(10), day(20), obj{"attachments": attachments("https://github.com/acme/web/pull/1")})
	f.add("u2", "OPS-1", "started", day(10), day(21), obj{"attachments": attachments("https://github.com/acme/web/pull/2")})
	s := openStore(t, t.TempDir())

	res, err := linearSyncer(f, s, Options{}, "eng", " ENG ").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k := lKeys(linearTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1"}) || res.Again {
		t.Fatalf("stored %v, again %v", k, res.Again)
	}
	calls := f.take()
	if teams := lOf(calls, "teams"); len(teams) != 1 || !reflect.DeepEqual(nested(teams[0].filter, "key", "in"), []any{"ENG"}) {
		t.Errorf("teams calls = %+v", teams)
	}
	for _, c := range lOf(calls, "issues") {
		if !reflect.DeepEqual(nested(c.filter, "team", "key", "in"), []any{"ENG"}) {
			t.Fatalf("an issues page is not limited to the team: %v", c.filter)
		}
	}
	if _, st := linearSaved(t, s); !reflect.DeepEqual(st.Teams, []string{"ENG"}) {
		t.Errorf("teams = %v", st.Teams)
	}

	// The same team again is a short sync.
	if res, err = linearSyncer(f, s, Options{Now: now.Add(time.Hour)}, "ENG").Sync(context.Background()); err != nil || res.Again {
		t.Fatalf("again = %v, err = %v", res.Again, err)
	}
	if n := len(lOf(f.take(), "issues")); n != 1 {
		t.Errorf("%d issue pages, want 1", n)
	}

	// Another team: the tickets and links of ENG must not stay behind.
	res, err = linearSyncer(f, s, Options{Now: now.Add(2 * time.Hour)}, "OPS").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Again || !res.First || res.Tickets.Added != 1 || res.Tickets.Total != 1 {
		t.Errorf("result = %+v", res)
	}
	if k := lKeys(linearTickets(t, s)); !reflect.DeepEqual(k, []string{"OPS-1"}) {
		t.Errorf("stored %v", k)
	}
	if l := linearLinks(t, s); !reflect.DeepEqual(l, []string{"github:acme/web#2 mentions linear:u2 source"}) {
		t.Errorf("links = %v", l)
	}
	// And every team: the same.
	res, err = linearSyncer(f, s, Options{Now: now.Add(3 * time.Hour)}).Sync(context.Background())
	if err != nil || !res.Again || res.Tickets.Total != 2 {
		t.Errorf("result = %+v, err = %v", res, err)
	}
}

func TestLinearATeamKeyThatNamesNoTeamIsAnError(t *testing.T) {
	f := newLinear(t)
	s := openStore(t, t.TempDir())
	_, err := linearSyncer(f, s, Options{}, "ENG", "NOPE", "zzz").Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no team with the key NOPE, ZZZ") {
		t.Fatalf("error = %v", err)
	}
	if _, ok := s.Scope(record.Linear, "acme"); ok {
		t.Error("a scope was written")
	}
	if n := len(lOf(f.take(), "issues")); n != 0 {
		t.Errorf("%d issue pages were read", n)
	}
}

func TestLinearABadKeyIsAnErrorThatDoesNotShowIt(t *testing.T) {
	f := newLinear(t)
	s := openStore(t, t.TempDir())
	l := linearSyncer(f, s, Options{})
	l.C.Key = "lin_api_wrong_key"
	_, err := l.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "LINEAR_API_KEY") || strings.Contains(err.Error(), "lin_api_wrong_key") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinearAWorkspaceNameTheStoreCannotHoldIsAnError(t *testing.T) {
	f := newLinear(t)
	f.org = "../up"
	s := openStore(t, t.TempDir())
	_, err := linearSyncer(f, s, Options{}).Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cannot name a scope") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinearHalvesThePageWhenLinearFindsItTooComplex(t *testing.T) {
	f := newLinear(t)
	f.maxPage = 10
	var want []string
	for i := 1; i <= 23; i++ {
		// All in the first slice, so it takes several pages.
		f.add(fmt.Sprintf("u%d", i), fmt.Sprintf("ENG-%d", i), "started", day(1), day(1).Add(time.Duration(i)*time.Minute), nil)
		want = append(want, fmt.Sprintf("ENG-%d", i))
	}
	sort.Strings(want)
	s := openStore(t, t.TempDir())
	var lines []string
	opt := Options{Logf: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	res, err := linearSyncer(f, s, opt).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if k := lKeys(linearTickets(t, s)); !reflect.DeepEqual(k, want) || res.Tickets.Added != 23 {
		t.Errorf("stored %d tickets, added %d", len(k), res.Tickets.Added)
	}
	// 50, 25 and 13 are refused. 7 is answered, and the size stays.
	var sizes []int
	for _, c := range lOf(f.take(), "issues")[:7] {
		sizes = append(sizes, c.size)
	}
	if !reflect.DeepEqual(sizes, []int{50, 25, 13, 7, 7, 7, 7}) {
		t.Errorf("page sizes = %v", sizes)
	}
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "retrying with 25 per page") {
		t.Errorf("log = %v", lines)
	}
}

func TestLinearAStoppedSyncGoesOnFromTheLastSlice(t *testing.T) {
	f := newLinear(t)
	f.add("u1", "ENG-1", "started", day(1), day(3), nil)
	f.add("u2", "ENG-2", "started", day(1), day(40), nil)
	f.add("u3", "ENG-3", "backlog", day(-80), day(-60), nil)
	dir := t.TempDir()
	s := openStore(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	f.onIssues = func(n int, r *http.Request) {
		if n == 5 {
			cancel()
			<-r.Context().Done()
		}
	}
	res, err := linearSyncer(f, s, Options{}).Sync(ctx)
	var stopped *StoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("error = %v, want a stopped sync", err)
	}
	if res.First || res.Tickets.Total != 1 {
		t.Errorf("result = %+v", res)
	}
	// Four slices finished: 28 days are held.
	sc, st := linearSaved(t, s)
	if !st.High.Equal(day(28)) || st.Open || !sc.SyncedAt.IsZero() {
		t.Errorf("high = %v, open = %v, synced = %v", st.High, st.Open, sc.SyncedAt)
	}
	f.take()
	f.onIssues = nil

	res, err = linearSyncer(f, s, Options{}).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.First || res.Tickets.Added != 2 || res.Tickets.Total != 3 {
		t.Errorf("result = %+v", res)
	}
	issues := lOf(f.take(), "issues")
	if got := str(nested(issues[0].filter, "updatedAt", "gte")); got != ts(day(28).Add(-10*time.Minute)) {
		t.Errorf("the next sync starts at %s", got)
	}
	if k := lKeys(linearTickets(t, s)); !reflect.DeepEqual(k, []string{"ENG-1", "ENG-2", "ENG-3"}) {
		t.Errorf("stored %v", k)
	}
	if _, st := linearSaved(t, s); !st.Open || !st.High.Equal(now) {
		t.Errorf("state = %+v", st)
	}
}

func TestLinearReadsTheRestOfALongStateHistory(t *testing.T) {
	f := newLinear(t)
	// Started on day 12 and done on day 13, which is all the issues query
	// carries. Then opened again on day 14, started on day 15 and done on day
	// 19. Linear still says completedAt day 13.
	start := history("started", day(12), "completed", day(13))
	start["pageInfo"] = obj{"hasNextPage": true, "endCursor": "h1"}
	f.add("u1", "ENG-1", "completed", day(10), day(19), obj{"startedAt": ts(day(12)), "completedAt": ts(day(13)), "stateHistory": start})
	f.more = map[string][][]obj{"u1": {
		history("unstarted", day(14), "started", day(15))["nodes"].([]obj),
		history("completed", day(19))["nodes"].([]obj),
	}}
	s := openStore(t, t.TempDir())
	if _, err := linearSyncer(f, s, Options{}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := linearTickets(t, s)["ENG-1"]
	if e.StartedAt == nil || !e.StartedAt.Equal(day(12)) || e.DoneAt == nil || !e.DoneAt.Equal(day(19)) || e.Reopened != 1 {
		t.Errorf("ENG-1: started %v, done %v, reopened %d", e.StartedAt, e.DoneAt, e.Reopened)
	}
	var after []string
	for _, c := range lOf(f.take(), "history") {
		after = append(after, c.after)
	}
	if !reflect.DeepEqual(after, []string{"h1", "h2"}) {
		t.Errorf("history pages asked for: %v", after)
	}
}

func TestLinearRecord(t *testing.T) {
	states := map[string]string{}
	for _, typ := range linearStateTypes {
		states["s-"+typ] = typ
	}
	at := func(n float64) *time.Time { d := day(n); return &d }
	base := func(typ string, more string) string {
		return fmt.Sprintf(`{"id":"u9","identifier":"ENG-9","previousIdentifiers":[],"title":"T","url":"https://linear.app/acme/issue/ENG-9",
			"createdAt":%q,"updatedAt":%q,"state":{"name":"Custom","type":%q},"team":{"key":"ENG"},
			"creator":{"displayName":"ada","app":false},"labels":{"nodes":[]},"attachments":{"nodes":[]},"stateHistory":{"nodes":[]}%s}`,
			ts(day(1)), ts(day(9)), typ, more)
	}
	span := func(typ string, n float64) string {
		return fmt.Sprintf(`{"stateId":"s-%s","startedAt":%q}`, typ, ts(day(n)))
	}
	hist := func(spans ...string) string { return `,"stateHistory":{"nodes":[` + strings.Join(spans, ",") + `]}` }
	cases := []struct {
		name     string
		json     string
		state    string
		started  *time.Time
		done     *time.Time
		closed   *time.Time
		reopened int
	}{
		{"triage is backlog", base("triage", ""), record.TicketBacklog, nil, nil, nil, 0},
		{"backlog", base("backlog", ""), record.TicketBacklog, nil, nil, nil, 0},
		{"unstarted", base("unstarted", ""), record.TicketUnstarted, nil, nil, nil, 0},
		{"a type this version does not know is backlog", base("paused", ""), record.TicketBacklog, nil, nil, nil, 0},
		{"started takes Linear's start time", base("started", `,"startedAt":"`+ts(day(4))+`"`), record.TicketStarted, at(4), nil, nil, 0},
		// Work started on day 2, went back, and started again on day 6.
		// Linear's own field says day 6. The history says day 2.
		{"the first start in the history wins", base("started", `,"startedAt":"`+ts(day(6))+`"`+hist(span("backlog", 1), span("started", 2), span("backlog", 3), span("started", 6))),
			record.TicketStarted, at(2), nil, nil, 0},
		{"a ticket back in the backlog keeps its first start", base("backlog", hist(span("started", 2), span("backlog", 3))), record.TicketBacklog, at(2), nil, nil, 0},
		{"done", base("completed", `,"completedAt":"`+ts(day(8))+`"`), record.TicketDone, nil, at(8), at(8), 0},
		{"done with no time takes the update time", base("completed", ""), record.TicketDone, nil, at(9), at(9), 0},
		// Done on day 3, opened again on day 4, done on day 5, opened on day 6, done on day 8.
		{"opened again twice", base("completed", `,"completedAt":"`+ts(day(8))+`"`+hist(span("started", 2), span("completed", 3), span("started", 4), span("completed", 5), span("unstarted", 6), span("completed", 8))),
			record.TicketDone, at(2), at(8), at(8), 2},
		// Linear keeps the first time an issue was done in completedAt, also
		// after it was opened again and done once more. The history has the
		// last time.
		{"done again takes the last time from the history", base("completed", `,"completedAt":"`+ts(day(3))+`"`+hist(span("started", 2), span("completed", 3), span("unstarted", 4), span("started", 5), span("completed", 7))),
			record.TicketDone, at(2), at(7), at(7), 1},
		{"cancelled again takes the last time from the history", base("canceled", `,"canceledAt":"`+ts(day(3))+`"`+hist(span("canceled", 3), span("started", 4), span("canceled", 6))),
			record.TicketCancelled, at(4), nil, at(6), 0},
		// A history that ends somewhere else leaves Linear's own time.
		{"done with a history that ends elsewhere takes completedAt", base("completed", `,"completedAt":"`+ts(day(8))+`"`+hist(span("started", 2))),
			record.TicketDone, at(2), at(8), at(8), 0},
		// Done, then cancelled: that is not opened again.
		{"done then cancelled is not opened again", base("canceled", `,"completedAt":"`+ts(day(3))+`","canceledAt":"`+ts(day(5))+`"`+hist(span("completed", 3), span("canceled", 5))),
			record.TicketCancelled, nil, nil, at(5), 0},
		{"cancelled then opened is not counted: it was never done", base("started", hist(span("canceled", 3), span("started", 5))), record.TicketStarted, at(5), nil, nil, 0},
		{"cancelled", base("canceled", `,"canceledAt":"`+ts(day(7))+`"`), record.TicketCancelled, nil, nil, at(7), 0},
		{"a duplicate is cancelled, closed when archived", base("duplicate", `,"archivedAt":"`+ts(day(8))+`"`), record.TicketCancelled, nil, nil, at(8), 0},
		{"cancelled with no time takes the update time", base("canceled", ""), record.TicketCancelled, nil, nil, at(9), 0},
		{"in the trash is cancelled", base("started", `,"trashed":true,"startedAt":"`+ts(day(2))+`"`), record.TicketCancelled, at(2), nil, at(9), 0},
		{"a done ticket in the trash is not done", base("completed", `,"trashed":true,"completedAt":"`+ts(day(8))+`","archivedAt":"`+ts(day(8.5))+`"`), record.TicketCancelled, nil, nil, at(8.5), 0},
		{"a state the workspace no longer has is skipped", base("started", `,"stateHistory":{"nodes":[{"stateId":"gone","startedAt":"`+ts(day(1))+`"},`+span("started", 3)+`]}`), record.TicketStarted, at(3), nil, nil, 0},
	}
	eq := func(a, b *time.Time) bool {
		if a == nil || b == nil {
			return a == b
		}
		return a.Equal(*b)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var n linearIssue
			if err := json.Unmarshal([]byte(c.json), &n); err != nil {
				t.Fatal(err)
			}
			got, links := linearRecord(&n, states)
			if got.State != c.state || !eq(got.StartedAt, c.started) || !eq(got.DoneAt, c.done) || !eq(got.ClosedAt, c.closed) || got.Reopened != c.reopened {
				t.Errorf("state %s, started %v, done %v, closed %v, reopened %d", got.State, got.StartedAt, got.DoneAt, got.ClosedAt, got.Reopened)
			}
			// The lists are never nil: the store reads an empty list back as
			// an empty one, and a nil here would count as a change each sync.
			if got.Aliases == nil || got.Labels == nil {
				t.Error("a list is nil")
			}
			if got.RawState != "Custom" || len(links) != 0 {
				t.Errorf("raw state %q, %d links", got.RawState, len(links))
			}
		})
	}
}

func TestLinearRecordNamesAndAuthors(t *testing.T) {
	decode := func(more string) record.Ticket {
		t.Helper()
		var n linearIssue
		j := `{"id":"u9","identifier":"OPS-4","createdAt":"2026-08-02T00:00:00Z","updatedAt":"2026-08-03T00:00:00Z","state":{"name":"Todo","type":"unstarted"},"team":{"key":"OPS"}` + more + `}`
		if err := json.Unmarshal([]byte(j), &n); err != nil {
			t.Fatal(err)
		}
		got, _ := linearRecord(&n, nil)
		return got
	}
	// A ticket that moved from ENG to OPS keeps its old key as an alias.
	if got := decode(`,"previousIdentifiers":["ENG-9","OPS-4",""]`); !reflect.DeepEqual(got.Aliases, []string{"ENG-9"}) || got.Key != "OPS-4" || got.ID != "linear:u9" {
		t.Errorf("aliases = %v, key = %s, id = %s", got.Aliases, got.Key, got.ID)
	}
	if got := decode(`,"creator":{"displayName":"ada","app":false}`); got.Author != (record.Actor{Login: "ada", Kind: record.ActorUser}) {
		t.Errorf("a person: %+v", got.Author)
	}
	if got := decode(`,"creator":{"displayName":"Triage Bot","app":true}`); got.Author != (record.Actor{Login: "Triage Bot", Kind: record.ActorBot}) {
		t.Errorf("an app user: %+v", got.Author)
	}
	if got := decode(`,"creator":null,"botActor":{"name":"GitHub"}`); got.Author != (record.Actor{Login: "GitHub", Kind: record.ActorBot}) {
		t.Errorf("an integration: %+v", got.Author)
	}
	if got := decode(`,"creator":null,"botActor":{"name":null}`); got.Author != (record.Actor{Kind: record.ActorBot}) {
		t.Errorf("an integration with no name: %+v", got.Author)
	}
	// The creator's account was deleted.
	if got := decode(``); got.Author != (record.Actor{Kind: record.ActorUser}) {
		t.Errorf("nobody: %+v", got.Author)
	}
	if got := decode(`,"labels":{"nodes":[{"name":"bug"},{"name":"p1"}]}`); !reflect.DeepEqual(got.Labels, []string{"bug", "p1"}) {
		t.Errorf("labels = %v", got.Labels)
	}
}

func TestPullRequestOf(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/acme/web/pull/12", "github:acme/web#12"},
		{"https://github.com/acme/web/pull/12/files", "github:acme/web#12"},
		{"https://github.com/acme/web/pull/12#issuecomment-1", "github:acme/web#12"},
		{"https://github.com/acme/web/pull/12?diff=split", "github:acme/web#12"},
		{"https://github.example.com/Acme/my.repo/pull/3", "github:Acme/my.repo#3"},
		{"https://github.com/acme/web/pulls", ""},
		{"https://github.com/acme/web/pull/abc", ""},
		{"https://github.com/acme/web/pull/0", ""},
		{"https://github.com/acme/web/issues/12", ""},
		{"https://gitlab.com/acme/group/web/pull/12", ""},
		{"https://github.com/acme/pull/12", ""},
		{"/acme/web/pull/12", ""},
		{"ftp://github.com/acme/web/pull/12", ""},
		{"not a url /pull/ 1", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, ok := pullRequestOf(c.url)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%q = %q, %v, want %q", c.url, got, ok, c.want)
		}
	}
}

// A ticket is ready when it first leaves triage and the backlog, whatever
// state it goes to. One still in the backlog is not ready.
func TestLinearReadyIsWhenATicketLeavesTheBacklog(t *testing.T) {
	states := map[string]string{}
	for _, s := range linearStateTypes {
		states["s-"+s] = s
	}
	issue := func(now string, spans ...any) *linearIssue {
		n := &linearIssue{ID: "u1", Identifier: "ENG-1", CreatedAt: day(10), UpdatedAt: day(20)}
		n.State.Type = now
		for i := 0; i+1 < len(spans); i += 2 {
			n.StateHistory.Nodes = append(n.StateHistory.Nodes, linearSpan{StateID: "s-" + spans[i].(string), StartedAt: spans[i+1].(time.Time)})
		}
		return n
	}
	for name, c := range map[string]struct {
		n    *linearIssue
		want *time.Time
	}{
		"backlog, then todo, then started": {issue("started", "backlog", day(10), "unstarted", day(13), "started", day(15)), ptr(day(13))},
		"straight from backlog to started": {issue("started", "triage", day(10), "backlog", day(11), "started", day(15)), ptr(day(15))},
		"made in todo":                     {issue("unstarted", "unstarted", day(10)), ptr(day(10))},
		"still in the backlog":             {issue("backlog", "triage", day(10), "backlog", day(12)), nil},
		"no history, in todo":              {issue("unstarted"), ptr(day(10))},
		"no history, in the backlog":       {issue("backlog"), nil},
		"back to the backlog after todo":   {issue("backlog", "backlog", day(10), "unstarted", day(12), "backlog", day(14)), ptr(day(12))},
	} {
		got, _ := linearRecord(c.n, states)
		if !eqTime(got.ReadyAt, c.want) {
			t.Errorf("%s: ReadyAt = %v, want %v", name, got.ReadyAt, c.want)
		}
	}
}

// A store whose tickets were read before ReadyAt reads them all again once.
func TestTicketsAreReadAgainOnceForANewField(t *testing.T) {
	w := &ticketWindow{Floor: day(0), High: day(80), Open: true}
	var spans [][2]time.Time
	saves := 0
	x := windowRead{o: Options{Since: day(0), Now: day(90)}, w: w, slice: 30 * 24 * time.Hour,
		span: func(_ context.Context, from, to time.Time) error {
			spans = append(spans, [2]time.Time{from, to})
			return nil
		},
		save: func() error { saves++; return nil }}
	if err := x.updated(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(spans) == 0 || !spans[0][0].Equal(day(0)) || w.Rev != ticketRev || w.Open {
		t.Fatalf("spans %v, window %+v: want a read from the floor, rev %d, open tickets to read again", spans, w, ticketRev)
	}
	// The next sync, ten days on, reads only what changed since.
	spans, w.Open = nil, true
	x.o.Now = day(100)
	if err := x.updated(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || !spans[0][0].Equal(day(90)) {
		t.Errorf("second sync read %v, want only the stretch after the last one", spans)
	}
}
