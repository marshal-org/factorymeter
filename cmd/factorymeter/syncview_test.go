package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/ingest"
	"github.com/marshal-org/factorymeter/pkg/store"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// hand is a clock a test moves by hand.
type hand struct {
	mu sync.Mutex
	at time.Time
}

func (h *hand) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.at
}

func (h *hand) add(d time.Duration) {
	h.mu.Lock()
	h.at = h.at.Add(d)
	h.mu.Unlock()
}

// screen is a small terminal. It knows the few codes the view writes, and it
// wraps a line that is wider than itself, as a terminal does.
type screen struct {
	mu     sync.Mutex
	w      int
	rows   [][]rune
	r, c   int
	hidden bool
	raw    strings.Builder
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.Write(p)
	rs := []rune(string(p))
	for i := 0; i < len(rs); i++ {
		switch ch := rs[i]; ch {
		case '\x1b':
			j := i + 2
			for j < len(rs) && (rs[j] < '@' || rs[j] > '~') {
				j++
			}
			arg := string(rs[i+2 : j])
			switch rs[j] {
			case 'A':
				n, _ := strconv.Atoi(arg)
				s.r = max(0, s.r-n)
			case 'K':
				s.row()
				s.rows[s.r] = nil
			case 'J':
				s.row()
				s.rows[s.r] = s.rows[s.r][:min(s.c, len(s.rows[s.r]))]
				s.rows = s.rows[:s.r+1]
			case 'l':
				s.hidden = true
			case 'h':
				s.hidden = false
			}
			i = j
		case '\r':
			s.c = 0
		case '\n':
			s.r, s.c = s.r+1, 0
		default:
			if s.c == s.w {
				s.r, s.c = s.r+1, 0
			}
			s.row()
			for len(s.rows[s.r]) <= s.c {
				s.rows[s.r] = append(s.rows[s.r], ' ')
			}
			s.rows[s.r][s.c] = ch
			s.c++
		}
	}
	return len(p), nil
}

func (s *screen) row() {
	for len(s.rows) <= s.r {
		s.rows = append(s.rows, nil)
	}
}

