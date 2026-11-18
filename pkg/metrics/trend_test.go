package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/model"
)

const week = 7 * 24 * time.Hour

// trendPR is a pull request opened on the second day of a week and merged
// leadH hours later, with no review.
func trendPR(n int, agent bool, weekOf time.Time, leadH float64) model.PR {
	opened := weekOf.Add(24 * time.Hour)
	merged := opened.Add(time.Duration(leadH * float64(time.Hour)))
	p := model.PR{Repo: "o/r", Number: n, Author: person("dev"), HeadRef: "fix/x", State: "MERGED",
		CreatedAt: opened, UpdatedAt: merged, MergedAt: &merged, ClosedAt: &merged, Additions: 10, Deletions: 2,
		Commits: []model.Commit{{SHA: fmt.Sprint("s", n), Parents: 1, CommittedAt: opened, Authors: []model.CommitAuthor{{Login: "dev"}}}}}
	if agent {
		p = agentPR(p)
	}
	return p
}

// trendRepo is eight weeks of history. Agents merge 5 pull requests a week in
// the first two weeks, taking 10 h each, and 10 a week in the last two, taking
// 2 h. People merge 5 a week at both ends, taking 4 h. One pull request waits
// for a review from week 2 to week 6.
func trendRepo() (model.RepoData, time.Time) {
	start := t0
	rd := model.RepoData{Repo: "o/r", Since: start, Until: start.Add(8 * week), CISource: "none", DeploySource: "none"}
	n := 0
	add := func(w, count int, agent bool, lead float64) {
		for i := 0; i < count; i++ {
			n++
			rd.PRs = append(rd.PRs, trendPR(n, agent, start.Add(time.Duration(w)*week), lead))
		}
	}
	for _, w := range []int{0, 1} {
		add(w, 5, true, 10)
		add(w, 5, false, 4)
	}
	for _, w := range []int{6, 7} {
		add(w, 10, true, 2)
		add(w, 5, false, 4)
	}
	opened, reviewed := start.Add(week+48*time.Hour), start.Add(5*week+48*time.Hour)
	rd.PRs = append(rd.PRs, model.PR{Repo: "o/r", Number: 999, Author: person("dev"), HeadRef: "fix/slow", State: "OPEN",
		CreatedAt: opened, UpdatedAt: reviewed,
		Commits: []model.Commit{{SHA: "slow", Parents: 1, CommittedAt: opened, Authors: []model.CommitAuthor{{Login: "dev"}}}},
		Reviews: []model.Review{{Author: person("reviewer"), State: "COMMENTED", SubmittedAt: reviewed}}})
	return rd, start
}

func find(t *testing.T, tr *Trend, metric, class string) *Series {
	t.Helper()
	for i := range tr.Series {
		if tr.Series[i].Metric == metric && tr.Series[i].Class == class {
			return &tr.Series[i]
		}
	}
	t.Fatalf("no series %s for %s", metric, class)
	return nil
}

func values(s *Series) string {
	var out []string
	for _, p := range s.Points {
		out = append(out, Value(p, s.Unit))
	}
	return strings.Join(out, " ")
}

