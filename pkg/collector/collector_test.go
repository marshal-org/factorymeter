package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/github"
)

var (
	t0    = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	since = t0.AddDate(0, 0, -14)
	until = t0
)

func day(n float64) time.Time { return since.Add(time.Duration(n * 24 * float64(time.Hour))) }
func ts(t time.Time) string   { return t.Format(time.RFC3339) }

type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// server plays GitHub. graphql answers POST /graphql and rest answers every GET.
type server struct {
	graphql func(gqlReq) any
	rest    func(path string, q url.Values) any
	gql     []gqlReq
	gets    []string
}

func (s *server) collector(t *testing.T, opt Options) *Collector {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		if r.Method == http.MethodPost {
			var req gqlReq
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("graphql body: %v", err)
			}
			s.gql = append(s.gql, req)
			out = s.graphql(req)
		} else {
			s.gets = append(s.gets, r.URL.Path+"?"+r.URL.RawQuery)
			if s.rest == nil {
				t.Errorf("unexpected GET %s", r.URL)
				out = []any{}
			} else {
				out = s.rest(r.URL.Path, r.URL.Query())
			}
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	c := github.New("t")
	c.BaseURL, c.GraphQLURL = srv.URL, srv.URL+"/graphql"
	if opt.Since.IsZero() {
		opt.Since, opt.Until = since, until
	}
	return &Collector{C: c, Opt: opt}
}

type obj = map[string]any

func node(n int, created, updated time.Time, shas ...string) obj {
	var commits []obj
	for _, sha := range shas {
		commits = append(commits, obj{"commit": obj{"oid": sha, "authoredDate": ts(created), "committedDate": ts(created),
			"message": "fix: a thing\n\nCo-Authored-By: Claude <noreply@anthropic.com>", "parents": obj{"totalCount": 1},
			"authors": obj{"nodes": []obj{{"name": "Dev", "email": "dev@example.invalid", "user": obj{"login": "dev"}}}}}})
	}
	return obj{"number": n, "url": fmt.Sprintf("https://github.com/o/r/pull/%d", n), "state": "OPEN", "createdAt": ts(created), "updatedAt": ts(updated),
		"headRefName": "fix/x", "author": obj{"__typename": "User", "login": "dev"},
		"commits": obj{"totalCount": len(shas), "nodes": commits}, "reviews": obj{"nodes": []obj{}}, "timelineItems": obj{"nodes": []obj{}}}
}

func prPage(next bool, cursor string, nodes ...obj) obj {
	return obj{"data": obj{"repository": obj{"isPrivate": true, "pullRequests": obj{"pageInfo": obj{"hasNextPage": next, "endCursor": cursor}, "nodes": nodes}}}}
}

