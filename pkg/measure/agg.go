// Package measure computes the numbers of the metric registry from records.
//
// It works in two steps. A Builder reads records and turns each ticket,
// change, CI round and deploy into observations: "this change got its first
// review at this time, after waiting this long". Report then adds the
// observations up per period and per slice. A metric id names both the stream
// of observations it reads and how they are added up, so a new metric needs a
// line that emits observations and nothing else.
//
// Nothing here talks to the network.
package measure

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/line"
)

// Classes an observation can belong to. classNone is for tickets and deploys,
// which nobody "wrote" in the sense a change is written.
const (
	classAgent int8 = iota
	classHuman
	classAutomation
	classNone
)

var classNames = []string{"agent", "human", "automation", ""}

// key says which slices an observation counts in.
type key struct {
	class int8
	agent int16 // index into Line.agents, -1 for none
	repo  int32 // index into Line.repos, -1 for none
}

// obs is one thing that happened at a station: an item left it, a wait ended.
type obs struct {
	at time.Time
	// v is hours or lines. For a share it is 1 when the item counts as a hit
	// and 0 when it does not.
	v float64
	k key
	// item is the change, ticket or deploy it is about: an index into
	// Line.items, or -1.
	item int32
	// mode says in which review mode the observation exists.
	mode uint8
}

// The review modes an observation can exist in. Most exist in both. A change
// with a review by an agent reads differently when only reviews by people
// count, so the stations that depend on it are written once for each mode.
const (
	modeBoth  uint8 = iota // in both
	modeAll                // only when every review counts
	modeHuman              // only when reviews by people count
)

// seenIn says whether something of a mode is read when reviews by people only
// count or when all do.
func seenIn(mode uint8, humanOnly bool) bool {
	return mode == modeBoth || (mode == modeHuman) == humanOnly
}

// stay is one item standing in a queue. A zero to means it still stands.
type stay struct {
	from, to time.Time
	k        key
	item     int32 // index into Line.items
	mode     uint8
}

// How a metric adds its observations up.
const (
	howPerWeek = iota
	howQuantile
	howShare
	howCount
	howOldest
	howHeldTotal
	howHeldShare
)

// calc is the stream a metric reads and how it adds it up.
type calc struct {
	stream string
	how    int
	q      float64
}

// calcOf reads the stream and the arithmetic out of a metric. The stream is
// the id without its statistic: review.wait.p50 reads review.wait.
func calcOf(m line.Metric) (calc, bool) {
	parts := strings.Split(m.ID, ".")
	if len(parts) < 2 {
		return calc{}, false
	}
	c := calc{stream: parts[0] + "." + parts[1]}
	stat := ""
	if len(parts) == 3 {
		stat = parts[2]
	}
	switch {
	case stat == "p50":
		c.how, c.q = howQuantile, 0.5
	case stat == "p90":
		c.how, c.q = howQuantile, 0.9
	case stat == "oldest":
		c.how = howOldest
	case m.Kind == line.Held && stat == "total":
		c.how = howHeldTotal
	case m.Kind == line.Held && stat == "share":
		c.how = howHeldShare
	case stat != "":
		return calc{}, false
	case m.Unit == line.PerWeek:
		c.how = howPerWeek
	case m.Unit == line.Share:
		c.how = howShare
	case m.Unit == line.Count:
		c.how = howCount
	default:
		return calc{}, false
	}
	return c, true
}

// quantile interpolates linearly between the two nearest ranks of a sorted
// list. The median of an even count is the mean of the two middle values.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// between returns the observations with from <= at < to. list is sorted by
// time.
func between(list []obs, from, to time.Time) []obs {
	lo := sort.Search(len(list), func(i int) bool { return !list[i].at.Before(from) })
	hi := sort.Search(len(list), func(i int) bool { return !list[i].at.Before(to) })
	return list[lo:hi]
}

func values(list []obs) []float64 {
	out := make([]float64, len(list))
	for i := range list {
		out[i] = list[i].v
	}
	sort.Float64s(out)
	return out
}

func weeks(from, to time.Time) float64 {
	return to.Sub(from).Hours() / (7 * 24)
}

func hours(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return d.Hours()
}

func ptr(v float64) *float64 { return &v }

// round4 keeps four decimals, so a value prints the same on every machine
// and can be checked by hand.
func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// perWeek is the count of the observations divided by the weeks.
func perWeek(list []obs, w float64) (*float64, int) {
	if w <= 0 {
		return nil, 0
	}
	return ptr(float64(len(list)) / w), len(list)
}

func quantileOf(list []obs, q float64) (*float64, int) {
	if len(list) == 0 {
		return nil, 0
	}
	return ptr(quantile(values(list), q)), len(list)
}

// shareOf is the share of the observations that are a hit.
func shareOf(list []obs) (*float64, int) {
	if len(list) == 0 {
		return nil, 0
	}
	hit := 0.0
	for i := range list {
		hit += list[i].v
	}
	return ptr(hit / float64(len(list))), len(list)
}

// heldTotal adds the hours up, each item cut off at the 90th percentile of
// the same items, and divides by the weeks.
func heldTotal(list []obs, w float64) (*float64, int) {
	if len(list) == 0 || w <= 0 {
		return nil, 0
	}
	v := values(list)
	top, sum := quantile(v, 0.9), 0.0
	for _, x := range v {
		sum += math.Min(x, top)
	}
	return ptr(sum / w), len(list)
}

// standing returns the items in a queue at a moment, the one that has stood
// longest first.
func standing(list []stay, at time.Time) []stay {
	var out []stay
	for i := range list {
		s := &list[i]
		if !s.from.After(at) && (s.to.IsZero() || s.to.After(at)) {
			out = append(out, *s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].from.Before(out[j].from) })
	return out
}

// spans are stretches of time, used for the time a CI run was in flight.
type span struct{ from, to time.Time }

type spans []span

// merge sorts the spans and joins the ones that touch or overlap.
func (s spans) merge() spans {
	if len(s) < 2 {
		return s
	}
	sort.Slice(s, func(i, j int) bool { return s[i].from.Before(s[j].from) })
	out := spans{s[0]}
	for _, x := range s[1:] {
		last := &out[len(out)-1]
		if x.from.After(last.to) {
			out = append(out, x)
		} else if x.to.After(last.to) {
			last.to = x.to
		}
	}
	return out
}

// within is the hours of merged spans that fall between from and to.
func (s spans) within(from, to time.Time) float64 {
	total := 0.0
	for _, x := range s {
		a, b := x.from, x.to
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if b.After(a) {
			total += b.Sub(a).Hours()
		}
	}
	return total
}

// total is the hours of merged spans.
func (s spans) total() float64 {
	t := 0.0
	for _, x := range s {
		t += x.to.Sub(x.from).Hours()
	}
	return t
}
