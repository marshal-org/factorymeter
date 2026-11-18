// Package metrics turns collected pull requests into the numbers in the
// report. Every definition used here is written out in docs/metrics.md.
package metrics

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// MinSample is the smallest group the report will compare or call a median
// on without a warning.
const MinSample = 5

// GapRatio is how far apart the agent and human medians must be before the
// headline calls it a difference.
const GapRatio = 1.5

// MinGapHours is the smallest wait worth reporting as a gap away from the
// constraint. Below it, a large ratio is a difference of minutes.
const MinGapHours = 1.0

// Dist summarises a list of durations or counts.
type Dist struct {
	N      int     `json:"n"`
	Median float64 `json:"median"`
	P75    float64 `json:"p75"`
	Mean   float64 `json:"mean"`
	Total  float64 `json:"total"`
	// Capped is the total with every value cut off at the 90th percentile.
	// The constraint is ranked on it, so that one stuck pull request cannot
	// outvote all the others.
	Capped float64 `json:"capped_total"`
}

func dist(v []float64) Dist {
	if len(v) == 0 {
		return Dist{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	t, capped, p90 := 0.0, 0.0, quantile(s, 0.9)
	for _, x := range s {
		t += x
		capped += math.Min(x, p90)
	}
	return Dist{N: len(s), Median: quantile(s, 0.5), P75: quantile(s, 0.75), Mean: t / float64(len(s)), Total: t, Capped: capped}
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

// Stage ids, in the order work flows through them.
const (
	StageDraft       = "draft"
	StageReviewWait  = "review_wait"
	StageReviewCycle = "review_cycle"
	StageMergeWait   = "merge_wait"
	StageUnreviewed  = "unreviewed"
	StageCI          = "ci"
)

// StageDef describes one stage of the line that GitHub history can time.
type StageDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Station is the Lights Out station the stage belongs to.
	Station string `json:"station"`
	// What says what the clock measures.
	What string `json:"what"`
	// where finishes "Pull requests spend most of their time ...".
	where string
	// verb and object build "<who> <verb> <ratio> longer <object>".
	verb, object string
}

// Stages lists every stage in line order.
var Stages = []StageDef{
	{ID: StageDraft, Name: "In draft", Station: "code", What: "opened as a draft until marked ready for review",
		where: "in draft, before anyone is asked to review", verb: "stay", object: "in draft"},
	{ID: StageReviewWait, Name: "Waiting for first review", Station: "review", What: "ready for review until the first review by a person other than the author",
		where: "waiting for a first review", verb: "wait", object: "for a first review"},
	{ID: StageReviewCycle, Name: "First review to approval", Station: "review", What: "first human review until the last approval before merge, or until merge when nobody approved",
		where: "between the first review and approval", verb: "take", object: "to get from first review to approval"},
	{ID: StageMergeWait, Name: "Approved, waiting to merge", Station: "merge", What: "last approval until merge",
		where: "approved and waiting to merge", verb: "wait", object: "to merge after approval"},
	{ID: StageUnreviewed, Name: "Ready, merged with no human review", Station: "merge", What: "ready for review until merge, for pull requests no other person reviewed",
		where: "ready and waiting for a merge that no other person reviews", verb: "wait", object: "between ready and merge when nobody reviews"},
	{ID: StageCI, Name: "CI running", Station: "ci", What: "time between open and merge while at least one GitHub Actions run was in flight",
		where: "waiting on CI", verb: "spend", object: "in CI"},
}

func stageDef(id string) StageDef {
	for _, s := range Stages {
		if s.ID == id {
			return s
		}
	}
	return StageDef{ID: id, Name: id}
}

// ClassStats holds the numbers for one class of pull requests.
type ClassStats struct {
	Class          string  `json:"class"`
	Touched        int     `json:"touched"`
	Merged         int     `json:"merged"`
	ClosedUnmerged int     `json:"closed_unmerged"`
	Open           int     `json:"open"`
	MergedPerWeek  float64 `json:"merged_per_week"`
	// MergeRate is merged / (merged + closed without merging), for pull
	// requests that closed in the window. Nil when none closed.
	MergeRate *float64 `json:"merge_rate"`
	Authors   int      `json:"authors"`

	LeadTimeH          Dist `json:"lead_time_hours"`
	TimeToFirstReviewH Dist `json:"time_to_first_review_hours"`
	// HumanReviewedShare is the share of merged pull requests that a person
	// other than the author reviewed before merge.
	HumanReviewedShare *float64 `json:"human_reviewed_share"`
	BotReviewedShare   *float64 `json:"bot_reviewed_share"`
	// ReworkRate is the share of reviewed, merged pull requests that were
	// sent back: a reviewer requested changes, or a commit landed after the
	// first human review.
	ReworkRate *float64 `json:"rework_rate"`
	Reviewed   int      `json:"reviewed"`

	// CIPRs is the number of merged pull requests with at least one counted
	// CI run.
	CIPRs           int      `json:"ci_prs"`
	CIMinutesPerRun Dist     `json:"ci_minutes_per_run"`
	CIRunsPerPR     Dist     `json:"ci_runs_per_pr"`
	CIFailureRate   *float64 `json:"ci_failure_rate"`

	SizeLines Dist `json:"size_lines"`
	// Stages is the wall-clock time of each stage, as a person would read it
	// off the pull request timeline.
	Stages map[string]Dist `json:"stage_hours"`
	// Exclusive is the same with the time a CI run was in flight taken out
	// and counted as CI. The exclusive stages add up to open to merge, so
	// they can be ranked against each other.
	Exclusive map[string]Dist `json:"stage_exclusive_hours"`
}

// AgentCount is the number of merged pull requests one agent was detected on.
type AgentCount struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Merged int    `json:"merged"`
	High   int    `json:"high_confidence"`
	Medium int    `json:"medium_confidence"`
}

// Constraint names the stage where pull requests spend the most time.
type Constraint struct {
	Stage   string `json:"stage"`
	Name    string `json:"name"`
	Station string `json:"station"`
	// Share is the stage's exclusive hours over the exclusive hours of all
	// stages, each capped at the stage's 90th percentile.
	Share      float64 `json:"share_of_open_to_merge"`
	TotalHours float64 `json:"total_hours"`
	// Comparable is true when both classes have enough pull requests in the
	// stage to compare.
	Comparable  bool    `json:"comparable"`
	AgentMedian float64 `json:"agent_median_hours"`
	HumanMedian float64 `json:"human_median_hours"`
	// Ratio is the agent figure over the human figure. Zero when not
	// comparable.
	Ratio float64 `json:"agent_over_human"`
	// Basis is "median", or "mean" when a median is zero.
	Basis string `json:"basis,omitempty"`
}

// LightsOut carries the measured inputs of the Lights Out simulator under the
// simulator's own key names. A key is left out when it could not be measured.
type LightsOut struct {
	Devs   *float64 `json:"devs,omitempty"`
	PRW    *float64 `json:"prw,omitempty"`
	CIMin  *float64 `json:"ciMin,omitempty"`
	RevH   *float64 `json:"revH,omitempty"`
	DPW    *float64 `json:"dpw,omitempty"`
	AI     string   `json:"ai"`
	Rework *float64 `json:"rework,omitempty"`
	CIRuns *float64 `json:"ciRuns,omitempty"`
	// NotMeasured lists the simulator inputs GitHub history cannot give.
	NotMeasured []string `json:"not_measured"`
	URL         string   `json:"url"`
}

// RepoRow is one line of the per-repository table.
type RepoRow struct {
	Repo        string  `json:"repo"`
	Merged      int     `json:"merged"`
	AgentMerged int     `json:"agent_merged"`
	HumanMerged int     `json:"human_merged"`
	ReviewWaitH float64 `json:"median_review_wait_hours"`
	ReviewedN   int     `json:"reviewed"`
}

// PRRow is one pull request with its class and stage clocks, for the detail
// export and for checking the report by hand.
type PRRow struct {
	URL            string             `json:"url"`
	Class          signature.Class    `json:"class"`
	Agents         []string           `json:"agents,omitempty"`
	PossibleAgents []string           `json:"possible_agents,omitempty"`
	Signatures     []string           `json:"signatures,omitempty"`
	State          string             `json:"state"`
	CreatedAt      time.Time          `json:"created_at"`
	ReadyAt        *time.Time         `json:"ready_at,omitempty"`
	FirstReviewAt  *time.Time         `json:"first_human_review_at,omitempty"`
	ApprovedAt     *time.Time         `json:"last_approval_at,omitempty"`
	MergedAt       *time.Time         `json:"merged_at,omitempty"`
	Rework         bool               `json:"rework,omitempty"`
	CIRuns         int                `json:"ci_runs,omitempty"`
	CIMinutes      []float64          `json:"ci_minutes,omitempty"`
	StageHours     map[string]float64 `json:"stage_hours,omitempty"`
	ExclusiveHours map[string]float64 `json:"stage_exclusive_hours,omitempty"`
}

// Report is everything the output formats print.
type Report struct {
	Target          string    `json:"target"`
	Repos           []string  `json:"repos"`
	Since           time.Time `json:"since"`
	Until           time.Time `json:"until"`
	Days            float64   `json:"days"`
	GeneratedAt     time.Time `json:"generated_at"`
	Version         string    `json:"factorymeter_version"`
	RegistryVersion int       `json:"registry_version"`

	Headline string   `json:"headline"`
	Findings []string `json:"findings"`

	Constraint *Constraint `json:"constraint"`

	// DisclosedAgentShare is agent pull requests over agent plus human pull
	// requests, among those merged in the window. It is a floor: agents that
	// leave no trace count as human.
	DisclosedAgentShare *float64     `json:"disclosed_agent_share"`
	PossibleAgentPRs    int          `json:"possible_agent_prs"`
	Agents              []AgentCount `json:"agents"`

	// Line is agent and human pull requests together: the work the line
	// exists to ship. The constraint is found on it.
	Line       ClassStats `json:"line"`
	All        ClassStats `json:"all"`
	Agent      ClassStats `json:"agent"`
	Human      ClassStats `json:"human"`
	Automation ClassStats `json:"automation"`

	WaitingForReview int     `json:"waiting_for_first_review_now"`
	OldestWaitingH   float64 `json:"oldest_waiting_hours"`

	DeploysPerWeek *float64 `json:"deploys_per_week"`
	DeploySource   string   `json:"deploy_source"`
	CISource       string   `json:"ci_source"`

	LightsOut LightsOut `json:"lights_out"`

	PerRepo []RepoRow `json:"per_repo,omitempty"`
	Limits  []string  `json:"limits"`
	Notes   []string  `json:"notes,omitempty"`
	PRs     []PRRow   `json:"pull_requests,omitempty"`
}

// prFacts are the per pull request values every aggregate is built from.
type prFacts struct {
	pr     *model.PR
	res    signature.Result
	merged bool

	firstReview *time.Time
	approved    *time.Time
	botReviewed bool
	rework      bool

	// stage is wall-clock hours per stage. excl is the same with CI time
	// carved out.
	stage, excl map[string]float64
	ciMinutes   []float64
	ciRuns      int
	ciFailed    int
}

func isHumanReview(r model.Review, author string) bool {
	return r.Author.Type != "Bot" && r.Author.Login != "" && r.Author.Login != "ghost" && !strings.EqualFold(r.Author.Login, author)
}

func hours(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return d.Hours()
}

// skippedConclusion lists runs that did no CI work.
var skippedConclusion = map[string]bool{"skipped": true, "cancelled": true, "stale": true, "action_required": true, "neutral": true, "": true}

func facts(pr *model.PR, res signature.Result, since, until time.Time) prFacts {
	f := prFacts{pr: pr, res: res, stage: map[string]float64{}, excl: map[string]float64{}}
	f.merged = pr.MergedAt != nil && !pr.MergedAt.Before(since) && !pr.MergedAt.After(until)
	end := until
	if pr.MergedAt != nil {
		end = *pr.MergedAt
	} else if pr.ClosedAt != nil {
		end = *pr.ClosedAt
	}
	for i := range pr.Reviews {
		r := pr.Reviews[i]
		if r.SubmittedAt.After(end) {
			continue
		}
		if r.Author.Type == "Bot" {
			f.botReviewed = true
			continue
		}
		if !isHumanReview(r, pr.Author.Login) {
			continue
		}
		if f.firstReview == nil {
			t := r.SubmittedAt
			f.firstReview = &t
		}
		if r.State == "APPROVED" {
			t := r.SubmittedAt
			f.approved = &t
		}
		if r.State == "CHANGES_REQUESTED" {
			f.rework = true
		}
	}
	if f.firstReview != nil {
		for _, c := range pr.Commits {
			// A merge commit only pulls the base branch in. It is not rework.
			if c.Parents < 2 && c.CommittedAt.After(*f.firstReview) {
				f.rework = true
				break
			}
		}
	}

	// CI: one "run" is everything GitHub Actions did for one pushed commit.
	type shaAgg struct {
		minutes  float64
		attempts int
		failed   bool
		counted  bool
	}
	bySHA := map[string]*shaAgg{}
	var order []string
	var busy spans
	for _, r := range pr.CIRuns {
		if r.Status != "completed" || skippedConclusion[r.Conclusion] {
			continue
		}
		start := r.StartedAt
		if start.IsZero() {
			start = r.CreatedAt
		}
		m := r.UpdatedAt.Sub(start).Minutes()
		if m <= 0 {
			continue
		}
		busy = append(busy, span{start, r.UpdatedAt})
		a := bySHA[r.SHA]
		if a == nil {
			a = &shaAgg{}
			bySHA[r.SHA] = a
			order = append(order, r.SHA)
		}
		a.counted = true
		if m > a.minutes {
			a.minutes = m
		}
		if r.Attempt > a.attempts {
			a.attempts = r.Attempt
		}
		if r.Conclusion == "failure" || r.Conclusion == "timed_out" || r.Attempt > 1 {
			a.failed = true
		}
	}
	busy = busy.merge()
	for _, sha := range order {
		a := bySHA[sha]
		f.ciMinutes = append(f.ciMinutes, a.minutes)
		extra := a.attempts - 1
		if extra < 0 {
			extra = 0
		}
		f.ciRuns += 1 + extra
		if a.failed {
			f.ciFailed++
		}
	}

	if !f.merged {
		return f
	}
	ready := pr.CreatedAt
	if pr.ReadyAt != nil {
		ready = *pr.ReadyAt
	}
	if ready.After(*pr.MergedAt) {
		ready = *pr.MergedAt
	}
	// Each stage is timed twice. Wall-clock is what the pull request timeline
	// shows. Exclusive takes out the time a CI run was in flight and counts
	// it as CI, so the exclusive stages add up to the time from open to merge.
	set := func(id string, from, to time.Time) {
		wall := hours(to.Sub(from))
		f.stage[id] = wall
		f.excl[id] = math.Max(0, wall-busy.within(from, to))
	}
	set(StageDraft, pr.CreatedAt, ready)
	if f.firstReview == nil {
		set(StageUnreviewed, ready, *pr.MergedAt)
	} else {
		first := *f.firstReview
		if first.Before(ready) {
			first = ready // reviewed while still a draft: it never waited
		}
		set(StageReviewWait, ready, first)
		done := *pr.MergedAt
		if f.approved != nil && !f.approved.Before(first) {
			done = *f.approved
		}
		set(StageReviewCycle, first, done)
		set(StageMergeWait, done, *pr.MergedAt)
	}
	if len(f.ciMinutes) > 0 {
		ci := busy.within(pr.CreatedAt, *pr.MergedAt)
		f.stage[StageCI], f.excl[StageCI] = ci, ci
	}
	return f
}

// span is a stretch of time during which a CI run was in flight.
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

func ptr(v float64) *float64 { return &v }

func share(num, den int) *float64 {
	if den == 0 {
		return nil
	}
	return ptr(float64(num) / float64(den))
}

func aggregate(class string, fs []prFacts, weeks float64, since, until time.Time) ClassStats {
	cs := ClassStats{Class: class, Stages: map[string]Dist{}, Exclusive: map[string]Dist{}}
	authors := map[string]bool{}
	stage, excl := map[string][]float64{}, map[string][]float64{}
	var lead, ttfr, ciMin, ciRuns, size []float64
	humanReviewed, botReviewed, reworked, ciSHAs, ciFailed := 0, 0, 0, 0, 0
	for i := range fs {
		f := &fs[i]
		cs.Touched++
		switch {
		case f.merged:
			cs.Merged++
		case f.pr.State == "CLOSED" && f.pr.ClosedAt != nil && !f.pr.ClosedAt.Before(since) && !f.pr.ClosedAt.After(until):
			cs.ClosedUnmerged++
		case f.pr.State == "OPEN":
			cs.Open++
		}
		if !f.merged {
			continue
		}
		authors[strings.ToLower(f.pr.Author.Login)] = true
		lead = append(lead, hours(f.pr.MergedAt.Sub(f.pr.CreatedAt)))
		size = append(size, float64(f.pr.Additions+f.pr.Deletions))
		for id, h := range f.stage {
			stage[id] = append(stage[id], h)
			excl[id] = append(excl[id], f.excl[id])
		}
		if f.botReviewed {
			botReviewed++
		}
		if f.firstReview != nil {
			humanReviewed++
			ttfr = append(ttfr, f.stage[StageReviewWait])
			if f.rework {
				reworked++
			}
		}
		if len(f.ciMinutes) > 0 {
			cs.CIPRs++
			ciMin = append(ciMin, f.ciMinutes...)
			ciRuns = append(ciRuns, float64(f.ciRuns))
			ciSHAs += len(f.ciMinutes)
			ciFailed += f.ciFailed
		}
	}
	cs.Authors = len(authors)
	if weeks > 0 {
		cs.MergedPerWeek = float64(cs.Merged) / weeks
	}
	cs.MergeRate = share(cs.Merged, cs.Merged+cs.ClosedUnmerged)
	cs.LeadTimeH, cs.TimeToFirstReviewH, cs.SizeLines = dist(lead), dist(ttfr), dist(size)
	cs.HumanReviewedShare, cs.BotReviewedShare = share(humanReviewed, cs.Merged), share(botReviewed, cs.Merged)
	cs.Reviewed = humanReviewed
	cs.ReworkRate = share(reworked, humanReviewed)
	cs.CIMinutesPerRun, cs.CIRunsPerPR = dist(ciMin), dist(ciRuns)
	cs.CIFailureRate = share(ciFailed, ciSHAs)
	for id, v := range stage {
		cs.Stages[id] = dist(v)
		cs.Exclusive[id] = dist(excl[id])
	}
	return cs
}

// Options tune Compute.
type Options struct {
	Target  string
	Version string
	Now     time.Time
	// Detail adds one row per pull request to the report.
	Detail bool
}

// Compute builds the report from collected repositories.
func Compute(repos []model.RepoData, reg *signature.Registry, opt Options) Report {
	rep := Report{Target: opt.Target, Version: opt.Version, GeneratedAt: opt.Now.UTC(), RegistryVersion: reg.Version, DeploySource: "none", CISource: "none"}
	if len(repos) == 0 {
		return rep
	}
	rep.Since, rep.Until = repos[0].Since, repos[0].Until
	rep.Days = rep.Until.Sub(rep.Since).Hours() / 24
	weeks := rep.Days / 7

	var all, agent, human, auto []prFacts
	agentCounts := map[string]*AgentCount{}
	deploys, deploySources, ciSources := 0, map[string]bool{}, map[string]bool{}
	for ri := range repos {
		rd := &repos[ri]
		rep.Repos = append(rep.Repos, rd.Repo)
		for _, n := range rd.Notes {
			rep.Notes = append(rep.Notes, rd.Repo+": "+n)
		}
		if rd.DeploySource != "none" && rd.DeploySource != "" {
			deploys += len(rd.Deploys)
			deploySources[rd.DeploySource] = true
		}
		if rd.CISource == "actions" {
			ciSources["actions"] = true
		}
		row := RepoRow{Repo: rd.Repo}
		var wait []float64
		for pi := range rd.PRs {
			pr := &rd.PRs[pi]
			res := reg.Classify(*pr)
			f := facts(pr, res, rep.Since, rep.Until)
			all = append(all, f)
			switch res.Class {
			case signature.ClassAgent:
				agent = append(agent, f)
			case signature.ClassAutomation:
				auto = append(auto, f)
			default:
				human = append(human, f)
			}
			if f.merged {
				row.Merged++
				switch res.Class {
				case signature.ClassAgent:
					row.AgentMerged++
				case signature.ClassHuman:
					row.HumanMerged++
				}
				if res.Class != signature.ClassAutomation && f.firstReview != nil {
					wait = append(wait, f.stage[StageReviewWait])
				}
				if res.Class == signature.ClassHuman && len(res.PossibleAgents) > 0 {
					rep.PossibleAgentPRs++
				}
				for _, id := range res.Agents {
					ac := agentCounts[id]
					if ac == nil {
						ac = &AgentCount{ID: id, Name: reg.AgentName(id)}
						agentCounts[id] = ac
					}
					ac.Merged++
					if strongest(res, id) == signature.High {
						ac.High++
					} else {
						ac.Medium++
					}
				}
			}
			// A pull request that is open, ready, and has no human review is
			// standing in the review queue right now.
			if pr.State == "OPEN" && !pr.IsDraft && f.firstReview == nil && res.Class != signature.ClassAutomation {
				rep.WaitingForReview++
				ready := pr.CreatedAt
				if pr.ReadyAt != nil {
					ready = *pr.ReadyAt
				}
				if h := hours(rep.Until.Sub(ready)); h > rep.OldestWaitingH {
					rep.OldestWaitingH = h
				}
			}
			if opt.Detail {
				rep.PRs = append(rep.PRs, prRow(f))
			}
		}
		d := dist(wait)
		row.ReviewWaitH, row.ReviewedN = d.Median, d.N
		rep.PerRepo = append(rep.PerRepo, row)
	}
	if len(repos) == 1 {
		rep.PerRepo = nil
	} else {
		sort.SliceStable(rep.PerRepo, func(i, j int) bool { return rep.PerRepo[i].Merged > rep.PerRepo[j].Merged })
	}

	rep.All = aggregate("all", all, weeks, rep.Since, rep.Until)
	rep.Agent = aggregate("agent", agent, weeks, rep.Since, rep.Until)
	rep.Human = aggregate("human", human, weeks, rep.Since, rep.Until)
	rep.Automation = aggregate("automation", auto, weeks, rep.Since, rep.Until)
	rep.DisclosedAgentShare = share(rep.Agent.Merged, rep.Agent.Merged+rep.Human.Merged)
	for _, a := range reg.Agents {
		if ac := agentCounts[a.ID]; ac != nil {
			rep.Agents = append(rep.Agents, *ac)
		}
	}
	sort.SliceStable(rep.Agents, func(i, j int) bool { return rep.Agents[i].Merged > rep.Agents[j].Merged })

	if len(deploySources) > 0 && weeks > 0 {
		rep.DeploysPerWeek = ptr(float64(deploys) / weeks)
		var src []string
		for s := range deploySources {
			src = append(src, s)
		}
		sort.Strings(src)
		rep.DeploySource = strings.Join(src, ", ")
	}
	if ciSources["actions"] {
		rep.CISource = "actions"
	}

	// The line is the agent and human pull requests together. Automation is
	// left out so a wave of dependency bumps cannot hide the constraint.
	line := aggregate("line", append(append([]prFacts{}, agent...), human...), weeks, rep.Since, rep.Until)
	rep.Line = line
	rep.Constraint = findConstraint(line, rep.Agent, rep.Human)
	rep.Headline = headline(rep.Constraint, line)
	rep.Findings = findings(&rep)
	rep.LightsOut = lightsOut(&rep, line, all)
	rep.Limits = limits(&rep)
	return rep
}

func strongest(res signature.Result, agent string) signature.Confidence {
	var best signature.Confidence
	for _, m := range res.Matches {
		if m.Agent == agent && m.Confidence.Rank() > best.Rank() {
			best = m.Confidence
		}
	}
	return best
}

func prRow(f prFacts) PRRow {
	row := PRRow{URL: f.pr.URL, Class: f.res.Class, Agents: f.res.Agents, PossibleAgents: f.res.PossibleAgents, State: f.pr.State,
		CreatedAt: f.pr.CreatedAt, ReadyAt: f.pr.ReadyAt, FirstReviewAt: f.firstReview, ApprovedAt: f.approved, MergedAt: f.pr.MergedAt,
		Rework: f.rework && f.firstReview != nil, CIRuns: f.ciRuns, CIMinutes: f.ciMinutes}
	for _, m := range f.res.Matches {
		row.Signatures = append(row.Signatures, m.Signature)
	}
	if f.merged {
		row.StageHours, row.ExclusiveHours = f.stage, f.excl
	}
	return row
}

// findConstraint picks the stage that holds the most pull request hours across
// the line. By Little's law, hours spent in a stage are in proportion to the
// work standing in it on an average day, so the largest total is the longest
// queue. Totals are capped at each stage's 90th percentile first.
func findConstraint(line, agent, human ClassStats) *Constraint {
	best, bestTotal, all := "", 0.0, 0.0
	for _, s := range Stages {
		d := line.Exclusive[s.ID]
		all += d.Capped
		if d.Capped > bestTotal {
			best, bestTotal = s.ID, d.Capped
		}
	}
	if best == "" {
		return nil
	}
	def := stageDef(best)
	c := &Constraint{Stage: best, Name: def.Name, Station: def.Station, TotalHours: line.Exclusive[best].Total, Share: bestTotal / all}
	c.Comparable, c.AgentMedian, c.HumanMedian, c.Ratio, c.Basis = compare(agent.Stages[best], human.Stages[best])
	return c
}

// compare sets the agent figure against the human figure for one stage. It
// uses medians, and means when a median is zero. It reports false when either
// class has fewer than MinSample pull requests in the stage.
func compare(a, h Dist) (ok bool, agent, human, ratio float64, basis string) {
	if a.N < MinSample || h.N < MinSample {
		return false, 0, 0, 0, ""
	}
	agent, human, basis = a.Median, h.Median, "median"
	if agent == 0 || human == 0 {
		agent, human, basis = a.Mean, h.Mean, "mean"
	}
	if agent <= 0 || human <= 0 {
		return false, agent, human, 0, basis
	}
	return true, agent, human, agent / human, basis
}

// StageShare is the stage's share of the exclusive hours of all stages, each
// capped at the stage's 90th percentile.
func StageShare(line ClassStats, id string) float64 {
	all := 0.0
	for _, s := range Stages {
		all += line.Exclusive[s.ID].Capped
	}
	if all == 0 {
		return 0
	}
	return line.Exclusive[id].Capped / all
}
