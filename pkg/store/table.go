package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"

	"github.com/marshal-org/factorymeter/pkg/record"
)

// Table is one kind of record in the store.
type Table[T any] struct {
	// Name is the directory of the table.
	Name string
	// ID returns the id a row is replaced by.
	ID func(*T) string
	// At returns the time that decides which month file a row lives in. It
	// must never change for a record. Nil keeps every row in one file.
	At func(*T) time.Time
}

// The tables of the store, one per record kind.
var (
	Changes = Table[record.Change]{Name: "changes",
		ID: func(r *record.Change) string { return r.ID }, At: func(r *record.Change) time.Time { return r.CreatedAt }}
	CIRuns = Table[record.CIRun]{Name: "ci_runs",
		ID: func(r *record.CIRun) string { return r.ID }, At: func(r *record.CIRun) time.Time { return r.CreatedAt }}
	Deploys = Table[record.Deploy]{Name: "deploys",
		ID: func(r *record.Deploy) string { return r.ID }, At: func(r *record.Deploy) time.Time { return r.CreatedAt }}
	Tickets = Table[record.Ticket]{Name: "tickets",
		ID: func(r *record.Ticket) string { return r.ID }, At: func(r *record.Ticket) time.Time { return r.CreatedAt }}
	Links = Table[record.Link]{Name: "links",
		ID: func(r *record.Link) string { return r.Kind + " " + r.From + " " + r.To }}
)

// TableNames lists the tables in the order a status prints them.
var TableNames = []string{Changes.Name, CIRuns.Name, Deploys.Name, Tickets.Name, Links.Name}

const (
	ext     = ".parquet"
	allPart = "all"
	month   = "2006-01"
)

func (t Table[T]) part(r *T) string {
	if t.At == nil {
		return allPart
	}
	return t.At(r).UTC().Format(month)
}

func (t Table[T]) dir(s *Store, source, scope string) (string, error) {
	d, err := scopeDir(source, scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, t.Name, d), nil
}

// Put adds rows to a scope. A row whose id is already stored replaces the
// stored one. It reports how many rows were new and how many stored rows
// changed. A month file whose rows all stay the same is not rewritten, so a
// sync that finds nothing new leaves the store untouched. Only one caller may
// write to a scope of a table at a time.
func (t Table[T]) Put(s *Store, source, scope string, rows []T) (added, changed int, err error) {
	if !s.writable {
		return 0, 0, errors.New("store: opened for reading")
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	dir, err := t.dir(s, source, scope)
	if err != nil {
		return 0, 0, err
	}
	byPart := map[string][]T{}
	for i := range rows {
		p := t.part(&rows[i])
		byPart[p] = append(byPart[p], rows[i])
	}
	for p, fresh := range byPart {
		path := filepath.Join(dir, p+ext)
		old, err := readFile[T](path)
		if err != nil {
			return added, changed, err
		}
		dirty := false
		stored := len(old)
		touched := map[int]bool{}
		at := make(map[string]int, len(old)+len(fresh))
		for i := range old {
			at[t.ID(&old[i])] = i
		}
		for i := range fresh {
			id := t.ID(&fresh[i])
			if j, ok := at[id]; ok {
				if j >= stored {
					// The same id twice in one call: the later row wins.
					old[j] = fresh[i]
				} else if !reflect.DeepEqual(old[j], fresh[i]) {
					old[j] = fresh[i]
					if !touched[j] {
						touched[j] = true
						changed++
					}
					dirty = true
				}
				continue
			}
			at[id] = len(old)
			old = append(old, fresh[i])
			added++
			dirty = true
		}
		if !dirty {
			continue
		}
		// A fixed order makes the file the same whatever order rows arrived in.
		sort.SliceStable(old, func(i, j int) bool {
			if t.At != nil {
				if a, b := t.At(&old[i]), t.At(&old[j]); !a.Equal(b) {
					return a.Before(b)
				}
			}
			return t.ID(&old[i]) < t.ID(&old[j])
		})
		err = replace(path, func(f *os.File) error {
			w := parquet.NewGenericWriter[T](f,
				parquet.Compression(&zstd.Codec{}),
				parquet.CreatedBy("factorymeter", strconv.Itoa(Format), ""),
				parquet.KeyValueMetadata("factorymeter.records", strconv.Itoa(record.SchemaVersion)))
			if _, err := w.Write(old); err != nil {
				return err
			}
			return w.Close()
		})
		if err != nil {
			return added, changed, fmt.Errorf("store: write %s: %w", path, err)
		}
	}
	return added, changed, nil
}

func readFile[T any](path string) ([]T, error) {
	rows, err := parquet.ReadFile[T](path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", path, err)
	}
	return rows, nil
}

// Parts lists the month files a scope has, oldest first.
func (t Table[T]) Parts(s *Store, source, scope string) ([]string, error) {
	dir, err := t.dir(s, source, scope)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ext) {
			parts = append(parts, strings.TrimSuffix(n, ext))
		}
	}
	sort.Strings(parts)
	return parts, nil
}

// Read returns the rows of one month file, oldest first.
func (t Table[T]) Read(s *Store, source, scope, part string) ([]T, error) {
	dir, err := t.dir(s, source, scope)
	if err != nil {
		return nil, err
	}
	return readFile[T](filepath.Join(dir, part+ext))
}

// Each calls fn with the rows of every month file of a scope that can hold a
// record created between from and to, oldest month first. A zero from or to
// leaves that side open. Rows outside the range in the first and last month
// are passed too: the caller filters by the time it cares about.
func (t Table[T]) Each(s *Store, source, scope string, from, to time.Time, fn func(part string, rows []T) error) error {
	parts, err := t.Parts(s, source, scope)
	if err != nil {
		return err
	}
	lo, hi := "", "9999-99"
	if !from.IsZero() {
		lo = from.UTC().Format(month)
	}
	if !to.IsZero() {
		hi = to.UTC().Format(month)
	}
	for _, p := range parts {
		if p != allPart && (p < lo || p > hi) {
			continue
		}
		rows, err := t.Read(s, source, scope, p)
		if err != nil {
			return err
		}
		if err := fn(p, rows); err != nil {
			return err
		}
	}
	return nil
}

// Count returns how many rows a scope has. It reads only the file footers.
func (t Table[T]) Count(s *Store, source, scope string) (int64, error) {
	dir, err := t.dir(s, source, scope)
	if err != nil {
		return 0, err
	}
	return countDir(dir)
}

func countDir(dir string) (int64, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int64
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		rows, err := countFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return n, err
		}
		n += rows
	}
	return n, nil
}

func countFile(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		return 0, fmt.Errorf("store: read %s: %w", path, err)
	}
	return pf.NumRows(), nil
}

// Count returns how many rows a table holds for a scope, by table name.
func (s *Store) Count(table, source, scope string) (int64, error) {
	d, err := scopeDir(source, scope)
	if err != nil {
		return 0, err
	}
	return countDir(filepath.Join(s.Dir, table, d))
}

// Size returns the bytes the store takes on disk.
func (s *Store) Size() (int64, error) {
	var n int64
	err := filepath.WalkDir(s.Dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if info, ierr := d.Info(); ierr == nil {
			n += info.Size()
		}
		return nil
	})
	return n, err
}
