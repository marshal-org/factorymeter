package measure

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
)

// What a slice cuts by.
const (
	byWhole int8 = iota
	byClass
	byAgent
	byRepo
)

// sel picks one slice of a stream.
type sel struct {
	by    int8
	class int8
	agent int16
	repo  int32
}

// parts is one stream split once for every slice a report can ask for. The
// order of the stream is kept, so a slice of observations stays sorted by
// time.
type parts[T any] struct {
	// whole is what agents and people did. Automation is left out.
	whole []T
	class [3][]T
	agent map[int16][]T
	// repo is whole, cut by repository.
	repo map[int32][]T
}

func split[T any](list []T, keyOf func(*T) key) *parts[T] {
	p := &parts[T]{agent: map[int16][]T{}, repo: map[int32][]T{}}
	for i := range list {
		k := keyOf(&list[i])
		if k.class != classAutomation {
			p.whole = append(p.whole, list[i])
			if k.repo >= 0 {
				p.repo[k.repo] = append(p.repo[k.repo], list[i])
			}
		}
		if k.class < classNone {
			p.class[k.class] = append(p.class[k.class], list[i])
		}
		if k.agent >= 0 {
			p.agent[k.agent] = append(p.agent[k.agent], list[i])
		}
	}
	return p
}

func (p *parts[T]) pick(s sel) []T {
	switch s.by {
	case byClass:
		return p.class[s.class]
	case byAgent:
		return p.agent[s.agent]
	case byRepo:
		return p.repo[s.repo]
	}
	return p.whole
}

func (l *Line) obsOf(stream string, s sel) []obs {
	p := l.obsPart[stream]
	if p == nil {
		p = split(l.obs[stream], func(o *obs) key { return o.k })
		l.obsPart[stream] = p
	}
	return p.pick(s)
}

func (l *Line) staysOf(stream string, s sel) []stay {
	p := l.stayPart[stream]
	if p == nil {
		p = split(l.stays[stream], func(o *stay) key { return o.k })
		l.stayPart[stream] = p
	}
	return p.pick(s)
}

// value computes one metric for one slice over the time from <= t < to. A
// queue is read at the moment to.
func (l *Line) value(c calc, s sel, from, to time.Time) (*float64, int) {
	switch c.how {
	case howCount:
		st := standing(l.staysOf(c.stream, s), to)
		return ptr(float64(len(st))), len(st)
	case howOldest:
		st := standing(l.staysOf(c.stream, s), to)
		if len(st) == 0 {
			return nil, 0
		}
		return ptr(to.Sub(st[0].from).Hours()), len(st)
	}
	list := between(l.obsOf(c.stream, s), from, to)
	w := weeks(from, to)
	switch c.how {
	case howPerWeek:
		return perWeek(list, w)
	case howQuantile:
		return quantileOf(list, c.q)
	case howShare:
		return shareOf(list)
	case howHeldTotal:
		return heldTotal(list, w)
	case howHeldShare:
		mine, n := heldTotal(list, w)
		if mine == nil {
			return nil, 0
		}
		sum := 0.0
		for _, st := range line.Stations {
			if !st.InConstraint {
				continue
			}
			if v, _ := heldTotal(between(l.obsOf(st.ID+".held", s), from, to), w); v != nil {
				sum += *v
			}
		}
		if sum <= 0 {
			return nil, n
		}
		return ptr(*mine / sum), n
	}
	return nil, 0
}

// any reports whether a slice of a metric has anything between two moments.
func (l *Line) any(c calc, s sel, from, to time.Time) bool {
	if c.how == howCount || c.how == howOldest {
		for _, st := range l.staysOf(c.stream, s) {
			if st.from.Before(to) && (st.to.IsZero() || st.to.After(from)) {
				return true
			}
		}
		return false
	}
	return len(between(l.obsOf(c.stream, s), from, to)) > 0
}

// The smallest moves a trend is called on.
const (
	// FlatShare is in points of a share: 0.02 is two points.
	FlatShare = 0.02
	// FlatRelative is a part of the previous value: 0.1 is ten percent.
	FlatRelative = 0.1
	// FlatCount is in items, for a queue.
	FlatCount = 2
	// FlatHours is in hours: 0.05 is three minutes.
	FlatHours = 0.05
)

