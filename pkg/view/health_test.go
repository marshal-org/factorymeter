package view

import (
	"strings"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

func TestGrade(t *testing.T) {
	ci := contract.Station{ID: line.CI, Name: "CI", Status: contract.StatusMeasured}
	series := func(rework float64, thin bool, trend string) *book {
		return open(&contract.Report{Metrics: []contract.Series{
			{Metric: "ci.rework", Unit: "share", Value: p(rework), Thin: thin},
			{Metric: "ci.wait.p50", Unit: "hours", Value: p(2), Previous: p(1), Trend: trend},
		}})
	}
	for _, c := range []struct {
		name   string
		held   float64
		rework float64
		thin   bool
		trend  string
		want   string
	}{
		{"all calm", 0.1, 0.05, false, contract.TrendBetter, "good"},
		{"a load to watch", 0.2, 0.05, false, contract.TrendFlat, "warn"},
		{"a strained load", 0.4, 0.05, false, contract.TrendFlat, "bad"},
		{"rework to watch", 0.1, 0.2, false, contract.TrendFlat, "warn"},
		{"strained rework", 0.1, 0.45, false, contract.TrendFlat, "bad"},
		{"rework on too few items is left out", 0.1, 0.9, true, contract.TrendFlat, "good"},
		{"a wait that got worse", 0.1, 0.05, false, contract.TrendWorse, "warn"},
	} {
		st := ci
		st.HeldShare = p(c.held)
		if got := grade(st, series(c.rework, c.thin, c.trend), 13); got.Class != c.want {
			t.Errorf("%s: %s, want %s: %q", c.name, got.Class, c.want, got.Reasons)
		}
	}
	h := grade(ci, open(&contract.Report{}), 13)
	if h.Class != "none" || len(h.Reasons) != 0 {
		t.Errorf("a station with no numbers: %+v", h)
	}
	off := ci
	off.Status = contract.StatusNotConnected
	if h := grade(off, series(0.9, false, contract.TrendWorse), 13); h.Class != "off" {
		t.Errorf("a station that is not connected: %+v", h)
	}
	st := ci
	st.HeldShare = p(0.1)
	if got := strings.Join(grade(st, series(0.3, false, contract.TrendWorse), 13).Reasons, " "); !strings.Contains(got, "Rework: CI rounds that failed, 30%.") ||
		!strings.Contains(got, "2.0h, from 1.0h in the 13 weeks before") {
		t.Errorf("reasons: %q", got)
	}
}

// A median and its 90th percentile share one card and one chart.
func TestHTMLPairsMedianAnd90th(t *testing.T) {
	r := sample()
	r.Metrics = append(r.Metrics, contract.Series{Metric: "review.wait.p90", Unit: "hours", Value: p(9), N: 6, Points: points(p(2), nil, p(3), p(9))})
	out := htmlOf(t, r, HTMLOptions{})
	for _, want := range []string{
		"<h4>Wait for first review</h4>",
		"Median and 90th percentile of 6 PRs",
		`<span class="cut">90th</span><span class="v">9.0h</span>`,
		`data-names="Median|90th percentile" data-keys="s1|s1 p90"`,
		"<code>review.wait.p50, review.wait.p90</code>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(out, "<h4>Wait for first review, 90th percentile</h4>") {
		t.Error("the 90th percentile has a card of its own")
	}
}
