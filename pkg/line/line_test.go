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

func TestRulesAreValid(t *testing.T) {
	for _, err := range ValidateRules(Rules) {
		t.Error(err)
	}
	// Every station that can be the constraint has a slow rule, so the
	// constraint always gets a recommendation.
	for _, st := range Stations {
		if _, ok := RuleByID(st.ID + ".slow"); ok != st.InConstraint {
			t.Errorf("%s: can be the constraint is %v, has a slow rule is %v", st.ID, st.InConstraint, ok)
		}
	}
	for _, r := range Rules {
		// A rule that sizes its gain says how. One that does not says nothing.
		if strings.HasSuffix(r.ID, ".slow") != (r.Sized == sizedHeld) {
			t.Errorf("%s: only the slow rules are sized by held hours", r.ID)
		}
		if r.Play != "" && r.PlayURL() != Playbook+r.Play+"/" {
			t.Errorf("%s: link %q", r.ID, r.PlayURL())
		}
	}
	if (Rule{}).PlayURL() != "" {
		t.Error("a rule with no playbook page has no link")
	}
}

// A report from last quarter names its rules by id. An id that is renamed
// breaks every trend of recommendations a reader keeps.
func TestRuleIDsThatMustNotChange(t *testing.T) {
	ids := []string{
		"code.slow", "ci.slow", "verify.slow", "review.slow", "merge.slow", "release.slow",
		"verify.missing", "ci.failing", "ci.agents-fail-more", "merge.reverted", "code.abandoned",
		"review.queue-growing", "merge.approved-waiting", "code.stale-drafts", "review.agents-wait-longer", "code.agent-changes-large",
		"spec.reopened", "spec.stale-tickets", "spec.backlog-thin",
	}
	for _, id := range ids {
		if _, ok := RuleByID(id); !ok {
			t.Errorf("rule %q is gone", id)
		}
	}
	if len(Rules) != len(ids) {
		t.Errorf("%d rules, and this test knows %d. Add the new ids here.", len(Rules), len(ids))
	}
}

// The words that say when a rule fires are built from the thresholds, so the
// two cannot drift. This pins the thresholds themselves.
func TestRuleThresholdsAreInTheWords(t *testing.T) {
	for id, want := range map[string]string{
		"review.slow":               "at least 25 percent longer than in its own best four weeks, and running at that pace would give back at least 5 percent of the hours changes are held before the merge",
		"release.slow":              "at least 5 percent of the hours changes are held on the whole line",
		"verify.missing":            "at least 50 percent of the changes merged",
		"ci.failing":                "At least 15 percent of the CI rounds",
		"ci.agents-fail-more":       "at least 5 points more often",
		"merge.reverted":            "At least 5 percent of the changes merged in the window were reverted later, and at least 3 of them",
		"code.abandoned":            "At least 20 percent of the changes closed",
		"review.queue-growing":      "At least 5 changes wait",
		"merge.approved-waiting":    "At least 3 changes have been approved for more than 24 hours",
		"code.stale-drafts":         "At least 3 changes have been a draft for more than 14 days",
		"review.agents-wait-longer": "at least 1.5 times as long for changes by agents as for changes by people, and at least 1 hour longer",
		"code.agent-changes-large":  "at least 2 times the size of the median change by a person, and at least 200 lines",
	} {
		r, _ := RuleByID(id)
		if !strings.Contains(r.Fires, want) {
			t.Errorf("%s fires: %q lacks %q", id, r.Fires, want)
		}
	}
}

func TestValidateRulesCatchesMistakes(t *testing.T) {
	good := Rule{ID: "review.too-slow", Station: Review, Title: "Do a thing", Fires: "When it is slow.", Reads: []string{"review.wait.p50"}, Sized: "By hours.", Play: "find-the-constraint", Since: V1}
	if errs := ValidateRules([]Rule{good}); len(errs) != 0 {
		t.Fatalf("the good rule is not valid: %v", errs)
	}
	cases := map[string]func(r *Rule){
		"must look like station.short-name": func(r *Rule) { r.ID = "Review.TooSlow" },
		"must start with the id of":         func(r *Rule) { r.Station = Merge },
		"start with the id of the station":  func(r *Rule) { r.ID, r.Station = "nowhere.slow", "nowhere" },
		"one line with no full stop":        func(r *Rule) { r.Title = "Do a thing." },
		"when the rule fires must be":       func(r *Rule) { r.Fires = "no stop" },
		"how the gain is sized must be":     func(r *Rule) { r.Sized = "no stop" },
		"reads no metric":                   func(r *Rule) { r.Reads = nil },
		"reads the unknown metric":          func(r *Rule) { r.Reads = []string{"review.mood"} },
		"unknown playbook page":             func(r *Rule) { r.Play = "work-harder" },
		"no contract version":               func(r *Rule) { r.Since = "" },
	}
	for want, change := range cases {
		r := good
		change(&r)
		found := false
		for _, err := range ValidateRules([]Rule{r}) {
			found = found || strings.Contains(err.Error(), want)
		}
		if !found {
			t.Errorf("no error with %q for %+v", want, r)
		}
	}
	found := false
	for _, err := range ValidateRules([]Rule{good, good}) {
		found = found || strings.Contains(err.Error(), "used twice")
	}
	if !found {
		t.Error("a repeated id was not caught")
	}
}
