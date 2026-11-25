package line

import (
	"fmt"
	"regexp"
	"strings"
)

// The numbers the recommendation rules compare with. A rule says when it
// fires in words built from these, so the docs cannot drift from the code.
const (
	// BestWeeks is the length of the stretch a station is sized against: its
	// own best four weeks of the window.
	BestWeeks = 4
	// SlowRatio is how far above its best four weeks a station that is not
	// the constraint must run before it gets a recommendation.
	SlowRatio = 1.25
	// SlowShare is the smallest gain worth a recommendation at a station that
	// is not the constraint, as a part of the hours it is compared with.
	SlowShare = 0.05
	// CIFailing is the share of failed CI rounds from which the checks get a
	// recommendation.
	CIFailing = 0.15
	// AgentCIGap is how many points more often CI must fail for agents than
	// for people. 0.05 is five points.
	AgentCIGap = 0.05
	// Reverted is the share of merges reverted from which reverts get a
	// recommendation, and RevertedMin the fewest reverts it takes.
	Reverted    = 0.05
	RevertedMin = 3
	// Abandoned is the share of closed changes that were not merged.
	Abandoned = 0.2
	// ReviewQueueMin is the smallest review queue that counts as a queue.
	ReviewQueueMin = 5
	// A draft is stale after StaleDraftDays. It takes StaleDraftMin of them.
	StaleDraftDays = 14
	StaleDraftMin  = 3
	// An approved change has waited too long after ApprovedHours. It takes
	// ApprovedMin of them.
	ApprovedHours = 24
	ApprovedMin   = 3
	// Ungated is the share of changes merged with no review from which a
	// line with no verify station is told to add a gate.
	Ungated = 0.5
	// An agent's changes are large when their median is AgentSizeRatio times
	// that of people and at least AgentSizeLines.
	AgentSizeRatio = 2.0
	AgentSizeLines = 200
	// Agents wait longer for review when their median wait is AgentWaitRatio
	// times that of people and at least AgentWaitHours more.
	AgentWaitRatio = 1.5
	AgentWaitHours = 1.0
	// Reopened is the share of finished tickets that had been finished
	// before and were opened again.
	Reopened = 0.1
	// A ticket nobody started is stale after StaleTicketDays. It takes
	// StaleTicketMin of them.
	StaleTicketDays = 90
	StaleTicketMin  = 5
	// The tickets ready to start are thin when they are fewer than the line
	// starts in BacklogWeeks.
	BacklogWeeks = 1
	// A recommendation drawn from HighSample items or more is high
	// confidence, from MediumSample or more medium, below that low.
	HighSample   = 30
	MediumSample = 10
)

// Playbook is where the pages a recommendation links to live.
const Playbook = "https://thesoftwarefactory.site/playbook/"

// Plays are the playbook pages a rule can link to.
var Plays = []string{
	"find-the-constraint", "set-a-baseline", "one-owner-for-the-whole-line", "environment-per-agent", "agents-prove-their-work",
	"decide-up-front", "out-of-the-synchronous-loop", "trust-is-earned-with-evidence", "guardrails-before-autonomy", "keep-the-line-fed",
}

// Rule is one recommendation factorymeter can make. It holds the definition
// only. The code that reads the numbers is in pkg/measure.
type Rule struct {
	// ID is stable: station, a dot, a short name with dashes.
	ID      string `json:"id"`
	Station string `json:"station"`
	// Title says what to do, in one line.
	Title string `json:"title"`
	// Fires says when the rule makes its recommendation.
	Fires string `json:"fires"`
	// Reads lists the metrics the rule looks at.
	Reads []string `json:"reads"`
	// Sized says how the hours freed are worked out. Empty for a rule that
	// never sizes its gain.
	Sized string `json:"sized"`
	// Play is the playbook page that describes the practice. Empty for none.
	Play  string `json:"play"`
	Since string `json:"since"`
}

// PlayURL is the link to the playbook page of a rule, or empty.
func (r Rule) PlayURL() string {
	if r.Play == "" {
		return ""
	}
	return Playbook + r.Play + "/"
}

const (
	sizedHeld = "The mean hours an item is held at the station now, less the same mean in the station's best four weeks, times the items that leave the station in a week. Each item is capped at the 90th percentile of the items it is averaged with."
	bothSides = "Both sides are drawn from at least 5 items."
)

func pct(v float64) string { return fmt.Sprintf("%.0f percent", v*100) }

