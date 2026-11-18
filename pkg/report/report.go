// Package report prints a metrics.Report as terminal text, Markdown, a
// one-page HTML file or JSON. Every format is built from the same View, so the
// numbers cannot drift between them.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/metrics"
)

// Table is a header row and body rows of already formatted cells.
type Table struct {
	Title  string
	Header []string
	Rows   [][]string
	Note   string
}

// StageBar is one stage of the time breakdown.
type StageBar struct {
	ID, Name, Station string
	Share             float64
	Constraint        bool
}

// View is the report laid out for printing.
type View struct {
	R          *metrics.Report
	Title      string
	Window     string
	Classes    Table
	Stages     Table
	Bars       []StageBar
	Agents     Table
	Repos      Table
	LightsOut  Table
	HasAgents  bool
	HasRepos   bool
	Constraint string
}

func num(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

func hoursOf(d metrics.Dist) string {
	if d.N == 0 {
		return "n/a"
	}
	return metrics.Hours(d.Median)
}

func minutesOf(d metrics.Dist) string {
	if d.N == 0 {
		return "n/a"
	}
	return metrics.Minutes(d.Median)
}

func inverse(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := 1 - *p
	return &v
}

func opt(v *float64, format func(float64) string) string {
	if v == nil {
		return "not measured"
	}
	return format(*v)
}

// NewView lays a report out.
func NewView(r *metrics.Report) View {
	v := View{R: r, Title: r.Target}
	v.Window = fmt.Sprintf("%s to %s (%s days)", r.Since.Format("2006-01-02"), r.Until.Format("2006-01-02"), num(float64(int(r.Days+0.5))))

	cols := []*metrics.ClassStats{&r.Agent, &r.Human, &r.Automation}
	row := func(label string, f func(c *metrics.ClassStats) string) []string {
		out := []string{label}
		for _, c := range cols {
			out = append(out, f(c))
		}
		return out
	}
	v.Classes = Table{
		Title:  "Agent and human pull requests",
		Header: []string{"", "Agent", "Human", "Automation"},
		Rows: [][]string{
			row("Pull requests merged", func(c *metrics.ClassStats) string { return fmt.Sprint(c.Merged) }),
			row("Merged per week", func(c *metrics.ClassStats) string { return fmt.Sprintf("%.1f", c.MergedPerWeek) }),
			row("Merge rate", func(c *metrics.ClassStats) string { return metrics.Percent(c.MergeRate) }),
			row("Open to merge, median", func(c *metrics.ClassStats) string { return hoursOf(c.LeadTimeH) }),
			row("Time to first review, median", func(c *metrics.ClassStats) string { return hoursOf(c.TimeToFirstReviewH) }),
			row("Merged with no human review", func(c *metrics.ClassStats) string { return metrics.Percent(inverse(c.HumanReviewedShare)) }),
			row("Rework rate", func(c *metrics.ClassStats) string { return metrics.Percent(c.ReworkRate) }),
			row("CI time per run, median", func(c *metrics.ClassStats) string { return minutesOf(c.CIMinutesPerRun) }),
			row("CI runs per pull request", func(c *metrics.ClassStats) string {
				if c.CIRunsPerPR.N == 0 {
					return "n/a"
				}
				return fmt.Sprintf("%.1f", c.CIRunsPerPR.Mean)
			}),
			row("CI runs that failed", func(c *metrics.ClassStats) string { return metrics.Percent(c.CIFailureRate) }),
			row("Size, median lines changed", func(c *metrics.ClassStats) string {
				if c.SizeLines.N == 0 {
					return "n/a"
				}
				return num(c.SizeLines.Median)
			}),
		},
		Note: "Automation is dependency bots and release bots. It is left out of the agent and human numbers. Rework rate is the share of reviewed pull requests that review sent back.",
	}

	v.Stages = Table{
		Title:  "Where the time goes",
		Header: []string{"Stage", "Station", "Share", "Agent median", "Human median", "Agent PRs", "Human PRs"},
		Note:   "For merged agent and human pull requests. Medians are wall-clock time, as the pull request timeline shows it. For the shares, time with a CI run in flight is taken out of the other stages and counted as CI, so the shares add up to 100%. Each pull request's time in a stage is capped at the stage's 90th percentile first, so one stuck pull request cannot name the constraint.",
	}
	for _, s := range metrics.Stages {
		a, h := r.Agent.Stages[s.ID], r.Human.Stages[s.ID]
		if a.N+h.N == 0 {
			continue
		}
		share := metrics.StageShare(r.Line, s.ID)
		isC := r.Constraint != nil && r.Constraint.Stage == s.ID
		name := s.Name
		if isC {
			name += " (constraint)"
			v.Constraint = s.Name
		}
		v.Stages.Rows = append(v.Stages.Rows, []string{name, s.Station, fmt.Sprintf("%.0f%%", share*100), hoursOf(a), hoursOf(h), fmt.Sprint(a.N), fmt.Sprint(h.N)})
		v.Bars = append(v.Bars, StageBar{ID: s.ID, Name: s.Name, Station: s.Station, Share: share, Constraint: isC})
	}

	v.Agents = Table{Title: "Disclosed agents", Header: []string{"Agent", "Merged PRs", "By account", "By trailer, footer or badge"}}
	for _, a := range r.Agents {
		v.Agents.Rows = append(v.Agents.Rows, []string{a.Name, fmt.Sprint(a.Merged), fmt.Sprint(a.High), fmt.Sprint(a.Medium)})
	}
	v.HasAgents = len(v.Agents.Rows) > 0

	v.Repos = Table{Title: "Repositories", Header: []string{"Repository", "Merged", "Agent", "Human", "First review, median"}}
	for i, p := range r.PerRepo {
		if i == 20 {
			v.Repos.Note = fmt.Sprintf("The %d busiest of %d repositories. The JSON export has all of them.", 20, len(r.PerRepo))
			break
		}
		if p.Merged == 0 {
			continue
		}
		wait := "n/a"
		if p.ReviewedN > 0 {
			wait = metrics.Hours(p.ReviewWaitH)
		}
		v.Repos.Rows = append(v.Repos.Rows, []string{p.Repo, fmt.Sprint(p.Merged), fmt.Sprint(p.AgentMerged), fmt.Sprint(p.HumanMerged), wait})
	}
	v.HasRepos = len(v.Repos.Rows) > 1

	lo := r.LightsOut
	ai := "Autocomplete in the editor"
	if lo.AI == "agents" {
		ai = "Coding agents"
	}
	v.LightsOut = Table{
		Title:  "Your numbers for Lights Out",
		Header: []string{"Simulator input", "Measured"},
		Rows: [][]string{
			{"Developers", opt(lo.Devs, num)},
			{"Pull requests merged per week", opt(lo.PRW, num)},
			{"CI minutes per run", opt(lo.CIMin, num)},
			{"Hours to first review", opt(lo.RevH, num)},
			{"Deploys per week", opt(lo.DPW, num)},
			{"CI runs per pull request", opt(lo.CIRuns, num)},
			{"Rework rate", opt(lo.Rework, func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) })},
			{"AI in use today", ai},
			{"Test environments", "not on GitHub, fill it in"},
			{"Product managers", "not on GitHub, fill it in"},
		},
	}
	return v
}

