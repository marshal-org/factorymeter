package line

import "strconv"

// V1 is the first version of the contract.
const V1 = "v1"

// set describes the numbers of one station in words. build turns it into
// metrics, so every station gets the same ids and the same statistics.
type set struct {
	station string
	needs   []string
	slices  []string
	// Each pair is a short name and the definition. An empty name means the
	// station has no such number.
	flowName, flow     string
	waitName, wait     string
	workName, work     string
	reworkName, rework string
	queueName, queue   string
	// oldest says what the age of the oldest queued item is counted from.
	oldest string
}

var (
	changeSlices = []string{SliceClass, SliceAgent, SliceRepo}
	repoOnly     = []string{SliceRepo}
)

const (
	another    = "A review counts when a person other than the author wrote it. Reviews by bots and by the author do not count."
	exclusive  = "Time with a CI run in flight is counted at ci and taken out of the station the change stood at, so no hour is counted twice."
	deployNote = "A production deploy of the commit a merge left on its branch holds the changes merged into that branch up to that merge. Of any other deploy only the time is known, and it is taken to hold the changes merged before it started."
)

var sets = []set{
	{
		station: Spec, needs: []string{NeedTicket}, slices: repoOnly,
		flowName:   "Tickets started",
		flow:       "Tickets that work started on in the period. Work starts when the ticket first moves to a started state or its first linked change is opened, whichever comes first.",
		waitName:   "Wait for work to start",
		wait:       "Hours from a ticket leaving the backlog to work starting on it, over the tickets that work started on in the period. A ticket leaves the backlog the first time it is in a state that is not triage or backlog. A GitHub issue has no backlog, so it waits from when it was created.",
		reworkName: "Tickets reopened",
		rework:     "Share of the tickets finished in the period that had been finished before and were opened again.",
		queueName:  "Tickets not started",
		queue:      "Tickets that are open, out of the backlog and that nobody has started, at the end of the period. Tickets in triage or the backlog are not waiting for the line, and cancelled tickets are left out.",
		oldest:     "it left the backlog",
	},
	{
		station: Code, needs: []string{NeedChange}, slices: changeSlices,
		flowName:   "PRs made ready",
		flow:       "Changes marked ready for review in the period. A change opened ready counts when it is opened.",
		workName:   "Time to write a PR",
		work:       "Hours from the start of a change to it being marked ready for review, over the changes marked ready in the period. The start is the first commit or the opening of the change, whichever is earlier. Merge commits, and commits written more than " + strconv.Itoa(EarlyCommitDays) + " days before the change was opened, do not count: they are history the change carries, or a wrong date.",
		reworkName: "PRs abandoned",
		rework:     "Share of the changes closed in the period that were closed without merging.",
		queueName:  "PRs in draft",
		queue:      "Changes that are open and still a draft at the end of the period.",
		oldest:     "it was opened",
	},
	{
		station: CI, needs: []string{NeedChange, NeedCIRun}, slices: changeSlices,
		flowName:   "CI rounds",
		flow:       "CI rounds that finished in the period. A CI round is every run on one commit of a change. The runs a merge queue starts for a change are a round of their own. Skipped and cancelled runs are left out.",
		waitName:   "Wait for a runner",
		wait:       "Hours from the first run of a CI round being created to the first run starting, over the rounds that finished in the period.",
		workName:   "CI round time",
		work:       "Hours with at least one run in flight, from the first run of a CI round starting to the last one finishing, over the rounds that finished in the period.",
		reworkName: "CI rounds that failed",
		rework:     "Share of the CI rounds finished in the period in which a run failed, timed out or was run again.",
	},
	{
		station: Verify, needs: []string{NeedChange, NeedDeploy}, slices: changeSlices,
		flowName:   "PRs verified",
		flow:       "Changes whose first deployment to an environment that is not production reported success in the period.",
		waitName:   "Wait for an environment",
		wait:       "Hours from a commit of a change being written to its deployment to an environment that is not production being created, over the deployments created in the period.",
		workName:   "Time to bring the PR up",
		work:       "Hours from such a deployment being created to it reporting success, over the deployments that reported success in the period.",
		reworkName: "Verify deployments that failed",
		rework:     "Share of the deployments of changes to an environment that is not production, finished in the period, that reported a failure or an error.",
	},
	{
		station: Review, needs: []string{NeedChange}, slices: changeSlices,
		flowName:   "PRs reviewed",
		flow:       "Merged changes that left review in the period. A change leaves review at its last approval before the merge, or at the merge when it was reviewed and never approved. " + another,
		waitName:   "Wait for first review",
		wait:       "Hours from a change being marked ready for review to its first review, over the changes that got their first review in the period. A review that lands while the change is a draft gives a wait of zero. " + another,
		workName:   "First review to approval",
		work:       "Hours from the first review of a change to it leaving review, over the merged changes that left review in the period. " + another,
		reworkName: "PRs sent back in review",
		rework:     "Share of the merged changes that left review in the period on which a reviewer requested changes, or which got a new commit after the first review. Merge commits do not count.",
		queueName:  "PRs waiting for first review",
		queue:      "Changes that are open, ready for review and without a review at the end of the period. " + another,
		oldest:     "it was marked ready for review",
	},
	{
		station: Merge, needs: []string{NeedChange}, slices: changeSlices,
		flowName:   "PRs merged",
		flow:       "Changes merged in the period.",
		waitName:   "Wait to merge",
		wait:       "Hours from a change arriving at merge to it merging, over the changes merged in the period. A change arrives when it leaves review. A change nobody else reviewed arrives when it is marked ready for review.",
		reworkName: "Merges reverted",
		rework:     "Share of the changes merged in the period that a later change reverted. A change reverts another when its description says so in the words the code host writes for a revert.",
		queueName:  "PRs approved and not merged",
		queue:      "Changes that are open and approved at the end of the period, with no request for changes after the last approval.",
		oldest:     "its last approval",
	},
	{
		station: Release, needs: []string{NeedChange, NeedDeploy}, slices: changeSlices,
		flowName:   "PRs released",
		flow:       "Merged changes that reached production in the period. " + deployNote,
		waitName:   "Wait for a release",
		wait:       "Hours from a change merging to the start of the production deploy that shipped it, over the changes that reached production in the period. " + deployNote,
		workName:   "Deploy time",
		work:       "Hours from a production deploy being created to it reporting success, over the production deploys that reported success in the period.",
		reworkName: "Deploys that failed",
		rework:     "Share of the production deploys finished in the period that reported a failure or an error.",
		queueName:  "PRs merged and not released",
		queue:      "Changes that are merged and that no production deploy has followed, at the end of the period.",
		oldest:     "it merged",
	},
}

