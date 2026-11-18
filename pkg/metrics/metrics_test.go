package metrics

import (
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

var (
	t0    = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC) // a Monday
	since = t0.AddDate(0, 0, -14)
	until = t0.AddDate(0, 0, 14)
)

func at(h float64) time.Time   { return t0.Add(time.Duration(h * float64(time.Hour))) }
func atp(h float64) *time.Time { t := at(h); return &t }

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func registry(t *testing.T) *signature.Registry {
	t.Helper()
	r, err := signature.Default()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func person(login string) model.Actor { return model.Actor{Login: login, Type: "User"} }

func run(sha string, from, to float64, conclusion string, attempt int) model.CIRun {
	return model.CIRun{SHA: sha, Status: "completed", Conclusion: conclusion, Attempt: attempt, CreatedAt: at(from), StartedAt: at(from), UpdatedAt: at(to), Event: "pull_request"}
}

// pr builds a merged pull request: opened at hour 0, with the given hours for
// ready, first review, approval and merge. A negative hour leaves the event out.
func pr(n int, author model.Actor, ready, review, approve, merge float64) model.PR {
	p := model.PR{Repo: "o/r", Number: n, URL: "https://github.com/o/r/pull/" + string(rune('0'+n%10)), Author: author, HeadRef: "fix/x",
		State: "MERGED", CreatedAt: at(0), UpdatedAt: at(merge), MergedAt: atp(merge), ClosedAt: atp(merge), Additions: 10, Deletions: 2,
		Commits: []model.Commit{{SHA: "a", Parents: 1, CommittedAt: at(0), Authors: []model.CommitAuthor{{Login: author.Login}}}}}
	if ready > 0 {
		p.ReadyAt = atp(ready)
	}
	if review >= 0 {
		p.Reviews = append(p.Reviews, model.Review{Author: person("reviewer"), State: "COMMENTED", SubmittedAt: at(review)})
	}
	if approve >= 0 {
		p.Reviews = append(p.Reviews, model.Review{Author: person("reviewer"), State: "APPROVED", SubmittedAt: at(approve)})
	}
	return p
}

func agentPR(p model.PR) model.PR {
	p.Commits[0].Trailers = []string{"Co-Authored-By: Claude <noreply@anthropic.com>"}
	return p
}

func human() signature.Result { return signature.Result{Class: signature.ClassHuman} }

func TestDistAndQuantile(t *testing.T) {
	d := dist([]float64{4, 1, 3, 2})
	near(t, "median of an even count", d.Median, 2.5)
	near(t, "mean", d.Mean, 2.5)
	near(t, "total", d.Total, 10)
	near(t, "p75", d.P75, 3.25)
	if d.N != 4 {
		t.Errorf("n: got %d", d.N)
	}
	if (dist(nil) != Dist{}) {
		t.Error("an empty list must give a zero Dist")
	}
	near(t, "one value", dist([]float64{7}).Median, 7)
	// Ten ordinary values and one stuck one: the cap holds the stuck one to
	// the 90th percentile.
	v := []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1000}
	near(t, "capped total", dist(v).Capped, 11)
	near(t, "total keeps the stuck value", dist(v).Total, 1010)
}

func TestSpans(t *testing.T) {
	s := spans{{at(5), at(6)}, {at(1), at(2)}, {at(1.5), at(3)}, {at(3), at(3.5)}}.merge()
	if len(s) != 2 {
		t.Fatalf("got %d spans, want 2: %v", len(s), s)
	}
	near(t, "all", s.within(at(0), at(10)), 3.5)
	near(t, "clipped on both sides", s.within(at(2), at(5.5)), 2)
	near(t, "outside", s.within(at(7), at(9)), 0)
	near(t, "no spans", spans(nil).merge().within(at(0), at(10)), 0)
}

