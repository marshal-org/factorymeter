package metrics

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/signature"
)

// LightsOutURL is the simulator the report links to.
const LightsOutURL = "https://thesoftwarefactory.site/lights-out/"

// Hours prints a duration given in hours the way a person would say it.
func Hours(h float64) string {
	switch {
	case h < 1.0/60:
		return "under a minute"
	case h < 1:
		return fmt.Sprintf("%.0f min", h*60)
	case h < 48:
		return trim(h) + " h"
	default:
		return trim(h/24) + " days"
	}
}

// Minutes prints a duration given in minutes.
func Minutes(m float64) string {
	if m < 1 {
		return fmt.Sprintf("%.0f s", m*60)
	}
	if m >= 120 {
		return trim(m/60) + " h"
	}
	return trim(m) + " min"
}

// Percent prints a share between 0 and 1.
func Percent(p *float64) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", *p*100)
}

// trim prints one decimal below 10 and none above, without a trailing ".0".
func trim(v float64) string {
	if v >= 10 {
		return strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

// Times prints a ratio: "5 times", "2.3 times".
func Times(r float64) string { return trim(r) + " times" }

func headline(c *Constraint, line ClassStats) string {
	if c == nil {
		return "No agent or human pull request merged in this window, so there is no constraint to name."
	}
	def := stageDef(c.Stage)
	d := line.Stages[c.Stage]
	if c.Comparable {
		a, h := Hours(c.AgentMedian), Hours(c.HumanMedian)
		switch {
		case c.Ratio >= GapRatio:
			return fmt.Sprintf("Your agent pull requests %s %s longer %s than human pull requests: a %s of %s against %s.",
				def.verb, Times(c.Ratio), def.object, c.Basis, a, h)
		case c.Ratio <= 1/GapRatio:
			return fmt.Sprintf("Your human pull requests %s %s longer %s than agent pull requests: a %s of %s against %s.",
				def.verb, Times(1/c.Ratio), def.object, c.Basis, h, a)
		}
		return fmt.Sprintf("Your pull requests spend most of their time %s. Agent and human pull requests are close there: a %s of %s against %s.",
			def.where, c.Basis, a, h)
	}
	return fmt.Sprintf("Your pull requests spend most of their time %s: a median of %s, and %.0f%% of the time from open to merge.",
		def.where, Hours(d.Median), c.Share*100)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func findings(r *Report) []string {
	var out []string
	if r.All.Merged == 0 {
		return []string{"No pull request merged in the window. Try a longer window with --since."}
	}
	if c := r.Constraint; c != nil {
		def := stageDef(c.Stage)
		out = append(out, fmt.Sprintf("The constraint is the %s station (%s). It holds %.0f%% of the time agent and human pull requests spend between open and merge.",
			def.Station, strings.ToLower(def.Name), c.Share*100))
		if !c.Comparable {
			out = append(out, fmt.Sprintf("There are too few pull requests to compare agents with humans at the constraint. It takes %d of each, and this window has %d agent and %d human.",
				MinSample, r.Agent.Stages[c.Stage].N, r.Human.Stages[c.Stage].N))
		}
	}
	if g := widestGap(r); g != "" {
		out = append(out, g)
	}
	if r.DisclosedAgentShare != nil {
		out = append(out, fmt.Sprintf("Disclosed agent share is %s: %d of the %d pull requests merged by agents and people. This is a floor, because an agent that leaves no trace counts as human.",
			Percent(r.DisclosedAgentShare), r.Agent.Merged, r.Agent.Merged+r.Human.Merged))
	}
	if a, h := r.Agent.TimeToFirstReviewH, r.Human.TimeToFirstReviewH; a.N > 0 && h.N > 0 {
		out = append(out, fmt.Sprintf("Time to first review is a median of %s for agent pull requests (%d reviewed) and %s for human pull requests (%d reviewed).",
			Hours(a.Median), a.N, Hours(h.Median), h.N))
	}
	if a, h := r.Agent.HumanReviewedShare, r.Human.HumanReviewedShare; a != nil && h != nil {
		out = append(out, fmt.Sprintf("%.0f%% of agent pull requests merged with no review by another person, against %.0f%% of human pull requests.", (1-*a)*100, (1-*h)*100))
	} else if h != nil {
		out = append(out, fmt.Sprintf("%.0f%% of human pull requests merged with no review by another person.", (1-*h)*100))
	}
	if a, h := r.Agent.ReworkRate, r.Human.ReworkRate; a != nil && h != nil {
		out = append(out, fmt.Sprintf("Review sent back %s of agent pull requests and %s of human pull requests.", Percent(a), Percent(h)))
	}
	if a, h := r.Agent.MergeRate, r.Human.MergeRate; a != nil && h != nil {
		out = append(out, fmt.Sprintf("%s of closed agent pull requests merged, against %s of human pull requests.", Percent(a), Percent(h)))
	}
	if r.WaitingForReview > 0 {
		out = append(out, fmt.Sprintf("%s waiting for a first review right now. The oldest has waited %s.",
			plural(r.WaitingForReview, "pull request is", "pull requests are"), Hours(r.OldestWaitingH)))
	}
	for _, c := range []struct {
		name string
		n    int
	}{{"agent", r.Agent.Merged}, {"human", r.Human.Merged}} {
		if c.n > 0 && c.n < MinSample {
			out = append(out, fmt.Sprintf("Only %s merged. Read the %s column as examples, not as a trend.", plural(c.n, c.name+" pull request", c.name+" pull requests"), c.name))
		}
	}
	return out
}

func round(v float64, places int) *float64 {
	p := math.Pow(10, float64(places))
	return ptr(math.Round(v*p) / p)
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// lightsOut maps the measured numbers onto the simulator's inputs. The clamps
// are the simulator's own form limits.
func lightsOut(r *Report, line ClassStats, all []prFacts) LightsOut {
	lo := LightsOut{AI: "auto", NotMeasured: []string{"envs", "pms"}}
	people := map[string]bool{}
	for i := range all {
		f := &all[i]
		if f.merged && f.pr.Author.Type != "Bot" && f.res.Class != signature.ClassAutomation && f.pr.Author.Login != "" {
			people[strings.ToLower(f.pr.Author.Login)] = true
		}
	}
	q := url.Values{}
	set := func(key string, v *float64) {
		if v != nil {
			q.Set(key, strconv.FormatFloat(*v, 'f', -1, 64))
		}
	}
	if len(people) > 0 {
		lo.Devs = ptr(clamp(float64(len(people)), 1, 2000))
	} else {
		lo.NotMeasured = append(lo.NotMeasured, "devs")
	}
	if line.Merged > 0 {
		lo.PRW = round(clamp(line.MergedPerWeek, 1, 20000), 1)
	} else {
		lo.NotMeasured = append(lo.NotMeasured, "prw")
	}
	if line.CIMinutesPerRun.N > 0 {
		lo.CIMin = round(clamp(line.CIMinutesPerRun.Median, 1, 600), 1)
		lo.CIRuns = round(clamp(line.CIRunsPerPR.Mean, 1, 20), 1)
	} else {
		lo.NotMeasured = append(lo.NotMeasured, "ciMin")
	}
	if line.TimeToFirstReviewH.N > 0 {
		lo.RevH = round(clamp(line.TimeToFirstReviewH.Median, 0.5, 200), 1)
	} else {
		lo.NotMeasured = append(lo.NotMeasured, "revH")
	}
	if r.DeploysPerWeek != nil && *r.DeploysPerWeek > 0 {
		lo.DPW = round(clamp(*r.DeploysPerWeek, 0.25, 500), 2)
	} else {
		lo.NotMeasured = append(lo.NotMeasured, "dpw")
	}
	if line.ReworkRate != nil && line.Reviewed >= MinSample {
		lo.Rework = round(clamp(*line.ReworkRate, 0, 0.9), 2)
	}
	if r.Agent.Merged > 0 {
		lo.AI = "agents"
	}
	set("devs", lo.Devs)
	set("prw", lo.PRW)
	set("ciMin", lo.CIMin)
	set("revH", lo.RevH)
	set("dpw", lo.DPW)
	set("rework", lo.Rework)
	set("ciRuns", lo.CIRuns)
	q.Set("ai", lo.AI)
	// The numbers ride after the "#". A browser never sends that part of an
	// address to a server, so they stay on the reader's machine. The query
	// only says where the visit came from.
	lo.URL = LightsOutURL + "?utm_source=factorymeter&utm_medium=report#" + q.Encode()
	return lo
}

func limits(r *Report) []string {
	out := []string{
		"Disclosed agent share is a floor. Cloud agents sign their work with their own account. Local agents are counted only when they leave a commit trailer, a footer or a badge, and a person can turn those off.",
		"A branch prefix alone is not counted as an agent, because people use the same prefixes by hand.",
		"Squash merges can drop commit trailers. factorymeter reads the commits of each pull request, not the merge commit, and at most the first 100 commits.",
		"A review by a bot does not count as a review. Time to first review counts only pull requests that another person reviewed.",
		"Times are wall-clock. Nights and weekends count.",
	}
	if r.PossibleAgentPRs > 0 {
		out[1] = fmt.Sprintf("A branch prefix alone is not counted as an agent, because people use the same prefixes by hand. %s had only a prefix match and %s counted as human.",
			plural(r.PossibleAgentPRs, "merged pull request", "merged pull requests"), map[bool]string{true: "is", false: "are"}[r.PossibleAgentPRs == 1])
	}
	if r.CISource == "actions" {
		out = append(out, "CI numbers cover GitHub Actions only. One CI run is everything Actions did for one pushed commit, timed by its longest workflow. Runs that a review or a comment set off are left out.")
		out = append(out, fmt.Sprintf("CI runs are read back to %s, one window before the start. A pull request that was open before then is missing its earliest runs.", r.Since.Add(-r.Until.Sub(r.Since)).Format("2006-01-02")))
	} else {
		out = append(out, "No GitHub Actions runs were found for these pull requests, so there are no CI numbers. Other CI systems are not read.")
	}
	if r.DeploysPerWeek != nil {
		out = append(out, "Deploys per week is counted from "+deploySourceWords(r.DeploySource)+".")
	} else {
		out = append(out, "No deployments or releases were found, so deploys per week is not measured.")
	}
	out = append(out, "The simulator inputs for test environments and product managers cannot be read from GitHub. Fill them in yourself.")
	return out
}

// deploySourceWords turns the collector's source labels into words. A report
// over several repositories can have more than one, joined by ", ".
func deploySourceWords(src string) string {
	parts := strings.Split(src, ", ")
	for i, p := range parts {
		switch env, ok := strings.CutPrefix(p, "deployments:"); {
		case ok:
			parts[i] = "deployments to the " + env + " environment"
		case p == "releases":
			parts[i] = "published releases"
		}
	}
	return strings.Join(parts, " and ")
}

// widestGap names the stage, other than the constraint, where agent and human
// pull requests differ the most.
func widestGap(r *Report) string {
	bestID, bestRatio := "", GapRatio
	var ba, bh float64
	var basis string
	for _, s := range Stages {
		if r.Constraint != nil && s.ID == r.Constraint.Stage {
			continue
		}
		ok, a, h, ratio, b := compare(r.Agent.Stages[s.ID], r.Human.Stages[s.ID])
		// A gap between two short waits is not worth a line in the report.
		if !ok || math.Max(a, h) < MinGapHours {
			continue
		}
		if ratio < 1 {
			ratio = 1 / ratio
		}
		if ratio >= bestRatio {
			bestID, bestRatio, ba, bh, basis = s.ID, ratio, a, h, b
		}
	}
	if bestID == "" {
		return ""
	}
	def := stageDef(bestID)
	who, other, x, y := "Agent", "human", ba, bh
	if bh > ba {
		who, other, x, y = "Human", "agent", bh, ba
	}
	return fmt.Sprintf("Away from the constraint, the widest gap is %s: %s pull requests %s %s longer %s than %s ones, a %s of %s against %s.",
		strings.ToLower(def.Name), who, def.verb, Times(bestRatio), def.object, other, basis, Hours(x), Hours(y))
}
