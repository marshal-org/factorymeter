//go:build live

// Package live runs factorymeter against real systems: a GitHub repository, a
// Linear workspace and a Jira Cloud site that are kept for testing. It is not
// part of "go test ./...". Run it with scripts/live-test.sh, which says what
// it needs, or with "go test -tags live ./test/live".
//
// The first run makes the tickets and pull requests it checks, and later runs
// find them by their titles and branches and check them again. So a run
// writes to the test systems only what is missing, and one ticket a run, to
// see an update arrive. FM_LIVE_RUN=<name> makes a new set under that name.
//
// What it checks is what a person sees: it builds the binary and runs it,
// with a config file, and reads the JSON of "store tickets" and "report".
package live

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// env is what the run needs, from the environment.
type env struct {
	repo        string // owner/repo of a GitHub repository the token can write to
	ghToken     string
	linearKey   string
	linearTeam  string // the team the Linear tickets are made in, such as MAR
	linearOther string // a second team, for a ticket that moves
	jiraSite    string
	jiraEmail   string
	jiraToken   string
	jiraProject string // a team-managed or company-managed software project
	run         string // the name of the set of fixtures
}

func need(t *testing.T) env {
	t.Helper()
	e := env{
		repo: os.Getenv("FM_LIVE_REPO"), ghToken: os.Getenv("GITHUB_TOKEN"),
		linearKey: os.Getenv("LINEAR_API_KEY"), linearTeam: os.Getenv("FM_LIVE_LINEAR_TEAM"), linearOther: os.Getenv("FM_LIVE_LINEAR_OTHER_TEAM"),
		jiraSite: os.Getenv("FM_LIVE_JIRA_SITE"), jiraEmail: os.Getenv("JIRA_EMAIL"), jiraToken: os.Getenv("JIRA_API_TOKEN"),
		jiraProject: os.Getenv("FM_LIVE_JIRA_PROJECT"), run: os.Getenv("FM_LIVE_RUN"),
	}
	if e.ghToken == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			e.ghToken = strings.TrimSpace(string(out))
			os.Setenv("GITHUB_TOKEN", e.ghToken)
		}
	}
	if e.run == "" {
		e.run = "fm-live"
	}
	var missing []string
	for name, v := range map[string]string{
		"FM_LIVE_REPO": e.repo, "GITHUB_TOKEN": e.ghToken, "LINEAR_API_KEY": e.linearKey, "FM_LIVE_LINEAR_TEAM": e.linearTeam,
		"FM_LIVE_LINEAR_OTHER_TEAM": e.linearOther, "FM_LIVE_JIRA_SITE": e.jiraSite, "JIRA_EMAIL": e.jiraEmail,
		"JIRA_API_TOKEN": e.jiraToken, "FM_LIVE_JIRA_PROJECT": e.jiraProject,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Skipf("the live run needs %s. scripts/live-test.sh says what each one is", strings.Join(missing, ", "))
	}
	return e
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// binary builds factorymeter once for the run.
func binary(t *testing.T, version string) string {
	t.Helper()
	if version != "" {
		out := filepath.Join(t.TempDir(), "factorymeter")
		if b, err := exec.Command("go", "build", "-ldflags", "-X main.version="+version, "-o", out, "../../cmd/factorymeter").CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, b)
		}
		return out
	}
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fm-live")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "factorymeter")
		if b, err := exec.Command("go", "build", "-o", binPath, "../../cmd/factorymeter").CombinedOutput(); err != nil {
			binErr = fmt.Errorf("%v\n%s", err, b)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// fm runs the binary in a directory and returns what it printed.
func fm(t *testing.T, dir string, extra []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary(t, ""), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extra...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// ---- small clients for the writes the fixtures need ----

func call(t *testing.T, method, url string, header map[string]string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && len(data) > 0 && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v: %.300s", method, url, err, data)
		}
	}
	if resp.StatusCode >= 300 && out != nil {
		t.Logf("%s %s: %d %.300s", method, url, resp.StatusCode, data)
	}
	return resp.StatusCode
}

