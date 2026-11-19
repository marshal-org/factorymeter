package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

var (
	now   = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	floor = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
)

func day(n float64) time.Time { return floor.Add(time.Duration(n * 24 * float64(time.Hour))) }

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.OpenWrite(dir, "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func syncer(h *fakeHub, s *store.Store, opt Options) *GitHub {
	if opt.Now.IsZero() {
		opt.Now = now
	}
	if opt.Since.IsZero() {
		opt.Since = floor
	}
	return &GitHub{C: h.client(), S: s, Opt: opt}
}

func numbers(t *testing.T, s *store.Store, repo string) []int {
	t.Helper()
	var out []int
	err := store.Changes.Each(s, record.GitHub, repo, time.Time{}, time.Time{}, func(_ string, rows []record.Change) error {
		for _, c := range rows {
			out = append(out, c.Number)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(out)
	return out
}

func same(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stateOf(t *testing.T, s *store.Store, repo string) state {
	t.Helper()
	sc, ok := s.Scope(record.GitHub, repo)
	if !ok {
		t.Fatalf("no scope for %s", repo)
	}
	var st state
	if err := json.Unmarshal(sc.State, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// small fills a repository with a few changes around the floor.
func small(h *fakeHub) *repo {
	r := h.add("acme/app")
	r.pr(1, "MERGED", day(-40), day(-30), "a1") // finished before the floor: not wanted
	r.pr(2, "OPEN", day(-60), day(-50), "a2")   // open and stale: wanted, for the queue
	r.pr(3, "MERGED", day(-5), day(2), "a3")    // created before the floor, touched after it
	r.pr(4, "MERGED", day(10), day(11), "a4")
	r.pr(5, "CLOSED", day(20), day(21), "a5")
	r.pr(6, "OPEN", day(90), day(91), "a6", "b6")
	return r
}

func TestFirstSyncReadsBackToTheFloor(t *testing.T) {
	h := newHub(t)
	small(h)
	s := openStore(t, t.TempDir())
	// The name is typed in another case than GitHub spells it.
	res, err := syncer(h, s, Options{}).Sync(context.Background(), "Acme/App")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := numbers(t, s, "acme/app"), []int{2, 3, 4, 5, 6}; !same(got, want) {
		t.Errorf("stored %v, want %v", got, want)
	}
	r := res.Repos[0]
	if r.Repo != "acme/app" || !r.First || r.Changes.Added != 5 || r.Changes.Total != 5 || !r.From.Equal(floor) {
		t.Errorf("result %+v", r)
	}
	sc, _ := s.Scope(record.GitHub, "acme/app")
	if sc.Name != "acme/app" || !sc.Private || !sc.From.Equal(floor) || !sc.SyncedAt.Equal(now) {
		t.Errorf("scope %+v", sc)
	}
	st := stateOf(t, s, "acme/app")
	if !st.Changes.High.Equal(day(91)) || !st.Changes.Floor.Equal(floor) || !st.Changes.Open {
		t.Errorf("state %+v", st.Changes)
	}
	rows, _ := store.Changes.Read(s, record.GitHub, "acme/app", "2026-10")
	if len(rows) != 1 || rows[0].ID != "github:acme/app#6" || rows[0].State != record.ChangeOpen || len(rows[0].Commits) != 2 ||
		rows[0].Commits[0].Trailers[0] != (record.Trailer{Key: "Co-Authored-By", Value: "Claude <noreply@anthropic.com>"}) {
		t.Errorf("change 6 stored as %+v", rows)
	}
}

func TestALaterSyncReadsOnlyWhatChanged(t *testing.T) {
	h := newHub(t)
	r := small(h)
	dir := t.TempDir()
	s := openStore(t, dir)
	g := syncer(h, s, Options{SkipCI: true, SkipDeploys: true})
	if _, err := g.Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	h.take()
	file := filepath.Join(dir, "changes", "github", "acme__app", "2026-10.parquet")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(file, old, old)

	// Nothing happened on GitHub: one request, and no file is written.
	res, err := g.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if calls := h.take(); len(calls) != 1 || calls[0].after != "" || calls[0].open {
		t.Errorf("a sync with nothing new made %+v", calls)
	}
	if c := res.Repos[0].Changes; c.Added != 0 || c.Changed != 0 || c.Total != 5 || res.Repos[0].First {
		t.Errorf("result %+v", res.Repos[0])
	}
	if st, _ := os.Stat(file); !st.ModTime().Equal(old) {
		t.Error("a file was rewritten though nothing changed")
	}

	// One change is merged and a new one is opened.
	r.prs[3].state, r.prs[3].updated, r.prs[3].title = "MERGED", now.Add(-2*time.Hour), "change 4, edited"
	r.pr(7, "OPEN", now.Add(-3*time.Hour), now.Add(-time.Hour), "a7")
	g.Opt.Now = now.Add(time.Hour)
	res, err = g.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Repos[0].Changes; c.Added != 1 || c.Changed != 1 || c.Total != 6 {
		t.Errorf("result %+v", res.Repos[0].Changes)
	}
	if calls := h.take(); len(calls) != 1 {
		t.Errorf("made %d requests, want 1", len(calls))
	}
	if st := stateOf(t, s, "acme/app"); !st.Changes.High.Equal(now.Add(-time.Hour)) {
		t.Errorf("high %v", st.Changes.High)
	}
	// The floor of the first sync stays, whatever a later sync asks for.
	g.Opt.Since = day(60)
	if _, err = g.Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := s.Scope(record.GitHub, "acme/app"); !sc.From.Equal(floor) {
		t.Errorf("the floor moved to %v", sc.From)
	}
}

// big fills a repository with n merged changes, one a day from the floor up.
func big(h *fakeHub, n int) *repo {
	r := h.add("acme/app")
	for i := 1; i <= n; i++ {
		r.pr(i, "MERGED", day(float64(i)), day(float64(i)+0.5), "s"+strings.Repeat("x", i%3))
	}
	return r
}

func TestAStoppedSyncGoesOnFromWhereItStopped(t *testing.T) {
	h := newHub(t)
	big(h, 60)
	dir := t.TempDir()
	s := openStore(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	// The seventh page never arrives: the sync is stopped while it waits.
	h.onPage = func(n int, r *http.Request) {
		if n == 7 {
			cancel()
			<-r.Context().Done()
		}
	}
	opt := Options{PageSize: 5, Checkpoint: 2, SkipCI: true, SkipDeploys: true}
	_, err := syncer(h, s, opt).Sync(ctx, "acme/app")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a cancel", err)
	}
	if got := len(numbers(t, s, "acme/app")); got != 30 {
		t.Errorf("%d changes stored after six pages of five, want 30", got)
	}
	sc, _ := s.Scope(record.GitHub, "acme/app")
	st := stateOf(t, s, "acme/app")
	if !sc.SyncedAt.IsZero() || !st.Changes.Floor.IsZero() || st.Changes.Cursor == "" {
		t.Errorf("after the stop: synced %v, state %+v", sc.SyncedAt, st.Changes)
	}
	saved := st.Changes.Cursor
	s.Close()

	// Another process, a day later.
	h.onPage = nil
	h.take()
	s = openStore(t, dir)
	later := syncer(h, s, opt)
	later.Opt.Now, later.Opt.Since = now.Add(24*time.Hour), floor.Add(24*time.Hour)
	res, err := later.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	calls := h.take()
	if calls[0].after != saved {
		t.Errorf("the second sync started at %q, want the saved place %q", calls[0].after, saved)
	}
	// Six pages are left, then one read from the top and one for open changes.
	if len(calls) != 8 {
		t.Errorf("the second sync made %d requests, want 8", len(calls))
	}
	if got := len(numbers(t, s, "acme/app")); got != 60 {
		t.Errorf("%d changes stored, want 60", got)
	}
	// The floor is the one the first sync set out for.
	if r := res.Repos[0]; !r.From.Equal(floor) || !r.First || r.Changes.Added != 30 {
		t.Errorf("result %+v", r)
	}
}

func TestARejectedCursorStartsFromTheTop(t *testing.T) {
	h := newHub(t)
	big(h, 30)
	s := openStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	h.onPage = func(n int, r *http.Request) {
		if n == 3 {
			cancel()
			<-r.Context().Done()
		}
	}
	opt := Options{PageSize: 5, Checkpoint: 1, SkipCI: true, SkipDeploys: true}
	if _, err := syncer(h, s, opt).Sync(ctx, "acme/app"); err == nil {
		t.Fatal("the stopped sync returned no error")
	}
	h.onPage, h.rejectCursors = nil, true
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(numbers(t, s, "acme/app")); got != 30 || res.Repos[0].Changes.Added != 20 {
		t.Errorf("%d changes stored, %d added, want 30 and 20", got, res.Repos[0].Changes.Added)
	}
}

func TestAnEarlierSinceReadsTheMissingPart(t *testing.T) {
	h := newHub(t)
	big(h, 60)
	s := openStore(t, t.TempDir())
	opt := Options{PageSize: 10, SkipCI: true, SkipDeploys: true, Since: day(40)}
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	// Changes 40 to 60 were updated at or after day 40.
	if got := numbers(t, s, "acme/app"); len(got) != 21 || got[0] != 40 {
		t.Fatalf("first sync stored %v", got)
	}
	first := h.take()
	opt.Since = day(10)
	res, err := syncer(h, s, opt).Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(t, s, "acme/app"); len(got) != 51 || got[0] != 10 {
		t.Errorf("after widening: %d changes from %d, want 51 from 10", len(got), got[0])
	}
	calls := h.take()
	if calls[0].after == "" {
		t.Error("the wider sync read from the top again")
	}
	// The first sync read three pages down, then the top and the open
	// changes. The wider one reads the last of the three again and three
	// more, then the top. The open changes are stored already.
	if len(of(first, "prs")) != 5 || len(calls) != 5 {
		t.Errorf("requests: first %d, wider %d", len(first), len(calls))
	}
	if !res.Repos[0].From.Equal(day(10)) {
		t.Errorf("from %v", res.Repos[0].From)
	}
}

func TestThePageShrinksWhenGitHubTimesOut(t *testing.T) {
	h := newHub(t)
	big(h, 20)
	h.maxPage = 7
	s := openStore(t, t.TempDir())
	if _, err := syncer(h, s, Options{SkipCI: true, SkipDeploys: true}).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if got := len(numbers(t, s, "acme/app")); got != 20 {
		t.Errorf("%d changes stored, want 20", got)
	}
	sizes := map[int]int{}
	for _, c := range h.take() {
		sizes[c.size]++
	}
	// 25 and 13 time out once each. Every later page asks for 7.
	if sizes[25] != 1 || sizes[13] != 1 || sizes[7] < 3 {
		t.Errorf("page sizes asked for: %v", sizes)
	}
}

func runs(t *testing.T, s *store.Store, repo string) []record.CIRun {
	t.Helper()
	var out []record.CIRun
	if err := store.CIRuns.Each(s, record.GitHub, repo, time.Time{}, time.Time{}, func(_ string, rows []record.CIRun) error {
		out = append(out, rows...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCIRunsOfPullRequests(t *testing.T) {
	h := newHub(t)
	r := small(h)
	r.runs = []ciRun{
		{1, "a4", "pull_request", day(10.1), "success"},
		{2, "a4", "push", day(11.1), "success"},
		{3, "a6", "pull_request", day(90.1), "failure"},
		{4, "zz", "pull_request_target", day(50), "success"},
		{5, "a6", "pull_request", now.Add(-time.Hour), ""}, // still running
		{6, "old", "pull_request", day(-3), "success"},     // before the floor
	}
	s := openStore(t, t.TempDir())
	g := syncer(h, s, Options{SkipDeploys: true})
	res, err := g.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	got := runs(t, s, "acme/app")
	if len(got) != 4 || res.Repos[0].CIRuns.Added != 4 {
		t.Fatalf("stored %d runs, want the 4 of pull request events since the floor: %+v", len(got), got)
	}
	for _, x := range got {
		if x.Event == "push" {
			t.Errorf("a push run was stored: %+v", x)
		}
	}
	st := stateOf(t, s, "acme/app")
	if st.CI.Mode != modePR || !st.CI.From.Equal(floor) || !st.CI.Through.Equal(now) {
		t.Errorf("ci state %+v", st.CI)
	}
	// August to November, two events a month: the newest month settles that
	// pull request events carry the CI, so no month asks for every event.
	first := of(h.take(), "runs")
	if len(first) != 8 {
		t.Errorf("the first sync made %d runs requests, want 8", len(first))
	}

	// The running one finishes. The next sync reads the last days again.
	r.runs[4].conclusion = "success"
	g.Opt.Now = now.Add(2 * time.Hour)
	res, err = g.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Repos[0].CIRuns; c.Added != 0 || c.Changed != 1 || c.Total != 4 {
		t.Errorf("result %+v", c)
	}
	second := of(h.take(), "runs")
	if len(second) != 2 || !strings.HasPrefix(second[0].created, ts(now.Add(-ciOverlap))) {
		t.Errorf("the second sync asked for %+v", second)
	}
}

func TestCIFallsBackToEveryEvent(t *testing.T) {
	h := newHub(t)
	r := small(h)
	// This repository builds on push only.
	r.runs = []ciRun{
		{1, "a4", "push", day(10.1), "success"},
		{2, "a6", "push", day(90.1), "success"},
		{3, "other", "schedule", day(30), "success"},
	}
	s := openStore(t, t.TempDir())
	if _, err := syncer(h, s, Options{SkipDeploys: true}).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	if st := stateOf(t, s, "acme/app"); st.CI.Mode != modeAll {
		t.Errorf("mode %q, want %q", st.CI.Mode, modeAll)
	}
	// November has no run on a change commit, so nothing is kept from it.
	// October settles the mode. From there down every run is kept.
	if got := runs(t, s, "acme/app"); len(got) != 3 {
		t.Errorf("stored %d runs, want 3: %+v", len(got), got)
	}
}

func TestCIStopsAtTheCapAndGoesOn(t *testing.T) {
	h := newHub(t)
	r := small(h)
	for i := 0; i < 250; i++ {
		r.runs = append(r.runs, ciRun{i + 1, "a6", "pull_request", day(91).Add(-time.Duration(i) * time.Hour), "success"})
	}
	s := openStore(t, t.TempDir())
	g := syncer(h, s, Options{SkipDeploys: true, MaxRuns: 100})
	res, err := g.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repos[0].Notes) != 1 || !strings.Contains(res.Repos[0].Notes[0], "stopped after 100 workflow runs") {
		t.Errorf("notes %q", res.Repos[0].Notes)
	}
	if got := len(runs(t, s, "acme/app")); got != 100 {
		t.Errorf("%d runs stored, want 100", got)
	}
	for i := 0; i < 5; i++ {
		if res, err = g.Sync(context.Background(), "acme/app"); err != nil {
			t.Fatal(err)
		}
		if len(res.Repos[0].Notes) == 0 {
			break
		}
	}
	if got := len(runs(t, s, "acme/app")); got != 250 || len(res.Repos[0].Notes) != 0 {
		t.Errorf("%d runs stored with notes %q, want all 250 and no note", got, res.Repos[0].Notes)
	}
	if st := stateOf(t, s, "acme/app"); !st.CI.From.Equal(floor) {
		t.Errorf("ci is stored from %v, want the floor", st.CI.From)
	}
}

func TestDeploysAndReleases(t *testing.T) {
	h := newHub(t)
	r := small(h)
	st := func(state string, at time.Time) obj { return obj{"state": state, "createdAt": ts(at)} }
	r.deploys = []deployment{
		{1, "production", day(12), "a4", []obj{st("INACTIVE", day(30)), st("SUCCESS", day(12.01)), st("IN_PROGRESS", day(12))}},
		{2, "staging", day(13), "a4", []obj{st("FAILURE", day(13.02)), st("IN_PROGRESS", day(13))}},
		{3, "production", now.Add(-time.Hour), "a6", []obj{st("IN_PROGRESS", now.Add(-time.Hour))}},
		{4, "production", day(-10), "a1", []obj{st("SUCCESS", day(-10))}}, // before the floor
	}
	r.releases = []release{
		{tag: "v1.0.0", created: day(14), published: day(14.5), sha: "a4"},
		{tag: "v1.1.0-rc1", pre: true, created: day(20), published: day(20), sha: "a5"},
		{tag: "v1.1.0", draft: true, created: day(90)},
	}
	s := openStore(t, t.TempDir())
	g := syncer(h, s, Options{SkipCI: true})
	if _, err := g.Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	by := map[string]record.Deploy{}
	store.Deploys.Each(s, record.GitHub, "acme/app", time.Time{}, time.Time{}, func(_ string, rows []record.Deploy) error {
		for _, d := range rows {
			by[d.ID] = d
		}
		return nil
	})
	if len(by) != 4 {
		t.Fatalf("stored %d deploys, want 4: %+v", len(by), by)
	}
	d1 := by["github:acme/app/deployment/1"]
	if d1.Outcome != record.DeploySuccess || d1.FinishedAt == nil || !d1.FinishedAt.Equal(day(12.01)) || !d1.Production || d1.SHA != "a4" || d1.Ref != "main" {
		t.Errorf("deployment 1: %+v", d1)
	}
	if d := by["github:acme/app/deployment/2"]; d.Outcome != record.DeployFailure || d.Production || d.FinishedAt == nil {
		t.Errorf("deployment 2: %+v", d)
	}
	if d := by["github:acme/app/deployment/3"]; d.Outcome != record.DeployRunning || d.FinishedAt != nil {
		t.Errorf("deployment 3: %+v", d)
	}
	rel := by["github:acme/app/release/v1.0.0"]
	if rel.Via != "release" || !rel.Production || !rel.CreatedAt.Equal(day(14.5)) || rel.Outcome != record.DeploySuccess || rel.SHA != "a4" {
		t.Errorf("release: %+v", rel)
	}

	// The running deployment succeeds and the draft is published.
	r.deploys[2].statuses = append([]obj{st("SUCCESS", now)}, r.deploys[2].statuses...)
	r.releases[2].draft, r.releases[2].published = false, now
	g.Opt.Now = now.Add(time.Hour)
	res, err := g.Sync(context.Background(), "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if c := res.Repos[0].Deploys; c.Added != 1 || c.Changed != 1 || c.Total != 5 {
		t.Errorf("result %+v", c)
	}
}

func TestOneFailingRepositoryDoesNotStopTheOthers(t *testing.T) {
	h := newHub(t)
	for _, name := range []string{"acme/one", "acme/gone", "acme/two"} {
		r := h.add(name)
		r.pr(1, "MERGED", day(1), day(2), "a")
	}
	h.repos["acme/gone"].missing = true
	s := openStore(t, t.TempDir())
	res, err := syncer(h, s, Options{SkipCI: true, SkipDeploys: true, Workers: 2}).Sync(context.Background(), "acme")
	if err == nil || !strings.Contains(err.Error(), "1 of 3 repositories") || !strings.Contains(err.Error(), "acme/gone") || !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(res.Repos) != 3 || res.Repos[0].Err != nil || res.Repos[1].Err == nil || res.Repos[2].Err != nil {
		t.Errorf("results %+v", res.Repos)
	}
	if got := len(s.Scopes(record.GitHub)); got != 2 {
		t.Errorf("%d scopes in the store, want 2", got)
	}
}

func TestSyncRefusesAnotherOwner(t *testing.T) {
	h := newHub(t)
	s := openStore(t, t.TempDir())
	if _, err := syncer(h, s, Options{}).Sync(context.Background(), "other/app"); err == nil || !strings.Contains(err.Error(), "holds acme") {
		t.Errorf("err = %v", err)
	}
}