// lines is what the screen shows.
func (s *screen) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.rows))
	for i, r := range s.rows {
		out[i] = strings.TrimRight(string(r), " ")
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func viewOn(w, h int) (*syncView, *screen, *hand) {
	sc, clock := &screen{w: w}, &hand{at: t0}
	v := newSyncView(sc, style{}, func() (int, int) { return w, h })
	v.clock = clock.now
	return v, sc, clock
}

// open sets a phase up as begin does, without the goroutine that draws it.
func open(v *syncView, p *phase) *phase {
	p.began, p.scopes = v.clock(), map[int]*scopeView{}
	if p.single {
		p.total, p.listed = 1, true
	}
	v.ph = p
	return p
}

// tick draws the screen again, as the view does ten times a second.
func tick(v *syncView) {
	v.mu.Lock()
	v.tick++
	v.draw()
	v.mu.Unlock()
}

var allSteps = []ingest.Step{ingest.StepChanges, ingest.StepIssues, ingest.StepCI, ingest.StepDeploys}

func same(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("screen:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFrameOfOneRepository(t *testing.T) {
	v, _, clock := viewOn(80, 24)
	p := open(v, &phase{system: "GitHub", target: "acme/app", single: true, workers: 4, steps: allSteps})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Scope: "acme/app", First: true})
	v.event(ingest.Event{Kind: ingest.StepStarted, Scope: "acme/app", Step: ingest.StepChanges})

	// Nothing tells yet: no number is made up.
	clock.add(time.Second)
	same(t, p.frame(v.st, clock.now(), 80, 24, 0, 1), []string{
		"  acme/app",
		"  ⠋ pull requests  reading                                                  1s",
		"  ○ issues",
		"  ○ CI runs",
		"  ○ deploys",
		"",
		"  ────────────────────────  0% · 1s · 1 request",
	})
	clock.add(2 * time.Second)
	if f := p.footer(v.st, clock.now(), 2, 80); f != "  ────────────────────────  0% · 3s · estimating the time left · 2 requests" {
		t.Errorf("footer before the first share: %q", f)
	}

	// Half of the pull requests in 10s. They are 5 parts of 10.5, so about a
	// quarter of the work is done and three times that is left.
	clock.add(7 * time.Second)
	v.event(ingest.Event{Kind: ingest.StepAdvanced, Scope: "acme/app", Step: ingest.StepChanges, Read: 1200, Share: 0.5, At: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
	same(t, p.frame(v.st, clock.now(), 80, 24, 1, 7), []string{
		"  acme/app",
		"  ⠙ pull requests  1,200 read · now at 2026-09-01                          10s",
		"  ○ issues",
		"  ○ CI runs",
		"  ○ deploys",
		"",
		"  ━━━━━━──────────────────  23% · 10s · about 30s left · 7 requests",
	})

	v.event(ingest.Event{Kind: ingest.StepDone, Scope: "acme/app", Step: ingest.StepChanges, Read: 2400, Added: 2300, Changed: 4, Took: 21 * time.Second})
	v.event(ingest.Event{Kind: ingest.StepStarted, Scope: "acme/app", Step: ingest.StepIssues})
	v.event(ingest.Event{Kind: ingest.StepDone, Scope: "acme/app", Step: ingest.StepIssues, Took: 2 * time.Second})
	v.event(ingest.Event{Kind: ingest.StepStarted, Scope: "acme/app", Step: ingest.StepCI})
	v.event(ingest.Event{Kind: ingest.StepDone, Scope: "acme/app", Step: ingest.StepCI, Took: 9 * time.Second, Err: errors.New("no access")})
	v.event(ingest.Event{Kind: ingest.StepStarted, Scope: "acme/app", Step: ingest.StepDeploys})
	v.event(ingest.Event{Kind: ingest.StepDone, Scope: "acme/app", Step: ingest.StepDeploys, Took: time.Second, Err: context.Canceled})
	v.event(ingest.Event{Kind: ingest.ScopeDone, Scope: "acme/app", Repo: &ingest.RepoResult{Notes: []string{"A limit."}}})
	same(t, p.kept(v.st, clock.now(), 80), []string{
		"  acme/app",
		"  ✓ pull requests  2,300 new, 4 updated                                    21s",
		"  ✓ issues         nothing new                                              2s",
		"  ! CI runs        not read                                                 9s",
		"  ! deploys        stopped                                                  1s",
		"  ! A limit.",
	})
}

func TestEstimateOfOneRepository(t *testing.T) {
	v, _, clock := viewOn(80, 24)
	p := open(v, &phase{system: "GitHub", target: "acme/app", single: true, workers: 4, steps: allSteps})

	// A repeat sync goes by the length of the last one until a step tells.
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Scope: "acme/app", Last: 40 * time.Second})
	clock.add(10 * time.Second)
	if left, ok := p.remaining(clock.now()); !ok || left != 30*time.Second {
		t.Errorf("going by the last sync: %v %v", left, ok)
	}
	if f := p.progress(clock.now()); f != 0.25 {
		t.Errorf("progress going by the last sync: %v", f)
	}
	// Past that length there is nothing to go by.
	clock.add(40 * time.Second)
	if _, ok := p.remaining(clock.now()); ok {
		t.Error("an estimate past the length of the last sync")
	}

	// The last sync read the whole window, or was the first: it tells nothing
	// about this one.
	for _, e := range []ingest.Event{
		{Kind: ingest.ScopeStarted, Scope: "acme/app", Last: 40 * time.Second, LastFull: true},
		{Kind: ingest.ScopeStarted, Scope: "acme/app", Last: 40 * time.Second, First: true},
	} {
		q := open(v, &phase{system: "GitHub", target: "acme/app", single: true, workers: 4, steps: allSteps})
		v.event(e)
		if q.expect != 0 {
			t.Errorf("%+v sets an expectation of %v", e, q.expect)
		}
	}
}

func TestBarNeverGoesBack(t *testing.T) {
	v, _, clock := viewOn(80, 24)
	p := open(v, &phase{system: "GitHub", target: "acme/app", single: true, workers: 4, steps: []ingest.Step{ingest.StepCI}})
	v.event(ingest.Event{Kind: ingest.StepStarted, Scope: "acme/app", Step: ingest.StepCI})
	clock.add(10 * time.Second)
	v.event(ingest.Event{Kind: ingest.StepAdvanced, Scope: "acme/app", Step: ingest.StepCI, Share: 0.8})
	before := p.progress(clock.now())
	if before < 0.79 || before > 0.81 {
		t.Fatalf("progress: %v", before)
	}
	// The step finds more runs than it knew of, and its share drops.
	clock.add(time.Second)
	v.event(ingest.Event{Kind: ingest.StepAdvanced, Scope: "acme/app", Step: ingest.StepCI, Share: 0.3})
	if left, _ := p.remaining(clock.now()); left < 25*time.Second {
		t.Errorf("the time left did not grow with the work: %v", left)
	}
	if after := p.progress(clock.now()); after != before {
		t.Errorf("the bar went from %v to %v", before, after)
	}
}

// owner opens a phase over n repositories and finishes the first done of
// them, each after took.
func owner(v *syncView, clock *hand, n, done int, took time.Duration) *phase {
	p := open(v, &phase{system: "GitHub", target: "acme", workers: 4, steps: allSteps})
	names := make([]string, n)
	for i := range names {
		names[i] = "acme/r" + strconv.Itoa(i)
	}
	v.event(ingest.Event{Kind: ingest.Listed, Total: n, Names: names})
	for i := 0; i < done; i++ {
		v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: i, Scope: names[i]})
		clock.add(took)
		v.event(ingest.Event{Kind: ingest.ScopeDone, Slot: i, Scope: names[i], Repo: &ingest.RepoResult{}, Took: took})
	}
	return p
}

