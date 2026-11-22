package view

import (
	"html"
	"html/template"
	"math"
	"strconv"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

// svg writes a drawing. Every attribute value and every text goes through
// the escaper, so nothing a report carries can break out of the drawing.
type svg struct{ strings.Builder }

// open writes a start tag. kv is attribute names and values in turn.
func (s *svg) open(name string, kv ...string) {
	s.WriteString("<" + name)
	for i := 0; i+1 < len(kv); i += 2 {
		// An address that is not there is left out, not written empty.
		if kv[i] == "data-drill" && kv[i+1] == "" {
			continue
		}
		s.WriteString(" " + kv[i] + `="` + html.EscapeString(kv[i+1]) + `"`)
	}
	s.WriteString(">")
}

// leaf writes an element with nothing inside.
func (s *svg) leaf(name string, kv ...string) {
	s.open(name, kv...)
	s.close(name)
}

func (s *svg) close(name string) { s.WriteString("</" + name + ">") }
func (s *svg) text(t string)     { s.WriteString(html.EscapeString(t)) }

// px prints a coordinate with one decimal at most.
func px(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }

// The size of a chart and the room around its plot, in the units of its
// viewBox. The bottom band holds the dates, so the drawing never needs a
// height it does not carry.
const (
	chartW, chartH = 280.0, 124.0
	padSide        = 4.0
	padTop         = 16.0
	padBottom      = 19.0
)

// track is one series of a chart.
type track struct {
	// name is shown beside the value. Empty on a chart of one series.
	name string
	// key is the class that colours the series: s1, s2 or s3.
	key string
	s   *contract.Series
}

// chart draws the weekly history of one metric: every week the report
// carries, with the weeks before the window dimmed.
type chart struct {
	r      *contract.Report
	m      line.Metric
	tracks []track
	// stack sets the tracks on top of each other. Only counts stack.
	stack bool
	label string
	// drill is the address of the items of a week, without the week's
	// index at its end. Empty draws a chart that cannot be clicked.
	drill string
}

// windowStart is the index of the first week of the window among the
// periods of the report.
func windowStart(r *contract.Report) int {
	for i, p := range r.Window.Periods {
		if !p.From.Before(r.Window.Since) {
			return i
		}
	}
	return len(r.Window.Periods)
}

// barred says whether a metric is drawn as bars: a count of a week, or the
// hours of a week added up. A median or a share is a line.
func barred(m line.Metric) bool {
	return m.Unit == line.PerWeek || m.Unit == line.Count || strings.HasSuffix(m.ID, ".held.total")
}

// ceiling rounds the largest value up to a number that reads well at the top
// of the scale. Hours round in minutes, hours or days, as they are printed.
func ceiling(unit line.Unit, v float64) float64 {
	nice := func(v float64) float64 {
		exp := math.Pow(10, math.Floor(math.Log10(v)))
		for _, m := range []float64{1, 2, 2.5, 5, 10} {
			if v <= m*exp*(1+1e-9) {
				return m * exp
			}
		}
		return 10 * exp
	}
	switch {
	case v <= 0:
		return 1
	case unit == line.Share:
		return math.Min(nice(v), 1)
	case unit == line.Hours && v >= 48:
		return nice(v/24) * 24
	case unit == line.Hours && v < 1:
		return nice(v*60) / 60
	}
	return nice(v)
}

func (c chart) thin(p contract.Point) bool { return c.m.MinSample > 0 && p.N < c.m.MinSample }

func class(key string, pre bool) string {
	if pre {
		return key + " pre"
	}
	return key
}

// bar is the outline of a bar that stands on the baseline: square below,
// rounded at the end that shows the value.
func bar(x, y, w, h, r float64) string {
	r = math.Max(math.Min(r, math.Min(w/2, h)), 0)
	return "M" + px(x) + " " + px(y+h) + "V" + px(y+r) + "Q" + px(x) + " " + px(y) + " " + px(x+r) + " " + px(y) +
		"H" + px(x+w-r) + "Q" + px(x+w) + " " + px(y) + " " + px(x+w) + " " + px(y+r) + "V" + px(y+h) + "Z"
}

func (c chart) draw() template.HTML {
	n := len(c.r.Window.Periods)
	some := false
	for _, t := range c.tracks {
		n = min(n, len(t.s.Points))
	}
	for _, t := range c.tracks {
		for _, p := range t.s.Points[:n] {
			some = some || p.Value != nil
		}
	}
	// A chart of nothing is left out: an empty frame reads as a week of zero.
	if n == 0 || !some {
		return ""
	}
	first := min(windowStart(c.r), n)
	bars := barred(c.m)
	top := 0.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for _, t := range c.tracks {
			if v := t.s.Points[i].Value; v != nil {
				if c.stack {
					sum += *v
				} else {
					sum = math.Max(sum, *v)
				}
			}
		}
		top = math.Max(top, sum)
	}
	top = ceiling(c.m.Unit, top)
	plotH := chartH - padTop - padBottom
	y0 := chartH - padBottom
	step := (chartW - 2*padSide) / float64(n)
	cx := func(i int) float64 { return padSide + step*(float64(i)+0.5) }
	cy := func(v float64) float64 { return y0 - math.Min(v/top, 1)*plotH }

	// What the pointer shows. The script finds the week under the pointer
	// from the geometry, so the whole plot is the target and no week needs a
	// mark of its own. Without the script the table of the station says the
	// same numbers.
	var names, keys, rows []string
	for _, t := range c.tracks {
		names, keys = append(names, t.name), append(keys, t.key)
		var cells []string
		for _, p := range t.s.Points[:n] {
			switch {
			case p.Value == nil:
				cells = append(cells, "")
			case bars:
				cells = append(cells, value(t.s, p.Value))
			default:
				cells = append(cells, value(t.s, p.Value)+";"+strconv.Itoa(p.N))
			}
		}
		rows = append(rows, strings.Join(cells, "|"))
	}
	s := &svg{}
	s.open("svg", "class", "chart", "viewBox", "0 0 "+px(chartW)+" "+px(chartH), "role", "img", "aria-label", c.label,
		"data-names", strings.Join(names, "|"), "data-keys", strings.Join(keys, "|"), "data-v", strings.Join(rows, "~"),
		"data-x", px(padSide), "data-step", strconv.FormatFloat(step, 'f', 4, 64), "data-y", px(padTop-4), "data-h", px(plotH+4),
		"data-drill", c.drill)

	// The furniture: two hairlines for the scale, the baseline, and a mark
	// where the window starts.
	for _, v := range []float64{top, top / 2} {
		s.leaf("line", "class", "grid", "x1", px(padSide), "x2", px(chartW-padSide), "y1", px(cy(v)), "y2", px(cy(v)))
	}
	s.leaf("line", "class", "axis", "x1", px(padSide), "x2", px(chartW-padSide), "y1", px(y0), "y2", px(y0))
	marked := first > 0 && first < n
	if marked {
		x := padSide + step*float64(first)
		s.leaf("line", "class", "from", "x1", px(x), "x2", px(x), "y1", px(padTop), "y2", px(y0+4))
	}

	if bars {
		bw := math.Max(math.Min(step-2, 24), 1.5)
		for i := 0; i < n; i++ {
			last := -1
			for ti, t := range c.tracks {
				if v := t.s.Points[i].Value; v != nil && *v > 0 {
					last = ti
				}
			}
			base := y0
			for ti, t := range c.tracks {
				v := t.s.Points[i].Value
				if v == nil || *v <= 0 {
					continue
				}
				h := math.Max(math.Min(*v/top, 1)*plotH, 1.5)
				// A gap of the surface keeps two fills apart, where there is room.
				gap := 0.0
				if base < y0 && h > 4 {
					gap = 2
				}
				r := 0.0
				if ti == last {
					r = 4
				}
				s.leaf("path", "class", class(t.key, i < first), "d", bar(cx(i)-bw/2, base-h, bw, h-gap, r))
				base -= h
				if !c.stack {
					break
				}
			}
		}
	} else {
		for _, t := range c.tracks {
			// A line joins two weeks only when both stand on enough items. A
			// week on too few is a faint dot, and a week with nothing is a gap.
			solid := func(i int) bool {
				return i >= 0 && i < n && t.s.Points[i].Value != nil && !c.thin(t.s.Points[i])
			}
			var before, in strings.Builder
			end := -1
			for i := 0; i < n; i++ {
				p := t.s.Points[i]
				if p.Value == nil {
					continue
				}
				x, y := px(cx(i)), px(cy(*p.Value))
				if !solid(i) {
					s.leaf("circle", "class", class(t.key+" few", i < first), "cx", x, "cy", y, "r", "2.2")
					continue
				}
				end = i
				if solid(i + 1) {
					d := &in
					if i+1 < first {
						d = &before
					}
					d.WriteString("M" + x + " " + y + "L" + px(cx(i+1)) + " " + px(cy(*t.s.Points[i+1].Value)))
				} else if !solid(i - 1) {
					s.leaf("circle", "class", class(t.key, i < first), "cx", x, "cy", y, "r", "2.2")
				}
			}
			if before.Len() > 0 {
				s.leaf("path", "class", "ln "+t.key+" pre", "d", before.String())
			}
			if in.Len() > 0 {
				s.leaf("path", "class", "ln "+t.key, "d", in.String())
			}
			// The last week that counts carries the one marker of the series.
			if end >= first {
				s.leaf("circle", "class", "end "+t.key, "cx", px(cx(end)), "cy", px(cy(*t.s.Points[end].Value)), "r", "4")
			}
		}
	}

	// The scale and the dates. The value at the top is the only one printed:
	// the rest is in the tooltip and in the table of the station.
	s.open("text", "class", "tick", "x", px(padSide), "y", px(padTop-5))
	s.text(strings.Replace(value(c.tracks[0].s, &top), ".0", "", 1))
	s.close("text")
	date := func(x float64, anchor, t string) {
		s.open("text", "class", "tick", "x", px(x), "y", px(chartH-5), "text-anchor", anchor)
		s.text(t)
		s.close("text")
	}
	const short = "Jan 2"
	if !marked || first >= 6 {
		date(padSide, "start", c.r.Window.Periods[0].From.Format(short))
	}
	if marked && n-first >= 6 {
		date(padSide+step*float64(first), "middle", c.r.Window.Periods[first].From.Format(short))
	}
	date(chartW-padSide, "end", c.r.Window.Periods[n-1].To.Format(short))

	s.close("svg")
	return template.HTML(s.String())
}

