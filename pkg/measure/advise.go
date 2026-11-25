package measure

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

// adviser holds what the recommendation rules read. The rules themselves are
// defined in pkg/line: when each fires and what it compares with. The code
// here does the reading.
type adviser struct {
	l      *Line
	o      Options
	w      window
	status map[string]stationState
	// whole is every series of the whole line, by metric id.
	whole map[string]contract.Series
	// top is the constraint. Empty when none is named.
	top string
}

// engines are the rules, by id. The rule every station shares, station.slow,
// is not listed: slow reads it for any station.
var engines = map[string]func(*adviser, line.Rule) *contract.Recommendation{
	"verify.missing":            (*adviser).verifyMissing,
	"ci.failing":                (*adviser).ciFailing,
	"ci.agents-fail-more":       (*adviser).ciAgentsFailMore,
	"merge.reverted":            (*adviser).mergeReverted,
	"code.abandoned":            (*adviser).codeAbandoned,
	"review.queue-growing":      (*adviser).reviewQueueGrowing,
	"merge.approved-waiting":    (*adviser).mergeApprovedWaiting,
	"code.stale-drafts":         (*adviser).codeStaleDrafts,
	"spec.reopened":             (*adviser).specReopened,
	"spec.stale-tickets":        (*adviser).specStaleTickets,
	"spec.backlog-thin":         (*adviser).specBacklogThin,
	"review.agents-wait-longer": (*adviser).reviewAgentsWaitLonger,
	"code.agent-changes-large":  (*adviser).codeAgentChangesLarge,
}

const slowSuffix = ".slow"

// advise runs every rule and puts the recommendations in order: the
// constraint first, then the largest gain, then the rest as the registry
// lists them.
func (l *Line) advise(o Options, w window, status map[string]stationState, whole map[string]contract.Series, top string) []contract.Recommendation {
	a := &adviser{l: l, o: o, w: w, status: status, whole: whole, top: top}
	out := []contract.Recommendation{}
	for _, r := range line.Rules {
		var rec *contract.Recommendation
		if strings.HasSuffix(r.ID, slowSuffix) {
			rec = a.slow(r)
		} else if f := engines[r.ID]; f != nil {
			rec = f(a, r)
		}
		if rec != nil {
			out = append(out, *rec)
		}
	}
	rank := func(r *contract.Recommendation) int {
		switch {
		case top != "" && r.Rule == top+slowSuffix:
			return 0
		case r.HoursFreed != nil && *r.HoursFreed > 0:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool {
		x, y := rank(&out[i]), rank(&out[j])
		if x != y {
			return x < y
		}
		return x == 1 && *out[i].HoursFreed > *out[j].HoursFreed
	})
	return out
}

func (a *adviser) start(r line.Rule) *contract.Recommendation {
	return &contract.Recommendation{Rule: r.ID, Station: r.Station, Title: r.Title, PlayURL: r.PlayURL(),
		Evidence: []contract.Evidence{}, Items: []contract.Item{}, Confidence: contract.ConfidenceHigh}
}

// grade is the confidence a count of items gives.
func grade(n int) string {
	switch {
	case n >= line.HighSample:
		return contract.ConfidenceHigh
	case n >= line.MediumSample:
		return contract.ConfidenceMedium
	}
	return contract.ConfidenceLow
}

// hold is the mean hours one item is held. Each item is cut off at the 90th
// percentile of the list, as heldTotal does, so hold times the items in a
// week is the held total.
func hold(list []obs) (float64, int) {
	if len(list) == 0 {
		return 0, 0
	}
	v := values(list)
	top, sum := quantile(v, 0.9), 0.0
	for _, x := range v {
		sum += math.Min(x, top)
	}
	return sum / float64(len(v)), len(v)
}

// best slides a stretch of line.BestWeeks weeks over the window, a week at a
// time, and returns the lowest value it finds. A stretch with fewer than
// line.MinSample items is passed over. A window no longer than the stretch has
// no best weeks to compare with.
func (a *adviser) best(f func(from, to time.Time) (float64, int)) (v float64, from time.Time, n int, ok bool) {
	if a.w.weeks <= line.BestWeeks {
		return 0, time.Time{}, 0, false
	}
	for i := 0; i+line.BestWeeks <= a.w.weeks; i++ {
		lo := a.w.since.Add(time.Duration(i) * week)
		x, k := f(lo, lo.Add(line.BestWeeks*week))
		if k < line.MinSample {
			continue
		}
		if !ok || x < v {
			v, from, n, ok = x, lo, k, true
		}
	}
	return v, from, n, ok
}

// sayHours says a number of hours the way a person would.
func sayHours(h float64) string {
	switch {
	case h*60 < 1:
		return "under a minute"
	case h < 1:
		return count(int(math.Round(h*60)), "minute")
	case h < 48:
		return fmt.Sprintf("%.1f hours", h)
	}
	return fmt.Sprintf("%.1f days", h/24)
}

// hoursAWeek says a sum of hours over a week.
func hoursAWeek(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1f hours a week", v)
	}
	return fmt.Sprintf("%.0f hours a week", v)
}

