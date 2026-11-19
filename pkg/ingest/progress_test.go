package ingest

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
)

// watch makes a sync tell its events to a list.
func watch(opt *Options) func() []Event {
	var mu sync.Mutex
	var seen []Event
	opt.Progress = func(e Event) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
	}
	return func() []Event {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		return out
	}
}

func TestPartIsTheShareOfAStretch(t *testing.T) {
	for _, c := range []struct {
		name           string
		start, end, at time.Time
		want           float64
	}{
		{"at the start", day(10), day(0), day(10), 0},
		{"half way down", day(10), day(0), day(5), 0.5},
		{"at the end", day(10), day(0), day(0), 1},
		{"past the end", day(10), day(0), day(-3), 1},
		{"before the start", day(10), day(0), day(12), 0},
		{"a read that goes up", day(0), day(10), day(2.5), 0.25},
		{"a stretch of no length", day(4), day(4), day(4), 1},
	} {
		if got := part(c.start, c.end, c.at); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A display draws a sync from its events. They come in a fixed order: the
// list of scopes, the start of the scope, each step from start to done, and
// the end of the scope.
func TestASyncTellsADisplayWhatItDoes(t *testing.T) {
	h := newHub(t)
	r := big(h, 60)
	for i := 1; i <= 30; i++ {
		r.runs = append(r.runs, ciRun{id: i, sha: "s", event: "pull_request", created: day(float64(i) + 0.2), conclusion: "success"})
	}
	s := openStore(t, t.TempDir())
	opt := Options{PageSize: 25}
	take := watch(&opt)
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	ev := take()
	if len(ev) < 4 {
		t.Fatalf("%d events", len(ev))
	}
	if e := ev[0]; e.Kind != Listed || e.Source != record.GitHub || e.Scope != "acme/app" || e.Total != 1 || !reflect.DeepEqual(e.Names, []string{"acme/app"}) {
		t.Errorf("first event: %+v", e)
	}
	if e := ev[1]; e.Kind != ScopeStarted || !e.First || e.Last != 0 || e.LastFull || !e.LastAt.IsZero() {
		t.Errorf("second event: %+v", e)
	}
	last := ev[len(ev)-1]
	if last.Kind != ScopeDone || last.Err != nil || last.Repo == nil || last.Repo.Changes.Added != 60 || last.Took <= 0 {
		t.Errorf("last event: %+v", last)
	}

	var order []Step
	var cur Step
	read, share, advanced := 0, 0.0, map[Step]int{}
	for i, e := range ev[1:] {
		if e.Source != record.GitHub || e.Slot != 0 || e.Scope != "acme/app" {
			t.Errorf("event %d does not name its scope: %+v", i+1, e)
		}
		switch e.Kind {
		case StepStarted:
			if cur != "" {
				t.Errorf("%s started before %s was done", e.Step, cur)
			}
			cur, read, share = e.Step, 0, 0
			order = append(order, e.Step)
		case StepAdvanced:
			if e.Step != cur {
				t.Errorf("%s advanced while %q ran", e.Step, cur)
			}
			if e.Share < 0 || e.Share > 1 || e.Read < read {
				t.Errorf("%s: share %v, read %d after %d", e.Step, e.Share, e.Read, read)
			}
			// The pull requests are read in one pass from the top down, so
			// their share never goes back. The share of CI runs can.
			if e.Step == StepChanges && e.Share < share {
				t.Errorf("the pull request share went back from %v to %v", share, e.Share)
			}
			read, share = e.Read, e.Share
			advanced[e.Step]++
		case StepDone:
			if e.Step != cur || e.Share != 1 || e.Err != nil || e.Read < read {
				t.Errorf("done: %+v while %q ran", e, cur)
			}
			cur = ""
		case Listed:
			t.Errorf("the scopes were listed twice")
		}
	}
	if !reflect.DeepEqual(order, opt.Steps()) || cur != "" {
		t.Errorf("steps %v, want %v, and none left open (%q)", order, opt.Steps(), cur)
	}
	// 60 pull requests at 25 a page are three pages, and the runs come in one.
	if advanced[StepChanges] < 3 || advanced[StepCI] < 1 {
		t.Errorf("advances by step: %v", advanced)
	}

	// The store keeps how long the sync took, and that it read the whole
	// window. The next sync is told so, and a display takes its first
	// estimate from it.
	sc, _ := s.Scope(record.GitHub, "acme/app")
	if sc.Took <= 0 || !sc.TookFull {
		t.Errorf("stored: took %v, full %v", sc.Took, sc.TookFull)
	}
	opt.Now = now.Add(time.Hour)
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	ev = take()
	want := time.Duration(sc.Took * float64(time.Second))
	if e := ev[1]; e.Kind != ScopeStarted || e.First || e.Last != want || !e.LastFull || !e.LastAt.Equal(now) {
		t.Errorf("start of the second sync: %+v, want the %v of the first", e, want)
	}
	if e := ev[len(ev)-1]; e.Kind != ScopeDone || e.Repo == nil || e.Repo.Changes.Added != 0 {
		t.Errorf("end of the second sync: %+v", e)
	}
	// A sync that only read what is new says so: its time is no measure of a
	// whole read.
	if sc, _ = s.Scope(record.GitHub, "acme/app"); sc.Took <= 0 || sc.TookFull {
		t.Errorf("stored after the second sync: took %v, full %v", sc.Took, sc.TookFull)
	}
}

// The share of the pull request read is taken from the time it has reached.
// A repository whose pull requests all sit in the last days of the window
// would stand near zero until its last page. The count GitHub gives sets a
// floor under it.
func TestPullRequestShareCountsWhatWasRead(t *testing.T) {
	h := newHub(t)
	r := h.add("acme/app")
	for i := 1; i <= 60; i++ {
		r.pr(i, "MERGED", day(88), day(90+float64(i)/100), "s")
	}
	s := openStore(t, t.TempDir())
	opt := Options{PageSize: 20, SkipCI: true, SkipDeploys: true, SkipTickets: true}
	take := watch(&opt)
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme/app"); err != nil {
		t.Fatal(err)
	}
	var shares []float64
	for _, e := range take() {
		if e.Kind == StepAdvanced && e.Step == StepChanges {
			shares = append(shares, e.Share)
		}
	}
	// By time alone the first page stands at 3 days of 93: about 0.03.
	if len(shares) < 3 || shares[0] < 0.33 || shares[0] > 0.34 || shares[1] < 0.66 || shares[2] != 1 {
		t.Errorf("shares by page: %v, want a third, two thirds and all", shares)
	}
}

// Every scope of an owner keeps its slot from its first event to its last,
// so a display can follow several at once.
func TestScopesOfAnOwnerKeepTheirSlots(t *testing.T) {
	h := newHub(t)
	names := []string{"acme/api", "acme/app", "acme/site"}
	for i, n := range names {
		h.add(n).pr(i+1, "MERGED", day(10), day(11), "s")
	}
	s := openStore(t, t.TempDir())
	opt := Options{Workers: 2, SkipCI: true, SkipDeploys: true, SkipTickets: true}
	take := watch(&opt)
	if _, err := syncer(h, s, opt).Sync(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	ev := take()
	if e := ev[0]; e.Kind != Listed || e.Total != 3 || e.Scope != "acme" || len(e.Names) != 3 {
		t.Fatalf("first event: %+v", e)
	}
	listed := ev[0].Names
	started, done := map[int]bool{}, map[int]bool{}
	for _, e := range ev[1:] {
		if e.Slot < 0 || e.Slot >= 3 || e.Scope != listed[e.Slot] {
			t.Errorf("slot %d carries %q, listed as %v", e.Slot, e.Scope, listed)
		}
		switch e.Kind {
		case ScopeStarted:
			started[e.Slot] = true
		case ScopeDone:
			if !started[e.Slot] || done[e.Slot] || e.Repo == nil || e.Err != nil {
				t.Errorf("end of slot %d: %+v", e.Slot, e)
			}
			done[e.Slot] = true
		default:
			if !started[e.Slot] || done[e.Slot] {
				t.Errorf("slot %d told %v outside its sync", e.Slot, e.Kind)
			}
		}
	}
	if len(started) != 3 || len(done) != 3 {
		t.Errorf("started %v, done %v", started, done)
	}
}