func TestEstimateOfAnOwnerCountsTheWorkers(t *testing.T) {
	// 4 of 13 are done at 10s each. 4 have just started and 5 wait: 4 x 9s
	// and 5 x 10s of work for 4 workers.
	v, _, clock := viewOn(120, 40)
	p := owner(v, clock, 13, 4, 10*time.Second)
	for i := 4; i < 8; i++ {
		v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: i, Scope: "acme/r" + strconv.Itoa(i)})
	}
	clock.add(time.Second)
	if left, ok := p.remaining(clock.now()); !ok || left != 21500*time.Millisecond {
		t.Errorf("time left: %v %v", left, ok)
	}

	// 12 of 13 are done and the last has just started. By the count of
	// repositories that is 92%. By the clock one more of them is left.
	v, _, clock = viewOn(120, 40)
	p = owner(v, clock, 13, 12, 10*time.Second)
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 12, Scope: "acme/r12"})
	clock.add(time.Second)
	if left, ok := p.remaining(clock.now()); !ok || left != 9*time.Second {
		t.Errorf("time left of the last repository: %v %v", left, ok)
	}
	if f := p.footer(v.st, clock.now(), 170, 120); f != "  ━━━━━━━━━━━━━━━━━━━━━━──  12 of 13 repositories · 93% · 2m 01s · about 10s left · 170 requests" {
		t.Errorf("footer: %q", f)
	}

	// A repository that is under way tells by its own pace, and the phase
	// cannot end before the slowest one does.
	v.event(ingest.Event{Kind: ingest.StepStarted, Slot: 12, Scope: "acme/r12", Step: ingest.StepChanges})
	clock.add(20 * time.Second)
	v.event(ingest.Event{Kind: ingest.StepAdvanced, Slot: 12, Scope: "acme/r12", Step: ingest.StepChanges, Share: 0.42})
	// 0.42 of 5 parts in 10.5 is a fifth of the repository, in 21s.
	if left, _ := p.remaining(clock.now()); left != 84*time.Second {
		t.Errorf("time left by the pace of the repository: %v", left)
	}
}

func TestFrameOfAnOwner(t *testing.T) {
	v, _, clock := viewOn(120, 40)
	p := open(v, &phase{system: "GitHub", target: "acme", workers: 4, steps: allSteps})
	same(t, p.frame(v.st, clock.now(), 120, 40, 0, 0), []string{
		"  ⠋ Listing the repositories of acme",
		"",
		"  ────────────────────────  0% · 0s",
	})
	names := []string{"acme/app", "acme/infrastructure", "acme/docs"}
	v.event(ingest.Event{Kind: ingest.Listed, Total: 3, Names: names})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 0, Scope: "acme/app"})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 1, Scope: "acme/infrastructure"})
	v.event(ingest.Event{Kind: ingest.StepStarted, Slot: 1, Scope: "acme/infrastructure", Step: ingest.StepCI})
	clock.add(4 * time.Second)
	v.event(ingest.Event{Kind: ingest.StepAdvanced, Slot: 1, Scope: "acme/infrastructure", Step: ingest.StepCI, Read: 300, At: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)})
	same(t, p.frame(v.st, clock.now(), 120, 40, 0, 12), []string{
		"  ⠋ app             starting                                                                      4s",
		"  ⠋ infrastructure  CI runs · 300 read · now at 2026-10-30                                        4s",
		"",
		"  ────────────────────────  0 of 3 repositories · 0% · 4s · estimating the time left · 12 requests",
	})

	// A short terminal shows the first rows and counts the rest.
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 2, Scope: "acme/docs"})
	got := p.frame(v.st, clock.now(), 120, 5, 0, 12)
	if len(got) != 4 || got[1] != "    and 2 more" {
		t.Errorf("a frame of 5 rows: %q", got)
	}
	// No row of a frame is wider than the terminal leaves room for, and a
	// narrow terminal gets a footer with less in it.
	for _, w := range []int{60, 80, 120, 200} {
		for _, l := range p.frame(v.st, clock.now(), w, 40, 0, 12) {
			if visible(l) > w-1 {
				t.Errorf("width %d: a row of %d cells: %q", w, visible(l), l)
			}
		}
	}
	for w, want := range map[int]string{
		120: "  ────────────────────────  0 of 3 repositories · 0% · 4s · estimating the time left · 12 requests",
		90:  "  ────────────────────────  0 of 3 repositories · 0% · 4s · estimating the time left",
		60:  "  ────────────  0 of 3 · 0% · 4s · estimating the time left",
	} {
		if got := p.footer(v.st, clock.now(), 12, w); got != want {
			t.Errorf("footer at width %d: %q", w, got)
		}
	}
	// A row that is cut says so: the date is not shown by half.
	if got := p.frame(v.st, clock.now(), 48, 40, 0, 12)[1]; got != "  ⠋ infrastructure  CI runs · 300 read · …   4s" {
		t.Errorf("a cut row: %q", got)
	}
}

