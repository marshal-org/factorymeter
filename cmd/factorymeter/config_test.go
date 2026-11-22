package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fullConfig = `
target: acme/app
store: ./fm-store
history: 30w
window: 8w
summary: true
registry: sigs.yml
sync:
  workers: 2
  max_runs: 500
  skip_ci: true
  skip_deploys: false
  skip_tickets: true
  include_forks: true
  include_archived: true
linear:
  teams: [ENG, ops]
jira:
  site: acme.atlassian.net
  projects: [PAY, SAM1]
serve:
  addr: 127.0.0.1:9000
  allow_hosts: [line.internal, "a,b.internal"]
`

func TestParseConfig(t *testing.T) {
	c, err := parseConfig([]byte(fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	if c.Target != "acme/app" || c.History != "30w" || c.Window != "8w" || c.Summary == nil || !*c.Summary || *c.Sync.Workers != 2 ||
		!c.Linear.On || !reflect.DeepEqual(c.Linear.Teams, []string{"ENG", "ops"}) || c.Jira.Site != "acme.atlassian.net" ||
		!reflect.DeepEqual(c.Serve.AllowHosts, []string{"line.internal", "a,b.internal"}) {
		t.Errorf("config = %+v", c)
	}
	for in, on := range map[string]bool{"linear: true": true, "linear: false": false, "linear: {}": true, "": false, "linear:": false} {
		c, err := parseConfig([]byte(in))
		if err != nil || c.Linear.On != on {
			t.Errorf("%q: on = %v, err = %v", in, c != nil && c.Linear.On, err)
		}
	}
}

func TestParseConfigRefuses(t *testing.T) {
	cases := []struct{ in, want string }{
		{"targett: acme", "field targett not found"},
		{"sync:\n  worker: 2", "field worker not found"},
		{"linear:\n  team: [ENG]", "field team not found in linear"},
		{"linear: maybe", "linear is true, false, or a list of teams"},
		{"target: [a", "not YAML"},
		// A key that names a secret is refused by its name.
		{"linear:\n  api_key: hunter2secret", `"api_key" holds a secret`},
		{"jira:\n  email: hunter2secret@acme.com", `"email" holds a secret`},
		{"github_token: hunter2secret", `"github_token" holds a secret`},
		{"jira:\n  token: hunter2secret", `"token" holds a secret`},
		// A value that looks like a token is refused wherever it is.
		{"jira:\n  site: ATATT3xhunter2secret", "the value of jira.site looks like a token"},
		{"target: lin_api_hunter2secret", "the value of target looks like a token"},
		{"serve:\n  allow_hosts: [ghp_hunter2secret]", "the value of serve.allow_hosts looks like a token"},
	}
	for _, c := range cases {
		_, err := parseConfig([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error = %v, want %q", c.in, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "hunter2secret") {
			t.Errorf("%q: the error repeats the secret: %v", c.in, err)
		}
	}
}

func TestFindConfig(t *testing.T) {
	dir := t.TempDir()
	if p, err := findConfig("", dir); p != "" || err != nil {
		t.Errorf("none: %q %v", p, err)
	}
	if p, err := findConfig("other.yml", dir); p != "other.yml" || err != nil {
		t.Errorf("named: %q %v", p, err)
	}
	os.MkdirAll(filepath.Join(dir, ".github"), 0o755)
	os.WriteFile(filepath.Join(dir, ".github", "factorymeter.yml"), nil, 0o644)
	if p, err := findConfig("", dir); p != filepath.Join(dir, ".github", "factorymeter.yml") || err != nil {
		t.Errorf("one: %q %v", p, err)
	}
	os.WriteFile(filepath.Join(dir, "factorymeter.yaml"), nil, 0o644)
	if _, err := findConfig("", dir); err == nil || !strings.Contains(err.Error(), "2 config files here: factorymeter.yaml, .github/factorymeter.yml") {
		t.Errorf("two: %v", err)
	}
}

// A setting of the file fills a flag the command line left alone, and a flag
// on the command line wins.
func TestConfigFillsTheFlagsTheCommandLineLeaves(t *testing.T) {
	c, err := parseConfig([]byte(fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	c.path = "factorymeter.yml"
	config = configState{file: c, set: map[string]bool{}}
	t.Cleanup(func() { config = configState{} })

	cmd := syncCmd()
	if err := cmd.ParseFlags([]string{"--workers", "7", "--jira-project", "OPS"}); err != nil {
		t.Fatal(err)
	}
	if err := c.apply(cmd); err != nil {
		t.Fatal(err)
	}
	get := func(name string) string { return cmd.Flags().Lookup(name).Value.String() }
	want := map[string]string{
		"since": "30w", "store": "./fm-store", "workers": "7", "max-runs": "500", "skip-ci": "true", "skip-deploys": "false",
		"skip-tickets": "true", "include-forks": "true", "include-archived": "true", "linear": "true", "linear-team": "[ENG,ops]",
		"jira": "acme.atlassian.net", "jira-project": "[OPS]",
	}
	for k, v := range want {
		if got := get(k); got != v {
			t.Errorf("--%s = %q, want %q", k, got, v)
		}
	}
	if fromConfig("workers") || fromConfig("jira-project") || !fromConfig("jira") || !fromConfig("linear") {
		t.Errorf("set by the file: %v", config.set)
	}

	// report reads the window, not the history.
	rep := reportCmd()
	rep.ParseFlags(nil)
	if err := c.apply(rep); err != nil {
		t.Fatal(err)
	}
	if got := rep.Flags().Lookup("since").Value.String(); got != "8w" {
		t.Errorf("report --since = %q, want the window", got)
	}
	// serve takes each host as one, also one with a comma in it, so the
	// check of the host refuses it instead of reading two.
	srv := serveCmd()
	srv.ParseFlags(nil)
	if err := c.apply(srv); err != nil {
		t.Fatal(err)
	}
	if got := srv.Flags().Lookup("allow-host").Value.String(); got != `[line.internal,"a,b.internal"]` || srv.Flags().Lookup("addr").Value.String() != "127.0.0.1:9000" {
		t.Errorf("serve: %q %q", got, srv.Flags().Lookup("addr").Value.String())
	}
	// A value the flag cannot take names the setting.
	bad := &fileConfig{path: "factorymeter.yml", Sync: syncConfig{}}
	bad.Serve.Addr = ""
	n := -1
	bad.Sync.Workers = &n
	sc := syncCmd()
	sc.ParseFlags(nil)
	if err := bad.apply(sc); err != nil {
		t.Errorf("a number the flag takes: %v", err)
	}
}

// Through the root command, in a directory with a config file.
func TestCommandsReadTheConfigFileOfTheDirectory(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	dir := t.TempDir()
	t.Chdir(dir)
	store := filepath.Join(dir, "st")
	os.WriteFile("factorymeter.yml", []byte("target: acme/app\nstore: "+store+"\nlinear:\n  teams: [ENG]\n"), 0o644)

	out, err := execute(newRoot(), "config", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Path   string `json:"path"`
		Target string `json:"target"`
		Linear struct {
			On    bool     `json:"on"`
			Teams []string `json:"teams"`
		} `json:"linear"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Path != "factorymeter.yml" || got.Target != "acme/app" || !got.Linear.On {
		t.Errorf("config json = %s (%v)", out, err)
	}
	out, err = execute(newRoot(), "config")
	if err != nil || !strings.Contains(out, "target                 acme/app") || !strings.Contains(out, "linear                 on, ENG") {
		t.Errorf("config = %q, %v", out, err)
	}

	// The target comes from the file, and an argument wins.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	out, err = execute(newRoot(), "store", "path")
	if err != nil || filepath.Base(strings.TrimSpace(out)) != "acme" {
		t.Errorf("store path = %q, %v", out, err)
	}
	out, err = execute(newRoot(), "store", "path", "other")
	if err != nil || filepath.Base(strings.TrimSpace(out)) != "other" {
		t.Errorf("store path other = %q, %v", out, err)
	}
	// The store of the file is used by a command that has --store.
	out, err = execute(newRoot(), "store", "info", "--format", "json")
	if err == nil || !strings.Contains(err.Error(), store) {
		t.Errorf("store info does not read the store of the file: %v %s", err, out)
	}
	// --no-config reads none.
	if _, err := execute(newRoot(), "store", "path", "--no-config"); err == nil || !strings.Contains(err.Error(), "no target. Give one") {
		t.Errorf("--no-config: %v", err)
	}
	// A command that takes nothing from the file does not read it, so a
	// broken file does not stop it.
	os.WriteFile("factorymeter.yml", []byte("token: x\n"), 0o644)
	if _, err := execute(newRoot(), "metrics"); err != nil {
		t.Errorf("metrics with a broken file: %v", err)
	}
	if _, err := execute(newRoot(), "store", "path"); err == nil || !strings.Contains(err.Error(), `factorymeter.yml: line 1: "token" holds a secret`) {
		t.Errorf("store path with a broken file: %v", err)
	}
	// config init will not write over it, and --force writes over that file.
	if _, err := execute(newRoot(), "config", "init", "--here"); err == nil || !strings.Contains(err.Error(), "factorymeter.yml is already there") {
		t.Errorf("init over a file: %v", err)
	}
	if out, err := execute(newRoot(), "config", "init", "acme", "--here", "--force"); err != nil || out != "Wrote factorymeter.yml. Every setting in it is optional.\n" {
		t.Errorf("init --force: %q %v", out, err)
	}
	// What init writes reads back, with the target it was given.
	c, err := readConfig("factorymeter.yml")
	if err != nil || c.Target != "acme" || c.Linear.On {
		t.Errorf("the written file: %+v %v", c, err)
	}
}

func TestConfigInitInAnEmptyDirectory(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	t.Chdir(t.TempDir())
	if _, err := execute(newRoot(), "config", "init", "--here"); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig("factorymeter.yml")
	if err != nil || c.Target != "my-org" {
		t.Errorf("%+v %v", c, err)
	}
	out, err := execute(newRoot(), "config")
	if err != nil || !strings.Contains(out, "Config file: ") || !strings.Contains(out, "target                 my-org") {
		t.Errorf("config = %q %v", out, err)
	}
}

// Linear and Jira that only the file turns on are skipped with a note when
// their keys are not set, so one file serves a run with keys and one without.
// A flag that turns them on still needs the key.
func TestSyncSkipsWhatTheConfigTurnsOnWithoutKeys(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	now := time.Now().UTC()
	var hubCalls, linCalls atomic.Int64
	hub, lin := fakeGitHub(t, now, &hubCalls), fakeLinear(t, now, &linCalls)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("GITHUB_API_URL", hub.URL)
	t.Setenv("GITHUB_GRAPHQL_URL", hub.URL+"/graphql")
	t.Setenv("LINEAR_API_URL", lin.URL)
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile("factorymeter.yml", []byte("target: acme/app\nhistory: 2w\nstore: st\nsync:\n  skip_ci: true\n  skip_deploys: true\n  skip_tickets: true\nlinear: true\njira:\n  site: acme.atlassian.net\n"), 0o644)

	out, err := execute(newRoot(), "sync", "--quiet")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"note: factorymeter.yml turns Linear on, and LINEAR_API_KEY is empty, so Linear is not read.",
		"note: factorymeter.yml names a Jira site, and JIRA_EMAIL or JIRA_API_TOKEN is empty, so Jira is not read.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if linCalls.Load() != 0 || hubCalls.Load() == 0 {
		t.Errorf("%d requests to GitHub, %d to Linear", hubCalls.Load(), linCalls.Load())
	}
	// A flag is an order: without the key it is an error.
	if _, err := execute(newRoot(), "sync", "--quiet", "--linear"); err == nil || !strings.Contains(err.Error(), "Set LINEAR_API_KEY") {
		t.Errorf("--linear with no key: %v", err)
	}
	// With the key, the file turns Linear on.
	t.Setenv("LINEAR_API_KEY", fakeLinearKey)
	out, err = execute(newRoot(), "sync", "--quiet")
	if err != nil || !strings.Contains(out, "Linear acme: tickets 2") || linCalls.Load() == 0 {
		t.Errorf("with a key: %v\n%s", err, out)
	}
}
