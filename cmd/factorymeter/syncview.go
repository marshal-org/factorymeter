package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marshal-org/factorymeter/pkg/ingest"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// The states of a step on the screen.
const (
	stepWaiting = iota
	stepRunning
	stepDone
	stepFailed
)

type stepView struct {
	state          int
	began          time.Time
	took           time.Duration
	read           int
	share          float64
	at             time.Time
	added, changed int
	err            error
}

type scopeView struct {
	name  string
	began time.Time
	// idle is the time the phase had spent waiting when the scope began.
	idle  time.Duration
	step  ingest.Step
	steps map[ingest.Step]*stepView
}

// phase is the read of one system: GitHub, then Linear, then Jira.
type phase struct {
	system string
	// target is what is read: a repository or an owner. A ticket system
	// learns its name on the way.
	target string
	// single is set when the phase reads one scope. It is then drawn step by
	// step. Else it is drawn scope by scope.
	single bool
	steps  []ingest.Step
	// expect is how long the phase may take, going by the last sync. Zero
	// when the store cannot tell.
	expect time.Duration
	// workers is the number of scopes read at the same time.
	workers int

	began    time.Time
	listed   bool
	total    int
	nameW    int
	scopes   map[int]*scopeView
	active   []int
	finished int
	// doneTook is the time the finished scopes took, all in all.
	doneTook time.Duration
	// shown is the share the bar last showed. The bar never goes back.
	shown float64
	// after are lines that follow the steps once the phase ends.
	after []string

	left    time.Duration
	leftAt  time.Time
	hasLeft bool

	// A wait for a rate limit runs from waitFrom to waitTo. waited is the
	// time the waits before it took.
	waitFrom, waitTo time.Time
	waited           time.Duration
}

// hold takes the news that the client waits until a time, for a rate limit.
// Several scopes can wait at once. Their waits count as one.
func (p *phase) hold(now, until time.Time) {
	if now.Before(p.waitTo) {
		if until.After(p.waitTo) {
			p.waitTo = until
		}
		return
	}
	p.waited += p.waitTo.Sub(p.waitFrom)
	p.waitFrom, p.waitTo = now, until
}

// idle is the time the phase has spent waiting so far.
func (p *phase) idle(now time.Time) time.Duration {
	end := p.waitTo
	if now.Before(end) {
		end = now
	}
	return p.waited + max(0, end.Sub(p.waitFrom))
}

// held is how long the wait that runs now still takes. Zero when none runs.
func (p *phase) held(now time.Time) time.Duration { return max(0, p.waitTo.Sub(now)) }

// mark is the sign in front of a row that is under way.
func (p *phase) mark(st style, now time.Time, tick int) string {
	if p.held(now) > 0 {
		return st.held()
	}
	return st.spin(tick)
}

// stepWeight is the share of the time of a sync a step tends to take. It
// only shapes the estimate of the time left.
var stepWeight = map[ingest.Step]float64{
	ingest.StepChanges: 5,
	ingest.StepIssues:  1,
	ingest.StepCI:      4,
	ingest.StepDeploys: 0.5,
	ingest.StepTickets: 1,
}

func (p *phase) scopeShare(s *scopeView) float64 {
	var sum, all float64
	for _, step := range p.steps {
		w := stepWeight[step]
		all += w
		sv := s.steps[step]
		if sv == nil {
			continue
		}
		switch sv.state {
		case stepDone, stepFailed:
			sum += w
		case stepRunning:
			sum += w * sv.share
		}
	}
	if all == 0 {
		return 0
	}
	return sum / all
}

// share is how much of the phase is done, from 0 to 1.
func (p *phase) share() float64 {
	if p.total == 0 {
		return 0
	}
	f := float64(p.finished)
	for _, slot := range p.active {
		f += p.scopeShare(p.scopes[slot])
	}
	return min(1, f/float64(p.total))
}

