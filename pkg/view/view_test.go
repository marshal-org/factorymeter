package view

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

func p(v float64) *float64 { return &v }

func TestNumber(t *testing.T) {
	cases := []struct {
		unit line.Unit
		v    *float64
		want string
	}{
		{line.Hours, nil, "-"},
		{line.Hours, p(0), "0m"},
		{line.Hours, p(0.5), "30m"},
		{line.Hours, p(5), "5.0h"},
		{line.Hours, p(47.9), "47.9h"},
		{line.Hours, p(72), "3.0d"},
		{line.Share, p(0), "0%"},
		{line.Share, p(0.004), "<1%"},
		{line.Share, p(0.25), "25%"},
		{line.Share, p(1), "100%"},
		{line.PerWeek, p(3), "3.0"},
		{line.PerWeek, p(256.61), "256.6"},
		{line.Count, p(4), "4"},
		{line.Lines, p(274), "274"},
	}
	for _, c := range cases {
		if got := Number(string(c.unit), c.v); got != c.want {
			t.Errorf("Number(%s, %v) = %q, want %q", c.unit, c.v, got, c.want)
		}
	}
}

func TestChange(t *testing.T) {
	cases := []struct {
		unit   line.Unit
		change *float64
		want   string
	}{
		{line.Hours, nil, ""},
		{line.Hours, p(0.2), "+20%"},
		{line.Hours, p(-0.5), "-50%"},
		{line.Hours, p(8.9), "+890%"},
		// 64 times as much reads better than "+6300%".
		{line.Hours, p(63), "64x"},
		// A share moves in points.
		{line.Share, p(0.05), "+5 pts"},
		{line.Share, p(-0.12), "-12 pts"},
	}
	for _, c := range cases {
		if got := Change(&contract.Series{Unit: string(c.unit), Change: c.change}); got != c.want {
			t.Errorf("Change(%s, %v) = %q, want %q", c.unit, c.change, got, c.want)
		}
	}
	if Change(nil) != "" {
		t.Error("no series must print nothing")
	}
}

func points(vals ...*float64) []contract.Point {
	out := make([]contract.Point, len(vals))
	for i, v := range vals {
		out[i] = contract.Point{Value: v}
	}
	return out
}

func TestSpark(t *testing.T) {
	// The top value gets the full bar. 2 of 5 is 2.8 of 7 steps, drawn as
	// step 3. 4 of 5 is 5.6, drawn as step 6. A week with nothing is a dot.
	if got := Spark(&contract.Series{Points: points(p(5), p(0), nil, p(2), p(4))}); got != "█▁·▄▇" {
		t.Errorf("spark = %q", got)
	}
	if got := Spark(&contract.Series{Points: points(p(0), p(0))}); got != "▁▁" {
		t.Errorf("all zero = %q", got)
	}
	if Spark(nil) != "" || Spark(&contract.Series{}) != "" {
		t.Error("no points must draw nothing")
	}
}

