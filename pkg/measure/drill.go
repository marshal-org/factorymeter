package measure

import (
	"fmt"
	"sort"
	"time"

	"github.com/marshal-org/factorymeter/pkg/line"
)

// Counted is one item a number was drawn from.
type Counted struct {
	Kind, Key, URL, Title, Class string
	// At is when it counted: when it left, waited or finished. For a queue
	// it is when the item joined the queue.
	At time.Time
	// Value is what it added: hours, lines, or for a count 1. For a share
	// Hit says whether it is one of the share. For a queue Value is the
	// hours it had stood at the end of the period.
	Value float64
	Hit   bool
}

// Drill is what one number of a report is made of: the period, the number,
// and every item it counted.
type Drill struct {
	Metric   line.Metric
	From, To time.Time
	// Week is the index of the period among the weekly points, or -1 for
	// the whole window.
	Week  int
	Weeks int
	Value *float64
	N     int
	Items []Counted
	// Unnamed counts observations that point at no item.
	Unnamed int
	// Share, Queue and Count say how Value relates to the items.
	Share, Queue, Count bool
}

// ErrNoMetric is returned for a metric the registry does not have or that is
// not drawn from items.
var ErrNoMetric = fmt.Errorf("no such number")

// Drill lists the items one number of the report was drawn from: over the
// whole window when week is -1, else over one weekly period of the charts,
// counted from the first (oldest).
func (l *Line) Drill(o Options, metric string, week int) (*Drill, error) {
	m, ok := line.ByID(metric)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNoMetric, metric)
	}
	c, ok := calcOf(m)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not drawn from items", ErrNoMetric, metric)
	}
	if o.Weeks <= 0 {
		o.Weeks = 13
	}
	if o.History <= 0 {
		o.History = 26
	}
	if o.History < o.Weeks {
		o.History = o.Weeks
	}
	w, _ := l.window(o)
	d := &Drill{Metric: m, Week: week, From: w.since, To: w.until}
	if week >= 0 {
		if week >= len(w.periods) {
			return nil, fmt.Errorf("there is no week %d: the charts have %d", week, len(w.periods))
		}
		d.From, d.To = w.periods[week].From, w.periods[week].To
	}
	d.Weeks = int((d.To.Sub(d.From) + day/2) / week7)
	whole := sel{by: byWhole}
	v, n := l.value(c, whole, d.From, d.To)
	// Rounded as the report rounds it, so the two show the same number.
	d.Value, d.N = rounded(v), n
	name := func(i int32) (Counted, bool) {
		if i < 0 || int(i) >= len(l.items) {
			return Counted{}, false
		}
		it := l.items[i]
		return Counted{Kind: it.kind, Key: it.key, URL: it.url, Title: it.title, Class: classNames[it.class]}, true
	}
	switch c.how {
	case howCount, howOldest:
		d.Queue = true
		for _, s := range standing(l.staysOf(c.stream, whole), d.To) {
			x, ok := name(s.item)
			if !ok {
				d.Unnamed++
				continue
			}
			x.At, x.Value = s.from, d.To.Sub(s.from).Hours()
			d.Items = append(d.Items, x)
		}
	default:
		d.Share = c.how == howShare
		d.Count = c.how == howPerWeek
		for _, ob := range between(l.obsOf(c.stream, whole), d.From, d.To) {
			x, ok := name(ob.item)
			if !ok {
				d.Unnamed++
				continue
			}
			x.At, x.Value, x.Hit = ob.at, ob.v, ob.v != 0
			d.Items = append(d.Items, x)
		}
	}
	// The largest first: the longest waits, the hits of a share, the
	// oldest in a queue. A count lists the newest first.
	sort.SliceStable(d.Items, func(i, j int) bool {
		a, b := d.Items[i], d.Items[j]
		if d.Count {
			return a.At.After(b.At)
		}
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		return a.At.After(b.At)
	})
	return d, nil
}

const week7 = 7 * day