// say puts the value of a metric in words.
func say(m line.Metric, v float64) string {
	if strings.HasSuffix(m.ID, ".held.total") {
		return hoursAWeek(v)
	}
	switch m.Unit {
	case line.Hours:
		return sayHours(v)
	case line.Share:
		return percent(v)
	case line.PerWeek:
		return fmt.Sprintf("%.1f a week", v)
	case line.Lines:
		return count(int(math.Round(v)), "line")
	}
	return fmt.Sprintf("%.0f", v)
}

// before names the window a value is compared with.
func (a *adviser) before(m line.Metric) string {
	if m.Kind == line.Queue {
		return "at the end of the " + count(a.w.weeks, "week") + " before"
	}
	return "in the " + count(a.w.weeks, "week") + " before"
}

// read is a number of the whole line as evidence, next to the window before.
func (a *adviser) read(id string) (contract.Evidence, bool) {
	s, ok := a.whole[id]
	m, known := line.ByID(id)
	if !ok || !known || s.Value == nil {
		return contract.Evidence{}, false
	}
	text := fmt.Sprintf("%s: %s.", m.Name, say(m, *s.Value))
	if s.Previous != nil {
		text += fmt.Sprintf(" It was %s %s.", say(m, *s.Previous), a.before(m))
	}
	return contract.Evidence{Metric: id, Value: s.Value, Versus: s.Previous, Text: text}, true
}

// cite adds the numbers of the whole line a rule read to its evidence.
func (a *adviser) cite(rec *contract.Recommendation, ids ...string) {
	for _, id := range ids {
		if e, ok := a.read(id); ok {
			rec.Evidence = append(rec.Evidence, e)
		}
	}
}

// classes reads a metric for agents and for people. It is ok only when both
// sides are drawn from enough items to compare.
func (a *adviser) classes(id string) (agent, human float64, n int, e contract.Evidence, ok bool) {
	m, known := line.ByID(id)
	c, has := calcOf(m)
	if !known || !has {
		return 0, 0, 0, e, false
	}
	av, an := a.l.value(c, sel{by: byClass, class: classAgent}, a.w.since, a.w.until)
	hv, hn := a.l.value(c, sel{by: byClass, class: classHuman}, a.w.since, a.w.until)
	if av == nil || hv == nil || an < line.MinSample || hn < line.MinSample {
		return 0, 0, 0, e, false
	}
	e = contract.Evidence{Metric: id, Slice: contract.Slice{Class: classNames[classAgent]}, Value: rounded(av), Versus: rounded(hv),
		Text: fmt.Sprintf("%s: %s for agents, over %d items. %s for people, over %d items.", m.Name, say(m, *av), an, say(m, *hv), hn)}
	return *av, *hv, min(an, hn), e, true
}

// pointers turns the first stays of a list into the items a report points
// at. at is the moment the hours they have stood are counted to.
func (l *Line) pointers(list []stay, at time.Time, most int) []contract.Item {
	out := []contract.Item{}
	for i, x := range list {
		if i == most {
			break
		}
		it := l.items[x.item]
		out = append(out, contract.Item{Kind: it.kind, Key: it.key, URL: it.url, Title: it.title,
			Class: classNames[it.class], Hours: round4(at.Sub(x.from).Hours())})
	}
	return out
}

// point adds the items to look at first. A summary names none.
func (a *adviser) point(rec *contract.Recommendation, list []stay) {
	if !a.o.Summary {
		rec.Items = a.l.pointers(list, a.w.until, 5)
	}
}