func (e env) linear(t *testing.T, query string, vars map[string]any, out any) {
	t.Helper()
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	call(t, "POST", "https://api.linear.app/graphql", map[string]string{"Authorization": e.linearKey}, map[string]any{"query": query, "variables": vars}, &env)
	if len(env.Errors) > 0 {
		t.Fatalf("linear: %s", env.Errors[0].Message)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			t.Fatal(err)
		}
	}
}

func (e env) jira(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(e.jiraEmail+":"+e.jiraToken))
	return call(t, method, "https://"+e.jiraSite+path, map[string]string{"Authorization": auth}, body, out)
}

func (e env) github(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	return call(t, method, "https://api.github.com"+path, map[string]string{"Authorization": "Bearer " + e.ghToken, "X-GitHub-Api-Version": "2022-11-28"}, body, out)
}

func pause() { time.Sleep(1500 * time.Millisecond) }

// ---- fixtures ----

type lIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	New        bool   `json:"-"`
}

// linearFixtures finds or makes the Linear tickets of the run.
type linearFixtures struct {
	e      env
	teams  map[string]string            // key -> id
	states map[string]map[string]string // team key -> state type or name -> id
	have   map[string]lIssue            // title -> issue
}

func newLinear(t *testing.T, e env) *linearFixtures {
	f := &linearFixtures{e: e, teams: map[string]string{}, states: map[string]map[string]string{}, have: map[string]lIssue{}}
	var teams struct {
		Teams struct {
			Nodes []struct{ ID, Key string } `json:"nodes"`
		} `json:"teams"`
	}
	e.linear(t, `query { teams(first: 100) { nodes { id key } } }`, nil, &teams)
	for _, n := range teams.Teams.Nodes {
		f.teams[strings.ToUpper(n.Key)] = n.ID
	}
	for _, k := range []string{e.linearTeam, e.linearOther} {
		if f.teams[strings.ToUpper(k)] == "" {
			t.Fatalf("the Linear workspace has no team %s", k)
		}
	}
	var st struct {
		WorkflowStates struct {
			Nodes []struct {
				ID, Name, Type string
				Team           struct{ Key string }
			} `json:"nodes"`
		} `json:"workflowStates"`
	}
	e.linear(t, `query { workflowStates(first: 200) { nodes { id name type team { key } } } }`, nil, &st)
	for _, s := range st.WorkflowStates.Nodes {
		k := strings.ToUpper(s.Team.Key)
		if f.states[k] == nil {
			f.states[k] = map[string]string{}
		}
		// The first state of a type stands for the type.
		if f.states[k][s.Type] == "" {
			f.states[k][s.Type] = s.ID
		}
		f.states[k][s.Name] = s.ID
	}
	var issues struct {
		Issues struct {
			Nodes []lIssue `json:"nodes"`
		} `json:"issues"`
	}
	e.linear(t, `query($p: String!) { issues(first: 100, includeArchived: true, filter: { title: { startsWith: $p } }) { nodes { id identifier title } } }`,
		map[string]any{"p": e.run + ": "}, &issues)
	for _, i := range issues.Issues.Nodes {
		f.have[i.Title] = i
	}
	return f
}

func (f *linearFixtures) state(team, typ string) string {
	return f.states[strings.ToUpper(team)][typ]
}

// issue finds the ticket of a case, or makes it and runs its moves.
func (f *linearFixtures) issue(t *testing.T, name, team string, moves ...string) lIssue {
	t.Helper()
	title := f.e.run + ": " + name
	if i, ok := f.have[title]; ok {
		return i
	}
	var out struct {
		IssueCreate struct {
			Issue lIssue `json:"issue"`
		} `json:"issueCreate"`
	}
	f.e.linear(t, `mutation($i: IssueCreateInput!) { issueCreate(input: $i) { issue { id identifier title } } }`,
		map[string]any{"i": map[string]any{"teamId": f.teams[strings.ToUpper(team)], "title": title, "stateId": f.state(team, "unstarted")}}, &out)
	i := out.IssueCreate.Issue
	i.New = true
	for _, m := range moves {
		pause()
		f.move(t, i, team, m)
	}
	f.have[title] = i
	return i
}

