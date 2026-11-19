package view

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"math"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

var (
	//go:embed assets/page.html
	pageHTML string
	//go:embed assets/page.css
	pageCSS string
	//go:embed assets/page.js
	pageJS string

	pageTmpl = template.Must(template.New("page").Parse(pageHTML))
)

func pin(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// CSP is the content security policy of the page. It lets the page run its
// own style and its own script, each pinned by its hash, and nothing else:
// no request to any host, no frame, no form.
func CSP() string {
	return "default-src 'none'; style-src " + pin(pageCSS) + "; script-src " + pin(pageJS) + "; img-src data:; base-uri 'none'; form-action 'none'"
}

// Link is one choice a served page offers: a window or a repository.
type Link struct {
	Label, URL string
	Current    bool
}

// Nav holds the links that only a served page has. A page written to a file
// has none: it shows one window and cannot ask for another.
type Nav struct {
	Windows []Link
	// Repos is every repository of an owner, with the whole owner first.
	Repos []Link
	// JSON is the link to the same report as factorymeter/v1 JSON.
	JSON string
}

// HTMLOptions sets what the page shows besides the report.
type HTMLOptions struct {
	Nav *Nav
}

// fig is one value with the way it moved.
type fig struct {
	Value string
	// Arrow is ▲ or ▼, and empty when the value did not move.
	Arrow string
	// Tone is "better" or "worse", and empty when the move is neither.
	Tone string
	// Moved says the move in words, for a reader who cannot see the arrow.
	Moved string
	Thin  bool
}

func figure(s *contract.Series) fig {
	if s == nil {
		return fig{Value: "-"}
	}
	f := fig{Value: value(s, s.Value), Thin: s.Thin && s.Value != nil}
	switch moved(s) {
	case 1:
		f.Arrow, f.Moved = "▲", "Up"
	case -1:
		f.Arrow, f.Moved = "▼", "Down"
	default:
		return f
	}
	if c := Change(s); c != "" {
		f.Moved += " " + strings.TrimLeft(c, "+-")
	}
	f.Moved += " against the weeks before"
	if s.Trend == contract.TrendBetter || s.Trend == contract.TrendWorse {
		f.Tone = s.Trend
		f.Moved += ", for the " + s.Trend
	}
	return f
}

type tile struct {
	Label string
	Fig   fig
	// Was says the value of the window before, or is empty.
	Was   string
	Spark template.HTML
}

type stationCard struct {
	ID, Name    string
	Step        int
	Status      string
	StatusLabel string
	Connected   bool
	Constraint  bool
	Out, Wait   fig
	Held        string
	Meter       template.HTML
	Spark       template.HTML
}

type itemRow struct {
	Key, URL, Title, Class, Standing string
}

type recCard struct {
	Rank                            int
	Title, Station, StationID, Rule string
	Confidence, Why, PlayURL, Freed string
	Evidence                        []string
	Items                           []itemRow
	Bar                             template.HTML
	Sized                           bool
	// Linked is false when the station has no part of the page to jump to.
	Linked                           bool
	ItemsLabel, FreedUnit, RuleFires string
}

type row struct {
	Label, URL string
	Cells      []fig
}

type grid struct {
	Head []string
	Rows []row
}

type plotCard struct {
	Title string
	Chart template.HTML
}

type metricCard struct {
	ID, Name, Definition  string
	Fig                   fig
	Before, Change, Trend string
	N                     int
	Chart                 template.HTML
}

type weekRow struct {
	Week  string
	Pre   bool
	Cells []string
}

type stationPane struct {
	ID, Name, Does, Item, Arrives, Leaves string
	Status, Missing                       string
	Constraint                            bool
	Cards                                 []metricCard
	Oldest                                []itemRow
	Head                                  []string
	Weeks                                 []weekRow
}

type htmlPage struct {
	Title, Target, Headline, Detail, Span string
	CSP, Weeks                            string
	CSS                                   template.CSS
	JS                                    template.JS
	Nav                                   *Nav
	Summary                               bool
	Tiles                                 []tile
	Stations                              []stationCard
	Missing                               []string
	Recs                                  []recCard
	AgentShare                            string
	Classes                               grid
	ClassPlots                            []plotCard
	Automation                            bool
	Agents, Repos                         grid
	Panes                                 []stationPane
	CoverRepos                            [][]string
	MoreRepos                             string
	Sources                               [][]string
	Limits                                []string
	Tool, Generated                       string
}

// safeURL keeps a link only when it is plain https. A report can come from a
// file somebody else wrote, so a link is not trusted to be one.
func safeURL(u string) string {
	if strings.HasPrefix(u, "https://") && !strings.ContainsAny(u, " \t\r\n\"'<>`") {
		return u
	}
	return ""
}

func itemOf(it contract.Item) itemRow {
	return itemRow{Key: it.Key, URL: safeURL(it.URL), Title: it.Title, Class: it.Class, Standing: Number(string(line.Hours), &it.Hours)}
}

var classNames = map[string]string{"agent": "Agents", "human": "People", "automation": "Automation"}

func buildHTML(r *contract.Report, opt HTMLOptions) *htmlPage {
	b := open(r)
	whole := contract.Slice{}
	weeks := int(math.Round(r.Window.Until.Sub(r.Window.Since).Hours() / (7 * 24)))
	p := &htmlPage{
		Title: "Delivery line", Target: r.Target, Headline: r.Verdict.Headline, Detail: r.Verdict.Detail,
		CSP: CSP(), CSS: template.CSS(pageCSS), JS: template.JS(pageJS), Nav: opt.Nav, Summary: r.Summary, Limits: r.Limits,
	}
	if r.Target != "" {
		p.Title += ": " + r.Target
	}
	p.Span = fmt.Sprintf("%s, %s to %s.", plural(weeks, "week"), day(r.Window.Since), day(r.Window.Until))
	if r.Window.Previous != nil {
		p.Span += fmt.Sprintf(" Compared with the %s before.", plural(weeks, "week"))
	}
	if n := len(r.Window.Periods); n > weeks {
		p.Span += fmt.Sprintf(" The charts go back %s. The weeks before the window are dimmed.", plural(n, "week"))
	}
	p.Tool = strings.TrimSpace(r.Tool.Name + " " + r.Tool.Version)
	if !r.GeneratedAt.IsZero() {
		p.Generated = r.GeneratedAt.UTC().Format("2006-01-02 15:04") + " UTC"
	}

	// The week each point of a chart stands for, once for the whole page.
	var ends []string
	for _, per := range r.Window.Periods {
		ends = append(ends, "Week to "+per.To.Format("Jan 2, 2006"))
	}
	p.Weeks = strings.Join(ends, "|")

	// The numbers the page leads with.
	for _, id := range []string{"line.flow", "line.lead.p50", "line.delivery.p50", "release.deploys", "line.agent_share"} {
		m, ok := line.ByID(id)
		s := b.get(id, whole)
		if !ok || s == nil || s.Value == nil {
			continue
		}
		t := tile{Label: label(m), Fig: figure(s), Spark: spark(r, m, s)}
		if s.Previous != nil {
			t.Was = fmt.Sprintf("%s in the %s before", value(s, s.Previous), plural(weeks, "week"))
			if c := Change(s); c != "" && t.Fig.Arrow != "" {
				t.Was = c + ". " + capitalize(t.Was)
			}
		}
		p.Tiles = append(p.Tiles, t)
	}

	// The line: every station side by side.
	status := map[string]string{contract.StatusMeasured: "measured", contract.StatusPartial: "partly measured", contract.StatusNotConnected: "not connected"}
	for i, st := range r.Stations {
		c := stationCard{ID: st.ID, Name: st.Name, Step: i + 1, Status: st.Status, StatusLabel: status[st.Status],
			Connected: st.Status != contract.StatusNotConnected, Constraint: st.Constraint}
		if c.Connected {
			c.Out, c.Wait = figure(b.get(st.ID+".flow", whole)), figure(b.get(st.ID+".wait.p50", whole))
			if m, ok := line.ByID(st.ID + ".flow"); ok {
				c.Spark = spark(r, m, b.get(m.ID, whole))
			}
			if st.HeldShare != nil {
				c.Held, c.Meter = Number(string(line.Share), st.HeldShare), meter(*st.HeldShare)
			}
		}
		p.Stations = append(p.Stations, c)
		if st.Missing != "" {
			p.Missing = append(p.Missing, st.Name+": "+st.Missing)
		}
	}

	// Where to invest. The bar sets each gain against the largest one.
	shown := map[string]bool{}
	for _, st := range r.Stations {
		shown[st.ID] = st.Status != contract.StatusNotConnected
	}
	most := 0.0
	for _, rec := range r.Recommendations {
		if rec.HoursFreed != nil {
			most = math.Max(most, *rec.HoursFreed)
		}
	}
	for i, rec := range r.Recommendations {
		c := recCard{Rank: i + 1, Title: rec.Title, Station: rec.Station, StationID: rec.Station, Rule: rec.Rule, Confidence: rec.Confidence,
			Why: rec.Why, PlayURL: safeURL(rec.PlayURL), Freed: freed(rec.HoursFreed), Sized: rec.HoursFreed != nil && *rec.HoursFreed > 0, Linked: shown[rec.Station]}
		if st, ok := line.StationByID(rec.Station); ok {
			c.Station = st.Name
		}
		if def, ok := line.RuleByID(rec.Rule); ok {
			c.RuleFires = def.Fires
		}
		if c.Sized {
			c.FreedUnit = "hours freed a week"
			if most > 0 {
				c.Bar = meter(*rec.HoursFreed / most)
			}
		}
		for _, e := range rec.Evidence {
			if e.Text != "" {
				c.Evidence = append(c.Evidence, e.Text)
			}
		}
		for _, it := range rec.Items {
			c.Items = append(c.Items, itemOf(it))
		}
		if n := len(c.Items); n > 0 {
			c.ItemsLabel = plural(n, "item") + " to look at"
		}
		p.Recs = append(p.Recs, c)
	}

	// Agents next to people.
	classes := []contract.Slice{{Class: "agent"}, {Class: "human"}, {Class: "automation"}}
	p.Classes.Head = []string{"Number", "Agents", "People", "Automation"}
	for _, id := range []string{"line.flow", "line.lead.p50", "line.size.p50", "code.work.p50", "code.rework", "ci.work.p50", "ci.rework",
		"review.wait.p50", "review.work.p50", "review.rework", "review.skipped", "merge.wait.p50", "merge.rework", "release.wait.p50"} {
		m, ok := line.ByID(id)
		rw := row{Label: label(m)}
		found := false
		for i, sl := range classes {
			s := b.get(id, sl)
			found = found || s != nil
			p.Automation = p.Automation || i == 2 && s != nil
			rw.Cells = append(rw.Cells, figure(s))
		}
		if ok && found {
			p.Classes.Rows = append(p.Classes.Rows, rw)
		}
	}
	if s := b.get("line.agent_share", whole); s != nil && s.Value != nil {
		p.AgentShare = Number(s.Unit, s.Value)
	}
	for _, id := range []string{"line.flow", "line.lead.p50", "review.wait.p50", "ci.rework"} {
		m, ok := line.ByID(id)
		if !ok {
			continue
		}
		c := chart{r: r, m: m, stack: barred(m), label: label(m) + ", week by week, agents and people"}
		for i, sl := range classes {
			if s := b.get(id, sl); s != nil && (i < 2 || c.stack) {
				c.tracks = append(c.tracks, track{name: classNames[sl.Class], key: fmt.Sprintf("s%d", i+1), s: s})
			}
		}
		if len(c.tracks) > 0 {
			if drawn := c.draw(); drawn != "" {
				p.ClassPlots = append(p.ClassPlots, plotCard{Title: label(m), Chart: drawn})
			}
		}
	}

	// One row per agent, and one per repository when the report is of an owner.
	links := map[string]string{}
	if opt.Nav != nil {
		for _, l := range opt.Nav.Repos {
			links[l.Label] = l.URL
		}
	}
	cut := func(ids []string, name func(contract.Slice) string, slice func(string) contract.Slice) []row {
		var rows []row
		seen := map[string]bool{}
		for i := range r.Metrics {
			n := name(r.Metrics[i].Slice)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			rw := row{Label: n, URL: links[n]}
			for _, id := range ids {
				rw.Cells = append(rw.Cells, figure(b.get(id, slice(n))))
			}
			rows = append(rows, rw)
		}
		return rows
	}
	p.Agents.Head = []string{"Agent", "Merged a week", "Open to merge", "Wait for review", "Sent back", "CI failed", "Abandoned"}
	p.Agents.Rows = cut([]string{"line.flow", "line.lead.p50", "review.wait.p50", "review.rework", "ci.rework", "code.rework"},
		func(s contract.Slice) string { return s.Agent }, func(n string) contract.Slice { return contract.Slice{Class: "agent", Agent: n} })
	p.Repos.Head = []string{"Repository", "Merged a week", "Open to merge", "Wait for review", "CI failed", "Wait for a release"}
	p.Repos.Rows = cut([]string{"line.flow", "line.lead.p50", "review.wait.p50", "ci.rework", "release.wait.p50"},
		func(s contract.Slice) string { return s.Repo }, func(n string) contract.Slice { return contract.Slice{Repo: n} })

	// Each station, and the line as a whole, number by number.
	first := windowStart(r)
	pane := func(id string) stationPane {
		var sp stationPane
		var cols []*contract.Series
		for _, m := range line.ForStation(id) {
			s := b.get(m.ID, whole)
			if s == nil {
				continue
			}
			c := metricCard{ID: m.ID, Name: label(m), Definition: m.Definition, Fig: figure(s), Change: Change(s), Trend: trendWord(s), N: s.N,
				Chart: chart{r: r, m: m, tracks: []track{{key: "s1", s: s}}, label: label(m) + ", week by week"}.draw()}
			if s.Previous != nil {
				c.Before = value(s, s.Previous)
			}
			sp.Cards = append(sp.Cards, c)
			if len(s.Points) >= len(r.Window.Periods) {
				sp.Head, cols = append(sp.Head, label(m)), append(cols, s)
			}
		}
		for i, per := range r.Window.Periods {
			if len(cols) == 0 {
				break
			}
			wr := weekRow{Week: day(per.To), Pre: i < first}
			for _, s := range cols {
				wr.Cells = append(wr.Cells, value(s, s.Points[i].Value))
			}
			sp.Weeks = append(sp.Weeks, wr)
		}
		return sp
	}
	for _, st := range r.Stations {
		if st.Status == contract.StatusNotConnected {
			continue
		}
		sp := pane(st.ID)
		sp.ID, sp.Name, sp.Status, sp.Missing, sp.Constraint = st.ID, st.Name, st.Status, st.Missing, st.Constraint
		if def, ok := line.StationByID(st.ID); ok {
			sp.Does, sp.Item, sp.Arrives, sp.Leaves = def.Does, def.Item, def.Arrives, def.Leaves
		}
		for _, it := range st.Oldest {
			sp.Oldest = append(sp.Oldest, itemOf(it))
		}
		p.Panes = append(p.Panes, sp)
	}
	if sp := pane(line.Whole); len(sp.Cards) > 0 {
		sp.ID, sp.Name, sp.Does = line.Whole, "The whole line", "The numbers of the line from end to end, over the changes merged in each week."
		p.Panes = append(p.Panes, sp)
	}

	// What the numbers stand on.
	for i, c := range r.Coverage.Repos {
		if i == 20 {
			p.MoreRepos = fmt.Sprintf("and %d more", len(r.Coverage.Repos)-20)
			break
		}
		synced, from := "not finished", "-"
		if !c.SyncedAt.IsZero() {
			synced = c.SyncedAt.UTC().Format("2006-01-02 15:04") + " UTC"
		}
		if !c.From.IsZero() {
			from = day(c.From)
		}
		p.CoverRepos = append(p.CoverRepos, []string{c.Repo, from, synced, fmt.Sprint(c.Changes)})
	}
	for _, s := range r.Coverage.Sources {
		src := s.Source
		if src == "" {
			src = "not connected"
		}
		p.Sources = append(p.Sources, []string{strings.ReplaceAll(s.Kind, "_", " "), src, fmt.Sprint(s.Records), s.Note})
	}
	return p
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// HTML writes the report as one page with its charts. The page carries its
// own style and script and loads nothing: it reads the same offline, from a
// file, and from "factorymeter serve".
func HTML(w io.Writer, r *contract.Report, opt HTMLOptions) error {
	return pageTmpl.Execute(w, buildHTML(r, opt))
}
