package view

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

func htmlOf(t *testing.T, r *contract.Report, opt HTMLOptions) string {
	t.Helper()
	var b bytes.Buffer
	if err := HTML(&b, r, opt); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestHTML(t *testing.T) {
	out := htmlOf(t, sample(), HTMLOptions{})
	if !strings.HasPrefix(out, "<!doctype html>\n") {
		t.Errorf("the page starts with %.40q", out)
	}
	for _, want := range []string{
		"<title>Delivery line: acme/app</title>",
		"<h1>Review is the constraint.</h1>",
		`<p class="lede">Changes stood there.</p>`,
		"Every number covers the 2 weeks from Oct 19, 2026 to Nov 1, 2026. A count is the average week of those weeks.",
		"Arrows compare with the 2 weeks before, Oct 5, 2026 to Oct 18, 2026. The charts go back 4 weeks",
		// Each value says what it is drawn from and what it is compared with.
		"Average week: 6 in 2 weeks",
		"The 2 weeks before: 2.5 (&#43;20%, better)",
		// The week each point stands for, once for every chart.
		`<body data-weeks="Week to Oct 12, 2026|Week to Oct 19, 2026|Week to Oct 26, 2026|Week to Nov 2, 2026">`,
		// Review holds 60 percent of the line: strained, and the reason is told.
		`<li class="st h-bad is-constraint" data-station="review">`,
		"<li>Load: holds 60% of the hours changes spend on the line.</li>",
		"<li>Rework: changes sent back in review, 25%.</li>",
		"<li>Wait: the median got worse: 3.5h, from 1.0h in the 2 weeks before.</li>",
		// Code has no number to grade, and merge holds 40 percent.
		`<li class="st h-none" data-station="code">`,
		`<li class="st h-bad" data-station="merge">`,
		`<li class="st h-off is-off" data-station="spec">`,
		// A station that is not connected says what it needs.
		`<section class="pane" id="station-spec" data-name="Spec">`,
		`<p class="missing">No tickets are in the store.</p>`,
		"<h4>What it needs</h4><p>Tickets. GitHub issues are read",
		// What a number counts is a note on its info mark.
		`aria-describedby="c-review.flow">i</button><span class="pop" id="c-review.flow" role="tooltip">`,
		`<span class="pop" id="health-rule" role="tooltip">`,
		// The sections are pages.
		`<a href="#overview">Overview</a>`,
		`<a class="st-a" href="#station-review">`,
		`<section class="pane" id="station-review" data-name="Review" data-constraint>`,
		`<section class="pane" id="station-code" data-name="Code">`,
		`<section class="pane" id="station-line" data-name="The whole line">`,
		// Bars carry the value of each week. A line carries the items too,
		// and a week with nothing is an empty cell.
		`data-v="5.0|0.0|2.0|4.0"`,
		`data-v="1.0h;0||1.5h;0|4.5h;0"`,
		// A number with no weekly history says so, and draws no empty frame.
		`<p class="none">No week by week data.</p>`,
		// An item with no link is plain text.
		`<span class="key">acme/app#13</span> <span class="it-t">Wait for me</span> <span class="it-m">standing 30.0h, human</span>`,
		"Agents wrote 33% of the changes merged by agents and people.",
		"<h3>By agent</h3>",
		`<th scope="row">claude-code</th>`,
		`<th scope="row">acme/lib</th><td>-</td><td>not finished</td><td class="num">1</td>`,
		`<td>runs | attempts</td>`,
		"<h3>Limits</h3><ul class=\"plain\"><li>A limit.</li></ul>",
		"nothing on this page loads from the network",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, not := range []string{
		// No recommendation: no section, and no link to one.
		`id="invest"`, `href="#invest"`,
		// A page in a file cannot ask for another window, repository or format.
		`class="seg"`, `class="menu"`, "report.json", "By repository",
		// Nothing is styled in place, and nothing loads.
		" style=", " src=", "<link", "<img", "<iframe",
	} {
		if strings.Contains(out, not) {
			t.Errorf("the page holds %q", not)
		}
	}
	if n := strings.Count(out, `is-off" data-station=`); n != 4 {
		t.Errorf("%d stations are drawn as not connected, want 4", n)
	}
	if n := strings.Count(out, "<script"); n != 1 {
		t.Errorf("the page holds %d scripts", n)
	}
	// The one link out is the simulator, and only a click follows it.
	if n := strings.Count(out, `href="http`); n != 1 || !strings.Contains(out, `<a class="sim-a" href="https://thesoftwarefactory.site/lights-out/`) {
		t.Errorf("the page holds %d links out, want the one to the simulator", n)
	}
	// The sections come in the order of the line.
	last := -1
	for _, h := range []string{`<section id="overview" class="page">`, `<div id="line" class="block">`, `id="station-spec"`, `id="station-code"`, `id="station-review"`,
		`id="station-merge"`, `id="station-release"`, `id="station-line"`, `<section id="agents" class="page">`, `<section id="coverage" class="page">`} {
		at := strings.Index(out, h)
		if at <= last {
			t.Errorf("%q is missing or out of order", h)
		}
		last = at
	}
}

// The policy lets the style and the script of the page run by their hash. A
// byte of difference between what is hashed and what is written would leave a
// page with no style and no script, so the test hashes what was written.
func TestHTMLPolicy(t *testing.T) {
	out := htmlOf(t, sample(), HTMLOptions{})
	meta := regexp.MustCompile(`<meta http-equiv="Content-Security-Policy" content="([^"]*)">`).FindStringSubmatch(out)
	if meta == nil {
		t.Fatal("the page carries no policy")
	}
	policy := html.UnescapeString(meta[1])
	if policy != CSP() {
		t.Errorf("the page says %q, CSP() says %q", policy, CSP())
	}
	for _, tag := range []string{"style", "script"} {
		body := regexp.MustCompile(`(?s)<` + tag + `>(.*?)</` + tag + `>`).FindStringSubmatch(out)
		if body == nil || len(body[1]) < 500 {
			t.Fatalf("the page carries no %s", tag)
		}
		sum := sha256.Sum256([]byte(body[1]))
		if pin := tag + "-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; !strings.Contains(policy, pin) {
			t.Errorf("the policy does not allow the %s that was written: want %s in %s", tag, pin, policy)
		}
	}
	for _, want := range []string{"default-src 'none'", "base-uri 'none'", "form-action 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy lacks %q", want)
		}
	}
	for _, not := range []string{"unsafe-inline", "unsafe-eval", "http", "*"} {
		if strings.Contains(policy, not) {
			t.Errorf("the policy holds %q", not)
		}
	}
}

