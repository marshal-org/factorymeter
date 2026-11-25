package measure

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
)

// fired lists the rules of the recommendations of a report, in order.
func fired(r *contract.Report) string {
	ids := make([]string, len(r.Recommendations))
	for i, rec := range r.Recommendations {
		ids[i] = rec.Rule
	}
	return strings.Join(ids, " ")
}

func advice(t *testing.T, r *contract.Report, rule string) contract.Recommendation {
	t.Helper()
	for _, rec := range r.Recommendations {
		if rec.Rule == rule {
			return rec
		}
	}
	t.Fatalf("no recommendation from %s. Fired: %s", rule, fired(r))
	return contract.Recommendation{}
}

// reads lists the metrics of the evidence of a recommendation, in order.
func reads(rec contract.Recommendation) string {
	ids := make([]string, len(rec.Evidence))
	for i, e := range rec.Evidence {
		ids[i] = e.Metric
	}
	return strings.Join(ids, " ")
}

func keys(list []contract.Item) string {
	out := make([]string, len(list))
	for i, it := range list {
		out[i] = fmt.Sprintf("%s:%s", it.Key, fmt.Sprint(it.Hours))
	}
	return strings.Join(out, " ")
}

// The registry says which rules exist and the engine runs them. Neither may
// hold a rule the other lacks.
func TestEveryRuleHasAnEngine(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range line.Rules {
		seen[r.ID] = true
		if strings.HasSuffix(r.ID, slowSuffix) {
			st, ok := line.StationByID(r.Station)
			if !ok || !st.InConstraint {
				t.Errorf("%s: only a station that can be the constraint has the slow rule", r.ID)
			}
			continue
		}
		if engines[r.ID] == nil {
			t.Errorf("the rule %s is defined and never run", r.ID)
		}
	}
	for id := range engines {
		if !seen[id] {
			t.Errorf("the engine runs %s, which is not a rule", id)
		}
	}
	for _, st := range line.Stations {
		if st.InConstraint && !seen[st.ID+slowSuffix] {
			t.Errorf("%s can be the constraint and has no slow rule", st.ID)
		}
	}
}

// keeps checks that a recommendation is what its rule says it is. A reader
// of the JSON looks the rule up by id and trusts the two to agree.
func keeps(t *testing.T, r *contract.Report) {
	t.Helper()
	seen := map[string]bool{}
	for _, rec := range r.Recommendations {
		def, ok := line.RuleByID(rec.Rule)
		if !ok {
			t.Errorf("%s is not a rule", rec.Rule)
			continue
		}
		if seen[rec.Rule] {
			t.Errorf("%s fired twice", rec.Rule)
		}
		seen[rec.Rule] = true
		if rec.Station != def.Station || rec.Title != def.Title || rec.PlayURL != def.PlayURL() {
			t.Errorf("%s does not carry the station, title and practice of its rule: %+v", rec.Rule, rec)
		}
		if rec.Why == "" || !strings.HasSuffix(rec.Why, ".") || rec.Evidence == nil || rec.Items == nil {
			t.Errorf("%s: a reason that is a sentence, evidence and items are always there: %+v", rec.Rule, rec)
		}
		if rec.Confidence != contract.ConfidenceHigh && rec.Confidence != contract.ConfidenceMedium && rec.Confidence != contract.ConfidenceLow {
			t.Errorf("%s: confidence %q", rec.Rule, rec.Confidence)
		}
		if rec.HoursFreed != nil && (def.Sized == "" || *rec.HoursFreed < 0) {
			t.Errorf("%s: hours freed %v from a rule that is sized by %q", rec.Rule, *rec.HoursFreed, def.Sized)
		}
		for _, e := range rec.Evidence {
			known := false
			for _, id := range def.Reads {
				known = known || id == e.Metric
			}
			if !known || e.Text == "" {
				t.Errorf("%s shows %s, which its rule does not read, or says nothing about it: %q", rec.Rule, e.Metric, e.Text)
			}
		}
	}
}

func TestHoldGradeAndWords(t *testing.T) {
	var list []obs
	for _, v := range []float64{10, 1, 2, 3, 4} {
		list = append(list, obs{v: v})
	}
	// The 90th percentile of 1 2 3 4 10 sits 0.6 of the way from 4 to 10:
	// 7.6. So 10 counts as 7.6, and the mean is 17.6 / 5.
	if v, n := hold(list); !near(v, 3.52) || n != 5 {
		t.Errorf("hold = %v from %d", v, n)
	}
	if v, n := hold(nil); v != 0 || n != 0 {
		t.Errorf("hold of nothing = %v from %d", v, n)
	}
	for n, want := range map[int]string{0: "low", 9: "low", 10: "medium", 29: "medium", 30: "high", 3000: "high"} {
		if got := grade(n); got != want {
			t.Errorf("grade(%d) = %s, want %s", n, got, want)
		}
	}
	for v, want := range map[float64]string{0: "under a minute", 0.01: "under a minute", 1.0 / 60: "1 minute", 0.5: "30 minutes",
		1: "1.0 hours", 47.9: "47.9 hours", 48: "2.0 days", 650.4: "27.1 days"} {
		if got := sayHours(v); got != want {
			t.Errorf("sayHours(%v) = %q, want %q", v, got, want)
		}
	}
	for v, want := range map[float64]string{0: "0.0 hours a week", 1.79: "1.8 hours a week", 9.94: "9.9 hours a week", 10: "10 hours a week", 5033.5: "5034 hours a week"} {
		if got := hoursAWeek(v); got != want {
			t.Errorf("hoursAWeek(%v) = %q, want %q", v, got, want)
		}
	}
}

