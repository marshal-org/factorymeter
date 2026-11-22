package line

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind groups metrics. Every station has the same five where they can be
// observed: flow, wait, work, rework and queue. Held is the time behind the
// constraint. Other holds the few numbers that fit none of them.
type Kind string

const (
	Flow   Kind = "flow"
	Wait   Kind = "wait"
	Work   Kind = "work"
	Rework Kind = "rework"
	Queue  Kind = "queue"
	Held   Kind = "held"
	Other  Kind = "other"
)

// Kinds lists the kinds in the order a station shows them.
var Kinds = []Kind{Flow, Wait, Work, Rework, Queue, Held, Other}

// KindDoes says what each kind of number answers.
var KindDoes = map[Kind]string{
	Flow:   "How many items leave the station each week.",
	Wait:   "How long an item waits before the station starts on it.",
	Work:   "How long the station works on an item.",
	Rework: "The share of items the station sends back or has to redo.",
	Queue:  "How many items stand at the station at the end of the period, and how old the oldest is.",
	Held:   "The hours items spend at the station, wait and work together. The constraint is the station with the most.",
	Other:  "Numbers that fit none of the five.",
}

// Unit is what a value counts.
type Unit string

const (
	PerWeek Unit = "per_week"
	Hours   Unit = "hours"
	Share   Unit = "share"
	Count   Unit = "count"
	Lines   Unit = "lines"
)

// Units lists the units in the order the docs print them.
var Units = []Unit{PerWeek, Hours, Share, Count, Lines}

// UnitDoes says how to read a value of each unit.
var UnitDoes = map[Unit]string{
	PerWeek: "Items per week: the count in the period divided by the length of the period in weeks.",
	Hours:   "Hours of wall-clock time. Nights and weekends count.",
	Share:   "A share between 0 and 1. 0.25 is 25 percent.",
	Count:   "A plain count at one moment.",
	Lines:   "Lines of code.",
}

// Good is the direction in which a metric gets better.
type Good string

const (
	Up   Good = "up"
	Down Good = "down"
	// Neither is for a number that is not better either way, such as the
	// share of changes written by agents.
	Neither Good = "neither"
)

// Slices a metric can be cut by.
const (
	// SliceClass cuts by who wrote the change: agent, human or automation.
	SliceClass = "class"
	// SliceAgent cuts agent changes by the agent that wrote them.
	SliceAgent = "agent"
	// SliceRepo cuts by repository.
	SliceRepo = "repo"
)

// MinSample is the fewest items a median, a percentile or a share is drawn
// from before the report trusts it. A value drawn from fewer is still
// printed, and marked.
const MinSample = 5

// Metric defines one number of the report. The id is the contract: it never
// changes meaning. A metric whose definition has to change gets a new id.
type Metric struct {
	// ID is "station.kind" or "station.kind.stat", for example
	// "review.wait.p50".
	ID      string `json:"id"`
	Station string `json:"station"`
	Kind    Kind   `json:"kind"`
	Name    string `json:"name"`
	Unit    Unit   `json:"unit"`
	Good    Good   `json:"good"`
	// MinSample is the fewest items the value should be drawn from. Zero for
	// a plain count.
	MinSample int `json:"min_sample"`
	// Slices lists what the metric can be cut by, besides the whole line.
	Slices []string `json:"slices"`
	// Needs lists the record kinds the metric is computed from.
	Needs []string `json:"needs"`
	// Definition says exactly what is counted, so a value can be checked by
	// hand.
	Definition string `json:"definition"`
	// Since is the contract version that introduced the metric.
	Since string `json:"since"`
}

// Registry is every metric, in the order the docs list them.
var Registry = build()

var byID = func() map[string]Metric {
	m := make(map[string]Metric, len(Registry))
	for _, x := range Registry {
		m[x.ID] = x
	}
	return m
}()

