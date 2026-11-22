package view

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/url"
	"strings"
	"time"

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

// ServedCSP is the policy of the page serve shows. Its repository menu is a
// form that asks the same server for the page again, so a form may go there.
func ServedCSP() string {
	return strings.Replace(CSP(), "form-action 'none'", "form-action 'self'", 1)
}

// Link is one choice a served page offers: a window or a repository.
type Link struct {
	Label, URL string
	Current    bool
}

// Field is a name and value a form sends.
type Field struct{ Name, Value string }

// Nav holds the links that only a served page has. A page written to a file
// has none: it shows one window and cannot ask for another.
type Nav struct {
	Windows []Link
	// Repos is every repository of an owner, Current when it is in the line
	// shown. The menu is a form that asks for the ticked ones.
	Repos []Link
	// RepoLabel names what the line is made of: "All repositories", one
	// name, or "3 of 13 repositories".
	RepoLabel string
	// AllRepos is the address of the whole owner.
	AllRepos string
	// Keep are the other settings of the page, sent again with the form.
	Keep []Field
	// Drill is the address of the items behind a number, with the settings
	// of the page and without the metric. Empty: the page cannot drill.
	Drill string
	// From and Until are the dates of the window, for the date form, and
	// Repos the repositories the form sends again.
	From, Until string
	KeepRepos   []string
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

// pop is what a reader gets by pointing at the info mark of a number: what it
// counts, in words.
type pop struct {
	ID    string
	Lines []string
	Code  string
}

type tile struct {
	Label, Basis string
	Fig          fig
	// Was says the value of the window before, or is empty.
	Was   string
	Spark template.HTML
	Info  pop
}

type stationCard struct {
	ID, Name    string
	Step        int
	Status      string
	StatusLabel string
	Connected   bool
	Constraint  bool
	// Next is the station that holds the most after the constraint.
	Next      bool
	Out, Wait fig
	// OutLabel names what leaves the station: "PRs out a week".
	OutLabel string
	Held     string
	Meter    template.HTML
	Spark    template.HTML
	Health   health
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
	// Linked is false when the station has no panel on the page to open.
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
	Info  pop
}

type metricCard struct {
	ID, Name, Basis, Was string
	// DrillURL lists the items of the number, DrillLabel says how many.
	DrillURL, DrillLabel string
	Fig                  fig
	// Alt is the 90th percentile of a card that also shows the median.
	Alt   *fig
	Chart template.HTML
	Info  pop
}

type weekRow struct {
	Week  string
	Pre   bool
	Cells []string
}

type stationPane struct {
	ID, Name, Does, Item, Arrives, Leaves string
	Status, Missing, Needs                string
	Off, Constraint                       bool
	Health                                health
	// Invest is the recommendations about this station, or every one for
	// the whole line.
	Invest []recLink
	// Next says which station would hold the line once this one is freed.
	Next  string
	Cards []metricCard
	// Empty names the numbers that have no data in the window.
	Empty  string
	Oldest []itemRow
	Head   []string
	Weeks  []weekRow
}

// recLink points from a station to a recommendation on the invest page.
type recLink struct {
	Rank         int
	Title, Freed string
}

// linkTo is the short form of a recommendation, with its gain when it has one.
func linkTo(rc recCard) recLink {
	l := recLink{Rank: rc.Rank, Title: rc.Title}
	if rc.Sized {
		l.Freed = rc.Freed
	}
	return l
}

// lead names the constraint and the station next in line, for the line.
type lead struct {
	Constraint, ConstraintID, ConstraintHeld string
	Next, NextID, NextHeld                   string
}

// tally is one part of the health of the line: how many stations have a grade.
type tally struct{ Class, Icon, Text string }

type htmlPage struct {
	Title, Target, Headline, Detail, Span string
	CSP, Weeks                            string
	CSS                                   template.CSS
	JS                                    template.JS
	Nav                                   *Nav
	Summary                               bool
	Tiles                                 []tile
	Stations                              []stationCard
	Health                                []tally
	HealthInfo                            pop
	Recs                                  []recCard
	Lead                                  *lead
	AgentShare                            string
	Classes                               grid
	ClassPlots                            []plotCard
	Automation                            bool
	Agents, Repos                         grid
	Panes                                 []stationPane
	CoverRepos                            [][]string
	MoreRepos                             string
	Sources                               [][]string
	Trackers                              [][]string
	TrackerNote                           string
	Limits                                []string
	Tool, Generated                       string
	// LightsOut is the link to the simulator, filled in with this line.
	LightsOut string
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

// connect says what a station is measured from and how to get it into the
// store, for a reader whose station is not connected.
var connect = map[string]string{
	line.Spec:    "Tickets. GitHub issues are read unless sync runs with --skip-tickets. To read Linear, sync with --linear and set LINEAR_API_KEY. To read Jira, sync with --jira and the site, and set JIRA_EMAIL and JIRA_API_TOKEN.",
	line.Code:    "Pull requests. Every sync reads them.",
	line.CI:      "GitHub Actions runs on the commits of pull requests. Sync reads them unless it runs with --skip-ci. Runs of another CI system are not read.",
	line.Verify:  fmt.Sprintf("Deployments of a pull request's own commits, made before it merges, to an environment whose name does not say prod, production or live. They are read from the GitHub Deployments API. Preview deploys of Vercel, Netlify or Render create them, and so does a GitHub Actions job that runs on pull requests with an environment such as staging. A deployment of the branch the change merges into, a deployment after the merge and GitHub Pages do not count. A repository needs at least %d of them.", line.MinSample),
	line.Review:  "Pull request reviews. Every sync reads them. A review counts when a person other than the author wrote it.",
	line.Merge:   "Pull requests. Every sync reads them.",
	line.Release: "Production deploys: deployments in the GitHub Deployments API to an environment whose name says prod, production or live. A repository with none uses its published releases instead. Sync reads them unless it runs with --skip-deploys.",
}

// name is the name of a metric on the page. A count of a week says so, since
// the page shows no unit beside it.
func name(m line.Metric) string {
	if m.Unit == line.PerWeek {
		return label(m) + " a week"
	}
	return label(m)
}

// basis says what a value is drawn from, in a few words.
func basis(m line.Metric, s *contract.Series, weeks int) string {
	what := noun(m.ID)
	switch {
	case m.Unit == line.PerWeek:
		return fmt.Sprintf("Average week: %s in %s", plural(s.N, what), plural(weeks, "week"))
	case m.Kind == line.Queue:
		return "At the end of the window"
	case m.Kind == line.Held && m.Unit == line.Hours:
		return "Average week"
	case strings.HasSuffix(m.ID, ".p50"):
		return "Median of " + plural(s.N, what)
	case strings.HasSuffix(m.ID, ".p90"):
		return "90th percentile of " + plural(s.N, what)
	case m.Unit == line.Share:
		return "Share of " + plural(s.N, what)
	}
	return plural(s.N, what)
}

// noun is what a metric counts, as its definition says: tickets at spec, CI
// rounds at CI, deployments for the time and failures of a deploy, and pull
// requests for the rest.
func noun(id string) string {
	station, rest, _ := strings.Cut(id, ".")
	switch {
	case station == line.Spec:
		return "ticket"
	case station == line.CI:
		return "CI round"
	case id == "release.deploys":
		return "deploy"
	case (station == line.Verify && !strings.HasPrefix(rest, "flow") && !strings.HasPrefix(rest, "held")) ||
		(station == line.Release && (strings.HasPrefix(rest, "work") || rest == "rework")):
		return "deployment"
	}
	return "PR"
}

// was says what a value was in the window before, and how it moved.
func was(s *contract.Series, weeks int) string {
	if s.Previous == nil {
		return "Nothing before to compare with"
	}
	out := fmt.Sprintf("The %s before: %s", plural(weeks, "week"), value(s, s.Previous))
	var how []string
	if c := Change(s); c != "" {
		how = append(how, c)
	}
	if t := trendWord(s); t != "" {
		how = append(how, t)
	}
	if len(how) > 0 {
		out += " (" + strings.Join(how, ", ") + ")"
	}
	return out
}

// until is the last day of a span that ends at t, which is not in it.
func until(t time.Time) time.Time { return t.Add(-time.Nanosecond) }

func date(t time.Time) string { return t.UTC().Format("Jan 2, 2006") }

func csp(opt HTMLOptions) string {
	if opt.Nav != nil {
		return ServedCSP()
	}
	return CSP()
}

func buildHTML(r *contract.Report, opt HTMLOptions) *htmlPage {
	b := open(r)
	whole := contract.Slice{}
	weeks := int(math.Round(r.Window.Until.Sub(r.Window.Since).Hours() / (7 * 24)))
	p := &htmlPage{
		Title: "Delivery line", Target: r.Target, Headline: r.Verdict.Headline, Detail: r.Verdict.Detail,
		CSP: csp(opt), CSS: template.CSS(pageCSS), JS: template.JS(pageJS), Nav: opt.Nav, Summary: r.Summary, Limits: r.Limits,
		HealthInfo: pop{ID: "health-rule", Lines: []string{HealthRule}},
		LightsOut:  safeURL(LightsOut(r)),
	}
	if r.Target != "" {
		p.Title += ": " + r.Target
	}
	p.Span = fmt.Sprintf("Every number covers the %s from %s to %s. A count is the average week of those weeks. A time is the median, or the 90th percentile, of the items in them.",
		plural(weeks, "week"), date(r.Window.Since), date(until(r.Window.Until)))
	if pr := r.Window.Previous; pr != nil {
		p.Span += fmt.Sprintf(" Arrows compare with the %s before, %s to %s.", plural(weeks, "week"), date(pr.From), date(until(pr.To)))
	} else {
		p.Span += fmt.Sprintf(" The store does not reach back to the %s before, so nothing is compared.", plural(weeks, "week"))
	}
	if n := len(r.Window.Periods); n > weeks {
		p.Span += fmt.Sprintf(" The charts go back %s, and the weeks before the window are dimmed.", plural(n, "week"))
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
		t := tile{Label: name(m), Basis: basis(m, s, weeks), Fig: figure(s), Spark: spark(r, m, s), Was: was(s, weeks),
			Info: pop{ID: "t-" + m.ID, Lines: []string{m.Definition}, Code: m.ID}}
		p.Tiles = append(p.Tiles, t)
	}

	// The line: every station side by side, with its health.
	status := map[string]string{contract.StatusMeasured: "measured", contract.StatusPartial: "partly measured", contract.StatusNotConnected: "not connected"}
	count := map[string]int{}
	for i, st := range r.Stations {
		c := stationCard{ID: st.ID, Name: st.Name, Step: i + 1, Status: st.Status, StatusLabel: status[st.Status],
			Connected: st.Status != contract.StatusNotConnected, Constraint: st.Constraint, Health: grade(st, b, weeks),
			OutLabel: outLabel(st.ID)}
		count[c.Health.Class]++
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
	}
	// The constraint, and the station that would hold the line once it is
	// freed: the one with the next largest share of the hours.
	ci, ni := -1, -1
	for i, st := range r.Stations {
		if st.Constraint {
			ci = i
		}
	}
	for i, st := range r.Stations {
		if i == ci || st.HeldShare == nil || *st.HeldShare <= 0 {
			continue
		}
		if ni < 0 || *st.HeldShare > *r.Stations[ni].HeldShare {
			ni = i
		}
	}
	if ci >= 0 {
		c := r.Stations[ci]
		p.Lead = &lead{Constraint: c.Name, ConstraintID: c.ID}
		if c.HeldShare != nil {
			p.Lead.ConstraintHeld = Number(string(line.Share), c.HeldShare)
		}
		if ni >= 0 {
			n := r.Stations[ni]
			p.Stations[ni].Next = true
			p.Lead.Next, p.Lead.NextID, p.Lead.NextHeld = n.Name, n.ID, Number(string(line.Share), n.HeldShare)
		}
	}
	for _, k := range []struct{ class, one, many string }{
		{"bad", "strained", "strained"}, {"warn", "to watch", "to watch"}, {"good", "flowing", "flowing"},
		{"none", "with too little data", "with too little data"}, {"off", "not connected", "not connected"},
	} {
		if n := count[k.class]; n > 0 {
			icon := map[string]string{"bad": "✕", "warn": "!", "good": "✓", "none": "·", "off": "○"}[k.class]
			p.Health = append(p.Health, tally{Class: k.class, Icon: icon, Text: fmt.Sprintf("%d %s", n, k.many)})
		}
	}

	// Where to invest. The bar sets each gain against the largest one.
	shown := map[string]bool{}
	for _, st := range r.Stations {
		shown[st.ID] = true
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
		rw := row{Label: name(m)}
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
		c := chart{r: r, m: m, stack: barred(m), label: name(m) + ", week by week, agents and people"}
		for i, sl := range classes {
			if s := b.get(id, sl); s != nil && (i < 2 || c.stack) {
				c.tracks = append(c.tracks, track{name: classNames[sl.Class], key: fmt.Sprintf("s%d", i+1), s: s})
			}
		}
		if len(c.tracks) > 0 {
			if drawn := c.draw(); drawn != "" {
				p.ClassPlots = append(p.ClassPlots, plotCard{Title: name(m), Chart: drawn, Info: pop{ID: "a-" + m.ID, Lines: []string{m.Definition}, Code: m.ID}})
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
	p.Agents.Head = []string{"Agent", "Merged a week", "Open to merge, median", "Wait for review, median", "Sent back", "CI failed", "Abandoned"}
	p.Agents.Rows = cut([]string{"line.flow", "line.lead.p50", "review.wait.p50", "review.rework", "ci.rework", "code.rework"},
		func(s contract.Slice) string { return s.Agent }, func(n string) contract.Slice { return contract.Slice{Class: "agent", Agent: n} })
	p.Repos.Head = []string{"Repository", "Merged a week", "Open to merge, median", "Wait for review, median", "CI failed", "Wait for a release, median"}
	p.Repos.Rows = cut([]string{"line.flow", "line.lead.p50", "review.wait.p50", "ci.rework", "release.wait.p50"},
		func(s contract.Slice) string { return s.Repo }, func(n string) contract.Slice { return contract.Slice{Repo: n} })

	// Each station, and the line as a whole, number by number. A median and
	// its 90th percentile share one card and one chart: they are two cuts of
	// the same items.
	first := windowStart(r)
	pane := func(id string) stationPane {
		var sp stationPane
		var cols []*contract.Series
		var empty []string
		for _, m := range line.ForStation(id) {
			s := b.get(m.ID, whole)
			if s == nil {
				continue
			}
			if len(s.Points) >= len(r.Window.Periods) {
				sp.Head, cols = append(sp.Head, label(m)), append(cols, s)
			}
			base, isP90 := strings.CutSuffix(m.ID, ".p90")
			if isP90 && b.get(base+".p50", whole) != nil {
				continue
			}
			c := metricCard{ID: m.ID, Name: name(m), Basis: basis(m, s, weeks), Fig: figure(s), Was: was(s, weeks),
				Info: pop{ID: "c-" + m.ID, Lines: []string{m.Definition}, Code: m.ID}}
			ch := chart{r: r, m: m, tracks: []track{{key: "s1", s: s}}, label: name(m) + ", week by week"}
			if base, ok := strings.CutSuffix(m.ID, ".p50"); ok {
				if hi := b.get(base+".p90", whole); hi != nil {
					f := figure(hi)
					c.Alt = &f
					c.Name = strings.TrimSuffix(label(m), ", median")
					c.Basis = "Median and 90th percentile of " + plural(s.N, noun(m.ID))
					c.Info.Lines = []string{strings.TrimSuffix(m.Definition, " The median."),
						"The median is the middle item: half took less. The 90th percentile is the slow end: one item in ten took longer."}
					c.Info.Code = m.ID + ", " + base + ".p90"
					ch.tracks = []track{{name: "Median", key: "s1", s: s}, {name: "90th percentile", key: "s1 p90", s: hi}}
					ch.label = c.Name + ", median and 90th percentile, week by week"
				}
			}
			if opt.Nav != nil && opt.Nav.Drill != "" && !r.Summary {
				sep := "?"
				if strings.Contains(opt.Nav.Drill, "?") {
					sep = "&"
				}
				c.DrillURL = opt.Nav.Drill + sep + "metric=" + url.QueryEscape(m.ID)
				ch.drill = c.DrillURL + "&week="
				if s.N > 0 {
					what := noun(m.ID)
					if m.Kind == line.Queue {
						c.DrillLabel = "See the " + plural(s.N, what) + " in the queue"
					} else {
						c.DrillLabel = "See the " + plural(s.N, what)
					}
				}
			}
			c.Chart = ch.draw()
			if c.Chart == "" && s.Value == nil {
				empty = append(empty, lower(c.Name))
				continue
			}
			sp.Cards = append(sp.Cards, c)
		}
		if len(empty) > 0 {
			sp.Empty = "No data in the window for " + strings.Join(empty, ", ") + "."
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
	for i, st := range r.Stations {
		var sp stationPane
		if st.Status != contract.StatusNotConnected {
			sp = pane(st.ID)
		}
		sp.ID, sp.Name, sp.Status, sp.Missing, sp.Constraint = st.ID, st.Name, st.Status, st.Missing, st.Constraint
		sp.Off, sp.Health = st.Status == contract.StatusNotConnected, p.Stations[i].Health
		if sp.Off || st.Status == contract.StatusPartial {
			sp.Needs = connect[st.ID]
		}
		if def, ok := line.StationByID(st.ID); ok {
			sp.Does, sp.Item, sp.Arrives, sp.Leaves = def.Does, def.Item, def.Arrives, def.Leaves
		}
		for _, it := range st.Oldest {
			sp.Oldest = append(sp.Oldest, itemOf(it))
		}
		for _, rc := range p.Recs {
			if rc.StationID == st.ID {
				sp.Invest = append(sp.Invest, linkTo(rc))
			}
		}
		if p.Lead != nil && st.Constraint && p.Lead.Next != "" {
			sp.Next = fmt.Sprintf("Once %s holds less, %s is next in line: it holds %s of the hours.", st.Name, p.Lead.Next, p.Lead.NextHeld)
		}
		p.Panes = append(p.Panes, sp)
	}
	if sp := pane(line.Whole); len(sp.Cards) > 0 {
		sp.ID, sp.Name, sp.Does = line.Whole, "The whole line", "The numbers of the line from end to end, over the changes merged in each week."
		for _, rc := range p.Recs {
			sp.Invest = append(sp.Invest, linkTo(rc))
		}
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
	p.Trackers, p.TrackerNote = trackerRows(r), TrackerKeysNote
	return p
}

// TrackerKeysNote says where the keys searched for come from.
const TrackerKeysNote = "A pull request is tied to a ticket when its branch, title or description names a key with one of these prefixes, such as ENG-12, in any case. The prefixes are read from the tickets themselves, so a team or project that is added later is searched for after the next sync. Linear also ties the pull requests it has attached."

// trackerRows are the rows of the table of ticket systems: one per Linear
// workspace or Jira site.
func trackerRows(r *contract.Report) [][]string {
	var rows [][]string
	for _, t := range r.Coverage.Trackers {
		name, keys, synced := t.Name, strings.Join(t.Keys, ", "), "not finished"
		if name == "" {
			name = "-"
		}
		if keys == "" {
			keys = "-"
		}
		if !t.SyncedAt.IsZero() {
			synced = t.SyncedAt.UTC().Format("2006-01-02 15:04") + " UTC"
		}
		system := map[string]string{"linear": "Linear", "jira": "Jira"}[t.Source]
		if system == "" {
			system = t.Source
		}
		rows = append(rows, []string{system, name, fmt.Sprint(t.Tickets), fmt.Sprint(t.Linked), keys, synced})
	}
	return rows
}

// HTML writes the report as one page with its charts. The page carries its
// own style and script and loads nothing: it reads the same offline, from a
// file, and from "factorymeter serve".
func HTML(w io.Writer, r *contract.Report, opt HTMLOptions) error {
	return pageTmpl.Execute(w, buildHTML(r, opt))
}