// extra holds the numbers that do not come from a set.
var extra = []Metric{
	{ID: "review.skipped", Station: Review, Kind: Other, Name: "PRs merged with no review", Unit: Share, Good: Down, MinSample: MinSample,
		Slices: changeSlices, Needs: []string{NeedChange},
		Definition: "Share of the changes merged in the period that no other person reviewed before the merge. " + another},
	{ID: "release.deploys", Station: Release, Kind: Other, Name: "Production deploys", Unit: PerWeek, Good: Up,
		Slices: repoOnly, Needs: []string{NeedDeploy},
		Definition: "Production deploys created in the period, per week. A deploy is a deployment to an environment whose name says production, or a published release when the repository has no such deployment."},
	{ID: "line.flow", Station: Whole, Kind: Flow, Name: "PRs merged", Unit: PerWeek, Good: Up,
		Slices: changeSlices, Needs: []string{NeedChange},
		Definition: "Changes merged in the period, per week. For the whole line this counts changes by agents and by people. Automation is left out."},
	{ID: "line.lead.p50", Station: Whole, Kind: Other, Name: "Open to merge, median", Unit: Hours, Good: Down, MinSample: MinSample,
		Slices: changeSlices, Needs: []string{NeedChange},
		Definition: "Hours from a change being opened to it merging, over the changes merged in the period. The median."},
	{ID: "line.lead.p90", Station: Whole, Kind: Other, Name: "Open to merge, 90th percentile", Unit: Hours, Good: Down, MinSample: MinSample,
		Slices: changeSlices, Needs: []string{NeedChange},
		Definition: "Hours from a change being opened to it merging, over the changes merged in the period. The 90th percentile: one change in ten took longer."},
	{ID: "line.delivery.p50", Station: Whole, Kind: Other, Name: "Start to production, median", Unit: Hours, Good: Down, MinSample: MinSample,
		Slices: changeSlices, Needs: []string{NeedChange, NeedDeploy},
		Definition: "Hours from the start of a change to the production deploy that shipped it, over the changes that reached production in the period. The start is the first commit or the opening of the change, whichever is earlier. Merge commits, and commits written more than " + strconv.Itoa(EarlyCommitDays) + " days before the change was opened, do not count: they are history the change carries, or a wrong date. The median. " + deployNote},
	{ID: "line.agent_share", Station: Whole, Kind: Other, Name: "Disclosed agent share", Unit: Share, Good: Neither, MinSample: MinSample,
		Slices: repoOnly, Needs: []string{NeedChange},
		Definition: "Changes by agents divided by changes by agents and by people, over the changes merged in the period. Automation is left out of both sides. It is a floor: an agent that leaves no trace counts as a person."},
	{ID: "line.size.p50", Station: Whole, Kind: Other, Name: "Lines changed, median", Unit: Lines, Good: Down, MinSample: MinSample,
		Slices: changeSlices, Needs: []string{NeedChange},
		Definition: "Lines added plus lines deleted, over the changes merged in the period. The median."},
}