// remaining estimates the time left.
//
// A scope that is under way tells by its own pace: the time it took so far,
// and the share of its work that is done. A scope that has not told enough
// yet, or has not started, is taken to be as long as a typical one of this
// run. The scopes are read a few at a time, so the time left is the work
// left divided over the workers, and never less than the longest scope
// still needs. Before any scope tells, the length of the last sync stands in.
//
// The pace is taken from the time spent reading. A wait for a rate limit is
// no work, so it is left out of the pace, and the part of it still to come
// is added to the time left.
func (p *phase) remaining(now time.Time) (time.Duration, bool) {
	idle, held := p.idle(now), p.held(now)
	elapsed := now.Sub(p.began) - idle
	var sum, longest, whole time.Duration
	var known int
	var young []time.Duration
	for _, slot := range p.active {
		s := p.scopes[slot]
		f, e := p.scopeShare(s), now.Sub(s.began)-(idle-s.idle)
		if f < 0.05 || e < 2*time.Second {
			young = append(young, e)
			continue
		}
		r := time.Duration(float64(e) * (1 - f) / f)
		sum, longest, whole, known = sum+r, max(longest, r), whole+e+r, known+1
	}
	var typical time.Duration
	switch {
	case p.finished > 0:
		typical = p.doneTook / time.Duration(p.finished)
	case known > 0:
		typical = whole / time.Duration(known)
	default:
		if p.expect > elapsed {
			return p.expect - elapsed + held, true
		}
		return 0, false
	}
	for _, e := range young {
		sum += max(0, typical-e)
	}
	waiting := max(0, p.total-p.finished-len(p.active))
	sum += time.Duration(waiting) * typical
	lanes := max(1, min(p.workers, p.total-p.finished))
	return max(longest, sum/time.Duration(lanes)) + held, true
}

// progress is the share of the time of the phase that has passed, going by
// the estimate of the time left. It never goes back.
func (p *phase) progress(now time.Time) float64 {
	f := p.share()
	if left, ok := p.remaining(now); ok {
		if elapsed := now.Sub(p.began); elapsed+left > 0 {
			f = float64(elapsed) / float64(elapsed+left)
		}
	}
	p.shown = max(p.shown, min(1, f))
	return p.shown
}

// eta is remaining, smoothed so the number on the screen does not jump with
// every page that comes in.
func (p *phase) eta(now time.Time) (time.Duration, bool) {
	raw, ok := p.remaining(now)
	if !ok {
		p.hasLeft = false
		return 0, false
	}
	if !p.hasLeft {
		p.left, p.leftAt, p.hasLeft = raw, now, true
		return raw, true
	}
	dt := now.Sub(p.leftAt)
	was := max(0, p.left-dt)
	pull := min(1, dt.Seconds()/3)
	p.left, p.leftAt = was+time.Duration(float64(raw-was)*pull), now
	return p.left, true
}

func (p *phase) scope(e ingest.Event, now time.Time) *scopeView {
	s := p.scopes[e.Slot]
	if s == nil {
		s = &scopeView{began: now, idle: p.idle(now), steps: map[ingest.Step]*stepView{}}
		p.scopes[e.Slot] = s
		p.active = append(p.active, e.Slot)
	}
	if e.Scope != "" {
		s.name = e.Scope
	}
	return s
}

// short is the name of a scope as a row shows it: a repository of an owner
// without the owner, which every row would repeat.
func (p *phase) short(name string) string {
	if !p.single {
		return strings.TrimPrefix(name, p.target+"/")
	}
	return name
}

func (p *phase) title() string {
	if p.system == "GitHub" {
		return p.target
	}
	if s := p.scopes[0]; s != nil && s.name != "" {
		return p.system + " " + s.name
	}
	return p.system
}

// rowWidth is the width of the rows of the phase. The rows of an owner hold
// a name and what was read, so they may be wider.
func (p *phase) rowWidth(width int) int {
	if p.single {
		return min(width-1, 78)
	}
	return min(width-1, 100)
}

// row puts a text at the left and a short one at the right of a line.
func row(st style, left, right string, width int) string {
	room := width - visible(right) - 2
	return pad(clip(left, room, st.more()), room) + "  " + right
}

func news(added, changed int) string {
	switch {
	case added > 0 && changed > 0:
		return fmt.Sprintf("%s new, %s updated", count(int64(added)), count(int64(changed)))
	case added > 0:
		return count(int64(added)) + " new"
	case changed > 0:
		return count(int64(changed)) + " updated"
	}
	return "nothing new"
}

