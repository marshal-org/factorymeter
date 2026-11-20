package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.5.0", "v0.4.0", true}, {"v0.4.0", "v0.5.0", false}, {"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true}, {"v0.4.0", "v0.4.0", false}, {"v0.4.0", "dev", true},
		{"dev", "v0.4.0", false}, {"v0.4.0-rc1", "v0.3.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestAsset(t *testing.T) {
	for in, want := range map[string]string{"linux/amd64": "factorymeter-linux-amd64", "darwin/arm64": "factorymeter-darwin-arm64", "windows/amd64": "factorymeter-windows-amd64.exe"} {
		goos, goarch, _ := strings.Cut(in, "/")
		if got, err := Asset(goos, goarch); got != want || err != nil {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	if _, err := Asset("freebsd", "amd64"); err == nil || !strings.Contains(err.Error(), "go install") {
		t.Errorf("freebsd: %v", err)
	}
}

// fakeReleases serves releases the way github.com does: /releases/latest
// redirects to the tag, and the files of a release are under download.
func fakeReleases(t *testing.T, latest string, files map[string][]byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/releases/download/"):
			if b, ok := files[strings.TrimPrefix(r.URL.Path, "/releases/download/")]; ok {
				w.Write(b)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestLatestAndFetch(t *testing.T) {
	bin := []byte("new binary")
	srv := fakeReleases(t, "v0.5.0", map[string][]byte{
		"v0.5.0/checksums.txt":            []byte(sum(bin) + "  factorymeter-linux-amd64\n" + strings.Repeat("0", 64) + "  factorymeter-linux-arm64\n"),
		"v0.5.0/factorymeter-linux-amd64": bin,
		"v0.5.0/factorymeter-linux-arm64": []byte("tampered"),
	})
	s := New(srv.URL)
	ctx := context.Background()
	if tag, err := s.Latest(ctx); tag != "v0.5.0" || err != nil {
		t.Fatalf("latest = %q %v", tag, err)
	}
	if b, err := s.Fetch(ctx, "v0.5.0", "factorymeter-linux-amd64"); err != nil || string(b) != "new binary" {
		t.Errorf("fetch = %q %v", b, err)
	}
	if _, err := s.Fetch(ctx, "v0.5.0", "factorymeter-linux-arm64"); err == nil || !strings.Contains(err.Error(), "does not match the checksum") {
		t.Errorf("a file that does not match: %v", err)
	}
	if _, err := s.Fetch(ctx, "v0.5.0", "factorymeter-darwin-arm64"); err == nil || !strings.Contains(err.Error(), "checksums.txt has no sum for factorymeter-darwin-arm64") {
		t.Errorf("a file with no sum: %v", err)
	}
	if _, err := s.Fetch(ctx, "v0.6.0", "factorymeter-linux-amd64"); err == nil || !strings.Contains(err.Error(), "release v0.6.0 has no file checksums.txt") {
		t.Errorf("a release that is not there: %v", err)
	}
	if _, err := s.Fetch(ctx, "latest", "x"); err == nil {
		t.Error("a tag that is not a version was fetched")
	}
	bad := fakeReleases(t, "nightly", nil)
	if _, err := New(bad.URL).Latest(ctx); err == nil || !strings.Contains(err.Error(), "not a version") {
		t.Errorf("a latest tag that is not a version: %v", err)
	}
}

func TestReplace(t *testing.T) {
	for _, windows := range []bool{false, true} {
		dir := t.TempDir()
		target := filepath.Join(dir, "factorymeter")
		os.WriteFile(target, []byte("old"), 0o750)
		if err := Replace(target, []byte("new"), windows); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(target)
		st, _ := os.Stat(target)
		if string(b) != "new" || st.Mode().Perm() != 0o750|0o111 {
			t.Errorf("windows %v: %q %v", windows, b, st.Mode())
		}
		entries, _ := os.ReadDir(dir)
		want := 1
		if windows {
			want = 2 // the old binary waits as factorymeter.old
		}
		if len(entries) != want {
			t.Errorf("windows %v: %d files left", windows, len(entries))
		}
		CleanOld(target)
	}
	// A directory that cannot be written to leaves the old binary.
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "factorymeter")
	os.WriteFile(target, []byte("old"), 0o755)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	if err := Replace(target, []byte("new"), false); err == nil || !strings.Contains(err.Error(), "permission denied. Run the upgrade as the user") {
		t.Errorf("read-only directory: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Errorf("the old binary changed: %q", b)
	}
}
