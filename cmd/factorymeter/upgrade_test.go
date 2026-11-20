package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/upgrade"
)

// buildAs builds the binary with a version, as a release does.
func buildAs(t *testing.T, dir, ver string) string {
	t.Helper()
	out := filepath.Join(dir, "factorymeter")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	if b, err := exec.Command("go", "build", "-ldflags", "-X main.version="+ver, "-o", out, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", ver, err, b)
	}
	return out
}

// An old release binary replaces itself with the newest one, from a stand-in
// for the releases of github.com.
func TestUpgradeReplacesTheBinaryInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two binaries")
	}
	asset, err := upgrade.Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	newBin, err := os.ReadFile(buildAs(t, t.TempDir(), "v9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(newBin)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
		case "/releases/download/v9.9.9/checksums.txt":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"))
		case "/releases/download/v9.9.9/" + asset:
			w.Write(newBin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := buildAs(t, t.TempDir(), "v0.0.1")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(old, args...)
		cmd.Env = append(os.Environ(), "FACTORYMETER_RELEASES_URL="+srv.URL)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, b)
		}
		return string(b)
	}
	if out := run("upgrade", "--check"); !strings.Contains(out, "factorymeter v9.9.9 is out. This is v0.0.1.") {
		t.Errorf("--check: %s", out)
	}
	out := run("upgrade")
	if !strings.Contains(out, "It matches the checksum of the release.") || !strings.Contains(out, ": v9.9.9, was v0.0.1.") {
		t.Errorf("upgrade: %s", out)
	}
	if v := run("--version"); !strings.Contains(v, "v9.9.9") {
		t.Errorf("after the upgrade the binary says %q", v)
	}
	if out := run("upgrade"); !strings.Contains(out, "factorymeter v9.9.9 is the newest release. Nothing to do.") {
		t.Errorf("a second upgrade: %s", out)
	}
	// Only the releases page and the two files were asked for.
	for _, p := range asked {
		if p != "/releases/latest" && !strings.HasPrefix(p, "/releases/download/v9.9.9/") {
			t.Errorf("asked for %s", p)
		}
	}
}

func TestUpgradeRefusesABinaryThatDoesNotMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	asset, err := upgrade.Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
		case "/releases/download/v9.9.9/checksums.txt":
			w.Write([]byte(strings.Repeat("ab", 32) + "  " + asset + "\n"))
		case "/releases/download/v9.9.9/" + asset:
			w.Write([]byte("#!/bin/sh\necho evil\n"))
		}
	}))
	defer srv.Close()
	old := buildAs(t, t.TempDir(), "v0.0.1")
	before, _ := os.ReadFile(old)
	cmd := exec.Command(old, "upgrade")
	cmd.Env = append(os.Environ(), "FACTORYMETER_RELEASES_URL="+srv.URL)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "does not match the checksum of release v9.9.9. Nothing was changed") {
		t.Errorf("err %v: %s", err, out)
	}
	if after, _ := os.ReadFile(old); string(after) != string(before) {
		t.Error("the binary changed")
	}
	// A build from source asks for --force.
	dev := buildAs(t, t.TempDir(), "dev")
	cmd = exec.Command(dev, "upgrade")
	cmd.Env = append(os.Environ(), "FACTORYMETER_RELEASES_URL="+srv.URL)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "built from source, not a release") {
		t.Errorf("dev: %v %s", err, out)
	}
}
