// Package contract defines what factorymeter writes out, and generates the
// reference docs and the JSON Schema from the definitions in code.
//
// The contract has three layers. Records (pkg/record) are what is kept.
// Metrics (pkg/line) are what is measured. The report in this file is what is
// written out. Inside one version fields are only ever added, and a metric id
// never changes meaning.
package contract

import "time"

// Version marks every report.
const Version = "factorymeter/v1"

// Report is one run of "factorymeter report" as JSON.
type Report struct {
	Schema          string           `json:"schema" doc:"Always factorymeter/v1." enum:"factorymeter/v1"`
	GeneratedAt     time.Time        `json:"generated_at" doc:"When the report was computed."`
	Tool            Tool             `json:"tool" doc:"The program that wrote the report."`
	Target          string           `json:"target" doc:"The owner or owner/repo that was measured. Empty in a summary."`
	Summary         bool             `json:"summary" doc:"True when every name, title and link was left out, so the file holds station numbers only."`
	Window          Window           `json:"window" doc:"The time the report covers."`
	Coverage        Coverage         `json:"coverage" doc:"What was loaded and what is missing."`
	Verdict         Verdict          `json:"verdict" doc:"The one-sentence answer."`
	Stations        []Station        `json:"stations" doc:"The seven stations of the line, in order."`
	Metrics         []Series         `json:"metrics" doc:"Every number, each with its weekly history."`
	Recommendations []Recommendation `json:"recommendations" doc:"Where to invest, most valuable first."`
	Limits          []string         `json:"limits" doc:"What the reader should know before trusting a number."`
}

// Tool names the program and the rule files behind a report.
type Tool struct {
	Name            string `json:"name" doc:"Always factorymeter."`
	Version         string `json:"version" doc:"The version of the program."`
	RegistryVersion string `json:"registry_version" doc:"The version of the agent signature registry used to tell agents from people."`
	RecordSchema    int    `json:"record_schema" doc:"The version of the record layout in the store the report was read from."`
}

// Window is the time a report covers.
type Window struct {
	Since      time.Time `json:"since" doc:"Start of the window the values are computed over."`
	Until      time.Time `json:"until" doc:"End of the window."`
	PeriodDays int       `json:"period_days" doc:"Length of one period of a history, in days. 7 unless asked otherwise."`
	Periods    []Period  `json:"periods" doc:"The periods of every history, oldest first. They can reach further back than the window."`
	Previous   *Period   `json:"previous" doc:"The window of the same length just before this one, which values are compared with. Null when the store does not reach that far back."`
}

// Period is one span of time.
type Period struct {
	From time.Time `json:"from" doc:"Start, inclusive."`
	To   time.Time `json:"to" doc:"End, exclusive."`
}

// Coverage says what stands behind the report.
type Coverage struct {
	Repos    []RepoCoverage    `json:"repos" doc:"One entry per repository in the store. Empty in a summary."`
	Sources  []SourceCoverage  `json:"sources" doc:"One entry per kind of record."`
	Trackers []TrackerCoverage `json:"trackers" doc:"One entry per ticket system that is not a repository, such as a Linear workspace or a Jira site. Empty when none is in the store."`
}

// TrackerCoverage is one ticket system that is not a repository.
type TrackerCoverage struct {
	Source   string    `json:"source" doc:"The ticket system." enum:"linear,jira"`
	Name     string    `json:"name" doc:"The URL key of the Linear workspace, or the host of the Jira site. Empty in a summary."`
	SyncedAt time.Time `json:"synced_at" doc:"When it was last synced. The zero time while its first sync has not finished."`
	Tickets  int       `json:"tickets" doc:"Its tickets in the store, in the window or not."`
	Linked   int       `json:"linked" doc:"Of those, the tickets a pull request in the report is linked to: attached to the ticket by the system, or naming its key in the branch, title or description."`
	Keys     []string  `json:"keys" doc:"The key prefixes a pull request is searched for, such as ENG: the teams or projects its tickets are in, now or before. Found in the tickets, never set by hand. Empty in a summary."`
}

// RepoCoverage is one repository in the store.
type RepoCoverage struct {
	Repo     string    `json:"repo" doc:"The repository, as owner/name."`
	Private  bool      `json:"private" doc:"True for a private repository."`
	From     time.Time `json:"from" doc:"How far back the store holds this repository."`
	SyncedAt time.Time `json:"synced_at" doc:"When the repository was last synced."`
	Changes  int       `json:"changes" doc:"Changes in the store for this repository."`
}

// SourceCoverage is one kind of record in the store.
type SourceCoverage struct {
	Kind    string `json:"kind" doc:"The kind of record." enum:"ticket,change,ci_run,deploy"`
	Source  string `json:"source" doc:"Where the records come from, such as github. Empty when nothing is connected."`
	Records int    `json:"records" doc:"How many records of this kind the window holds."`
	Note    string `json:"note" doc:"What to know about this source, such as which environment counts as production. Empty when there is nothing to say."`
}

// Verdict is the answer on top of the page.
type Verdict struct {
	Headline   string `json:"headline" doc:"One sentence that names the constraint and how it is moving."`
	Constraint string `json:"constraint" doc:"The id of the constraint station. Empty when too few changes merged to name one."`
	Detail     string `json:"detail" doc:"One or two sentences of evidence behind the headline."`
}

// Station statuses.
const (
	StatusMeasured     = "measured"
	StatusPartial      = "partial"
	StatusNotConnected = "not_connected"
)

