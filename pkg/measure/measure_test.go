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

// agentReview is a review from a review bot the registry knows.
func agentReview(state string, at time.Time) opt {
	return func(c *record.Change) {
		c.Reviews = append(c.Reviews, record.Review{Author: record.Actor{Login: "gemini-code-assist[bot]", Kind: record.ActorBot}, State: state, SubmittedAt: at, Via: record.ReviewViaReview})
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

// What the spec station writes down for tickets that do not walk the plain
// path: created, started, done.
func TestTicketsAtTheSpecStation(t *testing.T) {
	at := func(days float64) time.Time { return end.Add(time.Duration(days * float64(day))) }
	ptr := func(v time.Time) *time.Time { return &v }

	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	link := func(n int, key string) {
		rb.Link(&record.Link{Kind: record.LinkCloses, From: record.ChangeID(record.GitHub, repo, n), FromKind: record.KindChange,
			To: record.TicketID(record.GitHub, key), ToKind: record.KindTicket})
	}
	// Change 1 was opened a day before the ticket that describes it.
	c := change(1, at(-5))
	rb.Change(&c)
	link(1, repo+"#13")
	// Change 2 was opened a day after its ticket, which was then dropped.
	c = change(2, at(-3))
	rb.Change(&c)
	link(2, repo+"#14")
	// A link to a ticket in a repository the store does not hold.
	link(2, "acme/lib#9")
	// Change 3 was opened after tickets 17 and 18 were closed. It names them,
	// as in "a duplicate of #18": that did not start them.
	c = change(3, at(-1))
	rb.Change(&c)
	link(3, repo+"#17")
	link(3, repo+"#18")
	rb.Done()

	ticket := func(n int, state string, created time.Time, done, closed *time.Time, reopened int) {
		key := fmt.Sprintf("%s#%d", repo, n)
		b.Ticket(&record.Ticket{Source: record.GitHub, ID: record.TicketID(record.GitHub, key), Key: key, Project: repo,
			Title: "A ticket", State: state, CreatedAt: created, DoneAt: done, ClosedAt: closed, Reopened: reopened})
	}
	ticket(11, record.TicketCancelled, at(-10), nil, ptr(at(-9)), 0)   // a duplicate nobody started
	ticket(12, record.TicketDone, at(-8), ptr(at(-6)), ptr(at(-6)), 0) // closed by hand, no pull request
	ticket(13, record.TicketUnstarted, at(-4), nil, nil, 0)            // written up after the work began
	ticket(14, record.TicketCancelled, at(-4), nil, ptr(at(-2)), 0)    // started, then dropped
	ticket(15, record.TicketUnstarted, at(-2), nil, nil, 0)            // waits
	ticket(16, record.TicketDone, at(-7), ptr(at(-1)), ptr(at(-1)), 2) // came back twice
	ticket(17, record.TicketDone, at(-5), ptr(at(-3)), ptr(at(-3)), 0) // done by hand, named later
	ticket(18, record.TicketCancelled, at(-6), nil, ptr(at(-5)), 0)    // a duplicate, named later
	l := b.Line()

	want := map[string][]float64{
		// 13 started when it was created, not before. 14 waited a day.
		"spec.flow": {1, 1}, "spec.wait": {0, 24},
		// 12, 16 and 17 were finished. 16 had been finished before.
		"spec.rework": {0, 0, 1},
	}
	for stream, w := range want {
		if got := vals(l.obs[stream]); !same(got, w) {
			t.Errorf("%s = %v, want %v", stream, got, w)
		}
	}
	// 11 and 18 never stood in the queue and 13 stood in it for no time. 12,
	// 16 and 17 left it when they were closed, never having started.
	stays := [][2]float64{{-8, -6}, {-4, -3}, {-2, 0}, {-7, -1}, {-5, -3}}
	got := l.stays["spec.queue"]
	if len(got) != len(stays) {
		t.Fatalf("spec.queue = %+v, want %d stays", got, len(stays))
	}
	for i, w := range stays {
		to := at(w[1])
		if w[1] == 0 {
			to = time.Time{} // still standing
		}
		if !got[i].from.Equal(at(w[0])) || !got[i].to.Equal(to) {
			t.Errorf("stay %d = %+v, want from %v to %v days", i, got[i], w[0], w[1])
		}
	}
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
// An earlier production deploy, 30 hours before t0, shows the release station
// was there before the change merged.
//
// The ticket was created 48 hours before t0 and is linked from the change.
func TestStationsOfOneChange(t *testing.T) {
	t0 := end.Add(-3 * day)
	at := func(hours float64) time.Time { return t0.Add(time.Duration(hours * float64(h))) }

	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	c := change(1, at(1), draft, agentReview(record.ReviewCommented, at(4)), review(record.ReviewCommented, at(5)), review(record.ReviewApproved, at(9)), merged(at(10)))
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
		deployment("production", "main", "yy", true, at(-30), 30*time.Minute, record.DeploySuccess),
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
		// Review: ready at 3, an agent reviews at 4, a person at 5, approved
		// at 9. Commit a2 came after the first review, so it was sent back,
		// and it sits between the reviews at 5 and 9: two rounds. Held is 3
		// to 9 less the hour CI was busy with a2.
		"review.wait": {1}, "review.flow": {1}, "review.work": {5}, "review.rework": {1}, "review.held": {5}, "review.skipped": {0},
		"review.rounds": {2}, "review.first_agent": {1}, "review.first_human": {2},
		"review.by_human": {1}, "review.by_agent": {1}, "review.by_both": {1},
		// Merge: approved at 9, merged at 10.
		"merge.flow": {1}, "merge.wait": {1}, "merge.rework": {0}, "merge.held": {1},
		// Release: merged at 10, the deploy starts at 20 and is done at 20:30.
		"release.flow": {1}, "release.wait": {10}, "release.held": {10.5},
		// Two production deploys, each 30 minutes, both passed.
		"release.deploys": {1, 1}, "release.work": {0.5, 0.5}, "release.rework": {0, 0},
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
		"spec.queue": {-48, 1}, "code.queue": {1, 3}, "review.queue": {3, 4}, "merge.queue": {9, 10}, "release.queue": {10, 20.5},
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
	if r := l.repos[0]; r.verifyJoined != 1 || r.ciRuns != 5 || r.ciJoined != 4 || !r.ciWait || r.prodDeploys != 2 || r.viaRelease {
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

// A change that is fixed and deployed again, round after round, such as on a
// test machine per push. Commit a1 at 0 fails its round. Commit a2 at 5 gets
// a round that never reports (the machine was replaced), then passes at 6:30,
// then passes again on a re-run at 7:30. The change merges at 10.
func TestVerifyRounds(t *testing.T) {
	t0 := end.Add(-3 * day)
	at := func(hours float64) time.Time { return t0.Add(time.Duration(hours * float64(h))) }

	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	c := change(1, at(0), merged(at(10)))
	c.Commits = []record.Commit{commit("a1", at(0)), commit("a2", at(5))}
	c.CommitCount = 2
	rb.Change(&c)
	abandoned := record.Deploy{Source: record.GitHub, Repo: repo, Via: "deployment", Environment: "spot",
		Ref: "a2", SHA: "a2", CreatedAt: at(5.5), Outcome: record.DeployUnknown}
	for _, d := range []record.Deploy{
		deployment("spot", "a1", "a1", false, at(1), 30*time.Minute, record.DeployFailure),
		abandoned,
		deployment("spot", "a2", "a2", false, at(6), 30*time.Minute, record.DeploySuccess),
		deployment("spot", "a2", "a2", false, at(7), 30*time.Minute, record.DeploySuccess),
	} {
		rb.Deploy(&d)
	}
	rb.Done()
	l := b.Line()

	want := map[string][]float64{
		// The change leaves verify once, at its first pass at 6:30.
		"verify.flow": {1},
		// Every round waited from its commit: 1, 0.5, 1 and 2 hours.
		"verify.wait": {1, 0.5, 1, 2},
		// The two passes took 30 minutes each, and held the change from a2.
		"verify.work": {0.5, 0.5}, "verify.held": {1.5, 2.5},
		// The failure is rework. The round with no result is not counted.
		"verify.rework": {1, 0, 0},
	}
	for stream, w := range want {
		if got := vals(l.obs[stream]); !same(got, w) {
			t.Errorf("%s = %v, want %v", stream, got, w)
		}
	}
	if got := l.obs["verify.flow"]; len(got) == 1 && !got[0].at.Equal(at(6.5)) {
		t.Errorf("verified at %v, want %v", got[0].at, at(6.5))
	}
	if r := l.repos[0]; r.verifyJoined != 4 {
		t.Errorf("deployments joined: %d, want 4", r.verifyJoined)
	}
}

// The release station is measured from the first production deploy in the
// store. Before it, the store cannot tell a line that did not release from
// one whose releases were not recorded yet.
//
// Change 1 merges an hour after t0. The first production deploy is at 10
// hours. Change 2 merges at 12 hours and the next deploy starts at 15 hours
// and takes 30 minutes.
func TestReleaseIsMeasuredFromItsFirstDeploy(t *testing.T) {
	t0 := end.Add(-3 * day)
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	one := change(1, t0, merged(t0.Add(h)))
	two := change(2, t0.Add(11*h), merged(t0.Add(12*h)))
	rb.Change(&one)
	rb.Change(&two)
	for _, d := range []record.Deploy{
		deployment("production", "main", "p1", true, t0.Add(10*h), 30*time.Minute, record.DeploySuccess),
		deployment("production", "main", "p2", true, t0.Add(15*h), 30*time.Minute, record.DeploySuccess),
	} {
		rb.Deploy(&d)
	}
	rb.Done()
	l := b.Line()
	// Only change 2 is counted: it waited 3 hours and was held 3.5. Change
	// 1 is not taken as shipped by the first deploy, 9 hours after it merged.
	for stream, want := range map[string][]float64{
		"release.flow": {1}, "release.wait": {3}, "release.held": {3.5}, "line.delivery": {4.5}, unseenAtRelease: {1}, "release.deploys": {1, 1},
	} {
		if got := vals(l.obs[stream]); !same(got, want) {
			t.Errorf("%s = %v, want %v", stream, got, want)
		}
	}
	if got := l.stays["release.queue"]; len(got) != 1 || !got[0].from.Equal(t0.Add(12*h)) {
		t.Errorf("release.queue = %+v, want only change 2", got)
	}
	r, err := l.Report(Options{Target: repo, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	want := "1 change merged in the window before the first production deploy in the store, and is not counted at release. " +
		"The store cannot tell a line that did not release from one whose releases were not recorded yet."
	if !strings.Contains(strings.Join(r.Limits, "\n"), want) {
		t.Errorf("limits lack %q:\n%s", want, strings.Join(r.Limits, "\n"))
	}
	// No metric reads the stream of what was left out.
	for _, m := range line.Registry {
		if c, ok := calcOf(m); ok && c.stream == unseenAtRelease {
			t.Errorf("%s reads %s", m.ID, unseenAtRelease)
		}
	}
}

// mergedAs gives a merged change the commit its merge left on the branch.
func mergedAs(sha string, at time.Time) opt {
	return func(c *record.Change) {
		merged(at)(c)
		c.MergeSHA = sha
	}
}

func into(base string) opt { return func(c *record.Change) { c.BaseRef = base } }

// A deploy of the commit a merge left holds what merged into the branch up
// to that merge, and nothing that merged later.
//
// Hours after t0:
//
//	 1  change 1 merges into main as m1
//	 2  deploy of m1                       (the first deploy in the store)
//	 3  change 2 merges into main as m2
//	 4  change 3 merges into main as m3
//	 5  change 4 merges into main as m4
//	 6  deploy of m3: holds 2 and 3, not 4
//	 7  deploy of m2 again, a rollback: holds nothing new
//	 8  change 5 merges into the branch feat, which is never deployed
//	 9  change 6 merges feat into main as m6
//	10  deploy of m6: holds 4, 5 and 6
//	11  change 7 merges into main as m7
//	12  deploy of a commit that is no merge: by its time it holds 7
//	13  change 8 merges into main as m8, and nothing deploys after it
func TestADeployHoldsWhatMergedUpToItsCommit(t *testing.T) {
	t0 := end.Add(-3 * day)
	at := func(hours int) time.Time { return t0.Add(time.Duration(hours) * h) }
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	for _, c := range []record.Change{
		change(1, t0, mergedAs("m1", at(1))),
		change(2, at(2), mergedAs("m2", at(3))),
		change(3, at(3), mergedAs("m3", at(4))),
		change(4, at(4), mergedAs("m4", at(5))),
		change(5, at(7), mergedAs("m5", at(8)), into("feat")),
		change(6, at(8), mergedAs("m6", at(9))),
		change(7, at(10), mergedAs("m7", at(11))),
		change(8, at(12), mergedAs("m8", at(13))),
	} {
		rb.Change(&c)
	}
	for _, d := range []record.Deploy{
		deployment("production", "main", "m1", true, at(2), 0, record.DeploySuccess),
		deployment("production", "main", "m3", true, at(6), 0, record.DeploySuccess),
		deployment("production", "main", "m2", true, at(7), 0, record.DeploySuccess),
		deployment("production", "main", "m6", true, at(10), 0, record.DeploySuccess),
		deployment("production", "main", "pushed", true, at(12), 0, record.DeploySuccess),
	} {
		rb.Deploy(&d)
	}
	rb.Done()
	l := b.Line()

	// The wait of each change, in the order its deploy came. Change 1 merged
	// before the first deploy in the store and is not counted.
	//   2: 3 to 6    3: 4 to 6    4: 5 to 10    5: 8 to 10    6: 9 to 10    7: 11 to 12
	if got, want := vals(l.obs["release.wait"]), []float64{3, 2, 5, 2, 1, 1}; !same(got, want) {
		t.Errorf("release.wait = %v, want %v", got, want)
	}
	if got := vals(l.obs[unseenAtRelease]); len(got) != 1 {
		t.Errorf("unseen at release = %v, want change 1", got)
	}
	// Change 8 still stands in the queue. Change 4 stood from 5 to 10: the
	// two deploys in between did not take it out.
	var open, four int
	for _, s := range l.stays["release.queue"] {
		if s.to.IsZero() {
			open++
		}
		if s.from.Equal(at(5)) && s.to.Equal(at(10)) {
			four++
		}
	}
	if len(l.stays["release.queue"]) != 7 || open != 1 || four != 1 {
		t.Errorf("release.queue = %+v", l.stays["release.queue"])
	}

	r, err := l.Report(Options{Target: repo, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	want := "4 of the 5 production deploys in the store that did not fail are of the commit a merge left, and each holds the changes merged into its branch up to that merge. " +
		"Of the other deploys only the time is known: the first one after a merge is taken as the one that shipped it."
	if !strings.Contains(strings.Join(r.Limits, "\n"), want) {
		t.Errorf("limits lack %q:\n%s", want, strings.Join(r.Limits, "\n"))
	}
}

// A store written before the merge commit was read has none, and a deploy
// then holds what merged before it started, as it always did.
func TestADeployWithNoMergeCommitGoesByItsTime(t *testing.T) {
	t0 := end.Add(-3 * day)
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	for _, c := range []record.Change{change(1, t0, merged(t0.Add(h))), change(2, t0.Add(2*h), merged(t0.Add(3*h))), change(3, t0.Add(4*h), merged(t0.Add(5*h)))} {
		rb.Change(&c)
	}
	for _, d := range []record.Deploy{
		deployment("production", "main", "", true, t0.Add(2*h), 0, record.DeploySuccess),
		deployment("production", "main", "x", true, t0.Add(6*h), 0, record.DeploySuccess),
	} {
		rb.Deploy(&d)
	}
	rb.Done()
	l := b.Line()
	if got, want := vals(l.obs["release.wait"]), []float64{3, 1}; !same(got, want) {
		t.Errorf("release.wait = %v, want %v", got, want)
	}
	r, err := l.Report(Options{Target: repo, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	if want := "No production deploy in the store is of the commit a merge left"; !strings.Contains(strings.Join(r.Limits, "\n"), want) {
		t.Errorf("limits lack %q:\n%s", want, strings.Join(r.Limits, "\n"))
	}
}

// The runs a merge queue starts are on a commit the queue made. They join
// their change by the name of the branch, as a CI round of their own, and
// the time they fly is taken out of the merge station.
//
// The change opens at t0 with one commit. Its pull request round takes 30
// minutes. It is approved at 2 hours and merges at 4. The queue runs CI from
// 3h to 3h30 and fails, then again from 3h30 to 4h and passes.
func TestMergeQueueRuns(t *testing.T) {
	t0 := end.Add(-3 * day)
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	c := change(7, t0, review(record.ReviewApproved, t0.Add(2*h)), merged(t0.Add(4*h)))
	rb.Change(&c)
	queue := func(sha, branch, event string, from time.Time, conclusion string) record.CIRun {
		x := run(sha, 1, from, from, from.Add(30*time.Minute), conclusion)
		x.Event, x.Branch = event, branch
		return x
	}
	for _, x := range []record.CIRun{
		run("sha7", 1, t0, t0, t0.Add(30*time.Minute), "success"),
		queue("q1", "gh-readonly-queue/main/pr-7-aaa", "merge_group", t0.Add(3*h), "failure"),
		queue("q2", "gh-readonly-queue/main/pr-7-bbb", "merge_group", t0.Add(3*h+30*time.Minute), "success"),
		// None of these is a queue run of a change in the store.
		queue("q3", "gh-readonly-queue/main/pr-99-ccc", "merge_group", t0.Add(3*h), "failure"),
		queue("q4", "gh-readonly-queue/main/pr-7-ddd", "push", t0.Add(3*h), "failure"),
		queue("q5", "feat/pr-7-eee", "merge_group", t0.Add(3*h), "failure"),
	} {
		rb.CIRun(&x)
	}
	rb.Done()
	l := b.Line()
	// Three rounds: the pull request's and the two of the queue.
	if got, want := vals(l.obs["ci.rework"]), []float64{0, 1, 0}; !same(got, want) {
		t.Errorf("ci.rework = %v, want %v", got, want)
	}
	if got, want := vals(l.obs["ci.held"]), []float64{0.5, 0.5, 0.5}; !same(got, want) {
		t.Errorf("ci.held = %v, want %v", got, want)
	}
	// From the approval to the merge is 2 hours. The queue ran CI for one.
	if got, want := vals(l.obs["merge.held"]), []float64{1}; !same(got, want) {
		t.Errorf("merge.held = %v, want %v", got, want)
	}
	if got := l.repos[0]; got.ciRuns != 6 || got.ciJoined != 3 {
		t.Errorf("runs %d, joined %d, want 6 and 3", got.ciRuns, got.ciJoined)
	}

	for branch, want := range map[string]int{
		"gh-readonly-queue/main/pr-7-aaa":             7,
		"gh-readonly-queue/release/1.x/pr-1234-0a1b2": 1234,
		"gh-readonly-queue/pr-5-x/pr-6-abc":           6,
		"gh-readonly-queue/main/pr-7":                 0,
		"gh-readonly-queue/main/pr-x-aaa":             0,
		"gh-readonly-queue/main/pr-0-aaa":             0,
		"gh-readonly-queue/main":                      0,
		"main":                                        0,
		"":                                            0,
	} {
		if got, ok := queuedFor(branch); got != want && (ok || want != 0) || ok != (want != 0) {
			t.Errorf("queuedFor(%q) = %d, %v, want %d", branch, got, ok, want)
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
	for _, not := range []string{"wait for a runner", "production deploy"} {
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
	// A release before the changes merged shows the station was there.
	rb.Deploy(&record.Deploy{Source: record.GitHub, Repo: repo, Via: "release", Production: true, Ref: "v0.9.0", CreatedAt: t0.Add(-h), Outcome: record.DeployUnknown})
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
		line.Spec:    {"not_connected", "No tickets are in the store. Spec is measured from tickets, such as the issues of a repository."},
		line.Code:    {"measured", ""},
		line.CI:      {"partial", "No CI run joined a change in acme/lib."},
		line.Verify:  {"not_connected", "Fewer than 5 deployments to an environment that is not production were found on a commit of a change before it merged."},
		line.Review:  {"measured", "No review was seen on any change in the store. The time from ready to merge counts here, so a change that waits here may be waiting to merge."},
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
		"No production deploy in the store is of the commit a merge left, so the store cannot tell which changes a deploy holds. The first production deploy after a merge is taken as the one that shipped it.",
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

// A commit with a wrong date, a merge commit and a commit from long before
// the change was opened do not start it. Each would count years of writing.
func TestOldCommitsDoNotStartAChange(t *testing.T) {
	t0 := end.Add(-3 * day)
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	c := change(1, t0)
	epoch := commit("e", time.Unix(0, 0).UTC())
	old := commit("o", t0.Add(-(line.EarlyCommitDays+1)*day))
	merge := commit("m", t0.Add(-10*day))
	merge.Parents = 2
	mine := commit("a", t0.Add(-2*h))
	c.Commits = []record.Commit{epoch, old, merge, mine}
	c.CommitCount = 4
	rb.Change(&c)
	rb.Done()
	l := b.Line()
	if got := vals(l.obs["code.work"]); !same(got, []float64{2}) {
		t.Errorf("code.work = %v, want [2]: only the commit written for the change starts it", got)
	}
}

// A ticket in the backlog does not wait in front of the line. A ticket waits
// from when it left the backlog, not from when it was written.
func TestBacklogIsNotWaiting(t *testing.T) {
	at := func(days float64) time.Time { return end.Add(time.Duration(days * float64(day))) }
	ptr := func(v time.Time) *time.Time { return &v }
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	rb.Done()
	ticket := func(n int, state string, created time.Time, ready, started *time.Time) {
		key := fmt.Sprintf("ENG-%d", n)
		b.Ticket(&record.Ticket{Source: record.Linear, ID: record.TicketID(record.Linear, key), Key: key, Project: "ENG",
			Title: "A ticket", State: state, CreatedAt: created, ReadyAt: ready, StartedAt: started})
	}
	ticket(1, record.TicketBacklog, at(-60), nil, nil)                  // someday
	ticket(2, record.TicketUnstarted, at(-60), ptr(at(-3)), nil)        // in the backlog for 57 days, then next
	ticket(3, record.TicketStarted, at(-60), ptr(at(-10)), ptr(at(-8))) // waited 2 days once out of the backlog
	ticket(4, record.TicketStarted, at(-60), ptr(at(-5)), ptr(at(-5)))  // straight from the backlog to work
	ticket(5, record.TicketUnstarted, at(-4), nil, nil)                 // stored before ReadyAt: counts from creation
	l := b.Line()
	if got := vals(l.obs["spec.wait"]); !same(got, []float64{48, 0}) {
		t.Errorf("spec.wait = %v, want [48 0]", got)
	}
	from := map[time.Time]bool{}
	for _, s := range l.stays["spec.queue"] {
		from[s.from] = true
	}
	if len(l.stays["spec.queue"]) != 3 || !from[at(-3)] || !from[at(-10)] || !from[at(-4)] {
		t.Errorf("spec.queue = %+v, want ENG-2 from -3, ENG-3 from -10 and ENG-5 from -4; ENG-1 in the backlog never", l.stays["spec.queue"])
	}
}

// reviewBy adds a review or a comment from any account.
func reviewBy(login, kind, via, body, state string, at time.Time) opt {
	return func(c *record.Change) {
		c.Reviews = append(c.Reviews, record.Review{Author: record.Actor{Login: login, Kind: kind}, State: state, SubmittedAt: at, Via: via, Body: body})
	}
}

// oneChange builds a line of one change with the given builder options and
// returns its streams.
func oneChange(t *testing.T, human bool, c record.Change) *Line {
	t.Helper()
	b := NewBuilder(registry(t))
	if human {
		b.HumanReviewsOnly()
	}
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-26 * week), SyncedAt: end})
	rb.Change(&c)
	rb.Done()
	return b.Line()
}

// Issue #45: every review counts, agents and people apart, and a change with
// no review seen spends ready to merge at review.
func TestReviewsByPeopleAndAgents(t *testing.T) {
	t0 := end.Add(-3 * day)
	at := func(hours float64) time.Time { return t0.Add(time.Duration(hours * float64(h))) }
	get := func(l *Line, stream string) []float64 { return vals(l.obs[stream]) }
	cases := []struct {
		name  string
		human bool
		opts  []opt
		want  map[string][]float64
	}{
		{
			name: "no review seen: ready to merge is review time, merge waits for none of it",
			opts: []opt{merged(at(4))},
			want: map[string][]float64{"review.flow": {1}, "review.held": {4}, "review.skipped": {1}, "merge.wait": {0}, "merge.held": {0},
				"review.wait": nil, "review.rounds": nil, "review.by_human": {0}, "review.by_agent": {0}},
		},
		{
			name: "a review bot counts as a review by an agent",
			opts: []opt{reviewBy("gemini-code-assist[bot]", record.ActorBot, "review", "", record.ReviewCommented, at(1)), merged(at(4))},
			want: map[string][]float64{"review.wait": {1}, "review.skipped": {0}, "review.by_agent": {1}, "review.by_human": {0},
				"review.first_agent": {1}, "review.first_human": nil, "merge.wait": {0}},
		},
		{
			name: "an unknown bot account counts as an agent too",
			opts: []opt{reviewBy("acme-review[bot]", record.ActorBot, "review", "", record.ReviewCommented, at(2)), merged(at(4))},
			want: map[string][]float64{"review.skipped": {0}, "review.by_agent": {1}},
		},
		{
			name: "the author's review with text is an agent working through the account",
			opts: []opt{reviewBy("dev", record.ActorUser, "review", "## Review\nOne finding.", record.ReviewCommented, at(2)), merged(at(4))},
			want: map[string][]float64{"review.skipped": {0}, "review.by_agent": {1}, "review.by_human": {0}},
		},
		{
			name: "the author's reply to a thread is not a review",
			opts: []opt{reviewBy("dev", record.ActorUser, "review", "", record.ReviewCommented, at(2)), merged(at(4))},
			want: map[string][]float64{"review.skipped": {1}, "review.by_agent": {0}, "review.by_human": {0}},
		},
		{
			name: "a comment counts only when a review signature matches it",
			opts: []opt{
				reviewBy("release-please[bot]", record.ActorBot, "comment", "Release 1.2.0", record.ReviewCommented, at(1)),
				reviewBy("ana", record.ActorUser, "comment", "Looks good to me", record.ReviewCommented, at(1.5)),
				reviewBy("claude[bot]", record.ActorBot, "comment", "## Code review\nNo issues.", record.ReviewCommented, at(3)),
				merged(at(4)),
			},
			want: map[string][]float64{"review.wait": {3}, "review.by_agent": {1}, "review.by_human": {0}},
		},
		{
			name:  "human reviews only: a change only agents reviewed has no review seen",
			human: true,
			opts:  []opt{reviewBy("gemini-code-assist[bot]", record.ActorBot, "review", "", record.ReviewCommented, at(1)), merged(at(4))},
			want: map[string][]float64{"review.skipped": {1}, "review.wait": nil, "review.held": {4}, "merge.wait": {0},
				"review.by_agent": {1}, "review.first_agent": {1}},
		},
		{
			name:  "human reviews only: a person's review still counts",
			human: true,
			opts: []opt{reviewBy("gemini-code-assist[bot]", record.ActorBot, "review", "", record.ReviewCommented, at(1)),
				review(record.ReviewApproved, at(2)), merged(at(4))},
			want: map[string][]float64{"review.skipped": {0}, "review.wait": {2}, "review.by_both": {1}, "merge.wait": {2}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := change(1, at(0), tc.opts...)
			l := oneChange(t, tc.human, c)
			for stream, w := range tc.want {
				if got := get(l, stream); !same(got, w) {
					t.Errorf("%s = %v, want %v", stream, got, w)
				}
			}
		})
	}
}

func TestReviewRounds(t *testing.T) {
	t0 := end.Add(-3 * day)
	at := func(hours float64) time.Time { return t0.Add(time.Duration(hours * float64(h))) }
	rv := func(hs ...float64) []revLite {
		var out []revLite
		for _, x := range hs {
			out = append(out, revLite{at: at(x)})
		}
		return out
	}
	cm := func(merge bool, hs ...float64) []commitLite {
		var out []commitLite
		for _, x := range hs {
			out = append(out, commitLite{committed: at(x), merge: merge})
		}
		return out
	}
	cases := []struct {
		name    string
		reviews []revLite
		commits []commitLite
		want    int
	}{
		{"no review", nil, cm(false, 1), 0},
		{"one review", rv(2), cm(false, 1), 1},
		{"two reviews, no push between", rv(2, 3), cm(false, 1), 1},
		{"review, fix, review", rv(2, 4), cm(false, 1, 3), 2},
		{"review, fix, review, fix, review", rv(2, 4, 6), cm(false, 3, 5), 3},
		{"a merge of the base is not a fix", rv(2, 4), cm(true, 3), 1},
		{"a fix after the last review starts no round", rv(2), cm(false, 3), 1},
	}
	for _, tc := range cases {
		if got := rounds(tc.reviews, tc.commits); got != tc.want {
			t.Errorf("%s: rounds = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestParseReviews(t *testing.T) {
	for in, want := range map[string]bool{"": false, "all": false, "ALL": false, "human": true, " Human ": true} {
		o, err := ParseReviews(in)
		if err != nil || o.HumanReviewsOnly != want {
			t.Errorf("%q: %+v, %v", in, o, err)
		}
	}
	if _, err := ParseReviews("agents"); err == nil {
		t.Error("agents was accepted")
	}
}
