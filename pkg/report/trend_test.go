package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/metrics"
	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// trendSample is six weeks of history: agents merge more each week and get
// faster, people stay the same. Week 4 has too few pull requests.
func trendSample(t *testing.T, target string) metrics.Trend {
	t.Helper()
	reg, err := signature.Default()
	if err != nil {
		t.Fatal(err)
	}
	week := 7 * 24 * time.Hour
	rd := model.RepoData{Repo: "o/r", Since: t0, Until: t0.Add(6 * week), CISource: "actions", DeploySource: "deployments:production"}
	n := 0
	add := func(w, count int, agent bool, lead float64) {
		for i := 0; i < count; i++ {
			n++
			opened := t0.Add(time.Duration(w)*week + 24*time.Hour)
			merged := opened.Add(time.Duration(lead * float64(time.Hour)))
			sha := fmt.Sprint("s", n)
			p := model.PR{Repo: "o/r", Number: n, Author: model.Actor{Login: "dev", Type: "User"}, HeadRef: "fix/x", State: "MERGED",
				CreatedAt: opened, UpdatedAt: merged, MergedAt: &merged, ClosedAt: &merged, Additions: 40, Deletions: 2,
				Commits: []model.Commit{{SHA: sha, Parents: 1, CommittedAt: opened}},
				CIRuns: []model.CIRun{{SHA: sha, Status: "completed", Conclusion: "success", Attempt: 1,
					CreatedAt: opened, StartedAt: opened, UpdatedAt: opened.Add(12 * time.Minute), Event: "pull_request"}}}
			if agent {
				p.Commits[0].Trailers = []string{"Co-Authored-By: Claude <noreply@anthropic.com>"}
			}
			rd.PRs = append(rd.PRs, p)
		}
	}
	for w, agents := range []int{5, 6, 8, 1, 12, 14} {
		add(w, agents, true, 20-3*float64(w))
		if w != 3 {
			add(w, 5, false, 6)
		}
	}
	rd.Deploys = []model.Deploy{{At: t0.Add(5*week + time.Hour)}, {At: t0.Add(5*week + 30*time.Hour)}}
	tr, err := metrics.ComputeTrend([]model.RepoData{rd}, reg, metrics.TrendOptions{
		Options: metrics.Options{Target: target, Version: "test", Now: rd.Until}, Every: week})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func renderTrend(t *testing.T, tr *metrics.Trend) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, f := range map[string]func(*bytes.Buffer) error{
		"text":     func(b *bytes.Buffer) error { return TrendText(b, tr) },
		"markdown": func(b *bytes.Buffer) error { return TrendMarkdown(b, tr) },
		"html":     func(b *bytes.Buffer) error { return TrendHTML(b, tr) },
		"json":     func(b *bytes.Buffer) error { return TrendJSON(b, tr) },
	} {
		var b bytes.Buffer
		if err := f(&b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = b.String()
	}
	return out
}

func TestEveryFormatCarriesTheSameTrend(t *testing.T) {
	tr := trendSample(t, "o/r")
	out := renderTrend(t, &tr)
	if len(tr.Periods) != 6 || len(tr.Findings) < 3 {
		t.Fatalf("periods=%d findings=%d", len(tr.Periods), len(tr.Findings))
	}
	for name, s := range out {
		if name == "html" {
			s = strings.ReplaceAll(s, "&#43;", "+")
		}
		for _, want := range append([]string{tr.Headline}, tr.Findings...) {
			if !strings.Contains(flat(s), flat(want)) && name != "json" {
				t.Errorf("%s lacks %q", name, want)
			}
		}
		if i := strings.IndexAny(s, signature.LongDashes); i >= 0 {
			t.Errorf("%s: long dash near %q", name, s[max(0, i-40):min(len(s), i+40)])
		}
	}
	for _, name := range []string{"text", "markdown"} {
		for _, want := range []string{"Throughput", "Where the time goes", "Review", "CI", "Size", "Disclosed agents", "Week by week",
			"First 2 weeks", "Last 2 weeks", "By week", "▁", "█", "2026-03-02", "Claude Code"} {
			if !strings.Contains(out[name], want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
	var back metrics.Trend
	if err := json.Unmarshal([]byte(out["json"]), &back); err != nil {
		t.Fatal(err)
	}
	if back.Headline != tr.Headline || len(back.Periods) != 6 || len(back.Series) != len(tr.Series) || back.PeriodDays != 7 {
		t.Errorf("the JSON export lost something: %d periods, %d series", len(back.Periods), len(back.Series))
	}
	for _, s := range back.Series {
		if len(s.Points) != 6 {
			t.Errorf("series %s has %d points", s.ID, len(s.Points))
		}
	}
	// Markdown tables need the same number of cells in every row.
	cells := 0
	for _, l := range strings.Split(out["markdown"], "\n") {
		if !strings.HasPrefix(l, "|") {
			cells = 0
			continue
		}
		if n := strings.Count(l, "|"); cells == 0 {
			cells = n
		} else if n != cells {
			t.Errorf("ragged table row: %s", l)
		}
	}
}

func TestTrendHTMLIsOneSafeFile(t *testing.T) {
	tr := trendSample(t, `o/<script>alert(1)</script>`)
	tr.Findings = append(tr.Findings, `<img src=x onerror=alert(1)>`)
	h := renderTrend(t, &tr)["html"]
	if strings.Contains(h, "<script") || strings.Contains(h, "<img") {
		t.Error("the page did not escape a hostile value")
	}
	if !strings.Contains(h, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the escaped target is missing")
	}
	if !strings.HasPrefix(h, "<!doctype html>") || !strings.Contains(h, `<meta name="viewport"`) || !strings.Contains(h, "prefers-color-scheme:dark") {
		t.Error("the page lost its doctype, viewport or dark theme")
	}
	if m := regexp.MustCompile(`(?i)(src|@import|url\()\s*=?\s*["']?(https?:)?//`).FindString(h); m != "" {
		t.Errorf("the page loads an outside asset: %q", m)
	}
	if strings.Count(h, "<link") != 0 || regexp.MustCompile(`<[^>]*\son[a-z]+=`).MatchString(h) {
		t.Error("the page has a link tag or an event handler")
	}
	figures := strings.Count(h, "<figure")
	if figures < 8 || figures != strings.Count(h, "</figure>") || figures != strings.Count(h, "<svg") || figures != strings.Count(h, "</svg>") {
		t.Errorf("%d figures, %d svg", figures, strings.Count(h, "<svg"))
	}
	// One hover column per period in every chart, and one table per group
	// plus the table of periods.
	if got := strings.Count(h, `<g class="hit">`); got != figures*len(tr.Periods) {
		t.Errorf("%d hover columns for %d charts of %d periods", got, figures, len(tr.Periods))
	}
	if got := strings.Count(h, "<table>"); got != strings.Count(h, "<details>")+1 || got < 5 {
		t.Errorf("%d tables", got)
	}
	// The stage shares are one chart of stacked columns, and the week with
	// one merged pull request is faded.
	if strings.Count(h, `class="card wide"`) != 1 || strings.Count(h, `<g class="few">`) != 1 {
		t.Errorf("stage shares: %d wide charts, %d faded columns", strings.Count(h, `class="card wide"`), strings.Count(h, `<g class="few">`))
	}
	if strings.Contains(h, "NaN") || strings.Contains(h, "Inf") {
		t.Error("a chart holds a number that is not a number")
	}
}

func TestSpark(t *testing.T) {
	f := func(v float64) metrics.Point { return metrics.Point{Value: &v, N: 9} }
	if got := spark([]metrics.Point{{}, f(0), f(5), f(10)}); got != "·▁▅█" {
		t.Errorf("spark: %q", got)
	}
	if got := spark([]metrics.Point{f(0), f(0)}); got != "▁▁" {
		t.Errorf("a flat zero row: %q", got)
	}
}

func TestTicks(t *testing.T) {
	for _, c := range []struct {
		top  float64
		unit string
		want string
	}{
		{1, metrics.UnitShare, "0 50% 100%"},
		{187, metrics.UnitPerWeek, "0 50 100 150 200"},
		{3, metrics.UnitCount, "0 1 2 3"},
		{0, metrics.UnitCount, "0 1"},
		{0.6, metrics.UnitHours, "0 10 min 20 min 30 min 40 min"},
		{30, metrics.UnitHours, "0 10 h 20 h 30 h"},
		{324, metrics.UnitHours, "0 5 d 10 d 15 d"},
		{41, metrics.UnitMinutes, "0 20 min 40 min 60 min"},
	} {
		var got []string
		for _, v := range ticks(c.top, c.unit) {
			got = append(got, tickLabel(v, c.unit))
		}
		if s := strings.Join(got, " "); s != c.want {
			t.Errorf("ticks(%v, %s) = %q, want %q", c.top, c.unit, s, c.want)
		}
	}
}
