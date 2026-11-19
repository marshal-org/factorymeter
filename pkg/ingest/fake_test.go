package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/github"
)

// This file plays GitHub for the sync tests. It keeps pull requests, workflow
// runs, deployments and releases per repository and answers the queries the
// collector sends. Its cursors hold a position by value, an update time and a
// number, as GitHub's do, so a cursor saved in one sync works in the next.

type obj = map[string]any

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

type pr struct {
	number           int
	state            string
	title            string
	created, updated time.Time
	shas             []string
}

type ciRun struct {
	id         int
	sha, event string
	created    time.Time
	conclusion string
}

type deployment struct {
	id       int
	env      string
	created  time.Time
	sha      string
	statuses []obj // newest first, as GitHub returns them
}

type release struct {
	tag                string
	draft, pre         bool
	created, published time.Time
	sha                string
}

type repo struct {
	name     string
	missing  bool
	prs      []*pr
	runs     []ciRun
	deploys  []deployment
	releases []release
}

type call struct {
	// kind is "prs", "deploys", "repos" or "runs".
	kind string
	repo string
	// after is the cursor a pull request page was asked from.
	after string
	open  bool
	asc   bool
	size  int
	// event and created are the filters of a runs request.
	event   string
	created string
}

type fakeHub struct {
	t     *testing.T
	mu    sync.Mutex
	repos map[string]*repo
	// order lists the owner's repositories, most recently pushed first.
	order []string
	calls []call
	// maxPage makes GitHub time out on a bigger pull request page. Zero is no limit.
	maxPage int
	// rejectCursors makes every cursor invalid, once.
	rejectCursors bool
	// onPage runs before a pull request page is answered, with the number of
	// such pages asked for so far. It may block until the request is cancelled.
	onPage func(n int, r *http.Request)
	srv    *httptest.Server
}

