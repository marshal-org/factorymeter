package view

import (
	"math"
	"net/url"
	"strconv"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/metrics"
)

// LightsOut is the link to the Lights Out simulator, filled in with the
// numbers of a report. It is empty when the report has none of them.
//
// The numbers ride after the "#". A browser never sends that part of an
// address to a server, so opening the link shares nothing. The simulator
// reads them, fills in its form, and takes them out of the address bar. A
// number the report does not have is left out, and the simulator keeps its
// own default for it.
func LightsOut(r *contract.Report) string {
	b := open(r)
	whole := contract.Slice{}
	q := url.Values{}
	val := func(id string) (float64, bool) {
		s := b.get(id, whole)
		if s == nil || s.Value == nil || s.Thin {
			return 0, false
		}
		return *s.Value, true
	}
	set := func(key string, v, lo, hi float64, digits int) {
		p := math.Pow(10, float64(digits))
		v = math.Round(math.Max(lo, math.Min(hi, v))*p) / p
		q.Set(key, strconv.FormatFloat(v, 'f', -1, 64))
	}
	// The limits are the ones of the simulator's own form.
	flow, haveFlow := val("line.flow")
	if haveFlow && flow > 0 {
		set("prw", flow, 1, 20000, 1)
	}
	if h, ok := val("ci.work.p50"); ok {
		set("ciMin", h*60, 1, 600, 1)
	}
	if rounds, ok := val("ci.flow"); ok && haveFlow && flow > 0 {
		set("ciRuns", rounds/flow, 1, 20, 1)
	}
	if h, ok := val("review.wait.p50"); ok {
		set("revH", h, 0.5, 200, 1)
	}
	if d, ok := val("release.deploys"); ok && d > 0 {
		set("dpw", d, 0.25, 500, 2)
	}
	if s, ok := val("review.rework"); ok {
		set("rework", s, 0, 0.9, 2)
	}
	if len(q) == 0 {
		return ""
	}
	ai := "auto"
	if s, ok := val("line.agent_share"); ok && s > 0 {
		ai = "agents"
	}
	q.Set("ai", ai)
	return metrics.LightsOutURL + "?utm_source=factorymeter&utm_medium=page#" + q.Encode()
}