func (f *linearFixtures) move(t *testing.T, i lIssue, team, typ string) {
	t.Helper()
	f.e.linear(t, `mutation($id: String!, $s: String!) { issueUpdate(id: $id, input: { stateId: $s }) { success } }`,
		map[string]any{"id": i.ID, "s": f.state(team, typ)}, nil)
}

type jIssue struct {
	Key string
	New bool
}

// jiraFixtures finds or makes the Jira tickets of the run.
type jiraFixtures struct {
	e    env
	have map[string]string // summary -> key
}

func newJira(t *testing.T, e env) *jiraFixtures {
	f := &jiraFixtures{e: e, have: map[string]string{}}
	var out struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"issues"`
	}
	jql := fmt.Sprintf(`project = %q AND summary ~ %q ORDER BY created ASC`, e.jiraProject, e.run)
	e.jira(t, "GET", "/rest/api/3/search/jql?maxResults=100&fields=summary&jql="+url.QueryEscape(jql), nil, &out)
	for _, i := range out.Issues {
		if strings.HasPrefix(i.Fields.Summary, e.run+": ") {
			f.have[i.Fields.Summary] = i.Key
		}
	}
	return f
}

func (f *jiraFixtures) issue(t *testing.T, name, kind, parent string, moves ...string) jIssue {
	t.Helper()
	summary := f.e.run + ": " + name
	if k, ok := f.have[summary]; ok {
		return jIssue{Key: k}
	}
	fields := map[string]any{"project": map[string]any{"key": f.e.jiraProject}, "summary": summary, "issuetype": map[string]any{"name": kind}}
	if parent != "" {
		fields["parent"] = map[string]any{"key": parent}
	}
	var out struct {
		Key string `json:"key"`
	}
	code := f.e.jira(t, "POST", "/rest/api/3/issue", map[string]any{"fields": fields}, &out)
	if code >= 300 && kind == "Subtask" {
		// A company-managed project names it Sub-task.
		fields["issuetype"] = map[string]any{"name": "Sub-task"}
		code = f.e.jira(t, "POST", "/rest/api/3/issue", map[string]any{"fields": fields}, &out)
	}
	if code >= 300 || out.Key == "" {
		t.Fatalf("making the Jira %s %q: status %d", kind, name, code)
	}
	for _, m := range moves {
		pause()
		f.move(t, out.Key, m)
	}
	f.have[summary] = out.Key
	return jIssue{Key: out.Key, New: true}
}

// move takes an issue to the status of a category: new, indeterminate or done.
func (f *jiraFixtures) move(t *testing.T, key, category string) {
	t.Helper()
	var out struct {
		Transitions []struct {
			ID string `json:"id"`
			To struct {
				Name           string `json:"name"`
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"to"`
		} `json:"transitions"`
	}
	f.e.jira(t, "GET", "/rest/api/3/issue/"+key+"/transitions", nil, &out)
	for _, tr := range out.Transitions {
		// "In Review" is in progress too, so In Progress is looked for first.
		if tr.To.StatusCategory.Key == category && (category != "indeterminate" || strings.EqualFold(tr.To.Name, "In Progress")) {
			f.e.jira(t, "POST", "/rest/api/3/issue/"+key+"/transitions", map[string]any{"transition": map[string]any{"id": tr.ID}}, nil)
			return
		}
	}
	for _, tr := range out.Transitions {
		if tr.To.StatusCategory.Key == category {
			f.e.jira(t, "POST", "/rest/api/3/issue/"+key+"/transitions", map[string]any{"transition": map[string]any{"id": tr.ID}}, nil)
			return
		}
	}
	t.Fatalf("%s has no transition to a status of the category %s", key, category)
}