func build() []Metric {
	var out []Metric
	add := func(s set, id string, k Kind, name string, u Unit, g Good, min int, def string) {
		out = append(out, Metric{ID: s.station + "." + id, Station: s.station, Kind: k, Name: name, Unit: u, Good: g,
			MinSample: min, Slices: s.slices, Needs: s.needs, Definition: def, Since: V1})
	}
	for _, s := range sets {
		st, _ := StationByID(s.station)
		if s.flowName != "" {
			add(s, "flow", Flow, s.flowName, PerWeek, Up, 0, s.flow+" The count is divided by the length of the period in weeks.")
		}
		if s.waitName != "" {
			add(s, "wait.p50", Wait, s.waitName+", median", Hours, Down, MinSample, s.wait+" The median.")
			add(s, "wait.p90", Wait, s.waitName+", 90th percentile", Hours, Down, MinSample, s.wait+" The 90th percentile: one item in ten waited longer.")
		}
		if s.workName != "" {
			add(s, "work.p50", Work, s.workName+", median", Hours, Down, MinSample, s.work+" The median.")
			add(s, "work.p90", Work, s.workName+", 90th percentile", Hours, Down, MinSample, s.work+" The 90th percentile: one item in ten took longer.")
		}
		if s.reworkName != "" {
			add(s, "rework", Rework, s.reworkName, Share, Down, MinSample, s.rework)
		}
		if s.queueName != "" {
			add(s, "queue", Queue, s.queueName, Count, Down, 0, s.queue)
			add(s, "queue.oldest", Queue, s.queueName+", age of the oldest", Hours, Down, 0,
				"Hours since "+s.oldest+", for the item that has stood longest in this queue at the end of the period. "+s.queue)
		}
		if st.InConstraint {
			add(s, "held.total", Held, "Hours a week held at "+s.station, Hours, Down, MinSample,
				"Hours the items that left the "+s.station+" station in the period spent there, wait and work together, per week. Each item is capped at the 90th percentile of the station, so one item that sat for months cannot decide the total. "+exclusive)
			add(s, "held.share", Held, "Share of the line's time held at "+s.station, Share, Down, MinSample,
				"The held hours of the "+s.station+" station divided by the held hours of every station that counts for the constraint. The station with the largest share is the constraint.")
		}
		for _, m := range extra {
			if m.Station == s.station {
				m.Since = V1
				out = append(out, m)
			}
		}
	}
	for _, m := range extra {
		if m.Station == Whole {
			m.Since = V1
			out = append(out, m)
		}
	}
	return out
}
