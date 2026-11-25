// Package line is the model of the delivery line: the stations work passes
// through and the registry of every number factorymeter reports about them.
//
// It holds definitions only. Nothing here reads a repository or computes a
// value. The docs under docs/reference are generated from this package, so a
// definition is written once.
package line

// Station ids, in the order work passes through them.
const (
	Spec    = "spec"
	Code    = "code"
	CI      = "ci"
	Verify  = "verify"
	Review  = "review"
	Merge   = "merge"
	Release = "release"
	// Whole is not a station. It holds the numbers about the line as a whole.
	Whole = "line"
)

// Record kinds a station or a metric can need. They match pkg/record.
const (
	NeedTicket = "ticket"
	NeedChange = "change"
	NeedCIRun  = "ci_run"
	NeedDeploy = "deploy"
)

// Station is one step of the line.
type Station struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Does says what happens at the station.
	Does string `json:"does"`
	// Item is what moves through it: a ticket, a change, a CI round or a deploy.
	Item string `json:"item"`
	// Arrives and Leaves say when an item enters and leaves the station.
	Arrives string `json:"arrives"`
	Leaves  string `json:"leaves"`
	// Needs lists the record kinds the station is measured from. A station
	// with none of them in the store is shown as not connected.
	Needs []string `json:"needs"`
	// InConstraint is false for a station that can never be named the
	// constraint. Why says why.
	InConstraint bool   `json:"in_constraint"`
	Why          string `json:"why,omitempty"`
	// NotObservable maps a metric kind to the reason the station has no such
	// number.
	NotObservable map[Kind]string `json:"not_observable,omitempty"`
}

// Stations is the line, first station first.
var Stations = []Station{
	{
		ID: Spec, Name: "Spec", Item: "ticket", Needs: []string{NeedTicket},
		Does:    "A ticket is written and waits for someone to start on it.",
		Arrives: "The ticket is created.",
		Leaves:  "Work starts: the ticket first moves to a started state, or its first linked change is opened, whichever comes first.",
		Why:     "A backlog holds tickets on purpose, often for months. Counting that as waiting would name spec the constraint in every repository.",
		NotObservable: map[Kind]string{
			Work: "Writing a ticket leaves no start time.",
			Held: "Spec is left out of the constraint, so its hours are not added up.",
		},
	},
	{
		ID: Code, Name: "Code", Item: "change", Needs: []string{NeedChange}, InConstraint: true,
		Does:    "The change is written.",
		Arrives: "The first commit of the change is written or the change is opened, whichever is earlier.",
		Leaves:  "The change is marked ready for review. A change opened ready leaves when it is opened.",
		NotObservable: map[Kind]string{
			Wait: "Code has no queue of its own. A ticket waiting for someone to start waits at spec.",
		},
	},
	{
		ID: CI, Name: "CI", Item: "CI round", Needs: []string{NeedChange, NeedCIRun}, InConstraint: true,
		Does:    "The checks run on each push to a change. One CI round is every run on one commit of a change.",
		Arrives: "The first run on the commit is created.",
		Leaves:  "The last run on the commit finishes.",
		NotObservable: map[Kind]string{
			Queue: "A CI round lasts minutes. A count of rounds in flight at the end of a week says nothing about the week.",
		},
	},
	{
		ID: Verify, Name: "Verify", Item: "change", Needs: []string{NeedChange, NeedDeploy}, InConstraint: true,
		Does:    "The change is proved on a running environment before it merges.",
		Arrives: "A commit of the change is deployed to an environment that is not production.",
		Leaves:  "That deployment reports success.",
		NotObservable: map[Kind]string{
			Queue: "Nothing records that a change is waiting for an environment.",
		},
	},
	{
		ID: Review, Name: "Review", Item: "change", Needs: []string{NeedChange}, InConstraint: true,
		Does:    "A person or an agent reads the change and approves it or sends it back.",
		Arrives: "The change is marked ready for review.",
		Leaves:  "The last approval before the merge. A change that merges after a review with no approval, or with no review seen, leaves at the merge.",
	},
	{
		ID: Merge, Name: "Merge", Item: "change", Needs: []string{NeedChange}, InConstraint: true,
		Does:    "The approved change lands on the main branch.",
		Arrives: "The change leaves review: at its last approval, or at the merge when it had none.",
		Leaves:  "The change merges.",
		NotObservable: map[Kind]string{
			Work: "A merge is one click. It has no start time apart from its end.",
		},
	},
	{
		ID: Release, Name: "Release", Item: "change", Needs: []string{NeedChange, NeedDeploy}, InConstraint: true,
		Does:    "The merged change reaches production.",
		Arrives: "The change merges.",
		Leaves:  "The first production deploy that holds it. A change merged before the first production deploy in the store is not counted.",
	},
}

// StationByID finds a station. The second result is false for an unknown id
// and for Whole, which is not a station.
func StationByID(id string) (Station, bool) {
	for _, s := range Stations {
		if s.ID == id {
			return s, true
		}
	}
	return Station{}, false
}