func (p *phase) stepRow(st style, s *scopeView, step ingest.Step, now time.Time, width, tick int) string {
	label := pad(string(step), 15)
	var sv *stepView
	if s != nil {
		sv = s.steps[step]
	}
	if sv == nil {
		return "  " + st.todo() + " " + st.dim(strings.TrimRight(label, " "))
	}
	switch sv.state {
	case stepRunning:
		var parts []string
		if sv.read > 0 {
			parts = append(parts, count(int64(sv.read))+" read")
		}
		if !sv.at.IsZero() {
			parts = append(parts, "now at "+sv.at.Format("2006-01-02"))
		}
		if len(parts) == 0 {
			parts = []string{"reading"}
		}
		return row(st, "  "+p.mark(st, now, tick)+" "+label+strings.Join(parts, st.sep()), st.dim(clock(now.Sub(sv.began))), width)
	case stepFailed:
		why := "not read"
		if stopped(sv.err) {
			why = "stopped"
		}
		return row(st, "  "+st.warn()+" "+label+st.yellow(why), st.dim(clock(sv.took)), width)
	}
	return row(st, "  "+st.ok()+" "+label+news(sv.added, sv.changed), st.dim(clock(sv.took)), width)
}

// footer is the line under the rows: a bar, and how far the phase is. While
// the client waits for a rate limit it says so, and counts the wait down. A
// narrow terminal gets the same line with less in it: first without the
// requests, then with a short count and a short bar.
func (p *phase) footer(st style, now time.Time, requests, width int) string {
	elapsed := now.Sub(p.began)
	var of, short, sent string
	if !p.single && p.listed {
		of = fmt.Sprintf("%d of %s", p.finished, things(p.total, "repository", "repositories"))
		short = fmt.Sprintf("%d of %d", p.finished, p.total)
	}
	f := p.progress(now)
	state := []string{fmt.Sprintf("%d%%", int(f*100)), clock(elapsed)}
	held := p.held(now)
	if held > 0 {
		state = append(state, st.yellow(p.system+"'s rate limit: going on in "+clock(held)))
	}
	if left, ok := p.eta(now); ok {
		state = append(state, about(left)+" left")
	} else if p.expect > 0 && held == 0 {
		// The estimate came from the last sync, and this one has outrun it.
		state = append(state, "taking longer than the last sync")
	} else if elapsed >= 2*time.Second && held == 0 {
		state = append(state, "estimating the time left")
	}
	if requests > 0 {
		sent = things(requests, "request", "requests")
	}
	line := func(cells int, parts ...string) string {
		var said []string
		for _, s := range parts {
			if s != "" {
				said = append(said, s)
			}
		}
		return "  " + st.bar(f, cells) + "  " + strings.Join(said, st.sep())
	}
	mid := strings.Join(state, st.sep())
	for _, l := range []string{line(24, of, mid, sent), line(24, of, mid)} {
		if visible(l) < width {
			return l
		}
	}
	return line(12, short, mid)
}

// frame is the part of the screen that is drawn again and again while the
// phase runs.
func (p *phase) frame(st style, now time.Time, width, height, tick, requests int) []string {
	lw := p.rowWidth(width)
	var out []string
	switch {
	case p.single:
		out = append(out, "  "+st.bold(p.title()))
		for _, step := range p.steps {
			out = append(out, p.stepRow(st, p.scopes[0], step, now, lw, tick))
		}
	case !p.listed:
		out = append(out, "  "+st.spin(tick)+" Listing the repositories of "+st.bold(p.target))
	default:
		room := max(1, min(8, height-4))
		for i, slot := range p.active {
			if i == room {
				out = append(out, st.dim(fmt.Sprintf("    and %d more", len(p.active)-room)))
				break
			}
			s := p.scopes[slot]
			parts := []string{"starting"}
			if sv := s.steps[s.step]; sv != nil {
				parts = []string{string(s.step)}
				if sv.read > 0 {
					parts = append(parts, count(int64(sv.read))+" read")
				}
				if !sv.at.IsZero() {
					parts = append(parts, "now at "+sv.at.Format("2006-01-02"))
				}
			}
			left := "  " + p.mark(st, now, tick) + " " + pad(p.short(s.name), p.nameW) + "  " + strings.Join(parts, st.sep())
			out = append(out, row(st, left, st.dim(clock(now.Sub(s.began))), lw))
		}
	}
	return append(out, "", p.footer(st, now, requests, width))
}