func TestCell(t *testing.T) {
	cases := []struct {
		s    *contract.Series
		want string
	}{
		{nil, ""},
		{&contract.Series{Unit: "hours"}, "-"},
		// No previous value: nothing to point at.
		{&contract.Series{Unit: "hours", Value: p(2)}, "2.0h"},
		{&contract.Series{Unit: "hours", Value: p(2), Previous: p(1), Trend: contract.TrendWorse}, "2.0h ▲!"},
		{&contract.Series{Unit: "hours", Value: p(1), Previous: p(2), Trend: contract.TrendBetter}, "1.0h ▼"},
		{&contract.Series{Unit: "per_week", Value: p(2), Previous: p(3), Trend: contract.TrendWorse}, "2.0 ▼!"},
		// Too small a move to call: no arrow.
		{&contract.Series{Unit: "hours", Value: p(1.01), Previous: p(1), Trend: contract.TrendFlat}, "1.0h"},
		// Too few items: marked, and no arrow.
		{&contract.Series{Unit: "hours", Value: p(2), Previous: p(1), Trend: contract.TrendUnknown, Thin: true}, "2.0h ~"},
		// A number with no better direction still shows which way it moved.
		{&contract.Series{Unit: "share", Value: p(0.5), Previous: p(0.2), Trend: contract.TrendUnknown}, "50% ▲"},
		// Held hours are a sum over a week and stay in hours.
		{&contract.Series{Metric: "release.held.total", Unit: "hours", Value: p(8382)}, "8382h"},
		{&contract.Series{Metric: "code.held.total", Unit: "hours", Value: p(2.25)}, "2.2h"},
	}
	for _, c := range cases {
		if got := cell(c.s); got != c.want {
			t.Errorf("cell(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
	if got := trendWord(&contract.Series{Value: p(2), Previous: p(1), Trend: contract.TrendUnknown, Thin: true}); got != "too few to call" {
		t.Errorf("trend word = %q", got)
	}
	if got := trendWord(&contract.Series{Value: p(2), Trend: contract.TrendUnknown}); got != "" {
		t.Errorf("trend word with nothing before = %q", got)
	}
}

// sample is a report written by hand: two weeks, review is the constraint.
func sample() *contract.Report {
	until := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	var periods []contract.Period
	for i := 4; i > 0; i-- {
		periods = append(periods, contract.Period{From: until.Add(-time.Duration(i) * week), To: until.Add(-time.Duration(i-1) * week)})
	}
	whole, agent, human := contract.Slice{}, contract.Slice{Class: "agent"}, contract.Slice{Class: "human"}
	station := func(id, status, missing string) contract.Station {
		def, _ := line.StationByID(id)
		return contract.Station{ID: id, Name: def.Name, Status: status, Missing: missing}
	}
	review := station(line.Review, contract.StatusMeasured, "")
	review.Constraint, review.HeldShare = true, p(0.6)
	review.Oldest = []contract.Item{{Kind: "change", Key: "acme/app#13", Title: "Wait for me", Class: "human", Hours: 30}}
	merge := station(line.Merge, contract.StatusMeasured, "")
	merge.HeldShare = p(0.4)
	return &contract.Report{
		Schema: contract.Version, Target: "acme/app",
		Window:  contract.Window{Since: until.Add(-2 * week), Until: until, PeriodDays: 7, Periods: periods, Previous: &contract.Period{From: until.Add(-4 * week), To: until.Add(-2 * week)}},
		Verdict: contract.Verdict{Headline: "Review is the constraint.", Constraint: line.Review, Detail: "Changes stood there."},
		Stations: []contract.Station{
			station(line.Spec, contract.StatusNotConnected, "No tickets are in the store."),
			station(line.Code, contract.StatusMeasured, ""),
			station(line.CI, contract.StatusNotConnected, ""),
			station(line.Verify, contract.StatusNotConnected, ""),
			review, merge,
			station(line.Release, contract.StatusNotConnected, ""),
		},
		Metrics: []contract.Series{
			{Metric: "review.flow", Slice: whole, Unit: "per_week", Value: p(3), N: 6, Previous: p(2.5), PreviousN: 5, Change: p(0.2), Trend: contract.TrendBetter, Points: points(p(5), p(0), p(2), p(4))},
			{Metric: "review.wait.p50", Slice: whole, Unit: "hours", Value: p(3.5), N: 6, Previous: p(1), PreviousN: 5, Change: p(2.5), Trend: contract.TrendWorse, Points: points(p(1), nil, p(1.5), p(4.5))},
			{Metric: "review.rework", Slice: whole, Unit: "share", Value: p(0.25), N: 6, Previous: p(0.2), PreviousN: 5, Change: p(0.05), Trend: contract.TrendWorse},
			{Metric: "review.queue", Slice: whole, Unit: "count", Value: p(2), N: 2, Previous: p(2), PreviousN: 2, Change: p(0), Trend: contract.TrendFlat},
			{Metric: "review.held.share", Slice: whole, Unit: "share", Value: p(0.6), N: 6, Previous: p(0.59), PreviousN: 5, Change: p(0.01), Trend: contract.TrendFlat},
			{Metric: "review.held.total", Slice: whole, Unit: "hours", Value: p(8382), N: 6, Previous: p(130), PreviousN: 5, Change: p(63.4769), Trend: contract.TrendWorse, Points: points(p(1), p(2))},
			{Metric: "line.flow", Slice: whole, Unit: "per_week", Value: p(3), N: 6},
			{Metric: "line.agent_share", Slice: whole, Unit: "share", Value: p(0.3333), N: 6},
			{Metric: "line.flow", Slice: agent, Unit: "per_week", Value: p(1), N: 2},
			{Metric: "line.flow", Slice: human, Unit: "per_week", Value: p(2), N: 4},
			{Metric: "review.wait.p50", Slice: agent, Unit: "hours", Value: p(5.5), N: 2, Thin: true, Trend: contract.TrendUnknown},
			{Metric: "line.flow", Slice: contract.Slice{Class: "agent", Agent: "claude-code"}, Unit: "per_week", Value: p(1), N: 2},
		},
		Coverage: contract.Coverage{
			Repos: []contract.RepoCoverage{
				{Repo: "acme/app", From: time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), SyncedAt: until, Changes: 14},
				{Repo: "acme/lib", Changes: 1},
			},
			Sources: []contract.SourceCoverage{
				{Kind: "change", Source: "github", Records: 9},
				{Kind: "ci_run", Records: 0, Note: "runs | attempts"},
			},
		},
		Limits: []string{"A limit."},
	}
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	name := func(id string) string {
		m, ok := line.ByID(id)
		if !ok {
			t.Fatalf("no metric %s", id)
		}
		return label(m)
	}
	for _, want := range []string{
		"# Delivery line: acme/app\n",
		"2 weeks, 2026-10-19 to 2026-11-02. Compared with the 2 weeks before. History: 4 weeks.\n",
		"**Review is the constraint.**\n",
		"## The line\n",
		"| Station | Status | Out a week | Wait | Work | Rework | Queue | Held | Out, by week |\n| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n",
		// A station that is not connected has no numbers.
		"| Spec | not connected |  |  |  |  |  |  |  |\n",
		// Flow up and better. Wait and rework up and worse. The queue and
		// the held share moved too little to mark. No work series: empty.
		"| Review (constraint) | measured | 3.0 ▲ | 3.5h ▲! |  | 25% ▲! | 2 | 60% | █▁▄▇ |\n",
		"- Spec: No tickets are in the store.\n",
		"## Agents and people\n",
		"Agents wrote 33% of the changes merged by agents and people.",
		"| " + name("line.flow") + " | 1.0 | 2.0 | - |\n",
		"| " + name("review.wait.p50") + " | 5.5h ~ | - | - |\n",
		"### By agent\n",
		"| claude-code | 1.0 | - | - | - | - | - |\n",
		"## Each station\n",
		"### Review\n",
		"| Number | Now | Before | Change | Trend | Items | By week |\n",
		// 1 and 1.5 of 4.5 are 1.6 and 2.3 of 7 steps: both drawn as step 2.
		"| " + name("review.wait.p50") + " | 3.5h ▲! | 1.0h | +250% | worse | 6 | ▃·▃█ |\n",
		"| " + name("review.rework") + " | 25% ▲! | 20% | +5 pts | worse | 6 |  |\n",
		"| " + name("review.held.total") + " | 8382h ▲! | 130h | 64x | worse | 6 | ▅█ |\n",
		"Standing longest in the queue:\n",
		"- acme/app#13, standing 30.0h, human: Wait for me\n",
		"### The whole line\n",
		"## Coverage\n",
		"| acme/app | 2026-05-04 | 2026-11-02 00:00 UTC | 14 |\n",
		"| acme/lib | - | not finished | 1 |\n",
		"| change | github | 9 |  |\n",
		// A bar inside a cell would break the table.
		"| ci run | not connected | 0 | runs \\| attempts |\n",
		"## Limits\n\n- A limit.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, not := range []string{"### Spec\n", "### CI\n", "### Release\n", "Where to invest"} {
		if strings.Contains(out, not) {
			t.Errorf("the page holds %q", not)
		}
	}
	if t.Failed() {
		t.Log("\n" + out)
	}

	// The sections come in the order of the line.
	last := -1
	for _, h := range []string{"## The line", "## Agents and people", "## Each station", "### Code", "### Review", "### Merge", "### The whole line", "## Coverage", "## Limits"} {
		at := strings.Index(out, h+"\n")
		if at <= last {
			t.Errorf("%q is missing or out of order", h)
		}
		last = at
	}
}

func TestRecommendationsAndSummary(t *testing.T) {
	r := sample()
	r.Recommendations = []contract.Recommendation{
		{
			Rule: "review.slow", Station: line.Review, Title: "Cut the wait for review", Why: "Review is the constraint.",
			HoursFreed: p(8), Confidence: contract.ConfidenceHigh, PlayURL: "https://example.invalid/play/",
			Evidence: []contract.Evidence{
				{Metric: "review.held.total", Value: p(12), Versus: p(4), Text: "Changes were held 12 hours a week at review."},
				{Metric: "review.queue", Value: p(1)}, // evidence with no sentence is left out of the page
			},
			Items: []contract.Item{{Kind: "change", Key: "acme/app#13", Title: "Wait for me", Class: "human", Hours: 30}},
		},
		{Rule: "ci.failing", Station: line.CI, Title: "Cut the share of CI rounds that fail", Why: "A quarter fail.", HoursFreed: p(120.4), Confidence: contract.ConfidenceMedium},
		{Rule: "code.stale-drafts", Station: line.Code, Title: "Finish or close the drafts that stopped moving", Why: "Three drafts stand.", Confidence: contract.ConfidenceLow},
	}
	r.Target, r.Summary = "", true
	r.Window.Previous = nil
	var b bytes.Buffer
	if err := Markdown(&b, r); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"# Delivery line\n", "2 weeks, 2026-10-19 to 2026-11-02. History: 4 weeks.\n",
		// Hours under ten keep one decimal. A gain that is not sized is empty.
		"## Where to invest\n\n" +
			"| # | What to do | Station | Hours freed a week | Confidence |\n| ---: | --- | --- | ---: | --- |\n" +
			"| 1 | Cut the wait for review | Review | 8.0 | high |\n" +
			"| 2 | Cut the share of CI rounds that fail | CI | 120 | medium |\n" +
			"| 3 | Finish or close the drafts that stopped moving | Code |  | low |\n",
		"`factorymeter rules` lists every rule and when it fires.\n",
		"### 1. Cut the wait for review\n\nReview is the constraint.\n\n" +
			"- Changes were held 12 hours a week at review.\n" +
			"- Look at acme/app#13, standing 30.0h, human: Wait for me\n" +
			"- Practice: https://example.invalid/play/\n",
		// A recommendation with no evidence, item or practice is a heading and a paragraph.
		"### 3. Finish or close the drafts that stopped moving\n\nThree drafts stand.\n\n## Agents and people\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q:\n%s", want, out)
		}
	}
	// Where to invest comes right after the line and before the detail.
	if a, b := strings.Index(out, "## Where to invest"), strings.Index(out, "## Agents and people"); a < 0 || a > b {
		t.Error("where to invest must come before agents and people")
	}

	b.Reset()
	if err := Text(&b, r); err != nil {
		t.Fatal(err)
	}
	out = b.String()
	for _, want := range []string{"\nWHERE TO INVEST\n", "\n1. Cut the wait for review\n", "  * Look at acme/app#13, standing 30.0h, human: Wait for me\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the text lacks %q:\n%s", want, out)
		}
	}
}

func TestNoRecommendationsNoSection(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, sample()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "Where to invest") {
		t.Error("a report with no recommendations has no section for them")
	}
}

func TestText(t *testing.T) {
	var b bytes.Buffer
	if err := Text(&b, sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"DELIVERY LINE: ACME/APP\n=======================\n",
		"Review is the constraint.\n",
		"\nTHE LINE\n",
		"  * Spec: No tickets are in the store.\n",
		"\nReview\n",
		"  * acme/app#13, standing 30.0h, human: Wait for me\n",
		"\nLIMITS\n\n  * A limit.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the text lacks %q", want)
		}
	}
	if strings.Contains(out, "**") || strings.Contains(out, "|  |") {
		t.Error("the text holds Markdown")
	}
	// Columns line up: numbers are set to the right, so the two rows of
	// the coverage table end in the same column.
	var ends []int
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  acme/") {
			ends = append(ends, width(l))
		}
	}
	if len(ends) != 2 || ends[0] != ends[1] {
		t.Errorf("coverage rows end at %v", ends)
	}
	// No line ends in a space.
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("line ends in a space: %q", l)
		}
	}
	if t.Failed() {
		t.Log("\n" + out)
	}
}
