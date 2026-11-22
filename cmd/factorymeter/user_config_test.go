package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain points the user config file at a file that is not there, so a
// config file in the home of whoever runs the tests is never read.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "factorymeter-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("FACTORYMETER_CONFIG", filepath.Join(dir, "no-config.yml"))
	// The wizard never lists repositories on the real GitHub in a test.
	activeRepos = func(context.Context, string, time.Time) ([]string, error) {
		return nil, errors.New("no GitHub in tests")
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestUserConfigFile(t *testing.T) {
	t.Setenv("FACTORYMETER_CONFIG", "/etc/fm.yml")
	if p, err := userConfigFile(); p != "/etc/fm.yml" || err != nil {
		t.Errorf("FACTORYMETER_CONFIG: %q %v", p, err)
	}
	t.Setenv("FACTORYMETER_CONFIG", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if p, err := userConfigFile(); p != filepath.Join(xdg, "factorymeter", "config.yml") || err != nil {
		t.Errorf("XDG_CONFIG_HOME: %q %v", p, err)
	}
	// A relative XDG_CONFIG_HOME is not followed, as the spec says.
	t.Setenv("XDG_CONFIG_HOME", "rel")
	p, err := userConfigFile()
	if err != nil || strings.HasPrefix(p, "rel") {
		t.Errorf("relative XDG_CONFIG_HOME: %q %v", p, err)
	}
	if runtime.GOOS != "windows" {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if p, err := userConfigFile(); p != filepath.Join(home, ".config", "factorymeter", "config.yml") || err != nil {
			t.Errorf("home: %q %v", p, err)
		}
	}
}

// The order: the named file, else the file of the directory, else the user
// config file.
func TestFindConfigFallsBackToTheUserFile(t *testing.T) {
	user := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv("FACTORYMETER_CONFIG", user)
	dir := t.TempDir()
	if p, err := findConfig("", dir); p != "" || err != nil {
		t.Errorf("none: %q %v", p, err)
	}
	os.WriteFile(user, []byte("target: acme\n"), 0o644)
	if p, err := findConfig("", dir); p != user || err != nil {
		t.Errorf("user: %q %v", p, err)
	}
	local := filepath.Join(dir, "factorymeter.yml")
	os.WriteFile(local, nil, 0o644)
	if p, err := findConfig("", dir); p != local || err != nil {
		t.Errorf("the file here wins: %q %v", p, err)
	}
	if p, err := findConfig("other.yml", dir); p != "other.yml" || err != nil {
		t.Errorf("named wins: %q %v", p, err)
	}
}

// config init writes the user config file by default, and a command run in a
// directory with no file of its own reads it.
func TestConfigInitWritesTheUserFile(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	user := filepath.Join(t.TempDir(), "nested", "factorymeter", "config.yml")
	t.Setenv("FACTORYMETER_CONFIG", user)
	dir := t.TempDir()
	t.Chdir(dir)
	out, err := execute(newRoot(), "config", "init", "acme")
	if err != nil || out != "Wrote "+user+". Every setting in it is optional.\n" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "factorymeter.yml")); err == nil {
		t.Error("config init wrote a file in this directory")
	}
	raw, _ := os.ReadFile(user)
	if !strings.HasPrefix(string(raw), userHeader) {
		t.Errorf("the user file does not say where it is read:\n%s", raw)
	}
	out, err = execute(newRoot(), "config")
	if err != nil || !strings.Contains(out, "Config file: "+user) || !strings.Contains(out, "target                 acme") {
		t.Errorf("config: %v\n%s", err, out)
	}
	// It does not write over the file without --force.
	if _, err := execute(newRoot(), "config", "init"); err == nil || !strings.Contains(err.Error(), user+" is already there") {
		t.Errorf("again: %v", err)
	}
	// A file here wins, and config init says so when it writes the user file.
	os.WriteFile("factorymeter.yml", []byte("target: other\n"), 0o644)
	out, err = execute(newRoot(), "config", "init", "--force")
	if err != nil || !strings.Contains(out, "note: factorymeter.yml in this directory wins over it here.") {
		t.Errorf("--force with a file here: %q %v", out, err)
	}
	out, err = execute(newRoot(), "config")
	if err != nil || !strings.Contains(out, "Config file: factorymeter.yml") {
		t.Errorf("the file here does not win: %v\n%s", err, out)
	}
}

func TestConfigWithNoFileNamesTheUserFile(t *testing.T) {
	t.Cleanup(func() { config = configState{} })
	user := filepath.Join(t.TempDir(), "config.yml")
	t.Setenv("FACTORYMETER_CONFIG", user)
	t.Chdir(t.TempDir())
	out, err := execute(newRoot(), "config")
	if err != nil || !strings.Contains(out, "none at "+user) {
		t.Errorf("%v\n%s", err, out)
	}
}
