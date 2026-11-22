package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/view"
)

// drillPage says what one number counted, item by item, and how the number
// comes from them.
func drillPage(d *measure.Drill, target, back string) *view.DrillPage {
	m := d.Metric
	p := &view.DrillPage{Name: m.Name, Target: target, Definition: m.Definition, ItemHead: "Item", WhenHead: "When it counted"}
	if m.Unit == line.PerWeek {
		p.Name += " a week"
	}
	p.Station = "The whole line"
	if st, ok := line.StationByID(m.Station); ok {
		p.Station = st.Name
	}
	p.Back = back + "#station-" + m.Station
	day := func(t time.Time) string { return t.UTC().Format("Jan 2, 2006") }
	last := d.To.Add(-time.Second)
	if d.Week >= 0 {
		p.Period = "in the week " + day(d.From) + " to " + day(last)
	} else {
		p.Period = fmt.Sprintf("over the %s from %s to %s", countOf(d.Weeks, "week", "weeks"), day(d.From), day(last))
	}
	p.Value = view.Number(string(m.Unit), d.Value)
	if strings.HasSuffix(m.ID, ".held.total") && d.Value != nil {
		p.Value = view.Duration(*d.Value) + " a week"
	}
	what := "items"
	if len(d.Items) > 0 {
		what = map[string]string{record.KindChange: "PRs", record.KindTicket: "tickets", record.KindDeploy: "deploys"}[d.Items[0].Kind]
		p.ItemHead = map[string]string{record.KindChange: "PR", record.KindTicket: "Ticket", record.KindDeploy: "Deploy"}[d.Items[0].Kind]
	}
	if strings.HasPrefix(m.ID, "ci.") {
		what = "CI rounds, each named by its PR"
	}
	n := len(d.Items)
	hours := func(v float64) string { return view.Duration(v) }
	switch {
	case d.Queue:
		p.WhenHead, p.ValueHead = "In the queue since", "Standing"
		p.How = fmt.Sprintf("The %d %s standing in the queue at the end of the period, the longest standing first.", n, what)
		if m.Kind != line.Queue {
			p.How += " The number is how long the first of them had stood."
		}
	case d.Count:
		p.How = fmt.Sprintf("The %d %s below, over %s: %d divided by %d is %s a week. The newest first.",
			n, what, countOf(d.Weeks, "week", "weeks"), d.N, max(d.Weeks, 1), p.Value)
	case d.Share:
		hits := 0
		for _, x := range d.Items {
			if x.Hit {
				hits++
			}
		}
		p.ValueHead = "Counts toward it"
		p.How = fmt.Sprintf("%d of the %d %s below count toward the share: %s. They come first.", hits, n, what, p.Value)
	case m.Kind == line.Held:
		p.ValueHead = "Hours held"
		p.How = fmt.Sprintf("The hours each of the %d %s below stood at the station. Each is cut at the 90th percentile of them, added up and divided by the weeks.", n, what)
		if m.Unit == line.Share {
			p.How = fmt.Sprintf("The hours each of the %d %s below stood at the station. The number is this station's hours over the hours of every station in the constraint.", n, what)
		}
	case strings.HasSuffix(m.ID, ".p90"):
		p.ValueHead = valueHead(m)
		p.How = fmt.Sprintf("The 90th percentile of the %d %s below: one in ten took longer. The largest first.", n, what)
	default:
		p.ValueHead = valueHead(m)
		p.How = fmt.Sprintf("The median of the %d %s below: half are less. The largest first.", n, what)
	}
	if d.Unnamed > 0 {
		p.Others = fmt.Sprintf("%s more counted with no item to show.", countOf(d.Unnamed, "observation", "observations"))
	}
	for _, x := range d.Items {
		r := view.DrillRow{Key: x.Key, URL: x.URL, Title: x.Title, Who: x.Class, When: x.At.UTC().Format("2006-01-02 15:04")}
		switch {
		case d.Queue:
			r.Value = hours(x.Value)
		case d.Count:
		case d.Share:
			r.Value, r.Dim = "no", !x.Hit
			if x.Hit {
				r.Value = "yes"
			}
		case m.Unit == line.Lines:
			r.Value = fmt.Sprintf("%.0f", x.Value)
		default:
			r.Value = hours(x.Value)
		}
		p.Rows = append(p.Rows, r)
	}
	return p
}

func valueHead(m line.Metric) string {
	if m.Unit == line.Lines {
		return "Lines"
	}
	switch m.Kind {
	case line.Wait:
		return "Waited"
	case line.Work:
		return "Took"
	}
	return "Value"
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