// move compares a value with the previous one.
func move(m line.Metric, cur, prev *float64, thin bool) (*float64, string) {
	if cur == nil || prev == nil {
		return nil, contract.TrendUnknown
	}
	var change *float64
	d := *cur - *prev
	small := d == 0
	switch {
	case m.Unit == line.Share:
		change = ptr(round4(d))
		small = math.Abs(d) < FlatShare
	case *prev != 0:
		rel := d / math.Abs(*prev)
		change = ptr(round4(rel))
		small = math.Abs(rel) < FlatRelative
	}
	switch m.Unit {
	case line.Count:
		small = small || math.Abs(d) < FlatCount
	case line.Hours:
		small = small || math.Abs(d) < FlatHours
	}
	switch {
	case thin:
		return change, contract.TrendUnknown
	case small:
		return change, contract.TrendFlat
	case m.Good == line.Neither:
		return change, contract.TrendUnknown
	case (d > 0) == (m.Good == line.Up):
		return change, contract.TrendBetter
	}
	return change, contract.TrendWorse
}

func rounded(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return ptr(round4(*v))
}

// Options say what to report.
type Options struct {
	// Target is the owner or owner/repo, as the report names it.
	Target string
	// Until is the end of the window. Zero means the last sync.
	Until time.Time
	// Weeks is the length of the window. Zero means 13.
	Weeks int
	// Since, when set, starts the window: it then covers the whole weeks
	// from Since to Until, rounded up, and Weeks is not read.
	Since time.Time
	// History is how many weekly points a series carries. Zero means 26.
	History int
	// Summary leaves every name, title and link out.
	Summary bool
	// MaxRepos is the most repositories that get their own slices. Zero
	// means 20.
	MaxRepos int
	// Now is the time the report is stamped with.
	Now time.Time
	// Version is the version of the program and Registry the version of the
	// signature registry.
	Version, Registry string
}

type window struct {
	since, until time.Time
	weeks        int
	periods      []contract.Period
	prev         *contract.Period
	// back is the earliest moment any number of the report reads.
	back time.Time
}

const (
	day  = 24 * time.Hour
	week = 7 * day
)

func date(t time.Time) string { return t.UTC().Format("2006-01-02") }

// window lays the window, its periods and the window before it over what the
// store holds.
func (l *Line) window(o Options) (window, []string) {
	var limits []string
	var from, synced time.Time
	for i := range l.repos {
		r := &l.repos[i]
		if r.from.After(from) {
			from = r.from
		}
		if r.synced.After(synced) {
			synced = r.synced
		}
	}
	w := window{until: o.Until}
	if w.until.IsZero() {
		w.until = synced
	}
	if w.until.IsZero() {
		w.until = o.Now
	}
	w.until = w.until.UTC().Truncate(time.Second)
	if !o.Since.IsZero() {
		o.Weeks = max(1, int((w.until.Sub(o.Since)+week-time.Second)/week))
		o.History = max(o.History, o.Weeks)
	}

	history := o.History
	if !from.IsZero() {
		// A day of slack, so a store filled 26 weeks back gives 26 periods.
		if held := int((w.until.Sub(from) + day) / week); held < history {
			history = max(held, 1)
		}
	}
	w.weeks = min(o.Weeks, history)
	if w.weeks < o.Weeks {
		limits = append(limits, fmt.Sprintf("The store holds %s, so the window is %s and not %s. Sync with a longer --since to go further back.",
			count(history, "week"), count(w.weeks, "week"), count(o.Weeks, "week")))
	}
	w.since = w.until.Add(-time.Duration(w.weeks) * week)
	for i := history; i > 0; i-- {
		to := w.until.Add(-time.Duration(i-1) * week)
		w.periods = append(w.periods, contract.Period{From: to.Add(-week), To: to})
	}
	w.back = w.periods[0].From
	if start := w.since.Add(-time.Duration(w.weeks) * week); from.IsZero() || !start.Before(from.Add(-day)) {
		w.prev = &contract.Period{From: start, To: w.since}
		if start.Before(w.back) {
			w.back = start
		}
	} else {
		limits = append(limits, fmt.Sprintf("The store does not reach back to the %s before the window, so nothing is compared with them.", count(w.weeks, "week")))
	}
	return w, limits
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	if strings.HasSuffix(what, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(what, "y"))
	}
	return fmt.Sprintf("%d %ss", n, what)
}

