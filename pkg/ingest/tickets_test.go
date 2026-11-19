package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// ticketed is small with issues, and with pull requests that close them.
func ticketed(h *fakeHub) *repo {
	r := small(h)
	r.issue(101, "CLOSED", "COMPLETED", day(-50), day(-40)) // closed before the floor: not wanted
	r.issue(102, "OPEN", "", day(-70), day(-60), "bug")     // open and stale: wanted, for the queue
	r.issue(103, "CLOSED", "COMPLETED", day(-5), day(11))   // created before the floor, closed after it
	r.issue(104, "CLOSED", "NOT_PLANNED", day(12), day(13)) // closed as not planned
	r.issue(105, "OPEN", "", day(30), day(31), "bug", "p2")
	r.prs[0].closes = []string{"acme/app#101"} // finished before the floor: its link is not wanted
	r.prs[1].closes = []string{"acme/app#102"} // open and stale
	r.prs[3].closes = []string{"acme/app#103"}
	r.prs[5].closes = []string{"acme/app#105", "acme/lib#9"} // one in another repository
	return r
}

func tickets(t *testing.T, s *store.Store, repo string) map[string]record.Ticket {
	t.Helper()
	out := map[string]record.Ticket{}
	err := store.Tickets.Each(s, record.GitHub, repo, time.Time{}, time.Time{}, func(_ string, rows []record.Ticket) error {
		for _, r := range rows {
			out[strings.TrimPrefix(r.Key, repo+"#")] = r
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func keysOf(m map[string]record.Ticket) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// links lists the stored links as "change number>ticket key".
func links(t *testing.T, s *store.Store, repo string) string {
	t.Helper()
	var out []string
	err := store.Links.Each(s, record.GitHub, repo, time.Time{}, time.Time{}, func(_ string, rows []record.Link) error {
		for _, l := range rows {
			if l.Kind != record.LinkCloses || l.FromKind != record.KindChange || l.ToKind != record.KindTicket || l.Found != "source" {
				t.Errorf("link %+v", l)
			}
			out = append(out, strings.TrimPrefix(l.From, "github:"+repo+"#")+">"+strings.TrimPrefix(l.To, "github:"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

const allLinks = "2>acme/app#102 4>acme/app#103 6>acme/app#105 6>acme/lib#9"

func TestFirstSyncStoresTicketsAndLinks(t *testing.T) {
	h := newHub(t)
	ticketed(h)
	s := openStore(t, t.TempDir())
	res, err := syncer(h, s, Options{SkipCI: true, SkipDeploys: true}).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	got := tickets(t, s, "acme/app")
	if keysOf(got) != "102 103 104 105" {
		t.Fatalf("stored tickets %s", keysOf(got))
	}
	for key, want := range map[string]string{"102": record.TicketUnstarted, "103": record.TicketDone, "104": record.TicketCancelled, "105": record.TicketUnstarted} {
		if got[key].State != want {
			t.Errorf("ticket %s is %q, want %q", key, got[key].State, want)
		}
	}
	if tk := got["105"]; tk.ID != record.TicketID(record.GitHub, "acme/app#105") || tk.Project != "acme/app" || strings.Join(tk.Labels, " ") != "bug p2" ||
		!tk.UpdatedAt.Equal(day(31)) || tk.ClosedAt != nil {
		t.Errorf("ticket 105 stored as %+v", tk)
	}
	if tk := got["103"]; tk.DoneAt == nil || !tk.DoneAt.Equal(day(11)) {
		t.Errorf("ticket 103 stored as %+v", tk)
	}
	// A link is stored with its change. The change that finished before the
	// floor is not stored, so its link is not.
	if got := links(t, s, "acme/app"); got != allLinks {
		t.Errorf("links %q, want %q", got, allLinks)
	}
	r := res.Repos[0]
	if r.Tickets.Added != 4 || r.Tickets.Total != 4 || r.Links.Added != 4 || r.Links.Total != 4 || len(r.Notes) != 0 {
		t.Errorf("result %+v", r)
	}
	st := stateOf(t, s, "acme/app")
	if !st.Tickets.Floor.Equal(floor) || !st.Tickets.High.Equal(day(31)) || !st.Tickets.Open || !st.Changes.Links {
		t.Errorf("state %+v, links %v", st.Tickets, st.Changes.Links)
	}
	// One read up from the floor, one for the open issues below it.
	calls := of(h.take(), "issues")
	if len(calls) != 2 || calls[0].since != ts(floor) || calls[0].open || calls[1].since != "" || !calls[1].open || calls[0].size != 100 {
		t.Errorf("issues requests %+v", calls)
	}
}

func TestALaterSyncReadsOnlyTheTicketsUpdatedSince(t *testing.T) {
	h := newHub(t)
	r := ticketed(h)
	dir := t.TempDir()
	s := openStore(t, dir)
	opt := Options{SkipCI: true, SkipDeploys: true}
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	h.take()
	s.Close()

	// Nothing moved. The store is read back from its files, and no record
	// counts as changed.
	s = openStore(t, dir)
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	calls := of(h.take(), "issues")
	if len(calls) != 1 || calls[0].since != ts(day(31)) || calls[0].open {
		t.Errorf("issues requests %+v", calls)
	}
	if got := res.Repos[0]; got.Tickets != (Count{Total: 4}) || got.Links != (Count{Total: 4}) || got.Changes.Added+got.Changes.Changed != 0 {
		t.Errorf("a sync with nothing new: %+v", got)
	}

	// An open issue closes, a stale one is reopened, a new one arrives.
	r.issues[4].state, r.issues[4].reason, r.issues[4].updated, r.issues[4].closed = "CLOSED", "COMPLETED", day(92), day(92)
	r.issues[0].state, r.issues[0].reason, r.issues[0].updated, r.issues[0].closed, r.issues[0].reopened = "OPEN", "", day(92.2), time.Time{}, 1
	r.issue(106, "OPEN", "", day(92.5), day(92.5))
	res, err = syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	got := tickets(t, s, "acme/app")
	if keysOf(got) != "101 102 103 104 105 106" || got["105"].State != record.TicketDone || got["101"].Reopened != 1 || got["101"].State != record.TicketUnstarted {
		t.Errorf("stored %s, 105 %+v, 101 %+v", keysOf(got), got["105"], got["101"])
	}
	if c := res.Repos[0].Tickets; c.Added != 2 || c.Changed != 1 || c.Total != 6 {
		t.Errorf("tickets %+v", c)
	}
	if st := stateOf(t, s, "acme/app"); !st.Tickets.High.Equal(day(92.5)) {
		t.Errorf("state %+v", st.Tickets)
	}
}

func TestAnEarlierSinceReadsTheOlderTickets(t *testing.T) {
	h := newHub(t)
	ticketed(h)
	s := openStore(t, t.TempDir())
	opt := Options{SkipCI: true, SkipDeploys: true, Since: day(12)}
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	// Updated since day 12, and the open one from long before.
	if got := keysOf(tickets(t, s, "acme/app")); got != "102 104 105" {
		t.Fatalf("first sync stored %s", got)
	}
	h.take()
	opt.Since = day(-45)
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(tickets(t, s, "acme/app")); got != "101 102 103 104 105" {
		t.Errorf("after widening: %s", got)
	}
	if c := res.Repos[0].Tickets; c.Added != 2 || c.Changed != 0 {
		t.Errorf("tickets %+v", c)
	}
	// The stretch below the old floor, then the issues updated since the
	// last sync. The open issues are stored already.
	calls := of(h.take(), "issues")
	if len(calls) != 2 || calls[0].since != ts(day(-45)) || calls[1].since != ts(day(31)) || calls[0].open || calls[1].open {
		t.Errorf("issues requests %+v", calls)
	}
	if st := stateOf(t, s, "acme/app"); !st.Tickets.Floor.Equal(day(-45)) {
		t.Errorf("state %+v", st.Tickets)
	}
}

func TestAStoppedTicketReadGoesOn(t *testing.T) {
	h := newHub(t)
	r := h.add("acme/app")
	for i := 1; i <= 30; i++ {
		r.issue(i, "OPEN", "", day(float64(i)-0.5), day(float64(i)))
	}
	dir := t.TempDir()
	s := openStore(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	// The fifth page never arrives.
	h.onIssues = func(n int, r *http.Request) {
		if n == 5 {
			cancel()
			<-r.Context().Done()
		}
	}
	// Issues are read four to a pull request, so pages of four.
	opt := Options{PageSize: 1, Checkpoint: 2, SkipCI: true, SkipDeploys: true}
	if _, err := syncer(h, s, opt).Sync(ctx, "acme/app"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a cancel", err)
	}
	if got := len(tickets(t, s, "acme/app")); got != 16 {
		t.Errorf("%d tickets stored after four pages of four, want 16", got)
	}
	if st := stateOf(t, s, "acme/app"); !st.Tickets.High.Equal(day(16)) || st.Tickets.Open {
		t.Errorf("after the stop: %+v", st.Tickets)
	}
	s.Close()

	h.onIssues = nil
	h.take()
	s = openStore(t, dir)
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	calls := of(h.take(), "issues")
	if calls[0].since != ts(day(16)) || calls[0].after != "" {
		t.Errorf("the second sync started at %+v, want the saved time", calls[0])
	}
	// Issue 16 is read again and found the same.
	if c := res.Repos[0].Tickets; c.Added != 14 || c.Changed != 0 || c.Total != 30 {
		t.Errorf("tickets %+v", c)
	}
}

func TestTheIssuePageShrinksWhenGitHubTimesOut(t *testing.T) {
	h := newHub(t)
	r := h.add("acme/app")
	for i := 1; i <= 20; i++ {
		r.issue(i, "OPEN", "", day(float64(i)-0.5), day(float64(i)))
	}
	h.maxIssues = 7
	s := openStore(t, t.TempDir())
	if _, err := syncer(h, s, Options{SkipCI: true, SkipDeploys: true}).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if got := len(tickets(t, s, "acme/app")); got != 20 {
		t.Errorf("%d tickets stored, want 20", got)
	}
	sizes := map[int]int{}
	for _, c := range of(h.take(), "issues") {
		sizes[c.size]++
	}
	// Each bigger size times out once. The read of the open issues keeps the
	// size that worked.
	if sizes[100] != 1 || sizes[50] != 1 || sizes[25] != 1 || sizes[13] != 1 || sizes[7] != 4 {
		t.Errorf("page sizes asked for: %v", sizes)
	}
}

func TestIssuesThatCannotBeReadAreANote(t *testing.T) {
	h := newHub(t)
	ticketed(h)
	h.issuesErr = "FORBIDDEN"
	s := openStore(t, t.TempDir())
	opt := Options{SkipCI: true, SkipDeploys: true}
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	r := res.Repos[0]
	if len(r.Notes) != 1 || !strings.HasPrefix(r.Notes[0], "Issues could not be read: ") {
		t.Errorf("notes %q", r.Notes)
	}
	// The changes and their links are stored all the same.
	if r.Changes.Added != 5 || r.Links.Added != 4 || r.Tickets.Total != 0 {
		t.Errorf("result %+v", r)
	}
	// The next sync tries again, from the floor.
	h.issuesErr = ""
	h.take()
	res, err = syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Repos[0]; r.Tickets.Added != 4 || len(r.Notes) != 0 {
		t.Errorf("the next sync: %+v", r)
	}
	if calls := of(h.take(), "issues"); len(calls) != 2 || calls[0].since != ts(floor) {
		t.Errorf("issues requests %+v", calls)
	}
}

func TestSkipTickets(t *testing.T) {
	h := newHub(t)
	ticketed(h)
	s := openStore(t, t.TempDir())
	res, err := syncer(h, s, Options{SkipCI: true, SkipDeploys: true, SkipTickets: true}).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if calls := of(h.take(), "issues"); len(calls) != 0 {
		t.Errorf("issues requests %+v", calls)
	}
	if r := res.Repos[0]; r.Tickets.Total != 0 || len(r.Notes) != 0 {
		t.Errorf("result %+v", r)
	}
	st := stateOf(t, s, "acme/app")
	if !st.Tickets.Floor.IsZero() || st.Tickets.Open || st.Changes.Links {
		t.Errorf("state %+v, links %v", st.Tickets, st.Changes.Links)
	}
	if got := links(t, s, "acme/app"); got != "" {
		t.Errorf("links %q, want none", got)
	}
}

// A token that cannot read issues is refused the pull requests too, because
// the page asks what each one closes. The error says how to go on, and
// --skip-tickets leaves the question out.
func TestATokenThatCannotReadIssues(t *testing.T) {
	h := newHub(t)
	ticketed(h)
	h.noIssues = true
	s := openStore(t, t.TempDir())
	opt := Options{SkipCI: true, SkipDeploys: true}
	_, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err == nil || !errors.Is(err, github.ErrForbidden) || !strings.Contains(err.Error(), "read-only access to Issues, or pass --skip-tickets") {
		t.Fatalf("err %v", err)
	}
	h.take()

	opt.SkipTickets = true
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Repos[0]; r.Changes.Added != 5 || r.Links.Total != 0 || r.Tickets.Total != 0 || len(r.Notes) != 0 {
		t.Errorf("result %+v", r)
	}
	for _, c := range h.take() {
		if c.kind == "issues" || c.links {
			t.Errorf("asked about issues: %+v", c)
		}
	}
	// A second sync that skips tickets reads the top and no more.
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if calls := h.take(); len(calls) != 1 || calls[0].links {
		t.Errorf("requests %+v", calls)
	}

	// The token gets its access. The window is read once more for the links.
	h.noIssues, opt.SkipTickets = false, false
	res, err = syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := links(t, s, "acme/app"); got != allLinks {
		t.Errorf("links %q, want %q", got, allLinks)
	}
	if r := res.Repos[0]; r.Changes.Added+r.Changes.Changed != 0 || r.Links.Added != 4 || r.Tickets.Added != 4 {
		t.Errorf("result %+v", r)
	}
	if st := stateOf(t, s, "acme/app"); !st.Changes.Links {
		t.Error("the state does not say the links are stored")
	}
}

// A store written before links were read holds changes without them. The
// first sync after the upgrade reads the window once more.
func TestAStoreWithoutLinksReadsItsWindowOnceMore(t *testing.T) {
	h := newHub(t)
	r := small(h)
	s := openStore(t, t.TempDir())
	opt := Options{SkipCI: true, SkipDeploys: true, SkipTickets: true}
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	opt.SkipTickets = false
	// The links were there all along. The older version did not ask.
	r.prs[0].closes = []string{"acme/app#101"}
	r.prs[1].closes = []string{"acme/app#102"}
	r.prs[3].closes = []string{"acme/app#103"}
	r.prs[5].closes = []string{"acme/app#105", "acme/lib#9"}
	sc, _ := s.Scope(record.GitHub, "acme/app")
	var raw map[string]json.RawMessage
	var changes map[string]any
	if err := json.Unmarshal(sc.State, &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw["changes"], &changes); err != nil {
		t.Fatal(err)
	}
	delete(changes, "links")
	raw["changes"], _ = json.Marshal(changes)
	sc.State, _ = json.Marshal(raw)
	if err := s.PutScope(sc); err != nil {
		t.Fatal(err)
	}
	h.take()

	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := links(t, s, "acme/app"); got != allLinks {
		t.Errorf("links %q, want %q", got, allLinks)
	}
	// The changes are compared, not written again.
	if got := res.Repos[0]; got.Changes.Added+got.Changes.Changed != 0 || got.Links.Added != 4 {
		t.Errorf("result %+v", got)
	}
	st := stateOf(t, s, "acme/app")
	if !st.Changes.Links || !st.Changes.Open || !st.Changes.High.Equal(day(91)) {
		t.Errorf("state %+v", st.Changes)
	}
	// The window from the top, and the open changes below the floor.
	if calls := of(h.take(), "prs"); len(calls) != 2 || calls[0].open || !calls[1].open {
		t.Errorf("requests %+v", calls)
	}

	// Once is enough.
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if calls := of(h.take(), "prs"); len(calls) != 1 {
		t.Errorf("the sync after read %d pages of pull requests, want 1", len(calls))
	}
}
