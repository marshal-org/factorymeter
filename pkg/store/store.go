// Package store keeps records on disk between runs, so a report never has to
// read a line's whole history from its sources again.
//
// A store is a directory. It holds one manifest, store.json, and Parquet files
// laid out as
//
//	<table>/<source>/<scope>/<month>.parquet
//
// where a scope is a repository or a ticket project and the month is the month
// the record was created in. A record never moves between files, so a sync
// only rewrites the months it touched.
//
// The files are plain Parquet with the column names of the record layout. Any
// tool that reads Parquet can query them.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
)

// Format is the version of the directory layout and of the manifest.
const Format = 1

// ManifestFile is the name of the manifest inside a store.
const ManifestFile = "store.json"

const lockFile = "store.lock"

// A lock that nobody has touched for this long belongs to a sync that died.
const (
	lockBeat  = 20 * time.Second
	lockStale = 2 * time.Minute
)

// ErrNoStore means the directory holds no store yet.
var ErrNoStore = errors.New("no store")

// Manifest describes what a store holds.
type Manifest struct {
	Format    int               `json:"format"`
	Records   int               `json:"records"`
	Owner     string            `json:"owner"`
	CreatedAt time.Time         `json:"created_at"`
	Scopes    map[string]*Scope `json:"scopes"`
}

// Scope is one repository or one ticket project in the store.
type Scope struct {
	// Source is the system the records came from, such as "github".
	Source string `json:"source"`
	// Name is the scope's name on the source, such as "owner/repo".
	Name    string `json:"name"`
	Private bool   `json:"private"`
	// From is the time the store holds this scope's records from.
	From time.Time `json:"from"`
	// SyncedAt is when the last sync of this scope finished. Zero while the
	// first one has not finished.
	SyncedAt time.Time `json:"synced_at"`
	// Notes are limits the last sync ran into, in plain sentences.
	Notes []string `json:"notes"`
	// State belongs to the source adapter. It holds what the adapter needs to
	// read only what changed next time.
	State json.RawMessage `json:"state"`
}

// Key is the manifest key and the directory of a scope.
func Key(source, name string) string { return source + ":" + strings.ToLower(name) }

var scopeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9._-]+)?$`)

// scopeDir turns "owner/repo" into a directory name. Names are compared
// without case, as GitHub does, so one repository never gets two directories.
func scopeDir(source, name string) (string, error) {
	if !scopeName.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("store: %q is not a scope name", name)
	}
	if !scopeName.MatchString(source) || strings.Contains(source, "/") {
		return "", fmt.Errorf("store: %q is not a source name", source)
	}
	return filepath.Join(source, strings.ReplaceAll(strings.ToLower(name), "/", "__")), nil
}

// Store is an open store directory.
type Store struct {
	Dir string

	mu       sync.Mutex
	m        Manifest
	writable bool
	stop     chan struct{}
	done     chan struct{}
}

// DefaultDir is where the store of an owner lives when no directory is given:
// $FACTORYMETER_STORE, or factorymeter/<owner> under the user's cache directory.
func DefaultDir(owner string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("FACTORYMETER_STORE")); v != "" {
		return v, nil
	}
	if !scopeName.MatchString(owner) || strings.Contains(owner, "/") {
		return "", fmt.Errorf("store: %q is not an owner name", owner)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("store: no cache directory, pass --store: %w", err)
	}
	return filepath.Join(base, "factorymeter", strings.ToLower(owner)), nil
}

func readManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, fmt.Errorf("%w in %s", ErrNoStore, dir)
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("store: %s is damaged: %w", filepath.Join(dir, ManifestFile), err)
	}
	if m.Format > Format || m.Records > record.SchemaVersion {
		return m, fmt.Errorf("store: %s was written by a newer factorymeter (format %d, records %d). Upgrade, or use another directory", dir, m.Format, m.Records)
	}
	if m.Scopes == nil {
		m.Scopes = map[string]*Scope{}
	}
	return m, nil
}

// Open opens a store for reading. It fails with ErrNoStore when the directory
// holds none.
func Open(dir string) (*Store, error) {
	m, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	return &Store{Dir: dir, m: m}, nil
}

// OpenWrite opens the store of an owner for a sync, and creates it when the
// directory holds none. Only one process can hold a store open for writing.
// Close releases it.
func OpenWrite(dir, owner string, now time.Time) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := lock(dir); err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, writable: true, stop: make(chan struct{}), done: make(chan struct{})}
	m, err := readManifest(dir)
	switch {
	case errors.Is(err, ErrNoStore):
		m = Manifest{Format: Format, Records: record.SchemaVersion, Owner: owner, CreatedAt: now.UTC(), Scopes: map[string]*Scope{}}
		s.m = m
		if err := s.save(); err != nil {
			os.Remove(filepath.Join(dir, lockFile))
			return nil, err
		}
	case err != nil:
		os.Remove(filepath.Join(dir, lockFile))
		return nil, err
	case !strings.EqualFold(m.Owner, owner):
		os.Remove(filepath.Join(dir, lockFile))
		return nil, fmt.Errorf("store: %s holds %s, not %s. One store holds one owner", dir, m.Owner, owner)
	default:
		s.m = m
	}
	go s.beat()
	return s, nil
}

// lock takes the write lock of a directory. A lock file that has not been
// touched for a while was left by a process that died, and is taken over.
func lock(dir string) error {
	path := filepath.Join(dir, lockFile)
	for try := 0; try < 2; try++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			return f.Close()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		st, serr := os.Stat(path)
		if serr != nil {
			continue
		}
		if age := time.Since(st.ModTime()); age < lockStale {
			return fmt.Errorf("store: another sync is writing to %s. If none is running, delete %s", dir, path)
		}
		os.Remove(path)
	}
	return fmt.Errorf("store: could not lock %s", dir)
}

// beat touches the lock file while the store is open, so another process can
// tell a running sync from one that died.
func (s *Store) beat() {
	defer close(s.done)
	t := time.NewTicker(lockBeat)
	defer t.Stop()
	path := filepath.Join(s.Dir, lockFile)
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			os.Chtimes(path, now, now)
		}
	}
}

// Close releases the write lock. It does nothing on a store opened for reading.
func (s *Store) Close() error {
	if !s.writable {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		return nil
	}
	close(s.stop)
	<-s.done
	s.stop = nil
	return os.Remove(filepath.Join(s.Dir, lockFile))
}

// Owner is the owner the store holds.
func (s *Store) Owner() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m.Owner
}

// CreatedAt is when the store was first written.
func (s *Store) CreatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m.CreatedAt
}

func (sc *Scope) clone() Scope {
	c := *sc
	c.Notes = append([]string(nil), sc.Notes...)
	c.State = append(json.RawMessage(nil), sc.State...)
	return c
}

// Scope returns one scope, and whether the store knows it.
func (s *Store) Scope(source, name string) (Scope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.m.Scopes[Key(source, name)]
	if !ok {
		return Scope{Source: source, Name: name}, false
	}
	return sc.clone(), true
}

// Scopes lists the scopes of a source, by name. An empty source lists all.
func (s *Store) Scopes(source string) []Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Scope
	for _, sc := range s.m.Scopes {
		if source == "" || sc.Source == source {
			out = append(out, sc.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return Key(out[i].Source, out[i].Name) < Key(out[j].Source, out[j].Name) })
	return out
}

// PutScope writes a scope to the manifest. The manifest file is replaced in
// one step, so a crash leaves either the old one or the new one.
func (s *Store) PutScope(sc Scope) error {
	if _, err := scopeDir(sc.Source, sc.Name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.writable {
		return errors.New("store: opened for reading")
	}
	c := sc.clone()
	if c.Notes == nil {
		c.Notes = []string{}
	}
	s.m.Scopes[Key(sc.Source, sc.Name)] = &c
	return s.save()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	return replace(filepath.Join(s.Dir, ManifestFile), func(f *os.File) error {
		_, err := f.Write(append(b, '\n'))
		return err
	})
}

// replace writes a file under a temporary name and renames it into place.
func replace(path string, write func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