func TestScreenOfAnOwnerSync(t *testing.T) {
	const width = 60
	v, sc, clock := viewOn(width, 24)
	v.say("", "  factorymeter", "")
	names := []string{"acme/app", "acme/infrastructure", "acme/docs", "acme/site"}
	v.begin(&phase{system: "GitHub", target: "acme", workers: 2, steps: allSteps}, func() int { return 9 })
	v.event(ingest.Event{Kind: ingest.Listed, Total: 4, Names: names})
	for slot := 0; slot < 2; slot++ {
		v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: slot, Scope: names[slot]})
		v.event(ingest.Event{Kind: ingest.StepStarted, Slot: slot, Scope: names[slot], Step: ingest.StepChanges})
	}
	tick(v) // draws the two rows of the repositories that are under way
	clock.add(8 * time.Second)

	// A line that stays and is wider than the terminal comes in while the
	// rows under it are on the screen.
	v.event(ingest.Event{Kind: ingest.Noted, Slot: 1, Scope: names[1], Step: ingest.StepChanges, Detail: "github timed out on a page, retrying with 13 per page"})
	v.event(ingest.Event{Kind: ingest.ScopeDone, Slot: 0, Scope: names[0], Took: 8 * time.Second,
		Repo: &ingest.RepoResult{Changes: ingest.Count{Added: 1200, Changed: 3}, CIRuns: ingest.Count{Added: 319}, Deploys: ingest.Count{Added: 14}}})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 2, Scope: names[2]})
	tick(v)
	clock.add(4 * time.Second)
	v.event(ingest.Event{Kind: ingest.ScopeDone, Slot: 1, Scope: names[1], Took: 12 * time.Second,
		Err: errors.New("pull requests: github server error: something went wrong while the query ran")})
	v.event(ingest.Event{Kind: ingest.ScopeDone, Slot: 2, Scope: names[2], Took: 4 * time.Second, Repo: &ingest.RepoResult{}})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 3, Scope: names[3]})
	v.event(ingest.Event{Kind: ingest.ScopeDone, Slot: 3, Scope: names[3], Took: time.Second,
		Repo: &ingest.RepoResult{Tickets: ingest.Count{Changed: 2}, Notes: []string{"The CI runs of this repository could not be read with this token."}}})
	v.end()
	if sc.hidden {
		t.Error("the cursor stays hidden after the phase")
	}
	v.githubDone("acme", ingest.Result{Repos: []ingest.RepoResult{
		{Repo: names[0], Changes: ingest.Count{Added: 1200, Changed: 3, Total: 1200}, CIRuns: ingest.Count{Added: 319, Total: 319}, Deploys: ingest.Count{Added: 14, Total: 14}, From: t0.AddDate(0, 0, -7)},
		{Repo: names[1], Err: errors.New("github server error")},
		{Repo: names[2], Changes: ingest.Count{Total: 40}, From: t0.AddDate(0, 0, -6)},
		{Repo: names[3], Tickets: ingest.Count{Changed: 2, Total: 12}},
	}}, false, 172, 96*time.Second)
	v.next("acme")

	// Nothing of the rows that were drawn again and again is left: no
	// spinner, no bar, no row cut in two by the terminal.
	same(t, sc.lines(), []string{
		"",
		"  factorymeter",
		"",
		"  • infrastructure: github timed out on a page, retrying",
		"      with 13 per page",
		"  ✓ app             PRs +1,200, 3 updated · CI runs +319 ·",
		"      deploys +14  8s",
		"  ✗ infrastructure  failed: pull requests: github server",
		"      error: something went wrong while the query ran",
		"  ✓ site            issues 2 updated                     1s",
		"  ! site: The CI runs of this repository could not be read",
		"      with this token.",
		"",
		"  ! Synced 3 of 4 repositories of acme in 1m 36s · 172",
		"      requests to GitHub",
		"    2 had something new, 1 had nothing new.",
		"    1 repository could not be read. Run sync again to read",
		"      what is missing.",
		"    In the store: pull requests 1,240 · issues 12 · CI runs",
		"      319 · deploys 14 · from 2026-10-26",
		"",
		"  Next",
		"    factorymeter serve acme   the line as a page in the",
		"      browser",
		"    factorymeter report acme  the line here in the terminal",
	})
	for i, l := range sc.lines() {
		if len([]rune(l)) > width-1 {
			t.Errorf("row %d takes %d cells of %d: %q", i, len([]rune(l)), width, l)
		}
	}
	raw := sc.raw.String()
	if strings.Count(raw, "\x1b[?25l") != 1 || strings.Count(raw, "\x1b[?25h") != 1 {
		t.Errorf("the cursor was hidden %d times and shown %d times", strings.Count(raw, "\x1b[?25l"), strings.Count(raw, "\x1b[?25h"))
	}
}