// pull finds the pull request of a branch, or makes it: a branch with one
// file, the pull request, and a merge when asked.
func (e env) pull(t *testing.T, branch, title, body string, merge bool) int {
	t.Helper()
	owner, _, _ := strings.Cut(e.repo, "/")
	var found []struct {
		Number int `json:"number"`
	}
	e.github(t, "GET", "/repos/"+e.repo+"/pulls?state=all&head="+owner+":"+branch, nil, &found)
	if len(found) > 0 {
		return found[0].Number
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	e.github(t, "GET", "/repos/"+e.repo, nil, &repo)
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	e.github(t, "GET", "/repos/"+e.repo+"/git/ref/heads/"+repo.DefaultBranch, nil, &ref)
	if code := e.github(t, "POST", "/repos/"+e.repo+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA}, nil); code >= 300 && code != 422 {
		t.Fatalf("making branch %s: %d", branch, code)
	}
	file := "fm-live/" + strings.NewReplacer("/", "_").Replace(branch) + ".txt"
	content := base64.StdEncoding.EncodeToString([]byte(branch + "\n"))
	e.github(t, "PUT", "/repos/"+e.repo+"/contents/"+file, map[string]any{"message": title, "content": content, "branch": branch}, nil)
	var pr struct {
		Number int `json:"number"`
	}
	if code := e.github(t, "POST", "/repos/"+e.repo+"/pulls", map[string]any{"title": title, "head": branch, "base": repo.DefaultBranch, "body": body}, &pr); code >= 300 {
		t.Fatalf("opening the pull request of %s: %d", branch, code)
	}
	if merge {
		for try := 0; try < 10; try++ {
			time.Sleep(3 * time.Second)
			if code := e.github(t, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/merge", e.repo, pr.Number), map[string]any{"merge_method": "merge"}, nil); code < 300 {
				break
			}
		}
	}
	return pr.Number
}

// ---- what the binary says ----

type ticketRow struct {
	Source   string     `json:"source"`
	Key      string     `json:"key"`
	Aliases  []string   `json:"aliases"`
	State    string     `json:"state"`
	Created  time.Time  `json:"created"`
	Started  *time.Time `json:"started"`
	Done     *time.Time `json:"done"`
	Closed   *time.Time `json:"closed"`
	Reopened int        `json:"reopened"`
	Start    *time.Time `json:"start"`
	Pulls    []struct {
		Number int       `json:"number"`
		Opened time.Time `json:"opened"`
		By     []string  `json:"by"`
	} `json:"pulls"`
}

func tickets(t *testing.T, dir string, target string) map[string]ticketRow {
	t.Helper()
	out, err := fm(t, dir, nil, "store", "tickets", target, "--format", "json")
	if err != nil {
		t.Fatalf("store tickets: %v\n%s", err, out)
	}
	var rows []ticketRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("store tickets json: %v\n%.500s", err, out)
	}
	m := map[string]ticketRow{}
	for _, r := range rows {
		m[r.Key] = r
	}
	return m
}

