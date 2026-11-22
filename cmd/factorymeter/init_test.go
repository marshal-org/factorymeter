package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitWizardWritesTheConfig(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	now := time.Now().UTC()
	var linCalls, jiraCalls atomic.Int64
	lin, jsrv := fakeLinear(t, now, &linCalls), fakeJira(t, now, &jiraCalls)
	t.Setenv("LINEAR_API_URL", lin.URL)
	t.Setenv("LINEAR_API_KEY", fakeLinearKey)
	t.Setenv("JIRA_EMAIL", fakeJiraEmail)
	t.Setenv("JIRA_API_TOKEN", fakeJiraToken)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	user := filepath.Join(t.TempDir(), "conf", "factorymeter", "config.yml")
	t.Setenv("FACTORYMETER_CONFIG", user)
	dir := t.TempDir()
	t.Chdir(dir)

	// A bad target is asked again, a team that is not there too.
	answers := strings.Join([]string{"not a target", "acme/app", "12w", "y", "eng xyz", "eng", "y", jsrv.URL, "ops", "n"}, "\n") + "\n"
	root := newRoot()
	root.SetIn(strings.NewReader(answers))
	out, err := execute(root, "init")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"A GitHub token is set.", "ENG      Engineering", "OPS      Operations", "there is no XYZ here", "Wrote " + user} {
		if !strings.Contains(out, want) {
			t.Errorf("the wizard did not say %q:\n%s", want, out)
		}
	}
	// By default the answers go to the user config file, not this directory.
	if _, err := os.Stat(filepath.Join(dir, "factorymeter.yml")); err == nil {
		t.Error("init wrote a file in this directory")
	}
	c, err := readConfig(user)
	if err != nil {
		t.Fatal(err)
	}
	if c.Target != "acme/app" || c.History != "12w" || c.Window != "13w" || !c.Linear.On || !reflect.DeepEqual(c.Linear.Teams, []string{"ENG"}) ||
		c.Jira.Site != strings.TrimPrefix(jsrv.URL, "https://") || !reflect.DeepEqual(c.Jira.Projects, []string{"OPS"}) {
		t.Errorf("config = %+v", c)
	}
	raw, _ := os.ReadFile(user)
	if strings.Contains(string(raw), fakeLinearKey) || strings.Contains(string(raw), fakeJiraToken) || strings.Contains(string(raw), fakeJiraEmail) {
		t.Error("the file holds a key")
	}

	// A second run asks before it writes over the file, and keeps it on no.
	root = newRoot()
	root.SetIn(strings.NewReader("n\n"))
	out, err = execute(root, "init")
	if err != nil || !strings.Contains(out, "There is a config file already: "+user+".") || !strings.Contains(out, "Nothing was written.") {
		t.Errorf("second run: %v\n%s", err, out)
	}
}

// With --here the wizard writes factorymeter.yml in this directory, and keeps
// one file there.
func TestInitWizardHere(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	user := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv("FACTORYMETER_CONFIG", user)
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile(".factorymeter.yml", []byte("target: old/one\n"), 0o644)

	root := newRoot()
	root.SetIn(strings.NewReader("y\nacme/app\n\nn\nn\nn\n"))
	out, err := execute(root, "init", "--here")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "There is a config file already: .factorymeter.yml.") || !strings.Contains(out, "Wrote factorymeter.yml") {
		t.Errorf("the wizard said:\n%s", out)
	}
	if _, err := os.Stat(".factorymeter.yml"); err == nil {
		t.Error("the old file is still here, so the directory has two")
	}
	if c, err := readConfig("factorymeter.yml"); err != nil || c.Target != "acme/app" {
		t.Errorf("%+v %v", c, err)
	}
	if _, err := os.Stat(user); err == nil {
		t.Error("init --here wrote the user config file")
	}
}

// A file in this directory wins over the user config file, so init says so.
func TestInitWizardNamesAFileThatWins(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	t.Setenv("FACTORYMETER_CONFIG", filepath.Join(t.TempDir(), "config.yml"))
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Chdir(t.TempDir())
	os.WriteFile("factorymeter.yml", []byte("target: old/one\n"), 0o644)
	root := newRoot()
	root.SetIn(strings.NewReader("acme/app\n\nn\nn\nn\n"))
	out, err := execute(root, "init")
	if err != nil || !strings.Contains(out, "Note: factorymeter.yml in this directory wins over it here.") {
		t.Errorf("%v\n%s", err, out)
	}
}

// With no one at the keyboard every question takes its default, and a
// question with no default ends the wizard.
func TestInitWizardWithNoInput(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	dir := t.TempDir()
	t.Chdir(dir)
	root := newRoot()
	root.SetIn(strings.NewReader(""))
	if _, err := execute(root, "init"); err == nil || !strings.Contains(err.Error(), "no target") {
		t.Errorf("no input and no git remote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "factorymeter.yml")); err == nil {
		t.Error("a file was written")
	}
}

func TestGitRemotes(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:marshal-org/factorymeter.git":      "marshal-org/factorymeter",
		"https://github.com/marshal-org/factorymeter":      "marshal-org/factorymeter",
		"https://github.com/marshal-org/factorymeter.git/": "marshal-org/factorymeter",
		"ssh://git@github.com/acme/web.app.git":            "acme/web.app",
		"https://gitlab.com/acme/web":                      "",
	} {
		got := ""
		if m := remoteRe.FindStringSubmatch(in); m != nil {
			got = m[1] + "/" + m[2]
		}
		if got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
}

// Ctrl-C while the wizard waits for an answer stops it: it does not wait for
// a line that never comes, and it writes nothing.
func TestInitWizardStopsOnCtrlC(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	dir := t.TempDir()
	t.Chdir(dir)
	keyboard, typing := io.Pipe() // a terminal no one types on
	t.Cleanup(func() { typing.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	root := newRoot()
	root.SetIn(keyboard)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"init"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if !errors.As(err, new(toldErr)) || !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want a told stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wizard did not stop on Ctrl-C")
	}
	if !strings.Contains(out.String(), "Stopped. Nothing was written.") {
		t.Errorf("the wizard did not say it stopped:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "factorymeter.yml")); err == nil {
		t.Error("a file was written")
	}
}