// kept are the lines of a phase that stay on the screen once it ends.
func (p *phase) kept(st style, now time.Time, width int) []string {
	var out []string
	if p.single && p.scopes[0] != nil {
		lw := min(width-1, 78)
		out = append(out, "  "+st.bold(p.title()))
		for _, step := range p.steps {
			out = append(out, p.stepRow(st, p.scopes[0], step, now, lw, 0))
		}
	}
	return append(out, p.after...)
}

// repoNews says what a sync added to a repository, or nothing when it found
// nothing new.
func repoNews(st style, r ingest.RepoResult) string {
	var parts []string
	for _, c := range []struct {
		name string
		c    ingest.Count
	}{{"PRs", r.Changes}, {"issues", r.Tickets}, {"CI runs", r.CIRuns}, {"deploys", r.Deploys}} {
		switch {
		case c.c.Added > 0 && c.c.Changed > 0:
			parts = append(parts, fmt.Sprintf("%s +%s, %s updated", c.name, count(int64(c.c.Added)), count(int64(c.c.Changed))))
		case c.c.Added > 0:
			parts = append(parts, fmt.Sprintf("%s +%s", c.name, count(int64(c.c.Added))))
		case c.c.Changed > 0:
			parts = append(parts, fmt.Sprintf("%s %s updated", c.name, count(int64(c.c.Changed))))
		}
	}
	return strings.Join(parts, st.sep())
}

// syncView draws a running sync on a terminal: lines that stay, and under
// them a few lines that are drawn again as the sync moves.
type syncView struct {
	mu    sync.Mutex
	out   io.Writer
	st    style
	size  func() (int, int)
	clock func() time.Time

	ph *phase
	// waited is the time the phases that ended spent waiting for a rate
	// limit, and toldWait is set once the first wait was explained.
	waited   time.Duration
	toldWait bool
	requests func() int
	sent     int
	pending  []string
	drawn    int
	tick     int
	stop     chan struct{}
	done     chan struct{}
}

func newSyncView(out io.Writer, st style, size func() (int, int)) *syncView {
	return &syncView{out: out, st: st, size: size, clock: time.Now}
}

// draw writes the lines that wait and the frame of the phase, over the frame
// that is on the screen. The caller holds the lock.
func (v *syncView) draw() {
	w, h := v.size()
	var b strings.Builder
	if v.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", v.drawn)
	}
	b.WriteString("\r")
	for _, l := range v.pending {
		for _, r := range fold(l, w) {
			b.WriteString("\x1b[2K" + r + "\n")
		}
	}
	v.pending = nil
	var live []string
	if v.ph != nil {
		live = v.ph.frame(v.st, v.clock(), w, h, v.tick, v.sent)
	}
	for _, l := range live {
		b.WriteString("\x1b[2K" + clip(l, w, v.st.more()) + "\n")
	}
	b.WriteString("\x1b[J")
	v.drawn = len(live)
	io.WriteString(v.out, b.String())
}

// say prints lines that stay on the screen.
func (v *syncView) say(lines ...string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = append(v.pending, lines...)
	if v.ph == nil {
		v.draw()
	}
}

// warnf takes the lines of a client, such as a wait for a rate limit.
func (v *syncView) warnf(format string, a ...any) {
	v.say("  " + v.st.warn() + " " + v.st.yellow(fmt.Sprintf(format, a...)))
}

// waiting takes the news that a client is about to wait for a rate limit.
// The first wait is explained in a line that stays. Every wait is counted
// down on the screen.
func (v *syncView) waiting(d time.Duration) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.ph
	if p == nil {
		return
	}
	if !v.toldWait {
		v.toldWait = true
		v.pending = append(v.pending, "  "+v.st.warn()+" "+v.st.yellow(p.system+"'s rate limit is reached. The sync waits for it and goes on by itself."))
	}
	now := v.clock()
	p.hold(now, now.Add(d))
}