func (r ticketRow) pulledBy(n int) []string {
	for _, p := range r.Pulls {
		if p.Number == n {
			return p.By
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- the run ----

func TestLiveTicketsEndToEnd(t *testing.T) {
	e := need(t)
	lin := newLinear(t, e)
	jir := newJira(t, e)
	L, L2 := e.linearTeam, e.linearOther

	// Linear tickets, each in one shape a real workspace has.
	branch := lin.issue(t, "key in the branch", L)
	reopened := lin.issue(t, "reopened", L, "started", "completed", "unstarted", "started", "completed")
	cancelled := lin.issue(t, "cancelled never started", L, "canceled")
	duplicate := lin.issue(t, "duplicate", L)
	if duplicate.New {
		e.linear(t, `mutation($i: IssueRelationCreateInput!) { issueRelationCreate(input: $i) { success } }`,
			map[string]any{"i": map[string]any{"issueId": duplicate.ID, "relatedIssueId": cancelled.ID, "type": "duplicate"}}, nil)
		pause()
		lin.move(t, duplicate, L, "duplicate")
	}
	trashed := lin.issue(t, "trashed", L, "started")
	if trashed.New {
		pause()
		e.linear(t, `mutation($id: String!) { issueDelete(id: $id) { success } }`, map[string]any{"id": trashed.ID}, nil)
	}
	doneNoStart := lin.issue(t, "done without a start", L, "completed")
	moved := lin.issue(t, "moved to another team", L, "started")
	oldKey := moved.Identifier
	if moved.New {
		var out struct {
			IssueUpdate struct {
				Issue lIssue `json:"issue"`
			} `json:"issueUpdate"`
		}
		e.linear(t, `mutation($id: String!, $i: IssueUpdateInput!) { issueUpdate(id: $id, input: $i) { issue { id identifier title } } }`,
			map[string]any{"id": moved.ID, "i": map[string]any{"teamId": lin.teams[strings.ToUpper(L2)], "stateId": lin.state(L2, "started")}}, &out)
		moved = out.IssueUpdate.Issue
	} else {
		var out struct {
			Issue struct {
				PreviousIdentifiers []string `json:"previousIdentifiers"`
			} `json:"issue"`
		}
		e.linear(t, `query($id: String!) { issue(id: $id) { previousIdentifiers } }`, map[string]any{"id": moved.ID}, &out)
		if len(out.Issue.PreviousIdentifiers) > 0 {
			oldKey = out.Issue.PreviousIdentifiers[0]
		}
	}
	attached := lin.issue(t, "attached only", L)
	var long []string
	for i := 0; i < 8; i++ {
		long = append(long, "started", "completed", "unstarted")
	}
	long = append(long, "completed")
	longHistory := lin.issue(t, "a history of 25 moves", L, long...)
	openL := lin.issue(t, "named in an open pull request", L)
	touchL := lin.issue(t, "moved every run", L)

	// Jira tickets.
	jTitle := jir.issue(t, "key in the title", "Task", "", "indeterminate", "done")
	jReopened := jir.issue(t, "reopened", "Task", "", "indeterminate", "done", "new", "indeterminate", "done")
	jNever := jir.issue(t, "named in an open pull request", "Task", "")
	jSub := jir.issue(t, "a subtask", "Subtask", jTitle.Key, "indeterminate")
	jDoneNoStart := jir.issue(t, "done without a start", "Task", "", "done")
	jEpic := jir.issue(t, "an epic", "Epic", "")
	jTouch := jir.issue(t, "moved every run", "Task", "")

	// Pull requests that name them.
	run := e.run
	pBranch := e.pull(t, run+"/"+strings.ToLower(branch.Identifier)+"-work", run+": the key is in the branch", "No key here.", true)
	pTitle := e.pull(t, run+"/jira-title", run+": "+jTitle.Key+" in the title", fmt.Sprintf("Also the subtask %s and the epic %s.", jSub.Key, jEpic.Key), true)
	pOld := e.pull(t, run+"/old-key", run+": an old key", "Mentions "+oldKey+", the key the ticket had before it moved.", true)
	jPrefix, _, _ := strings.Cut(jTitle.Key, "-")
	lPrefix, _, _ := strings.Cut(reopened.Identifier, "-")
	negatives := fmt.Sprintf("None of these are keys: utf-8, sha-256, x%s, %sx, %s-999999, %s-999999.", jTitle.Key, jTitle.Key, jPrefix, lPrefix)
	pNeg := e.pull(t, run+"/negatives", run+": look-alikes", negatives, true)
	pAttached := e.pull(t, run+"/attached", run+": attached only", "No key anywhere.", true)
	if attached.New {
		e.linear(t, `mutation($i: AttachmentCreateInput!) { attachmentCreate(input: $i) { success } }`, map[string]any{"i": map[string]any{
			"issueId": attached.ID, "url": fmt.Sprintf("https://github.com/%s/pull/%d", e.repo, pAttached), "title": run + " attached"}}, nil)
	}
	many := fmt.Sprintf("Mentions %s, %s, %s, %s, %s, %s, %s and %s.", reopened.Identifier, jReopened.Key, cancelled.Identifier, duplicate.Identifier,
		trashed.Identifier, doneNoStart.Identifier, jDoneNoStart.Key, longHistory.Identifier)
	pMany := e.pull(t, run+"/many", run+": many keys in the description", many, true)
	pOpen := e.pull(t, run+"/open", fmt.Sprintf("%s: lower case (%s) and (%s)", run, strings.ToLower(openL.Identifier), strings.ToLower(jNever.Key)), "Left open on purpose.", false)

	// One ticket of each system moves every run, so a sync sees an update.
	lStarted, jStarted := time.Now().Unix()%2 == 0, time.Now().Unix()%2 == 0
	if lStarted {
		lin.move(t, touchL, L, "started")
	} else {
		lin.move(t, touchL, L, "unstarted")
	}
	if jStarted {
		jir.move(t, jTouch.Key, "indeterminate")
	} else {
		jir.move(t, jTouch.Key, "new")
	}
	// Linear and Jira can show a change a little after they accept it.
	time.Sleep(10 * time.Second)

	// The person's side: a config file, and the binary.
	dir := t.TempDir()
	cfg := fmt.Sprintf("target: %s\nstore: store\nhistory: 26w\nsync:\n  skip_ci: true\n  skip_deploys: true\nlinear:\n  teams: [%s, %s]\njira:\n  site: %s\n  projects: [%s]\n",
		e.repo, L, L2, e.jiraSite, e.jiraProject)
	if err := os.WriteFile(filepath.Join(dir, "factorymeter.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := fm(t, dir, nil, "sync", "--plain")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	for _, want := range []string{"config: factorymeter.yml", "Linear ", "Jira " + e.jiraSite} {
		if !strings.Contains(out, want) {
			t.Errorf("sync did not say %q:\n%s", want, out)
		}
	}
	for _, prefix := range []string{strings.ToUpper(L), strings.ToUpper(L2), strings.ToUpper(e.jiraProject)} {
		if !regexp.MustCompile(`keys [A-Z0-9 ]*\b` + prefix + `\b`).MatchString(out) {
			t.Errorf("sync did not list the key prefix %s:\n%s", prefix, out)
		}
	}

	all := tickets(t, dir, strings.SplitN(e.repo, "/", 2)[0])
	one := tickets(t, dir, e.repo)
	get := func(key string) ticketRow {
		t.Helper()
		r, ok := all[key]
		if !ok {
			t.Errorf("%s is not in the store", key)
		}
		return r
	}
	check := func(name string, ok bool, format string, args ...any) {
		t.Helper()
		if !ok {
			t.Errorf(name+": "+format, args...)
		}
	}

	r := get(branch.Identifier)
	check("key in the branch", contains(r.pulledBy(pBranch), "key") && r.Start != nil, "pulls %+v start %v", r.Pulls, r.Start)

	r = get(reopened.Identifier)
	check("reopened", r.State == "done" && r.Reopened == 1 && r.Done != nil && r.Started != nil && r.Done.Sub(*r.Started) > 4*time.Second,
		"state %s reopened %d started %v done %v: the done time is the last time it was done, not the first", r.State, r.Reopened, r.Started, r.Done)
	check("reopened", contains(r.pulledBy(pMany), "key"), "pulls %+v", r.Pulls)

	r = get(cancelled.Identifier)
	check("cancelled", r.State == "cancelled" && r.Start == nil, "state %s start %v: a pull request after the close does not start it", r.State, r.Start)
	r = get(duplicate.Identifier)
	check("duplicate", r.State == "cancelled" && r.Start == nil, "state %s start %v", r.State, r.Start)
	r = get(trashed.Identifier)
	check("trashed", r.State == "cancelled" && r.Started != nil, "state %s started %v", r.State, r.Started)
	r = get(doneNoStart.Identifier)
	check("done without a start", r.State == "done" && r.Start == nil, "state %s start %v", r.State, r.Start)

	r = get(moved.Identifier)
	check("moved", contains(r.Aliases, oldKey) && contains(r.pulledBy(pOld), "key"), "aliases %v pulls %+v, want %s by key", r.Aliases, r.Pulls, oldKey)

	r = get(attached.Identifier)
	by := r.pulledBy(pAttached)
	check("attached only", contains(by, "attached") && !contains(by, "key") && r.Start != nil && !r.Start.Before(r.Pulls[0].Opened),
		"pulls %+v start %v", r.Pulls, r.Start)

	r = get(longHistory.Identifier)
	check("a history of 25 moves", r.State == "done" && r.Reopened == 8, "state %s reopened %d: the history is read past its first page", r.State, r.Reopened)

	r = get(openL.Identifier)
	check("lower case, Linear", contains(r.pulledBy(pOpen), "key"), "pulls %+v", r.Pulls)
	r = get(jNever.Key)
	check("lower case, Jira", contains(r.pulledBy(pOpen), "key") && r.Start != nil, "pulls %+v start %v", r.Pulls, r.Start)

	r = get(jTitle.Key)
	check("Jira key in the title", r.State == "done" && contains(r.pulledBy(pTitle), "key"), "state %s pulls %+v", r.State, r.Pulls)
	r = get(jReopened.Key)
	check("Jira reopened", r.State == "done" && r.Reopened == 1, "state %s reopened %d", r.State, r.Reopened)
	r = get(jSub.Key)
	check("Jira subtask", r.State == "started" && contains(r.pulledBy(pTitle), "key"), "state %s pulls %+v", r.State, r.Pulls)
	r = get(jDoneNoStart.Key)
	check("Jira done without a start", r.State == "done" && r.Start == nil, "state %s start %v", r.State, r.Start)
	if _, ok := all[jEpic.Key]; ok {
		t.Errorf("the epic %s is in the store: an epic is not a ticket", jEpic.Key)
	}

	// No ticket is tied to the pull request of look-alikes.
	for k, r := range all {
		if r.pulledBy(pNeg) != nil {
			t.Errorf("%s is tied to the pull request of look-alikes (#%d)", k, pNeg)
		}
	}
	// The report of the repository lists only tickets a pull request is tied to.
	if _, ok := one[touchL.Identifier]; ok {
		t.Errorf("%s has no pull request and is in the list of the repository", touchL.Identifier)
	}
	if _, ok := one[reopened.Identifier]; !ok {
		t.Errorf("%s is tied to #%d and is not in the list of the repository", reopened.Identifier, pMany)
	}

	// The report sees the two systems with the keys it searches for.
	out, err = fm(t, dir, nil, "report", "--format", "json")
	if err != nil {
		t.Fatalf("report: %v\n%.500s", err, out)
	}
	var rep struct {
		Coverage struct {
			Trackers []struct {
				Source  string   `json:"source"`
				Tickets int      `json:"tickets"`
				Linked  int      `json:"linked"`
				Keys    []string `json:"keys"`
			} `json:"trackers"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Coverage.Trackers) != 2 {
		t.Fatalf("trackers = %+v", rep.Coverage.Trackers)
	}
	for _, tr := range rep.Coverage.Trackers {
		if tr.Linked < 6 || tr.Tickets < tr.Linked || len(tr.Keys) == 0 {
			t.Errorf("tracker %+v", tr)
		}
	}

	// A second sync reads only what changed: the two tickets moved above.
	if lStarted {
		lin.move(t, touchL, L, "unstarted")
	} else {
		lin.move(t, touchL, L, "started")
	}
	if jStarted {
		jir.move(t, jTouch.Key, "new")
	} else {
		jir.move(t, jTouch.Key, "indeterminate")
	}
	time.Sleep(10 * time.Second)
	out, err = fm(t, dir, nil, "sync", "--plain")
	if err != nil {
		t.Fatalf("second sync: %v\n%s", err, out)
	}
	for _, re := range []string{`Linear [^:]+: tickets \d+ \(\+\d+, [1-9]\d* updated\)`, `Jira [^:]+: tickets \d+ \(\+\d+, [1-9]\d* updated\)`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("the second sync did not read an update (%s):\n%s", re, out)
		}
	}
	after := tickets(t, dir, strings.SplitN(e.repo, "/", 2)[0])
	wantL, wantJ := "started", "started"
	if lStarted {
		wantL = "unstarted"
	}
	if jStarted {
		wantJ = "unstarted"
	}
	if s := after[touchL.Identifier].State; s != wantL {
		t.Errorf("%s is %s after the second sync, want %s", touchL.Identifier, s, wantL)
	}
	if s := after[jTouch.Key].State; s != wantJ {
		t.Errorf("%s is %s after the second sync, want %s", jTouch.Key, s, wantJ)
	}
}

// Errors a person can make, against the real APIs. Each one fails fast, says
// what to do, and never repeats a secret.
func TestLiveErrors(t *testing.T) {
	e := need(t)
	dir := t.TempDir()
	base := []string{"sync", e.repo, "--store", filepath.Join(dir, "store"), "--plain", "--skip-ci", "--skip-deploys", "--skip-tickets", "--no-config"}
	cases := []struct {
		name  string
		env   []string
		args  []string
		want  string
		since time.Duration
	}{
		{"a wrong Linear key", []string{"LINEAR_API_KEY=lin_api_not_a_key"}, []string{"--linear"}, "linear rejected the key (401)", 0},
		{"a Linear team that is not there", nil, []string{"--linear-team", "NOPE9"}, "has no team with the key NOPE9", 0},
		{"a wrong Jira token", []string{"JIRA_API_TOKEN=wrong"}, []string{"--jira", e.jiraSite}, "jira rejected the email and the API token (401)", 0},
		{"a Jira site name without its domain", nil, []string{"--jira", strings.SplitN(e.jiraSite, ".", 2)[0]}, "is not a whole host", 0},
		{"a Jira project that is not there", nil, []string{"--jira", e.jiraSite, "--jira-project", "NOPE9"}, "has no project with the key NOPE9", 0},
		{"a Jira site that is not there", nil, []string{"--jira", "fm-live-no-such-site-9.atlassian.net"}, "does not answer as a Jira Cloud site does", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			began := time.Now()
			out, err := fm(t, dir, c.env, append(base, c.args...)...)
			if err == nil || !strings.Contains(out, c.want) {
				t.Errorf("err %v, want %q in:\n%s", err, c.want, out)
			}
			if took := time.Since(began); took > 45*time.Second {
				t.Errorf("took %s", took)
			}
			for _, secret := range []string{e.linearKey, e.jiraToken, e.ghToken} {
				if strings.Contains(out, secret) {
					t.Error("the output holds a secret")
				}
			}
		})
	}
}

// The install script of the site and "upgrade", against the real releases.
// Only the network is needed.
func TestLiveReleases(t *testing.T) {
	if os.Getenv("FM_LIVE_RELEASES") == "0" {
		t.Skip("FM_LIVE_RELEASES=0")
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get("https://github.com/marshal-org/factorymeter/releases/latest")
	if err != nil {
		t.Skip("github.com cannot be reached: ", err)
	}
	resp.Body.Close()
	latest := filepath.Base(resp.Header.Get("Location"))
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(latest) {
		t.Fatalf("latest release = %q", latest)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the install script needs a POSIX shell")
	}

	t.Run("install", func(t *testing.T) {
		home := t.TempDir()
		cmd := exec.Command("sh", "../../site/install.sh")
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/local/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "Installed factorymeter "+latest) {
			t.Fatalf("%v\n%s", err, out)
		}
		v, err := exec.Command(filepath.Join(home, ".local", "bin", "factorymeter"), "--version").Output()
		if err != nil || !strings.Contains(string(v), latest) {
			t.Errorf("installed binary says %q %v", v, err)
		}
	})

	t.Run("upgrade", func(t *testing.T) {
		old := binary(t, "v0.0.1")
		out, err := exec.Command(old, "upgrade").CombinedOutput()
		if err != nil || !strings.Contains(string(out), ": "+latest+", was v0.0.1.") {
			t.Fatalf("%v\n%s", err, out)
		}
		v, _ := exec.Command(old, "--version").Output()
		if !strings.Contains(string(v), latest) {
			t.Errorf("after the upgrade the binary says %q", v)
		}
	})
}
