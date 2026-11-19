package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// These tests run scripts/action.sh, the steps of the GitHub Action, with the
// real binary and a stand-in for the GitHub CLI.

// gh stands in for the GitHub CLI. It answers the three calls the script
// makes from files the test names, and writes every call to a log.
const ghStub = `#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$GH_LOG"
for last; do :; done
case "$1" in
  api)
    case "$*" in
      *actions/artifacts*) jq -r "$last" "$GH_ARTIFACTS" ;;
      *) jq -r "$last" "$GH_REPO" ;;
    esac
    ;;
  run)
    mkdir -p "$last"
    cp -R "$GH_SAVED/$3/." "$last"
    ;;
esac
`

var (
	actionOnce sync.Once
	actionBin  string
	actionErr  error
)

// actionTool builds the binary once, and skips the test where the script
// cannot run.
func actionTool(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the script needs bash")
	}
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on this machine", tool)
		}
	}
	actionOnce.Do(func() {
		dir, err := os.MkdirTemp("", "factorymeter-action")
		if err != nil {
			actionErr = err
			return
		}
		actionBin = filepath.Join(dir, "factorymeter")
		if out, err := exec.Command("go", "build", "-o", actionBin, ".").CombinedOutput(); err != nil {
			actionErr = fmt.Errorf("%w: %s", err, out)
		}
	})
	if actionErr != nil {
		t.Fatalf("building the binary: %v", actionErr)
	}
	return actionBin
}

// runner is one job on a runner: its temp directory, its files and its
// environment.
type runner struct {
	t    *testing.T
	temp string
	env  map[string]string
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	bin := actionTool(t)
	temp := t.TempDir()
	stubs := filepath.Join(temp, "bin")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubs, "gh"), []byte(ghStub), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &runner{t: t, temp: temp, env: map[string]string{
		"PATH":                stubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":                temp,
		"RUNNER_TEMP":         temp,
		"FM_BIN":              bin,
		"GITHUB_REPOSITORY":   "acme/app",
		"GITHUB_OUTPUT":       filepath.Join(temp, "output"),
		"GITHUB_STEP_SUMMARY": filepath.Join(temp, "summary"),
		"GITHUB_EVENT_PATH":   filepath.Join(temp, "event.json"),
		"GH_LOG":              filepath.Join(temp, "gh.log"),
		"GH_ARTIFACTS":        filepath.Join(temp, "artifacts.json"),
		"GH_REPO":             filepath.Join(temp, "repo.json"),
		"GH_SAVED":            filepath.Join(temp, "saved"),
		"FM_TARGET":           "acme/app",
	}}
	return r
}

func (r *runner) write(name, text string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.temp, name), []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *runner) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.temp, name))
	return string(b)
}

// step runs one step with extra variables, and returns what it printed and
// the outputs it set.
func (r *runner) step(verb string, extra ...string) (string, map[string]string) {
	r.t.Helper()
	os.Remove(r.env["GITHUB_OUTPUT"])
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "action.sh"), verb)
	for k, v := range r.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, extra...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("%s: %v\n%s", verb, err, out.String())
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(r.read("output")), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	return out.String(), got
}