// A report can be a file somebody else wrote. Nothing it carries may become
// markup, and no link it carries is followed unless it is plain https.
func TestHTMLHostile(t *testing.T) {
	const bad = `"><script>alert(1)</script><img src=x onerror=alert(1)>`
	r := sample()
	r.Target = bad
	r.Verdict.Headline, r.Verdict.Detail = "</h1>"+bad, bad
	r.Limits = []string{bad}
	r.Stations[4].Name = bad
	r.Stations[4].Oldest = []contract.Item{
		{Key: bad, Title: bad, URL: "javascript:alert(1)", Hours: 3},
		{Key: "plain", URL: "http://example.invalid/2", Hours: 3},
		{Key: "quoted", URL: `https://example.invalid/"onmouseover="x`, Hours: 3},
		{Key: "data", URL: "data:text/html,x", Hours: 3},
		{Key: "good", URL: "https://example.invalid/5", Hours: 3},
	}
	r.Coverage.Repos[0].Repo = bad
	r.Coverage.Sources[0].Note = bad
	r.Metrics = append(r.Metrics, contract.Series{Metric: "line.flow", Slice: contract.Slice{Class: "agent", Agent: bad}, Unit: "per_week", Value: p(1), N: 2})
	r.Recommendations = []contract.Recommendation{{Rule: bad, Station: `review" onclick="x`, Title: bad, Why: bad, Confidence: bad, PlayURL: "//example.invalid/play",
		Evidence: []contract.Evidence{{Text: bad}}, Items: []contract.Item{{Key: bad, URL: "vbscript:x", Title: bad}}}}
	out := htmlOf(t, r, HTMLOptions{Nav: &Nav{JSON: "/report.json", Repos: []Link{{Label: bad, URL: "javascript:alert(2)"}, {Label: "x", URL: "/"}},
		Windows: []Link{{Label: bad, URL: "/?since=4w"}}}})

	if n := strings.Count(out, "<script"); n != 1 {
		t.Errorf("the page holds %d scripts, want the one of the page", n)
	}
	for _, not := range []string{"<img", "javascript:alert", "vbscript:", "data:text", "http://example.invalid", "//example.invalid/play", ` onclick="`, ` onerror="`,
		` onmouseover="`, "</h1></h1>"} {
		if strings.Contains(out, not) {
			t.Errorf("the page holds %q", not)
		}
	}
	// The one good link is kept, and it does not tell the other side where
	// the reader came from.
	if want := `<a href="https://example.invalid/5" rel="noreferrer noopener">good</a>`; !strings.Contains(out, want) {
		t.Errorf("the page lacks %q", want)
	}
	// Besides the link to the simulator, which the page builds from its own
	// address and numbers, no other link from the report gets through.
	if n := strings.Count(out, `href="https://`) - strings.Count(out, `href="https://thesoftwarefactory.site/lights-out/?`); n != 1 {
		t.Errorf("the page holds %d outside links from the report, want 1", n)
	}
	// What was text is still there to read.
	if !strings.Contains(out, html.EscapeString("<script>alert(1)</script>")) {
		t.Error("the hostile text is gone, where it should be shown as text")
	}
}