type stationState struct{ status, missing string }

// where names the repositories something is true for. A summary names none.
func where(names []string, of int, summary bool) string {
	if summary || len(names) > 5 {
		return fmt.Sprintf("%d of %s", len(names), count(of, "repository"))
	}
	return strings.Join(names, ", ")
}

// status says which stations the store can measure.
func (l *Line) status(summary bool) map[string]stationState {
	out := map[string]stationState{}
	var with []*repoInfo
	for i := range l.repos {
		if l.repos[i].changes > 0 {
			with = append(with, &l.repos[i])
		}
	}
	lacking := func(has func(*repoInfo) bool) []string {
		var names []string
		for _, r := range with {
			if !has(r) {
				names = append(names, r.name)
			}
		}
		return names
	}
	changes := stationState{status: contract.StatusMeasured}
	if len(with) == 0 {
		changes = stationState{contract.StatusNotConnected, "No changes are in the store."}
	}
	out[line.Code], out[line.Review], out[line.Merge] = changes, changes, changes
	if len(with) > 0 && len(l.obs["review.wait"]) == 0 {
		out[line.Review] = stationState{contract.StatusMeasured, "No change in the store was reviewed by another person. A change that waits here may be waiting to merge."}
	}

	out[line.Spec] = stationState{status: contract.StatusMeasured}
	if len(l.sources[record.KindTicket]) == 0 {
		out[line.Spec] = stationState{contract.StatusNotConnected, "No tickets are in the store. Spec is measured from tickets, such as the issues of a repository."}
	}

	part := func(none string, some string, has func(*repoInfo) bool) stationState {
		without := lacking(has)
		switch {
		case len(with) == 0 || len(without) == len(with):
			return stationState{contract.StatusNotConnected, none}
		case len(without) > 0:
			return stationState{contract.StatusPartial, some + " " + where(without, len(with), summary) + "."}
		}
		return stationState{status: contract.StatusMeasured}
	}
	out[line.CI] = part("No CI run on a commit of a change is in the store.", "No CI run joined a change in",
		func(r *repoInfo) bool { return r.ciJoined > 0 })
	out[line.Verify] = part(fmt.Sprintf("Fewer than %d deployments to an environment that is not production were found on a commit of a change before it merged.", line.MinSample),
		"Too few deployments before production were found in", func(r *repoInfo) bool { return r.verifyJoined >= line.MinSample })
	rel := part("No production deployment and no published release is in the store.", "No production deploy is in the store for",
		func(r *repoInfo) bool { return r.prodDeploys > 0 })
	if rel.status != contract.StatusNotConnected {
		if stand := lacking(func(r *repoInfo) bool { return !r.viaRelease }); len(stand) > 0 {
			rel.status = contract.StatusPartial
			rel.missing = strings.TrimSpace(rel.missing + " Published releases stand in for production deploys in " + where(stand, len(with), summary) + ".")
		}
	}
	out[line.Release] = rel
	return out
}

func has(list []string, v string) bool { return slices.Contains(list, v) }

func (l *Line) sliceOf(s sel) contract.Slice {
	switch s.by {
	case byClass:
		return contract.Slice{Class: classNames[s.class]}
	case byAgent:
		return contract.Slice{Class: classNames[classAgent], Agent: l.agents[s.agent]}
	case byRepo:
		return contract.Slice{Repo: l.names[s.repo]}
	}
	return contract.Slice{}
}