// Station is one station of the line in a report.
type Station struct {
	ID         string   `json:"id" doc:"The station id." enum:"spec,code,ci,verify,review,merge,release"`
	Name       string   `json:"name" doc:"The station name."`
	Status     string   `json:"status" doc:"Whether the station could be measured." enum:"measured,partial,not_connected"`
	Missing    string   `json:"missing" doc:"What to connect to measure the station fully. Empty when nothing is missing."`
	Constraint bool     `json:"constraint" doc:"True for the one station named the constraint."`
	HeldShare  *float64 `json:"held_share" doc:"The station's share of the hours held on the line, between 0 and 1. Null when it was not measured or is left out of the constraint."`
	Oldest     []Item   `json:"oldest" doc:"The items that have stood longest in the station's queue, oldest first. Empty in a summary."`
}

// Item is one ticket or change a report points at.
type Item struct {
	Kind  string  `json:"kind" doc:"What the item is." enum:"ticket,change"`
	Key   string  `json:"key" doc:"The name people use for it, such as owner/repo#12."`
	URL   string  `json:"url" doc:"Link to it on the source."`
	Title string  `json:"title" doc:"Its title."`
	Class string  `json:"class" doc:"Who wrote it. Empty for a ticket." enum:"agent,human,automation,"`
	Hours float64 `json:"hours" doc:"How long it has stood in the queue, in hours."`
}

// Slice says which part of the line a series is about. Every field empty
// means the whole line.
type Slice struct {
	Class string `json:"class" doc:"Who wrote the changes. Empty for all of them." enum:"agent,human,automation,"`
	Agent string `json:"agent" doc:"The id of one agent in the signature registry. Empty for all."`
	Repo  string `json:"repo" doc:"One repository. Empty for all."`
}

// Trends.
const (
	TrendBetter  = "better"
	TrendWorse   = "worse"
	TrendFlat    = "flat"
	TrendUnknown = "unknown"
)

// Series is one metric for one slice: its value over the window, the value
// it is compared with, and its history.
type Series struct {
	Metric    string   `json:"metric" doc:"The metric id from the registry, such as review.wait.p50." pattern:"^[a-z]+[.][a-z][a-z0-9_]*([.][a-z][a-z0-9_]*)?$"`
	Slice     Slice    `json:"slice" doc:"The part of the line the series is about."`
	Unit      string   `json:"unit" doc:"What the values count." enum:"per_week,hours,share,count,lines"`
	Value     *float64 `json:"value" doc:"The value over the window. Null when nothing was counted."`
	N         int      `json:"n" doc:"How many items the value is drawn from."`
	Thin      bool     `json:"thin" doc:"True when the value is drawn from fewer items than the metric's minimum sample."`
	Previous  *float64 `json:"previous" doc:"The value over the previous window. Null when the store does not reach that far back or nothing was counted."`
	PreviousN int      `json:"previous_n" doc:"How many items the previous value is drawn from."`
	Change    *float64 `json:"change" doc:"How the value moved against the previous one. For a share it is the difference, so 0.05 is five points. For anything else it is relative, so 0.2 is 20 percent more. Null when either end is missing or the previous value is zero."`
	Trend     string   `json:"trend" doc:"Whether the move is good news, given the direction in which the metric gets better. Flat when the move is too small to call. Unknown when either end is missing or thin, or when the metric is not better in either direction." enum:"better,worse,flat,unknown"`
	Points    []Point  `json:"points" doc:"One value per period of the window's periods, oldest first."`
}

// Point is one period of a series.
type Point struct {
	Value *float64 `json:"value" doc:"The value in the period. Null when nothing was counted."`
	N     int      `json:"n" doc:"How many items the value is drawn from."`
}

// Confidence levels.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Recommendation is one thing to do, with the numbers that led to it.
type Recommendation struct {
	Rule       string     `json:"rule" doc:"The id of the rule that fired, such as review.queue-growing. Rule ids are stable."`
	Station    string     `json:"station" doc:"The station the recommendation is about."`
	Title      string     `json:"title" doc:"What to do, in one line."`
	Why        string     `json:"why" doc:"The reasoning in a sentence or two."`
	Evidence   []Evidence `json:"evidence" doc:"The numbers the rule read."`
	HoursFreed *float64   `json:"hours_freed" doc:"Hours of waiting per week that would go away if the station ran as it did in its own best four weeks of the window. Null when that cannot be sized."`
	Confidence string     `json:"confidence" doc:"How much data stands behind the recommendation." enum:"high,medium,low"`
	PlayURL    string     `json:"play_url" doc:"Link to the playbook page that describes the practice. Empty when there is none."`
	Items      []Item     `json:"items" doc:"The items to look at first. Empty in a summary."`
}

// Evidence is one number behind a recommendation.
type Evidence struct {
	Metric string   `json:"metric" doc:"The metric id." pattern:"^[a-z]+[.][a-z][a-z0-9_]*([.][a-z][a-z0-9_]*)?$"`
	Slice  Slice    `json:"slice" doc:"The part of the line the number is about."`
	Value  *float64 `json:"value" doc:"The value the rule read."`
	Versus *float64 `json:"versus" doc:"The value it was compared with: the previous window, the best four weeks or another slice. Null when the rule compared with a fixed threshold."`
	Text   string   `json:"text" doc:"The number in words."`
}