func TestRepoPagesDownToTheWindow(t *testing.T) {
	bot := node(2, day(2), day(3), "b")
	bot["author"] = obj{"__typename": "Bot", "login": "copilot-swe-agent"}
	bot["reviews"] = obj{"nodes": []obj{
		{"state": "APPROVED", "submittedAt": ts(day(2.5)), "author": obj{"__typename": "User", "login": "rev"}},
		{"state": "COMMENTED", "submittedAt": ts(day(2.2)), "author": obj{"__typename": "Bot", "login": "copilot-pull-request-reviewer"}},
		{"state": "PENDING", "submittedAt": nil, "author": obj{"__typename": "User", "login": "rev"}},
	}}
	ghost := node(3, day(4), day(5), "c")
	ghost["author"] = nil
	big := node(4, day(1), day(2), "d")
	big["commits"].(obj)["totalCount"] = 250
	s := &server{graphql: func(r gqlReq) any {
		if r.Variables["owner"] != "o" || r.Variables["name"] != "r" || r.Variables["n"] != float64(25) {
			t.Errorf("variables: %v", r.Variables)
		}
		switch r.Variables["after"] {
		case nil:
			return prPage(true, "c1", node(1, day(1), day(13), "a"), bot, node(9, until.Add(time.Hour), until.Add(2*time.Hour), "late"))
		case "c1":
			// The second one was last touched before the window. The scan ends
			// there and never asks for the third page.
			return prPage(true, "c2", ghost, big, node(5, day(-9), day(-1), "old"), node(6, day(1), day(2), "unread"))
		}
		t.Errorf("a page past the window was requested: %v", r.Variables["after"])
		return prPage(false, "")
	}}
	var logged []string
	k := s.collector(t, Options{SkipCI: true, SkipDeploys: true, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }})
	rd, err := k.Repo(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, p := range rd.PRs {
		got = append(got, p.Number)
	}
	if fmt.Sprint(got) != "[1 2 3 4]" || len(s.gql) != 2 || len(s.gets) != 0 {
		t.Fatalf("pull requests %v, %d graphql calls, %d REST calls", got, len(s.gql), len(s.gets))
	}
	if !rd.Private || rd.CISource != "none" || rd.DeploySource != "none" || !rd.Since.Equal(since) {
		t.Errorf("repo data: %+v", rd)
	}
	p := rd.PRs[1]
	if p.Author.Login != "copilot-swe-agent[bot]" || p.Author.Type != "Bot" {
		t.Errorf("a bot must get its [bot] suffix back: %+v", p.Author)
	}
	if len(p.Reviews) != 2 || p.Reviews[0].Author.Login != "copilot-pull-request-reviewer[bot]" || p.Reviews[1].State != "APPROVED" {
		t.Errorf("reviews must be sorted by time with pending ones dropped: %+v", p.Reviews)
	}
	if rd.PRs[2].Author.Login != "ghost" {
		t.Errorf("a deleted author: %+v", rd.PRs[2].Author)
	}
	c := rd.PRs[0].Commits[0]
	if c.SHA != "a" || c.Parents != 1 || len(c.Trailers) != 1 || c.Authors[0].Login != "dev" || !rd.PRs[0].ReadyAt.Equal(day(1)) {
		t.Errorf("commit: %+v ready: %v", c, rd.PRs[0].ReadyAt)
	}
	if len(rd.Notes) != 1 || !strings.Contains(rd.Notes[0], "more than 100 commits") {
		t.Errorf("notes: %q", rd.Notes)
	}
	if len(logged) != 1 || logged[0] != "o/r: 4 pull requests touched in the window" {
		t.Errorf("log: %q", logged)
	}
}

func TestRepoHalvesThePageWhenGitHubTimesOut(t *testing.T) {
	var sizes []float64
	s := &server{graphql: func(r gqlReq) any {
		n := r.Variables["n"].(float64)
		sizes = append(sizes, n)
		if n > 7 {
			return obj{"errors": []obj{{"message": "Something went wrong while executing your query."}}}
		}
		return prPage(false, "", node(1, day(1), day(2), "a"))
	}}
	rd, err := s.collector(t, Options{SkipCI: true, SkipDeploys: true}).Repo(context.Background(), "o/r")
	if err != nil || len(rd.PRs) != 1 || fmt.Sprint(sizes) != "[25 13 7]" {
		t.Errorf("prs=%d sizes=%v err=%v", len(rd.PRs), sizes, err)
	}
}