func TestFactsStagesForAReviewedPullRequest(t *testing.T) {
	// Draft for 2 h, first review at 10 h, approved at 20 h, merged at 24 h.
	p := pr(1, person("dev"), 2, 10, 20, 24)
	p.Commits = append(p.Commits, model.Commit{SHA: "b", Parents: 1, CommittedAt: at(12)})
	p.CIRuns = []model.CIRun{
		run("a", 0, 1, "failure", 1),      // in draft
		run("a", 0.5, 1.5, "success", 1),  // overlaps the first: 1.5 h busy in all
		run("b", 12, 13, "success", 2),    // in the review cycle, second attempt
		run("b", 12, 12.2, "skipped", 1),  // did no work
		run("c", 30, 31, "success", 1),    // after merge
		{SHA: "d", Status: "in_progress"}, // not finished
	}
	f := facts(&p, human(), since, until)
	if !f.merged {
		t.Fatal("not counted as merged")
	}
	for id, want := range map[string]float64{StageDraft: 2, StageReviewWait: 8, StageReviewCycle: 10, StageMergeWait: 4, StageCI: 2.5} {
		near(t, "wall "+id, f.stage[id], want)
	}
	for id, want := range map[string]float64{StageDraft: 0.5, StageReviewWait: 8, StageReviewCycle: 9, StageMergeWait: 4, StageCI: 2.5} {
		near(t, "exclusive "+id, f.excl[id], want)
	}
	sum := 0.0
	for _, h := range f.excl {
		sum += h
	}
	near(t, "the exclusive stages add up to open to merge", sum, 24)
	if _, ok := f.stage[StageUnreviewed]; ok {
		t.Error("a reviewed pull request has no unreviewed stage")
	}
	if !f.rework {
		t.Error("a commit after the first review is rework")
	}
	// Three pushed commits had counted runs: a, b and c. b ran twice.
	if f.ciRuns != 4 || f.ciFailed != 2 || len(f.ciMinutes) != 3 {
		t.Errorf("ci: runs=%d failed=%d minutes=%v, want 4 runs, 2 failed, 3 commits", f.ciRuns, f.ciFailed, f.ciMinutes)
	}
	near(t, "minutes for commit a is its longest workflow", f.ciMinutes[0], 60)
}

func TestFactsReviewRules(t *testing.T) {
	base := func() model.PR { return pr(2, person("dev"), -1, -1, -1, 10) }

	p := base()
	f := facts(&p, human(), since, until)
	near(t, "unreviewed", f.stage[StageUnreviewed], 10)
	if f.firstReview != nil || f.rework {
		t.Error("no review means no first review and no rework")
	}

	p = base()
	p.Reviews = []model.Review{
		{Author: model.Actor{Login: "copilot-pull-request-reviewer[bot]", Type: "Bot"}, State: "COMMENTED", SubmittedAt: at(1)},
		{Author: person("Dev"), State: "COMMENTED", SubmittedAt: at(2)},
		{Author: person("reviewer"), State: "APPROVED", SubmittedAt: at(11)}, // after merge
	}
	f = facts(&p, human(), since, until)
	if f.firstReview != nil || !f.botReviewed {
		t.Errorf("a bot, the author and a review after merge are not human reviews: first=%v bot=%v", f.firstReview, f.botReviewed)
	}

	p = base()
	p.Reviews = []model.Review{{Author: person("reviewer"), State: "CHANGES_REQUESTED", SubmittedAt: at(3)}}
	f = facts(&p, human(), since, until)
	if !f.rework {
		t.Error("changes requested is rework")
	}
	near(t, "no approval: the cycle runs to merge", f.stage[StageReviewCycle], 7)
	near(t, "no approval: no merge wait", f.stage[StageMergeWait], 0)

	p = base()
	p.Reviews = []model.Review{{Author: person("reviewer"), State: "APPROVED", SubmittedAt: at(3)}}
	p.Commits = append(p.Commits, model.Commit{SHA: "m", Parents: 2, CommittedAt: at(5)})
	f = facts(&p, human(), since, until)
	if f.rework {
		t.Error("a merge commit after review only pulls the base branch in. It is not rework")
	}

	// Reviewed while still a draft: it never waited for a first review.
	p = pr(3, person("dev"), 5, 2, 6, 10)
	f = facts(&p, human(), since, until)
	near(t, "draft", f.stage[StageDraft], 5)
	near(t, "review wait", f.stage[StageReviewWait], 0)
	near(t, "review cycle", f.stage[StageReviewCycle], 1)

	// Merged before the window: touched, not in the cohort.
	p = base()
	old := since.Add(-time.Hour)
	p.MergedAt = &old
	if f = facts(&p, human(), since, until); f.merged || len(f.stage) != 0 {
		t.Error("a pull request merged before the window is not in the cohort")
	}
}

