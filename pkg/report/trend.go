package report

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/metrics"
)

// TrendGroup is one group of series, laid out for printing.
type TrendGroup struct {
	Name string
	// Summary has one row per series: both ends, the change and a sparkline.
	Summary Table
	// Full has one row per series and one column per period.
	Full   Table
	Charts []template.HTML
}

// TrendView is the trend laid out for printing.
type TrendView struct {
	T      *metrics.Trend
	Title  string
	Window string
	Groups []TrendGroup
	// ByPeriod has one row per period.
	ByPeriod Table
}

var sparkBars = []rune("▁▂▃▄▅▆▇█")

// spark draws a series as one character per period. The row runs from zero to
// its own highest value. A period with no value is a dot.
func spark(pts []metrics.Point) string {
	top := 0.0
	for _, p := range pts {
		if p.Value != nil && *p.Value > top {
			top = *p.Value
		}
	}
	var b strings.Builder
	for _, p := range pts {
		switch {
		case p.Value == nil:
			b.WriteRune('·')
		case top == 0:
			b.WriteRune(sparkBars[0])
		default:
			b.WriteRune(sparkBars[int(math.Round(*p.Value/top*float64(len(sparkBars)-1)))])
		}
	}
	return b.String()
}

func classWord(class string) string {
	if class == metrics.ClassLine {
		return "all"
	}
	return class
}

// cell prints a point, with a star when few items stand behind it.
func cell(p metrics.Point, unit string) string {
	s := metrics.Value(p, unit)
	if p.Thin() && unit != metrics.UnitPerWeek && unit != metrics.UnitCount {
		s += "*"
	}
	return s
}

const thinNote = "A star marks a value drawn from fewer than 5 pull requests or CI runs."

// NewTrendView lays a trend out.
func NewTrendView(t *metrics.Trend) TrendView {
	v := TrendView{T: t, Title: t.Target}
	one, many := t.PeriodWord()
	v.Window = fmt.Sprintf("%s to %s, %d %s", t.Since.Format("2006-01-02"), t.Until.Format("2006-01-02"), len(t.Periods), many)
	if one == "period" {
		v.Window += " of " + num(t.PeriodDays) + " days"
	}
	first, last := t.EndWords()
	first, last = upper(first), upper(last)

	for _, g := range metrics.Groups {
		tg := TrendGroup{Name: g}
		tg.Summary = Table{Title: g, Header: []string{"", "", first, last, "Change", "By " + one}}
		tg.Full = Table{Header: []string{"", first, last, "Change"}, Note: thinNote}
		for _, p := range t.Periods {
			tg.Full.Header = append(tg.Full.Header, p.From.Format("Jan 2"))
		}
		for i := range t.Series {
			s := &t.Series[i]
			if s.Group != g {
				continue
			}
			tg.Summary.Rows = append(tg.Summary.Rows, []string{s.Name, classWord(s.Class), cell(s.First, s.Unit), cell(s.Last, s.Unit), metrics.ChangeWords(s), spark(s.Points)})
			row := []string{s.Name + " (" + classWord(s.Class) + ")", cell(s.First, s.Unit), cell(s.Last, s.Unit), metrics.ChangeWords(s)}
			for _, p := range s.Points {
				row = append(row, cell(p, s.Unit))
			}
			tg.Full.Rows = append(tg.Full.Rows, row)
		}
		if len(tg.Summary.Rows) == 0 {
			continue
		}
		tg.Charts = charts(t, g)
		v.Groups = append(v.Groups, tg)
	}

	head := map[string]string{"week": "Week of", "day": "Day"}[one]
	if head == "" {
		head = "Period from"
	}
	v.ByPeriod = Table{
		Title:  upper(one) + " by " + one,
		Header: []string{head, "Agent", "Human", "Automation", "Agent share", "Open to merge, agent", "Open to merge, human", "Waiting at the end", "Constraint", "Share"},
		Note:   "The first three columns are pull requests merged. " + upper(one) + "s with fewer than 5 merged agent and human pull requests name no constraint.",
	}
	for _, p := range t.Periods {
		c, share := "n/a", "n/a"
		if p.Constraint != nil {
			c, share = strings.ToLower(p.Constraint.Name), fmt.Sprintf("%.0f%%", p.Constraint.Share*100)
		}
		v.ByPeriod.Rows = append(v.ByPeriod.Rows, []string{
			p.From.Format("2006-01-02"), fmt.Sprint(p.Agent.Merged), fmt.Sprint(p.Human.Merged), fmt.Sprint(p.Automation.Merged),
			metrics.Percent(p.DisclosedAgentShare), hoursOf(p.Agent.LeadTimeH), hoursOf(p.Human.LeadTimeH),
			fmt.Sprint(p.WaitingForReview), c, share,
		})
	}
	return v
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// TrendJSON writes the trend as indented JSON.
func TrendJSON(w io.Writer, t *metrics.Trend) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(t)
}

func bullets(b *strings.Builder, items []string) {
	for _, f := range items {
		for i, l := range wrap(f, 86) {
			if i == 0 {
				b.WriteString("- " + l + "\n")
			} else {
				b.WriteString("  " + l + "\n")
			}
		}
	}
}

const sparkNote = "Each line on the right runs from zero to the row's own highest value. A dot is a period with no value."

// TrendText writes the terminal trend.
func TrendText(w io.Writer, t *metrics.Trend) error {
	v := NewTrendView(t)
	var b strings.Builder
	fmt.Fprintf(&b, "factorymeter trend  %s  %s\n\n", v.Title, v.Window)
	for _, l := range wrap(t.Headline, 88) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	bullets(&b, t.Findings)
	b.WriteString("\n")
	for _, g := range v.Groups {
		textTable(&b, g.Summary)
	}
	for _, l := range wrap(sparkNote+" "+thinNote, 88) {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	textTable(&b, v.ByPeriod)
	b.WriteString("Limits\n")
	bullets(&b, append(append([]string{}, t.Limits...), t.Notes...))
	_, err := io.WriteString(w, b.String())
	return err
}

// TrendMarkdown writes the trend for a GitHub job summary or an issue.
func TrendMarkdown(w io.Writer, t *metrics.Trend) error {
	v := NewTrendView(t)
	var b strings.Builder
	fmt.Fprintf(&b, "## factorymeter trend: %s\n\n%s\n\n**%s**\n\n", v.Title, v.Window, t.Headline)
	for _, f := range t.Findings {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\n")
	for _, g := range v.Groups {
		mdTable(&b, g.Summary)
	}
	b.WriteString(sparkNote + " " + thinNote + "\n\n")
	mdTable(&b, v.ByPeriod)
	b.WriteString("### Limits\n\n")
	for _, f := range append(append([]string{}, t.Limits...), t.Notes...) {
		b.WriteString("- " + f + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
