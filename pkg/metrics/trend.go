package metrics

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// Units a series can be measured in.
const (
	UnitPerWeek = "per_week"
	UnitCount   = "count"
	UnitHours   = "hours"
	UnitMinutes = "minutes"
	UnitShare   = "share"
	UnitLines   = "lines"
	UnitRuns    = "runs"
)

// Classes a series can describe.
const (
	ClassAgent      = "agent"
	ClassHuman      = "human"
	ClassAutomation = "automation"
	// ClassLine is agent and human pull requests together.
	ClassLine = "line"
)

// Groups of series, in the order the trend prints them.
const (
	GroupThroughput = "Throughput"
	GroupFlow       = "Where the time goes"
	GroupReview     = "Review"
	GroupCI         = "CI"
	GroupSize       = "Size"
	GroupAgents     = "Disclosed agents"
)

// Groups lists the groups in print order.
var Groups = []string{GroupThroughput, GroupFlow, GroupReview, GroupCI, GroupSize, GroupAgents}

// ShareStep is how many points a share must move before the trend calls it a
// change. RelativeStep is the same for every other number, as a fraction.
const (
	ShareStep    = 0.05
	RelativeStep = 0.20
)

// Point is one value of a series, with the number of pull requests, runs or
// deploys it was drawn from.
type Point struct {
	Value *float64 `json:"value"`
	N     int      `json:"n"`
}

// Thin reports whether the point rests on fewer than MinSample items.
func (p Point) Thin() bool { return p.Value != nil && p.N < MinSample }

// Series is one number followed over the periods.
type Series struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	// Metric is the id shared by the agent and human series of one number.
	Metric string `json:"metric"`
	Name   string `json:"name"`
	Class  string `json:"class"`
	Unit   string `json:"unit"`
	// Points holds one value per period, oldest first.
	Points []Point `json:"points"`
	// First and Last are the same number over the first and the last periods
	// of the window taken together. Trend.Span says how many periods that is.
	First Point `json:"first"`
	Last  Point `json:"last"`
	// Change is Last over First minus one. For a share it is Last minus First,
	// in points. Nil when either end is missing, or First is zero.
	Change *float64 `json:"change"`
	// Notable is true when the change is large enough, and rests on enough
	// pull requests, to be called a change.
	Notable bool `json:"notable"`

	subject string
	quiet   bool
}

// Period is one slice of the window.
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Constraint is nil when too few pull requests merged in the period.
	Constraint *Constraint `json:"constraint"`
	// StageShares is each stage's share of the time from open to merge.
	StageShares map[string]float64 `json:"stage_shares"`
	Agent       ClassStats         `json:"agent"`
	Human       ClassStats         `json:"human"`
	Automation  ClassStats         `json:"automation"`
	Line        ClassStats         `json:"line"`

	DisclosedAgentShare *float64     `json:"disclosed_agent_share"`
	Agents              []AgentCount `json:"agents"`
	Deploys             int          `json:"deploys"`
	// WaitingForReview counts the pull requests that stood in the review
	// queue when the period ended.
	WaitingForReview int     `json:"waiting_for_first_review_at_end"`
	OldestWaitingH   float64 `json:"oldest_waiting_hours"`
}

// End is the report of the first or the last periods of the window.
type End struct {
	From       time.Time   `json:"from"`
	To         time.Time   `json:"to"`
	Constraint *Constraint `json:"constraint"`
	Merged     int         `json:"merged"`
}

// Trend is the change over time: the same numbers as the report, once per
// period.
type Trend struct {
	Target          string    `json:"target"`
	Repos           []string  `json:"repos"`
	Since           time.Time `json:"since"`
	Until           time.Time `json:"until"`
	GeneratedAt     time.Time `json:"generated_at"`
	Version         string    `json:"factorymeter_version"`
	RegistryVersion int       `json:"registry_version"`

	// PeriodDays is the length of one period.
	PeriodDays float64 `json:"period_days"`
	// Span is how many periods make up each end of the comparison.
	Span  int `json:"span_periods"`
	First End `json:"first"`
	Last  End `json:"last"`

	Headline string   `json:"headline"`
	Findings []string `json:"findings"`

	Periods []Period `json:"periods"`
	Series  []Series `json:"series"`
	Limits  []string `json:"limits"`
	Notes   []string `json:"notes,omitempty"`
}