// line builds a report with n agent and n human pull requests. Agents wait
// agentWait hours for a first review and humans wait humanWait.
func line(t *testing.T, n int, agentWait, humanWait float64) Report {
	t.Helper()
	var prs []model.PR
	for i := 0; i < n; i++ {
		prs = append(prs, agentPR(pr(i, person("dev"), -1, agentWait, agentWait+1, agentWait+2)))
		prs = append(prs, pr(100+i, person("dev2"), -1, humanWait, humanWait+1, humanWait+2))
	}
	return Compute([]model.RepoData{{Repo: "o/r", Since: since, Until: until, PRs: prs, DeploySource: "none", CISource: "none"}}, registry(t), Options{Target: "o/r", Now: until})
}

func TestConstraintAndHeadline(t *testing.T) {
	r := line(t, 6, 30, 5)
	c := r.Constraint
	if c == nil || c.Stage != StageReviewWait || c.Station != "review" {
		t.Fatalf("constraint: %+v", c)
	}
	if !c.Comparable || c.Basis != "median" {
		t.Fatalf("6 of each must be comparable on medians: %+v", c)
	}
	near(t, "ratio", c.Ratio, 6)
	near(t, "share", c.Share, (6*30+6*5)/float64(6*32+6*7))
	near(t, "the stage shares add up to one", StageShare(r.Line, StageReviewWait)+StageShare(r.Line, StageReviewCycle)+StageShare(r.Line, StageMergeWait)+StageShare(r.Line, StageDraft), 1)
	want := "Your agent pull requests wait 6 times longer for a first review than human pull requests: a median of 30 h against 5 h."
	if r.Headline != want {
		t.Errorf("headline\n got: %s\nwant: %s", r.Headline, want)
	}
	near(t, "disclosed agent share", *r.DisclosedAgentShare, 0.5)

	r = line(t, 6, 5, 30)
	if !strings.HasPrefix(r.Headline, "Your human pull requests wait 6 times longer for a first review than agent pull requests") {
		t.Errorf("headline: %s", r.Headline)
	}

	r = line(t, 6, 10, 12)
	if !strings.Contains(r.Headline, "Agent and human pull requests are close there") {
		t.Errorf("headline: %s", r.Headline)
	}

	// Too few of each to compare: the headline names the constraint alone.
	r = line(t, 4, 30, 5)
	if r.Constraint.Comparable {
		t.Error("4 of each is below MinSample")
	}
	if !strings.HasPrefix(r.Headline, "Your pull requests spend most of their time waiting for a first review: a median of") {
		t.Errorf("headline: %s", r.Headline)
	}
	found := false
	for _, f := range r.Findings {
		found = found || strings.Contains(f, "too few pull requests to compare")
	}
	if !found {
		t.Errorf("the findings do not say the sample is too small: %q", r.Findings)
	}
}

func TestOneStuckPullRequestDoesNotNameTheConstraint(t *testing.T) {
	var prs []model.PR
	for i := 0; i < 20; i++ {
		prs = append(prs, pr(i, person("dev"), -1, 4, 5, 5.5)) // 4 h waiting for review
	}
	prs = append(prs, pr(99, person("dev"), 2000, 2000.1, 2000.2, 2000.3)) // one draft open for 83 days
	r := Compute([]model.RepoData{{Repo: "o/r", Since: since.AddDate(0, 0, -100), Until: until.AddDate(0, 0, 100), PRs: prs}}, registry(t), Options{Now: until})
	if r.Line.Stages[StageDraft].Total < r.Line.Stages[StageReviewWait].Total {
		t.Fatal("the test needs the raw draft total to be the largest")
	}
	if r.Constraint.Stage != StageReviewWait {
		t.Errorf("constraint is %q, want %q", r.Constraint.Stage, StageReviewWait)
	}
}

func TestCompare(t *testing.T) {
	five := func(v float64) Dist { return dist([]float64{v, v, v, v, v}) }
	if ok, _, _, _, _ := compare(dist([]float64{1, 1, 1, 1}), five(2)); ok {
		t.Error("4 pull requests is too few")
	}
	ok, a, h, ratio, basis := compare(five(3), five(2))
	if !ok || a != 3 || h != 2 || ratio != 1.5 || basis != "median" {
		t.Errorf("got %v %v %v %v %q", ok, a, h, ratio, basis)
	}
	// A zero median switches both sides to the mean.
	ok, a, h, _, basis = compare(dist([]float64{0, 0, 0, 5, 5}), five(4))
	if !ok || basis != "mean" || a != 2 || h != 4 {
		t.Errorf("got %v %v %v %q", ok, a, h, basis)
	}
	if ok, _, _, _, _ := compare(five(0), five(4)); ok {
		t.Error("nothing to compare when one side is all zero")
	}
}