// spark is the history of a series with no scale and no dates, for a place
// that prints the value beside it.
func spark(r *contract.Report, m line.Metric, sr *contract.Series) template.HTML {
	if sr == nil {
		return ""
	}
	n := min(len(r.Window.Periods), len(sr.Points))
	if n == 0 {
		return ""
	}
	const w, h, pad = 120.0, 30.0, 3.0
	first := min(windowStart(r), n)
	top := 0.0
	for _, p := range sr.Points[:n] {
		if p.Value != nil {
			top = math.Max(top, *p.Value)
		}
	}
	if top <= 0 {
		top = 1
	}
	step := w / float64(n)
	cx := func(i int) float64 { return step * (float64(i) + 0.5) }
	cy := func(v float64) float64 { return h - pad - v/top*(h-2*pad) }
	s := &svg{}
	s.open("svg", "class", "spark", "viewBox", "0 0 "+px(w)+" "+px(h), "aria-hidden", "true")
	s.leaf("line", "class", "axis", "x1", "0", "x2", px(w), "y1", px(h-pad), "y2", px(h-pad))
	if barred(m) {
		bw := math.Max(step-1.5, 1.5)
		for i, p := range sr.Points[:n] {
			if p.Value != nil && *p.Value > 0 {
				bh := math.Max(*p.Value/top*(h-2*pad), 1)
				s.leaf("path", "class", class("s1", i < first), "d", bar(cx(i)-bw/2, h-pad-bh, bw, bh, 1.5))
			}
		}
	} else {
		var before, in strings.Builder
		prev, end := -1, -1
		for i, p := range sr.Points[:n] {
			if p.Value == nil {
				prev = -1
				continue
			}
			if prev >= 0 {
				d := &in
				if i < first {
					d = &before
				}
				d.WriteString("M" + px(cx(prev)) + " " + px(cy(*sr.Points[prev].Value)) + "L" + px(cx(i)) + " " + px(cy(*p.Value)))
			}
			prev, end = i, i
		}
		if before.Len() > 0 {
			s.leaf("path", "class", "ln s1 pre", "d", before.String())
		}
		if in.Len() > 0 {
			s.leaf("path", "class", "ln s1", "d", in.String())
		}
		if end >= 0 {
			s.leaf("circle", "class", class("s1", end < first), "cx", px(cx(end)), "cy", px(cy(*sr.Points[end].Value)), "r", "2.5")
		}
	}
	s.close("svg")
	return template.HTML(s.String())
}

// meter draws a share of a whole as a filled track.
func meter(share float64) template.HTML {
	share = math.Max(math.Min(share, 1), 0)
	s := &svg{}
	s.open("svg", "class", "meter", "viewBox", "0 0 100 6", "preserveAspectRatio", "none", "aria-hidden", "true")
	s.leaf("rect", "class", "track", "width", "100", "height", "6")
	s.leaf("rect", "class", "fill", "width", px(share*100), "height", "6")
	s.close("svg")
	return template.HTML(s.String())
}