// waiting is the items that have stood in a queue for longer than a time at
// the end of the window, the one that has stood longest first.
func (a *adviser) waiting(queue string, longer time.Duration) []stay {
	var out []stay
	for _, s := range standing(a.l.staysOf(queue, sel{}), a.w.until) {
		if a.w.until.Sub(s.from) > longer {
			out = append(out, s)
		}
	}
	return out
}

func stationWord(st line.Station) string {
	if st.ID == line.CI {
		return st.Name
	}
	return strings.ToLower(st.Name)
}

// base is the hours a week a gain at a station is compared with: what the
// stations before the merge hold or, for release, what the whole line holds.
func (a *adviser) base(station string) float64 {
	sum := 0.0
	for _, st := range line.Stations {
		if !st.InConstraint || (st.ID == line.Release && station != line.Release) {
			continue
		}
		if v := a.whole[st.ID+".held.total"].Value; v != nil {
			sum += *v
		}
	}
	return sum
}

// slow is the rule every station that can be the constraint has: the station
// holds items longer than it did in its own best four weeks.
func (a *adviser) slow(r line.Rule) *contract.Recommendation {
	st, ok := line.StationByID(r.Station)
	if !ok || a.status[st.ID].status == contract.StatusNotConnected {
		return nil
	}
	held := a.l.obsOf(st.ID+".held", sel{})
	now, n := hold(between(held, a.w.since, a.w.until))
	if n == 0 {
		return nil
	}
	rate := float64(n) / float64(a.w.weeks)
	best, from, bestN, sized := a.best(func(from, to time.Time) (float64, int) { return hold(between(held, from, to)) })
	freed := 0.0
	if sized {
		freed = math.Max(0, now-best) * rate
	}
	isTop := a.top == st.ID
	if !isTop && (n < line.MinSample || !sized || freed <= 0 || now < line.SlowRatio*best || freed < line.SlowShare*a.base(st.ID)) {
		return nil
	}

	rec := a.start(r)
	rec.Confidence = grade(n)
	one := capital("a " + st.Item)
	if isTop {
		rec.Why = st.Name + " is the constraint."
		if share := a.whole[st.ID+".held.share"].Value; share != nil {
			rec.Why = fmt.Sprintf("%s is the constraint: it holds %s of the hours changes spend on the line.", st.Name, percent(*share))
		}
	} else {
		rec.Why = st.Name + " is not the constraint, but it has run faster than it does now."
	}
	switch {
	case sized && freed > 0:
		rec.Why += fmt.Sprintf(" %s is held %s here on average. In the best four weeks of the window, from %s, it was %s. At that pace %s of waiting go away.",
			one, sayHours(now), date(from), sayHours(best), hoursAWeek(freed))
	case sized:
		rec.Why += fmt.Sprintf(" %s is held %s here on average, and no four weeks of the window did better. The gain has to come from a change in how the station works.",
			one, sayHours(now))
	default:
		rec.Why += fmt.Sprintf(" %s is held %s here on average. The window is too short, or holds too few items, to compare the station with its own best four weeks.",
			one, sayHours(now))
	}
	if sized {
		rec.HoursFreed = ptr(round4(freed))
		rec.Confidence = grade(min(n, bestN))
	}

	total := st.ID + ".held.total"
	for _, id := range r.Reads {
		s := a.whole[id]
		if id != total || s.Value == nil {
			a.cite(rec, id)
			continue
		}
		m, _ := line.ByID(id)
		e := contract.Evidence{Metric: id, Value: s.Value, Versus: s.Previous,
			Text: fmt.Sprintf("%ss were held %s at %s.", capital(st.Item), say(m, *s.Value), stationWord(st))}
		switch {
		case sized && freed > 0:
			e.Versus = ptr(round4(best * rate))
			e.Text += fmt.Sprintf(" At the pace of the best four weeks, from %s, it would be %s.", date(from), say(m, best*rate))
		case sized:
			e.Text += " No four weeks of the window did better."
		case s.Previous != nil:
			e.Text += fmt.Sprintf(" It was %s %s.", say(m, *s.Previous), a.before(m))
		}
		rec.Evidence = append(rec.Evidence, e)
	}
	return rec
}