func TestClassesAndRates(t *testing.T) {
	bot := model.Actor{Login: "dependabot[bot]", Type: "Bot"}
	closed := pr(7, person("dev"), -1, -1, -1, 3)
	closed.State, closed.MergedAt = "CLOSED", nil
	open := pr(8, person("dev3"), -1, -1, -1, 3)
	open.State, open.MergedAt, open.ClosedAt = "OPEN", nil, nil
	draft := open
	draft.Number, draft.IsDraft = 9, true
	prs := []model.PR{
		agentPR(pr(1, person("dev"), -1, 2, 3, 4)),
		pr(2, person("dev"), -1, 2, 3, 4),
		pr(3, person("Dev2"), -1, -1, -1, 4),
		pr(4, bot, -1, -1, -1, 1),
		closed, open, draft,
	}
	prs[1].Reviews[0].State = "CHANGES_REQUESTED"
	rd := model.RepoData{Repo: "o/r", Since: since, Until: until, PRs: prs, DeploySource: "releases", CISource: "none",
		Deploys: []model.Deploy{{At: at(1)}, {At: at(2)}, {At: at(3)}, {At: at(4)}}}
	r := Compute([]model.RepoData{rd}, registry(t), Options{Now: until, Detail: true})
	if r.Agent.Merged != 1 || r.Human.Merged != 2 || r.Automation.Merged != 1 || r.All.Merged != 4 || r.Line.Merged != 3 {
		t.Errorf("merged: agent=%d human=%d automation=%d all=%d line=%d", r.Agent.Merged, r.Human.Merged, r.Automation.Merged, r.All.Merged, r.Line.Merged)
	}
	near(t, "disclosed agent share leaves automation out", *r.DisclosedAgentShare, 1.0/3)
	near(t, "human merge rate", *r.Human.MergeRate, 2.0/3)
	near(t, "human merged per week", r.Human.MergedPerWeek, 0.5)
	near(t, "human reviewed share", *r.Human.HumanReviewedShare, 0.5)
	near(t, "human rework is over reviewed pull requests", *r.Human.ReworkRate, 1)
	near(t, "agent rework", *r.Agent.ReworkRate, 0)
	if r.Automation.ReworkRate != nil {
		t.Error("no reviewed automation pull request, so no rework rate")
	}
	if r.WaitingForReview != 1 {
		t.Errorf("waiting for review now: got %d, want 1 (the draft does not wait)", r.WaitingForReview)
	}
	near(t, "oldest waiting", r.OldestWaitingH, until.Sub(t0).Hours())
	near(t, "deploys per week", *r.DeploysPerWeek, 1)
	if len(r.PRs) != len(prs) {
		t.Errorf("detail rows: got %d, want %d", len(r.PRs), len(prs))
	}
	if len(r.Agents) != 1 || r.Agents[0].ID != "claude-code" || r.Agents[0].Medium != 1 || r.Agents[0].High != 0 {
		t.Errorf("agents: %+v", r.Agents)
	}
	// One agent and two human pull requests: the report must warn that both
	// columns are thin.
	all := strings.Join(r.Findings, "\n")
	for _, want := range []string{"Only 1 agent pull request merged. Read the agent column", "Only 2 human pull requests merged. Read the human column"} {
		if !strings.Contains(all, want) {
			t.Errorf("no small-sample warning %q in:\n%s", want, all)
		}
	}

	lo := r.LightsOut
	near(t, "devs counts people, any case, no bots", *lo.Devs, 2)
	near(t, "prw is clamped up to the form minimum", *lo.PRW, 1)
	near(t, "revH is clamped up to the form minimum", *lo.RevH, 2)
	near(t, "dpw", *lo.DPW, 1)
	if lo.AI != "agents" || lo.CIMin != nil || lo.CIRuns != nil || lo.Rework != nil {
		t.Errorf("lights out: %+v", lo)
	}
	u, err := url.Parse(lo.URL)
	if err != nil || !strings.HasPrefix(lo.URL, LightsOutURL+"?utm_source=factorymeter&utm_medium=report#") {
		t.Fatalf("url: %q %v", lo.URL, err)
	}
	// The numbers ride in the fragment, which a browser never sends to a
	// server. The query only names the source of the visit.
	if u.RawQuery != "utm_source=factorymeter&utm_medium=report" {
		t.Errorf("the query must hold no measured number: %q", u.RawQuery)
	}
	q, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("devs") != "2" || q.Get("prw") != "1" || q.Get("revH") != "2" || q.Get("dpw") != "1" || q.Get("ai") != "agents" || q.Has("ciMin") || q.Has("rework") {
		t.Errorf("fragment: %v", q)
	}
	if strings.Join(lo.NotMeasured, ",") != "envs,pms,ciMin" {
		t.Errorf("not measured: %v", lo.NotMeasured)
	}
}

