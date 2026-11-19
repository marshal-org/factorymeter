package view

import (
	"net/url"
	"strings"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/contract"
)

func TestLightsOut(t *testing.T) {
	r := sample()
	r.Metrics = append(r.Metrics,
		contract.Series{Metric: "ci.work.p50", Unit: "hours", Value: p(0.25), N: 9},
		contract.Series{Metric: "ci.flow", Unit: "per_week", Value: p(9), N: 18},
		contract.Series{Metric: "release.deploys", Unit: "per_week", Value: p(0.1), N: 1},
	)
	u := LightsOut(r)
	base, frag, ok := strings.Cut(u, "#")
	if !ok || base != "https://thesoftwarefactory.site/lights-out/?utm_source=factorymeter&utm_medium=page" {
		t.Fatalf("link: %q", u)
	}
	q, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatal(err)
	}
	// Each number in the simulator's unit, inside its form's limits.
	for k, want := range map[string]string{
		"prw":    "3",    // changes merged a week
		"ciMin":  "15",   // a quarter of an hour
		"ciRuns": "3",    // nine rounds a week over three changes
		"revH":   "3.5",  // wait for first review
		"dpw":    "0.25", // raised to the least the form takes
		"rework": "0.25", // sent back in review
		"ai":     "agents",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	// What the report does not know is left to the simulator.
	if q.Has("devs") {
		t.Error("the link names a number of developers the report does not hold")
	}
	if LightsOut(&contract.Report{}) != "" {
		t.Error("a report with no numbers links to the simulator")
	}
	if out := htmlOf(t, r, HTMLOptions{}); !strings.Contains(out, `<a class="sim-a" href="https://thesoftwarefactory.site/lights-out/?utm_source=factorymeter&amp;utm_medium=page#`) {
		t.Error("the page does not link to the simulator")
	}
}