func weeksWord(n int) string {
	if n == 1 {
		return "one week"
	}
	return fmt.Sprintf("%d weeks", n)
}

// slow builds the rule every station that can be the constraint has.
func slow(station, title, play string, reads ...string) Rule {
	against := "the hours changes are held before the merge"
	if station == Release {
		against = "the hours changes are held on the whole line"
	}
	return Rule{
		ID: station + ".slow", Station: station, Title: title, Play: play, Sized: sizedHeld,
		Reads: append([]string{station + ".held.total", station + ".held.share"}, reads...),
		Fires: fmt.Sprintf("The station is the constraint. Or it holds an item at least %.0f percent longer than in its own best four weeks, and running at that pace would give back at least %s of %s.",
			(SlowRatio-1)*100, pct(SlowShare), against),
	}
}

// Rules is every recommendation, in the order they are listed when nothing
// else ranks them.
var Rules = stamp([]Rule{
	slow(Code, "Get changes ready for review sooner", "decide-up-front", "code.work.p50"),
	slow(CI, "Make CI rounds shorter", "find-the-constraint", "ci.work.p50", "ci.wait.p50"),
	slow(Verify, "Bring changes up on an environment faster", "environment-per-agent", "verify.wait.p50", "verify.work.p50"),
	slow(Review, "Cut the wait for review", "trust-is-earned-with-evidence", "review.wait.p50", "review.work.p50", "review.queue"),
	slow(Merge, "Merge finished changes sooner", "out-of-the-synchronous-loop", "merge.wait.p50", "merge.queue"),
	slow(Release, "Release more often", "find-the-constraint", "release.wait.p50", "release.deploys", "release.queue"),
	{
		ID: "verify.missing", Station: Verify, Title: "Prove changes on a running environment before they merge", Play: "agents-prove-their-work",
		Reads: []string{"review.skipped"},
		Fires: fmt.Sprintf("The verify station is not connected, and at least %s of the changes merged in the window had no review seen.", pct(Ungated)),
	},
	{
		ID: "ci.failing", Station: CI, Title: "Cut the share of CI rounds that fail",
		Reads: []string{"ci.rework", "ci.flow", "ci.work.p50"},
		Fires: fmt.Sprintf("At least %s of the CI rounds in the window failed, timed out or were run again. The evidence names the workflows whose runs did so most often.", pct(CIFailing)),
		Sized: "The share of failed rounds now, less the share in the best four weeks, times the rounds in a week, times the mean hours a round holds a change.",
	},
	{
		ID: "ci.agents-fail-more", Station: CI, Title: "Have agents run the checks before they push", Play: "agents-prove-their-work",
		Reads: []string{"ci.rework"},
		Fires: fmt.Sprintf("CI rounds on changes by agents fail at least %.0f points more often than rounds on changes by people. %s", AgentCIGap*100, bothSides),
	},
	{
		ID: "merge.reverted", Station: Merge, Title: "Find out why merged changes get reverted", Play: "agents-prove-their-work",
		Reads: []string{"merge.rework"},
		Fires: fmt.Sprintf("At least %s of the changes merged in the window were reverted later, and at least %d of them.", pct(Reverted), RevertedMin),
	},
	{
		ID: "code.abandoned", Station: Code, Title: "Find out why changes are closed without merging", Play: "decide-up-front",
		Reads: []string{"code.rework"},
		Fires: fmt.Sprintf("At least %s of the changes closed in the window were closed without merging.", pct(Abandoned)),
	},
	{
		ID: "review.queue-growing", Station: Review, Title: "Clear the review queue", Play: "trust-is-earned-with-evidence",
		Reads: []string{"review.queue", "review.queue.oldest", "review.wait.p50"},
		Fires: fmt.Sprintf("At least %d changes wait for a first review at the end of the window, and the trend of the queue is worse: it is longer than at the end of the window before by more than the report calls flat. The rule is silent when no change in the store was ever reviewed.", ReviewQueueMin),
	},
	{
		ID: "merge.approved-waiting", Station: Merge, Title: "Merge the changes that are already approved", Play: "out-of-the-synchronous-loop",
		Reads: []string{"merge.queue", "merge.queue.oldest"},
		Fires: fmt.Sprintf("At least %d changes have been approved for more than %d hours and are not merged at the end of the window.", ApprovedMin, ApprovedHours),
	},
	{
		ID: "code.stale-drafts", Station: Code, Title: "Finish or close the drafts that stopped moving",
		Reads: []string{"code.queue", "code.queue.oldest"},
		Fires: fmt.Sprintf("At least %d changes have been a draft for more than %d days at the end of the window.", StaleDraftMin, StaleDraftDays),
	},
	{
		ID: "spec.reopened", Station: Spec, Title: "Find out why finished tickets are opened again", Play: "decide-up-front",
		Reads: []string{"spec.rework"},
		Fires: fmt.Sprintf("At least %s of the tickets finished in the window had been finished before and were opened again. The share is drawn from at least %d tickets.", pct(Reopened), MinSample),
	},
	{
		ID: "spec.stale-tickets", Station: Spec, Title: "Close or schedule the tickets nobody has started",
		Reads: []string{"spec.queue", "spec.queue.oldest"},
		Fires: fmt.Sprintf("At least %d tickets have been open for more than %d days at the end of the window with no work started on them.", StaleTicketMin, StaleTicketDays),
	},
	{
		ID: "spec.backlog-thin", Station: Spec, Title: "Write up more work that is ready to start", Play: "keep-the-line-fed",
		Reads: []string{"spec.queue", "spec.flow"},
		Fires: fmt.Sprintf("Fewer tickets are open and not started at the end of the window than the line starts in %s. The pace is drawn from at least %d tickets started in the window.", weeksWord(BacklogWeeks), MinSample),
	},
	{
		ID: "review.agents-wait-longer", Station: Review, Title: "Find out why changes by agents wait longer for review", Play: "trust-is-earned-with-evidence",
		Reads: []string{"review.wait.p50"},
		Fires: fmt.Sprintf("The median wait for a first review is at least %.1f times as long for changes by agents as for changes by people, and at least %.0f hour longer. %s", AgentWaitRatio, AgentWaitHours, bothSides),
	},
	{
		ID: "code.agent-changes-large", Station: Code, Title: "Have agents ship smaller changes",
		Reads: []string{"line.size.p50"},
		Fires: fmt.Sprintf("The median change by an agent is at least %.0f times the size of the median change by a person, and at least %d lines. %s", AgentSizeRatio, AgentSizeLines, bothSides),
	},
})

