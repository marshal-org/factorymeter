package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/ingest"
	"github.com/marshal-org/factorymeter/pkg/record"
)

const (
	fakeJiraEmail = "ada@example.com"
	fakeJiraToken = "jira_api_token_that_must_not_be_printed"
)

var (
	jqlFrom = regexp.MustCompile(`updated >= "([0-9-]{10})"`)
	jqlTo   = regexp.MustCompile(`updated < "([0-9-]{10})"`)
	jqlIn   = regexp.MustCompile(`project in \(([^)]*)\)`)
)

// fakeJira answers a Jira Cloud site with the projects ENG and OPS and two
// tickets of ENG, both updated a day ago:
//
//	ENG-1  created five days ago, in To Do, never started in Jira
//	ENG-2  created four days ago, moved to In Progress a day ago
//
// It answers GET requests only, and only to the account of the test.
func fakeJira(t *testing.T, now time.Time, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	jts := func(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000-0700") }
	updated := now.Add(-24 * time.Hour)
	status := func(id, name, category string) obj {
		return obj{"id": id, "name": name, "statusCategory": obj{"key": category}}
	}
	todo, doing := status("1", "To Do", "new"), status("3", "In Progress", "indeterminate")
	issue := func(n int, st obj, created time.Time, history ...obj) obj {
		key := fmt.Sprintf("ENG-%d", n)
		return obj{"id": fmt.Sprintf("1000%d", n), "key": key,
			"fields": obj{"summary": "Ticket " + key, "created": jts(created), "updated": jts(updated), "resolutiondate": nil, "resolution": nil,
				"status": st, "labels": []string{}, "issuetype": obj{"name": "Task", "hierarchyLevel": 0}, "project": obj{"key": "ENG"},
				"creator": obj{"displayName": "Ada", "accountType": "atlassian", "emailAddress": fakeJiraEmail}},
			"changelog": obj{"startAt": 0, "maxResults": len(history), "total": len(history), "histories": history}}
	}
	day := func(m []string) (time.Time, bool) {
		if m == nil {
			return time.Time{}, false
		}
		at, err := time.Parse("2006-01-02", m[1])
		return at, err == nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: Jira is asked with GET only", r.Method, r.URL.Path)
		}
		if email, tok, ok := r.BasicAuth(); !ok || email != fakeJiraEmail || tok != fakeJiraToken {
			t.Errorf("%s was asked as %q, want the account of the test", r.URL.Path, email)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "factorymeter/") {
			t.Errorf("User-Agent = %q", got)
		}
		q := r.URL.Query()
		var out any
		switch r.URL.Path {
		case "/rest/api/3/myself":
			out = obj{"accountId": "5b10a2844c20165700ede21g", "displayName": "Ada"}
		case "/rest/api/3/status":
			out = []obj{todo, doing, status("5", "Done", "done")}
		case "/rest/api/3/project/search":
			values := []obj{}
			for _, k := range q["keys"] {
				if k == "ENG" || k == "OPS" {
					values = append(values, obj{"key": k})
				}
			}
			out = obj{"values": values, "isLast": true}
		case "/rest/api/3/search/jql":
			jql := q.Get("jql")
			inside := q.Get("nextPageToken") == ""
			if from, ok := day(jqlFrom.FindStringSubmatch(jql)); ok && updated.Before(from) {
				inside = false
			}
			if to, ok := day(jqlTo.FindStringSubmatch(jql)); ok && !updated.Before(to) {
				inside = false
			}
			if m := jqlIn.FindStringSubmatch(jql); m != nil && !strings.Contains(m[1], `"ENG"`) {
				inside = false
			}
			issues := []obj{}
			if inside {
				issues = []obj{
					issue(1, todo, now.Add(-120*time.Hour)),
					issue(2, doing, now.Add(-96*time.Hour), obj{"id": "900", "created": jts(updated),
						"items": []obj{{"field": "status", "fieldtype": "jira", "from": "1", "fromString": "To Do", "to": "3", "toString": "In Progress"}}}),
				}
			}
			out = obj{"issues": issues, "isLast": true}
		default:
			t.Errorf("unexpected request to Jira: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// val prints a value that may be missing.
func val(v *float64) string {
	if v == nil {
		return "nothing"
	}
	return fmt.Sprint(*v)
}

func TestSyncWithJira(t *testing.T) {
	// Whole seconds, so the hours between a ticket and a pull request are whole.
	now := time.Now().UTC().Truncate(time.Second)
	var hubCalls, jiraCalls atomic.Int64
	hub, jsrv := fakeGitHub(t, now, &hubCalls), fakeJira(t, now, &jiraCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	dir := filepath.Join(t.TempDir(), "store")
	sync := func(more ...string) (string, error) {
		return execute(syncCmd(), append([]string{"acme/app", "--store", dir, "--since", "2w", "--skip-ci", "--skip-deploys", "--skip-tickets", "--quiet"}, more...)...)
	}

	// No account, half an account, no site: an error before the first
	// request and before the store exists.
	for _, c := range []struct {
		email, token string
		flags        []string
		want         string
	}{
		{" ", "", []string{"--jira", jsrv.URL}, "Set JIRA_EMAIL to the email of an account"},
		{fakeJiraEmail, " ", []string{"--jira", jsrv.URL}, "Set JIRA_API_TOKEN"},
		{"", fakeJiraToken, []string{"--jira", jsrv.URL}, "Set JIRA_EMAIL"},
		{fakeJiraEmail, fakeJiraToken, []string{"--jira-project", "ENG"}, "--jira-project needs --jira"},
		{fakeJiraEmail, fakeJiraToken, []string{"--jira", " "}, "--jira needs the host"},
		{fakeJiraEmail, fakeJiraToken, []string{"--jira", "http://acme.atlassian.net"}, "must be asked over https"},
		{fakeJiraEmail, fakeJiraToken, []string{"--jira", "https://ada:hunter2@acme.atlassian.net"}, "must not carry a name or a password"},
	} {
		t.Setenv("JIRA_EMAIL", c.email)
		t.Setenv("JIRA_API_TOKEN", c.token)
		text, err := sync(c.flags...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%v as %q: %v, want %q", c.flags, c.email, err, c.want)
		}
		if all := text + err.Error(); strings.Contains(all, fakeJiraToken) || strings.Contains(all, "hunter2") {
			t.Errorf("%v: the error holds a secret: %s", c.flags, all)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) || hubCalls.Load() != 0 || jiraCalls.Load() != 0 {
		t.Fatalf("with no account: store %v, %d requests to GitHub, %d to Jira", err, hubCalls.Load(), jiraCalls.Load())
	}

	t.Setenv("JIRA_EMAIL", fakeJiraEmail)
	t.Setenv("JIRA_API_TOKEN", fakeJiraToken)
	text, err := sync("--jira", jsrv.URL)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	// The line of GitHub stays as it was: the Action reads the count from it.
	if !regexp.MustCompile(`(?m)^[0-9]+ requests to GitHub in [0-9]+s\.$`).MatchString(text) {
		t.Errorf("no line of requests to GitHub:\n%s", text)
	}
	from := now.Add(-14 * 24 * time.Hour).Format("2006-01-02")
	for _, want := range []string{"Jira 127.0.0.1: tickets 2 (+2), from " + from + "\n", fmt.Sprintf("%d requests to Jira in ", jiraCalls.Load())} {
		if !strings.Contains(text, want) {
			t.Errorf("the output lacks %q:\n%s", want, text)
		}
	}
	for _, not := range []string{fakeJiraToken, fakeJiraEmail, "note:", "links to pull requests", "Linear"} {
		if strings.Contains(text, not) {
			t.Errorf("the output holds %q:\n%s", not, text)
		}
	}
	// Nothing in the store holds the token or the email.
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && (bytes.Contains(b, []byte(fakeJiraToken)) || bytes.Contains(b, []byte(fakeJiraEmail))) {
			t.Errorf("%s holds the token or the email", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	info, err := execute(storeCmd(), "info", "acme", "--store", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme: 1 repositories, Jira tickets, ", "127.0.0.1 (Jira)  -"} {
		if !strings.Contains(info, want) {
			t.Errorf("store info lacks %q:\n%s", want, info)
		}
	}
	js, err := execute(storeCmd(), "info", "acme", "--store", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var si storeInfo
	if err := json.Unmarshal([]byte(js), &si); err != nil {
		t.Fatal(err)
	}
	// The scopes come in the order of their keys: the repository, then the site.
	if len(si.Scopes) != 2 || si.Scopes[1].Source != record.Jira || si.Scopes[1].Name != "127.0.0.1" || !si.Scopes[1].Private ||
		si.Scopes[1].Records["tickets"] != 2 || si.Scopes[1].Records["links"] != 0 {
		t.Errorf("store info json: %+v", si.Scopes)
	}

	// The report counts both tickets at the spec station. ENG-1 was created
	// five days ago and starts with pull request 1, whose branch names it,
	// three days ago: 48 hours. ENG-2 was created four days ago and starts
	// in Jira a day ago: 72 hours. No pull request names it, and Jira has
	// none attached. The middle of the two is 60 hours.
	raw, err := execute(reportCmd(), "acme/app", "--store", dir, "--since", "2w", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rep contract.Report
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range rep.Metrics {
		if s.Metric == "spec.wait.p50" && s.Slice == (contract.Slice{}) {
			found = true
			// ENG-2 has no pull request of this repository, so the report of
			// the repository counts ENG-1 alone.
			if s.Value == nil || *s.Value != 48 || s.N != 1 {
				t.Errorf("spec.wait.p50 of the repository = %v of %d tickets, want 48 of 1", val(s.Value), s.N)
			}
		}
	}
	if !found {
		t.Error("the report has no spec.wait.p50")
	}
	tickets := ""
	for _, sc := range rep.Coverage.Sources {
		if sc.Kind == record.KindTicket {
			tickets = sc.Source
		}
	}
	if tickets != "jira" {
		t.Errorf("ticket coverage = %q, want jira", tickets)
	}
	limits := strings.Join(rep.Limits, "\n")
	for _, want := range []string{
		"Only the Jira tickets that a pull request of this repository is linked to are counted. 1 ticket in the store is left out.",
		"A pull request counts as linked to a Jira ticket when its branch, title or description names the key of the ticket.",
	} {
		if !strings.Contains(limits, want) {
			t.Errorf("the limits lack %q:\n%s", want, limits)
		}
	}
	// The report of the owner counts both.
	raw, err = execute(reportCmd(), "acme", "--store", dir, "--since", "2w", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	rep = contract.Report{}
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Metrics {
		if s.Metric == "spec.wait.p50" && s.Slice == (contract.Slice{}) && (s.Value == nil || *s.Value != 60 || s.N != 2) {
			t.Errorf("spec.wait.p50 of the owner = %v of %d tickets, want 60 of 2", val(s.Value), s.N)
		}
	}

	// A later sync without --jira says the tickets were not read.
	before := jiraCalls.Load()
	text, err = sync()
	if err != nil || !strings.Contains(text, "note: the store holds Jira tickets, and this sync did not read Jira. Pass --jira") || jiraCalls.Load() != before {
		t.Errorf("a sync without --jira: %v, %d requests to Jira\n%s", err, jiraCalls.Load()-before, text)
	}
	// A next sync reads what changed, and nothing did.
	text, err = sync("--jira", jsrv.URL)
	if err != nil || !strings.Contains(text, "Jira 127.0.0.1: tickets 2, from "+from+"\n") || strings.Contains(text, "note:") {
		t.Errorf("a next sync: %v\n%s", err, text)
	}

	// A project that is not there fails, after the lines of GitHub are printed.
	text, err = sync("--jira", jsrv.URL, "--jira-project", "nope")
	if err == nil || !strings.Contains(err.Error(), "NOPE") || !strings.Contains(text, "requests to GitHub in") || !strings.Contains(text, "failed: Jira 127.0.0.1: ") {
		t.Errorf("a project that is not there: %v\n%s", err, text)
	}
	// The project that is there reads again, since the list of projects changed.
	text, err = sync("--jira", jsrv.URL, "--jira-project", "eng")
	if err != nil || !strings.Contains(text, "Jira 127.0.0.1: tickets 2 (+2), from ") || !strings.Contains(text, "note: Jira 127.0.0.1: the list of projects changed, so the tickets were read again from the start.") {
		t.Errorf("one project: %v\n%s", err, text)
	}
	// A project with no tickets drops what the other one had.
	text, err = sync("--jira", jsrv.URL, "--jira-project", "OPS")
	if err != nil || !strings.Contains(text, "Jira 127.0.0.1: tickets 0, from ") {
		t.Errorf("another project: %v\n%s", err, text)
	}
}

// One sync reads GitHub, then Linear, then Jira, and one store holds all three.
func TestSyncWithLinearAndJira(t *testing.T) {
	now := time.Now().UTC()
	var hubCalls, linCalls, jiraCalls atomic.Int64
	hub, lin, jsrv := fakeGitHub(t, now, &hubCalls), fakeLinear(t, now, &linCalls), fakeJira(t, now, &jiraCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	t.Setenv("LINEAR_API_URL", lin.URL)
	t.Setenv("LINEAR_API_KEY", fakeLinearKey)
	t.Setenv("JIRA_EMAIL", fakeJiraEmail)
	t.Setenv("JIRA_API_TOKEN", fakeJiraToken)
	dir := filepath.Join(t.TempDir(), "store")
	base := []string{"acme/app", "--store", dir, "--since", "2w", "--skip-ci", "--skip-deploys", "--skip-tickets", "--quiet"}
	text, err := execute(syncCmd(), append(base, "--linear", "--jira", jsrv.URL)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	hubAt, linAt, jiraAt := strings.Index(text, "requests to GitHub in"), strings.Index(text, "Linear acme: tickets 2 (+2), links to pull requests 1 (+1)"), strings.Index(text, "Jira 127.0.0.1: tickets 2 (+2), from ")
	if hubAt < 0 || linAt < hubAt || jiraAt < linAt || strings.Contains(text, "note:") {
		t.Errorf("the three sources do not come in order:\n%s", text)
	}
	info, err := execute(storeCmd(), "info", "acme", "--store", dir)
	if err != nil || !strings.Contains(info, "127.0.0.1 (Jira)") || !strings.Contains(info, "acme (Linear)") {
		t.Errorf("store info: %v\n%s", err, info)
	}
	// A sync of GitHub alone names both systems that were not read.
	text, err = execute(syncCmd(), base...)
	if err != nil || !strings.Contains(text, "did not read Linear. Pass --linear") || !strings.Contains(text, "did not read Jira. Pass --jira") {
		t.Errorf("a sync of GitHub alone: %v\n%s", err, text)
	}
	// A wrong project of Jira fails the command, and Linear is still read.
	text, err = execute(syncCmd(), append(base, "--linear", "--jira", jsrv.URL, "--jira-project", "NOPE")...)
	if err == nil || !strings.Contains(text, "Linear acme: tickets 2") || !strings.Contains(text, "failed: Jira 127.0.0.1: ") {
		t.Errorf("a wrong project next to Linear: %v\n%s", err, text)
	}
}

// When nothing of GitHub could be read there is no pull request to tie a
// ticket to, so Jira is not asked.
func TestSyncDoesNotReadJiraWhenGitHubFailed(t *testing.T) {
	var jiraCalls atomic.Int64
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	t.Cleanup(hub.Close)
	jsrv := fakeJira(t, time.Now().UTC(), &jiraCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	t.Setenv("JIRA_EMAIL", fakeJiraEmail)
	t.Setenv("JIRA_API_TOKEN", fakeJiraToken)
	text, err := execute(syncCmd(), "acme/app", "--store", filepath.Join(t.TempDir(), "store"), "--since", "2w",
		"--skip-ci", "--skip-deploys", "--skip-tickets", "--quiet", "--jira", jsrv.URL)
	if err == nil || jiraCalls.Load() != 0 || strings.Contains(text, "Jira") {
		t.Errorf("err %v, %d requests to Jira\n%s", err, jiraCalls.Load(), text)
	}
}

func TestPrintJira(t *testing.T) {
	var out bytes.Buffer
	from := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	printJira(&out, ingest.SourceResult{Source: record.Jira, Name: "acme.atlassian.net", Tickets: ingest.Count{Added: 3, Changed: 1, Total: 120}, From: from, Again: true},
		nil, 14, 2600*time.Millisecond)
	want := "Jira acme.atlassian.net: tickets 120 (+3, 1 updated), from 2026-05-04\n" +
		"note: Jira acme.atlassian.net: the list of projects changed, so the tickets were read again from the start.\n" +
		"14 requests to Jira in 3s.\n"
	if got := out.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	out.Reset()
	printJira(&out, ingest.SourceResult{Source: record.Jira, Name: "acme.atlassian.net", Tickets: ingest.Count{Total: 7}, From: from},
		&ingest.StoppedError{Cause: context.Canceled}, 3, time.Second)
	if got := out.String(); !strings.Contains(got, "from 2026-05-04, stopped\n") || strings.Contains(got, "failed") {
		t.Errorf("a stopped sync: %q", got)
	}
	// The account was rejected, so the site has no name yet.
	out.Reset()
	printJira(&out, ingest.SourceResult{Source: record.Jira}, errors.New("jira rejected the email and the API token (401)"), 1, 0)
	if got, want := out.String(), "failed: Jira: jira rejected the email and the API token (401)\n1 requests to Jira in 0s.\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestActionSyncReadsJira(t *testing.T) {
	now := time.Now().UTC()
	var hubCalls, jiraCalls atomic.Int64
	hub, jsrv := fakeGitHub(t, now, &hubCalls), fakeJira(t, now, &jiraCalls)
	r := newRunner(t)
	env := []string{"GITHUB_TOKEN=a-fake-github-token", "GITHUB_API_URL=" + hub.URL, "GITHUB_GRAPHQL_URL=" + hub.URL + "/graphql", "FM_HISTORY=2w"}
	site, email, token := "FM_JIRA_SITE="+jsrv.URL, "JIRA_EMAIL="+fakeJiraEmail, "JIRA_API_TOKEN="+fakeJiraToken

	// Nothing of Jira is set: Jira is not asked, and there is no warning.
	text, out := r.step("sync", env...)
	// The path of the store carries the name of the test, so the text is
	// searched for the lines of Jira, not for its name.
	if jiraCalls.Load() != 0 || strings.Contains(text, "requests to Jira") || strings.Contains(text, "::warning::") || out["requests"] != fmt.Sprint(hubCalls.Load()) {
		t.Errorf("nothing set: %d requests to Jira, outputs %v\n%s", jiraCalls.Load(), out, text)
	}
	// A part of the three is missing, as the secrets are for a pull request
	// from a fork: Jira is not asked, and a warning names what is empty.
	for _, c := range []struct {
		set  []string
		want string
	}{
		{[]string{site}, "Empty: jira-email jira-api-token. So Jira is not read."},
		{[]string{site, email}, "Empty: jira-api-token. So Jira is not read."},
		{[]string{email, token}, "Empty: jira-site. So Jira is not read."},
		{[]string{"FM_JIRA_PROJECTS=ENG"}, "Empty: jira-site jira-email jira-api-token. So Jira is not read."},
	} {
		text, _ = r.step("sync", append(env, c.set...)...)
		if want := "::warning::factorymeter: Jira needs jira-site, jira-email and jira-api-token. " + c.want; jiraCalls.Load() != 0 || !strings.Contains(text, want) {
			t.Errorf("%d of the inputs set: %d requests to Jira, want %q\n%s", len(c.set), jiraCalls.Load(), want, text)
		}
		if strings.Contains(text, fakeJiraToken) || strings.Contains(text, fakeJiraEmail) {
			t.Errorf("the warning holds the token or the email:\n%s", text)
		}
	}

	before := hubCalls.Load()
	text, out = r.step("sync", append(env, site, email, token)...)
	if !strings.Contains(text, "Jira 127.0.0.1: tickets 2 (+2), from ") || strings.Contains(text, fakeJiraToken) || strings.Contains(text, fakeJiraEmail) {
		t.Errorf("with an account:\n%s", text)
	}
	// The output counts the requests to GitHub, not the ones to Jira.
	if got := fmt.Sprint(hubCalls.Load() - before); out["requests"] != got || jiraCalls.Load() == 0 {
		t.Errorf("requests = %q, want %s; %d requests to Jira", out["requests"], got, jiraCalls.Load())
	}

	// Projects are split on commas, spaces and line breaks, and are not
	// globbed: a star stays a key that is not one.
	text, _ = r.step("sync", append(env, site, email, token, "FM_JIRA_PROJECTS= eng,\n ENG ")...)
	if !strings.Contains(text, "Jira 127.0.0.1: tickets 2") || strings.Contains(text, "failed") {
		t.Errorf("projects:\n%s", text)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "action.sh"), "sync")
	for k, v := range r.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, append(env, site, email, token, "FM_JIRA_PROJECTS=ENG *")...)
	b, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(b), `"*" is not the key of a Jira project`) {
		t.Errorf("a star as a project: %v\n%s", err, b)
	}

	// A store with Jira tickets is private, whatever the repository is.
	r.write("event.json", `{"repository": {"private": false}}`)
	if text, out := r.step("share"); out["names"] != "false" || out["save"] != "false" || !strings.Contains(text, "or tickets of Linear or Jira") {
		t.Errorf("share in a public repository: %v\n%s", out, text)
	}
}