func TestTrendCutsTheWindowIntoWholePeriods(t *testing.T) {
	rd, start := trendRepo()
	tr, err := ComputeTrend([]model.RepoData{rd}, registry(t), TrendOptions{Options: Options{Target: "o/r", Now: rd.Until}, Every: week})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Periods) != 8 || tr.Span != 2 || tr.PeriodDays != 7 || !tr.Since.Equal(start) || !tr.Until.Equal(rd.Until) {
		t.Fatalf("periods=%d span=%d days=%v since=%v", len(tr.Periods), tr.Span, tr.PeriodDays, tr.Since)
	}
	if !tr.First.From.Equal(start) || !tr.First.To.Equal(start.Add(2*week)) || !tr.Last.From.Equal(start.Add(6*week)) || !tr.Last.To.Equal(rd.Until) {
		t.Errorf("ends: %+v %+v", tr.First, tr.Last)
	}
	for i, p := range tr.Periods {
		if !p.From.Equal(start.Add(time.Duration(i)*week)) || p.To.Sub(p.From) != week {
			t.Errorf("period %d runs from %v to %v", i, p.From, p.To)
		}
	}

	merged := find(t, &tr, "merged_per_week", ClassAgent)
	if got := values(merged); got != "5 5 0 0 0 0 10 10" {
		t.Errorf("agent merged per week: %s", got)
	}
	near(t, "first", *merged.First.Value, 5)
	near(t, "last", *merged.Last.Value, 10)
	near(t, "change", *merged.Change, 1)
	if !merged.Notable || ChangeWords(merged) != "+100%" {
		t.Errorf("doubling must be notable: %v %q", merged.Notable, ChangeWords(merged))
	}
	if h := find(t, &tr, "merged_per_week", ClassHuman); h.Notable || ChangeWords(h) != "0" {
		t.Errorf("no change must not be notable: %v %q", h.Notable, ChangeWords(h))
	}

	lead := find(t, &tr, "lead_time", ClassAgent)
	near(t, "lead first", *lead.First.Value, 10)
	near(t, "lead last", *lead.Last.Value, 2)
	near(t, "lead change", *lead.Change, -0.8)
	if lead.Points[3].Value != nil || lead.Points[0].N != 5 || lead.Points[7].N != 10 {
		t.Errorf("a week with no merge has no lead time: %+v", lead.Points)
	}

	// The review queue of a past week is rebuilt from timestamps.
	if got := values(find(t, &tr, "waiting_for_review", ClassLine)); got != "0 1 1 1 1 0 0 0" {
		t.Errorf("waiting at the end of each week: %s", got)
	}
	if tr.Periods[4].WaitingForReview != 1 || tr.Periods[5].WaitingForReview != 0 {
		t.Errorf("period rows disagree with the series")
	}
	near(t, "oldest wait at the end of week 5", tr.Periods[4].OldestWaitingH, (3*week + 5*24*time.Hour).Hours())

	total := 0
	for _, p := range tr.Periods {
		total += p.Agent.Merged + p.Human.Merged
		if p.Agent.Merged+p.Human.Merged < MinSample && p.Constraint != nil {
			t.Errorf("a week with %d merged names a constraint", p.Agent.Merged+p.Human.Merged)
		}
	}
	if total != 50 {
		t.Errorf("the weeks hold %d merged pull requests, want 50", total)
	}

	if !strings.HasPrefix(tr.Headline, "The constraint has not moved.") {
		t.Errorf("headline: %s", tr.Headline)
	}
	all := strings.Join(tr.Findings, "\n")
	for _, want := range []string{
		"first 2 weeks", "last 2 weeks",
		"Agent pull requests merged per week rose from 5 to 10.",
		"The median time from open to merge for agent pull requests fell from 10 h to 2 h.",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("findings lack %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "human pull requests merged per week") {
		t.Errorf("a flat series was reported as a change:\n%s", all)
	}
	// CI was not collected, so the trend carries no CI series.
	for _, s := range tr.Series {
		if s.Group == GroupCI {
			t.Errorf("series %s has no data and must be dropped", s.ID)
		}
	}
}

func TestTrendPeriodsAreCountedBackFromTheEnd(t *testing.T) {
	rd, start := trendRepo()
	tr, err := ComputeTrend([]model.RepoData{rd}, registry(t), TrendOptions{Options: Options{Now: rd.Until}, Every: 3 * week})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Periods) != 2 || !tr.Since.Equal(start.Add(2*week)) || tr.Span != 1 {
		t.Errorf("periods=%d since=%v span=%d", len(tr.Periods), tr.Since, tr.Span)
	}
	one, many := tr.PeriodWord()
	if one != "period" || many != "periods" {
		t.Errorf("a period of 21 days is called %q, %q", one, many)
	}

	// A narrower window than the collection.
	tr, err = ComputeTrend([]model.RepoData{rd}, registry(t), TrendOptions{Options: Options{Now: rd.Until, Since: start.Add(4 * week)}, Every: week})
	if err != nil || len(tr.Periods) != 4 || !tr.Since.Equal(start.Add(4*week)) {
		t.Errorf("narrowed: %v periods=%d since=%v", err, len(tr.Periods), tr.Since)
	}
}