// TrendOptions tune ComputeTrend.
type TrendOptions struct {
	Options
	// Every is the length of one period.
	Every time.Duration
}

// seriesDef describes one number the trend follows. get reads it from the
// report of one period.
type seriesDef struct {
	metric, group, name, unit string
	// subject starts the sentence that reports a change. A "%s" in it takes
	// the class.
	subject string
	classes []string
	quiet   bool
	get     func(r *Report, cs *ClassStats) Point
}

func val(v float64, n int) Point { return Point{Value: ptr(v), N: n} }

func optional(v *float64, n int) Point {
	if v == nil {
		return Point{N: n}
	}
	return val(*v, n)
}

func median(d Dist) Point {
	if d.N == 0 {
		return Point{}
	}
	return val(d.Median, d.N)
}

func classOf(r *Report, class string) *ClassStats {
	switch class {
	case ClassAgent:
		return &r.Agent
	case ClassHuman:
		return &r.Human
	case ClassAutomation:
		return &r.Automation
	}
	return &r.Line
}

var both = []string{ClassAgent, ClassHuman}

func seriesDefs(agents []AgentCount) []seriesDef {
	defs := []seriesDef{
		{metric: "merged_per_week", group: GroupThroughput, name: "Pull requests merged per week", unit: UnitPerWeek,
			subject: "%s pull requests merged per week", classes: []string{ClassAgent, ClassHuman, ClassAutomation},
			get: func(r *Report, c *ClassStats) Point { return val(c.MergedPerWeek, c.Merged) }},
		{metric: "disclosed_agent_share", group: GroupThroughput, name: "Disclosed agent share", unit: UnitShare,
			subject: "Disclosed agent share", classes: []string{ClassLine},
			get: func(r *Report, c *ClassStats) Point { return optional(r.DisclosedAgentShare, c.Merged) }},
		{metric: "merge_rate", group: GroupThroughput, name: "Merge rate", unit: UnitShare,
			subject: "The merge rate of %s pull requests", classes: both,
			get: func(r *Report, c *ClassStats) Point { return optional(c.MergeRate, c.Merged+c.ClosedUnmerged) }},
		{metric: "deploys_per_week", group: GroupThroughput, name: "Deploys per week", unit: UnitPerWeek,
			subject: "Deploys per week", classes: []string{ClassLine},
			get: func(r *Report, c *ClassStats) Point {
				if r.DeploysPerWeek == nil {
					return Point{}
				}
				return val(*r.DeploysPerWeek, int(math.Round(*r.DeploysPerWeek*r.Days/7)))
			}},

		{metric: "lead_time", group: GroupFlow, name: "Open to merge, median", unit: UnitHours,
			subject: "The median time from open to merge for %s pull requests", classes: both,
			get: func(r *Report, c *ClassStats) Point { return median(c.LeadTimeH) }},
	}
	for _, s := range Stages {
		id, name := s.ID, s.Name
		defs = append(defs, seriesDef{metric: "stage_share_" + id, group: GroupFlow, name: name + ", share of open to merge", unit: UnitShare,
			subject: "The share of time spent " + s.where, classes: []string{ClassLine}, quiet: true,
			get: func(r *Report, c *ClassStats) Point {
				d := c.Exclusive[id]
				if d.N == 0 {
					return Point{}
				}
				return val(StageShare(*c, id), d.N)
			}})
	}
	for _, s := range Stages {
		id, name := s.ID, s.Name
		defs = append(defs, seriesDef{metric: "stage_" + id, group: GroupFlow, name: name + ", median", unit: UnitHours,
			subject: "The median time %s pull requests " + s.verb + " " + s.object, classes: both, quiet: true,
			get: func(r *Report, c *ClassStats) Point { return median(c.Stages[id]) }})
	}
	defs = append(defs,
		seriesDef{metric: "time_to_first_review", group: GroupReview, name: "Time to first review, median", unit: UnitHours,
			subject: "The median time to first review for %s pull requests", classes: both,
			get: func(r *Report, c *ClassStats) Point { return median(c.TimeToFirstReviewH) }},
		seriesDef{metric: "no_human_review", group: GroupReview, name: "Merged with no human review", unit: UnitShare,
			subject: "The share of %s pull requests merged with no review by another person", classes: both,
			get: func(r *Report, c *ClassStats) Point {
				if c.HumanReviewedShare == nil {
					return Point{}
				}
				return val(1-*c.HumanReviewedShare, c.Merged)
			}},
		seriesDef{metric: "rework_rate", group: GroupReview, name: "Rework rate", unit: UnitShare,
			subject: "The share of reviewed %s pull requests that review sent back", classes: both,
			get: func(r *Report, c *ClassStats) Point { return optional(c.ReworkRate, c.Reviewed) }},
		seriesDef{metric: "waiting_for_review", group: GroupReview, name: "Waiting for first review at the end of the period", unit: UnitCount,
			subject: "The number of pull requests waiting for a first review", classes: []string{ClassLine},
			get: func(r *Report, c *ClassStats) Point { return val(float64(r.WaitingForReview), r.WaitingForReview) }},
		seriesDef{metric: "oldest_waiting", group: GroupReview, name: "Oldest wait for first review at the end of the period", unit: UnitHours,
			subject: "The longest wait for a first review", classes: []string{ClassLine}, quiet: true,
			get: func(r *Report, c *ClassStats) Point {
				if r.WaitingForReview == 0 {
					return Point{}
				}
				return val(r.OldestWaitingH, r.WaitingForReview)
			}},

		seriesDef{metric: "ci_minutes_per_run", group: GroupCI, name: "CI time per run, median", unit: UnitMinutes,
			subject: "The median CI run on %s pull requests", classes: both,
			get: func(r *Report, c *ClassStats) Point { return median(c.CIMinutesPerRun) }},
		seriesDef{metric: "ci_runs_per_pr", group: GroupCI, name: "CI runs per pull request", unit: UnitRuns,
			subject: "CI runs per %s pull request", classes: both,
			get: func(r *Report, c *ClassStats) Point {
				if c.CIRunsPerPR.N == 0 {
					return Point{}
				}
				return val(c.CIRunsPerPR.Mean, c.CIRunsPerPR.N)
			}},
		seriesDef{metric: "ci_failure_rate", group: GroupCI, name: "CI runs that failed", unit: UnitShare,
			subject: "The share of CI runs that failed on %s pull requests", classes: both,
			get: func(r *Report, c *ClassStats) Point { return optional(c.CIFailureRate, c.CIMinutesPerRun.N) }},

		seriesDef{metric: "size_lines", group: GroupSize, name: "Size, median lines changed", unit: UnitLines,
			subject: "The median size of %s pull requests, in lines changed,", classes: both,
			get: func(r *Report, c *ClassStats) Point { return median(c.SizeLines) }},
	)
	for _, a := range agents {
		id := a.ID
		defs = append(defs, seriesDef{metric: "agent_" + id, group: GroupAgents, name: a.Name + ", pull requests merged per week", unit: UnitPerWeek,
			subject: a.Name + " pull requests merged per week", classes: []string{ClassAgent}, quiet: true,
			get: func(r *Report, c *ClassStats) Point {
				for _, x := range r.Agents {
					if x.ID == id && r.Days > 0 {
						return val(float64(x.Merged)/(r.Days/7), x.Merged)
					}
				}
				return val(0, 0)
			}})
	}
	return defs
}