// paced is eight weeks of one repository. Two changes by people open in
// each week, a day and two days into it. Each is approved after wait(week)
// and merged merge(week) later. Weeks are numbered 1 to 8, oldest first.
func paced(t *testing.T, wait, merge func(week int) time.Duration) *contract.Report {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	n := 0
	for k := 1; k <= 8; k++ {
		start := end.Add(-time.Duration(9-k) * week)
		for _, d := range []time.Duration{day, 2 * day} {
			n++
			ok := start.Add(d + wait(k))
			c := change(n, start.Add(d), review(record.ReviewApproved, ok), merged(ok.Add(merge(k))))
			rb.Change(&c)
		}
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Weeks: 8, History: 16, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func steady(d time.Duration) func(int) time.Duration {
	return func(int) time.Duration { return d }
}

// firstHalf is one pace in weeks 1 to 4 and another in weeks 5 to 8.
func firstHalf(early, late time.Duration) func(int) time.Duration {
	return func(k int) time.Duration {
		if k <= 4 {
			return early
		}
		return late
	}
}

// The constraint, sized against its own best four weeks.
//
// Review held a change 2 hours in weeks 1 to 4 and 10 hours in weeks 5 to
// 8. Sixteen changes: eight at 2 and eight at 10. The 90th percentile is 10,
// so nothing is cut: the mean is 96 / 16 = 6 hours, and review held 96 / 8 =
// 12 hours a week. The best four weeks are the first four, at 2 hours. Two
// changes leave review a week, so at that pace it holds 2 x 2 = 4 hours a
// week, and (6 - 2) x 2 = 8 hours a week go away.
func TestConstraintIsSizedAgainstItsBestFourWeeks(t *testing.T) {
	r := paced(t, firstHalf(2*h, 10*h), steady(0))
	if r.Verdict.Constraint != line.Review {
		t.Fatalf("verdict: %+v", r.Verdict)
	}
	keeps(t, r)
	// Code and merge hold nothing, so no other rule has anything to say.
	if got := fired(r); got != "review.slow" {
		t.Fatalf("fired: %s", got)
	}
	rec := r.Recommendations[0]
	if rec.Station != line.Review || rec.Title != "Cut the wait for review" ||
		rec.PlayURL != "https://thesoftwarefactory.site/playbook/trust-is-earned-with-evidence/" || len(rec.Items) != 0 {
		t.Errorf("head: %+v", rec)
	}
	// The window starts 8 weeks before 2026-11-02.
	want := "Review is the constraint: it holds 100 percent of the hours changes spend on the line. " +
		"A change is held 6.0 hours here on average. In the best four weeks of the window, from 2026-09-07, it was 2.0 hours. " +
		"At that pace 8.0 hours a week of waiting go away."
	if rec.Why != want {
		t.Errorf("why:\n got %s\nwant %s", rec.Why, want)
	}
	if num(rec.HoursFreed) != "8" {
		t.Errorf("hours freed = %s", num(rec.HoursFreed))
	}
	// Sixteen changes in the window, eight in the best four weeks. The
	// smaller count grades the gain, and eight is below ten.
	if rec.Confidence != contract.ConfidenceLow {
		t.Errorf("confidence = %s", rec.Confidence)
	}
	if got := reads(rec); got != "review.held.total review.held.share review.wait.p50 review.work.p50 review.queue" {
		t.Errorf("evidence: %s", got)
	}
	e := rec.Evidence[0]
	if num(e.Value) != "12" || num(e.Versus) != "4" ||
		e.Text != "Changes were held 12 hours a week at review. At the pace of the best four weeks, from 2026-09-07, it would be 4.0 hours a week." {
		t.Errorf("held total: %s against %s: %s", num(e.Value), num(e.Versus), e.Text)
	}
	// The median of eight waits of 2 hours and eight of 10 is halfway: 6.
	if e := rec.Evidence[2]; num(e.Value) != "6" || e.Text != "Wait for first review, median: 6.0 hours." {
		t.Errorf("wait: %s: %s", num(e.Value), e.Text)
	}
}

// The best four weeks are four weeks in a row, wherever they fall.
func TestBestFourWeeksAreAStretch(t *testing.T) {
	// Review held 2 hours in weeks 1 to 3 and 10 hours after. The best four
	// weeks in a row are the first four: six changes at 2 and two at 10, 32
	// hours over eight changes, 4 hours each. The best three weeks alone
	// would say 2. The whole window is 6 x 2 + 10 x 10 = 112 hours over
	// sixteen changes, 7 hours each. (7 - 4) x 2 = 6 hours a week go away.
	r := paced(t, func(k int) time.Duration {
		if k <= 3 {
			return 2 * h
		}
		return 10 * h
	}, steady(0))
	rec := advice(t, r, "review.slow")
	if num(rec.HoursFreed) != "6" || !strings.Contains(rec.Why, "from 2026-09-07, it was 4.0 hours.") {
		t.Errorf("freed %s: %s", num(rec.HoursFreed), rec.Why)
	}
	// Fast in weeks 3 to 6 and slow around them. The stretch starts two
	// weeks into the window. Eight changes at 10 and eight at 2: 6 hours
	// each, against 2. (6 - 2) x 2 = 8 hours a week.
	r = paced(t, func(k int) time.Duration {
		if k >= 3 && k <= 6 {
			return 2 * h
		}
		return 10 * h
	}, steady(0))
	rec = advice(t, r, "review.slow")
	if num(rec.HoursFreed) != "8" || !strings.Contains(rec.Why, "from 2026-09-21, it was 2.0 hours.") {
		t.Errorf("freed %s: %s", num(rec.HoursFreed), rec.Why)
	}
}

// The constraint comes first even when it has no gain to show and another
// station does.
//
// Review holds every change 10 hours, so no four weeks did better: 20 hours
// a week. Merge held 1 hour in weeks 1 to 4 and 3 hours after: the mean is
// 32 / 16 = 2 hours, 4 hours a week. Review has 20 of the 24 hours, 83
// percent. At its best pace merge frees (2 - 1) x 2 = 2 hours a week. That
// is twice its best and more than 5 percent of 24, so the rule fires.
func TestConstraintComesFirstAndAnotherStationCanFire(t *testing.T) {
	r := paced(t, steady(10*h), firstHalf(h, 3*h))
	if got := fired(r); got != "review.slow merge.slow" {
		t.Fatalf("fired: %s", got)
	}
	keeps(t, r)
	top := r.Recommendations[0]
	want := "Review is the constraint: it holds 83 percent of the hours changes spend on the line. " +
		"A change is held 10.0 hours here on average, and no four weeks of the window did better. " +
		"The gain has to come from a change in how the station works."
	if top.Why != want || num(top.HoursFreed) != "0" {
		t.Errorf("review: freed %s:\n got %s\nwant %s", num(top.HoursFreed), top.Why, want)
	}
	if e := top.Evidence[0]; num(e.Value) != "20" || e.Text != "Changes were held 20 hours a week at review. No four weeks of the window did better." {
		t.Errorf("review held total: %s: %s", num(e.Value), e.Text)
	}
	m := r.Recommendations[1]
	want = "Merge is not the constraint, but it has run faster than it does now. " +
		"A change is held 2.0 hours here on average. In the best four weeks of the window, from 2026-09-07, it was 1.0 hours. " +
		"At that pace 2.0 hours a week of waiting go away."
	if m.Why != want || num(m.HoursFreed) != "2" || m.Confidence != contract.ConfidenceLow {
		t.Errorf("merge: freed %s, %s:\n got %s\nwant %s", num(m.HoursFreed), m.Confidence, m.Why, want)
	}
	if e := m.Evidence[0]; e.Metric != "merge.held.total" || num(e.Value) != "4" || num(e.Versus) != "2" {
		t.Errorf("merge held total: %+v", e)
	}
}

// A station that is not the constraint has two bars to pass. Each case here
// passes one and stops at the other.
func TestAStationCloseToItsBestSaysNothing(t *testing.T) {
	// The pace. Merge holds every change 13 hours: 26 hours a week, the
	// constraint. Review held 10 hours and then 14: the mean is 12, which is
	// 20 percent over its best and short of the 25 it takes. The gain would
	// pass: (12 - 10) x 2 = 4 hours a week, and 5 percent of the 26 + 24
	// hours held before the merge is 2.5.
	r := paced(t, firstHalf(10*h, 14*h), steady(13*h))
	if got := fired(r); got != "merge.slow" {
		t.Errorf("20 percent over its best: fired %s", got)
	}
	// The gain. Review holds every change 10 hours: 20 hours a week, the
	// constraint. Merge held half an hour and then an hour and a half: the
	// mean is 1 hour, twice its best. But (1 - 0.5) x 2 = 1 hour a week, and
	// 5 percent of the 20 + 2 hours held before the merge is 1.1.
	r = paced(t, steady(10*h), firstHalf(30*time.Minute, 90*time.Minute))
	if got := fired(r); got != "review.slow" {
		t.Errorf("a gain under 5 percent: fired %s", got)
	}
	// The same merge with a little more to gain: 0.5 and then 1.7 hours.
	// The mean is 1.1, the gain 1.2 hours a week, and 5 percent of 20 + 2.2
	// is 1.11.
	r = paced(t, steady(10*h), firstHalf(30*time.Minute, 102*time.Minute))
	if got := fired(r); got != "review.slow merge.slow" {
		t.Errorf("a gain over 5 percent: fired %s", got)
	}
}

// A window of four weeks or less has no best four weeks to compare with.
// The constraint is still named, and its gain is left empty.
func TestAShortWindowIsNotSized(t *testing.T) {
	r, err := scenario(t).Report(Options{Target: repo, Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	// Merge holds 52 percent of the hours: see TestReport.
	if got := fired(r); got != "merge.slow" {
		t.Fatalf("fired: %s", got)
	}
	keeps(t, r)
	rec := r.Recommendations[0]
	// Held at merge: 2 2 2 2 2 and 22 capped at 12 is 22 hours over six
	// changes, 3.7 hours each.
	want := "Merge is the constraint: it holds 52 percent of the hours changes spend on the line. " +
		"A change is held 3.7 hours here on average. " +
		"The window is too short, or holds too few items, to compare the station with its own best four weeks."
	if rec.Why != want || rec.HoursFreed != nil || rec.Confidence != contract.ConfidenceLow {
		t.Errorf("merge: freed %s, %s:\n got %s\nwant %s", num(rec.HoursFreed), rec.Confidence, rec.Why, want)
	}
	// Without a best pace the held hours are set next to the weeks before.
	if e := rec.Evidence[0]; num(e.Value) != "11" || num(e.Versus) != "2.5" ||
		e.Text != "Changes were held 11 hours a week at merge. It was 2.5 hours a week in the 2 weeks before." {
		t.Errorf("held total: %s against %s: %s", num(e.Value), num(e.Versus), e.Text)
	}
}

// stuck is a repository where nothing merged and work stands still: four
// drafts opened 40, 30, 20 and 5 days ago, and four changes approved 5 days,
// 3 days, 2 days and 12 hours ago that are still open.
func stuck(t *testing.T, summary bool) *contract.Report {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	for i, d := range []time.Duration{40 * day, 30 * day, 20 * day, 5 * day} {
		c := change(i+1, end.Add(-d), draft)
		rb.Change(&c)
	}
	for i, d := range []time.Duration{5 * day, 3 * day, 2 * day, 12 * h} {
		c := change(i+5, end.Add(-10*day), review(record.ReviewApproved, end.Add(-d)))
		rb.Change(&c)
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Summary: summary, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestQueueRulesPointAtItems(t *testing.T) {
	r := stuck(t, false)
	keeps(t, r)
	// Nothing merged, so no constraint is named and no gain is sized. The
	// rules come in the order of the registry.
	if got := fired(r); got != "merge.approved-waiting code.stale-drafts" {
		t.Fatalf("fired: %s", got)
	}
	m := r.Recommendations[0]
	// Three were approved more than 24 hours ago. The one approved 12
	// hours ago is not counted.
	want := "3 changes were approved more than 24 hours ago and are still open. The oldest has waited 5.0 days since its approval. " +
		"They passed review and deliver nothing until they merge."
	if m.Why != want || m.HoursFreed != nil || m.Confidence != contract.ConfidenceHigh {
		t.Errorf("approved: freed %s, %s:\n got %s\nwant %s", num(m.HoursFreed), m.Confidence, m.Why, want)
	}
	if got := keys(m.Items); got != "acme/app#5:120 acme/app#6:72 acme/app#7:48" {
		t.Errorf("approved items: %s", got)
	}
	// All four stand in the queue. The rule counts only the three.
	if e := m.Evidence[0]; e.Metric != "merge.queue" || num(e.Value) != "4" || !strings.HasPrefix(e.Text, "PRs approved and not merged: 4.") {
		t.Errorf("approved evidence: %+v", e)
	}
	if it := m.Items[0]; it.Kind != record.KindChange || it.URL != "https://example.invalid/5" || it.Title != "Change 5" || it.Class != "human" {
		t.Errorf("item: %+v", it)
	}
	d := r.Recommendations[1]
	// Three drafts are older than 14 days. The one opened 5 days ago is not.
	want = "3 changes have been a draft for more than 14 days. The oldest was opened 40.0 days ago. " +
		"A draft that stopped moving is work that was started and delivers nothing."
	if d.Why != want || d.PlayURL != "" {
		t.Errorf("drafts: %q\n got %s\nwant %s", d.PlayURL, d.Why, want)
	}
	if got := keys(d.Items); got != "acme/app#1:960 acme/app#2:720 acme/app#3:480" {
		t.Errorf("draft items: %s", got)
	}
}

func TestSummaryRecommendsAndNamesNothing(t *testing.T) {
	r := stuck(t, true)
	keeps(t, r)
	if got := fired(r); got != "merge.approved-waiting code.stale-drafts" {
		t.Fatalf("fired: %s", got)
	}
	for _, rec := range r.Recommendations {
		if rec.Items == nil || len(rec.Items) != 0 {
			t.Errorf("%s names items in a summary: %+v", rec.Rule, rec.Items)
		}
	}
	raw, err := json.Marshal(r.Recommendations)
	if err != nil {
		t.Fatal(err)
	}
	for _, not := range []string{"acme", "Change 5", "example.invalid", "#5"} {
		if strings.Contains(string(raw), not) {
			t.Errorf("the recommendations of a summary hold %q", not)
		}
	}
}

// Agents next to people, in two weeks.
//
// Six changes by people: 15 lines, approved after 2 hours, CI passes.
// Six by an agent: 400 lines, approved after 6 hours, CI fails on three.
func TestRulesThatCompareAgentsWithPeople(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
	for i := 1; i <= 12; i++ {
		at := end.Add(-5*day + time.Duration(i)*h)
		wait, opts, result := 2*h, []opt{}, "success"
		if i > 6 {
			wait, opts = 6*h, append(opts, fromAgent, func(c *record.Change) { c.Additions, c.Deletions = 300, 100 })
			if i > 9 {
				result = "failure"
			}
		}
		c := change(i, at, append(opts, review(record.ReviewApproved, at.Add(wait)), merged(at.Add(wait)))...)
		rb.Change(&c)
		x := run(fmt.Sprintf("sha%d", i), 1, at, at, at.Add(30*time.Minute), result)
		rb.CIRun(&x)
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	// Review holds the most. Two weeks cannot be sized, so the rest come
	// in the order of the registry.
	if got := fired(r); got != "review.slow ci.failing ci.agents-fail-more review.agents-wait-longer code.agent-changes-large" {
		t.Fatalf("fired: %s", got)
	}
	keeps(t, r)
	// Three of twelve rounds failed.
	c := advice(t, r, "ci.failing")
	want := "25 percent of the CI rounds in the window failed, timed out or were run again. Each of them sends a change through CI one more time."
	if c.Why != want || c.HoursFreed != nil || c.Confidence != contract.ConfidenceMedium || reads(c) != "ci.rework ci.rework ci.flow ci.work.p50" {
		t.Errorf("ci.failing: freed %s, %s, %s:\n got %s\nwant %s", num(c.HoursFreed), c.Confidence, reads(c), c.Why, want)
	}
	// The second line of evidence names the workflow. It has no number of
	// its own.
	if e := c.Evidence[1]; e.Value != nil || e.Text != `The workflows whose runs failed, timed out or were run again most often: "ci" in 3 of its 12 runs.` {
		t.Errorf("the workflows: %+v", e)
	}
	// Three of the agent's six, none of the people's six.
	g := advice(t, r, "ci.agents-fail-more")
	want = "CI fails on 50 percent of the rounds on changes by agents and on 0 percent of the rounds on changes by people. " +
		"An agent that runs the checks before it pushes takes those rounds off the line."
	if g.Why != want || g.Confidence != contract.ConfidenceLow {
		t.Errorf("ci.agents-fail-more: %s:\n got %s\nwant %s", g.Confidence, g.Why, want)
	}
	if e := g.Evidence[0]; e.Slice.Class != "agent" || num(e.Value) != "0.5" || num(e.Versus) != "0" ||
		e.Text != "CI rounds that failed: 50 percent for agents, over 6 items. 0 percent for people, over 6 items." {
		t.Errorf("ci.agents-fail-more evidence: %+v", e)
	}
	w := advice(t, r, "review.agents-wait-longer")
	want = "A change by an agent waits 6.0 hours for its first review at the median. A change by a person waits 2.0 hours. " +
		"Look at what differs: the size of the change, the proof that it works, or who is asked to review it."
	if w.Why != want {
		t.Errorf("review.agents-wait-longer:\n got %s\nwant %s", w.Why, want)
	}
	if e := w.Evidence[0]; e.Metric != "review.wait.p50" || num(e.Value) != "6" || num(e.Versus) != "2" {
		t.Errorf("review.agents-wait-longer evidence: %+v", e)
	}
	s := advice(t, r, "code.agent-changes-large")
	want = "The median change by an agent is 400 lines. The median change by a person is 15 lines. " +
		"A large change takes longer to review and is harder to prove."
	if s.Why != want {
		t.Errorf("code.agent-changes-large:\n got %s\nwant %s", s.Why, want)
	}
}

// A line with no gate: ten changes merged an hour after they opened with no
// review, three of them reverted by the change after, and four more closed
// without merging.
func TestRulesAboutALineWithNoGate(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	for i := 1; i <= 14; i++ {
		at := end.Add(-time.Duration(20-i) * day)
		last := merged(at.Add(h))
		if i > 10 {
			last = closed(at.Add(h))
		}
		c := change(i, at, last)
		if i == 2 || i == 4 || i == 6 {
			c.Body = fmt.Sprintf("Reverts acme/app#%d", i-1)
		}
		rb.Change(&c)
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	// With no review seen, a change spends its hour from ready to merge at
	// review. Review is the constraint and never did better.
	if got := fired(r); got != "review.slow verify.missing merge.reverted code.abandoned" {
		t.Fatalf("fired: %s", got)
	}
	keeps(t, r)
	if m := r.Recommendations[0]; num(m.HoursFreed) != "0" || !strings.Contains(m.Why, "A change is held 1.0 hours here on average, and no four weeks of the window did better.") {
		t.Errorf("review.slow: freed %s: %s", num(m.HoursFreed), m.Why)
	}
	v := advice(t, r, "verify.missing")
	if !strings.HasPrefix(v.Why, "100 percent of the changes merged in the window had no review seen, and the store holds too few deployments") ||
		reads(v) != "review.skipped" || v.Confidence != contract.ConfidenceMedium || v.HoursFreed != nil {
		t.Errorf("verify.missing: %s, %s: %s", v.Confidence, reads(v), v.Why)
	}
	// Changes 1, 3 and 5 of the ten merged.
	x := advice(t, r, "merge.reverted")
	want := "3 of the 10 changes merged in the window were reverted later. " +
		"A revert is work the line did twice, and a fault that reached the main branch before anything caught it."
	if x.Why != want {
		t.Errorf("merge.reverted:\n got %s\nwant %s", x.Why, want)
	}
	if e := x.Evidence[0]; num(e.Value) != "0.3" || e.Text != "Merges reverted: 30 percent." {
		t.Errorf("merge.reverted evidence: %+v", e)
	}
	// Four of fourteen closed changes: 28.6 percent.
	a := advice(t, r, "code.abandoned")
	want = "29 percent of the 14 changes closed in the window were closed without merging. The work on them was thrown away. " +
		"Settling what to build before the work starts keeps it off the line."
	if a.Why != want {
		t.Errorf("code.abandoned:\n got %s\nwant %s", a.Why, want)
	}
}

// queued is a repository where n changes wait for a first review at the end:
// change 2 opened a day ago, change 3 two days ago, and so on. Before them,
// left changes were opened 20 days ago and approved and merged 10 days ago,
// so they stood in the queue when the window before ended. Change 1 was
// reviewed and merged 20 days ago, unless nobody reviews here.
func queued(t *testing.T, n, left int, reviewed bool) *contract.Report {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
	if reviewed {
		c := change(1, end.Add(-20*day), review(record.ReviewApproved, end.Add(-20*day+h)), merged(end.Add(-20*day+h)))
		rb.Change(&c)
	}
	for i := 2; i <= n+1; i++ {
		c := change(i, end.Add(-time.Duration(i-1)*day))
		rb.Change(&c)
	}
	for i := 0; i < left; i++ {
		c := change(100+i, end.Add(-20*day), review(record.ReviewApproved, end.Add(-10*day)), merged(end.Add(-10*day)))
		rb.Change(&c)
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	keeps(t, r)
	return r
}

func TestReviewQueueGrowing(t *testing.T) {
	// Six wait now. None waited two weeks ago.
	r := queued(t, 6, 0, true)
	if got := fired(r); got != "review.queue-growing" {
		t.Fatalf("fired: %s", got)
	}
	rec := r.Recommendations[0]
	want := "6 changes wait for a first review at the end of the window. It was 0 at the end of the 2 weeks before. " +
		"Changes arrive faster than they are reviewed."
	if rec.Why != want || rec.HoursFreed != nil || rec.Confidence != contract.ConfidenceHigh {
		t.Errorf("freed %s, %s:\n got %s\nwant %s", num(rec.HoursFreed), rec.Confidence, rec.Why, want)
	}
	if e := rec.Evidence[0]; e.Metric != "review.queue" || num(e.Value) != "6" || num(e.Versus) != "0" || !strings.HasSuffix(e.Text, ": 6. It was 0 at the end of the 2 weeks before.") {
		t.Errorf("evidence: %+v", e)
	}
	// The five that stood longest, the oldest first. Change 7 opened six
	// days ago.
	if got := keys(rec.Items); got != "acme/app#7:144 acme/app#6:120 acme/app#5:96 acme/app#4:72 acme/app#3:48" {
		t.Errorf("items: %s", got)
	}

	for name, r := range map[string]*contract.Report{
		// Four is not a queue.
		"four wait": queued(t, 4, 0, true),
		// Six now and five two weeks ago: one more is flat, not worse.
		"five waited before": queued(t, 6, 5, true),
		// Nobody ever reviewed a change here, so nothing waits for a review.
		"nobody reviews": queued(t, 6, 0, false),
	} {
		if strings.Contains(fired(r), "review.queue-growing") {
			t.Errorf("%s: fired %s", name, fired(r))
		}
	}
}

// A share of failed CI rounds, sized against the best four weeks.
//
// Two changes a week for eight weeks. Each has one CI round of 30 minutes,
// is approved 2 hours after it opens and merges then. The rounds pass in
// weeks 1 to 4 and fail in weeks 5 to 8: 8 of 16 failed, against 0 of 8 in
// the best four weeks. There are 2 rounds a week and a round holds a change
// half an hour, so (0.5 - 0) x 2 x 0.5 = 0.5 hours a week go away.
func TestCIFailingIsSized(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	n := 0
	for k := 1; k <= 8; k++ {
		for _, d := range []time.Duration{day, 2 * day} {
			n++
			at := end.Add(-time.Duration(9-k)*week + d)
			c := change(n, at, review(record.ReviewApproved, at.Add(2*h)), merged(at.Add(2*h)))
			rb.Change(&c)
			result := "success"
			if k > 4 {
				result = "failure"
			}
			x := run(fmt.Sprintf("sha%d", n), 1, at, at, at.Add(30*time.Minute), result)
			rb.CIRun(&x)
		}
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Weeks: 8, History: 16, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	keeps(t, r)
	// Review holds a change the 2 hours less the half hour CI is busy: 3
	// hours a week against 1 for CI. It is the constraint and never did
	// better. CI rounds are as long as ever, so ci.slow is silent.
	if got := fired(r); got != "review.slow ci.failing" {
		t.Fatalf("fired: %s", got)
	}
	rec := r.Recommendations[1]
	want := "50 percent of the CI rounds in the window failed, timed out or were run again. Each of them sends a change through CI one more time. " +
		"In the best four weeks of the window, from 2026-09-07, it was 0 percent. At that share 0.5 hours a week of CI go away."
	// Sixteen rounds in the window, eight in the best four weeks.
	if rec.Why != want || num(rec.HoursFreed) != "0.5" || rec.Confidence != contract.ConfidenceLow {
		t.Errorf("freed %s, %s:\n got %s\nwant %s", num(rec.HoursFreed), rec.Confidence, rec.Why, want)
	}
	if e := rec.Evidence[0]; e.Metric != "ci.rework" || num(e.Value) != "0.5" || num(e.Versus) != "0" ||
		e.Text != "CI rounds that failed: 50 percent. In the best four weeks of the window, from 2026-09-07, it was 0 percent." {
		t.Errorf("evidence: %+v", e)
	}
}

// A line where changes are reviewed gets no advice to add a gate, and two
// reverts are too few to speak of.
func TestQuietRules(t *testing.T) {
	r, err := scenario(t).Report(Options{Target: repo, Weeks: 2, History: 4, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range r.Recommendations {
		for _, not := range []string{"verify.missing", "merge.reverted", "review.queue-growing", "ci.failing"} {
			if rec.Rule == not {
				t.Errorf("%s fired: %s", not, rec.Why)
			}
		}
	}
}

// ticketed is a line where six tickets were started and finished in the last
// two weeks, one of them for the second time. open lists how long ago each
// ticket that nobody has started was created.
func ticketed(t *testing.T, done int, summary bool, open ...time.Duration) *contract.Report {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	id := func(n int) string { return record.TicketID(record.GitHub, fmt.Sprintf("%s#%d", repo, n)) }
	for i := 1; i <= done; i++ {
		c := change(i, end.Add(-time.Duration(i+1)*day), review(record.ReviewApproved, end.Add(-time.Duration(i)*day-h)), merged(end.Add(-time.Duration(i)*day)))
		rb.Change(&c)
		rb.Link(&record.Link{Kind: record.LinkCloses, From: c.ID, FromKind: record.KindChange, To: id(50 + i), ToKind: record.KindTicket})
	}
	rb.Done()
	ticket := func(n int, tk record.Ticket) {
		tk.Source, tk.ID, tk.Key, tk.Project = record.GitHub, id(n), fmt.Sprintf("%s#%d", repo, n), repo
		tk.URL, tk.Title = fmt.Sprintf("https://example.invalid/issues/%d", n), fmt.Sprintf("Ticket %d", n)
		b.Ticket(&tk)
	}
	for i := 1; i <= done; i++ {
		at := end.Add(-time.Duration(i) * day)
		tk := record.Ticket{State: record.TicketDone, CreatedAt: end.Add(-time.Duration(i+2) * day), DoneAt: &at, ClosedAt: &at}
		if i == 3 {
			tk.Reopened = 1
		}
		ticket(50+i, tk)
	}
	for i, d := range open {
		ticket(101+i, record.Ticket{State: record.TicketUnstarted, CreatedAt: end.Add(-d)})
	}
	r, err := b.Line().Report(Options{Target: repo, Summary: summary, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// specFired is the spec rules among the recommendations, in order.
func specFired(r *contract.Report) string {
	var ids []string
	for _, rec := range r.Recommendations {
		if rec.Station == line.Spec {
			ids = append(ids, rec.Rule)
		}
	}
	return strings.Join(ids, " ")
}

func TestSpecRulesReadTheTickets(t *testing.T) {
	r := ticketed(t, 6, false, 200*day, 150*day, 120*day, 100*day, 95*day, 30*day)
	keeps(t, r)
	// Six tickets wait, more than the line starts in a week, so the tickets
	// ready to start are not thin.
	if got := specFired(r); got != "spec.reopened spec.stale-tickets" {
		t.Fatalf("spec rules fired: %s. All: %s", got, fired(r))
	}
	re := advice(t, r, "spec.reopened")
	want := "17 percent of the 6 tickets finished in the window had been finished before and were opened again. " +
		"A ticket that comes back was closed before the work was right, or before anyone agreed on what done means."
	if re.Why != want || re.HoursFreed != nil || re.Confidence != contract.ConfidenceLow || reads(re) != "spec.rework" || len(re.Items) != 0 {
		t.Errorf("reopened: freed %s, %s, reads %s:\n got %s\nwant %s", num(re.HoursFreed), re.Confidence, reads(re), re.Why, want)
	}
	st := advice(t, r, "spec.stale-tickets")
	// Five were created more than 90 days ago. The one from 30 days ago is
	// not counted, and stands in the queue all the same.
	want = "5 tickets have been open for more than 90 days and nobody has started them. The oldest was created 200.0 days ago. " +
		"A list of work nobody will do hides the work that is ready, from people and from agents."
	if st.Why != want || st.Confidence != contract.ConfidenceHigh || st.PlayURL != "" || reads(st) != "spec.queue spec.queue.oldest" {
		t.Errorf("stale: %s, reads %s:\n got %s\nwant %s", st.Confidence, reads(st), st.Why, want)
	}
	if got := keys(st.Items); got != "acme/app#101:4800 acme/app#102:3600 acme/app#103:2880 acme/app#104:2400 acme/app#105:2280" {
		t.Errorf("stale items: %s", got)
	}
	if e := st.Evidence[0]; num(e.Value) != "6" || !strings.HasPrefix(e.Text, "Tickets not started: 6.") {
		t.Errorf("stale evidence: %+v", e)
	}
	if it := st.Items[0]; it.Kind != record.KindTicket || it.URL != "https://example.invalid/issues/101" || it.Title != "Ticket 101" || it.Class != "" {
		t.Errorf("item: %+v", it)
	}

	// A summary counts the tickets and names none.
	sum := advice(t, ticketed(t, 6, true, 200*day, 150*day, 120*day, 100*day, 95*day), "spec.stale-tickets")
	if sum.Items == nil || len(sum.Items) != 0 || !strings.HasPrefix(sum.Why, "5 tickets have been open") {
		t.Errorf("summary: %+v", sum)
	}
}

func TestSpecBacklogThin(t *testing.T) {
	// Six tickets started in 13 weeks and none waits.
	r := ticketed(t, 6, false)
	keeps(t, r)
	if got := specFired(r); got != "spec.reopened spec.backlog-thin" {
		t.Fatalf("spec rules fired: %s. All: %s", got, fired(r))
	}
	th := advice(t, r, "spec.backlog-thin")
	want := "0 tickets are open and not started at the end of the window. The line started 0.5 tickets a week. That is less than one week of work ready to start. " +
		"When the tickets run out the line stands still, whatever the stations after it can do."
	if th.Why != want || th.HoursFreed != nil || th.Confidence != contract.ConfidenceLow || reads(th) != "spec.queue spec.flow" ||
		th.PlayURL != "https://thesoftwarefactory.site/playbook/keep-the-line-fed/" {
		t.Errorf("thin: %s, reads %s, %s:\n got %s\nwant %s", th.Confidence, reads(th), th.PlayURL, th.Why, want)
	}

	// One ticket ready is more than the half a ticket the line starts in a week.
	if got := specFired(ticketed(t, 6, false, 2*day)); got != "spec.reopened" {
		t.Errorf("with one ticket ready: %s", got)
	}
	// Four tickets started is too few to know the pace, and too few to
	// trust a share of them.
	if got := specFired(ticketed(t, 4, false)); got != "" {
		t.Errorf("with four tickets: %s", got)
	}
	// A line with no ticket source is told nothing about tickets.
	if got := specFired(ticketed(t, 0, false)); got != "" {
		t.Errorf("with no tickets: %s", got)
	}
}

// The evidence of ci.failing names the workflows behind the failed rounds.
//
// Twelve changes in a week, in two repositories. Every change runs three
// workflows on its commit. "e2e" fails on 6 of them, "build" is run again on
// 2, "lint" always passes. In acme/lib "e2e" fails once in 2 runs.
func TestCIFailingNamesTheWorkflows(t *testing.T) {
	build := func(summary bool) *contract.Report {
		t.Helper()
		b := NewBuilder(registry(t))
		rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
		for i := 1; i <= 10; i++ {
			at := end.Add(-5*day + time.Duration(i)*h)
			c := change(i, at, review(record.ReviewApproved, at.Add(2*h)), merged(at.Add(2*h)))
			rb.Change(&c)
			for _, wf := range []string{"e2e", "build", "lint"} {
				x := run(fmt.Sprintf("sha%d", i), 1, at, at, at.Add(30*time.Minute), "success")
				x.Workflow = wf
				switch {
				case wf == "e2e" && i <= 6:
					x.Conclusion = "failure"
				case wf == "build" && i <= 2:
					// A run that passed on its second attempt failed once.
					x.Attempt = 2
				}
				rb.CIRun(&x)
			}
			// A cancelled run and one that never ran the checks count for nothing.
			x := run(fmt.Sprintf("sha%d", i), 1, at, at, at.Add(time.Minute), "cancelled")
			x.Workflow = "e2e"
			rb.CIRun(&x)
		}
		// A run before the window is not counted.
		old := change(50, end.Add(-3*week), merged(end.Add(-3*week+h)))
		rb.Change(&old)
		x := run("sha50", 1, end.Add(-3*week), end.Add(-3*week), end.Add(-3*week+h), "failure")
		x.Workflow = "nightly"
		rb.CIRun(&x)
		rb.Done()

		rb = b.Repo(RepoScope{Name: "acme/lib", From: end.Add(-4 * week), SyncedAt: end})
		for i := 1; i <= 2; i++ {
			at := end.Add(-4*day + time.Duration(i)*h)
			c := change(i, at, review(record.ReviewApproved, at.Add(2*h)), merged(at.Add(2*h)))
			c.Repo, c.ID = "acme/lib", record.ChangeID(record.GitHub, "acme/lib", i)
			rb.Change(&c)
			x := run(fmt.Sprintf("sha%d", i), 1, at, at, at.Add(30*time.Minute), "success")
			x.Repo, x.Workflow = "acme/lib", "e2e"
			if i == 1 {
				x.Conclusion = "timed_out"
			}
			rb.CIRun(&x)
		}
		rb.Done()
		r, err := b.Line().Report(Options{Target: "acme", Weeks: 1, History: 4, Now: end, Summary: summary})
		if err != nil {
			t.Fatal(err)
		}
		keeps(t, r)
		return r
	}

	rec := advice(t, build(false), "ci.failing")
	// The most failures first. With two repositories on the line, each
	// workflow says whose it is. "lint" never failed and is not named.
	want := `The workflows whose runs failed, timed out or were run again most often: "e2e" of acme/app in 6 of its 10 runs, "build" of acme/app in 2 of its 10 runs, "e2e" of acme/lib in 1 of its 2 runs.`
	if len(rec.Evidence) < 2 || rec.Evidence[1].Text != want || rec.Evidence[1].Metric != "ci.rework" {
		t.Errorf("evidence:\n got %+v\nwant %s", rec.Evidence, want)
	}

	// A workflow name is a name. A summary leaves the line out.
	sum := advice(t, build(true), "ci.failing")
	for _, e := range sum.Evidence {
		if strings.Contains(e.Text, "e2e") || strings.Contains(e.Text, "workflows") {
			t.Errorf("a summary names workflows: %s", e.Text)
		}
	}
}

func TestFailingFlowsOfAQuietLine(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
	c := change(1, end.Add(-day), merged(end.Add(-day+h)))
	rb.Change(&c)
	x := run("sha1", 1, end.Add(-day), end.Add(-day), end.Add(-day+h), "success")
	rb.CIRun(&x)
	rb.Done()
	if got := b.Line().failingFlows(end.Add(-week), end, 3); got != "" {
		t.Errorf("no run failed, and the line says %q", got)
	}
}