func TestTrendNeedsTwoPeriods(t *testing.T) {
	rd, _ := trendRepo()
	reg := registry(t)
	if _, err := ComputeTrend([]model.RepoData{rd}, reg, TrendOptions{Every: 5 * week}); err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Errorf("one period must fail with a hint: %v", err)
	}
	if _, err := ComputeTrend([]model.RepoData{rd}, reg, TrendOptions{}); err == nil {
		t.Error("a zero period must fail")
	}
	if _, err := ComputeTrend(nil, reg, TrendOptions{Every: week}); err == nil {
		t.Error("no repositories must fail")
	}
}

func TestWaitingAt(t *testing.T) {
	opened, ready, reviewed, merged := at(0), at(10), at(30), at(40)
	p := model.PR{Author: person("dev"), State: "MERGED", CreatedAt: opened, ReadyAt: &ready, MergedAt: &merged, ClosedAt: &merged,
		Reviews: []model.Review{
			{Author: person("dev"), State: "COMMENTED", SubmittedAt: at(12)},
			{Author: model.Actor{Login: "review-bot[bot]", Type: "Bot"}, State: "COMMENTED", SubmittedAt: at(13)},
			{Author: person("reviewer"), State: "APPROVED", SubmittedAt: reviewed},
		}}
	for _, c := range []struct {
		h    float64
		want bool
	}{{-1, false}, {5, false}, {20, true}, {35, false}, {50, false}} {
		got, wait := waitingAt(&p, human().Class, at(c.h))
		if got != c.want {
			t.Errorf("at hour %v: waiting=%v, want %v", c.h, got, c.want)
		}
		if got {
			near(t, "wait", wait, c.h-10)
		}
	}
}

func TestChangeWordsAndValue(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		s    Series
		want string
	}{
		{Series{Unit: UnitShare, First: Point{Value: f(0.5), N: 9}, Last: Point{Value: f(0.62), N: 9}}, "+12 points"},
		{Series{Unit: UnitHours, First: Point{Value: f(10), N: 9}, Last: Point{Value: f(7), N: 9}}, "-30%"},
		{Series{Unit: UnitHours, First: Point{Value: f(1), N: 9}, Last: Point{Value: f(15), N: 9}}, "x15"},
		{Series{Unit: UnitPerWeek, First: Point{Value: f(0), N: 0}, Last: Point{Value: f(3), N: 3}}, "new"},
		{Series{Unit: UnitPerWeek, First: Point{Value: f(0), N: 0}, Last: Point{Value: f(0), N: 0}}, "0"},
		{Series{Unit: UnitHours, First: Point{}, Last: Point{Value: f(3), N: 9}}, "n/a"},
	} {
		c.s.setChange()
		if got := ChangeWords(&c.s); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.s.Last, got, c.want)
		}
	}
	if Value(Point{Value: f(84.6)}, UnitPerWeek) != "84.6" || Value(Point{Value: f(42)}, UnitPerWeek) != "42" || Value(Point{}, UnitHours) != "n/a" {
		t.Error("Value prints a number per week badly")
	}
	// A change on too few pull requests is never notable.
	thin := Series{Unit: UnitHours, First: Point{Value: f(1), N: 2}, Last: Point{Value: f(30), N: 9}}
	thin.setChange()
	if thin.Notable {
		t.Error("a change from 2 pull requests must not be notable")
	}
}