// private marks the one repository of a store as private.
func private(t *testing.T, dir string, now time.Time) {
	t.Helper()
	s, err := store.OpenWrite(dir, "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := s.Scope(record.GitHub, "acme/app")
	sc.Private = true
	if err := s.PutScope(sc); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

const artifacts = `{"artifacts": [
  {"created_at": "2026-11-01T00:00:00Z", "expired": false, "workflow_run": {"id": 5, "repository_id": 1, "head_repository_id": 1}},
  {"created_at": "2026-11-08T00:00:00Z", "expired": false, "workflow_run": {"id": 7, "repository_id": 1, "head_repository_id": 1}},
  {"created_at": "2026-11-09T00:00:00Z", "expired": false, "workflow_run": {"id": 8, "repository_id": 1, "head_repository_id": 2}},
  {"created_at": "2026-11-10T00:00:00Z", "expired": true, "workflow_run": {"id": 9, "repository_id": 1, "head_repository_id": 1}},
  {"created_at": "2026-11-11T00:00:00Z", "expired": false, "workflow_run": null}
]}`

func TestActionRestoresTheNewestStoreOfItsOwnRepository(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	r := newRunner(t)
	r.write("artifacts.json", artifacts)
	// Run 7 saved a store. A lock file a killed sync left is not brought back.
	saved := reportStore(t, now)
	if err := os.WriteFile(filepath.Join(saved, "store.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.temp, "saved"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, filepath.Join(r.temp, "saved", "7")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.temp, "factorymeter-store")

	// The newest one is expired, the one before came from a fork, and one has
	// no run. Run 7 is the newest that counts.
	text, out := r.step("restore", "FM_TARGET=https://github.com/Acme/App/")
	if out["restored"] != "true" || out["name"] != "factorymeter-store-acme" {
		t.Fatalf("outputs %v\n%s", out, text)
	}
	if !strings.Contains(text, "Restored the store that run 7 saved.") || !strings.Contains(text, "acme/app") {
		t.Errorf("printed:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.json")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.lock")); err == nil {
		t.Error("the lock file came back with the store")
	}
	log := r.read("gh.log")
	if !strings.Contains(log, "name=factorymeter-store-acme") || !strings.Contains(log, "run download 7 --repo acme/app --name factorymeter-store-acme") {
		t.Errorf("gh was asked:\n%s", log)
	}

	// A store of another owner is left behind.
	text, out = r.step("restore", "FM_TARGET=other/app", "FM_STORE_NAME=line")
	if out["restored"] != "false" || out["name"] != "line-other" || !strings.Contains(text, "holds acme, not other") {
		t.Errorf("another owner: outputs %v\n%s", out, text)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the store of another owner stayed on the runner")
	}

	// So is one this version cannot read.
	r.write(filepath.Join("saved", "7", "store.json"), `{"format": 99}`)
	text, out = r.step("restore")
	if out["restored"] != "false" || !strings.Contains(text, "::warning::factorymeter: this version cannot read the store that run 7 saved") {
		t.Errorf("a newer store: outputs %v\n%s", out, text)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a store that cannot be read stayed on the runner")
	}

	// Nothing saved yet.
	r.write("artifacts.json", `{"artifacts": []}`)
	text, out = r.step("restore")
	if out["restored"] != "false" || !strings.Contains(text, "No store from an earlier run.") || strings.Contains(text, "::warning::") {
		t.Errorf("nothing saved: outputs %v\n%s", out, text)
	}

	// The job cannot list artifacts: a warning, not a failure.
	os.Remove(filepath.Join(r.temp, "artifacts.json"))
	text, out = r.step("restore")
	if out["restored"] != "false" || !strings.Contains(text, "actions: read") {
		t.Errorf("no permission: outputs %v\n%s", out, text)
	}

	// store: false asks GitHub nothing.
	os.Remove(filepath.Join(r.temp, "gh.log"))
	_, out = r.step("restore", "FM_STORE=false")
	if out["restored"] != "false" || r.read("gh.log") != "" {
		t.Errorf("store false: outputs %v, gh was asked %q", out, r.read("gh.log"))
	}
}

func TestActionKeepsAPrivateStoreOutOfAPublicRepository(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	r := newRunner(t)
	dir := filepath.Join(r.temp, "factorymeter-store")

	// No store, such as after a build that failed: nothing to save.
	if _, out := r.step("share"); out["names"] != "true" || out["save"] != "false" {
		t.Errorf("no store: %v", out)
	}

	if err := os.Rename(reportStore(t, now), dir); err != nil {
		t.Fatal(err)
	}
	const public, hidden = `{"repository": {"private": false}}`, `{"repository": {"private": true}}`

	// A store of public repositories can be saved anywhere.
	r.write("event.json", public)
	if text, out := r.step("share"); out["names"] != "true" || out["save"] != "true" || strings.Contains(text, "::warning::") {
		t.Errorf("public store, public repository: %v\n%s", out, text)
	}
	if _, out := r.step("share", "FM_STORE=false"); out["names"] != "true" || out["save"] != "false" {
		t.Errorf("store false: %v", out)
	}

	private(t, dir, now)
	text, out := r.step("share")
	if out["names"] != "false" || out["save"] != "false" || !strings.Contains(text, "::warning::factorymeter: acme/app is public and the store holds a private repository or tickets of Linear or Jira") {
		t.Errorf("private store, public repository: %v\n%s", out, text)
	}
	r.write("event.json", hidden)
	if text, out := r.step("share"); out["names"] != "true" || out["save"] != "true" || strings.Contains(text, "::warning::") {
		t.Errorf("private store, private repository: %v\n%s", out, text)
	}

	// An event that does not say, such as a schedule: the API is asked.
	r.write("event.json", `{"schedule": "27 12 * * 3"}`)
	r.write("repo.json", `{"private": true}`)
	if _, out := r.step("share"); out["names"] != "true" || out["save"] != "true" {
		t.Errorf("the API says private: %v", out)
	}
	r.write("repo.json", `{"private": false}`)
	if _, out := r.step("share"); out["names"] != "false" || out["save"] != "false" {
		t.Errorf("the API says public: %v", out)
	}
	// Nobody says: the repository counts as public.
	os.Remove(filepath.Join(r.temp, "repo.json"))
	if _, out := r.step("share"); out["names"] != "false" || out["save"] != "false" {
		t.Errorf("nobody says: %v", out)
	}
	// A store that cannot be read counts as private.
	r.write("event.json", public)
	r.write(filepath.Join("factorymeter-store", "store.json"), "{")
	if _, out := r.step("share"); out["names"] != "false" || out["save"] != "false" {
		t.Errorf("a damaged store: %v", out)
	}
}

func TestActionReport(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	r := newRunner(t)
	if err := os.Rename(reportStore(t, now), filepath.Join(r.temp, "factorymeter-store")); err != nil {
		t.Fatal(err)
	}
	files := []string{"factorymeter.md", "factorymeter.html", "factorymeter.json"}
	all := func() string {
		var b strings.Builder
		for _, f := range files {
			text := r.read(filepath.Join("factorymeter-report", f))
			if text == "" {
				t.Errorf("%s is empty", f)
			}
			b.WriteString(text)
		}
		return b.String()
	}

	_, out := r.step("report", "FM_NAMES=true", "FM_SINCE=2w")
	want := map[string]string{
		"headline":         "Review is the constraint. It holds 67 percent of the hours changes spend on the line.",
		"constraint":       "review",
		"constraint-share": "0.6667",
		"agent-share":      "0",
		"markdown":         filepath.Join(r.temp, "factorymeter-report", "factorymeter.md"),
		"html":             filepath.Join(r.temp, "factorymeter-report", "factorymeter.html"),
		"json":             filepath.Join(r.temp, "factorymeter-report", "factorymeter.json"),
		"store":            filepath.Join(r.temp, "factorymeter-store"),
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("output %s = %q, want %q", k, out[k], v)
		}
	}
	if out["recommendation"] == "" {
		t.Errorf("no recommendation in %v", out)
	}
	if text := all(); !strings.Contains(text, "acme/app") || !strings.Contains(text, "Secret title 7") {
		t.Error("the report with names lacks them")
	}
	if md := r.read(filepath.Join("factorymeter-report", "factorymeter.md")); md == "" || r.read("summary") != md {
		t.Error("the job summary is not the Markdown report")
	}

	// Without a clear yes the report carries no names, in any of its files or
	// in the job summary. summary: false leaves the job summary alone.
	os.Remove(filepath.Join(r.temp, "summary"))
	for _, names := range []string{"FM_NAMES=false", "FM_NAMES=", "FM_NAMES=yes"} {
		_, out = r.step("report", names, "FM_SINCE=2w", "FM_SUMMARY=false")
		if out["constraint"] != "review" {
			t.Errorf("%s: outputs %v", names, out)
		}
		for _, secret := range []string{"acme/app", "Secret title", "example.invalid"} {
			if strings.Contains(all(), secret) {
				t.Errorf("%s: the report holds %q", names, secret)
			}
		}
	}
	if r.read("summary") != "" {
		t.Error("summary false wrote the job summary")
	}
}