func TestRepoErrors(t *testing.T) {
	s := &server{graphql: func(r gqlReq) any {
		return obj{"errors": []obj{{"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}}}
	}}
	k := s.collector(t, Options{})
	if _, err := k.Repo(context.Background(), "o/r"); !errors.Is(err, github.ErrNotFound) || !strings.HasPrefix(err.Error(), "o/r: pull requests:") {
		t.Errorf("got %v", err)
	}
	if _, err := k.Repo(context.Background(), "nope"); err == nil || len(s.gql) != 1 {
		t.Errorf("a bad name must fail before any call: %v, %d calls", err, len(s.gql))
	}
}

func TestReadyAt(t *testing.T) {
	ev := func(kind string, at time.Time) struct {
		Typename  string    `json:"__typename"`
		CreatedAt time.Time `json:"createdAt"`
	} {
		return struct {
			Typename  string    `json:"__typename"`
			CreatedAt time.Time `json:"createdAt"`
		}{kind, at}
	}
	var g gqlPR
	g.CreatedAt = day(1)
	if got := readyAt(g); got == nil || !got.Equal(day(1)) {
		t.Errorf("opened ready: %v", got)
	}
	g.IsDraft = true
	if got := readyAt(g); got != nil {
		t.Errorf("still a draft: %v", got)
	}
	// The events arrive out of order. The first one in time decides.
	g.TimelineItems.Nodes = append(g.TimelineItems.Nodes, ev("ConvertToDraftEvent", day(3)), ev("ReadyForReviewEvent", day(2)), ev("ReadyForReviewEvent", day(4)))
	if got := readyAt(g); got == nil || !got.Equal(day(2)) {
		t.Errorf("opened as a draft: %v", got)
	}
	g.TimelineItems.Nodes = g.TimelineItems.Nodes[:0]
	g.TimelineItems.Nodes = append(g.TimelineItems.Nodes, ev("ConvertToDraftEvent", day(2)), ev("ReadyForReviewEvent", day(3)))
	if got := readyAt(g); got == nil || !got.Equal(day(1)) {
		t.Errorf("opened ready, then converted to a draft: %v", got)
	}
}

func run(id int, sha, event string, at time.Time) obj {
	return obj{"id": id, "name": "ci", "event": event, "head_sha": sha, "run_attempt": 1, "status": "completed", "conclusion": "success",
		"created_at": ts(at), "run_started_at": ts(at), "updated_at": ts(at.Add(5 * time.Minute))}
}

func onePage(r gqlReq) any {
	return prPage(false, "", node(1, day(-3), day(2), "a", "b"), node(2, day(5), day(6), "b"))
}

func created(t *testing.T, q url.Values) (time.Time, time.Time) {
	t.Helper()
	a, b, ok := strings.Cut(q.Get("created"), "..")
	from, err1 := time.Parse(time.RFC3339, a)
	to, err2 := time.Parse(time.RFC3339, b)
	if !ok || err1 != nil || err2 != nil {
		t.Fatalf("created filter %q", q.Get("created"))
	}
	return from, to
}

func TestCIRunsJoinOnTheCommit(t *testing.T) {
	s := &server{graphql: onePage}
	s.rest = func(path string, q url.Values) any {
		if path != "/repos/o/r/actions/runs" || q.Get("per_page") != "100" {
			t.Errorf("request: %s %v", path, q)
		}
		// The read goes back to the oldest pull request, which opened three
		// days before the window.
		if from, to := created(t, q); !from.Equal(day(-3)) || !to.Equal(until) {
			t.Errorf("range: %v to %v", from, to)
		}
		switch q.Get("event") {
		case "pull_request":
			return obj{"total_count": 3, "workflow_runs": []obj{run(1, "a", "pull_request", day(1)), run(2, "b", "pull_request", day(2)), run(3, "elsewhere", "pull_request", day(2))}}
		case "pull_request_target":
			return obj{"total_count": 0, "workflow_runs": []obj{}}
		}
		t.Errorf("runs for every event were read although pull request runs exist: %v", q)
		return obj{}
	}
	rd, err := s.collector(t, Options{SkipDeploys: true}).Repo(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	// Commit b is in both pull requests, so its run belongs to both.
	if len(rd.PRs[0].CIRuns) != 2 || len(rd.PRs[1].CIRuns) != 1 || rd.PRs[1].CIRuns[0].ID != 2 || rd.CISource != "actions" || len(s.gets) != 2 {
		t.Errorf("runs: %d and %d, source %q, %d REST calls", len(rd.PRs[0].CIRuns), len(rd.PRs[1].CIRuns), rd.CISource, len(s.gets))
	}
	r := rd.PRs[0].CIRuns[0]
	if r.Workflow != "ci" || r.Event != "pull_request" || r.Attempt != 1 || r.Conclusion != "success" || !r.UpdatedAt.Equal(day(1).Add(5*time.Minute)) {
		t.Errorf("run: %+v", r)
	}
}

func TestCIRunsFallBackToEveryEvent(t *testing.T) {
	s := &server{graphql: onePage}
	s.rest = func(path string, q url.Values) any {
		if q.Get("event") == "" {
			return obj{"total_count": 1, "workflow_runs": []obj{run(7, "a", "push", day(1))}}
		}
		return obj{"total_count": 1, "workflow_runs": []obj{run(1, "elsewhere", "pull_request", day(1))}}
	}
	rd, err := s.collector(t, Options{SkipDeploys: true}).Repo(context.Background(), "o/r")
	if err != nil || len(rd.PRs[0].CIRuns) != 1 || rd.PRs[0].CIRuns[0].Event != "push" || rd.CISource != "actions" || len(s.gets) != 3 {
		t.Errorf("runs=%v source=%q gets=%d err=%v", rd.PRs[0].CIRuns, rd.CISource, len(s.gets), err)
	}
}

func TestCIRunsSplitARangeOverTheSearchCap(t *testing.T) {
	s := &server{graphql: onePage}
	whole := until.Sub(day(-3))
	id := 0
	s.rest = func(path string, q url.Values) any {
		if q.Get("event") != "pull_request" {
			return obj{"total_count": 0, "workflow_runs": []obj{}}
		}
		from, to := created(t, q)
		if to.Sub(from) > whole/2+time.Hour {
			// More than the API will page through. Nothing on this page may be
			// used, or the runs would be counted twice after the split.
			return obj{"total_count": 1500, "workflow_runs": []obj{run(9999, "a", "pull_request", day(1))}}
		}
		// Each half holds 150 runs: a full page of 100, then 50.
		n := 100
		if q.Get("page") == "2" {
			n = 50
		}
		runs := make([]obj, n)
		for i := range runs {
			id++
			runs[i] = run(id, "a", "pull_request", from.Add(time.Minute))
		}
		return obj{"total_count": 150, "workflow_runs": runs}
	}
	rd, err := s.collector(t, Options{SkipDeploys: true}).Repo(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, r := range rd.PRs[0].CIRuns {
		if seen[r.ID] || r.ID == 9999 {
			t.Fatalf("run %d was attached twice or came from the page over the cap", r.ID)
		}
		seen[r.ID] = true
	}
	// One call over the cap, then two pages for each half, then one call for
	// the other event.
	if len(seen) != 300 || len(s.gets) != 6 {
		t.Errorf("%d runs from %d REST calls, want 300 from 6", len(seen), len(s.gets))
	}
}

func TestCIRunsStopAtTheCap(t *testing.T) {
	s := &server{graphql: onePage}
	s.rest = func(path string, q url.Values) any {
		runs := make([]obj, 100)
		for i := range runs {
			runs[i] = run(i+1, "a", "pull_request", day(1))
		}
		return obj{"total_count": 900, "workflow_runs": runs}
	}
	rd, err := s.collector(t, Options{SkipDeploys: true, MaxRuns: 200}).Repo(context.Background(), "o/r")
	if err != nil || len(rd.PRs[0].CIRuns) != 200 || len(rd.Notes) != 1 || !strings.Contains(rd.Notes[0], "more than 200 workflow runs") {
		t.Errorf("runs=%d notes=%q err=%v", len(rd.PRs[0].CIRuns), rd.Notes, err)
	}
}

func TestACIFailureIsANoteNotAnError(t *testing.T) {
	s := &server{graphql: onePage}
	srvErr := 0
	s.rest = func(path string, q url.Values) any { srvErr++; return "not an object" }
	rd, err := s.collector(t, Options{}).Repo(context.Background(), "o/r")
	if err != nil || len(rd.PRs) != 2 || len(rd.Notes) != 2 || !strings.HasPrefix(rd.Notes[0], "CI runs could not be read:") || !strings.HasPrefix(rd.Notes[1], "Deploys could not be read:") {
		t.Errorf("prs=%d notes=%q err=%v", len(rd.PRs), rd.Notes, err)
	}
}

func TestDeploys(t *testing.T) {
	dep := func(env string, at time.Time) obj {
		return obj{"environment": env, "ref": "main", "created_at": ts(at)}
	}
	rel := func(tag string, at time.Time, draft, pre bool) obj {
		return obj{"tag_name": tag, "draft": draft, "prerelease": pre, "published_at": ts(at)}
	}
	cases := []struct {
		name     string
		deps     []obj
		rels     []obj
		source   string
		n        int
		releases bool
	}{
		{"a production environment wins", []obj{dep("production", day(1)), dep("Production-EU", day(2)), dep("production", day(3)), dep("staging", day(1)), dep("staging", day(2)), dep("staging", day(3)), dep("reproduction", day(3)), dep("production", day(-2))},
			[]obj{rel("v1", day(1), false, false)}, "deployments:production", 2, false},
		{"releases when no environment looks like production", []obj{dep("staging", day(1)), dep("github-pages", day(1))},
			[]obj{rel("v3", day(3), false, false), rel("v2", day(2), true, false), rel("v2-rc", day(2), false, true), rel("v0", day(-5), false, false), rel("v9", until.Add(time.Hour), false, false)}, "releases", 1, true},
		{"pages when there is nothing else", []obj{dep("github-pages", day(1)), dep("github-pages", day(2))}, nil, "deployments:github-pages", 2, true},
		{"nothing", nil, nil, "none", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &server{graphql: onePage}
			s.rest = func(path string, q url.Values) any {
				switch path {
				case "/repos/o/r/deployments":
					return append([]obj{}, c.deps...)
				case "/repos/o/r/releases":
					return append([]obj{}, c.rels...)
				}
				t.Errorf("unexpected GET %s", path)
				return nil
			}
			rd, err := s.collector(t, Options{SkipCI: true}).Repo(context.Background(), "o/r")
			if err != nil || rd.DeploySource != c.source || len(rd.Deploys) != c.n {
				t.Errorf("source=%q deploys=%d err=%v", rd.DeploySource, len(rd.Deploys), err)
			}
			if asked := len(s.gets) == 2; asked != c.releases {
				t.Errorf("REST calls: %q", s.gets)
			}
		})
	}
}

func TestReposAndTarget(t *testing.T) {
	repo := func(name string, pushed time.Time, archived, fork bool) obj {
		return obj{"nameWithOwner": "o/" + name, "isArchived": archived, "isFork": fork, "pushedAt": ts(pushed)}
	}
	page := func(next bool, nodes ...obj) obj {
		return obj{"data": obj{"repositoryOwner": obj{"repositories": obj{"pageInfo": obj{"hasNextPage": next, "endCursor": "c1"}, "nodes": nodes}}}}
	}
	owner := func(r gqlReq) any {
		if !strings.Contains(r.Query, "repositoryOwner") {
			return prPage(false, "", node(1, day(1), day(2), "a"))
		}
		if r.Variables["after"] == nil {
			return page(true, repo("a", day(13), false, false), repo("archived", day(12), true, false), repo("fork", day(11), false, true))
		}
		return page(true, repo("b", day(1), false, false), repo("stale", day(-1), false, false), repo("unread", day(5), false, false))
	}
	s := &server{graphql: owner}
	got, err := s.collector(t, Options{}).Repos(context.Background(), "o")
	if err != nil || fmt.Sprint(got) != "[o/a o/b]" || len(s.gql) != 2 {
		t.Errorf("got %v from %d calls, err %v", got, len(s.gql), err)
	}
	s = &server{graphql: owner}
	got, _ = s.collector(t, Options{IncludeForks: true, IncludeArchived: true}).Repos(context.Background(), "o")
	if fmt.Sprint(got) != "[o/a o/archived o/fork o/b]" {
		t.Errorf("with forks and archived: %v", got)
	}

	s = &server{graphql: owner}
	data, err := s.collector(t, Options{SkipCI: true, SkipDeploys: true}).Target(context.Background(), "o")
	if err != nil || len(data) != 2 || data[0].Repo != "o/a" || data[1].Repo != "o/b" || len(data[1].PRs) != 1 {
		t.Errorf("target owner: %d repos, err %v", len(data), err)
	}
	data, err = s.collector(t, Options{SkipCI: true, SkipDeploys: true}).Target(context.Background(), "o/a")
	if err != nil || len(data) != 1 || data[0].Repo != "o/a" {
		t.Errorf("target repo: %v %v", data, err)
	}

	s = &server{graphql: func(gqlReq) any { return page(false, repo("stale", day(-1), false, false)) }}
	if _, err := s.collector(t, Options{}).Target(context.Background(), "o"); err == nil || !strings.Contains(err.Error(), "o has no repository with a push since") {
		t.Errorf("an idle owner: %v", err)
	}
	s = &server{graphql: func(gqlReq) any { return obj{"data": obj{"repositoryOwner": nil}} }}
	if _, err := s.collector(t, Options{}).Repos(context.Background(), "o"); !errors.Is(err, github.ErrNotFound) {
		t.Errorf("an unknown owner: %v", err)
	}
}

func TestPullRequest(t *testing.T) {
	s := &server{graphql: func(r gqlReq) any {
		if r.Variables["number"] == float64(404) {
			return obj{"data": obj{"repository": obj{"pullRequest": nil}}}
		}
		return obj{"data": obj{"repository": obj{"pullRequest": node(12, day(1), day(2), "a")}}}
	}}
	k := s.collector(t, Options{})
	pr, err := k.PullRequest(context.Background(), "o/r", 12)
	if err != nil || pr.Number != 12 || pr.Repo != "o/r" || len(pr.Commits) != 1 {
		t.Errorf("got %+v %v", pr, err)
	}
	if _, err := k.PullRequest(context.Background(), "o/r", 404); !errors.Is(err, github.ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestParseRefs(t *testing.T) {
	for _, bad := range []string{"", "o", "/r", "o/", "o/r/x"} {
		if _, _, err := SplitRepo(bad); err == nil {
			t.Errorf("SplitRepo(%q) must fail", bad)
		}
	}
	if o, n, err := SplitRepo("o/r.js"); err != nil || o != "o" || n != "r.js" {
		t.Errorf("got %q %q %v", o, n, err)
	}
	for in, want := range map[string]string{
		"https://github.com/marshal-org/runlease/pull/78": "marshal-org/runlease#78",
		"github.com/marshal-org/runlease/pull/78/files":   "marshal-org/runlease#78",
		"http://github.com/o/r/pull/1#issuecomment-1":     "o/r#1",
		"o/r#12":                          "o/r#12",
		"https://github.com/o/r/issues/5": "",
		"o/r":                             "",
		"o#3":                             "",
		"o/r#abc":                         "",
		"https://gitlab.com/o/r/pull/1":   "",
	} {
		repo, n, err := ParsePRRef(in)
		got := ""
		if err == nil {
			got = fmt.Sprintf("%s#%d", repo, n)
		}
		if got != want {
			t.Errorf("ParsePRRef(%q) = %q (%v), want %q", in, got, err, want)
		}
	}
}