// begin starts to draw a phase. requests tells how many requests were sent.
func (v *syncView) begin(p *phase, requests func() int) {
	if v == nil {
		return
	}
	v.mu.Lock()
	p.began, p.scopes = v.clock(), map[int]*scopeView{}
	if p.single {
		p.total, p.listed = 1, true
	}
	v.ph, v.requests, v.sent = p, requests, 0
	v.stop, v.done = make(chan struct{}), make(chan struct{})
	io.WriteString(v.out, "\x1b[?25l")
	v.draw()
	v.mu.Unlock()
	go v.loop(v.stop, v.done)
}

func (v *syncView) loop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			// The count is read without the lock of the view, so a client
			// that logs while it counts cannot block the screen.
			n := 0
			if v.requests != nil {
				n = v.requests()
			}
			v.mu.Lock()
			v.tick++
			v.sent = n
			v.draw()
			v.mu.Unlock()
		}
	}
}

// end stops drawing the phase. Its steps stay on the screen. It is safe to
// call when no phase runs.
func (v *syncView) end() {
	if v == nil {
		return
	}
	v.mu.Lock()
	p := v.ph
	if p == nil {
		v.mu.Unlock()
		return
	}
	v.mu.Unlock()
	close(v.stop)
	<-v.done
	v.mu.Lock()
	defer v.mu.Unlock()
	w, _ := v.size()
	v.pending = append(v.pending, p.kept(v.st, v.clock(), w)...)
	v.waited += p.idle(v.clock())
	v.ph = nil
	v.draw()
	io.WriteString(v.out, "\x1b[?25h")
}

// event takes the news of the sync. It is called from several goroutines.
func (v *syncView) event(e ingest.Event) {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.ph
	if p == nil {
		return
	}
	now, st := v.clock(), v.st
	switch e.Kind {
	case ingest.Listed:
		p.total, p.listed = e.Total, true
		for _, n := range e.Names {
			p.nameW = max(p.nameW, len(p.short(n)))
		}
		p.nameW = min(p.nameW, 36)
	case ingest.ScopeStarted:
		p.scope(e, now)
		if p.single && !e.First && !e.LastFull && e.Last > 0 {
			p.expect = e.Last
		}
	case ingest.StepStarted:
		s := p.scope(e, now)
		s.step, s.steps[e.Step] = e.Step, &stepView{state: stepRunning, began: now}
	case ingest.StepAdvanced:
		if sv := p.scope(e, now).steps[e.Step]; sv != nil {
			sv.read, sv.share, sv.at = e.Read, e.Share, e.At
		}
	case ingest.StepDone:
		if sv := p.scope(e, now).steps[e.Step]; sv != nil {
			sv.state, sv.took, sv.read, sv.added, sv.changed, sv.err = stepDone, e.Took, e.Read, e.Added, e.Changed, e.Err
			if e.Err != nil {
				sv.state = stepFailed
			}
		}
	case ingest.Noted:
		line := e.Detail
		if !p.single && e.Scope != "" {
			line = p.short(e.Scope) + ": " + line
		}
		v.pending = append(v.pending, "  "+st.dim(st.pick("•", "*")+" "+line))
	case ingest.ScopeDone:
		s := p.scope(e, now)
		for i, slot := range p.active {
			if slot == e.Slot {
				p.active = append(p.active[:i:i], p.active[i+1:]...)
				break
			}
		}
		p.finished++
		p.doneTook += now.Sub(s.began) - (p.idle(now) - s.idle)
		if p.single {
			if e.Repo != nil {
				for _, n := range e.Repo.Notes {
					p.after = append(p.after, "  "+st.warn()+" "+st.yellow(n))
				}
			}
			break
		}
		w, _ := v.size()
		lw, name := p.rowWidth(w), pad(p.short(s.name), p.nameW)
		switch {
		case stopped(e.Err):
		case e.Err != nil:
			v.pending = append(v.pending, "  "+st.bad()+" "+name+"  "+st.red("failed: "+e.Err.Error()))
		case e.Repo != nil:
			if n := repoNews(st, *e.Repo); n != "" {
				left, took := "  "+st.ok()+" "+name+"  "+n, st.dim(clock(e.Took))
				if visible(left)+2+visible(took) > lw {
					// A line that stays is never cut. It is folded when it
					// is drawn.
					v.pending = append(v.pending, left+"  "+took)
				} else {
					v.pending = append(v.pending, row(st, left, took, lw))
				}
			}
		}
		if e.Repo != nil {
			for _, n := range e.Repo.Notes {
				v.pending = append(v.pending, "  "+st.warn()+" "+st.yellow(p.short(s.name)+": "+n))
			}
		}
	}
}