func TestHTMLNav(t *testing.T) {
	r := sample()
	r.Target = "acme"
	r.Metrics = append(r.Metrics,
		contract.Series{Metric: "line.flow", Slice: contract.Slice{Repo: "acme/app"}, Unit: "per_week", Value: p(3), N: 6},
		contract.Series{Metric: "line.flow", Slice: contract.Slice{Repo: "acme/lib"}, Unit: "per_week", Value: p(1), N: 2})
	out := htmlOf(t, r, HTMLOptions{Nav: &Nav{
		Windows:   []Link{{Label: "4 weeks", URL: "/?since=4w"}, {Label: "13 weeks", URL: "/", Current: true}},
		Repos:     []Link{{Label: "acme/app", URL: "/?repo=acme%2Fapp", Current: true}},
		RepoLabel: "All repositories", AllRepos: "/",
		JSON: "/report.json?since=4w",
	}})
	for _, want := range []string{
		`<div class="seg" role="group" aria-label="Window"><a href="/?since=4w">4 weeks</a><a href="/" class="on" aria-current="true">13 weeks</a></div>`,
		`<details class="menu"><summary>All repositories</summary>`,
		`<label><input type="checkbox" name="repo" value="acme/app" checked>acme/app</label>`,
		"form-action &#39;self&#39;",
		"<h3>By repository</h3>",
		// A repository the page can narrow to is a link in its row. One it
		// cannot is plain text.
		`<th scope="row"><a href="/?repo=acme%2Fapp">acme/app</a></th>`,
		`<th scope="row">acme/lib</th><td class="num"><span class="v">1.0</span>`,
		`<a href="/report.json?since=4w">the same report as JSON</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

func TestHTMLRecommendationsAndSummary(t *testing.T) {
	r := sample()
	r.Recommendations = []contract.Recommendation{
		{Rule: "review.wait.long", Station: line.Review, Title: "Answer reviews sooner", Why: "Changes wait.", HoursFreed: p(12), Confidence: contract.ConfidenceHigh,
			PlayURL:  "https://example.invalid/play",
			Evidence: []contract.Evidence{{Text: "Wait for a first review: 3.5h, from 1.0h."}, {}},
			Items:    []contract.Item{{Kind: "change", Key: "acme/app#13", URL: "https://example.invalid/13", Title: "Wait for me", Class: "human", Hours: 30}}},
		// Sized at nothing: the rule fired, and there is no gain to show.
		{Rule: "merge.wait.long", Station: line.Merge, Title: "Merge sooner", Why: "Approved changes wait.", HoursFreed: p(0), Confidence: contract.ConfidenceLow},
		// A station that is not connected has a panel too, and is linked.
		{Rule: "spec.missing", Station: line.Spec, Title: "Connect the tickets", Why: "No tickets.", Confidence: contract.ConfidenceMedium},
	}
	out := htmlOf(t, r, HTMLOptions{})
	for _, want := range []string{
		`<a href="#invest">Where to invest</a>`,
		`<span class="rank">1</span>`,
		"<h3>Answer reviews sooner</h3>",
		`<a href="#station-review">Review</a>`,
		"confidence high",
		`<p class="gain-l">hours freed a week</p>`,
		"<li>Wait for a first review: 3.5h, from 1.0h.</li>",
		"<summary>1 item to look at</summary>",
		`<a href="https://example.invalid/13" rel="noreferrer noopener">acme/app#13</a>`,
		`<a href="https://example.invalid/play" rel="noreferrer noopener">The practice, in the playbook</a>`,
		`<p class="rec-m"><a href="#station-spec">Spec</a> <span class="sep">`,
		// The line names the constraint, the station next in line, and the
		// way to the full list.
		`Constraint: <a href="#station-review">Review</a>, holds 60% of the hours`,
		`Next in line: <a href="#station-merge">Merge</a>, holds 40% of the hours`,
		`<a class="go" href="#invest">Where to invest (3)</a>`,
		`<span class="badge next">Next</span>`,
		"Once Review holds less, Merge is next in line: it holds 40% of the hours.",
		// Each station lists what to invest in there, and links to it.
		`<li class="rec" id="rec-1">`,
		`<h4>Where to invest here</h4><ol class="mini"><li><a href="#rec-1"><span class="rank sm">1</span><span>Answer reviews sooner</span></a><span class="it-m">12 hours freed a week</span></li></ol>`,
		`<li><a href="#rec-2"><span class="rank sm">2</span><span>Merge sooner</span></a></li>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if n := strings.Count(out, `class="gain"`); n != 1 {
		t.Errorf("%d recommendations show a gain, want 1", n)
	}
	// A station the report does not list has no panel, and is not linked.
	r.Recommendations[2].Station = "elsewhere"
	if out := htmlOf(t, r, HTMLOptions{}); !strings.Contains(out, `<p class="rec-m">elsewhere <span class="sep">`) {
		t.Error("a recommendation links to a station that has no panel")
	}
	if strings.Contains(out, "<li></li>") {
		t.Error("evidence with no text is printed as an empty line")
	}

	// A summary names nothing.
	r.Target, r.Summary = "", true
	out = htmlOf(t, r, HTMLOptions{})
	for _, want := range []string{"<title>Delivery line</title>", `<span class="pill">summary, no names</span>`} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q", want)
		}
	}
	if strings.Contains(out, "Delivery line of") {
		t.Error("the summary names a target")
	}
}

func TestChart(t *testing.T) {
	r := sample()
	flow, _ := line.ByID("review.flow")
	wait, _ := line.ByID("review.wait.p50")
	series := func(pts []contract.Point) *contract.Series {
		return &contract.Series{Unit: string(wait.Unit), Points: pts}
	}
	draw := func(m line.Metric, s *contract.Series) string {
		return string(chart{r: r, m: m, tracks: []track{{key: "s1", s: s}}, label: `a "label"`}.draw())
	}
	// Nothing to draw is no drawing: an empty frame reads as a week of zero.
	for _, s := range []*contract.Series{series(nil), series(points(nil, nil, nil, nil))} {
		if got := draw(wait, s); got != "" {
			t.Errorf("an empty series drew %.80q", got)
		}
	}

	// A count is bars. The two weeks before the window are dimmed, and a week
	// of zero has no bar.
	bars := draw(flow, &contract.Series{Unit: "per_week", Points: points(p(5), p(0), p(2), p(4))})
	if n := strings.Count(bars, "<path"); n != 3 {
		t.Errorf("%d bars, want 3:\n%s", n, bars)
	}
	if n := strings.Count(bars, `class="s1 pre"`); n != 1 {
		t.Errorf("%d bars are dimmed, want 1", n)
	}
	for _, want := range []string{`aria-label="a &#34;label&#34;"`, `role="img"`, `class="from"`, `data-names=""`, `data-keys="s1"`, `>5</text>`, ">Nov 2</text>"} {
		if !strings.Contains(bars, want) {
			t.Errorf("the bars lack %q:\n%s", want, bars)
		}
	}

	// A median is a line. It joins two weeks only when both stand on enough
	// items: a week on too few is a faint dot, and a missing week is a gap.
	pts := []contract.Point{{Value: p(1), N: 9}, {Value: p(2), N: 9}, {Value: p(3), N: 1}, {Value: p(4), N: 9}}
	ln := draw(wait, series(pts))
	if n := strings.Count(ln, `class="ln s1 pre"`); n != 1 {
		t.Errorf("%d dimmed line paths, want 1:\n%s", n, ln)
	}
	if strings.Contains(ln, `class="ln s1"`) {
		t.Errorf("a line crosses a week that stands on too few items:\n%s", ln)
	}
	if n := strings.Count(ln, `class="s1 few"`); n != 1 {
		t.Errorf("%d faint dots, want 1:\n%s", n, ln)
	}
	if n := strings.Count(ln, `class="end s1"`); n != 1 {
		t.Errorf("%d end markers, want 1:\n%s", n, ln)
	}
	if !strings.Contains(ln, `data-v="1.0h;9|2.0h;9|3.0h;1|4.0h;9"`) {
		t.Errorf("the line lacks its values:\n%s", ln)
	}

	// Two series on one chart: both are named for the tooltip.
	both := string(chart{r: r, m: flow, stack: true, label: "x", tracks: []track{
		{name: "Agents", key: "s1", s: &contract.Series{Unit: "per_week", Points: points(p(1), p(1), p(1), p(1))}},
		{name: "People", key: "s2", s: &contract.Series{Unit: "per_week", Points: points(p(2), nil, p(2), p(2))}},
	}}.draw())
	for _, want := range []string{`data-names="Agents|People"`, `data-keys="s1|s2"`, `data-v="1.0|1.0|1.0|1.0~2.0||2.0|2.0"`} {
		if !strings.Contains(both, want) {
			t.Errorf("the stack lacks %q:\n%s", want, both)
		}
	}
	if n := strings.Count(both, "<path"); n != 7 {
		t.Errorf("%d segments, want 7", n)
	}
}

func TestCeiling(t *testing.T) {
	cases := []struct {
		unit line.Unit
		v    float64
		want float64
	}{
		{line.PerWeek, 0, 1},
		{line.PerWeek, 7, 10},
		{line.PerWeek, 2.3, 2.5},
		{line.Count, 100, 100},
		// A share never goes past the whole.
		{line.Share, 0.33, 0.5},
		{line.Share, 0.9, 1},
		// Hours round in the unit they are printed in: minutes, hours, days.
		{line.Hours, 0.4, 25.0 / 60},
		{line.Hours, 30, 50},
		{line.Hours, 100, 120},
	}
	for _, c := range cases {
		if got := ceiling(c.unit, c.v); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("ceiling(%s, %v) = %v, want %v", c.unit, c.v, got, c.want)
		}
	}
}

func TestSafeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/app/pull/1": "https://github.com/acme/app/pull/1",
		"http://github.com/acme/app/pull/1":  "",
		"javascript:alert(1)":                "",
		"//example.invalid":                  "",
		"https://example.invalid/a b":        "",
		`https://example.invalid/"x`:         "",
		"https://example.invalid/<x>":        "",
		"":                                   "",
	} {
		if got := safeURL(in); got != want {
			t.Errorf("safeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMeter(t *testing.T) {
	for share, want := range map[float64]string{0.25: `width="25"`, 2: `width="100"`, -1: `class="fill" width="0"`} {
		if got := string(meter(share)); !strings.Contains(got, want) {
			t.Errorf("meter(%v) = %s, want %s", share, got, want)
		}
	}
}