// counted reports whether the unit counts things, so that zero is a value and
// a change from nothing is still a change.
func counted(unit string) bool { return unit == UnitPerWeek || unit == UnitCount }

// floor is the smallest value worth calling a change in each unit.
func floor(unit string) float64 {
	switch unit {
	case UnitHours:
		return 0.25
	case UnitMinutes, UnitPerWeek, UnitCount, UnitRuns:
		return 1
	case UnitLines:
		return 10
	}
	return 0
}

// setChange fills Change and Notable from the two ends.
func (s *Series) setChange() {
	a, b := s.First, s.Last
	if a.Value == nil || b.Value == nil {
		return
	}
	enough := a.N >= MinSample && b.N >= MinSample
	if counted(s.Unit) {
		enough = a.N >= MinSample || b.N >= MinSample
	}
	if s.Unit == UnitShare {
		d := *b.Value - *a.Value
		s.Change = &d
		s.Notable = enough && math.Abs(d) >= ShareStep
		return
	}
	big := math.Max(*a.Value, *b.Value) >= floor(s.Unit)
	if *a.Value == 0 {
		s.Notable = enough && big
		return
	}
	d := *b.Value / *a.Value - 1
	s.Change = &d
	s.Notable = enough && big && math.Abs(d) >= RelativeStep
}

