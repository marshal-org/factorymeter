package line

import (
	"strings"
	"testing"
)

func TestRegistryIsValid(t *testing.T) {
	for _, err := range Validate(Registry) {
		t.Error(err)
	}
	if len(Registry) == 0 {
		t.Fatal("the registry is empty")
	}
}

func TestStationsAreTheLine(t *testing.T) {
	want := []string{Spec, Code, CI, Verify, Review, Merge, Release}
	if len(Stations) != len(want) {
		t.Fatalf("got %d stations, want %d", len(Stations), len(want))
	}
	for i, id := range want {
		s := Stations[i]
		if s.ID != id {
			t.Errorf("station %d is %q, want %q", i, s.ID, id)
		}
		for name, v := range map[string]string{"name": s.Name, "does": s.Does, "item": s.Item, "arrives": s.Arrives, "leaves": s.Leaves} {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: no %s", s.ID, name)
			}
		}
		if len(s.Needs) == 0 {
			t.Errorf("%s: needs no record kind", s.ID)
		}
	}
	if _, ok := StationByID(Whole); ok {
		t.Error("the whole line must not be a station")
	}
}

// The ids below are the contract. A test that fails here means an id was
// renamed or dropped, which breaks everyone who reads the JSON.
func TestIDsThatMustNotChange(t *testing.T) {
	for _, id := range []string{
		"spec.flow", "spec.wait.p50", "spec.queue", "spec.queue.oldest",
		"code.flow", "code.work.p50", "code.rework", "code.queue", "code.held.share",
		"ci.flow", "ci.wait.p50", "ci.work.p50", "ci.work.p90", "ci.rework", "ci.held.total",
		"verify.flow", "verify.work.p50",
		"review.flow", "review.wait.p50", "review.wait.p90", "review.work.p50", "review.rework", "review.queue", "review.queue.oldest", "review.skipped", "review.held.share",
		"merge.flow", "merge.wait.p50", "merge.rework", "merge.queue", "merge.held.share",
		"release.flow", "release.wait.p50", "release.work.p50", "release.rework", "release.queue", "release.deploys",
		"line.flow", "line.lead.p50", "line.lead.p90", "line.delivery.p50", "line.agent_share", "line.size.p50",
	} {
		if _, ok := ByID(id); !ok {
			t.Errorf("metric %q is gone", id)
		}
	}
}

func TestStationsHaveNoNumbersTheyCannotObserve(t *testing.T) {
	for id, kind := range map[string]Kind{"spec.work.p50": Work, "spec.held.share": Held, "code.wait.p50": Wait, "ci.queue": Queue, "merge.work.p50": Work} {
		if _, ok := ByID(id); ok {
			t.Errorf("%s exists, but the station says %s cannot be observed", id, kind)
		}
	}
	if s, _ := StationByID(Spec); s.InConstraint {
		t.Error("spec must be left out of the constraint")
	}
}

func TestForStation(t *testing.T) {
	total := len(ForStation(Whole))
	for _, s := range Stations {
		n := len(ForStation(s.ID))
		if n == 0 {
			t.Errorf("%s has no metrics", s.ID)
		}
		total += n
	}
	if total != len(Registry) {
		t.Errorf("stations hold %d metrics, the registry %d", total, len(Registry))
	}
}

func TestValidateCatchesMistakes(t *testing.T) {
	good := Metric{ID: "review.wait.p50", Station: Review, Kind: Wait, Name: "x", Unit: Hours, Good: Down, Slices: []string{SliceClass}, Needs: []string{NeedChange}, Definition: "A sentence.", Since: V1}
	cases := map[string]func(m *Metric){
		"must look like":       func(m *Metric) { m.ID = "Review.Wait" },
		"match the station":    func(m *Metric) { m.Station = Merge },
		"must be the kind":     func(m *Metric) { m.Kind = Work },
		"unknown unit":         func(m *Metric) { m.Unit = "days" },
		"unknown direction":    func(m *Metric) { m.Good = "sideways" },
		"unknown slice":        func(m *Metric) { m.Slices = []string{"team"} },
		"unknown record kind":  func(m *Metric) { m.Needs = []string{"incident"} },
		"nothing could":        func(m *Metric) { m.Needs = nil },
		"ends with a full":     func(m *Metric) { m.Definition = "no stop" },
		"cannot be observed":   func(m *Metric) { m.ID, m.Station = "code.wait.p50", Code },
		"no contract version":  func(m *Metric) { m.Since = "" },
		"no name":              func(m *Metric) { m.Name = " " },
		"the minimum sample":   func(m *Metric) { m.MinSample = -1 },
		"left out of the cons": func(m *Metric) { m.ID, m.Station, m.Kind = "spec.held.total", Spec, Held },
	}
	for want, change := range cases {
		m := good
		change(&m)
		found := false
		for _, err := range Validate([]Metric{m}) {
			found = found || strings.Contains(err.Error(), want)
		}
		if !found {
			t.Errorf("no error with %q for %+v", want, m)
		}
	}
	found := false
	for _, err := range Validate([]Metric{good, good}) {
		found = found || strings.Contains(err.Error(), "used twice")
	}
	if !found {
		t.Error("a repeated id was not caught")
	}
}