// syncPlan is what a sync is about to do, for the lines that open it.
type syncPlan struct {
	target   string
	now      time.Time
	since    time.Time
	steps    []ingest.Step
	workers  int
	store    string
	config   string
	linear   bool
	teams    []string
	jira     string
	projects []string
	// known are the scopes of GitHub the store holds.
	known []store.Scope
}

func (pl syncPlan) single() bool { return strings.Contains(pl.target, "/") }

// mine are the known scopes this sync reads again.
func (pl syncPlan) mine() []store.Scope {
	var out []store.Scope
	for _, sc := range pl.known {
		if sc.SyncedAt.IsZero() {
			continue
		}
		if !pl.single() || strings.EqualFold(sc.Name, pl.target) {
			out = append(out, sc)
		}
	}
	return out
}

// timing says how long the sync may take, going by the last one, and how
// long that is when the last one tells. A first sync has nothing to go by.
func (pl syncPlan) timing() (string, time.Duration) {
	mine := pl.mine()
	if len(mine) == 0 {
		return "a first sync reads the whole window, so it is the longest one. An estimate shows once it is under way.", 0
	}
	var sum float64
	full := false
	for _, sc := range mine {
		if sc.Took == 0 {
			return "reads only what changed since the last sync. An estimate shows once it is under way.", 0
		}
		sum += sc.Took
		full = full || sc.TookFull
	}
	took := time.Duration(sum / float64(min(max(1, pl.workers), len(mine))) * float64(time.Second))
	if full {
		// The time of one scope is measured. The time of several, read a few
		// at a time, is worked out from theirs.
		length := clock(took)
		if len(mine) > 1 {
			length = about(took)
		}
		return "the last sync read the whole window in " + length + ". This one reads only what changed, so it is quicker.", 0
	}
	return about(took) + ", going by the last sync", took
}

// line says in one plain line what the sync reads, for a log.
func (pl syncPlan) line() string {
	names := make([]string, len(pl.steps))
	for i, s := range pl.steps {
		names[i] = string(s)
	}
	return fmt.Sprintf("reading %s of %s. History goes back to %s.", strings.Join(names, ", "), pl.target, pl.since.Format("2006-01-02"))
}

// header are the lines that open a sync: a welcome, and what the run will do.
func (pl syncPlan) header(st style, width int) []string {
	room := max(30, min(width-1, 78)-2)
	mine := pl.mine()
	var last time.Time
	for _, sc := range mine {
		if sc.SyncedAt.After(last) {
			last = sc.SyncedAt
		}
	}
	welcome := "Welcome to factorymeter. This run reads the history of " + pl.target + " from GitHub into a store on this machine. It only reads, and what it reads stays here."
	switch {
	case !last.IsZero():
		welcome = "Welcome back. The last sync of " + pl.target + " was " + ago(pl.now.Sub(last)) + "."
	case len(pl.known) > 0:
		welcome = "Welcome back. " + pl.target + " is new to this store."
	}
	out := []string{"", "  " + st.bold("factorymeter") + " " + st.dim(version), ""}
	for _, l := range wrap(welcome, room) {
		out = append(out, "  "+strings.Replace(l, pl.target, st.bold(pl.target), 1))
	}
	// A value that is too long for the line goes on under itself.
	field := func(name, value string) {
		for i, l := range wrap(value, room-9) {
			if i > 0 {
				name = ""
			}
			out = append(out, "  "+st.dim(pad(name, 9))+l)
		}
	}
	out = append(out, "")
	if pl.single() {
		field("Target", pl.target+" (one repository)")
	} else {
		field("Target", pl.target+" (every repository with a push in the window)")
	}
	weeks := int(pl.now.Sub(pl.since).Hours()/24/7 + 0.5)
	window := things(weeks, "week", "weeks") + ", back to " + pl.since.Format("2006-01-02")
	if weeks < 1 {
		window = "back to " + pl.since.Format("2006-01-02")
	}
	if !last.IsZero() {
		window = "what changed since the last sync. History goes back to " + pl.since.Format("2006-01-02")
	}
	field("Window", window)
	names := make([]string, len(pl.steps))
	for i, s := range pl.steps {
		names[i] = string(s)
	}
	field("Reads", strings.Join(names, ", "))
	if pl.linear {
		l := "Linear tickets"
		if len(pl.teams) > 0 {
			l += " of " + strings.Join(pl.teams, ", ")
		}
		field("", l)
	}
	if pl.jira != "" {
		j := "Jira tickets of " + pl.jira
		if len(pl.projects) > 0 {
			j += ", " + strings.Join(pl.projects, ", ")
		}
		field("", j)
	}
	field("Store", tilde(pl.store))
	if pl.config != "" {
		field("Config", tilde(pl.config))
	}
	t, _ := pl.timing()
	field("Time", t)
	return append(out, "")
}

