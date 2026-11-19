package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/marshal-org/factorymeter/pkg/record"
)

var t0 = time.Date(2026, 8, 30, 10, 30, 0, 0, time.UTC)

func change(n int, created time.Time, title string) record.Change {
	ready := created.Add(time.Hour)
	return record.Change{
		Source: record.GitHub, ID: record.ChangeID(record.GitHub, "Acme/App", n), Repo: "Acme/App", Number: n, Title: title,
		Author: record.Actor{Login: "dev", Kind: record.ActorUser}, State: record.ChangeMerged,
		CreatedAt: created, UpdatedAt: created.Add(2 * time.Hour), ReadyAt: &ready,
		Commits: []record.Commit{{SHA: "abc", AuthoredAt: created, CommittedAt: created, Parents: 1,
			Authors:  []record.CommitAuthor{{Login: "dev", Email: "dev@example.invalid", Name: "Dev"}},
			Trailers: []record.Trailer{{Key: "Co-authored-by", Value: "Bot <bot@example.invalid>"}}}},
		Reviews: []record.Review{{Author: record.Actor{Login: "rev", Kind: record.ActorUser}, State: record.ReviewApproved, SubmittedAt: created.Add(90 * time.Minute)}},
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := OpenWrite(filepath.Join(t.TempDir(), "s"), "Acme", t0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutKeepsOneRowPerIDInItsMonth(t *testing.T) {
	s := open(t)
	first := []record.Change{change(2, t0, "two"), change(1, t0.AddDate(0, 0, -1), "one"), change(3, t0.AddDate(0, 0, 3), "three")}
	added, changed, err := Changes.Put(s, record.GitHub, "Acme/App", first)
	if err != nil || added != 3 || changed != 0 {
		t.Fatalf("added %d, changed %d, err %v", added, changed, err)
	}
	// A second write with one known id and one new id replaces the first and
	// adds the second. The known row keeps its file.
	added, changed, err = Changes.Put(s, record.GitHub, "acme/app", []record.Change{change(2, t0, "two, again"), change(4, t0.Add(time.Minute), "four")})
	if err != nil || added != 1 || changed != 1 {
		t.Fatalf("added %d, changed %d, err %v", added, changed, err)
	}
	parts, err := Changes.Parts(s, record.GitHub, "Acme/App")
	if err != nil || !reflect.DeepEqual(parts, []string{"2026-08", "2026-09"}) {
		t.Fatalf("parts %v, err %v", parts, err)
	}
	aug, err := Changes.Read(s, record.GitHub, "Acme/App", "2026-08")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range aug {
		got = append(got, c.Title)
	}
	if strings.Join(got, "|") != "one|two, again|four" {
		t.Errorf("rows must be one per id, oldest first: %v", got)
	}
	// Everything that went in comes back, down to the nested lists and the
	// optional times.
	want := change(1, t0.AddDate(0, 0, -1), "one")
	if !reflect.DeepEqual(aug[0], want) {
		t.Errorf("round trip\n got %+v\nwant %+v", aug[0], want)
	}
	if aug[0].MergedAt != nil || aug[0].ReadyAt == nil {
		t.Errorf("a missing time must stay nil and a set one must stay set: %+v", aug[0])
	}
	n, err := Changes.Count(s, record.GitHub, "Acme/App")
	if err != nil || n != 4 {
		t.Errorf("count %d, err %v", n, err)
	}
	if n, err := s.Count("changes", record.GitHub, "Acme/App"); err != nil || n != 4 {
		t.Errorf("count by name %d, err %v", n, err)
	}
	if n, err := CIRuns.Count(s, record.GitHub, "Acme/App"); err != nil || n != 0 {
		t.Errorf("an empty table counts 0: %d, %v", n, err)
	}
	// No temporary file is left behind.
	ents, _ := os.ReadDir(filepath.Join(s.Dir, "changes", "github", "acme__app"))
	if len(ents) != 2 {
		t.Errorf("directory holds %d entries, want the 2 month files", len(ents))
	}
}

// Writing rows the store already holds must not touch a file: a weekly sync of
// a quiet repository then costs no disk writes, and a file's time says when
// its content last changed.
func TestPutLeavesAnUnchangedFileAlone(t *testing.T) {
	s := open(t)
	rows := []record.Change{change(1, t0, "one"), change(2, t0.Add(time.Hour), "two")}
	// A change with no commit and no review reads back with empty lists. It
	// must still compare equal to the row that went in.
	bare := change(3, t0.Add(2*time.Hour), "bare")
	bare.Commits, bare.Reviews = []record.Commit{}, []record.Review{}
	rows = append(rows, bare)
	if _, _, err := Changes.Put(s, record.GitHub, "acme/app", rows); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, "changes", "github", "acme__app", "2026-08.parquet")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(path, old, old)
	added, changed, err := Changes.Put(s, record.GitHub, "acme/app", rows)
	if err != nil || added != 0 || changed != 0 {
		t.Fatalf("added %d, changed %d, err %v", added, changed, err)
	}
	if st, _ := os.Stat(path); !st.ModTime().Equal(old) {
		t.Error("the file was rewritten though no row changed")
	}
	rows[1].Title = "two, edited"
	added, changed, err = Changes.Put(s, record.GitHub, "acme/app", rows)
	if err != nil || added != 0 || changed != 1 {
		t.Fatalf("after an edit: added %d, changed %d, err %v", added, changed, err)
	}
	if st, _ := os.Stat(path); st.ModTime().Equal(old) {
		t.Error("the file was not rewritten though a row changed")
	}
}

func TestEachReadsOnlyTheMonthsAsked(t *testing.T) {
	s := open(t)
	var rows []record.CIRun
	for m := 0; m < 6; m++ {
		at := time.Date(2026, time.Month(3+m), 15, 0, 0, 0, 0, time.UTC)
		rows = append(rows, record.CIRun{Source: record.GitHub, ID: record.CIRunID(record.GitHub, "acme/app", int64(m), 1), Repo: "acme/app", CreatedAt: at, StartedAt: at, UpdatedAt: at})
	}
	if _, _, err := CIRuns.Put(s, record.GitHub, "acme/app", rows); err != nil {
		t.Fatal(err)
	}
	var seen []string
	err := CIRuns.Each(s, record.GitHub, "acme/app", time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		func(part string, rows []record.CIRun) error { seen = append(seen, part); return nil })
	if err != nil || strings.Join(seen, " ") != "2026-05 2026-06 2026-07" {
		t.Errorf("parts %v, err %v", seen, err)
	}
	seen = nil
	CIRuns.Each(s, record.GitHub, "acme/app", time.Time{}, time.Time{}, func(part string, _ []record.CIRun) error { seen = append(seen, part); return nil })
	if len(seen) != 6 {
		t.Errorf("an open range reads every month: %v", seen)
	}
	stop := errors.New("stop")
	if err := CIRuns.Each(s, record.GitHub, "acme/app", time.Time{}, time.Time{}, func(string, []record.CIRun) error { return stop }); err != stop {
		t.Errorf("the callback's error must come back: %v", err)
	}
}

func TestLinksLiveInOneFile(t *testing.T) {
	s := open(t)
	l := record.Link{Kind: record.LinkCloses, From: "github:acme/app#1", FromKind: record.KindChange, To: "github:acme/app#9", ToKind: record.KindTicket, Found: "body"}
	if n, _, err := Links.Put(s, record.GitHub, "acme/app", []record.Link{l, l}); err != nil || n != 1 {
		t.Fatalf("added %d, err %v", n, err)
	}
	parts, _ := Links.Parts(s, record.GitHub, "acme/app")
	if !reflect.DeepEqual(parts, []string{"all"}) {
		t.Errorf("parts %v", parts)
	}
}

func TestFilesUseTheContractColumnNames(t *testing.T) {
	s := open(t)
	if _, _, err := Changes.Put(s, record.GitHub, "acme/app", []record.Change{change(1, t0, "one")}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(s.Dir, "changes", "github", "acme__app", "2026-08.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for _, c := range pf.Schema().Fields() {
		cols = append(cols, c.Name())
	}
	// The columns are the json names of record.Change, in order.
	var want []string
	rt := reflect.TypeOf(record.Change{})
	for i := 0; i < rt.NumField(); i++ {
		want = append(want, rt.Field(i).Tag.Get("json"))
	}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("columns\n got %v\nwant %v", cols, want)
	}
}

// Every field of every record must name its Parquet column after its json
// name, so a file reads the same as the contract.
func TestEveryRecordFieldHasItsColumn(t *testing.T) {
	var walk func(rt reflect.Type)
	seen := map[reflect.Type]bool{}
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || rt == reflect.TypeOf(time.Time{}) || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("parquet"), ",")
			if name != f.Tag.Get("json") {
				t.Errorf("%s.%s: parquet column %q, json name %q", rt.Name(), f.Name, name, f.Tag.Get("json"))
			}
			if (f.Type.Kind() == reflect.Pointer) != strings.Contains(opts, "optional") {
				t.Errorf("%s.%s: a pointer, and only a pointer, is optional", rt.Name(), f.Name)
			}
			if (f.Type == reflect.TypeOf(time.Time{}) || f.Type == reflect.TypeOf(&time.Time{})) != strings.Contains(opts, "timestamp(microsecond)") {
				t.Errorf("%s.%s: a time, and only a time, is a microsecond timestamp", rt.Name(), f.Name)
			}
			if (f.Type.Kind() == reflect.Slice) != strings.Contains(opts, "list") {
				t.Errorf("%s.%s: a slice, and only a slice, is a list", rt.Name(), f.Name)
			}
			walk(f.Type)
		}
	}
	for _, v := range []any{record.Change{}, record.CIRun{}, record.Deploy{}, record.Ticket{}, record.Link{}} {
		walk(reflect.TypeOf(v))
	}
}

func TestManifestSurvivesAReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s, err := OpenWrite(dir, "Acme", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Scope(record.GitHub, "Acme/App"); ok {
		t.Fatal("a new store knows no scope")
	}
	sc := Scope{Source: record.GitHub, Name: "Acme/App", Private: true, From: t0, State: json.RawMessage(`{"cursor":"c9"}`)}
	if err := s.PutScope(sc); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScope(Scope{Source: record.GitHub, Name: "../x"}); err == nil {
		t.Error("a scope name that leaves the store must be refused")
	}
	// A second writer is turned away while the first holds the store.
	if _, err := OpenWrite(dir, "acme", t0); err == nil || !strings.Contains(err.Error(), "another sync") {
		t.Errorf("second writer: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.Scope(record.GitHub, "acme/app")
	if !ok || got.Name != "Acme/App" || !got.Private || string(got.State) == "" || !got.From.Equal(t0) {
		t.Errorf("scope after reopen: %+v", got)
	}
	if r.Owner() != "Acme" || !r.CreatedAt().Equal(t0) || len(r.Scopes(record.GitHub)) != 1 || len(r.Scopes("linear")) != 0 {
		t.Errorf("manifest after reopen: owner %q, scopes %v", r.Owner(), r.Scopes(""))
	}
	if err := r.PutScope(sc); err == nil {
		t.Error("a store opened for reading must refuse a write")
	}
	if _, _, err := Changes.Put(r, record.GitHub, "acme/app", []record.Change{change(1, t0, "x")}); err == nil {
		t.Error("a store opened for reading must refuse rows")
	}
	// The same directory cannot hold a second owner.
	if _, err := OpenWrite(dir, "other", t0); err == nil || !strings.Contains(err.Error(), "holds Acme") {
		t.Errorf("second owner: %v", err)
	}
	// The refusal must not leave the lock behind.
	w, err := OpenWrite(dir, "ACME", t0)
	if err != nil {
		t.Fatalf("reopen for writing: %v", err)
	}
	w.Close()
	if n, err := r.Size(); err != nil || n == 0 {
		t.Errorf("size %d, err %v", n, err)
	}
}

func TestOpenRefusesWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); !errors.Is(err, ErrNoStore) {
		t.Errorf("an empty directory: %v", err)
	}
	os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{"format": 99, "records": 1, "owner": "acme"}`), 0o644)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "newer factorymeter") {
		t.Errorf("a newer format: %v", err)
	}
	os.WriteFile(filepath.Join(dir, ManifestFile), []byte(`{not json`), 0o644)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("a damaged manifest: %v", err)
	}
}

func TestAStaleLockIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockFile)
	os.WriteFile(path, []byte("1\n"), 0o644)
	old := time.Now().Add(-2 * lockStale)
	os.Chtimes(path, old, old)
	s, err := OpenWrite(dir, "acme", t0)
	if err != nil {
		t.Fatalf("a lock nobody touched for a while must be taken over: %v", err)
	}
	s.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("close must remove the lock: %v", err)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("FACTORYMETER_STORE", "")
	d, err := DefaultDir("Acme")
	if err != nil || filepath.Base(d) != "acme" || filepath.Base(filepath.Dir(d)) != "factorymeter" {
		t.Errorf("dir %q, err %v", d, err)
	}
	if _, err := DefaultDir("a/b"); err == nil {
		t.Error("an owner has no slash")
	}
	t.Setenv("FACTORYMETER_STORE", "/somewhere")
	if d, _ := DefaultDir("acme"); d != "/somewhere" {
		t.Errorf("the environment wins: %q", d)
	}
}
