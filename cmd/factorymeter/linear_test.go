package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/ingest"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
	"github.com/marshal-org/factorymeter/pkg/web"
)

const fakeLinearKey = "lin_api_key_that_must_not_be_printed"

type obj = map[string]any

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// fakeGitHub answers the pull requests of acme/app: number 1 on a branch that
// names ENG-1, opened three days ago, and number 2, opened two days ago.
// The owner acme also lists acme/gone, which cannot be read.
// The repository has no CI runs, no deploys and no issues.
func fakeGitHub(t *testing.T, now time.Time, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	pull := func(n int, branch string, created time.Time) obj {
		return obj{"number": n, "url": fmt.Sprintf("https://github.com/acme/app/pull/%d", n), "title": fmt.Sprintf("Change %d", n),
			"state": "OPEN", "createdAt": stamp(created), "updatedAt": stamp(created), "headRefName": branch, "baseRefName": "main",
			"author": obj{"__typename": "User", "login": "dev"}, "commits": obj{"totalCount": 0, "nodes": []obj{}},
			"reviews": obj{"nodes": []obj{}}, "timelineItems": obj{"nodes": []obj{}}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		empty := obj{"pageInfo": obj{"hasNextPage": false, "endCursor": nil}, "nodes": []obj{}}
		switch err := json.Unmarshal(body, &req); {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions/runs"):
			json.NewEncoder(w).Encode(obj{"total_count": 0, "workflow_runs": []obj{}})
			return
		case err == nil && strings.Contains(req.Query, "issues(first"):
			json.NewEncoder(w).Encode(obj{"data": obj{"repository": obj{"issues": empty}}})
			return
		case err == nil && strings.Contains(req.Query, "deployments("):
			json.NewEncoder(w).Encode(obj{"data": obj{"repository": obj{"deployments": empty, "releases": empty}}})
			return
		case err == nil && strings.Contains(req.Query, "repositoryOwner"):
			json.NewEncoder(w).Encode(obj{"data": obj{"repositoryOwner": obj{"repositories": obj{"pageInfo": obj{"hasNextPage": false},
				"nodes": []obj{{"nameWithOwner": "acme/app", "pushedAt": stamp(now)}, {"nameWithOwner": "acme/gone", "pushedAt": stamp(now)}}}}}})
			return
		case err != nil || !strings.Contains(req.Query, "pullRequests("):
			t.Errorf("unexpected request to GitHub: %s %s %s", r.Method, r.URL, body)
			return
		case req.Variables["name"] == "gone":
			json.NewEncoder(w).Encode(obj{"errors": []obj{{"type": "NOT_FOUND", "message": "Could not resolve to a Repository with the name 'acme/gone'."}}})
			return
		}
		nodes := []obj{}
		if req.Variables["after"] == nil {
			nodes = []obj{pull(1, "ada/eng-1-login", now.Add(-72*time.Hour)), pull(2, "fix/x", now.Add(-48*time.Hour))}
		}
		json.NewEncoder(w).Encode(obj{"data": obj{"repository": obj{"nameWithOwner": "acme/app", "isPrivate": true,
			"pullRequests": obj{"pageInfo": obj{"hasNextPage": false, "endCursor": nil}, "nodes": nodes}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeLinear answers the workspace acme with the team ENG and two tickets,
// both updated a day ago:
//
//	ENG-1  created five days ago, never started in Linear
//	ENG-2  created four days ago, started a day ago, with pull request 2 attached
func fakeLinear(t *testing.T, now time.Time, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	updated := now.Add(-24 * time.Hour)
	issue := func(n int, typ string, created time.Time, started *time.Time, pulls ...string) obj {
		atts := []obj{}
		for _, u := range pulls {
			atts = append(atts, obj{"url": u})
		}
		hist := []obj{{"startedAt": stamp(created), "stateId": "s-unstarted"}}
		var at any
		if started != nil {
			at = stamp(*started)
			hist = append(hist, obj{"startedAt": stamp(*started), "stateId": "s-started"})
		}
		key := fmt.Sprintf("ENG-%d", n)
		return obj{"id": fmt.Sprintf("u%d", n), "identifier": key, "previousIdentifiers": []string{}, "title": "Ticket " + key,
			"url": "https://linear.app/acme/issue/" + key, "createdAt": stamp(created), "updatedAt": stamp(updated), "startedAt": at,
			"state": obj{"name": typ, "type": typ}, "team": obj{"key": "ENG"}, "creator": obj{"displayName": "Ada", "app": false},
			"labels": obj{"nodes": []obj{}}, "attachments": obj{"nodes": atts}, "stateHistory": obj{"nodes": hist}}
	}
	inside := func(filter map[string]any) bool {
		u, _ := filter["updatedAt"].(map[string]any)
		if v, ok := u["gte"].(string); ok {
			if from, _ := time.Parse(time.RFC3339, v); updated.Before(from) {
				return false
			}
		}
		if v, ok := u["lt"].(string); ok {
			if to, _ := time.Parse(time.RFC3339, v); !updated.Before(to) {
				return false
			}
		}
		return true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != fakeLinearKey {
			t.Errorf("Authorization = %q, want the key as given", r.Header.Get("Authorization"))
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		filter, _ := req.Variables["filter"].(map[string]any)
		var data obj
		switch {
		case strings.Contains(req.Query, "issues("):
			nodes := []obj{}
			if inside(filter) && req.Variables["after"] == nil {
				started := now.Add(-24 * time.Hour)
				nodes = []obj{issue(1, "unstarted", now.Add(-120*time.Hour), nil),
					issue(2, "started", now.Add(-96*time.Hour), &started, "https://github.com/acme/app/pull/2")}
			}
			data = obj{"issues": obj{"nodes": nodes, "pageInfo": obj{"hasNextPage": false, "endCursor": nil}}}
		case strings.Contains(req.Query, "workflowStates("):
			data = obj{"workflowStates": obj{"nodes": []obj{{"id": "s-unstarted", "type": "unstarted"}, {"id": "s-started", "type": "started"}},
				"pageInfo": obj{"hasNextPage": false, "endCursor": nil}}}
		case strings.Contains(req.Query, "teams("):
			nodes := []obj{}
			team, _ := filter["key"].(map[string]any)
			for _, k := range team["in"].([]any) {
				if k == "ENG" {
					nodes = append(nodes, obj{"key": "ENG"})
				}
			}
			data = obj{"teams": obj{"nodes": nodes}}
		case strings.Contains(req.Query, "organization"):
			data = obj{"organization": obj{"urlKey": "acme", "name": "Acme"}}
		default:
			t.Errorf("unexpected query to Linear: %s", req.Query)
		}
		json.NewEncoder(w).Encode(obj{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func execute(cmd *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	err := cmd.Execute()
	return out.String(), err
}

func TestSyncWithLinear(t *testing.T) {
	now := time.Now().UTC()
	var hubCalls, linCalls atomic.Int64
	hub, lin := fakeGitHub(t, now, &hubCalls), fakeLinear(t, now, &linCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	t.Setenv("LINEAR_API_URL", lin.URL)
	dir := filepath.Join(t.TempDir(), "store")
	sync := func(more ...string) (string, error) {
		return execute(syncCmd(), append([]string{"acme/app", "--store", dir, "--since", "2w", "--skip-ci", "--skip-deploys", "--skip-tickets", "--quiet"}, more...)...)
	}

	// No key: an error before the first request and before the store exists.
	t.Setenv("LINEAR_API_KEY", " ")
	for _, flags := range [][]string{{"--linear"}, {"--linear-team", "ENG"}} {
		if _, err := sync(flags...); err == nil || !strings.Contains(err.Error(), "Set LINEAR_API_KEY") {
			t.Fatalf("%v with no key: %v", flags, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) || hubCalls.Load() != 0 || linCalls.Load() != 0 {
		t.Fatalf("with no key: store %v, %d requests to GitHub, %d to Linear", err, hubCalls.Load(), linCalls.Load())
	}

	t.Setenv("LINEAR_API_KEY", fakeLinearKey)
	text, err := sync("--linear")
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	// The line of GitHub stays as it was: the Action reads the count from it.
	if !regexp.MustCompile(`(?m)^[0-9]+ requests to GitHub in [0-9]+s\.$`).MatchString(text) {
		t.Errorf("no line of requests to GitHub:\n%s", text)
	}
	from := now.Add(-14 * 24 * time.Hour).Format("2006-01-02")
	for _, want := range []string{"Linear acme: tickets 2 (+2), links to pull requests 1 (+1), from " + from + "\n", fmt.Sprintf("%d requests to Linear in ", linCalls.Load())} {
		if !strings.Contains(text, want) {
			t.Errorf("the output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, fakeLinearKey) || strings.Contains(text, "note:") {
		t.Errorf("the output holds the key or a note:\n%s", text)
	}
	// Nothing in the store holds the key.
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, []byte(fakeLinearKey)) {
			t.Errorf("%s holds the key", path)
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
	for _, want := range []string{"acme: 1 repositories, Linear tickets, ", "acme (Linear)  -        -        -        2  "} {
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
	// The scopes come in the order of their keys: the repository, then the workspace.
	if len(si.Scopes) != 2 || si.Scopes[1].Source != record.Linear || si.Scopes[1].Name != "acme" || !si.Scopes[1].Private ||
		si.Scopes[1].Records["tickets"] != 2 || si.Scopes[1].Records["links"] != 1 {
		t.Errorf("store info json: %+v", si.Scopes)
	}

	// The report counts both tickets at the spec station. ENG-1 was created
	// five days ago and starts with pull request 1, whose branch names it,
	// three days ago: 48 hours. ENG-2 was created four days ago and starts
	// with pull request 2, which Linear has attached, two days ago, a day
	// before Linear's own start: 48 hours.
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
			if s.Value == nil || *s.Value != 48 || s.N != 2 {
				t.Errorf("spec.wait.p50 = %v of %d tickets, want 48 of 2", s.Value, s.N)
			}
		}
	}
	if !found {
		t.Error("the report has no spec.wait.p50")
	}
	tickets := ""
	for _, sc := range rep.Coverage.Sources {
		if sc.Kind == record.KindTicket {
			tickets = fmt.Sprintf("%s %d", sc.Source, sc.Records)
		}
	}
	if tickets != "linear 2" {
		t.Errorf("ticket coverage = %q, want linear 2", tickets)
	}
	if limits := strings.Join(rep.Limits, "\n"); !strings.Contains(limits, "Only the Linear tickets that a pull request of this repository is linked to are counted.") {
		t.Errorf("the limits do not explain the Linear tickets:\n%s", limits)
	}

	// The page offers repositories, and a workspace is not one.
	reg, err := loadRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	src := &lines{dir: dir, target: "acme", reg: reg, now: time.Now}
	if got, err := src.Repos(); err != nil || len(got) != 1 || got[0] != "acme/app" {
		t.Errorf("repos of the page = %v, %v", got, err)
	}
	// A sync of Linear alone makes the kept line of one repository stale.
	if _, err := src.Report(web.Query{Weeks: 2, Repo: "acme/app"}); err != nil {
		t.Fatal(err)
	}
	kept := src.kept["acme/app"].line
	s, err := store.OpenWrite(dir, "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Scope(record.Linear, "acme")
	sc.SyncedAt = sc.SyncedAt.Add(time.Minute)
	if err := s.PutScope(sc); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := src.Report(web.Query{Weeks: 2, Repo: "acme/app"}); err != nil || src.kept["acme/app"].line == kept {
		t.Errorf("after a sync of Linear the line was not read again, err %v", err)
	}

	// A later sync without --linear says the tickets were not read.
	before := linCalls.Load()
	text, err = sync()
	if err != nil || !strings.Contains(text, "note: the store holds Linear tickets, and this sync did not read Linear. Pass --linear") || linCalls.Load() != before {
		t.Errorf("a sync without --linear: %v, %d requests to Linear\n%s", err, linCalls.Load()-before, text)
	}

	// A team that is not there fails, after the lines of GitHub are printed.
	text, err = sync("--linear-team", "nope")
	if err == nil || !strings.Contains(err.Error(), "NOPE") || !strings.Contains(text, "requests to GitHub in") || !strings.Contains(text, "failed: Linear acme: ") {
		t.Errorf("a team that is not there: %v\n%s", err, text)
	}
	// The team that is there reads again, since the list of teams changed.
	text, err = sync("--linear-team", "eng")
	if err != nil || !strings.Contains(text, "Linear acme: tickets 2 (+2)") || !strings.Contains(text, "note: Linear acme: the list of teams changed") {
		t.Errorf("one team: %v\n%s", err, text)
	}
}

func TestPrintLinear(t *testing.T) {
	var out bytes.Buffer
	from := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	printLinear(&out, ingest.SourceResult{Source: record.Linear, Name: "acme", Tickets: ingest.Count{Added: 3, Changed: 1, Total: 120}, Links: ingest.Count{Total: 40}, From: from},
		nil, 14, 2600*time.Millisecond)
	if got, want := out.String(), "Linear acme: tickets 120 (+3, 1 updated), links to pull requests 40, from 2026-05-04\n14 requests to Linear in 3s.\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	out.Reset()
	printLinear(&out, ingest.SourceResult{Source: record.Linear, Name: "acme", Tickets: ingest.Count{Total: 7}, From: from},
		&ingest.StoppedError{Cause: context.Canceled}, 3, time.Second)
	if got := out.String(); !strings.Contains(got, "from 2026-05-04, stopped\n") || strings.Contains(got, "failed") {
		t.Errorf("a stopped sync: %q", got)
	}
	// The key was rejected, so the workspace has no name yet.
	out.Reset()
	printLinear(&out, ingest.SourceResult{Source: record.Linear}, errors.New("linear rejected the key (401)"), 1, 0)
	if got, want := out.String(), "failed: Linear: linear rejected the key (401)\n1 requests to Linear in 0s.\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// When nothing of GitHub could be read there is no pull request to tie a
// ticket to, so Linear is not asked.
func TestSyncDoesNotReadLinearWhenGitHubFailed(t *testing.T) {
	var linCalls atomic.Int64
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	t.Cleanup(hub.Close)
	lin := fakeLinear(t, time.Now().UTC(), &linCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	t.Setenv("LINEAR_API_URL", lin.URL)
	t.Setenv("LINEAR_API_KEY", fakeLinearKey)
	text, err := execute(syncCmd(), "acme/app", "--store", filepath.Join(t.TempDir(), "store"), "--since", "2w",
		"--skip-ci", "--skip-deploys", "--skip-tickets", "--quiet", "--linear")
	if err == nil || linCalls.Load() != 0 || strings.Contains(text, "Linear") {
		t.Errorf("err %v, %d requests to Linear\n%s", err, linCalls.Load(), text)
	}
}

// One repository of the owner fails and one is read: Linear is still read,
// and the command fails with the error of GitHub.
func TestSyncReadsLinearWhenOneRepositoryFailed(t *testing.T) {
	now := time.Now().UTC()
	var hubCalls, linCalls atomic.Int64
	hub, lin := fakeGitHub(t, now, &hubCalls), fakeLinear(t, now, &linCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	t.Setenv("LINEAR_API_URL", lin.URL)
	t.Setenv("LINEAR_API_KEY", fakeLinearKey)
	text, err := execute(syncCmd(), "acme", "--store", filepath.Join(t.TempDir(), "store"), "--since", "2w",
		"--skip-ci", "--skip-deploys", "--skip-tickets", "--quiet", "--linear")
	if err == nil || !strings.Contains(text, "failed: acme/gone: ") || !strings.Contains(text, "Linear acme: tickets 2 (+2), links to pull requests 1 (+1)") {
		t.Errorf("err %v, %d requests to Linear\n%s", err, linCalls.Load(), text)
	}
}

func TestAnyRead(t *testing.T) {
	bad := ingest.RepoResult{Repo: "acme/gone", Err: errors.New("gone")}
	for _, c := range []struct {
		repos []ingest.RepoResult
		want  bool
	}{
		{nil, false},
		{[]ingest.RepoResult{bad}, false},
		{[]ingest.RepoResult{bad, {Repo: "acme/app"}}, true},
	} {
		if got := anyRead(ingest.Result{Repos: c.repos}); got != c.want {
			t.Errorf("anyRead of %d repositories = %v, want %v", len(c.repos), got, c.want)
		}
	}
}

// The sync step of the Action reads Linear when the workflow gives a key, and
// the key never becomes an argument.
func TestActionSyncReadsLinear(t *testing.T) {
	now := time.Now().UTC()
	var hubCalls, linCalls atomic.Int64
	hub, lin := fakeGitHub(t, now, &hubCalls), fakeLinear(t, now, &linCalls)
	r := newRunner(t)
	env := []string{"GITHUB_TOKEN=a-fake-github-token", "GITHUB_API_URL=" + hub.URL, "GITHUB_GRAPHQL_URL=" + hub.URL + "/graphql",
		"LINEAR_API_URL=" + lin.URL, "FM_HISTORY=2w"}

	// No key: Linear is not asked. Teams with no key get a warning.
	text, out := r.step("sync", env...)
	if linCalls.Load() != 0 || strings.Contains(text, "requests to Linear") || out["requests"] != fmt.Sprint(hubCalls.Load()) {
		t.Errorf("no key: %d requests to Linear, %d to GitHub, outputs %v\n%s", linCalls.Load(), hubCalls.Load(), out, text)
	}
	text, _ = r.step("sync", append(env, "FM_LINEAR_TEAMS=ENG")...)
	if linCalls.Load() != 0 || !strings.Contains(text, "::warning::factorymeter: linear-teams is set and linear-api-key is empty, so Linear is not read.") {
		t.Errorf("teams with no key: %d requests to Linear\n%s", linCalls.Load(), text)
	}

	before := hubCalls.Load()
	text, out = r.step("sync", append(env, "LINEAR_API_KEY="+fakeLinearKey)...)
	if !strings.Contains(text, "Linear acme: tickets 2 (+2), links to pull requests 1 (+1)") || strings.Contains(text, fakeLinearKey) {
		t.Errorf("with a key:\n%s", text)
	}
	// The output counts the requests to GitHub, not the ones to Linear.
	if got := fmt.Sprint(hubCalls.Load() - before); out["requests"] != got || linCalls.Load() == 0 {
		t.Errorf("requests = %q, want %s; %d requests to Linear", out["requests"], got, linCalls.Load())
	}

	// Teams are split on commas, spaces and line breaks, and are not globbed:
	// a star stays a team that Linear does not know.
	text, _ = r.step("sync", append(env, "LINEAR_API_KEY="+fakeLinearKey, "FM_LINEAR_TEAMS= eng,\n ENG ")...)
	if !strings.Contains(text, "Linear acme: tickets 2") || strings.Contains(text, "failed") {
		t.Errorf("teams:\n%s", text)
	}
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "action.sh"), "sync")
	for k, v := range r.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, append(env, "LINEAR_API_KEY="+fakeLinearKey, "FM_LINEAR_TEAMS=ENG *")...)
	b, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "no team with the key * that") {
		t.Errorf("a star as a team: %v\n%s", err, b)
	}

	// A store with Linear tickets is private, whatever the repository is.
	r.write("event.json", `{"repository": {"private": false}}`)
	if text, out := r.step("share"); out["names"] != "false" || out["save"] != "false" || !strings.Contains(text, "or Linear tickets") {
		t.Errorf("share in a public repository: %v\n%s", out, text)
	}
}