// ComputeTrend cuts the collected window into periods of the same length and
// measures each one the way Compute measures the whole. The periods are
// counted back from the end of the window, so every period is whole and the
// last one ends where the collection ended.
func ComputeTrend(repos []model.RepoData, reg *signature.Registry, opt TrendOptions) (Trend, error) {
	t := Trend{Target: opt.Target, Version: opt.Version, GeneratedAt: opt.Now.UTC(), RegistryVersion: reg.Version}
	if len(repos) == 0 {
		return t, errors.New("nothing was collected")
	}
	if opt.Every <= 0 {
		return t, errors.New("the period must be longer than zero")
	}
	since, until := window(repos, opt.Options)
	n := int(until.Sub(since) / opt.Every)
	if n < 2 {
		return t, fmt.Errorf("the window from %s to %s holds %d whole periods of %s. A trend needs at least 2. Use a longer --since or a shorter --every",
			since.Format("2006-01-02"), until.Format("2006-01-02"), n, daysWord(opt.Every.Hours()/24))
	}
	t.Since, t.Until = until.Add(-time.Duration(n)*opt.Every), until
	t.PeriodDays = opt.Every.Hours() / 24
	t.Span = n / 3
	if t.Span < 1 {
		t.Span = 1
	}
	at := func(from, to time.Time) Report {
		o := opt.Options
		o.Since, o.Until, o.Detail = from, to, false
		return Compute(repos, reg, o)
	}
	whole := at(t.Since, t.Until)
	t.Repos, t.Notes = whole.Repos, whole.Notes

	edge := func(i int) time.Time { return t.Since.Add(time.Duration(i) * opt.Every) }
	reports := make([]Report, n)
	for i := range reports {
		reports[i] = at(edge(i), edge(i+1))
		r := &reports[i]
		p := Period{From: r.Since, To: r.Until, Constraint: r.Constraint, StageShares: map[string]float64{},
			Agent: r.Agent, Human: r.Human, Automation: r.Automation, Line: r.Line,
			DisclosedAgentShare: r.DisclosedAgentShare, Agents: r.Agents,
			WaitingForReview: r.WaitingForReview, OldestWaitingH: r.OldestWaitingH}
		if r.DeploysPerWeek != nil {
			p.Deploys = int(math.Round(*r.DeploysPerWeek * r.Days / 7))
		}
		for _, s := range Stages {
			if r.Line.Exclusive[s.ID].N > 0 {
				p.StageShares[s.ID] = StageShare(r.Line, s.ID)
			}
		}
		t.Periods = append(t.Periods, p)
	}
	first, last := at(edge(0), edge(t.Span)), at(edge(n-t.Span), edge(n))
	t.First = End{From: first.Since, To: first.Until, Constraint: first.Constraint, Merged: first.Line.Merged}
	t.Last = End{From: last.Since, To: last.Until, Constraint: last.Constraint, Merged: last.Line.Merged}

	for _, d := range seriesDefs(whole.Agents) {
		for _, class := range d.classes {
			s := Series{ID: d.metric, Group: d.group, Metric: d.metric, Name: d.name, Class: class, Unit: d.unit, quiet: d.quiet}
			if len(d.classes) > 1 {
				s.ID = d.metric + "." + class
			}
			s.subject = d.subject
			if strings.Contains(d.subject, "%s") {
				s.subject = fmt.Sprintf(d.subject, class)
			}
			any := false
			for i := range reports {
				p := d.get(&reports[i], classOf(&reports[i], class))
				any = any || p.Value != nil && (*p.Value != 0 || !counted(d.unit))
				s.Points = append(s.Points, p)
			}
			// A number the repository never had is left out: a stage nobody
			// passed through, or deploys when none were found.
			if !any {
				continue
			}
			s.First, s.Last = d.get(&first, classOf(&first, class)), d.get(&last, classOf(&last, class))
			s.setChange()
			t.Series = append(t.Series, s)
		}
	}
	t.Headline = trendHeadline(&t)
	t.Findings = trendFindings(&t)
	t.Limits = trendLimits(&t, &whole)
	return t, nil
}