// holds are the lines that say what the store holds of what was read.
func holds(st style, parts []string, from time.Time) string {
	if !from.IsZero() {
		parts = append(parts, "from "+from.Format("2006-01-02"))
	}
	return "    " + st.dim("In the store: "+strings.Join(parts, st.pick(" · ", " | ")))
}

// slow says how much of a sync was a wait for a rate limit, so a long run
// explains itself. It takes the waits that were counted, once.
func (v *syncView) slow(system string) []string {
	v.mu.Lock()
	d := v.waited
	v.waited = 0
	v.mu.Unlock()
	if d < time.Second {
		return nil
	}
	return []string{"    " + v.st.dim(clock(d)+" of that was a wait for "+system+"'s rate limit.")}
}

// githubDone prints how the read of GitHub ended.
func (v *syncView) githubDone(target string, res ingest.Result, halted bool, calls int, took time.Duration) {
	if v == nil {
		return
	}
	st := v.st
	wait := v.slow("GitHub")
	if halted {
		v.say("", "  "+st.warn()+" "+st.yellow("Stopped after "+clock(took)+". What was read is stored, and the next sync goes on from there."), "")
		return
	}
	if len(res.Repos) == 0 {
		return
	}
	var read, fresh, failed int
	var total ingest.RepoResult
	var from time.Time
	for _, r := range res.Repos {
		if r.Err != nil {
			failed++
			continue
		}
		read++
		if repoNews(st, r) != "" {
			fresh++
		}
		total.Changes.Total += r.Changes.Total
		total.Tickets.Total += r.Tickets.Total
		total.CIRuns.Total += r.CIRuns.Total
		total.Deploys.Total += r.Deploys.Total
		if !r.From.IsZero() && (from.IsZero() || r.From.Before(from)) {
			from = r.From
		}
	}
	tail := st.sep() + things(calls, "request", "requests") + " to GitHub"
	single := strings.Contains(target, "/")
	var out []string
	switch {
	case read == 0:
		out = append(out, "  "+st.bad()+" "+st.red(target+" could not be read."))
	case single && fresh == 0:
		out = append(out, "  "+st.ok()+" "+st.bold(target)+" is up to date"+st.sep()+clock(took)+tail)
	case single:
		out = append(out, "  "+st.ok()+" Synced "+st.bold(target)+" in "+clock(took)+tail)
		out = append(out, wait...)
	default:
		mark, n := st.ok(), things(read, "repository", "repositories")
		if failed > 0 {
			mark, n = st.warn(), fmt.Sprintf("%d of %d repositories", read, read+failed)
		}
		out = append(out, "  "+mark+" Synced "+n+" of "+st.bold(target)+" in "+clock(took)+tail)
		out = append(out, wait...)
		out = append(out, "    "+st.dim(fmt.Sprintf("%d had something new, %d had nothing new.", fresh, read-fresh)))
		if failed > 0 {
			out = append(out, "    "+st.yellow(things(failed, "repository", "repositories")+" could not be read. Run sync again to read what is missing."))
		}
	}
	if read > 0 {
		out = append(out, holds(st, []string{
			"pull requests " + count(total.Changes.Total),
			"issues " + count(total.Tickets.Total),
			"CI runs " + count(total.CIRuns.Total),
			"deploys " + count(total.Deploys.Total),
		}, from))
	}
	v.say(append(append([]string{""}, out...), "")...)
}

