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

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func at(h float64) time.Time { return t0.Add(time.Duration(h * float64(time.Hour))) }

// sample builds a report over two repositories: six agent pull requests that
// wait 30 h for a first review and six human ones that wait 5 h.
func sample(t *testing.T, target string) metrics.Report {
	t.Helper()
	reg, err := signature.Default()
	if err != nil {
		t.Fatal(err)
	}
	mk := func(repo string, n int, agent bool, wait float64) model.PR {
		merged := at(wait + 2)
		p := model.PR{Repo: repo, Number: n, Author: model.Actor{Login: "dev", Type: "User"}, HeadRef: "fix/x", State: "MERGED",
			CreatedAt: at(0), UpdatedAt: merged, MergedAt: &merged, Additions: 40, Deletions: 2,
			Commits: []model.Commit{{SHA: fmt.Sprintf("%s-%d", repo, n), Parents: 1, CommittedAt: at(0)}},
			Reviews: []model.Review{{Author: model.Actor{Login: "rev", Type: "User"}, State: "APPROVED", SubmittedAt: at(wait)}},
			CIRuns: []model.CIRun{{SHA: fmt.Sprintf("%s-%d", repo, n), Status: "completed", Conclusion: "success", Attempt: 1,
				CreatedAt: at(0), StartedAt: at(0), UpdatedAt: at(0.2), Event: "pull_request"}}}
		if agent {
			p.Commits[0].Trailers = []string{"Co-Authored-By: Claude <noreply@anthropic.com>"}
		}
		return p
	}
	var a, b []model.PR
	for i := 0; i < 6; i++ {
		a = append(a, mk("o/a", i, true, 30))
		b = append(b, mk("o/b|c", i, false, 5))
	}
	win := func(repo string, prs []model.PR) model.RepoData {
		return model.RepoData{Repo: repo, Since: t0.AddDate(0, 0, -14), Until: t0.AddDate(0, 0, 14), PRs: prs, CISource: "actions", DeploySource: "releases",
			Deploys: []model.Deploy{{At: at(1)}, {At: at(2)}}, Notes: []string{"A note from the collector."}}
	}
	return metrics.Compute([]model.RepoData{win("o/a", a), win("o/b|c", b)}, reg, metrics.Options{Target: target, Version: "test", Now: t0.AddDate(0, 0, 14)})
}

func render(t *testing.T, r *metrics.Report) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, f := range map[string]func(*bytes.Buffer) error{
		"text":     func(b *bytes.Buffer) error { return Text(b, r) },
		"markdown": func(b *bytes.Buffer) error { return Markdown(b, r) },
		"html":     func(b *bytes.Buffer) error { return HTML(b, r) },
		"json":     func(b *bytes.Buffer) error { return JSON(b, r) },
	} {
		var b bytes.Buffer
		if err := f(&b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = b.String()
	}
	return out
}

// flat undoes the line wrapping of the terminal report.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestEveryFormatCarriesTheSameReport(t *testing.T) {
	r := sample(t, "o")
	if r.Constraint == nil || r.Constraint.Stage != metrics.StageReviewWait {
		t.Fatalf("the sample must have the review wait as its constraint: %+v", r.Constraint)
	}
	out := render(t, &r)
	amp := strings.NewReplacer("&amp;", "&", "&#43;", "+", "&#39;", "'")
	for name, s := range out {
		if name == "json" {
			continue
		}
		s = amp.Replace(flat(s))
		for _, want := range []string{
			r.Headline,
			"Waiting for first review (constraint)",
			"Disclosed agent share is 50%",
			"Disclosed agents", "Claude Code",
			"Repositories",
			r.LightsOut.URL,
			"A branch prefix alone is not counted as an agent",
			"A note from the collector.",
			"not on GitHub, fill it in",
		} {
			if !strings.Contains(s, flat(want)) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
	}
	if !strings.HasPrefix(out["text"], "factorymeter  o  2026-02-16 to 2026-03-16 (28 days)\n") {
		t.Errorf("text header: %q", strings.SplitN(out["text"], "\n", 2)[0])
	}
}

// The owner's rule for this project: no long dashes in anything it prints.
func TestNoLongDashesInAnyFormat(t *testing.T) {
	r := sample(t, "o")
	for name, s := range render(t, &r) {
		if i := strings.IndexAny(s, signature.LongDashes); i >= 0 {
			t.Errorf("%s: long dash near %q", name, s[max(0, i-40):min(len(s), i+40)])
		}
	}
}