func (a *adviser) verifyMissing(r line.Rule) *contract.Recommendation {
	if a.status[line.Verify].status != contract.StatusNotConnected {
		return nil
	}
	s, ok := a.whole["review.skipped"]
	if !ok || s.Value == nil || s.N < line.MinSample || *s.Value < line.Ungated {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(s.N)
	rec.Why = fmt.Sprintf("%s of the changes merged in the window had no review seen, and the store holds too few deployments before production on the commits of changes to measure a verify station. So nothing in the store shows that a change was proven to work before it merged. Proof that is recorded as a deployment to a test environment on the commit of a change shows up here as the verify station.",
		capital(percent(*s.Value)))
	a.cite(rec, r.Reads...)
	return rec
}

func (a *adviser) ciFailing(r line.Rule) *contract.Recommendation {
	s, ok := a.whole["ci.rework"]
	if !ok || s.Value == nil || s.N < line.MinSample || *s.Value < line.CIFailing {
		return nil
	}
	failed := a.l.obsOf("ci.rework", sel{})
	list := between(failed, a.w.since, a.w.until)
	now, _ := shareOf(list)
	perRound, _ := hold(between(a.l.obsOf("ci.held", sel{}), a.w.since, a.w.until))
	rate := float64(len(list)) / float64(a.w.weeks)
	best, from, bestN, sized := a.best(func(from, to time.Time) (float64, int) {
		v, n := shareOf(between(failed, from, to))
		if v == nil {
			return 0, 0
		}
		return *v, n
	})

	rec := a.start(r)
	rec.Confidence = grade(s.N)
	rec.Why = fmt.Sprintf("%s of the CI rounds in the window failed, timed out or were run again. Each of them sends a change through CI one more time.", capital(percent(*now)))
	m, _ := line.ByID("ci.rework")
	e := contract.Evidence{Metric: m.ID, Value: s.Value, Versus: s.Previous, Text: fmt.Sprintf("%s: %s.", m.Name, say(m, *now))}
	switch {
	case sized:
		freed := math.Max(0, *now-best) * rate * perRound
		rec.HoursFreed = ptr(round4(freed))
		rec.Confidence = grade(min(s.N, bestN))
		if freed > 0 {
			e.Versus = ptr(round4(best))
			e.Text += fmt.Sprintf(" In the best four weeks of the window, from %s, it was %s.", date(from), say(m, best))
			rec.Why += fmt.Sprintf(" In the best four weeks of the window, from %s, it was %s. At that share %s of CI go away.", date(from), percent(best), hoursAWeek(freed))
		} else {
			e.Text += " No four weeks of the window did better."
			rec.Why += " No four weeks of the window did better."
		}
	case s.Previous != nil:
		e.Text += fmt.Sprintf(" It was %s %s.", say(m, *s.Previous), a.before(m))
	}
	rec.Evidence = append(rec.Evidence, e)
	// A workflow name is a name, so a summary leaves it out.
	if text := a.l.failingFlows(a.w.since, a.w.until, 3); text != "" && !a.o.Summary {
		rec.Evidence = append(rec.Evidence, contract.Evidence{Metric: m.ID, Text: text})
	}
	a.cite(rec, "ci.flow", "ci.work.p50")
	return rec
}

func (a *adviser) ciAgentsFailMore(r line.Rule) *contract.Recommendation {
	agent, human, n, e, ok := a.classes("ci.rework")
	if !ok || agent-human < line.AgentCIGap {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(n)
	rec.Why = fmt.Sprintf("CI fails on %s of the rounds on changes by agents and on %s of the rounds on changes by people. An agent that runs the checks before it pushes takes those rounds off the line.",
		percent(agent), percent(human))
	rec.Evidence = append(rec.Evidence, e)
	return rec
}

func (a *adviser) mergeReverted(r line.Rule) *contract.Recommendation {
	s, ok := a.whole["merge.rework"]
	if !ok || s.Value == nil || *s.Value < line.Reverted {
		return nil
	}
	reverts := 0
	for _, x := range between(a.l.obsOf("merge.rework", sel{}), a.w.since, a.w.until) {
		if x.v > 0 {
			reverts++
		}
	}
	if reverts < line.RevertedMin {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(s.N)
	rec.Why = fmt.Sprintf("%d of the %s merged in the window were reverted later. A revert is work the line did twice, and a fault that reached the main branch before anything caught it.",
		reverts, count(s.N, "change"))
	a.cite(rec, r.Reads...)
	return rec
}

func (a *adviser) codeAbandoned(r line.Rule) *contract.Recommendation {
	s, ok := a.whole["code.rework"]
	if !ok || s.Value == nil || s.N < line.MinSample || *s.Value < line.Abandoned {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(s.N)
	rec.Why = fmt.Sprintf("%s of the %s closed in the window were closed without merging. The work on them was thrown away. Settling what to build before the work starts keeps it off the line.",
		capital(percent(*s.Value)), count(s.N, "change"))
	a.cite(rec, r.Reads...)
	return rec
}

func (a *adviser) reviewQueueGrowing(r line.Rule) *contract.Recommendation {
	// Where nobody reviews, a change that is ready is waiting to merge.
	if len(a.l.obs["review.wait"]) == 0 {
		return nil
	}
	s, ok := a.whole["review.queue"]
	if !ok || s.Value == nil || s.Previous == nil || *s.Value < line.ReviewQueueMin || s.Trend != contract.TrendWorse {
		return nil
	}
	rec := a.start(r)
	rec.Why = fmt.Sprintf("%.0f changes wait for a first review at the end of the window. It was %.0f at the end of the %s before. Changes arrive faster than they are reviewed.",
		*s.Value, *s.Previous, count(a.w.weeks, "week"))
	a.cite(rec, r.Reads...)
	a.point(rec, standing(a.l.staysOf("review.queue", sel{}), a.w.until))
	return rec
}

func (a *adviser) mergeApprovedWaiting(r line.Rule) *contract.Recommendation {
	if a.status[line.Merge].status == contract.StatusNotConnected {
		return nil
	}
	list := a.waiting("merge.queue", line.ApprovedHours*time.Hour)
	if len(list) < line.ApprovedMin {
		return nil
	}
	rec := a.start(r)
	rec.Why = fmt.Sprintf("%d changes were approved more than %d hours ago and are still open. The oldest has waited %s since its approval. They passed review and deliver nothing until they merge.",
		len(list), line.ApprovedHours, sayHours(a.w.until.Sub(list[0].from).Hours()))
	a.cite(rec, r.Reads...)
	a.point(rec, list)
	return rec
}

func (a *adviser) codeStaleDrafts(r line.Rule) *contract.Recommendation {
	if a.status[line.Code].status == contract.StatusNotConnected {
		return nil
	}
	list := a.waiting("code.queue", line.StaleDraftDays*day)
	if len(list) < line.StaleDraftMin {
		return nil
	}
	rec := a.start(r)
	rec.Why = fmt.Sprintf("%d changes have been a draft for more than %d days. The oldest was opened %s ago. A draft that stopped moving is work that was started and delivers nothing.",
		len(list), line.StaleDraftDays, sayHours(a.w.until.Sub(list[0].from).Hours()))
	a.cite(rec, r.Reads...)
	a.point(rec, list)
	return rec
}

func (a *adviser) specReopened(r line.Rule) *contract.Recommendation {
	s, ok := a.whole["spec.rework"]
	if !ok || s.Value == nil || s.N < line.MinSample || *s.Value < line.Reopened {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(s.N)
	rec.Why = fmt.Sprintf("%s of the %s finished in the window had been finished before and were opened again. A ticket that comes back was closed before the work was right, or before anyone agreed on what done means.",
		capital(percent(*s.Value)), count(s.N, "ticket"))
	a.cite(rec, r.Reads...)
	return rec
}

func (a *adviser) specStaleTickets(r line.Rule) *contract.Recommendation {
	if a.status[line.Spec].status == contract.StatusNotConnected {
		return nil
	}
	list := a.waiting("spec.queue", line.StaleTicketDays*day)
	if len(list) < line.StaleTicketMin {
		return nil
	}
	rec := a.start(r)
	rec.Why = fmt.Sprintf("%d tickets have been open for more than %d days and nobody has started them. The oldest was created %s ago. A list of work nobody will do hides the work that is ready, from people and from agents.",
		len(list), line.StaleTicketDays, sayHours(a.w.until.Sub(list[0].from).Hours()))
	a.cite(rec, r.Reads...)
	a.point(rec, list)
	return rec
}

func (a *adviser) specBacklogThin(r line.Rule) *contract.Recommendation {
	if a.status[line.Spec].status == contract.StatusNotConnected {
		return nil
	}
	s, ok := a.whole["spec.flow"]
	if !ok || s.Value == nil || s.N < line.MinSample {
		return nil
	}
	ready := len(standing(a.l.staysOf("spec.queue", sel{}), a.w.until))
	if float64(ready) >= *s.Value*line.BacklogWeeks {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(s.N)
	rec.Why = fmt.Sprintf("%s open and not started at the end of the window. The line started %.1f tickets a week. That is less than %s of work ready to start. When the tickets run out the line stands still, whatever the stations after it can do.",
		capital(plural(ready, "1 ticket is", fmt.Sprintf("%d tickets are", ready))), *s.Value, plural(line.BacklogWeeks, "one week", fmt.Sprintf("%d weeks", line.BacklogWeeks)))
	a.cite(rec, r.Reads...)
	return rec
}

func (a *adviser) reviewAgentsWaitLonger(r line.Rule) *contract.Recommendation {
	agent, human, n, e, ok := a.classes("review.wait.p50")
	if !ok || agent < line.AgentWaitRatio*human || agent-human < line.AgentWaitHours {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(n)
	rec.Why = fmt.Sprintf("A change by an agent waits %s for its first review at the median. A change by a person waits %s. Look at what differs: the size of the change, the proof that it works, or who is asked to review it.",
		sayHours(agent), sayHours(human))
	rec.Evidence = append(rec.Evidence, e)
	return rec
}

func (a *adviser) codeAgentChangesLarge(r line.Rule) *contract.Recommendation {
	agent, human, n, e, ok := a.classes("line.size.p50")
	if !ok || agent < line.AgentSizeRatio*human || agent < line.AgentSizeLines {
		return nil
	}
	rec := a.start(r)
	rec.Confidence = grade(n)
	rec.Why = fmt.Sprintf("The median change by an agent is %s. The median change by a person is %s. A large change takes longer to review and is harder to prove.",
		count(int(math.Round(agent)), "line"), count(int(math.Round(human)), "line"))
	rec.Evidence = append(rec.Evidence, e)
	return rec
}

// failingFlows names the workflows whose runs failed, timed out or were run
// again most often between two moments, the worst first. It counts runs, not
// rounds: one round can hold the runs of several workflows.
func (l *Line) failingFlows(from, to time.Time, most int) string {
	lo := sort.Search(len(l.flowRuns), func(i int) bool { return !l.flowRuns[i].at.Before(from) })
	hi := sort.Search(len(l.flowRuns), func(i int) bool { return !l.flowRuns[i].at.Before(to) })
	type tally struct {
		flow         int32
		runs, failed int
	}
	by := map[int32]*tally{}
	var list []*tally
	for _, x := range l.flowRuns[lo:hi] {
		t := by[x.flow]
		if t == nil {
			t = &tally{flow: x.flow}
			by[x.flow] = t
			list = append(list, t)
		}
		t.runs++
		if x.failed {
			t.failed++
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.failed != b.failed {
			return a.failed > b.failed
		}
		if a.runs != b.runs {
			return a.runs < b.runs
		}
		fa, fb := l.flows[a.flow], l.flows[b.flow]
		if fa.name != fb.name {
			return fa.name < fb.name
		}
		return l.names[fa.repo] < l.names[fb.repo]
	})
	// The repository is named when the line has more than one.
	repos := map[int32]bool{}
	for _, f := range l.flows {
		repos[f.repo] = true
	}
	var parts []string
	for _, t := range list {
		if t.failed == 0 || len(parts) == most {
			break
		}
		f := l.flows[t.flow]
		name := strconv.Quote(f.name)
		if len(repos) > 1 {
			name += " of " + l.names[f.repo]
		}
		parts = append(parts, fmt.Sprintf("%s in %d of its %s", name, t.failed, count(t.runs, "run")))
	}
	if len(parts) == 0 {
		return ""
	}
	return "The workflows whose runs failed, timed out or were run again most often: " + strings.Join(parts, ", ") + "."
}
