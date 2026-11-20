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

// The install script of the website, run by each shell on this machine
// against a stand-in for the releases of github.com.
func TestInstallScript(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds a binary and needs a POSIX shell")
	}
	asset, err := upgrade.Asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	script, _ := filepath.Abs(filepath.Join("..", "..", "site", "install.sh"))
	bin, err := os.ReadFile(buildAs(t, t.TempDir(), "v9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	good := hex.EncodeToString(sum[:])
	sums := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
		case "/releases/download/v9.9.9/checksums.txt", "/releases/download/v9.9.8/checksums.txt":
			w.Write([]byte(sums + "  " + asset + "\n"))
		case "/releases/download/v9.9.9/" + asset, "/releases/download/v9.9.8/" + asset:
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var shells [][]string
	for _, sh := range [][]string{{"sh"}, {"dash"}, {"bash"}, {"busybox", "sh"}} {
		if _, err := exec.LookPath(sh[0]); err == nil {
			shells = append(shells, sh)
		}
	}
	for _, sh := range shells {
		t.Run(strings.Join(sh, " "), func(t *testing.T) {
			home := t.TempDir()
			run := func(env ...string) (string, error) {
				cmd := exec.Command(sh[0], append(sh[1:], script)...)
				cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "FACTORYMETER_RELEASES_URL=" + srv.URL}, env...)
				b, err := cmd.CombinedOutput()
				return string(b), err
			}
			sums = good
			out, err := run()
			installed := filepath.Join(home, ".local", "bin", "factorymeter")
			if err != nil || !strings.Contains(out, "Installed factorymeter v9.9.9 to "+installed) || !strings.Contains(out, "is not on your PATH") {
				t.Fatalf("%v\n%s", err, out)
			}
			if v, err := exec.Command(installed, "--version").Output(); err != nil || !strings.Contains(string(v), "v9.9.9") {
				t.Errorf("the installed binary says %q %v", v, err)
			}
			// A directory picked by hand, and a release picked by hand. The
			// stand-in serves the v9.9.9 binary as v9.9.8 too, which the
			// script finds out by running it.
			dir := filepath.Join(home, "tools")
			out, err = run("FACTORYMETER_VERSION=9.9.9", "FACTORYMETER_INSTALL_DIR="+dir)
			if err != nil || !strings.Contains(out, "Installed factorymeter v9.9.9 to "+filepath.Join(dir, "factorymeter")) {
				t.Errorf("a version and a directory by hand: %v\n%s", err, out)
			}
			out, err = run("FACTORYMETER_VERSION=v9.9.8", "FACTORYMETER_INSTALL_DIR="+filepath.Join(home, "x"))
			if err == nil || !strings.Contains(out, "or is not factorymeter v9.9.8. Nothing was installed") {
				t.Errorf("a binary of another version: %v\n%s", err, out)
			}
			// A download that does not match installs nothing.
			sums = strings.Repeat("ab", 32)
			other := filepath.Join(home, "other")
			out, err = run("FACTORYMETER_INSTALL_DIR=" + other)
			if err == nil || !strings.Contains(out, "does not match the checksum of release v9.9.9. Nothing was installed") {
				t.Errorf("a bad sum: %v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(other, "factorymeter")); err == nil {
				t.Error("a binary that does not match was installed")
			}
		})
	}
}
