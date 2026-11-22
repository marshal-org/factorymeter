package main

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParsePicks(t *testing.T) {
	items := []string{"acme/app", "acme/api", "acme/web", "acme/lib"}
	for _, c := range []struct {
		in   string
		want []int
		bad  bool
	}{
		{in: "", want: nil},
		{in: "all", want: nil},
		{in: "1 3", want: []int{0, 2}},
		{in: "2-3,api", want: []int{1, 2}},
		{in: "acme/lib WEB", want: []int{3, 2}},
		{in: "1-4", want: nil},
		{in: "5", bad: true},
		{in: "3-1", bad: true},
		{in: "nope", bad: true},
	} {
		got, err := parsePicks(c.in, items)
		if (err != nil) != c.bad || (!c.bad && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("parsePicks(%q) = %v, %v; want %v, bad %v", c.in, got, err, c.want, c.bad)
		}
	}
}

func TestPickKeys(t *testing.T) {
	p := newPick([]string{"a", "b", "c", "d"}, 2)
	if got := p.picked(); len(got) != 4 {
		t.Fatalf("all ticked at the start, got %v", got)
	}
	p.press(keyAll) // all off
	if p.press(keyEnter) || p.warn == "" {
		t.Fatal("Enter with nothing ticked must refuse and say why")
	}
	p.press(keyDown)
	p.press(keyToggle)
	p.press(keyDown)
	p.press(keyDown)
	p.press(keyToggle)
	if p.top != 2 || p.cur != 3 {
		t.Fatalf("the list scrolls with the cursor: top %d cur %d", p.top, p.cur)
	}
	if !p.press(keyEnter) || !reflect.DeepEqual(p.picked(), []int{1, 3}) {
		t.Fatalf("picked %v", p.picked())
	}
	for _, c := range []struct {
		in   string
		k    pickKey
		took int
	}{{"\x1b[A", keyUp, 3}, {"\x1bOB", keyDown, 3}, {"\x1b[", keyNone, 0}, {"\r", keyEnter, 1}, {"\x03", keyStop, 1}, {" ", keyToggle, 1}} {
		if k, n := keyOf([]byte(c.in)); k != c.k || n != c.took {
			t.Errorf("keyOf(%q) = %v, %d", c.in, k, n)
		}
	}
}

func TestReposOf(t *testing.T) {
	got, err := reposOf("acme", []string{"app", "acme/api", "ACME/app"})
	if err != nil || !reflect.DeepEqual(got, []string{"acme/app", "acme/api"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := reposOf("acme", []string{"other/app"}); err == nil {
		t.Fatal("a repository of another owner must be refused")
	}
	if _, err := reposOf("acme/app", []string{"api"}); err == nil {
		t.Fatal("--repo with a one-repository target must be refused")
	}
}

func TestBrowserURL(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:7377": "http://127.0.0.1:7377/",
		"[::]:7377":      "http://127.0.0.1:7377/",
		"0.0.0.0:80":     "http://127.0.0.1:80/",
		"localhost:9":    "http://localhost:9/",
	} {
		if got := browserURL(in); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// For an owner the wizard lists its active repositories and writes the ones
// picked as repos.
func TestInitWizardPicksRepos(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	was := activeRepos
	t.Cleanup(func() { activeRepos = was })
	activeRepos = func(context.Context, string, time.Time) ([]string, error) {
		return []string{"acme/app", "acme/api", "acme/web"}, nil
	}
	t.Setenv("GITHUB_TOKEN", "a-fake-github-token")
	t.Setenv("LINEAR_API_KEY", "")
	user := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv("FACTORYMETER_CONFIG", user)
	t.Chdir(t.TempDir())
	root := newRoot()
	root.SetIn(strings.NewReader(strings.Join([]string{"acme", "13w", "9", "1 web", "n", "n", "n"}, "\n") + "\n"))
	out, err := execute(root, "init")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"3. Which repositories of acme?", " 2  acme/api", "9 is not on the list", "Picked: app, web.", "4. Do your tickets live in Linear?", "6. Run the first sync now?"} {
		if !strings.Contains(out, want) {
			t.Errorf("the wizard did not say %q:\n%s", want, out)
		}
	}
	c, err := readConfig(user)
	if err != nil {
		t.Fatal(err)
	}
	if c.Target != "acme" || !reflect.DeepEqual(c.Repos, []string{"app", "web"}) {
		t.Errorf("config = %+v", c)
	}
}
