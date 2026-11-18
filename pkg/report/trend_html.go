package report

import (
	"fmt"
	"html"
	"html/template"
	"io"
	"math"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/metrics"
)

// The charts are plain SVG written here, so the page stays one file with no
// scripts. Hover works with CSS alone: each period has an invisible column
// that shows its own crosshair and tip while the pointer is over it.

const (
	kindLine = iota
	kindColumns
	kindShares
)

// mark is one series of a chart: a line, or one layer of the columns.
type mark struct {
	// key picks the color: a class or a stage.
	key   string
	label string
	pts   []metrics.Point
}

type figure struct {
	t     *metrics.Trend
	title string
	unit  string
	kind  int
	marks []mark
	note  string
}

var classKey = map[string]string{
	metrics.ClassAgent: "agent", metrics.ClassHuman: "human", metrics.ClassAutomation: "auto", metrics.ClassLine: "all",
}

var classLabel = map[string]string{
	metrics.ClassAgent: "Agent", metrics.ClassHuman: "Human", metrics.ClassAutomation: "Automation", metrics.ClassLine: "All",
}

// columnMetrics are counts. They are drawn as columns, not lines.
var columnMetrics = map[string]bool{"merged_per_week": true, "deploys_per_week": true, "waiting_for_review": true}

const sharePrefix = "stage_share_"

// charts builds the figures of one group, one per metric. Agent and human
// share a chart. The stage shares share one chart of stacked columns.
func charts(t *metrics.Trend, group string) []template.HTML {
	var figs []*figure
	byMetric := map[string]*figure{}
	one, _ := t.PeriodWord()
	for i := range t.Series {
		s := &t.Series[i]
		if s.Group != group {
			continue
		}
		id, m := s.Metric, mark{key: classKey[s.Class], label: classLabel[s.Class], pts: s.Points}
		title := s.Name
		if stage, ok := strings.CutPrefix(s.Metric, sharePrefix); ok {
			id, title = sharePrefix, "Where the time goes, share of open to merge"
			for n, d := range metrics.Stages {
				if d.ID == stage {
					m.key, m.label = fmt.Sprintf("s%d", n+1), d.Name
				}
			}
		}
		f := byMetric[id]
		if f == nil {
			f = &figure{t: t, title: title, unit: s.Unit}
			switch {
			case id == sharePrefix:
				f.kind = kindShares
				f.note = fmt.Sprintf("The constraint of a %s is its largest part. A faded column has fewer than %d merged pull requests.", one, metrics.MinSample)
			case columnMetrics[id]:
				f.kind = kindColumns
			}
			byMetric[id] = f
			figs = append(figs, f)
		}
		f.marks = append(f.marks, m)
	}
	var out []template.HTML
	for _, f := range figs {
		out = append(out, f.render())
	}
	return out
}

func esc(s string) string { return html.EscapeString(s) }

// ticks picks round axis values that cover top. Durations are rounded in the
// unit a person would read them in.
func ticks(top float64, unit string) []float64 {
	scale := 1.0
	switch {
	case unit == metrics.UnitShare:
		scale = 0.01
	case unit == metrics.UnitHours && top >= 48:
		scale = 24
	case unit == metrics.UnitHours && top < 1, unit == metrics.UnitMinutes && top < 1:
		scale = 1.0 / 60
	case unit == metrics.UnitMinutes && top >= 120:
		scale = 60
	}
	m := top / scale
	if m <= 0 {
		m = 1
	}
	pow := math.Pow(10, math.Floor(math.Log10(m)))
	step := pow * 10
	for _, f := range []float64{0.2, 0.5, 1, 2, 5} {
		if m/(f*pow) <= 4 {
			step = f * pow
			break
		}
	}
	whole := unit == metrics.UnitCount || unit == metrics.UnitLines || unit == metrics.UnitShare
	if whole && step < 1 {
		step = 1
	}
	var out []float64
	for v := 0.0; ; v += step {
		out = append(out, v*scale)
		if v >= m-1e-9 || (unit == metrics.UnitShare && v >= 100) {
			break
		}
	}
	return out
}

func tickLabel(v float64, unit string) string {
	if v == 0 {
		return "0"
	}
	switch unit {
	case metrics.UnitHours:
		return strings.Replace(metrics.Hours(v), " days", " d", 1)
	case metrics.UnitMinutes:
		return metrics.Minutes(v)
	case metrics.UnitShare:
		return fmt.Sprintf("%.0f%%", v*100)
	}
	return num(math.Round(v*10) / 10)
}

// tipValue prints one value for the hover tip.
func tipValue(p metrics.Point, unit string) string {
	s := metrics.Value(p, unit)
	if unit == metrics.UnitPerWeek || unit == metrics.UnitCount {
		return s
	}
	if p.Thin() {
		return fmt.Sprintf("%s (n %d, few)", s, p.N)
	}
	return fmt.Sprintf("%s (n %d)", s, p.N)
}

