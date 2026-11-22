package measure

import (
	"errors"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/contract"
)

// The items a drill lists are the ones the number of the report was drawn
// from: as many as its N, adding up to its value.
func TestDrillListsWhatTheNumberCounted(t *testing.T) {
	l := scenario(t)
	o := Options{Weeks: 2, Until: end, Now: end}
	r, err := l.Report(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"merge.flow", "review.wait.p50", "line.agent_share", "review.queue", "merge.held.total"} {
		d, err := l.Drill(o, id, -1)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		s := find(t, r, id, contract.Slice{})
		if num(d.Value) != num(s.Value) || d.N != s.N || len(d.Items)+d.Unnamed != d.N {
			t.Errorf("%s: drill %s n=%d items=%d, report %s n=%d", id, num(d.Value), d.N, len(d.Items), num(s.Value), s.N)
		}
		for _, x := range d.Items {
			if x.Key == "" || x.Kind != "change" {
				t.Errorf("%s: an item with no name: %+v", id, x)
			}
		}
	}
	// The bot's change is automation: not on the line, so not listed.
	d, _ := l.Drill(o, "merge.flow", -1)
	for _, x := range d.Items {
		if x.Class == "automation" {
			t.Errorf("merge.flow lists automation: %+v", x)
		}
	}
	// A share lists its hits first.
	d, _ = l.Drill(o, "line.agent_share", -1)
	if !d.Share || len(d.Items) == 0 || !d.Items[0].Hit || d.Items[len(d.Items)-1].Hit {
		t.Errorf("agent share: %+v", d.Items)
	}
	// One week of a chart: the same N as that point of the series.
	s := find(t, r, "merge.flow", contract.Slice{})
	last := len(s.Points) - 1
	wk, _ := l.Drill(o, "merge.flow", last)
	if wk.N != s.Points[last].N || len(wk.Items) != s.Points[last].N {
		t.Errorf("week %d: drill n=%d items=%d, point n=%d", last, wk.N, len(wk.Items), s.Points[last].N)
	}
	if _, err := l.Drill(o, "merge.flow", 999); err == nil {
		t.Error("a week the charts do not have is not an error")
	}
	if _, err := l.Drill(o, "nope", -1); !errors.Is(err, ErrNoMetric) {
		t.Errorf("an unknown metric: %v", err)
	}
}
