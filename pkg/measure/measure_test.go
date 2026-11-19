package measure

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// Every expected number in this file is worked out by hand in the comment
// next to it. None is copied from what the code printed.

const repo = "acme/app"

// end is where the windows of the tests end.
var end = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

const h = time.Hour

func registry(t *testing.T) *signature.Registry {
	t.Helper()
	reg, err := signature.Default()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func commit(sha string, at time.Time) record.Commit {
	return record.Commit{SHA: sha, AuthoredAt: at, CommittedAt: at, Parents: 1,
		Authors: []record.CommitAuthor{{Login: "dev", Email: "dev@example.invalid", Name: "Dev"}}}
}

type opt func(*record.Change)

// change is a change a person opened ready for review, with one commit
// written at the same moment, 10 lines added and 5 deleted.
func change(n int, created time.Time, opts ...opt) record.Change {
	ready := created
	c := record.Change{Source: record.GitHub, ID: record.ChangeID(record.GitHub, repo, n), Repo: repo, Number: n,
		URL: fmt.Sprintf("https://example.invalid/%d", n), Title: fmt.Sprintf("Change %d", n),
		Author: record.Actor{Login: "dev", Kind: record.ActorUser}, HeadRef: fmt.Sprintf("feat/%d", n), BaseRef: "main",
		State: record.ChangeOpen, CreatedAt: created, UpdatedAt: created, ReadyAt: &ready, Additions: 10, Deletions: 5, CommitCount: 1,
		Commits: []record.Commit{commit(fmt.Sprintf("sha%d", n), created)}}
	for _, o := range opts {
		o(&c)
	}
	return c
}

func review(state string, at time.Time) opt {
	return func(c *record.Change) {
		c.Reviews = append(c.Reviews, record.Review{Author: record.Actor{Login: "rev", Kind: record.ActorUser}, State: state, SubmittedAt: at})
	}
}

func merged(at time.Time) opt {
	return func(c *record.Change) { c.State, c.MergedAt, c.ClosedAt = record.ChangeMerged, &at, &at }
}

func closed(at time.Time) opt {
	return func(c *record.Change) { c.State, c.ClosedAt = record.ChangeClosed, &at }
}

func draft(c *record.Change) { c.Draft, c.ReadyAt = true, nil }

// fromAgent adds the trailer Claude Code writes by default.
func fromAgent(c *record.Change) {
	c.Commits[0].Trailers = []record.Trailer{{Key: "Co-Authored-By", Value: "Claude <noreply@anthropic.com>"}}
}

func fromBot(c *record.Change) {
	c.Author = record.Actor{Login: "dependabot[bot]", Kind: record.ActorBot}
}

func run(sha string, attempt int, created, started, updated time.Time, conclusion string) record.CIRun {
	return record.CIRun{Source: record.GitHub, Repo: repo, Workflow: "ci", Event: "pull_request", SHA: sha, Attempt: attempt,
		Status: "completed", Conclusion: conclusion, CreatedAt: created, StartedAt: started, UpdatedAt: updated}
}

func deployment(env, ref, sha string, production bool, created time.Time, took time.Duration, outcome string) record.Deploy {
	done := created.Add(took)
	return record.Deploy{Source: record.GitHub, Repo: repo, Via: "deployment", Environment: env, Production: production,
		Ref: ref, SHA: sha, CreatedAt: created, FinishedAt: &done, Outcome: outcome}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func vals(list []obs) []float64 {
	out := make([]float64, len(list))
	for i := range list {
		out[i] = list[i].v
	}
	return out
}

func same(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !near(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestStationsOfOneChange walks one change down the whole line and checks
// what each station wrote down about it.
//
// Hours after t0:
//
//	 0     first commit a1
//	 1     opened as a draft; CI run on a1 created
//	 1:10  the run starts
//	 1:40  the run passes
//	 2     a1 deployed to a preview environment, up 15 minutes later
//	 3     ready for review
//	 5     first review, a comment
//	 6     commit a2; CI run on a2 created and started
//	 6:30  the run fails
//	 7     the run is started again
//	 7:30  the second attempt passes
//	 9     approved
//	10     merged
//	20     production deploy starts, done 30 minutes later
//
// The ticket was created 48 hours before t0 and is linked from the change.
func TestStationsOfOneChange(t *testing.T) {
	t0 := end.Add(-3 * day)
	at := func(hours float64) time.Time { return t0.Add(time.Duration(hours * float64(h))) }

	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	c := change(1, at(1), draft, review(record.ReviewCommented, at(5)), review(record.ReviewApproved, at(9)), merged(at(10)))
	ready := at(3)
	c.ReadyAt = &ready
	c.Commits = []record.Commit{commit("a1", at(0)), commit("a2", at(6))}
	c.CommitCount = 2
	rb.Change(&c)
	for _, r := range []record.CIRun{
		run("a1", 1, at(1), at(1+10.0/60), at(1+40.0/60), "success"),
		run("a2", 1, at(6), at(6), at(6.5), "failure"),
		run("a2", 2, at(6), at(7), at(7.5), "success"),
		run("zz", 1, at(11), at(11), at(11.5), "success"), // on the main branch: joins no change
		run("a2", 1, at(6), at(6), at(6.1), "skipped"),    // never ran the checks
	} {
		rb.CIRun(&r)
	}
	for _, d := range []record.Deploy{
		deployment("preview", "feat/1", "a1", false, at(2), 15*time.Minute, record.DeploySuccess),
		deployment("preview", "feat/1", "a2", false, at(12), 15*time.Minute, record.DeploySuccess),  // after the merge
		deployment("github-pages", "feat/1", "a1", false, at(2), time.Minute, record.DeploySuccess), // a published site
		deployment("staging", "main", "a1", false, at(2), time.Minute, record.DeploySuccess),        // the branch it merges into
		deployment("production", "main", "zz", true, at(20), 30*time.Minute, record.DeploySuccess),
	} {
		rb.Deploy(&d)
	}
	tid := record.TicketID(record.GitHub, repo+"#7")
	rb.Link(&record.Link{Kind: record.LinkCloses, From: c.ID, FromKind: record.KindChange, To: tid, ToKind: record.KindTicket})
	rb.Done()
	done := at(11)
	b.Ticket(&record.Ticket{Source: record.GitHub, ID: tid, Key: repo + "#7", Project: repo, Title: "A ticket",
		State: record.TicketDone, CreatedAt: at(-48), DoneAt: &done, ClosedAt: &done})
	l := b.Line()

	want := map[string][]float64{
		// Spec: the ticket never moved to a started state, so work starts
		// when the linked change opens, 48 + 1 hours after the ticket.
		"spec.flow": {1}, "spec.wait": {49}, "spec.rework": {0},
		// Code: 3 hours from the first commit to ready. The CI run on a1 is
		// in the CI station from 1:00 to 1:40, so code holds 3h - 40m.
		"code.flow": {1}, "code.work": {3}, "code.held": {3 - 40.0/60}, "code.rework": {0},
		// CI, round a1: waited 10 minutes for a runner, ran 30, held 40.
		// Round a2: no wait, ran 6:00-6:30 and 7:00-7:30, failed once.
		"ci.flow": {1, 1}, "ci.wait": {10.0 / 60, 0}, "ci.work": {0.5, 1}, "ci.rework": {0, 1}, "ci.held": {40.0 / 60, 1},
		// Verify: the preview came 2 hours after the commit and took 15
		// minutes. The other three deployments are not proofs.
		"verify.flow": {1}, "verify.wait": {2}, "verify.work": {0.25}, "verify.held": {2.25}, "verify.rework": {0},
		// Review: ready at 3, first review at 5, approved at 9. Commit a2
		// came after the first review, so it was sent back. Held is 3 to 9
		// less the hour CI was busy with a2.
		"review.wait": {2}, "review.flow": {1}, "review.work": {4}, "review.rework": {1}, "review.held": {5}, "review.skipped": {0},
		// Merge: approved at 9, merged at 10.
		"merge.flow": {1}, "merge.wait": {1}, "merge.rework": {0}, "merge.held": {1},
		// Release: merged at 10, the deploy starts at 20 and is done at 20:30.
		"release.flow": {1}, "release.wait": {10}, "release.held": {10.5}, "release.deploys": {1}, "release.work": {0.5}, "release.rework": {0},
		// The line: opened at 1, merged at 10, in production 20.5 hours
		// after the first commit. 10 + 5 lines.
		"line.flow": {1}, "line.lead": {9}, "line.delivery": {20.5}, "line.size": {15}, "line.agent_share": {0},
	}
	for stream, w := range want {
		if got := vals(l.obs[stream]); !same(got, w) {
			t.Errorf("%s = %v, want %v", stream, got, w)
		}
	}
	for stream := range l.obs {
		if _, ok := want[stream]; !ok {
			t.Errorf("stream %s is written and not checked", stream)
		}
	}

	stays := map[string][2]float64{
		"spec.queue": {-48, 1}, "code.queue": {1, 3}, "review.queue": {3, 5}, "merge.queue": {9, 10}, "release.queue": {10, 20.5},
	}
	for queue, w := range stays {
		got := l.stays[queue]
		if len(got) != 1 || !got[0].from.Equal(at(w[0])) || !got[0].to.Equal(at(w[1])) {
			t.Errorf("%s = %+v, want one stay from %v to %v", queue, got, w[0], w[1])
		}
	}
	if len(l.stays) != len(stays) {
		t.Errorf("queues written: %d, checked: %d", len(l.stays), len(stays))
	}
	if r := l.repos[0]; r.verifyJoined != 1 || r.ciRuns != 5 || r.ciJoined != 4 || !r.ciWait || r.prodDeploys != 1 || r.viaRelease {
		t.Errorf("repository: %+v", r)
	}

	// Every number of the registry has a way to be added up and a stream
	// this one change wrote to. A metric added to the registry without a
	// line in the builder fails here.
	for _, m := range line.Registry {
		c, ok := calcOf(m)
		if !ok {
			t.Errorf("%s: no way to add it up", m.ID)
			continue
		}
		n := len(l.obs[c.stream])
		if c.how == howCount || c.how == howOldest {
			n = len(l.stays[c.stream])
		}
		if n == 0 {
			t.Errorf("%s reads the stream %s, and nothing writes it", m.ID, c.stream)
		}
	}
}

func TestRevertAndAbandon(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	t0 := end.Add(-2 * day)
	one := change(1, t0, merged(t0.Add(h)))
	two := change(2, t0.Add(2*h), merged(t0.Add(3*h)))
	two.Body = "Reverts acme/app#1\n\nIt broke the build."
	other := change(3, t0.Add(4*h), merged(t0.Add(5*h)))
	other.Body = "Reverts other/repo#2" // another repository's change
	gone := change(4, t0.Add(6*h), closed(t0.Add(7*h)))
	for _, c := range []record.Change{one, two, other, gone} {
		rb.Change(&c)
	}
	rb.Done()
	l := b.Line()
	// Change 1 was reverted. Changes 2 and 3 were not.
	if got := vals(l.obs["merge.rework"]); !same(got, []float64{1, 0, 0}) {
		t.Errorf("merge.rework = %v", got)
	}
	// Three merged, one closed without a merge.
	if got := vals(l.obs["code.rework"]); !same(got, []float64{0, 0, 0, 1}) {
		t.Errorf("code.rework = %v", got)
	}
	// Nobody else reviewed any of the three.
	if got := vals(l.obs["review.skipped"]); !same(got, []float64{1, 1, 1}) {
		t.Errorf("review.skipped = %v", got)
	}
}

// scenario is four weeks of one repository.
//
// Week 1: five changes by people, each approved 1 hour after it opened and
// merged 1 hour after that.
// Week 2: nothing.
// Week 3: two changes by people, approved after 1 and 2 hours, merged 2
// hours later.
// Week 4: two by people, approved after 3 and 4 hours and merged 2 hours
// later. Two by an agent, approved after 5 and 6 hours and merged 2 and 22
// hours later. One by a bot, approved and merged an hour apart.
// Open at the end: one change ready and unreviewed for 30 hours, and one
// draft opened 50 hours ago.
func scenario(t *testing.T) *Line {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
	n := 0
	add := func(created time.Time, wait, merge time.Duration, opts ...opt) {
		n++
		ok := created.Add(wait)
		c := change(n, created, append([]opt{review(record.ReviewApproved, ok), merged(ok.Add(merge))}, opts...)...)
		rb.Change(&c)
	}
	for range 5 {
		add(end.Add(-25*day), h, h)
	}
	add(end.Add(-10*day), 1*h, 2*h)
	add(end.Add(-10*day), 2*h, 2*h)
	add(end.Add(-5*day), 3*h, 2*h)
	add(end.Add(-5*day), 4*h, 2*h)
	add(end.Add(-5*day), 5*h, 2*h, fromAgent)
	add(end.Add(-5*day), 6*h, 22*h, fromAgent)
	add(end.Add(-5*day), h, h, fromBot)
	waiting := change(13, end.Add(-30*h))
	rb.Change(&waiting)
	drafted := change(14, end.Add(-50*h), draft)
	rb.Change(&drafted)
	rb.Done()
	return b.Line()
}

func find(t *testing.T, r *contract.Report, metric string, sl contract.Slice) contract.Series {
	t.Helper()
	for _, s := range r.Metrics {
		if s.Metric == metric && s.Slice == sl {
			return s
		}
	}
	t.Fatalf("no series %s %+v", metric, sl)
	return contract.Series{}
}

func num(v *float64) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprint(*v)
}

func TestReport(t *testing.T) {
	l := scenario(t)
	now := end.Add(h)
	r, err := l.Report(Options{Target: repo, Weeks: 2, History: 4, Now: now, Version: "test", Registry: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != contract.Version || !r.GeneratedAt.Equal(now) || r.Target != repo {
		t.Errorf("head: %+v", r)
	}
	w := r.Window
	if !w.Since.Equal(end.Add(-2*week)) || !w.Until.Equal(end) || len(w.Periods) != 4 || w.Previous == nil ||
		!w.Previous.From.Equal(end.Add(-4*week)) || !w.Periods[0].From.Equal(end.Add(-4*week)) || !w.Periods[3].To.Equal(end) {
		t.Errorf("window: %+v", w)
	}

	whole := []struct {
		metric          string
		value, previous string
		n, previousN    int
		change, trend   string
		thin            bool
		points          string
	}{
		// Six changes by agents and people merged in two weeks, five in
		// the two weeks before. The bot's change is left out.
		{"line.flow", "3", "2.5", 6, 5, "0.2", "better", false, "5 0 2 4"},
		// Opened to merged: 3 4 5 6 7 28 hours. The median is 5.5. Before,
		// every change took 2.
		{"line.lead.p50", "5.5", "2", 6, 5, "1.75", "worse", false, "2 nil 3.5 6.5"},
		// Two of the six carry the agent's trailer. The share has no
		// better direction.
		{"line.agent_share", "0.3333", "0", 6, 5, "0.3333", "unknown", false, "0 nil 0 0.5"},
		{"line.size.p50", "15", "15", 6, 5, "0", "flat", false, "15 nil 15 15"},
		// Waits for the first review: 1 2 3 4 5 6 hours. Median 3.5. The
		// 90th percentile is halfway between 5 and 6.
		{"review.wait.p50", "3.5", "1", 6, 5, "2.5", "worse", false, "1 nil 1.5 4.5"},
		{"review.wait.p90", "5.5", "1", 6, 5, "4.5", "worse", false, "1 nil 1.9 5.7"},
		{"review.flow", "3", "2.5", 6, 5, "0.2", "better", false, "5 0 2 4"},
		{"review.rework", "0", "0", 6, 5, "0", "flat", false, "0 nil 0 0"},
		{"review.skipped", "0", "0", 6, 5, "0", "flat", false, "0 nil 0 0"},
		// One change stands unreviewed at the end, for 30 hours. A week
		// earlier and before, none stood.
		{"review.queue", "1", "0", 1, 0, "nil", "flat", false, "0 0 0 1"},
		{"review.queue.oldest", "30", "nil", 1, 0, "nil", "unknown", false, "nil nil nil 30"},
		// Approval to merge: 2 2 2 2 2 22 hours. The 90th percentile is
		// halfway between 2 and 22.
		{"merge.wait.p50", "2", "1", 6, 5, "1", "worse", false, "1 nil 2 2"},
		{"merge.wait.p90", "12", "1", 6, 5, "11", "worse", false, "1 nil 2 16"},
		{"merge.queue", "0", "0", 0, 0, "nil", "flat", false, "0 0 0 0"},
		// Held at review: 1 2 3 4 5 6, capped at the 90th percentile 5.5,
		// is 20.5 hours over two weeks. Before: five times 1 hour. A week
		// has its own cap: 1 and 2 capped at 1.9 is 2.9, and 3 4 5 6 capped
		// at 5.7 is 17.7.
		{"review.held.total", "10.25", "2.5", 6, 5, "3.1", "worse", false, "5 nil 2.9 17.7"},
		// Held at merge: 2 2 2 2 2 and 22 capped at 12 is 22 hours over
		// two weeks. In the last week alone the cap is 16: 2 2 2 16 is 22.
		{"merge.held.total", "11", "2.5", 6, 5, "3.4", "worse", false, "5 nil 4 22"},
		// The line held 10.25 + 11 + 0 hours a week. Review has 10.25 of
		// 21.25 and merge 11 of 21.25. Before, each had half. By week:
		// 2.9 of 6.9, then 17.7 of 39.7.
		{"review.held.share", "0.4824", "0.5", 6, 5, "-0.0176", "flat", false, "0.5 nil 0.4203 0.4458"},
		{"merge.held.share", "0.5176", "0.5", 6, 5, "0.0176", "flat", false, "0.5 nil 0.5797 0.5542"},
		// Every change opens ready with its first commit, so writing takes
		// no time. Seven were made ready: the six and the one that waits.
		{"code.flow", "3.5", "2.5", 7, 5, "0.4", "better", false, "5 0 2 5"},
		{"code.work.p50", "0", "0", 7, 5, "nil", "flat", false, "0 nil 0 0"},
		{"code.rework", "0", "0", 6, 5, "0", "flat", false, "0 nil 0 0"},
		{"code.queue", "1", "0", 1, 0, "nil", "flat", false, "0 0 0 1"},
		{"code.queue.oldest", "50", "nil", 1, 0, "nil", "unknown", false, "nil nil nil 50"},
	}
	for _, c := range whole {
		s := find(t, r, c.metric, contract.Slice{})
		var pts []string
		for _, p := range s.Points {
			pts = append(pts, num(p.Value))
		}
		got := fmt.Sprintf("%s (%d), before %s (%d), change %s, %s, thin %v, points %s",
			num(s.Value), s.N, num(s.Previous), s.PreviousN, num(s.Change), s.Trend, s.Thin, strings.Join(pts, " "))
		want := fmt.Sprintf("%s (%d), before %s (%d), change %s, %s, thin %v, points %s",
			c.value, c.n, c.previous, c.previousN, c.change, c.trend, c.thin, c.points)
		if got != want {
			t.Errorf("%s\n got %s\nwant %s", c.metric, got, want)
		}
	}

	// By class. Two changes are too few to call a median, so the value is
	// marked thin and carries no trend.
	agent, human, bot := contract.Slice{Class: "agent"}, contract.Slice{Class: "human"}, contract.Slice{Class: "automation"}
	if s := find(t, r, "line.flow", agent); num(s.Value) != "1" || s.N != 2 || s.Thin || s.Trend != contract.TrendBetter || s.Change != nil {
		t.Errorf("agent line.flow: %+v", s)
	}
	if s := find(t, r, "review.wait.p50", agent); num(s.Value) != "5.5" || s.N != 2 || !s.Thin || s.Previous != nil || s.Trend != contract.TrendUnknown {
		t.Errorf("agent review.wait.p50: %+v", s)
	}
	// People waited 1 2 3 4 hours: median 2.5 from four changes, thin.
	if s := find(t, r, "review.wait.p50", human); num(s.Value) != "2.5" || s.N != 4 || !s.Thin || num(s.Previous) != "1" || num(s.Change) != "1.5" || s.Trend != contract.TrendUnknown {
		t.Errorf("human review.wait.p50: %+v", s)
	}
	if s := find(t, r, "line.flow", bot); num(s.Value) != "0.5" || s.N != 1 {
		t.Errorf("automation line.flow: %+v", s)
	}
	if s := find(t, r, "line.flow", contract.Slice{Class: "agent", Agent: "claude-code"}); num(s.Value) != "1" || s.N != 2 {
		t.Errorf("claude-code line.flow: %+v", s)
	}

	// Stations.
	status := map[string]string{}
	for _, st := range r.Stations {
		status[st.ID] = st.Status
		switch st.ID {
		case line.Merge:
			if !st.Constraint || num(st.HeldShare) != "0.5176" {
				t.Errorf("merge: %+v", st)
			}
		case line.Review:
			if st.Constraint || len(st.Oldest) != 1 || st.Oldest[0].Key != "acme/app#13" || st.Oldest[0].Hours != 30 || st.Oldest[0].Class != "human" || st.Oldest[0].Title != "Change 13" {
				t.Errorf("review: %+v", st)
			}
		case line.Code:
			if len(st.Oldest) != 1 || st.Oldest[0].Key != "acme/app#14" || st.Oldest[0].Hours != 50 {
				t.Errorf("code: %+v", st)
			}
		}
	}
	wantStatus := map[string]string{line.Spec: "not_connected", line.Code: "measured", line.CI: "not_connected", line.Verify: "not_connected",
		line.Review: "measured", line.Merge: "measured", line.Release: "not_connected"}
	for id, w := range wantStatus {
		if status[id] != w {
			t.Errorf("status of %s = %s, want %s", id, status[id], w)
		}
	}
	for _, s := range r.Metrics {
		if m, _ := line.ByID(s.Metric); wantStatus[m.Station] == "not_connected" {
			t.Errorf("%s is reported for a station that is not connected", s.Metric)
		}
	}

	if got, want := r.Verdict.Headline, "Merge is the constraint. It holds 52 percent of the hours changes spend on the line, about the same as in the weeks before."; got != want {
		t.Errorf("headline\n got %s\nwant %s", got, want)
	}
	if got, want := r.Verdict.Detail, "Over the 2 weeks to 2026-11-02, changes stood 11 hours a week at merge and 3.0 a week left it. Review is next with 48 percent."; got != want {
		t.Errorf("detail\n got %s\nwant %s", got, want)
	}
	if r.Verdict.Constraint != line.Merge {
		t.Errorf("constraint = %q", r.Verdict.Constraint)
	}

	// Coverage: nine changes were opened in the window, the bot's included.
	if len(r.Coverage.Repos) != 1 || r.Coverage.Repos[0].Repo != repo || r.Coverage.Repos[0].Changes != 14 {
		t.Errorf("repos: %+v", r.Coverage.Repos)
	}
	for _, s := range r.Coverage.Sources {
		if want := map[string]int{record.KindChange: 9}[s.Kind]; s.Records != want {
			t.Errorf("%s records in the window = %d, want %d", s.Kind, s.Records, want)
		}
	}
	limits := strings.Join(r.Limits, "\n")
	for _, want := range []string{"The agent share is a floor.", "Hours are hours on the clock.", "Writing time starts at the first commit"} {
		if !strings.Contains(limits, want) {
			t.Errorf("limits lack %q:\n%s", want, limits)
		}
	}
	for _, not := range []string{"wait for a runner", "list of the commits in each deploy"} {
		if strings.Contains(limits, not) {
			t.Errorf("limits speak of a station that is not connected: %q", not)
		}
	}
}

// A summary is what a person can hand to someone outside. It must hold
// station numbers and no name, title or link.
func TestSummaryNamesNothing(t *testing.T) {
	r, err := scenario(t).Report(Options{Target: repo, Weeks: 2, History: 4, Summary: true, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, not := range []string{"acme", "Change 1", "example.invalid", "#13"} {
		if strings.Contains(string(raw), not) {
			t.Errorf("the summary holds %q", not)
		}
	}
	if !r.Summary || r.Target != "" || len(r.Coverage.Repos) != 0 {
		t.Errorf("summary head: %+v", r)
	}
	// The numbers are the same as in the full report.
	if s := find(t, r, "merge.held.share", contract.Slice{}); num(s.Value) != "0.5176" {
		t.Errorf("merge.held.share = %s", num(s.Value))
	}
}

func TestTooFewToNameAConstraint(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	for i := 1; i <= 4; i++ {
		c := change(i, end.Add(-3*day), merged(end.Add(-3*day+h)))
		rb.Change(&c)
	}
	rb.Done()
	r, err := b.Line().Report(Options{Target: repo, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict.Constraint != "" || r.Verdict.Headline != "Too few changes merged to name a constraint." ||
		r.Verdict.Detail != "4 changes by agents and people merged in the 13 weeks to 2026-11-02. A constraint is named from 5." {
		t.Errorf("verdict: %+v", r.Verdict)
	}
	for _, st := range r.Stations {
		if st.Constraint {
			t.Errorf("%s is marked as the constraint", st.ID)
		}
	}
}

func TestEmptyLine(t *testing.T) {
	if _, err := NewBuilder(registry(t)).Line().Report(Options{Now: end}); err == nil || !strings.Contains(err.Error(), "factorymeter sync") {
		t.Errorf("err = %v", err)
	}
}

func TestWindow(t *testing.T) {
	line := func(from time.Time) *Line {
		return &Line{repos: []repoInfo{{name: repo, from: from, synced: end}}}
	}
	// A store filled 26 weeks back, on the afternoon of that day, gives 26
	// periods, a 13 week window and the 13 weeks before it.
	w, limits := line(end.Add(-26*week + 15*h)).window(Options{Weeks: 13, History: 26})
	if w.weeks != 13 || len(w.periods) != 26 || w.prev == nil || !w.prev.From.Equal(end.Add(-26*week)) || !w.back.Equal(end.Add(-26*week)) || len(limits) != 0 {
		t.Errorf("26 weeks: %+v %v", w, limits)
	}
	// A store that holds three weeks cannot give thirteen.
	w, limits = line(end.Add(-3 * week)).window(Options{Weeks: 13, History: 26})
	if w.weeks != 3 || len(w.periods) != 3 || w.prev != nil || !w.since.Equal(end.Add(-3*week)) || len(limits) != 2 ||
		limits[0] != "The store holds 3 weeks, so the window is 3 weeks and not 13 weeks. Sync with a longer --since to go further back." ||
		limits[1] != "The store does not reach back to the 3 weeks before the window, so nothing is compared with them." {
		t.Errorf("3 weeks: %+v %v", w, limits)
	}
	// Thirteen weeks held: the window fits, the weeks before do not.
	w, limits = line(end.Add(-13 * week)).window(Options{Weeks: 13, History: 26})
	if w.weeks != 13 || len(w.periods) != 13 || w.prev != nil || len(limits) != 1 {
		t.Errorf("13 weeks: %+v %v", w, limits)
	}
	// --until moves the end, and the periods with it.
	w, _ = line(end.Add(-26 * week)).window(Options{Weeks: 4, History: 8, Until: end.Add(-week)})
	if !w.until.Equal(end.Add(-week)) || !w.since.Equal(end.Add(-5*week)) || !w.periods[7].To.Equal(end.Add(-week)) || w.prev == nil {
		t.Errorf("until: %+v", w)
	}
}

func TestMove(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	cases := []struct {
		metric    string
		cur, prev *float64
		thin      bool
		change    string
		trend     string
	}{
		// Hours, lower is better.
		{"review.wait.p50", p(2), p(1), false, "1", "worse"},
		{"review.wait.p50", p(1), p(2), false, "-0.5", "better"},
		{"review.wait.p50", p(1.05), p(1), false, "0.05", "flat"},       // under ten percent
		{"review.wait.p50", p(0.01), p(0.03), false, "-0.6667", "flat"}, // a minute: under three minutes
		{"review.wait.p50", p(1), p(0), false, "nil", "worse"},          // nothing to divide by
		{"review.wait.p50", p(2), p(1), true, "1", "unknown"},           // too few items
		{"review.wait.p50", nil, p(1), false, "nil", "unknown"},
		{"review.wait.p50", p(1), nil, false, "nil", "unknown"},
		// A share moves in points.
		{"review.rework", p(0.2), p(0.1), false, "0.1", "worse"},
		{"review.rework", p(0.1), p(0.09), false, "0.01", "flat"}, // under two points
		{"review.rework", p(0.05), p(0.1), false, "-0.05", "better"},
		// A queue.
		{"review.queue", p(3), p(2), false, "0.5", "flat"}, // one item more
		{"review.queue", p(10), p(5), false, "1", "worse"},
		{"review.queue", p(2), p(0), false, "nil", "worse"},
		// A flow, higher is better.
		{"line.flow", p(3), p(2.5), false, "0.2", "better"},
		{"line.flow", p(2.4), p(2.5), false, "-0.04", "flat"},
		{"line.flow", p(1), p(2), false, "-0.5", "worse"},
		// The agent share has no better direction.
		{"line.agent_share", p(0.5), p(0.2), false, "0.3", "unknown"},
		{"line.agent_share", p(0.21), p(0.2), false, "0.01", "flat"},
	}
	for _, c := range cases {
		m, ok := line.ByID(c.metric)
		if !ok {
			t.Fatalf("no metric %s", c.metric)
		}
		change, trend := move(m, c.cur, c.prev, c.thin)
		if num(change) != c.change || trend != c.trend {
			t.Errorf("%s %s after %s: change %s, %s. Want %s, %s", c.metric, num(c.cur), num(c.prev), num(change), trend, c.change, c.trend)
		}
	}
}

func TestQuantileAndHeld(t *testing.T) {
	if got := quantile([]float64{1, 2, 3, 4}, 0.5); got != 2.5 {
		t.Errorf("median of an even count = %v", got)
	}
	if got := quantile([]float64{7}, 0.9); got != 7 {
		t.Errorf("one value = %v", got)
	}
	// Ten items of 1 hour and one of 1000. The 90th percentile is 1, so
	// the long one counts as 1: 11 hours over 2 weeks.
	var list []obs
	for range 10 {
		list = append(list, obs{v: 1})
	}
	list = append(list, obs{v: 1000})
	if v, n := heldTotal(list, 2); n != 11 || !near(*v, 5.5) {
		t.Errorf("held total = %v from %d", *v, n)
	}
	// CI runs that overlap count once.
	t0 := end
	s := spans{{t0.Add(2 * h), t0.Add(4 * h)}, {t0, t0.Add(h)}, {t0.Add(3 * h), t0.Add(5 * h)}}.merge()
	if len(s) != 2 || s.total() != 4 || s.within(t0.Add(30*time.Minute), t0.Add(3*h)) != 1.5 {
		t.Errorf("spans: %v, total %v", s, s.total())
	}
}

// What the report says when a station cannot be measured, or can be measured
// in part.
func TestStatus(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	t0 := end.Add(-3 * day)
	for i := 1; i <= 6; i++ {
		c := change(i, t0, merged(t0.Add(h)))
		rb.Change(&c)
		// A run stamped as started the moment it was created.
		r := run(fmt.Sprintf("sha%d", i), 1, t0, t0, t0.Add(30*time.Minute), "success")
		rb.CIRun(&r)
		if i <= 4 {
			d := deployment("preview", c.HeadRef, fmt.Sprintf("sha%d", i), false, t0.Add(10*time.Minute), time.Minute, record.DeploySuccess)
			rb.Deploy(&d)
		}
	}
	rb.Deploy(&record.Deploy{Source: record.GitHub, Repo: repo, Via: "release", Production: true, Ref: "v1.0.0", CreatedAt: t0.Add(5 * h), Outcome: record.DeployUnknown})
	rb.Done()
	// A second repository with changes and nothing else.
	rb = b.Repo(RepoScope{Name: "acme/lib", From: end.Add(-26 * week)})
	c := change(1, t0, merged(t0.Add(h)))
	c.Repo, c.ID = "acme/lib", record.ChangeID(record.GitHub, "acme/lib", 1)
	rb.Change(&c)
	rb.Done()

	r, err := b.Line().Report(Options{Target: "acme", Now: end})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		line.Spec:    {"not_connected", "No tickets are in the store. Spec is measured from a ticket source."},
		line.Code:    {"measured", ""},
		line.CI:      {"partial", "No CI run joined a change in acme/lib."},
		line.Verify:  {"not_connected", "Fewer than 5 deployments to an environment that is not production were found on a commit of a change before it merged."},
		line.Review:  {"measured", "No change in the store was reviewed by another person. A change that waits here may be waiting to merge."},
		line.Merge:   {"measured", ""},
		line.Release: {"partial", "No production deploy is in the store for acme/lib. Published releases stand in for production deploys in acme/app."},
	}
	for _, st := range r.Stations {
		if w := want[st.ID]; st.Status != w[0] || st.Missing != w[1] {
			t.Errorf("%s: %s, %q\nwant %s, %q", st.ID, st.Status, st.Missing, w[0], w[1])
		}
	}
	// No run shows a wait for a runner, so the wait is empty, not zero.
	if s := find(t, r, "ci.wait.p50", contract.Slice{}); s.Value != nil || s.N != 0 {
		t.Errorf("ci.wait.p50 = %s from %d", num(s.Value), s.N)
	}
	if s := find(t, r, "ci.work.p50", contract.Slice{}); num(s.Value) != "0.5" || s.N != 6 {
		t.Errorf("ci.work.p50 = %s from %d", num(s.Value), s.N)
	}
	// The six changes merged an hour after t0 and the release came at 5
	// hours: each waited 4.
	if s := find(t, r, "release.wait.p50", contract.Slice{}); num(s.Value) != "4" || s.N != 6 {
		t.Errorf("release.wait.p50 = %s from %d", num(s.Value), s.N)
	}
	// With two repositories, each gets its own series.
	if s := find(t, r, "line.flow", contract.Slice{Repo: "acme/lib"}); s.N != 1 {
		t.Errorf("acme/lib line.flow: %+v", s)
	}
	limits := strings.Join(r.Limits, "\n")
	for _, w := range []string{
		"The CI runs in the store are stamped as started the moment they are created, so the wait for a runner cannot be seen. It is left empty.",
		"The first sync has not finished for acme/lib. Its numbers are not complete. Run sync again.",
		"The store has no list of the commits in each deploy.",
	} {
		if !strings.Contains(limits, w) {
			t.Errorf("limits lack %q:\n%s", w, limits)
		}
	}
	// Release tops the held hours here. The detail then says what holds
	// changes before the merge.
	if r.Verdict.Constraint != line.Release || !strings.Contains(r.Verdict.Detail, "Before the merge, ") {
		t.Errorf("verdict: %+v", r.Verdict)
	}
}