// daysWord names a period length: "week", "day", "14 days".
func daysWord(days float64) string {
	switch days {
	case 7:
		return "1 week"
	case 1:
		return "1 day"
	}
	if days == math.Trunc(days) && int(days)%7 == 0 {
		return fmt.Sprintf("%d weeks", int(days)/7)
	}
	return trim(days) + " days"
}

// PeriodWord is the noun for one period and for several: "week" and "weeks",
// or "period" when a period is not a day or a week long.
func (t *Trend) PeriodWord() (one, many string) {
	switch t.PeriodDays {
	case 7:
		return "week", "weeks"
	case 1:
		return "day", "days"
	}
	return "period", "periods"
}

// SpanWords names one end of the comparison: "8 weeks", "1 week", "4 periods".
func (t *Trend) SpanWords() string {
	one, many := t.PeriodWord()
	return plural(t.Span, one, many)
}

// EndWords names the two ends of the comparison: "first 8 weeks" and "last 8
// weeks", or "first week" and "last week".
func (t *Trend) EndWords() (first, last string) {
	one, _ := t.PeriodWord()
	if t.Span == 1 {
		return "first " + one, "last " + one
	}
	return "first " + t.SpanWords(), "last " + t.SpanWords()
}

// Value prints a point in its unit.
func Value(p Point, unit string) string {
	if p.Value == nil {
		return "n/a"
	}
	v := *p.Value
	switch unit {
	case UnitHours:
		return Hours(v)
	case UnitMinutes:
		return Minutes(v)
	case UnitShare:
		return fmt.Sprintf("%.0f%%", v*100)
	case UnitCount, UnitLines:
		return trim(v)
	}
	// A count per week is a whole number when the period is one week.
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}

// ChangeWords prints the change of a series: "+35%", "-12 points", "new".
func ChangeWords(s *Series) string {
	switch {
	case s.First.Value == nil || s.Last.Value == nil:
		return "n/a"
	case s.Change == nil && *s.Last.Value == 0:
		return "0"
	case s.Change == nil:
		return "new"
	case math.Abs(*s.Change) < 0.005:
		return "0"
	case s.Unit == UnitShare:
		return fmt.Sprintf("%+.0f points", *s.Change*100)
	case *s.Change >= 9.95:
		return "x" + trim(*s.Change+1)
	}
	return fmt.Sprintf("%+.0f%%", *s.Change*100)
}

func constraintWords(c *Constraint) string {
	return fmt.Sprintf("the %s station (%s)", c.Station, strings.ToLower(c.Name))
}