func newHub(t *testing.T) *fakeHub {
	h := &fakeHub{t: t, repos: map[string]*repo{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) client() *github.Client {
	c := github.New("t")
	c.BaseURL, c.GraphQLURL = h.srv.URL, h.srv.URL+"/graphql"
	return c
}

func (h *fakeHub) add(name string) *repo {
	r := &repo{name: name}
	h.mu.Lock()
	h.repos[strings.ToLower(name)] = r
	h.order = append(h.order, name)
	h.mu.Unlock()
	return r
}

func (r *repo) pr(n int, state string, created, updated time.Time, shas ...string) *pr {
	p := &pr{number: n, state: state, title: fmt.Sprintf("change %d", n), created: created, updated: updated, shas: shas}
	r.prs = append(r.prs, p)
	return p
}

// take returns the calls made so far and forgets them.
func (h *fakeHub) take() []call {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.calls
	h.calls = nil
	return c
}

func of(calls []call, kind string) []call {
	var out []call
	for _, c := range calls {
		if c.kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			h.t.Errorf("graphql body: %v", err)
		}
		json.NewEncoder(w).Encode(h.graphql(req.Query, req.Variables, r))
		return
	}
	json.NewEncoder(w).Encode(h.rest(r))
}

func gqlErr(kind, msg string) obj {
	return obj{"data": obj{"repository": nil}, "errors": []obj{{"type": kind, "message": msg}}}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func (h *fakeHub) find(vars map[string]any) *repo {
	r := h.repos[strings.ToLower(str(vars["owner"])+"/"+str(vars["name"]))]
	if r == nil || r.missing {
		return nil
	}
	return r
}

func cursor(p *pr) string { return fmt.Sprintf("v2:%d:%d", p.updated.UnixNano(), p.number) }

func parseCursor(s string) (int64, int, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] != "v2" {
		return 0, 0, false
	}
	at, err1 := strconv.ParseInt(parts[1], 10, 64)
	n, err2 := strconv.Atoi(parts[2])
	return at, n, err1 == nil && err2 == nil
}

func (h *fakeHub) graphql(query string, vars map[string]any, req *http.Request) any {
	h.mu.Lock()
	switch {
	case strings.Contains(query, "repositoryOwner"):
		h.calls = append(h.calls, call{kind: "repos"})
		var nodes []obj
		for i, name := range h.order {
			nodes = append(nodes, obj{"nameWithOwner": name, "pushedAt": ts(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour))})
		}
		h.mu.Unlock()
		return obj{"data": obj{"repositoryOwner": obj{"repositories": obj{"pageInfo": obj{"hasNextPage": false}, "nodes": nodes}}}}

	case strings.Contains(query, "pullRequests("):
		c := call{kind: "prs", repo: str(vars["owner"]) + "/" + str(vars["name"]), after: str(vars["after"]), asc: vars["dir"] == "ASC"}
		if n, ok := vars["n"].(float64); ok {
			c.size = int(n)
		}
		if st, ok := vars["states"].([]any); ok && len(st) == 1 && st[0] == "OPEN" {
			c.open = true
		}
		h.calls = append(h.calls, c)
		n := len(of(h.calls, "prs"))
		hook := h.onPage
		h.mu.Unlock()
		if hook != nil {
			hook(n, req)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.page(c, vars)

	case strings.Contains(query, "deployments("):
		h.calls = append(h.calls, call{kind: "deploys", repo: str(vars["owner"]) + "/" + str(vars["name"])})
		defer h.mu.Unlock()
		r := h.find(vars)
		if r == nil {
			return obj{"data": obj{"repository": nil}}
		}
		var deps, rels []obj
		ds := append([]deployment(nil), r.deploys...)
		sort.Slice(ds, func(i, j int) bool { return ds[i].created.After(ds[j].created) })
		for _, d := range ds {
			n := obj{"databaseId": d.id, "environment": d.env, "createdAt": ts(d.created), "commitOid": d.sha,
				"ref": obj{"name": "main"}, "statuses": obj{"nodes": d.statuses}}
			if len(d.statuses) > 0 {
				n["latestStatus"] = d.statuses[0]
			}
			deps = append(deps, n)
		}
		rs := append([]release(nil), r.releases...)
		sort.Slice(rs, func(i, j int) bool { return rs[i].created.After(rs[j].created) })
		for _, x := range rs {
			n := obj{"tagName": x.tag, "isDraft": x.draft, "isPrerelease": x.pre, "createdAt": ts(x.created), "tagCommit": obj{"oid": x.sha}}
			if !x.published.IsZero() {
				n["publishedAt"] = ts(x.published)
			}
			rels = append(rels, n)
		}
		return obj{"data": obj{"repository": obj{
			"deployments": obj{"pageInfo": obj{"hasNextPage": false}, "nodes": deps},
			"releases":    obj{"pageInfo": obj{"hasNextPage": false}, "nodes": rels}}}}
	}
	h.mu.Unlock()
	h.t.Errorf("unexpected query: %s", query)
	return obj{}
}

// page answers one pull request page. The caller holds the lock.
func (h *fakeHub) page(c call, vars map[string]any) any {
	r := h.find(vars)
	if r == nil {
		return obj{"data": obj{"repository": nil}}
	}
	if h.maxPage > 0 && c.size > h.maxPage {
		return obj{"errors": []obj{{"message": "Something went wrong while executing your query. This may be the result of a timeout."}}}
	}
	list := make([]*pr, 0, len(r.prs))
	for _, p := range r.prs {
		if !c.open || p.state == "OPEN" {
			list = append(list, p)
		}
	}
	less := func(a, b *pr) bool {
		if !a.updated.Equal(b.updated) {
			return a.updated.Before(b.updated)
		}
		return a.number < b.number
	}
	sort.Slice(list, func(i, j int) bool {
		if c.asc {
			return less(list[i], list[j])
		}
		return less(list[j], list[i])
	})
	if c.after != "" {
		at, n, ok := parseCursor(c.after)
		if !ok || h.rejectCursors {
			h.rejectCursors = false
			return gqlErr("INVALID_CURSOR_ARGUMENTS", "`"+c.after+"` does not appear to be a valid cursor.")
		}
		mark := &pr{number: n, updated: time.Unix(0, at)}
		rest := list[:0:0]
		for _, p := range list {
			if c.asc && less(mark, p) || !c.asc && less(p, mark) {
				rest = append(rest, p)
			}
		}
		list = rest
	}
	more := len(list) > c.size
	if more {
		list = list[:c.size]
	}
	nodes := []obj{}
	end := ""
	for _, p := range list {
		var commits []obj
		for _, sha := range p.shas {
			commits = append(commits, obj{"commit": obj{"oid": sha, "authoredDate": ts(p.created), "committedDate": ts(p.created),
				"message": "fix: a thing\n\nCo-Authored-By: Claude <noreply@anthropic.com>", "parents": obj{"totalCount": 1},
				"authors": obj{"nodes": []obj{{"name": "Dev", "email": "dev@example.invalid", "user": obj{"login": "dev"}}}}}})
		}
		nodes = append(nodes, obj{"number": p.number, "url": fmt.Sprintf("https://github.com/%s/pull/%d", r.name, p.number),
			"title": p.title, "state": p.state, "createdAt": ts(p.created), "updatedAt": ts(p.updated),
			"headRefName": "fix/x", "baseRefName": "main", "author": obj{"__typename": "User", "login": "dev"},
			"commits": obj{"totalCount": len(p.shas), "nodes": commits}, "reviews": obj{"nodes": []obj{}}, "timelineItems": obj{"nodes": []obj{}}})
		end = cursor(p)
	}
	return obj{"data": obj{"repository": obj{"nameWithOwner": r.name, "isPrivate": true,
		"pullRequests": obj{"pageInfo": obj{"hasNextPage": more, "endCursor": end}, "nodes": nodes}}}}
}

func (h *fakeHub) rest(req *http.Request) any {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := req.URL.Path
	if !strings.HasPrefix(path, "/repos/") || !strings.HasSuffix(path, "/actions/runs") {
		h.t.Errorf("unexpected GET %s", req.URL)
		return obj{}
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/"), "/actions/runs")
	q := req.URL.Query()
	h.calls = append(h.calls, call{kind: "runs", repo: name, event: q.Get("event"), created: q.Get("created")})
	r := h.repos[strings.ToLower(name)]
	if r == nil {
		return obj{"total_count": 0, "workflow_runs": []obj{}}
	}
	lo, hi, ok := strings.Cut(q.Get("created"), "..")
	from, err1 := time.Parse(time.RFC3339, lo)
	to, err2 := time.Parse(time.RFC3339, hi)
	if !ok || err1 != nil || err2 != nil {
		h.t.Errorf("created=%q is not a range", q.Get("created"))
	}
	var hit []ciRun
	for _, x := range r.runs {
		if x.created.Before(from) || x.created.After(to) || q.Get("event") != "" && q.Get("event") != x.event {
			continue
		}
		hit = append(hit, x)
	}
	sort.Slice(hit, func(i, j int) bool { return hit[i].created.After(hit[j].created) })
	total := len(hit)
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	start := min((page-1)*100, total)
	hit = hit[start:min(start+100, total)]
	runs := []obj{}
	for _, x := range hit {
		status, conclusion := "completed", x.conclusion
		if conclusion == "" {
			status = "in_progress"
		}
		runs = append(runs, obj{"id": x.id, "name": "ci", "event": x.event, "head_sha": x.sha, "run_attempt": 1,
			"status": status, "conclusion": conclusion,
			"created_at": ts(x.created), "run_started_at": ts(x.created), "updated_at": ts(x.created.Add(5 * time.Minute))})
	}
	return obj{"total_count": total, "workflow_runs": runs}
}
