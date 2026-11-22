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
	t.Chdir(t.TempDir())

	// A bad target is asked again, a team that is not there too.
	answers := strings.Join([]string{"not a target", "acme/app", "12w", "y", "eng xyz", "eng", "y", jsrv.URL, "ops", "n"}, "\n") + "\n"
	root := newRoot()
	root.SetIn(strings.NewReader(answers))
	out, err := execute(root, "init")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"A GitHub token is set.", "ENG      Engineering", "OPS      Operations", "there is no XYZ here", "Wrote factorymeter.yml"} {
		if !strings.Contains(out, want) {
			t.Errorf("the wizard did not say %q:\n%s", want, out)
		}
	}
	c, err := readConfig("factorymeter.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Target != "acme/app" || c.History != "12w" || c.Window != "13w" || !c.Linear.On || !reflect.DeepEqual(c.Linear.Teams, []string{"ENG"}) ||
		c.Jira.Site != strings.TrimPrefix(jsrv.URL, "https://") || !reflect.DeepEqual(c.Jira.Projects, []string{"OPS"}) {
		t.Errorf("config = %+v", c)
	}
	raw, _ := os.ReadFile("factorymeter.yml")
	if strings.Contains(string(raw), fakeLinearKey) || strings.Contains(string(raw), fakeJiraToken) || strings.Contains(string(raw), fakeJiraEmail) {
		t.Error("the file holds a key")
	}

	// A second run asks before it writes over the file, and keeps it on no.
	root = newRoot()
	root.SetIn(strings.NewReader("n\n"))
	out, err = execute(root, "init")
	if err != nil || !strings.Contains(out, "There is a config file here already: factorymeter.yml.") || !strings.Contains(out, "Nothing was written.") {
		t.Errorf("second run: %v\n%s", err, out)
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