// sourceDone prints how the read of a ticket system ended.
func (v *syncView) sourceDone(system, parts string, links bool, r ingest.SourceResult, err error, calls int, took time.Duration) {
	if v == nil {
		return
	}
	st := v.st
	wait := v.slow(system)
	name := system
	if r.Name != "" {
		name += " " + r.Name
	}
	tail := st.sep() + things(calls, "request", "requests") + " to " + system
	var out []string
	switch {
	case stopped(err):
		out = append(out, "  "+st.warn()+" "+st.yellow("Stopped after "+clock(took)+". What was read is stored, and the next sync goes on from there."))
	case err != nil:
		out = append(out, "  "+st.bad()+" "+st.red(name+" could not be read."))
	case r.Tickets.Added+r.Tickets.Changed+r.Links.Added+r.Links.Changed == 0:
		out = append(out, "  "+st.ok()+" "+st.bold(name)+" is up to date"+st.sep()+clock(took)+tail)
	default:
		out = append(out, "  "+st.ok()+" Synced "+st.bold(name)+" in "+clock(took)+tail)
		out = append(out, wait...)
	}
	if r.Again {
		out = append(out, "  "+st.warn()+" "+st.yellow("The list of "+parts+" changed, so the tickets were read again from the start."))
	}
	if err == nil {
		held := []string{"tickets " + count(r.Tickets.Total)}
		if links {
			held = append(held, "links to pull requests "+count(r.Links.Total))
		}
		if len(r.Keys) > 0 {
			held = append(held, "keys "+strings.Join(r.Keys, " "))
		}
		out = append(out, holds(st, held, r.From))
	}
	v.say(append(append([]string{""}, out...), "")...)
}

// next tells what to run now that the store is filled.
func (v *syncView) next(target string) {
	if v == nil {
		return
	}
	st := v.st
	v.say(
		"  "+st.bold("Next"),
		"    "+st.cyan(pad("factorymeter serve "+target, 22+len(target)))+st.dim("the line as a page in the browser"),
		"    "+st.cyan(pad("factorymeter report "+target, 22+len(target)))+st.dim("the line here in the terminal"),
		"",
	)
}

// things writes a number with the word for what it counts.
func things(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return count(int64(n)) + " " + many
}

// wrap breaks a text into lines of at most width cells, at its spaces.
func wrap(s string, width int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			out, line = append(out, line), word
		}
	}
	return append(out, line)
}

// count writes a number with a comma between the thousands.
func count(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// clock writes a length of time that has passed.
func clock(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
}

// about writes a length of time that is an estimate, no finer than the
// estimate is.
func about(d time.Duration) string {
	s := int(d.Seconds())
	if s < 5 {
		return "a few seconds"
	}
	// A number that rounds up to the next unit is written in that unit:
	// 58s is about 1m, not about 60s.
	if s < 60 {
		if s = (s + 2) / 5 * 5; s < 60 {
			return fmt.Sprintf("about %ds", s)
		}
	}
	if s < 600 {
		if s = (s + 7) / 15 * 15; s < 600 {
			if s%60 == 0 {
				return fmt.Sprintf("about %dm", s/60)
			}
			return fmt.Sprintf("about %dm %02ds", s/60, s%60)
		}
	}
	if s < 3600 {
		if m := (s + 30) / 60; m < 60 {
			return fmt.Sprintf("about %dm", m)
		}
	}
	m := (s + 150) / 300 * 5
	return fmt.Sprintf("about %dh %02dm", m/60, m%60)
}

// ago writes how long ago something was.
func ago(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case d < time.Minute:
		return "a moment ago"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours()/24), "day")
}

// tilde writes a path under the home directory the short way.
func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		return filepath.Join("~", rel)
	}
	return path
}

// ticketPhase is the phase of a ticket system.
func ticketPhase(system string) *phase {
	return &phase{system: system, single: true, workers: 1, steps: []ingest.Step{ingest.StepTickets}}
}