func TestViewIsOptional(t *testing.T) {
	// A sync that draws nothing holds no view. Every call of the command on
	// it must be safe.
	var v *syncView
	v.say("x")
	v.begin(&phase{}, nil)
	v.end()
	v.githubDone("acme/app", ingest.Result{}, false, 0, 0)
	v.sourceDone("Linear", "teams", true, ingest.SourceResult{}, nil, 0, 0)
	v.next("acme/app")

	// end without a phase writes nothing and does not block.
	w, sc, _ := viewOn(80, 24)
	w.end()
	if sc.raw.Len() != 0 {
		t.Errorf("end without a phase wrote %q", sc.raw.String())
	}
}

func TestHowASyncEnds(t *testing.T) {
	end := func(target string, res ingest.Result, halted bool) []string {
		v, sc, _ := viewOn(100, 24)
		v.githubDone(target, res, halted, 1, 75*time.Second)
		return sc.lines()
	}
	fresh := ingest.RepoResult{Repo: "acme/app", Changes: ingest.Count{Added: 2, Total: 1402}, Tickets: ingest.Count{Total: 80}, From: t0.AddDate(0, 0, -182)}
	same(t, end("acme/app", ingest.Result{Repos: []ingest.RepoResult{fresh}}, false), []string{
		"",
		"  ✓ Synced acme/app in 1m 15s · 1 request to GitHub",
		"    In the store: pull requests 1,402 · issues 80 · CI runs 0 · deploys 0 · from 2026-05-04",
	})
	fresh.Changes.Added = 0
	same(t, end("acme/app", ingest.Result{Repos: []ingest.RepoResult{fresh}}, false), []string{
		"",
		"  ✓ acme/app is up to date · 1m 15s · 1 request to GitHub",
		"    In the store: pull requests 1,402 · issues 80 · CI runs 0 · deploys 0 · from 2026-05-04",
	})
	same(t, end("acme/app", ingest.Result{Repos: []ingest.RepoResult{{Repo: "acme/app", Err: errors.New("no")}}}, false), []string{
		"",
		"  ✗ acme/app could not be read.",
	})
	same(t, end("acme", ingest.Result{Repos: []ingest.RepoResult{fresh, fresh}}, false), []string{
		"",
		"  ✓ Synced 2 repositories of acme in 1m 15s · 1 request to GitHub",
		"    0 had something new, 2 had nothing new.",
		"    In the store: pull requests 2,804 · issues 160 · CI runs 0 · deploys 0 · from 2026-05-04",
	})
	same(t, end("acme/app", ingest.Result{Repos: []ingest.RepoResult{fresh}}, true), []string{
		"",
		"  ! Stopped after 1m 15s. What was read is stored, and the next sync goes on from there.",
	})
	if got := end("acme", ingest.Result{}, false); len(got) != 0 {
		t.Errorf("an owner with no repository to read: %q", got)
	}

	v, sc, _ := viewOn(100, 24)
	v.sourceDone("Linear", "teams", true, ingest.SourceResult{Name: "acme", Tickets: ingest.Count{Added: 5, Total: 900}, Links: ingest.Count{Total: 40}, From: t0.AddDate(0, 0, -30), Again: true}, nil, 12, 8*time.Second)
	v.sourceDone("Jira", "projects", false, ingest.SourceResult{Name: "acme.atlassian.net", Tickets: ingest.Count{Total: 7}}, nil, 3, time.Second)
	v.sourceDone("Jira", "projects", false, ingest.SourceResult{}, errors.New("no"), 1, time.Second)
	v.sourceDone("Jira", "projects", false, ingest.SourceResult{}, context.Canceled, 1, time.Second)
	same(t, sc.lines(), []string{
		"",
		"  ✓ Synced Linear acme in 8s · 12 requests to Linear",
		"  ! The list of teams changed, so the tickets were read again from the start.",
		"    In the store: tickets 900 · links to pull requests 40 · from 2026-10-03",
		"",
		"",
		"  ✓ Jira acme.atlassian.net is up to date · 1s · 3 requests to Jira",
		"    In the store: tickets 7",
		"",
		"",
		"  ✗ Jira could not be read.",
		"",
		"",
		"  ! Stopped after 1s. What was read is stored, and the next sync goes on from there.",
	})
}