// roundTop is a column part with its two top corners rounded.
func roundTop(x, y, w, h float64) string {
	r := math.Min(4, math.Min(w/2, h))
	return fmt.Sprintf(`<path class="fl" d="M%.1f %.1fV%.1fQ%.1f %.1f %.1f %.1fH%.1fQ%.1f %.1f %.1f %.1fV%.1fZ"/>`,
		x, y+h, y+r, x, y, x+r, y, x+w-r, x+w, y, x+w, y+r, y+h)
}

func (f *figure) render() template.HTML {
	wide := f.kind == kindShares
	W, H := 320.0, 170.0
	maxLabels := 5
	if wide {
		W, H, maxLabels = 640, 210, 9
	}
	const pl, pr, pt, pb = 44.0, 14.0, 8.0, 22.0
	pw, ph := W-pl-pr, H-pt-pb
	periods := f.t.Periods
	n := len(periods)
	band := pw / float64(n)
	cx := func(i int) float64 { return pl + (float64(i)+0.5)*band }

	// The top of the axis: the highest value, or the tallest stack.
	top := 0.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for _, m := range f.marks {
			if v := m.pts[i].Value; v != nil {
				sum += *v
				if f.kind == kindLine {
					top = math.Max(top, *v)
				}
			}
		}
		if f.kind != kindLine {
			top = math.Max(top, sum)
		}
	}
	if f.kind == kindShares {
		top = 1
	}
	tk := ticks(top, f.unit)
	top = tk[len(tk)-1]
	y := func(v float64) float64 { return pt + ph - v/top*ph }

	var b strings.Builder
	one, _ := f.t.PeriodWord()
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" role="img" aria-label="%s, by %s. The table below has the numbers.">`, W, H, esc(f.title), one)
	for i, v := range tk {
		class := "gl"
		if i == 0 {
			class = "ax"
		}
		fmt.Fprintf(&b, `<line class="%s" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/><text x="%.1f" y="%.1f" text-anchor="end">%s</text>`,
			class, pl, W-pr, y(v), y(v), pl-6, y(v)+3.5, esc(tickLabel(v, f.unit)))
	}
	every := int(math.Ceil(float64(n) / float64(maxLabels)))
	for i, p := range periods {
		if (n-1-i)%every == 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle">%s</text>`, cx(i), H-6, p.From.Format("Jan 2"))
		}
	}

	if f.kind == kindLine {
		for _, m := range f.marks {
			fmt.Fprintf(&b, `<g class="k-%s">`, m.key)
			var d strings.Builder
			last := -1
			for i, p := range m.pts {
				if p.Value == nil {
					continue
				}
				cmd := "L"
				if i == 0 || m.pts[i-1].Value == nil {
					cmd = "M"
				}
				fmt.Fprintf(&d, "%s%.1f %.1f", cmd, cx(i), y(*p.Value))
				last = i
			}
			if d.Len() > 0 {
				fmt.Fprintf(&b, `<path class="ln" d="%s"/>`, d.String())
			}
			for i, p := range m.pts {
				if p.Value == nil {
					continue
				}
				alone := (i == 0 || m.pts[i-1].Value == nil) && (i == n-1 || m.pts[i+1].Value == nil)
				switch {
				case p.Thin() && i == last:
					fmt.Fprintf(&b, `<circle class="thin" cx="%.1f" cy="%.1f" r="4"/>`, cx(i), y(*p.Value))
				case p.Thin():
					fmt.Fprintf(&b, `<circle class="thin" cx="%.1f" cy="%.1f" r="3"/>`, cx(i), y(*p.Value))
				case i == last:
					fmt.Fprintf(&b, `<circle class="dot" cx="%.1f" cy="%.1f" r="4"/>`, cx(i), y(*p.Value))
				case alone:
					fmt.Fprintf(&b, `<circle class="dot" cx="%.1f" cy="%.1f" r="3"/>`, cx(i), y(*p.Value))
				}
			}
			b.WriteString(`</g>`)
		}
	} else {
		bw := math.Max(2, math.Min(24, band*0.72))
		for i := 0; i < n; i++ {
			topMark := -1
			for j, m := range f.marks {
				if v := m.pts[i].Value; v != nil && *v > 0 {
					topMark = j
				}
			}
			if topMark < 0 {
				continue
			}
			if f.kind == kindShares && periods[i].Line.Merged < metrics.MinSample {
				b.WriteString(`<g class="few">`)
			} else {
				b.WriteString(`<g>`)
			}
			base := 0.0
			for j, m := range f.marks {
				v := m.pts[i].Value
				if v == nil || *v <= 0 {
					continue
				}
				y0, y1 := y(base+*v), y(base)
				base += *v
				// A 2px gap of surface keeps one part apart from the next.
				if base > *v && y1-y0 > 4 {
					y1 -= 2
				}
				h := math.Max(y1-y0, 1)
				fmt.Fprintf(&b, `<g class="k-%s">`, m.key)
				if j == topMark {
					b.WriteString(roundTop(cx(i)-bw/2, y0, bw, h))
				} else {
					fmt.Fprintf(&b, `<rect class="fl" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, cx(i)-bw/2, y0, bw, h)
				}
				b.WriteString(`</g>`)
			}
			b.WriteString(`</g>`)
		}
	}

	// One invisible column per period carries the hover tip.
	for i, p := range periods {
		type row struct{ key, text string }
		rows := []row{}
		for _, m := range f.marks {
			if m.pts[i].Value == nil {
				continue
			}
			text := tipValue(m.pts[i], f.unit)
			if len(f.marks) > 1 {
				text = m.label + "  " + text
			}
			rows = append(rows, row{m.key, text})
		}
		if len(rows) == 0 {
			rows = append(rows, row{"", "no value"})
		}
		head := p.From.Format("Jan 2") + " to " + p.To.Format("Jan 2")
		chars := len(head)
		for _, r := range rows {
			chars = max(chars, len(r.text)+2)
		}
		tw, th := 18+float64(chars)*5.9, 10+15*float64(len(rows)+1)
		tx := cx(i) + 8
		if tx+tw > W-2 {
			tx = cx(i) - 8 - tw
		}
		tx = math.Max(tx, 2)
		fmt.Fprintf(&b, `<g class="hit"><rect class="hot" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, cx(i)-band/2, pt, band, ph)
		if f.kind == kindLine {
			fmt.Fprintf(&b, `<line class="cross" x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f"/>`, cx(i), cx(i), pt, pt+ph)
		} else {
			fmt.Fprintf(&b, `<rect class="cross band" x="%.1f" y="%.1f" width="%.1f" height="%.1f"/>`, cx(i)-band/2, pt, band, ph)
		}
		fmt.Fprintf(&b, `<g class="tip" transform="translate(%.1f %.1f)"><rect width="%.1f" height="%.1f" rx="4"/><text class="th" x="8" y="15">%s</text>`, tx, pt+2, tw, th, esc(head))
		for j, r := range rows {
			ty := 15 + 15*float64(j+1)
			if r.key == "" {
				fmt.Fprintf(&b, `<text x="8" y="%.1f">%s</text>`, ty, esc(r.text))
				continue
			}
			fmt.Fprintf(&b, `<circle class="k-%s fl" cx="12" cy="%.1f" r="3.5"/><text x="21" y="%.1f">%s</text>`, r.key, ty-3.5, ty, esc(r.text))
		}
		b.WriteString(`</g></g>`)
	}
	b.WriteString(`</svg>`)

	// The caption names the chart and doubles as the legend: a color dot, the
	// series and its latest value.
	var c strings.Builder
	class := "card"
	if wide {
		class = "card wide"
	}
	fmt.Fprintf(&c, `<figure class="%s"><figcaption>%s<span class="keys">`, class, esc(f.title))
	for _, m := range f.marks {
		latest := m.pts[n-1]
		label := m.label
		if len(f.marks) == 1 {
			label = "Latest " + one
		}
		fmt.Fprintf(&c, `<span><i class="k-%s"></i>%s <b>%s</b></span>`, m.key, esc(label), esc(metrics.Value(latest, f.unit)))
	}
	c.WriteString(`</span></figcaption>`)
	if wide {
		c.WriteString(`<div class="scroll">` + b.String() + `</div>`)
	} else {
		c.WriteString(b.String())
	}
	if f.note != "" {
		fmt.Fprintf(&c, `<p class="note">%s</p>`, esc(f.note))
	}
	c.WriteString(`</figure>`)
	return template.HTML(c.String())
}

// trendCSS adds the charts to the base style. Series colors follow the thing
// they stand for, in a fixed order, and have their own dark values.
const trendCSS = `
:root{--grid:#e9e7df;--c-agent:#2a78d6;--c-human:#eb6834;--c-auto:#9a978f;--c-all:#5d6168;--c-s1:#1baf7a;--c-s2:#eda100;--c-s3:#e87ba4;--c-s4:#008300;--c-s5:#6250d6;--c-s6:#e34948}
@media (prefers-color-scheme:dark){:root{--grid:#292c31;--c-agent:#3987e5;--c-human:#d95926;--c-auto:#7d8087;--c-all:#a0a4ab;--c-s1:#199e70;--c-s2:#c98500;--c-s3:#d55181;--c-s4:#008300;--c-s5:#9085e9;--c-s6:#e66767}}
.k-agent{--k:var(--c-agent)}.k-human{--k:var(--c-human)}.k-auto{--k:var(--c-auto)}.k-all{--k:var(--c-all)}
.k-s1{--k:var(--c-s1)}.k-s2{--k:var(--c-s2)}.k-s3{--k:var(--c-s3)}.k-s4{--k:var(--c-s4)}.k-s5{--k:var(--c-s5)}.k-s6{--k:var(--c-s6)}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px}
figure{margin:0;border:1px solid var(--line);border-radius:8px;padding:12px 12px 8px;min-width:0}
figure.wide{grid-column:1/-1}
figcaption{font-size:14px;font-weight:600;margin-bottom:8px;line-height:1.35}
.keys{display:flex;flex-wrap:wrap;gap:2px 14px;font-size:13px;font-weight:400;color:var(--mute);margin-top:3px}
.keys b{color:var(--ink);font-weight:600;font-variant-numeric:tabular-nums}
.keys i{display:inline-block;width:9px;height:9px;border-radius:50%;margin-right:5px;background:var(--k)}
svg{display:block;width:100%;height:auto}
.wide svg{min-width:560px}
svg text{font-size:10px;fill:var(--mute);font-variant-numeric:tabular-nums}
.gl{stroke:var(--grid)}.ax{stroke:var(--bar)}
.ln{fill:none;stroke:var(--k);stroke-width:2;stroke-linejoin:round;stroke-linecap:round}
.fl{fill:var(--k)}
.dot{fill:var(--k);stroke:var(--card);stroke-width:2}
.thin{fill:var(--card);stroke:var(--k);stroke-width:1.5}
.few{opacity:.4}
.hot{fill:transparent}
.cross{stroke:var(--mute);stroke-width:1}
.cross.band{stroke:none;fill:var(--ink);opacity:.07}
.tip,.cross{pointer-events:none}
.hit .cross,.hit .tip{visibility:hidden}
.hit:hover .cross,.hit:hover .tip{visibility:visible}
.tip rect{fill:var(--card);stroke:var(--line)}
.tip text{font-size:11px;fill:var(--ink)}
.tip text.th{fill:var(--mute)}
details{margin-top:14px}
summary{cursor:pointer;color:var(--mute);font-size:14px}
details .scroll{margin-top:10px}
details td,details th{font-size:13px;padding:6px 8px}
.periods td,.periods th{font-size:13px;padding:6px 8px}
.periods thead th{white-space:normal;vertical-align:bottom}
.periods td:first-child{white-space:nowrap}
.periods td:nth-child(9),.periods th:nth-child(9){white-space:normal;text-align:left;min-width:150px}
`

var trendPage = template.Must(template.New("trend").Funcs(template.FuncMap{
	"css": func() template.CSS { return template.CSS(baseCSS + trendCSS) },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="generator" content="factorymeter {{.T.Version}}">
<title>factorymeter trend: {{.Title}}</title>
<style>
{{css}}
</style>
</head>
<body>
<main>
<header>
<p>factorymeter trend &middot; {{.Title}} &middot; {{.Window}}</p>
<h1>{{.T.Headline}}</h1>
</header>

<section>
<h2>What changed</h2>
<ul>{{range .T.Findings}}<li>{{.}}</li>{{end}}</ul>
</section>

{{range .Groups}}<section>
<h2>{{.Name}}</h2>
<div class="grid">{{range .Charts}}{{.}}{{end}}</div>
<details><summary>Table: every number in this group</summary>{{template "table" .Full}}</details>
</section>
{{end}}
<section class="periods">
<h2>{{.ByPeriod.Title}}</h2>
{{template "table" .ByPeriod}}
</section>

<section>
<h2>Limits</h2>
<ul>{{range .T.Limits}}<li>{{.}}</li>{{end}}{{range .T.Notes}}<li>{{.}}</li>{{end}}</ul>
</section>

<footer>Made with <a href="https://github.com/marshal-org/factorymeter">factorymeter</a> {{.T.Version}}, signature registry version {{.T.RegistryVersion}}, on {{.T.GeneratedAt.Format "2006-01-02"}}. It read GitHub with a read-only token and sent nothing anywhere else.</footer>
</main>
</body>
</html>
{{define "table"}}<div class="scroll"><table><thead><tr>{{range .Header}}<th>{{.}}</th>{{end}}</tr></thead><tbody>{{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}</tbody></table></div>{{if .Note}}<p class="note">{{.Note}}</p>{{end}}{{end}}`))

// TrendHTML writes the one-page trend.
func TrendHTML(w io.Writer, t *metrics.Trend) error {
	return trendPage.Execute(w, NewTrendView(t))
}
