// Package upgrade replaces a factorymeter binary with one from a release. It
// finds the newest release by the redirect of the releases page, so it needs
// no token and no API call, downloads the file for this system with the
// checksums of the release, checks the file against them, and puts it where
// the running binary is.
package upgrade

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBase is the repository the releases come from.
const DefaultBase = "https://github.com/marshal-org/factorymeter"

// maxSize is the largest binary it downloads. A release binary is about 18 MB.
const maxSize = 200 << 20

// Source is where releases are read from.
type Source struct {
	// Base is the address of the repository, such as DefaultBase. Its
	// /releases/latest redirects to the newest release, and the files of a
	// release are at /releases/download/<tag>/<name>.
	Base string
	HTTP *http.Client
	// UserAgent is sent with every request.
	UserAgent string
}

// New returns a source for a repository address.
func New(base string) *Source {
	return &Source{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 5 * time.Minute}, UserAgent: "factorymeter"}
}

var tagRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// Valid says whether a version is a release tag, such as v0.5.0.
func Valid(v string) bool { return tagRe.MatchString(v) }

// Newer says whether release a is newer than release b. A version that is not
// a release, such as dev, is older than every release.
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	if pa == nil {
		return false
	}
	if pb == nil {
		return true
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) []int {
	m := tagRe.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	out := make([]int, 3)
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out
}

// Asset is the name of the release file for a system.
func Asset(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64":
		return "factorymeter-" + goos + "-" + goarch, nil
	case "windows/amd64":
		return "factorymeter-windows-amd64.exe", nil
	}
	return "", fmt.Errorf("there is no release file for %s/%s. Build it from source: go install github.com/marshal-org/factorymeter/cmd/factorymeter@latest", goos, goarch)
}

func (s *Source) get(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.UserAgent)
	return c.Do(req)
}

// Latest is the tag of the newest release.
func (s *Source) Latest(ctx context.Context) (string, error) {
	c := *s.HTTP
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := s.get(ctx, &c, s.Base+"/releases/latest")
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", s.Base, err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
		return "", fmt.Errorf("%s/releases/latest did not point at a release (%d)", s.Base, resp.StatusCode)
	}
	tag := path.Base(loc)
	if !Valid(tag) {
		return "", fmt.Errorf("the newest release has a tag that is not a version: %q", tag)
	}
	return tag, nil
}

func (s *Source) download(ctx context.Context, tag, name string, limit int64) ([]byte, error) {
	url := s.Base + "/releases/download/" + tag + "/" + name
	resp, err := s.get(ctx, s.HTTP, url)
	if err != nil {
		return nil, fmt.Errorf("cannot download %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("release %s has no file %s", tag, name)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("downloading %s of %s: status %d", name, tag, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", name, limit>>20)
	}
	return data, nil
}

// Fetch downloads the file of a release and checks it against the checksums
// of the same release.
func (s *Source) Fetch(ctx context.Context, tag, asset string) ([]byte, error) {
	if !Valid(tag) {
		return nil, fmt.Errorf("%q is not a release version, such as v0.5.0", tag)
	}
	sums, err := s.download(ctx, tag, "checksums.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	want, err := sumOf(sums, asset)
	if err != nil {
		return nil, fmt.Errorf("release %s: %w", tag, err)
	}
	data, err := s.download(ctx, tag, asset, maxSize)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("the downloaded %s does not match the checksum of release %s. Nothing was changed", asset, tag)
	}
	return data, nil
}

// sumOf finds the SHA-256 of a file in a checksums.txt.
func sumOf(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			if len(f[0]) != 64 {
				break
			}
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no sum for %s", name)
}

// Replace puts a new binary where the old one is, keeping its mode. The new
// file is written next to the old one and renamed over it, so a failure
// leaves the old binary as it was. Windows does not let a running program be
// overwritten, so there the old one is first moved aside to <name>.old, which
// the next upgrade removes.
func Replace(target string, data []byte, windows bool) error {
	st, err := os.Stat(target)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".factorymeter-upgrade-*")
	if err != nil {
		return writeErr(dir, err)
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), st.Mode().Perm()|0o111); err != nil {
		return err
	}
	if windows {
		old := target + ".old"
		os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			return writeErr(dir, err)
		}
		if err := os.Rename(tmp.Name(), target); err != nil {
			os.Rename(old, target)
			return writeErr(dir, err)
		}
		done = true
		return nil
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return writeErr(dir, err)
	}
	done = true
	return nil
}

// TempCopy writes a binary to a new file, ready to run, so it can be tried
// before it replaces the old one. The caller removes it.
func TempCopy(data []byte, windows bool) (string, error) {
	pattern := "factorymeter-new-*"
	if windows {
		pattern += ".exe"
	}
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// CleanOld removes the binary an earlier upgrade on Windows moved aside.
func CleanOld(target string) {
	os.Remove(target + ".old")
}

func writeErr(dir string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot write to %s: permission denied. Run the upgrade as the user that owns it, such as with sudo, or install into a directory you own", dir)
	}
	return err
}