func TestHeaderSaysWhatTheSyncWillDo(t *testing.T) {
	plan := syncPlan{
		target: "acme/app", now: t0, since: t0.AddDate(0, 0, -182), steps: allSteps, workers: 4,
		store: "/var/lib/factorymeter",
	}
	same(t, plan.header(style{}, 80), []string{
		"",
		"  factorymeter dev",
		"",
		"  Welcome to factorymeter. This run reads the history of acme/app from GitHub",
		"  into a store on this machine. It only reads, and what it reads stays here.",
		"",
		"  Target   acme/app (one repository)",
		"  Window   26 weeks, back to 2026-05-04",
		"  Reads    pull requests, issues, CI runs, deploys",
		"  Store    /var/lib/factorymeter",
		"  Time     a first sync reads the whole window, so it is the longest one. An",
		"           estimate shows once it is under way.",
		"",
	})
	if got := plan.line(); got != "reading pull requests, issues, CI runs, deploys of acme/app. History goes back to 2026-05-04." {
		t.Errorf("the plain line: %q", got)
	}

	// The store knows the repository: the header says when it was read last
	// and how long this sync may take.
	plan.known = []store.Scope{{Name: "Acme/App", SyncedAt: t0.Add(-3 * time.Hour), Took: 42}}
	plan.linear, plan.teams, plan.jira, plan.projects = true, []string{"ENG", "OPS"}, "acme.atlassian.net", []string{"WEB"}
	same(t, plan.header(style{}, 80), []string{
		"",
		"  factorymeter dev",
		"",
		"  Welcome back. The last sync of acme/app was 3 hours ago.",
		"",
		"  Target   acme/app (one repository)",
		"  Window   what changed since the last sync. History goes back to 2026-05-04",
		"  Reads    pull requests, issues, CI runs, deploys",
		"           Linear tickets of ENG, OPS",
		"           Jira tickets of acme.atlassian.net, WEB",
		"  Store    /var/lib/factorymeter",
		"  Time     about 40s, going by the last sync",
		"",
	})
	if text, took := plan.timing(); took != 42*time.Second || text != "about 40s, going by the last sync" {
		t.Errorf("timing: %q %v", text, took)
	}

	// The last sync read the whole window: this one is quicker, by an
	// amount nobody knows.
	plan.known[0].TookFull = true
	if text, took := plan.timing(); took != 0 || text != "the last sync read the whole window in 42s. This one reads only what changed, so it is quicker." {
		t.Errorf("timing after a full sync: %q %v", text, took)
	}
	// A store of an older version does not know how long a sync took.
	plan.known[0].Took = 0
	if text, took := plan.timing(); took != 0 || text != "reads only what changed since the last sync. An estimate shows once it is under way." {
		t.Errorf("timing without a length: %q %v", text, took)
	}

	// An owner: the repositories are read a few at a time, so their lengths
	// are shared out over the workers.
	owner := syncPlan{target: "acme", now: t0, since: t0.AddDate(0, 0, -7), steps: allSteps[:1], workers: 4, store: "/s",
		known: []store.Scope{
			{Name: "acme/a", SyncedAt: t0.Add(-26 * time.Hour), Took: 60},
			{Name: "acme/b", SyncedAt: t0.Add(-50 * time.Hour), Took: 20},
			{Name: "other/never", Took: 500},
		}}
	if text, took := owner.timing(); took != 40*time.Second || text != "about 40s, going by the last sync" {
		t.Errorf("timing of an owner: %q %v", text, took)
	}
	got := owner.header(style{}, 80)
	for _, want := range []string{
		"  Welcome back. The last sync of acme was 26 hours ago.",
		"  Target   acme (every repository with a push in the window)",
		"  Reads    pull requests",
	} {
		if !strings.Contains(strings.Join(got, "\n"), want+"\n") {
			t.Errorf("header of an owner lacks %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
	// A store that holds other repositories only.
	owner.target = "new/repo"
	if got := owner.header(style{}, 80)[3]; got != "  Welcome back. new/repo is new to this store." {
		t.Errorf("welcome to a known store: %q", got)
	}

	// The header fits a narrow terminal, and a wide one does not stretch it.
	for _, w := range []int{40, 60, 80, 200} {
		for _, l := range plan.header(style{}, w) {
			if len([]rune(l)) > min(w-1, 78) {
				t.Errorf("width %d: a line of %d cells: %q", w, len([]rune(l)), l)
			}
		}
	}
	short := syncPlan{target: "acme/app", now: t0, since: t0.AddDate(0, 0, -3), steps: allSteps, store: "/s"}
	if got := short.header(style{}, 80)[7]; got != "  Window   back to 2026-10-30" {
		t.Errorf("a window under a week: %q", got)
	}
	short.since = t0.AddDate(0, 0, -7)
	if got := short.header(style{}, 80)[7]; got != "  Window   1 week, back to 2026-10-26" {
		t.Errorf("a window of a week: %q", got)
	}
}

func TestWordsForNumbersAndTimes(t *testing.T) {
	for got, want := range map[string]string{
		things(0, "request", "requests"):       "0 requests",
		things(1, "request", "requests"):       "1 request",
		things(1200, "repository", "x"):        "1,200 x",
		count(0):                               "0",
		count(999):                             "999",
		count(1000):                            "1,000",
		count(1234567):                         "1,234,567",
		clock(0):                               "0s",
		clock(59400 * time.Millisecond):        "59s",
		clock(59600 * time.Millisecond):        "1m 00s",
		clock(125 * time.Second):               "2m 05s",
		clock(3725 * time.Second):              "1h 02m",
		about(0):                               "a few seconds",
		about(4 * time.Second):                 "a few seconds",
		about(5 * time.Second):                 "about 5s",
		about(32 * time.Second):                "about 30s",
		about(57 * time.Second):                "about 55s",
		about(58 * time.Second):                "about 1m",
		about(599 * time.Second):               "about 10m",
		about(100 * time.Second):               "about 1m 45s",
		about(119 * time.Second):               "about 2m",
		about(25 * time.Minute):                "about 25m",
		about(59*time.Minute + 50*time.Second): "about 1h 00m",
		about(95 * time.Minute):                "about 1h 35m",
		ago(10 * time.Second):                  "a moment ago",
		ago(time.Minute):                       "1 minute ago",
		ago(59 * time.Minute):                  "59 minutes ago",
		ago(time.Hour):                         "1 hour ago",
		ago(47 * time.Hour):                    "47 hours ago",
		ago(72 * time.Hour):                    "3 days ago",
		news(0, 0):                             "nothing new",
		news(1200, 0):                          "1,200 new",
		news(0, 3):                             "3 updated",
		news(5, 3):                             "5 new, 3 updated",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if got := wrap("one two three four", 9); strings.Join(got, "|") != "one two|three|four" {
		t.Errorf("wrap: %q", got)
	}
	if got := wrap("", 9); len(got) != 1 || got[0] != "" {
		t.Errorf("wrap of nothing: %q", got)
	}
	if got := repoNews(style{}, ingest.RepoResult{}); got != "" {
		t.Errorf("news of a repository with nothing new: %q", got)
	}
}

// GitHub tells the client to wait. The screen says so and counts the wait
// down, the rows stop turning, and the wait is no part of the pace: the
// estimate is the work that is left, plus the wait that is left.
func TestAWaitForARateLimit(t *testing.T) {
	v, sc, clock := viewOn(100, 24)
	p := open(v, &phase{system: "GitHub", target: "acme/app", single: true, workers: 4, steps: allSteps})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Scope: "acme/app", First: true})
	v.event(ingest.Event{Kind: ingest.StepStarted, Scope: "acme/app", Step: ingest.StepChanges})
	clock.add(10 * time.Second)
	v.event(ingest.Event{Kind: ingest.StepAdvanced, Scope: "acme/app", Step: ingest.StepChanges, Read: 50, Share: 0.5})
	work, _ := p.remaining(clock.now())
	if work < 31*time.Second || work > 33*time.Second {
		t.Fatalf("the work left before the wait: %v", work)
	}

	v.waiting(time.Minute)
	tick(v)
	same(t, sc.lines(), []string{
		"  ! GitHub's rate limit is reached. The sync waits for it and retries by itself.",
		"  acme/app",
		"  ‖ pull requests  50 read                                                 10s",
		"  ○ issues",
		"  ○ CI runs",
		"  ○ deploys",
		"",
		"  ━━──────────────────────  9% · 10s · GitHub's rate limit: retrying in 1m 00s · about 1m 30s left",
	})

	// Half a minute into the wait a second read is told to wait too. The two
	// waits are one: no second line, and the count goes to the later end.
	clock.add(30 * time.Second)
	if left, _ := p.remaining(clock.now()); left != work+30*time.Second {
		t.Errorf("30s into the wait: %v left, want the work of %v and 30s of wait", left, work)
	}
	v.waiting(time.Minute)
	tick(v)
	got := sc.lines()
	if len(got) != 8 || !strings.Contains(got[7], "retrying in 1m 00s") || !strings.Contains(got[2], "‖ pull requests  50 read") {
		t.Errorf("screen during the second wait:\n%s", strings.Join(got, "\n"))
	}

	// The wait is over. The row turns again, and the 90s of waiting did not
	// make the read look slow: the work left is what it was.
	clock.add(time.Minute)
	if left, _ := p.remaining(clock.now()); left != work {
		t.Errorf("after the wait: %v left, want %v", left, work)
	}
	if idle := p.idle(clock.now()); idle != 90*time.Second {
		t.Errorf("time spent waiting: %v", idle)
	}
	// Five more seconds of reading with no news: 15s for a quarter of the
	// work, so about 48s for the rest.
	clock.add(5 * time.Second)
	tick(v)
	got = sc.lines()
	if strings.Contains(got[7], "rate limit") || strings.Contains(got[2], "‖") || !strings.HasSuffix(got[7], "1m 45s · about 50s left") {
		t.Errorf("screen after the wait:\n%s", strings.Join(got, "\n"))
	}

	// A third wait, later, is counted down again but not explained again.
	v.waiting(20 * time.Second)
	clock.add(20 * time.Second)
	tick(v)
	if got = sc.lines(); len(got) != 8 {
		t.Errorf("a later wait wrote a line that stays:\n%s", strings.Join(got, "\n"))
	}
	if idle := p.idle(clock.now()); idle != 110*time.Second {
		t.Errorf("time spent waiting, all in all: %v", idle)
	}
}

// A sync that had to wait says at the end how much of its time the wait
// was, once.
func TestTheEndTellsOfTheWait(t *testing.T) {
	v, sc, _ := viewOn(100, 24)
	v.waited = 6*time.Minute + 5*time.Second
	fresh := ingest.RepoResult{Repo: "acme/app", Changes: ingest.Count{Added: 2, Total: 1402}, From: t0.AddDate(0, 0, -182)}
	v.githubDone("acme/app", ingest.Result{Repos: []ingest.RepoResult{fresh}}, false, 40, 7*time.Minute)
	v.sourceDone("Linear", "teams", true, ingest.SourceResult{Name: "acme", Tickets: ingest.Count{Added: 5, Total: 900}}, nil, 12, 8*time.Second)
	same(t, sc.lines(), []string{
		"",
		"  ✓ Synced acme/app in 7m 00s · 40 requests to GitHub",
		"    6m 05s of that was a wait for GitHub's rate limit.",
		"    In the store: pull requests 1,402 · issues 0 · CI runs 0 · deploys 0 · from 2026-05-04",
		"",
		"",
		"  ✓ Synced Linear acme in 8s · 12 requests to Linear",
		"    In the store: tickets 900 · links to pull requests 0",
	})
}

// A scope of an owner that starts after a wait is not charged for it.
func TestAWaitIsNoPartOfThePaceOfAnOwner(t *testing.T) {
	v, _, clock := viewOn(120, 24)
	p := open(v, &phase{system: "GitHub", target: "acme", workers: 1, steps: []ingest.Step{ingest.StepChanges}})
	v.event(ingest.Event{Kind: ingest.Listed, Total: 3, Names: []string{"acme/a", "acme/b", "acme/c"}})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 0, Scope: "acme/a", First: true})
	clock.add(4 * time.Second)
	v.waiting(time.Minute)
	clock.add(time.Minute)
	clock.add(6 * time.Second)
	v.event(ingest.Event{Kind: ingest.ScopeDone, Slot: 0, Scope: "acme/a", Repo: &ingest.RepoResult{Repo: "acme/a"}})
	if p.doneTook != 10*time.Second {
		t.Errorf("the first repository took %v of reading, want 10s", p.doneTook)
	}
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 1, Scope: "acme/b", First: true})
	if left, ok := p.remaining(clock.now()); !ok || left != 20*time.Second {
		t.Errorf("two repositories of 10s each are left: %v %v", left, ok)
	}
}