// ByID finds a metric by its id.
// EarlyCommitDays is how long before a change was opened a commit may be
// written and still start it. An older commit is history the change carries,
// such as the commits of a branch it brings in, or a commit with a wrong date.
const EarlyCommitDays = 90

func ByID(id string) (Metric, bool) {
	m, ok := byID[id]
	return m, ok
}

// ForStation lists the metrics of one station, or of the whole line for
// Whole, in registry order.
func ForStation(station string) []Metric {
	var out []Metric
	for _, m := range Registry {
		if m.Station == station {
			out = append(out, m)
		}
	}
	return out
}

// IDPattern is the form of a metric id. It is written without a backslash so
// it can sit in a struct tag.
const IDPattern = `^[a-z]+[.][a-z][a-z0-9_]*([.][a-z][a-z0-9_]*)?$`

var idForm = regexp.MustCompile(IDPattern)

func known[T comparable](v T, list ...T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Validate checks a list of metrics against the rules of the contract. It
// returns every problem it finds.
func Validate(list []Metric) []error {
	var errs []error
	bad := func(m Metric, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", m.ID, fmt.Sprintf(format, a...)))
	}
	seen := map[string]bool{}
	for _, m := range list {
		if !idForm.MatchString(m.ID) {
			bad(m, "the id must look like station.kind or station.kind.stat")
			continue
		}
		if seen[m.ID] {
			bad(m, "the id is used twice")
		}
		seen[m.ID] = true
		parts := strings.Split(m.ID, ".")
		st, isStation := StationByID(m.Station)
		if parts[0] != m.Station || !isStation && m.Station != Whole {
			bad(m, "the id must start with a station id or %q, and match the station field %q", Whole, m.Station)
		}
		if !known(m.Kind, Kinds...) {
			bad(m, "unknown kind %q", m.Kind)
		} else if m.Kind != Other && parts[1] != string(m.Kind) {
			bad(m, "the second part of the id must be the kind %q", m.Kind)
		}
		if isStation {
			if why, off := st.NotObservable[m.Kind]; off {
				bad(m, "the station says %s cannot be observed: %s", m.Kind, why)
			}
			if m.Kind == Held && !st.InConstraint {
				bad(m, "the station is left out of the constraint and has no held hours")
			}
		}
		if !known(m.Unit, Units...) {
			bad(m, "unknown unit %q", m.Unit)
		}
		if !known(m.Good, Up, Down, Neither) {
			bad(m, "unknown direction %q", m.Good)
		}
		if m.MinSample < 0 {
			bad(m, "the minimum sample is negative")
		}
		for _, s := range m.Slices {
			if !known(s, SliceClass, SliceAgent, SliceRepo) {
				bad(m, "unknown slice %q", s)
			}
		}
		if len(m.Needs) == 0 {
			bad(m, "no record kind is needed, so nothing could compute it")
		}
		for _, n := range m.Needs {
			if !known(n, NeedTicket, NeedChange, NeedCIRun, NeedDeploy) {
				bad(m, "unknown record kind %q", n)
			}
		}
		if strings.TrimSpace(m.Name) == "" {
			bad(m, "no name")
		}
		if d := strings.TrimSpace(m.Definition); d == "" || !strings.HasSuffix(d, ".") {
			bad(m, "the definition must be a sentence that ends with a full stop")
		}
		if m.Since == "" {
			bad(m, "no contract version")
		}
	}
	// Every station offers each of the five numbers or says why it cannot.
	for _, st := range Stations {
		for _, k := range []Kind{Flow, Wait, Work, Rework, Queue} {
			_, off := st.NotObservable[k]
			has := false
			for _, m := range list {
				has = has || m.Station == st.ID && m.Kind == k
			}
			if !has && !off {
				errs = append(errs, fmt.Errorf("%s: no %s metric and no reason why it cannot be observed", st.ID, k))
			}
		}
		if st.InConstraint == (st.Why != "") {
			errs = append(errs, fmt.Errorf("%s: a station gives a reason exactly when it is left out of the constraint", st.ID))
		}
	}
	return errs
}