func stamp(list []Rule) []Rule {
	for i := range list {
		list[i].Since = V1
	}
	return list
}

// RuleByID finds a rule by its id.
func RuleByID(id string) (Rule, bool) {
	for _, r := range Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// RuleIDPattern is the form of a rule id. It is written without a backslash
// so it can sit in a struct tag.
const RuleIDPattern = `^[a-z]+[.][a-z][a-z0-9]*(-[a-z0-9]+)*$`

var ruleForm = regexp.MustCompile(RuleIDPattern)

// ValidateRules checks a list of rules. It returns every problem it finds.
func ValidateRules(list []Rule) []error {
	var errs []error
	bad := func(r Rule, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", r.ID, fmt.Sprintf(format, a...)))
	}
	seen := map[string]bool{}
	for _, r := range list {
		if !ruleForm.MatchString(r.ID) {
			bad(r, "the id must look like station.short-name")
			continue
		}
		if seen[r.ID] {
			bad(r, "the id is used twice")
		}
		seen[r.ID] = true
		if _, ok := StationByID(r.Station); !ok || !strings.HasPrefix(r.ID, r.Station+".") {
			bad(r, "the id must start with the id of the station %q", r.Station)
		}
		if t := strings.TrimSpace(r.Title); t == "" || strings.HasSuffix(t, ".") {
			bad(r, "the title is one line with no full stop")
		}
		if f := strings.TrimSpace(r.Fires); f == "" || !strings.HasSuffix(f, ".") {
			bad(r, "when the rule fires must be a sentence that ends with a full stop")
		}
		if r.Sized != "" && !strings.HasSuffix(r.Sized, ".") {
			bad(r, "how the gain is sized must be a sentence that ends with a full stop")
		}
		if len(r.Reads) == 0 {
			bad(r, "the rule reads no metric")
		}
		for _, id := range r.Reads {
			if _, ok := ByID(id); !ok {
				bad(r, "reads the unknown metric %q", id)
			}
		}
		if r.Play != "" && !known(r.Play, Plays...) {
			bad(r, "unknown playbook page %q", r.Play)
		}
		if r.Since == "" {
			bad(r, "no contract version")
		}
	}
	return errs
}