// An error the screen has told is still an error, and still the one it was.
func TestAToldErrorKeepsItsCause(t *testing.T) {
	err := error(toldErr{context.Canceled})
	if !errors.Is(err, context.Canceled) || !errors.As(err, new(toldErr)) {
		t.Errorf("a told error lost its cause: %v", err)
	}
	if errors.As(errors.New("no"), new(toldErr)) {
		t.Error("a plain error reads as told")
	}
}

// A sync that outruns the time of the last one says so. It does not go on
// to say that it is estimating.
func TestASyncThatOutrunsTheLastOne(t *testing.T) {
	v, scr, clock := viewOn(100, 24)
	open(v, &phase{system: "GitHub", target: "acme/app", single: true, workers: 1, expect: 5 * time.Second, steps: []ingest.Step{ingest.StepChanges}})
	v.event(ingest.Event{Kind: ingest.ScopeStarted, Slot: 0, Scope: "acme/app"})
	v.event(ingest.Event{Kind: ingest.StepStarted, Slot: 0, Scope: "acme/app", Step: ingest.StepChanges})
	clock.add(3 * time.Second)
	tick(v)
	if last := scr.lines()[len(scr.lines())-1]; !strings.Contains(last, "a few seconds left") {
		t.Errorf("inside the time of the last sync: %q", last)
	}
	clock.add(30 * time.Second)
	tick(v)
	last := scr.lines()[len(scr.lines())-1]
	if !strings.Contains(last, "taking longer than the last sync") || strings.Contains(last, "estimating") {
		t.Errorf("past the time of the last sync: %q", last)
	}
}