// JSON writes the report as indented JSON.
func JSON(w io.Writer, r *metrics.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

func textTable(b *strings.Builder, t Table) {
	width := make([]int, len(t.Header))
	for i, h := range t.Header {
		width[i] = len(h)
	}
	for _, r := range t.Rows {
		for i, c := range r {
			if len(c) > width[i] {
				width[i] = len(c)
			}
		}
	}
	line := func(cells []string) {
		b.WriteString("  ")
		for i, c := range cells {
			if i == 0 {
				fmt.Fprintf(b, "%-*s", width[i], c)
			} else {
				fmt.Fprintf(b, "  %*s", width[i], c)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(t.Title + "\n")
	line(t.Header)
	for _, r := range t.Rows {
		line(r)
	}
	if t.Note != "" {
		for _, l := range wrap(t.Note, 86) {
			b.WriteString("  " + l + "\n")
		}
	}
	b.WriteString("\n")
}

func wrap(s string, width int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && len(line)+1+len(w) > width {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// Text writes the terminal report.
func Text(w io.Writer, r *metrics.Report) error {
	v := NewView(r)
	var b strings.Builder
	fmt.Fprintf(&b, "factorymeter  %s  %s\n\n", v.Title, v.Window)
	for _, l := range wrap(r.Headline, 88) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	for _, f := range r.Findings {
		for i, l := range wrap(f, 86) {
			if i == 0 {
				b.WriteString("- " + l + "\n")
			} else {
				b.WriteString("  " + l + "\n")
			}
		}
	}
	b.WriteString("\n")
	textTable(&b, v.Classes)
	if len(v.Stages.Rows) > 0 {
		textTable(&b, v.Stages)
	}
	if v.HasAgents {
		textTable(&b, v.Agents)
	}
	if v.HasRepos {
		textTable(&b, v.Repos)
	}
	textTable(&b, v.LightsOut)
	b.WriteString("Open your factory in Lights Out with these numbers:\n  " + r.LightsOut.URL + "\n\n")
	b.WriteString("Limits\n")
	for _, f := range append(append([]string{}, r.Limits...), r.Notes...) {
		for i, l := range wrap(f, 86) {
			if i == 0 {
				b.WriteString("- " + l + "\n")
			} else {
				b.WriteString("  " + l + "\n")
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func mdTable(b *strings.Builder, t Table) {
	esc := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
	b.WriteString("### " + t.Title + "\n\n|")
	for _, h := range t.Header {
		b.WriteString(" " + esc(h) + " |")
	}
	b.WriteString("\n|")
	for i := range t.Header {
		if i == 0 {
			b.WriteString(" --- |")
		} else {
			b.WriteString(" ---: |")
		}
	}
	b.WriteString("\n")
	for _, r := range t.Rows {
		b.WriteString("|")
		for _, c := range r {
			b.WriteString(" " + esc(c) + " |")
		}
		b.WriteString("\n")
	}
	if t.Note != "" {
		b.WriteString("\n" + t.Note + "\n")
	}
	b.WriteString("\n")
}

// Markdown writes the report for a GitHub job summary or an issue.
func Markdown(w io.Writer, r *metrics.Report) error {
	v := NewView(r)
	var b strings.Builder
	fmt.Fprintf(&b, "## factorymeter: %s\n\n%s\n\n**%s**\n\n", v.Title, v.Window, r.Headline)
	for _, f := range r.Findings {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\n")
	mdTable(&b, v.Classes)
	if len(v.Stages.Rows) > 0 {
		mdTable(&b, v.Stages)
	}
	if v.HasAgents {
		mdTable(&b, v.Agents)
	}
	if v.HasRepos {
		mdTable(&b, v.Repos)
	}
	mdTable(&b, v.LightsOut)
	fmt.Fprintf(&b, "[Open your factory in Lights Out with these numbers](%s)\n\n### Limits\n\n", r.LightsOut.URL)
	for _, f := range append(append([]string{}, r.Limits...), r.Notes...) {
		b.WriteString("- " + f + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