func trendHeadline(t *Trend) string {
	a, b := t.First.Constraint, t.Last.Constraint
	first, last := t.EndWords()
	switch {
	case b == nil && t.Last.Merged == 0:
		return fmt.Sprintf("No agent or human pull request merged in the %s, so there is no constraint to name.", last)
	case b == nil:
		return fmt.Sprintf("Only %s merged in the %s. That is too few to name a constraint: it takes %d.",
			plural(t.Last.Merged, "agent or human pull request", "agent or human pull requests"), last, MinSample)
	case a == nil:
		return fmt.Sprintf("The constraint is now %s, with %.0f%% of the time from open to merge. The %s had too few pull requests to name one.",
			constraintWords(b), b.Share*100, first)
	case a.Stage != b.Stage:
		return fmt.Sprintf("The constraint moved from %s to %s.", constraintWords(a), constraintWords(b))
	case math.Abs(b.Share-a.Share) < ShareStep:
		return fmt.Sprintf("The constraint has not moved. It is still %s, with about %.0f%% of the time from open to merge.", constraintWords(b), b.Share*100)
	}
	verb := "grew"
	if b.Share < a.Share {
		verb = "shrank"
	}
	return fmt.Sprintf("The constraint is still %s. Its share of the time from open to merge %s from %.0f%% to %.0f%%.",
		constraintWords(b), verb, a.Share*100, b.Share*100)
}

// MaxFindings caps the list of changes above the table. The table has all of
// them.
const MaxFindings = 12

func trendFindings(t *Trend) []string {
	one, many := t.PeriodWord()
	first, last := t.EndWords()
	out := []string{fmt.Sprintf("This compares the %s (%s to %s) with the %s (%s to %s).",
		first, t.First.From.Format("2006-01-02"), t.First.To.Format("2006-01-02"),
		last, t.Last.From.Format("2006-01-02"), t.Last.To.Format("2006-01-02"))}

	// How often each stage was the constraint.
	counts, none, order := map[string]int{}, 0, []string{}
	for _, p := range t.Periods {
		if p.Constraint == nil {
			none++
			continue
		}
		if counts[p.Constraint.Stage] == 0 {
			order = append(order, p.Constraint.Stage)
		}
		counts[p.Constraint.Stage]++
	}
	if len(order) > 0 {
		var parts []string
		for _, s := range Stages {
			if c := counts[s.ID]; c > 0 {
				parts = append(parts, fmt.Sprintf("%s in %d", strings.ToLower(s.Name), c))
			}
		}
		line := fmt.Sprintf("Of %d %s, the constraint was %s.", len(t.Periods), many, joinAnd(parts))
		if none > 0 {
			line += fmt.Sprintf(" %s had too few pull requests to name one.", plural(none, one, many))
		}
		out = append(out, line)
	}

	shown, more := 0, 0
	for i := range t.Series {
		s := &t.Series[i]
		if !s.Notable || s.quiet {
			continue
		}
		if shown == MaxFindings {
			more++
			continue
		}
		shown++
		verb := "rose"
		if *s.Last.Value < *s.First.Value {
			verb = "fell"
		}
		out = append(out, fmt.Sprintf("%s %s from %s to %s.", upperFirst(s.subject), verb, Value(s.First, s.Unit), Value(s.Last, s.Unit)))
	}
	if more > 0 {
		out = append(out, fmt.Sprintf("%s changed as well. The table has every number.", plural(more, "more number", "more numbers")))
	}
	if shown == 0 {
		out = append(out, "No number moved far enough to call it a change. The table has every number.")
	}
	return out
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func trendLimits(t *Trend, whole *Report) []string {
	one, many := t.PeriodWord()
	first, last := t.EndWords()
	out := []string{
		fmt.Sprintf("A change is called a change when a share moves by %.0f points or more, or another number by %.0f%% or more, between the %s and the %s. Both ends need at least %d pull requests behind them.",
			ShareStep*100, RelativeStep*100, first, last, MinSample),
		fmt.Sprintf("One %s can hold few pull requests. A value drawn from fewer than %d is marked, and a %s with fewer than %d merged pull requests names no constraint.", one, MinSample, one, MinSample),
		fmt.Sprintf("A pull request counts in the %s it merged in. The %s are counted back from the end of the window, so each one is whole.", one, many),
		"The review queue of a past " + one + " is rebuilt from timestamps. A pull request that is a draft today counts as a draft all along, and one that nobody touched since before the window started is not read.",
	}
	l := limits(whole)
	// The last limit is about the simulator inputs, which a trend does not print.
	return append(out, l[:len(l)-1]...)
}
