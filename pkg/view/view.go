// Package view prints a factorymeter/v1 report for people: as text for a
// terminal, as Markdown for a job summary or a pull request, and as one HTML
// page with charts.
//
// It reads the report and nothing else, so anything it shows can also be
// read out of the JSON. The page follows the line: the verdict, the seven
// stations side by side, agents next to people, then each station with its
// history, and last what the numbers stand on.
package view

import (
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

// What a block of the page is.
const (
	head = iota
	para
	table
	list
)

// block is one piece of the page. Text and Markdown lay the same blocks out.
type block struct {
	kind  int
	level int
	text  string
	// rows is a table, header first. right marks the columns set to the right.
	rows  [][]string
	right []bool
	items []string
}

type page struct {
	blocks []block
}

func (p *page) head(level int, text string) {
	p.blocks = append(p.blocks, block{kind: head, level: level, text: text})
}
func (p *page) para(text string) { p.blocks = append(p.blocks, block{kind: para, text: text}) }
func (p *page) list(items []string) {
	if len(items) > 0 {
		p.blocks = append(p.blocks, block{kind: list, items: items})
	}
}

// table adds a table. A header that starts with ">" sets its column to the
// right.
func (p *page) table(header []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	b := block{kind: table, right: make([]bool, len(header))}
	h := make([]string, len(header))
	for i, c := range header {
		b.right[i] = strings.HasPrefix(c, ">")
		h[i] = strings.TrimPrefix(c, ">")
	}
	b.rows = append([][]string{h}, rows...)
	p.blocks = append(p.blocks, b)
}

// book finds the series of a report.
type book struct {
	r  *contract.Report
	by map[string]map[contract.Slice]*contract.Series
}

func open(r *contract.Report) *book {
	b := &book{r: r, by: map[string]map[contract.Slice]*contract.Series{}}
	for i := range r.Metrics {
		s := &r.Metrics[i]
		if b.by[s.Metric] == nil {
			b.by[s.Metric] = map[contract.Slice]*contract.Series{}
		}
		b.by[s.Metric][s.Slice] = s
	}
	return b
}

func (b *book) get(metric string, sl contract.Slice) *contract.Series { return b.by[metric][sl] }

// Number prints a value in its unit, short enough for a table cell.
func Number(unit string, v *float64) string {
	if v == nil {
		return "-"
	}
	x := *v
	switch line.Unit(unit) {
	case line.Hours:
		switch {
		case x < 1:
			return fmt.Sprintf("%.0fm", x*60)
		case x < 48:
			return fmt.Sprintf("%.1fh", x)
		}
		return fmt.Sprintf("%.1fd", x/24)
	case line.Share:
		if x > 0 && x < 0.005 {
			return "<1%"
		}
		return fmt.Sprintf("%.0f%%", x*100)
	case line.PerWeek:
		return fmt.Sprintf("%.1f", x)
	}
	return fmt.Sprintf("%.0f", x)
}

// Change prints how a value moved: points for a share, percent for the rest.
func Change(s *contract.Series) string {
	if s == nil || s.Change == nil {
		return ""
	}
	c := *s.Change
	switch {
	case line.Unit(s.Unit) == line.Share:
		return fmt.Sprintf("%+.0f pts", c*100)
	case c >= 9:
		// "+2165%" reads worse than what it says: 23 times as much.
		return fmt.Sprintf("%.0fx", c+1)
	}
	return fmt.Sprintf("%+.0f%%", c*100)
}

// mark is what follows a value in a table: which way it moved, "!" when the
// move is for the worse, and "~" when the value is drawn from too few items.
func mark(s *contract.Series) string {
	if s == nil || s.Value == nil {
		return ""
	}
	out := ""
	if s.Previous != nil && s.Trend != contract.TrendFlat && !(s.Trend == contract.TrendUnknown && s.Thin) {
		switch moved(s) {
		case 1:
			out = " ▲"
		case -1:
			out = " ▼"
		}
		if s.Trend == contract.TrendWorse {
			out += "!"
		}
	}
	if s.Thin {
		out += " ~"
	}
	return out
}

// moved says which way a value went against the weeks before: 1 up, -1 down,
// and 0 when it stood still or the move cannot be called.
func moved(s *contract.Series) int {
	if s == nil || s.Value == nil || s.Previous == nil || s.Trend == contract.TrendFlat || s.Trend == contract.TrendUnknown && s.Thin {
		return 0
	}
	switch {
	case *s.Value > *s.Previous:
		return 1
	case *s.Value < *s.Previous:
		return -1
	}
	return 0
}

func cell(s *contract.Series) string {
	if s == nil {
		return ""
	}
	return value(s, s.Value) + mark(s)
}

// value prints a value of a series. The hours a station holds are a sum over
// a week, so they stay in hours: "8382h" and not a number of days.
func value(s *contract.Series, v *float64) string {
	if v != nil && strings.HasSuffix(s.Metric, ".held.total") {
		if *v < 10 {
			return fmt.Sprintf("%.1fh", *v)
		}
		return fmt.Sprintf("%.0fh", *v)
	}
	return Number(s.Unit, v)
}

// or shows a dash where a slice has no series.
func or(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var bars = []rune("▁▂▃▄▅▆▇█")

// Spark draws the history of a series, one character per period. A period
// with nothing counted is a dot.
func Spark(s *contract.Series) string {
	if s == nil {
		return ""
	}
	top := 0.0
	for _, p := range s.Points {
		if p.Value != nil {
			top = math.Max(top, *p.Value)
		}
	}
	var b strings.Builder
	for _, p := range s.Points {
		switch {
		case p.Value == nil:
			b.WriteRune('·')
		case top <= 0:
			b.WriteRune(bars[0])
		default:
			b.WriteRune(bars[int(math.Round(*p.Value/top*float64(len(bars)-1)))])
		}
	}
	return b.String()
}

func trendWord(s *contract.Series) string {
	if s == nil || s.Previous == nil || s.Value == nil {
		return ""
	}
	if s.Trend == contract.TrendUnknown {
		if s.Thin {
			return "too few to call"
		}
		return ""
	}
	return s.Trend
}

func day(t interface{ Format(string) string }) string { return t.Format("2006-01-02") }

// items names what leaves a station, as a reader of a GitHub line calls it:
// a change is a pull request.
func items(station string) string {
	st, ok := line.StationByID(station)
	switch {
	case !ok:
		return ""
	case st.Item == "change":
		return "PRs"
	}
	return st.Item + "s"
}

// outLabel is the label of what leaves a station each week.
func outLabel(station string) string {
	it := items(station)
	if it == "" {
		return "Out a week"
	}
	if it != "PRs" {
		it = strings.ToUpper(it[:1]) + it[1:]
	}
	if station == line.CI {
		return it + " a week"
	}
	return it + " out a week"
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func build(r *contract.Report) *page {
	b, p := open(r), &page{}
	weeks := int(math.Round(r.Window.Until.Sub(r.Window.Since).Hours() / (7 * 24)))
	title := "Delivery line"
	if r.Target != "" {
		title += ": " + r.Target
	}
	p.head(1, title)
	span := fmt.Sprintf("%s, %s to %s.", plural(weeks, "week"), day(r.Window.Since), day(r.Window.Until))
	if r.Window.Previous != nil {
		span += fmt.Sprintf(" Compared with the %s before.", plural(weeks, "week"))
	}
	span += fmt.Sprintf(" History: %s.", plural(len(r.Window.Periods), "week"))
	p.para(span)
	p.para("**" + r.Verdict.Headline + "**")
	p.para(r.Verdict.Detail)

	// The line: every station side by side.
	p.head(2, "The line")
	var rows [][]string
	for _, st := range r.Stations {
		name := st.Name
		if st.Constraint {
			name += " (constraint)"
		}
		if st.Status == contract.StatusNotConnected {
			rows = append(rows, []string{name, "not connected", "", "", "", "", "", "", "", ""})
			continue
		}
		g := func(id string) *contract.Series { return b.get(st.ID+"."+id, contract.Slice{}) }
		held := ""
		if st.HeldShare != nil {
			held = cell(g("held.share"))
		}
		rows = append(rows, []string{name, st.Status, items(st.ID), cell(g("flow")), cell(g("wait.p50")), cell(g("work.p50")),
			cell(g("rework")), cell(g("queue")), held, Spark(g("flow"))})
	}
	p.table([]string{"Station", "Status", "Item", ">Out a week", ">Wait", ">Work", ">Rework", ">Queue", ">Held", "Out, by week"}, rows)
	p.para("Wait and work are medians. Held is the station's share of the hours changes spend on the line. ▲ and ▼ show a move against the weeks before, ! a move for the worse, ~ a value drawn from fewer than 5 items.")
	var missing []string
	for _, st := range r.Stations {
		if st.Missing != "" {
			missing = append(missing, st.Name+": "+st.Missing)
		}
	}
	p.list(missing)

	if len(r.Recommendations) > 0 {
		p.head(2, "Where to invest")
		rows = nil
		for i, rec := range r.Recommendations {
			name := rec.Station
			if st, ok := line.StationByID(rec.Station); ok {
				name = st.Name
			}
			rows = append(rows, []string{fmt.Sprint(i + 1), rec.Title, name, freed(rec.HoursFreed), rec.Confidence})
		}
		p.table([]string{">#", "What to do", "Station", ">Hours freed a week", "Confidence"}, rows)
		p.para("Hours freed is the waiting that goes away each week if the station gets back to the pace of its own best four weeks. It is empty where a gain cannot be sized, and 0 where no four weeks of the window did better. Confidence is how much data stands behind the numbers. `factorymeter rules` lists every rule and when it fires.")
		for i, rec := range r.Recommendations {
			p.head(3, fmt.Sprintf("%d. %s", i+1, rec.Title))
			p.para(rec.Why)
			var items []string
			for _, e := range rec.Evidence {
				if e.Text != "" {
					items = append(items, e.Text)
				}
			}
			for _, it := range rec.Items {
				items = append(items, "Look at "+standing(it))
			}
			if rec.PlayURL != "" {
				items = append(items, "Practice: "+rec.PlayURL)
			}
			p.list(items)
		}
	}

	// Agents next to people.
	classes := []contract.Slice{{Class: "agent"}, {Class: "human"}, {Class: "automation"}}
	rows = nil
	for _, id := range []string{"line.flow", "line.lead.p50", "line.size.p50", "code.work.p50", "code.rework", "ci.work.p50", "ci.rework",
		"review.wait.p50", "review.work.p50", "review.rework", "review.skipped", "merge.wait.p50", "merge.rework", "release.wait.p50"} {
		m, ok := line.ByID(id)
		row := []string{label(m), "", "", ""}
		any := false
		for i, sl := range classes {
			if s := b.get(id, sl); s != nil {
				row[i+1] = cell(s)
				any = true
			}
			row[i+1] = or(row[i+1])
		}
		if ok && any {
			rows = append(rows, row)
		}
	}
	if len(rows) > 0 {
		p.head(2, "Agents and people")
		if s := b.get("line.agent_share", contract.Slice{}); s != nil && s.Value != nil {
			p.para(fmt.Sprintf("Agents wrote %s of the changes merged by agents and people. That is a floor: only changes that carry a known signature count.", Number(s.Unit, s.Value)))
		}
		p.table([]string{"Number", ">Agents", ">People", ">Automation"}, rows)
	}
	rows = nil
	seen := map[string]bool{}
	for i := range r.Metrics {
		a := r.Metrics[i].Slice.Agent
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		sl := contract.Slice{Class: "agent", Agent: a}
		row := []string{a}
		for _, id := range []string{"line.flow", "line.lead.p50", "review.wait.p50", "review.rework", "ci.rework", "code.rework"} {
			row = append(row, or(cell(b.get(id, sl))))
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		p.head(3, "By agent")
		p.table([]string{"Agent", ">Merged a week", ">Open to merge", ">Wait for review", ">Sent back", ">CI failed", ">Abandoned"}, rows)
	}

	// Each station, and the line as a whole, number by number.
	detail := func(station string) {
		var rows [][]string
		for _, m := range line.ForStation(station) {
			s := b.get(m.ID, contract.Slice{})
			if s == nil {
				continue
			}
			rows = append(rows, []string{label(m), cell(s), value(s, s.Previous), Change(s), trendWord(s), fmt.Sprint(s.N), Spark(s)})
		}
		p.table([]string{"Number", ">Now", ">Before", ">Change", "Trend", ">Items", "By week"}, rows)
	}
	p.head(2, "Each station")
	for _, st := range r.Stations {
		if st.Status == contract.StatusNotConnected {
			continue
		}
		p.head(3, st.Name)
		if def, ok := line.StationByID(st.ID); ok {
			p.para(def.Does)
		}
		detail(st.ID)
		if len(st.Oldest) > 0 {
			var items []string
			for _, it := range st.Oldest {
				items = append(items, standing(it))
			}
			p.para("Standing longest in the queue:")
			p.list(items)
		}
	}
	p.head(3, "The whole line")
	detail(line.Whole)

	// What the numbers stand on.
	p.head(2, "Coverage")
	rows = nil
	for i, c := range r.Coverage.Repos {
		if i == 20 {
			rows = append(rows, []string{fmt.Sprintf("and %d more", len(r.Coverage.Repos)-20), "", "", ""})
			break
		}
		synced := "not finished"
		if !c.SyncedAt.IsZero() {
			synced = c.SyncedAt.UTC().Format("2006-01-02 15:04") + " UTC"
		}
		from := "-"
		if !c.From.IsZero() {
			from = day(c.From)
		}
		rows = append(rows, []string{c.Repo, from, synced, fmt.Sprint(c.Changes)})
	}
	p.table([]string{"Repository", "From", "Synced", ">PRs"}, rows)
	rows = nil
	for _, s := range r.Coverage.Sources {
		src := s.Source
		if src == "" {
			src = "not connected"
		}
		rows = append(rows, []string{strings.ReplaceAll(s.Kind, "_", " "), src, fmt.Sprint(s.Records), s.Note})
	}
	p.table([]string{"Records", "Source", ">In the window", "Note"}, rows)
	if len(r.Coverage.Trackers) > 0 {
		p.table([]string{"Tickets from", "Workspace or site", ">Tickets", ">Linked to a pull request", "Keys searched for", "Synced"}, trackerRows(r))
		p.para(TrackerKeysNote)
	}
	if len(r.Limits) > 0 {
		p.head(2, "Limits")
		p.list(r.Limits)
	}
	if u := LightsOut(r); u != "" {
		p.head(2, "Simulate it")
		p.para("Lights Out, the simulator of The Software Factory, plays this line forward with these numbers: " + u)
		p.para("The numbers ride after the # in the link. A browser never sends that part to a server.")
	}
	return p
}

// label is the name of a metric with its statistic spelled the short way.
// standing names an item and how long it has stood where it is.
func standing(it contract.Item) string {
	who := ""
	if it.Class != "" {
		who = ", " + it.Class
	}
	return fmt.Sprintf("%s, standing %s%s: %s", it.Key, Number(string(line.Hours), &it.Hours), who, it.Title)
}

// freed prints the hours a recommendation frees in a week.
func freed(v *float64) string {
	switch {
	case v == nil:
		return ""
	case *v > 0 && *v < 10:
		return fmt.Sprintf("%.1f", *v)
	}
	return fmt.Sprintf("%.0f", *v)
}

func label(m line.Metric) string {
	n := m.Name
	switch {
	case strings.HasSuffix(m.ID, ".p50") && !strings.Contains(n, "median"):
		n += ", median"
	case strings.HasSuffix(m.ID, ".p90") && !strings.Contains(n, "90th"):
		n += ", 90th percentile"
	}
	return n
}

func width(s string) int { return utf8.RuneCountInString(s) }

func pad(s string, w int, right bool) string {
	fill := strings.Repeat(" ", max(w-width(s), 0))
	if right {
		return fill + s
	}
	return s + fill
}

// Text prints the report for a terminal.
func Text(w io.Writer, r *contract.Report) error {
	var b strings.Builder
	for i, k := range build(r).blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		switch k.kind {
		case head:
			text := k.text
			if k.level <= 2 {
				text = strings.ToUpper(text)
			}
			b.WriteString(text + "\n")
			if k.level == 1 {
				b.WriteString(strings.Repeat("=", width(text)) + "\n")
			}
		case para:
			b.WriteString(strings.ReplaceAll(k.text, "**", "") + "\n")
		case list:
			for _, it := range k.items {
				b.WriteString("  * " + it + "\n")
			}
		case table:
			wide := make([]int, len(k.rows[0]))
			for _, row := range k.rows {
				for i, c := range row {
					wide[i] = max(wide[i], width(c))
				}
			}
			for _, row := range k.rows {
				var cells []string
				for i, c := range row {
					cells = append(cells, pad(c, wide[i], k.right[i]))
				}
				b.WriteString("  " + strings.TrimRight(strings.Join(cells, "  "), " ") + "\n")
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Markdown prints the report as GitHub-flavoured Markdown.
func Markdown(w io.Writer, r *contract.Report) error {
	esc := strings.NewReplacer("|", "\\|", "\n", " ")
	var b strings.Builder
	for i, k := range build(r).blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		switch k.kind {
		case head:
			b.WriteString(strings.Repeat("#", k.level) + " " + k.text + "\n")
		case para:
			b.WriteString(k.text + "\n")
		case list:
			for _, it := range k.items {
				b.WriteString("- " + it + "\n")
			}
		case table:
			for n, row := range k.rows {
				b.WriteString("|")
				for _, c := range row {
					b.WriteString(" " + esc.Replace(c) + " |")
				}
				b.WriteString("\n")
				if n == 0 {
					b.WriteString("|")
					for _, right := range k.right {
						if right {
							b.WriteString(" ---: |")
						} else {
							b.WriteString(" --- |")
						}
					}
					b.WriteString("\n")
				}
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