func (l *Line) series(m line.Metric, c calc, s sel, w window) contract.Series {
	v, n := l.value(c, s, w.since, w.until)
	out := contract.Series{Metric: m.ID, Slice: l.sliceOf(s), Unit: string(m.Unit), Value: rounded(v), N: n,
		Trend: contract.TrendUnknown, Points: make([]contract.Point, len(w.periods))}
	thin := func(v *float64, n int) bool { return v != nil && m.MinSample > 0 && n < m.MinSample }
	out.Thin = thin(v, n)
	if w.prev != nil {
		pv, pn := l.value(c, s, w.prev.From, w.prev.To)
		out.Previous, out.PreviousN = rounded(pv), pn
		out.Change, out.Trend = move(m, v, pv, out.Thin || thin(pv, pn))
	}
	for i, p := range w.periods {
		pv, pn := l.value(c, s, p.From, p.To)
		out.Points[i] = contract.Point{Value: rounded(pv), N: pn}
	}
	return out
}

// Report computes every number of the registry over a window.
func (l *Line) Report(o Options) (*contract.Report, error) {
	if len(l.repos) == 0 && len(l.sources[record.KindTicket]) == 0 {
		return nil, errors.New("the store holds nothing for this target. Run \"factorymeter sync\" first")
	}
	if o.Weeks <= 0 {
		o.Weeks = 13
	}
	if o.History <= 0 {
		o.History = 26
	}
	if o.History < o.Weeks {
		o.History = o.Weeks
	}
	if o.MaxRepos <= 0 {
		o.MaxRepos = 20
	}
	w, limits := l.window(o)
	status := l.status(o.Summary)

	rep := &contract.Report{
		Schema: contract.Version, GeneratedAt: o.Now.UTC().Truncate(time.Second), Target: o.Target, Summary: o.Summary,
		Tool:     contract.Tool{Name: "factorymeter", Version: o.Version, RegistryVersion: o.Registry, RecordSchema: record.SchemaVersion},
		Window:   contract.Window{Since: w.since, Until: w.until, PeriodDays: 7, Periods: w.periods, Previous: w.prev},
		Coverage: contract.Coverage{Repos: []contract.RepoCoverage{}}, Stations: []contract.Station{},
		Metrics: []contract.Series{}, Recommendations: []contract.Recommendation{},
	}
	if o.Summary {
		rep.Target = ""
	}

	// The slices worth a series: the classes, the agents with the most
	// merged changes first, and the busiest repositories.
	flow := calc{stream: "line.flow", how: howPerWeek}
	merged := func(s sel) int { return len(between(l.obsOf(flow.stream, s), w.since, w.until)) }
	var agents, repos []sel
	for i := range l.agents {
		agents = append(agents, sel{by: byAgent, agent: int16(i)})
	}
	sort.SliceStable(agents, func(i, j int) bool {
		a, b := merged(agents[i]), merged(agents[j])
		if a != b {
			return a > b
		}
		return l.agents[agents[i].agent] < l.agents[agents[j].agent]
	})
	if len(l.names) > 1 && !o.Summary {
		for i := range l.names {
			repos = append(repos, sel{by: byRepo, repo: int32(i)})
		}
		sort.SliceStable(repos, func(i, j int) bool {
			a, b := merged(repos[i]), merged(repos[j])
			if a != b {
				return a > b
			}
			return l.names[repos[i].repo] < l.names[repos[j].repo]
		})
		if len(repos) > o.MaxRepos {
			limits = append(limits, fmt.Sprintf("Only the %d repositories with the most merged changes get their own numbers. The other %d count in the whole line.",
				o.MaxRepos, len(repos)-o.MaxRepos))
			repos = repos[:o.MaxRepos]
		}
	}

	whole := map[string]contract.Series{}
	for _, m := range line.Registry {
		c, ok := calcOf(m)
		if !ok {
			continue
		}
		if m.Station != line.Whole && status[m.Station].status == contract.StatusNotConnected {
			continue
		}
		s := l.series(m, c, sel{}, w)
		whole[m.ID] = s
		rep.Metrics = append(rep.Metrics, s)
		var cut []sel
		if has(m.Slices, line.SliceClass) {
			for _, class := range []int8{classAgent, classHuman, classAutomation} {
				cut = append(cut, sel{by: byClass, class: class})
			}
		}
		if has(m.Slices, line.SliceAgent) {
			cut = append(cut, agents...)
		}
		if has(m.Slices, line.SliceRepo) {
			cut = append(cut, repos...)
		}
		for _, s := range cut {
			if l.any(c, s, w.back, w.until) {
				rep.Metrics = append(rep.Metrics, l.series(m, c, s, w))
			}
		}
	}

	// The constraint is the station that holds the most hours.
	type share struct {
		st   line.Station
		v    float64
		prev *float64
	}
	var shares []share
	for _, st := range line.Stations {
		s := contract.Station{ID: st.ID, Name: st.Name, Status: status[st.ID].status, Missing: status[st.ID].missing, Oldest: []contract.Item{}}
		if hs, ok := whole[st.ID+".held.share"]; ok && st.InConstraint && hs.Value != nil {
			s.HeldShare = hs.Value
			shares = append(shares, share{st, *hs.Value, hs.Previous})
		}
		if !o.Summary && s.Status != contract.StatusNotConnected {
			s.Oldest = l.pointers(standing(l.staysOf(st.ID+".queue", sel{}), w.until), w.until, 5)
		}
		rep.Stations = append(rep.Stations, s)
	}
	sort.SliceStable(shares, func(i, j int) bool { return shares[i].v > shares[j].v })
	span := fmt.Sprintf("the %s to %s", count(w.weeks, "week"), date(w.until))
	if n := whole["line.flow"].N; n < line.MinSample || len(shares) == 0 {
		rep.Verdict = contract.Verdict{
			Headline: "Too few changes merged to name a constraint.",
			Detail: fmt.Sprintf("%s by agents and people merged in %s. A constraint is named from %d.",
				capital(count(n, "change")), span, line.MinSample),
		}
	} else {
		top := shares[0]
		for i := range rep.Stations {
			rep.Stations[i].Constraint = rep.Stations[i].ID == top.st.ID
		}
		head := fmt.Sprintf("%s is the constraint. It holds %s of the hours changes spend on the line", top.st.Name, percent(top.v))
		if top.prev != nil {
			switch d := top.v - *top.prev; {
			case d >= FlatShare:
				head += ", up from " + percent(*top.prev)
			case d <= -FlatShare:
				head += ", down from " + percent(*top.prev)
			default:
				head += ", about the same as in the weeks before"
			}
		}
		detail := fmt.Sprintf("Over %s", span)
		if held := whole[top.st.ID+".held.total"].Value; held != nil {
			detail += fmt.Sprintf(", changes stood %.0f hours a week at %s", *held, strings.ToLower(top.st.Name))
		}
		if out := whole[top.st.ID+".flow"]; out.Value != nil {
			detail += fmt.Sprintf(" and %.1f a week left it", *out.Value)
		}
		detail += "."
		if len(shares) > 1 && shares[1].v >= 0.005 {
			next := shares[1]
			if rest := 1 - top.v; top.st.ID == line.Release && rest > 0 {
				// Waiting for a release dwarfs the rest wherever releases
				// are cut on a schedule. Say what holds changes before it.
				detail += fmt.Sprintf(" Before the merge, %s holds the most: %s of those hours.", strings.ToLower(next.st.Name), percent(next.v/rest))
			} else {
				detail += fmt.Sprintf(" %s is next with %s.", next.st.Name, percent(next.v))
			}
		}
		rep.Verdict = contract.Verdict{Headline: head + ".", Constraint: top.st.ID, Detail: detail}
	}

	rep.Recommendations = l.advise(o, w, status, whole, rep.Verdict.Constraint)

	rep.Coverage, limits = l.coverage(o, w, status, limits)
	rep.Limits = limits
	return rep, nil
}

