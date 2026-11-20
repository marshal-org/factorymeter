package measure

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

const lib = "acme/lib"

// in puts a change into another repository.
func in(name string) opt {
	return func(c *record.Change) { c.Repo, c.ID = name, record.ChangeID(record.GitHub, name, c.Number) }
}

func branch(name string) opt { return func(c *record.Change) { c.HeadRef = name } }
func titled(s string) opt    { return func(c *record.Change) { c.Title = s } }
func body(s string) opt      { return func(c *record.Change) { c.Body = s } }

// linearStore writes two repositories and a Linear workspace to a store.
//
// Days before the end, and what ties each ticket to a pull request:
//
//	ENG-1  created 14, started 11 in Linear   lib#9 (opened 12) names it in its description,
//	                                          app#1 (opened 10) in its branch
//	ENG-2  created 9                          lib#1 (opened 8) names it in its title, in lower case
//	ENG-3  created 7, started 5, was OPS-7    app#2 (opened 6) names the old key in its title
//	ENG-4  created 5, started 2               app#4 (opened 3) is attached in Linear
//	ENG-5  created 4                          nothing
//	ENG-6  created 20, started 19, done 15    nothing
//
// app#3 names keys that are not there: ENG-22, XENG-2, utf-8.
func linearStore(t *testing.T, linearSynced time.Time) *store.Store {
	t.Helper()
	at := func(days float64) time.Time { return end.Add(time.Duration(days * float64(day))) }
	ptr := func(v time.Time) *time.Time { return &v }
	s, err := store.OpenWrite(t.TempDir(), "acme", end)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	must := func(_, _ int, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{repo, lib} {
		if err := s.PutScope(store.Scope{Source: record.GitHub, Name: name, Private: true, From: end.Add(-26 * week), SyncedAt: end}); err != nil {
			t.Fatal(err)
		}
	}
	must(store.Changes.Put(s, record.GitHub, repo, []record.Change{
		change(1, at(-10), branch("ada/eng-1-login")),
		change(2, at(-6), titled("Fix OPS-7: retry the call")),
		change(3, at(-4), body("Reads utf-8. This is not ENG-22 and not XENG-2.")),
		change(4, at(-3)),
	}))
	must(store.Changes.Put(s, record.GitHub, lib, []record.Change{
		change(1, at(-8), in(lib), titled("eng-2: a parser")),
		change(9, at(-12), in(lib), body("Groundwork for ENG-1.\n\nAnd ENG-1 again.")),
	}))
	// An issue of a repository stays with its repository.
	must(store.Tickets.Put(s, record.GitHub, repo, []record.Ticket{{Source: record.GitHub, ID: record.TicketID(record.GitHub, repo+"#50"),
		Key: repo + "#50", Project: repo, Title: "An issue", State: record.TicketUnstarted, CreatedAt: at(-3), UpdatedAt: at(-3), Aliases: []string{}, Labels: []string{}}}))

	if err := s.PutScope(store.Scope{Source: record.Linear, Name: "acme", Private: true, From: end.Add(-26 * week), SyncedAt: linearSynced}); err != nil {
		t.Fatal(err)
	}
	ticket := func(n int, state string, created float64, started, done *time.Time, aliases ...string) record.Ticket {
		key := "ENG-" + string(rune('0'+n))
		if aliases == nil {
			aliases = []string{}
		}
		return record.Ticket{Source: record.Linear, ID: record.TicketID(record.Linear, "u"+key[4:]), Key: key, Aliases: aliases, Project: "ENG",
			Title: "Ticket " + key, URL: "https://linear.app/acme/issue/" + key, State: state, CreatedAt: at(created), UpdatedAt: at(created),
			StartedAt: started, DoneAt: done, ClosedAt: done, Labels: []string{}}
	}
	must(store.Tickets.Put(s, record.Linear, "acme", []record.Ticket{
		ticket(1, record.TicketStarted, -14, ptr(at(-11)), nil),
		ticket(2, record.TicketUnstarted, -9, nil, nil),
		ticket(3, record.TicketStarted, -7, ptr(at(-5)), nil, "OPS-7"),
		ticket(4, record.TicketStarted, -5, ptr(at(-2)), nil),
		ticket(5, record.TicketUnstarted, -4, nil, nil),
		ticket(6, record.TicketDone, -20, ptr(at(-19)), ptr(at(-15))),
	}))
	attached := func(from, to string) record.Link {
		return record.Link{Kind: record.LinkMentions, From: from, FromKind: record.KindChange, To: to, ToKind: record.KindTicket, Found: "source"}
	}
	must(store.Links.Put(s, record.Linear, "acme", []record.Link{
		attached("github:Acme/App#4", "linear:u4"),
		// A pull request the store does not hold, and a repository it does not hold.
		attached("github:acme/app#99", "linear:u5"),
		attached("github:other/thing#5", "linear:u5"),
	}))
	return s
}

func TestLoadTiesLinearTicketsToPullRequests(t *testing.T) {
	s := linearStore(t, end)
	l, err := Load(s, registry(t), "acme")
	if err != nil {
		t.Fatal(err)
	}
	// A Linear team is not a repository.
	if !reflect.DeepEqual(l.names, []string{repo, lib}) || len(l.repos) != 2 {
		t.Fatalf("names = %v, %d repositories", l.names, len(l.repos))
	}
	// In the order they started: ENG-6 a day after it was created (Linear),
	// ENG-1 two days (lib#9 opened before Linear's own start), ENG-2 one day,
	// ENG-3 one day (app#2 opened before Linear's start), ENG-4 two days.
	if got := vals(l.obsOf("spec.wait", sel{})); !same(got, []float64{24, 48, 24, 24, 48}) {
		t.Errorf("spec.wait = %v", got)
	}
	// Each ticket counts for the repository of its first pull request.
	if got := vals(l.obsOf("spec.wait", sel{by: byRepo, repo: 0})); !same(got, []float64{24, 48}) {
		t.Errorf("spec.wait of %s = %v, want ENG-3 and ENG-4", repo, got)
	}
	if got := vals(l.obsOf("spec.wait", sel{by: byRepo, repo: 1})); !same(got, []float64{48, 24}) {
		t.Errorf("spec.wait of %s = %v, want ENG-1 and ENG-2", lib, got)
	}
	// Six tickets and the issue stood in the queue. ENG-5 and the issue still do.
	var open, none int
	for _, st := range l.stays["spec.queue"] {
		if st.to.IsZero() {
			open++
		}
		if st.k.repo < 0 {
			none++
		}
	}
	if n := len(l.stays["spec.queue"]); n != 7 || open != 2 || none != 2 {
		t.Errorf("spec.queue: %d stays, %d open, %d of no repository; want 7, 2 (ENG-5 and the issue), 2 (ENG-5 and ENG-6)", n, open, none)
	}

	r, err := l.Report(Options{Target: "acme", Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	var tickets string
	for _, sc := range r.Coverage.Sources {
		if sc.Kind == record.KindTicket {
			tickets = sc.Source + " | " + sc.Note
			if sc.Records != 6 {
				// Five Linear tickets and the issue. ENG-1 was created at the
				// very start of the window of two weeks, ENG-6 before it.
				t.Errorf("%d tickets created in the window, want 6", sc.Records)
			}
		}
	}
	if !strings.HasPrefix(tickets, "github, linear | ") || !strings.Contains(tickets, "A GitHub issue has no started state") || !strings.Contains(tickets, "A Linear ticket starts when") {
		t.Errorf("ticket coverage = %q", tickets)
	}
	limits := strings.Join(r.Limits, "\n")
	for _, want := range []string{
		"counts as linked to a Linear ticket when Linear has it attached",
		"A Linear ticket counts for the repository of the first pull request linked to it.",
	} {
		if !strings.Contains(limits, want) {
			t.Errorf("the limits do not say %q:\n%s", want, limits)
		}
	}
	for _, not := range []string{"The first sync of Linear", "The last sync of Linear", "Only the Linear tickets"} {
		if strings.Contains(limits, not) {
			t.Errorf("the limits say %q", not)
		}
	}
	if len(r.Coverage.Repos) != 2 {
		t.Errorf("coverage lists %d repositories, want 2", len(r.Coverage.Repos))
	}

	// A summary names no ticket, no team and no workspace.
	sum, err := l.Report(Options{Target: "acme", Weeks: 2, History: 4, Now: end, Summary: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sum)
	for _, not := range []string{"acme", "ENG-", "OPS-", "linear.app", "Ticket ENG"} {
		if strings.Contains(string(raw), not) {
			t.Errorf("the summary holds %q", not)
		}
	}
}

func TestLoadOfOneRepositoryCountsItsLinkedTicketsOnly(t *testing.T) {
	s := linearStore(t, end)
	l, err := Load(s, registry(t), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(l.names, []string{repo}) {
		t.Fatalf("names = %v", l.names)
	}
	// lib#9 is not read, so ENG-1 starts with Linear, three days after it was
	// created. Then ENG-3 and ENG-4 as in the report of the owner.
	if got := vals(l.obsOf("spec.wait", sel{})); !same(got, []float64{72, 24, 48}) {
		t.Errorf("spec.wait = %v", got)
	}
	r, err := l.Report(Options{Target: repo, Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	limits := strings.Join(r.Limits, "\n")
	// ENG-2, ENG-5 and ENG-6.
	if want := "Only the Linear tickets that a pull request of this repository is linked to are counted. 3 tickets in the store are left out."; !strings.Contains(limits, want) {
		t.Errorf("the limits do not say %q:\n%s", want, limits)
	}
	if strings.Contains(limits, "counts for the repository of the first pull request") {
		t.Error("the report of one repository explains how tickets are split by repository")
	}
	// Six tickets, three of them linked, and the key prefixes of their teams.
	if got := r.Coverage.Trackers; len(got) != 1 || got[0].Source != "linear" || got[0].Name != "acme" || got[0].Tickets != 6 || got[0].Linked != 3 ||
		!reflect.DeepEqual(got[0].Keys, []string{"ENG", "OPS"}) || got[0].SyncedAt.IsZero() {
		t.Errorf("trackers = %+v", got)
	}
	// A summary names no workspace and no key.
	sum, err := l.Report(Options{Target: repo, Weeks: 2, History: 4, Now: end, Summary: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.Coverage.Trackers; len(got) != 1 || got[0].Name != "" || len(got[0].Keys) != 0 || got[0].Keys == nil || got[0].Tickets != 6 {
		t.Errorf("summary trackers = %+v", got)
	}

	if _, err := Load(s, registry(t), "acme/nope"); err == nil || !strings.Contains(err.Error(), "holds nothing for acme/nope") {
		t.Errorf("a repository the store does not hold: %v", err)
	}
}

func TestReportSaysWhenLinearIsBehind(t *testing.T) {
	cases := []struct {
		name   string
		synced time.Time
		want   string
	}{
		{"three days old", end.Add(-3 * day), "The last sync of Linear is more than a day older than the end of the window."},
		{"never finished", time.Time{}, "The first sync of Linear has not finished."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := Load(linearStore(t, c.synced), registry(t), "acme")
			if err != nil {
				t.Fatal(err)
			}
			r, err := l.Report(Options{Target: "acme", Weeks: 2, History: 4, Now: end})
			if err != nil {
				t.Fatal(err)
			}
			if limits := strings.Join(r.Limits, "\n"); !strings.Contains(limits, c.want) {
				t.Errorf("the limits do not say %q:\n%s", c.want, limits)
			}
		})
	}
}

// A Jira site attaches no pull request to a ticket, so only the key ties the
// two, and the report says so.
//
//	PAY-1  created 6               app#1 (opened 4) names it in its title
//	PAY-2  created 9, started 8    app#2 (opened 5) names it in its branch
//	PAY-3  created 3               nothing
func TestLoadTiesJiraTicketsToPullRequestsByKey(t *testing.T) {
	at := func(days float64) time.Time { return end.Add(time.Duration(days * float64(day))) }
	s, err := store.OpenWrite(t.TempDir(), "acme", end)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(_, _ int, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const site = "acme.atlassian.net"
	for _, sc := range []store.Scope{
		{Source: record.GitHub, Name: repo, Private: true, From: end.Add(-26 * week), SyncedAt: end},
		{Source: record.Jira, Name: site, Private: true, From: end.Add(-26 * week), SyncedAt: end},
	} {
		if err := s.PutScope(sc); err != nil {
			t.Fatal(err)
		}
	}
	must(store.Changes.Put(s, record.GitHub, repo, []record.Change{
		change(1, at(-4), titled("PAY-1: charge the card")),
		change(2, at(-5), branch("ada/pay-2-refund")),
	}))
	ticket := func(n int, state string, created float64, started *time.Time) record.Ticket {
		key := "PAY-" + string(rune('0'+n))
		return record.Ticket{Source: record.Jira, ID: record.TicketID(record.Jira, site+"/1000"+key[4:]), Key: key, Aliases: []string{}, Project: "PAY",
			Title: "Ticket " + key, URL: "https://" + site + "/browse/" + key, State: state, CreatedAt: at(created), UpdatedAt: at(created),
			StartedAt: started, Labels: []string{}}
	}
	started := at(-8)
	must(store.Tickets.Put(s, record.Jira, site, []record.Ticket{
		ticket(1, record.TicketUnstarted, -6, nil),
		ticket(2, record.TicketStarted, -9, &started),
		ticket(3, record.TicketUnstarted, -3, nil),
	}))

	l, err := Load(s, registry(t), "acme")
	if err != nil {
		t.Fatal(err)
	}
	// PAY-2 starts in Jira a day after it was created, before its pull
	// request. PAY-1 starts with its pull request, two days after.
	if got := vals(l.obsOf("spec.wait", sel{})); !same(got, []float64{24, 48}) {
		t.Errorf("spec.wait = %v, want 24 and 48", got)
	}
	r, err := l.Report(Options{Target: "acme", Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range r.Coverage.Sources {
		if sc.Kind == record.KindTicket && (sc.Source != "jira" || sc.Records != 3 || !strings.Contains(sc.Note, "A Jira ticket starts when")) {
			t.Errorf("ticket coverage = %+v", sc)
		}
	}
	limits := strings.Join(r.Limits, "\n")
	if want := "A pull request counts as linked to a Jira ticket when its branch, title or description names the key of the ticket. A word that only looks like a key is taken for one. A ticket deleted in Jira stays in the store."; !strings.Contains(limits, want) {
		t.Errorf("the limits do not say %q:\n%s", want, limits)
	}
	if strings.Contains(limits, "attached") {
		t.Errorf("the limits speak of a pull request that Jira has attached:\n%s", limits)
	}
}

// A store can hold a Linear workspace and a Jira site, as it does while a
// team moves from one to the other. A key that both have goes to Linear.
func TestAKeyInLinearAndInJiraGoesToLinear(t *testing.T) {
	var k ticketKeys
	k.id = map[string]string{}
	// The scopes come in the order of their keys: Jira, then Linear.
	k.add(&record.Ticket{Source: record.Jira, ID: "jira:acme.atlassian.net/10001", Key: "ENG-1"})
	k.add(&record.Ticket{Source: record.Jira, ID: "jira:acme.atlassian.net/10002", Key: "ENG-2"})
	k.add(&record.Ticket{Source: record.Linear, ID: "linear:u1", Key: "ENG-1"})
	k.done()
	if got := k.in("ENG-1 and ENG-2"); !reflect.DeepEqual(got, []string{"linear:u1", "jira:acme.atlassian.net/10002"}) {
		t.Errorf("got %v", got)
	}
}

func TestAStoreWithOnlyTicketsOfAnotherSystemHoldsNothing(t *testing.T) {
	s, err := store.OpenWrite(t.TempDir(), "acme", end)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutScope(store.Scope{Source: record.Linear, Name: "acme", Private: true, SyncedAt: end}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(s, registry(t), "acme"); err == nil || !strings.Contains(err.Error(), "factorymeter sync acme") {
		t.Errorf("error = %v", err)
	}
}

func TestTicketKeysInText(t *testing.T) {
	var k ticketKeys
	k.id = map[string]string{}
	add := func(id, key string, aliases ...string) {
		k.add(&record.Ticket{ID: id, Key: key, Aliases: aliases})
	}
	add("a", "ENG-1")
	add("b", "ENG-12")
	add("c", "RENG-1")
	add("d", "OPS-3", "ENG-7")
	// This one once had the key that d has now, and the key ENG-9.
	add("e", "WEB-4", "OPS-3", "ENG-9")
	add("f", "X1-2")
	add("g", "broken")
	add("h", "-5")
	add("i", "ENG-123456789")
	k.done()

	cases := []struct {
		text string
		want []string
	}{
		{"ENG-1", []string{"a"}},
		{"eng-1", []string{"a"}},
		{"ada/eng-1-fix-login", []string{"a"}},
		{"feat/ENG-1_and_more", []string{"a"}},
		{"Fix (ENG-1), then ENG-12.", []string{"a", "b"}},
		{"ENG-1 ENG-1 eng-1", []string{"a"}},
		// A longer number is another key.
		{"ENG-123", nil},
		{"ENG-12", []string{"b"}},
		// So is one with more digits than a key is read with.
		{"ENG-123456789", []string{"i"}},
		{"ENG-1234567890", nil},
		// A longer prefix is another team.
		{"RENG-1", []string{"c"}},
		{"XENG-1", nil},
		{"2ENG-1", nil},
		{"my_ENG-1", []string{"a"}},
		// A letter right after the number makes it another word.
		{"ENG-1x", nil},
		{"ENG-12abc ENG-1a", nil},
		{"eng-1-fix eng-12/x eng-12.", []string{"a", "b"}},
		// A key a ticket had before finds it. A key a ticket has now wins.
		{"ENG-7", []string{"d"}},
		{"OPS-3", []string{"d"}},
		{"ENG-9", []string{"e"}},
		{"x1-2", []string{"f"}},
		// Words that look like keys of teams that are not there.
		{"utf-8 sha-256 v1-2 covid-19", nil},
		{"ENG-", nil},
		{"ENG 1", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := k.in(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q = %v, want %v", c.text, got, c.want)
		}
	}
	if got := k.in("see ENG-1", "and RENG-1", "and ENG-1"); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("three texts = %v", got)
	}
	// With no ticket of another system nothing is searched.
	var none ticketKeys
	none.id = map[string]string{}
	none.done()
	if got := none.in("ENG-1"); got != nil {
		t.Errorf("no tickets = %v", got)
	}
}

func TestPullOf(t *testing.T) {
	cases := []struct {
		id, repo string
		num      int
	}{
		{"github:acme/app#4", "acme/app", 4},
		{"github:Acme/App#12", "acme/app", 12},
		{"github:acme/app#0", "", 0},
		{"github:acme/app#x", "", 0},
		{"github:acme/app", "", 0},
		{"github:#4", "", 0},
		{"linear:acme/app#4", "", 0},
		{"", "", 0},
	}
	for _, c := range cases {
		repo, num, ok := pullOf(c.id)
		if ok != (c.repo != "") || (ok && (repo != c.repo || num != c.num)) {
			t.Errorf("%q = %q, %d, %v", c.id, repo, num, ok)
		}
	}
}