func TestEmptyWindow(t *testing.T) {
	r := Compute([]model.RepoData{{Repo: "o/r", Since: since, Until: until}}, registry(t), Options{Now: until})
	if r.Constraint != nil || r.DisclosedAgentShare != nil {
		t.Errorf("an empty window has no constraint and no share: %+v", r.Constraint)
	}
	if !strings.Contains(r.Headline, "no constraint to name") || len(r.Findings) != 1 {
		t.Errorf("headline %q findings %q", r.Headline, r.Findings)
	}
	if r.LightsOut.AI != "auto" || !strings.HasSuffix(r.LightsOut.URL, "?utm_source=factorymeter&utm_medium=report#ai=auto") {
		t.Errorf("lights out: %+v", r.LightsOut)
	}
	if got := Compute(nil, registry(t), Options{}); got.Constraint != nil {
		t.Error("no repositories, no constraint")
	}
}

func TestWidestGapIgnoresShortWaits(t *testing.T) {
	// The constraint is the review wait. Away from it, agents spend 10 times
	// longer than humans waiting to merge, but that is 12 minutes against 1.
	var prs []model.PR
	for i := 0; i < 6; i++ {
		prs = append(prs, agentPR(pr(i, person("dev"), -1, 20, 20, 20.2)))
		prs = append(prs, pr(100+i, person("dev2"), -1, 20, 20, 20.02))
	}
	r := Compute([]model.RepoData{{Repo: "o/r", Since: since, Until: until, PRs: prs}}, registry(t), Options{Now: until})
	if g := widestGap(&r); g != "" {
		t.Errorf("a gap of minutes was reported: %s", g)
	}
	for i := range prs {
		if prs[i].Number < 100 {
			*prs[i].MergedAt = at(60)
		} else {
			*prs[i].MergedAt = at(24)
		}
	}
	r = Compute([]model.RepoData{{Repo: "o/r", Since: since, Until: until, PRs: prs}}, registry(t), Options{Now: until})
	if r.Constraint.Stage != StageMergeWait {
		t.Fatalf("constraint: %s", r.Constraint.Stage)
	}
	if !strings.HasPrefix(r.Headline, "Your agent pull requests wait 10 times longer to merge after approval than human pull requests") {
		t.Errorf("headline: %s", r.Headline)
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[float64]string{0.001: "under a minute", 0.5: "30 min", 1.04: "1 h", 2.56: "2.6 h", 30: "30 h", 72: "3 days", 240: "10 days"} {
		if got := Hours(in); got != want {
			t.Errorf("Hours(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{0.5: "30 s", 4.24: "4.2 min", 45: "45 min", 150: "2.5 h"} {
		if got := Minutes(in); got != want {
			t.Errorf("Minutes(%v) = %q, want %q", in, got, want)
		}
	}
	if Percent(nil) != "n/a" || Percent(ptr(0.126)) != "13%" || Times(2.34) != "2.3 times" || Times(24) != "24 times" {
		t.Error("Percent or Times formatting changed")
	}
}

// The owner's rule for this project: no long dashes in anything it prints.
func TestNoLongDashesInText(t *testing.T) {
	r := line(t, 6, 30, 5)
	all := []string{r.Headline}
	all = append(all, r.Findings...)
	all = append(all, r.Limits...)
	for _, s := range Stages {
		all = append(all, s.Name, s.What, s.where, s.object)
	}
	for _, s := range all {
		if strings.ContainsAny(s, signature.LongDashes) {
			t.Errorf("long dash in %q", s)
		}
	}
}

func TestDeploySourceWords(t *testing.T) {
	for in, want := range map[string]string{
		"releases":                      "published releases",
		"deployments:production":        "deployments to the production environment",
		"deployments:prod-eu, releases": "deployments to the prod-eu environment and published releases",
		"something new":                 "something new",
	} {
		if got := deploySourceWords(in); got != want {
			t.Errorf("deploySourceWords(%q) = %q, want %q", in, got, want)
		}
	}
}