func percent(v float64) string {
	if v > 0 && v < 0.005 {
		return "under 1 percent"
	}
	return fmt.Sprintf("%.0f percent", v*100)
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// coverage says what the report stands on, and adds what the reader should
// know to the limits.
func (l *Line) coverage(o Options, w window, status map[string]stationState, limits []string) (contract.Coverage, []string) {
	cov := contract.Coverage{Repos: []contract.RepoCoverage{}, Trackers: []contract.TrackerCoverage{}}
	var unjoined, runs, cut, releases, blind, ships, byCommit int
	var unfinished, stale []string
	envs := map[string]bool{}
	notes := map[string][]string{}
	var noteOrder []string
	froms := map[string]bool{}
	for i := range l.repos {
		r := &l.repos[i]
		if !o.Summary {
			cov.Repos = append(cov.Repos, contract.RepoCoverage{Repo: r.name, Private: r.private, From: r.from, SyncedAt: r.synced, Changes: r.changes})
		}
		runs += r.ciRuns
		unjoined += r.ciRuns - r.ciJoined
		cut += r.cut
		if r.ciJoined > 0 && !r.ciWait {
			blind++
		}
		if r.viaRelease {
			releases++
		}
		ships += r.ships
		byCommit += r.shipsByCommit
		for _, e := range r.environments {
			envs[e] = true
		}
		switch {
		case r.unfinished:
			unfinished = append(unfinished, r.name)
		case w.until.Sub(r.synced) > day:
			stale = append(stale, r.name)
		}
		if !r.from.IsZero() {
			froms[date(r.from)] = true
		}
		for _, n := range r.notes {
			if _, ok := notes[n]; !ok {
				noteOrder = append(noteOrder, n)
			}
			notes[n] = append(notes[n], r.name)
		}
	}
	sort.Slice(cov.Repos, func(i, j int) bool { return cov.Repos[i].Repo < cov.Repos[j].Repo })

	inWindow := func(kind string) int {
		list := l.seen[kind]
		lo := sort.Search(len(list), func(i int) bool { return list[i] >= w.since.Unix() })
		hi := sort.Search(len(list), func(i int) bool { return list[i] >= w.until.Unix() })
		return hi - lo
	}
	for _, kind := range []string{record.KindTicket, record.KindChange, record.KindCIRun, record.KindDeploy} {
		var src []string
		for s := range l.sources[kind] {
			src = append(src, s)
		}
		sort.Strings(src)
		sc := contract.SourceCoverage{Kind: kind, Source: strings.Join(src, ", "), Records: inWindow(kind)}
		switch kind {
		case record.KindTicket:
			var say []string
			if l.sources[kind][record.GitHub] {
				say = append(say, "A GitHub issue has no started state. It starts when the first pull request that closes it is opened.")
			}
			for _, name := range l.ticketSystems() {
				say = append(say, fmt.Sprintf("A %s ticket starts when it first enters a started state, or when the first pull request linked to it is opened, whichever is earlier.", name))
			}
			sc.Note = strings.Join(say, " ")
		case record.KindCIRun:
			if unjoined > 0 {
				sc.Note = fmt.Sprintf("%d of the %d runs in the store are on commits of no change, such as runs on the main branch. They are left out.", unjoined, runs)
			}
		case record.KindDeploy:
			var names []string
			for e := range envs {
				names = append(names, e)
			}
			sort.Strings(names)
			var say []string
			if len(names) > 0 && !o.Summary {
				say = append(say, "Production is a deployment to "+strings.Join(names, ", ")+".")
			}
			if releases > 0 {
				say = append(say, "Published releases stand in for production deploys where a repository has no production deployment.")
			}
			sc.Note = strings.Join(say, " ")
		}
		cov.Sources = append(cov.Sources, sc)
	}

	total := len(l.repos)
	if len(unfinished) > 0 {
		limits = append(limits, fmt.Sprintf("The first sync has not finished for %s. Its numbers are not complete. Run sync again.", where(unfinished, total, o.Summary)))
	}
	if len(stale) > 0 {
		limits = append(limits, fmt.Sprintf("The last sync is more than a day older than the end of the window for %s. Their numbers stop early.", where(stale, total, o.Summary)))
	}
	if len(froms) > 1 {
		limits = append(limits, "The repositories do not all go back to the same day. The history starts at the latest of them, and older weeks of the others are not shown.")
	}
	for _, n := range noteOrder {
		if total > 1 {
			n = where(notes[n], total, o.Summary) + ": " + n
		}
		limits = append(limits, n)
	}
	for i := range l.tickets {
		t := &l.tickets[i]
		tc := contract.TrackerCoverage{Source: t.source, Name: t.name, SyncedAt: t.synced, Tickets: t.tickets, Linked: t.linked, Keys: t.keys}
		if o.Summary {
			tc.Name, tc.Keys = "", []string{}
		}
		if tc.Keys == nil {
			tc.Keys = []string{}
		}
		cov.Trackers = append(cov.Trackers, tc)
		name := record.SourceName(t.source)
		switch {
		case t.unfinished:
			limits = append(limits, fmt.Sprintf("The first sync of %s has not finished. Its tickets are not complete. Run sync again.", name))
		case w.until.Sub(t.synced) > day:
			limits = append(limits, fmt.Sprintf("The last sync of %s is more than a day older than the end of the window. What changed on its tickets since is missing. Run sync with %s again.", name, name))
		}
		for _, n := range t.notes {
			limits = append(limits, name+": "+n)
		}
		// Linear attaches pull requests to a ticket. Jira has no list of them
		// that a sync reads, so only the key ties the two.
		attached := fmt.Sprintf("when %s has it attached, or ", name)
		if t.source == record.Jira {
			attached = ""
		}
		limits = append(limits, fmt.Sprintf("A pull request counts as linked to a %s ticket %swhen its branch, title or description names the key of the ticket. A word that only looks like a key is taken for one. A ticket deleted in %s stays in the store.", name, attached, name))
		switch {
		case t.linkedOnly && t.skipped > 0:
			limits = append(limits, fmt.Sprintf("Only the %s tickets that a pull request of this repository is linked to are counted. %s in the store %s left out. The report of the owner counts them all.",
				name, capital(count(t.skipped, "ticket")), plural(t.skipped, "is", "are")))
		case t.linkedOnly:
			limits = append(limits, fmt.Sprintf("Only the %s tickets that a pull request of this repository is linked to are counted.", name))
		case total > 1:
			limits = append(limits, fmt.Sprintf("A %s ticket counts for the repository of the first pull request linked to it. A ticket with no pull request counts in the whole line only.", name))
		}
	}
	if cut > 0 {
		limits = append(limits, fmt.Sprintf("%s more commits than were read. Rework after review and CI rounds on their later commits can be missed.", capital(count(cut, "change")+" "+plural(cut, "has", "have"))))
	}
	limits = append(limits,
		"The agent share is a floor. A change counts as written by an agent only when it carries a signature the registry knows.",
		"Hours are hours on the clock. Nights and weekends count.")
	if l.sources[record.KindTicket][record.GitHub] {
		limits = append(limits, "A GitHub issue closed with no pull request that closes it never counts as started. A link from a pull request to an issue stays in the store when it is later taken off on GitHub.")
	}
	if status[line.Code].status != contract.StatusNotConnected {
		limits = append(limits, "Writing time starts at the first commit of a change. Work before that commit is not seen, so a change opened right after its first commit shows almost none.")
	}
	if blind > 0 {
		limits = append(limits, "The CI runs in the store are stamped as started the moment they are created, so the wait for a runner cannot be seen. It is left empty.")
	}
	if status[line.Release].status != contract.StatusNotConnected {
		const held, timed = "holds the changes merged into its branch up to that merge", "the first one after a merge is taken as the one that shipped it"
		switch {
		case byCommit == 0:
			limits = append(limits, "No production deploy in the store is of the commit a merge left, so the store cannot tell which changes a deploy holds. The first production deploy after a merge is taken as the one that shipped it.")
		case byCommit < ships:
			limits = append(limits, fmt.Sprintf("%d of the %d production deploys in the store that did not fail are of the commit a merge left, and each %s. Of the other deploys only the time is known: %s.", byCommit, ships, held, timed))
		default:
			limits = append(limits, fmt.Sprintf("Every production deploy in the store that did not fail is of the commit a merge left, and each %s. A change merged into a branch that is never deployed is taken to ship with the first deploy whose commit merged after it.", held))
		}
		if n := len(between(l.obsOf(unseenAtRelease, sel{}), w.since, w.until)); n > 0 {
			limits = append(limits, fmt.Sprintf("%s merged in the window before the first production deploy in the store, and %s not counted at release. The store cannot tell a line that did not release from one whose releases were not recorded yet.",
				capital(count(n, "change")), plural(n, "is", "are")))
		}
	}
	return cov, limits
}

// ticketSystems names the ticket systems of the line that are not
// repositories, each once.
func (l *Line) ticketSystems() []string {
	var out []string
	for i := range l.tickets {
		if name := record.SourceName(l.tickets[i].source); !has(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