func TestHTMLIsOneSafeFile(t *testing.T) {
	r := sample(t, `o/<script>alert(1)</script>`)
	r.Findings = append(r.Findings, `<img src=x onerror=alert(1)>`)
	r.LightsOut.URL = `javascript:alert(1)`
	h := render(t, &r)["html"]
	if strings.Contains(h, "<script") || strings.Contains(h, "<img") || strings.Contains(h, `href="javascript:`) {
		t.Error("the page did not escape a hostile value")
	}
	if !strings.Contains(h, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the escaped target is missing")
	}
	if !strings.HasPrefix(h, "<!doctype html>") || !strings.Contains(h, `<meta name="viewport"`) || !strings.Contains(h, "prefers-color-scheme:dark") {
		t.Error("the page lost its doctype, viewport or dark theme")
	}
	// No outside assets: nothing is loaded when the page opens. Links a
	// reader may click are fine.
	if m := regexp.MustCompile(`(?i)(src|@import|url\()\s*=?\s*["']?(https?:)?//`).FindString(h); m != "" {
		t.Errorf("the page loads an outside asset: %q", m)
	}
	if n := strings.Count(h, "<link"); n != 0 {
		t.Errorf("%d link tags", n)
	}
	r = sample(t, "o")
	h = render(t, &r)["html"]
	if strings.Count(h, `class="bar on"`) != 1 || !strings.Contains(h, `<a class="cta" href="https://thesoftwarefactory.site/lights-out/?`) {
		t.Error("the page must mark one constraint bar and link to Lights Out")
	}
	if strings.Count(h, "<section>") != strings.Count(h, "</section>") || strings.Count(h, "<table>") != 5 {
		t.Errorf("sections or tables are unbalanced: %d tables", strings.Count(h, "<table>"))
	}
}

func TestJSONRoundTrips(t *testing.T) {
	r := sample(t, "o")
	js := render(t, &r)["json"]
	var back metrics.Report
	if err := json.Unmarshal([]byte(js), &back); err != nil {
		t.Fatal(err)
	}
	if back.Headline != r.Headline || back.Constraint.Stage != r.Constraint.Stage || back.LightsOut.URL != r.LightsOut.URL || back.Agent.Merged != 6 {
		t.Errorf("round trip changed the report: %+v", back.Constraint)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"target", "since", "until", "headline", "findings", "constraint", "disclosed_agent_share", "agents", "line", "agent", "human", "automation",
		"lights_out", "limits", "per_repo", "factorymeter_version", "registry_version", "ci_source", "deploy_source"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("the export lost the key %q", key)
		}
	}
	for _, key := range []string{`"stage_hours"`, `"stage_exclusive_hours"`, `"time_to_first_review_hours"`, `"rework_rate"`, `"merge_rate"`, `"merged_per_week"`} {
		if !strings.Contains(js, key) {
			t.Errorf("the export lost the key %s", key)
		}
	}
	if strings.Contains(js, `\u00`+"26") || strings.Contains(js, `"prs"`) {
		t.Error("the link must stay readable, and rows per pull request need --detail")
	}
}

func TestMarkdownTablesAreWellFormed(t *testing.T) {
	r := sample(t, "o")
	md := render(t, &r)["markdown"]
	if !strings.HasPrefix(md, "## factorymeter: o\n") || !strings.Contains(md, "**"+r.Headline+"**") {
		t.Error("the summary must open with the target and the headline")
	}
	if !strings.Contains(md, `| o/b\|c |`) {
		t.Error("a pipe in a cell must be escaped")
	}
	cells := regexp.MustCompile(`(^|[^\\])\|`)
	width, tables := 0, 0
	for _, l := range strings.Split(md, "\n") {
		if !strings.HasPrefix(l, "|") {
			width = 0
			continue
		}
		n := len(cells.FindAllString(l, -1))
		if width == 0 {
			width, tables = n, tables+1
		} else if n != width {
			t.Errorf("row has %d columns, the header has %d: %s", n-1, width-1, l)
		}
	}
	if tables != 5 {
		t.Errorf("%d tables, want 5", tables)
	}
	if !strings.Contains(md, "[Open your factory in Lights Out with these numbers]("+r.LightsOut.URL+")") {
		t.Error("the Lights Out link is missing")
	}
}

func TestAnEmptyWindowStillPrints(t *testing.T) {
	reg, _ := signature.Default()
	r := metrics.Compute([]model.RepoData{{Repo: "o/r", Since: t0.AddDate(0, 0, -30), Until: t0, CISource: "none", DeploySource: "none"}}, reg, metrics.Options{Target: "o/r", Now: t0})
	out := render(t, &r)
	for name, s := range out {
		if !strings.Contains(flat(s), "No pull request merged in the window") {
			t.Errorf("%s: no finding for the empty window", name)
		}
	}
	if strings.Contains(out["html"], `class="bars"`) || strings.Contains(out["text"], "Where the time goes") || strings.Contains(out["markdown"], "Disclosed agents") {
		t.Error("an empty window must not print empty stage or agent tables")
	}
}

func TestWrap(t *testing.T) {
	got := wrap("one two three four", 9)
	if strings.Join(got, "|") != "one two|three|four" {
		t.Errorf("got %q", got)
	}
	if got := wrap("", 10); len(got) > 1 {
		t.Errorf("empty: %q", got)
	}
	long := "https://thesoftwarefactory.site/lights-out/?a=1"
	if got := wrap(long, 10); len(got) != 1 || got[0] != long {
		t.Errorf("a long word must stay whole: %q", got)
	}
}
