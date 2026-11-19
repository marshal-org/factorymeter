package ingest

import (
	"time"
)

// Step is one part of the sync of a scope.
type Step string

const (
	StepChanges Step = "pull requests"
	StepIssues  Step = "issues"
	StepCI      Step = "CI runs"
	StepDeploys Step = "deploys"
	// StepTickets is the one step of a ticket system, such as Linear or Jira.
	StepTickets Step = "tickets"
)

// Steps are the steps a sync of a repository takes with these options, in
// the order it takes them.
func (o Options) Steps() []Step {
	steps := []Step{StepChanges}
	if !o.SkipTickets {
		steps = append(steps, StepIssues)
	}
	if !o.SkipCI {
		steps = append(steps, StepCI)
	}
	if !o.SkipDeploys {
		steps = append(steps, StepDeploys)
	}
	return steps
}

// Kind says what an Event tells.
type Kind int

const (
	// Listed tells which scopes the sync reads. Total holds their number and
	// Names their names.
	Listed Kind = iota + 1
	// ScopeStarted tells that the sync of a scope began. First and Last say
	// what the store knew of the scope.
	ScopeStarted
	// StepStarted, StepAdvanced and StepDone follow one step of a scope.
	StepStarted
	StepAdvanced
	StepDone
	// Noted carries a sentence a person should see, in Detail: a read that
	// goes on from where it stopped, or a page that had to be asked for again.
	Noted
	// ScopeDone tells that the sync of a scope ended, well or not.
	ScopeDone
)

// Event is one piece of news from a running sync. A display draws progress
// from the events. Nothing in a sync depends on them.
type Event struct {
	Kind Kind
	// Source is the system that is read, such as "github".
	Source string
	// Slot is the place of the scope in the list of the sync. It stays the
	// same from the first event of a scope to the last, while Scope can change
	// its spelling to the one the source uses.
	Slot  int
	Scope string
	// Total is the number of scopes, in a Listed event, and Names are their
	// names by slot.
	Total int
	Names []string
	// First is set in a ScopeStarted event when no sync of the scope has
	// finished yet. Last is how long the last finished one took, and LastFull
	// whether that one read the whole window. Both are zero when unknown.
	First    bool
	Last     time.Duration
	LastFull bool
	// LastAt is when the last finished sync ran.
	LastAt time.Time

	Step Step
	// Read is the number of records the step has read from the source so far.
	Read int
	// Share is how much of the step is done, from 0 to 1. It is 0 while the
	// step cannot tell. It goes back when a step finds more work than it
	// knew of.
	Share float64
	// At is the time in the window the read has reached. Zero when the step
	// does not walk the window.
	At time.Time
	// Detail is a sentence for a Noted event.
	Detail string
	// Added and Changed say what a finished step did to the store.
	Added, Changed int

	// Took is how long a finished step or scope took.
	Took time.Duration
	// Repo is the outcome of a repository, in its ScopeDone event. Tickets is
	// the outcome of a ticket system.
	Repo    *RepoResult
	Tickets *SourceResult
	// Err is why a step or a scope ended early.
	Err error
}

func (o Options) emit(e Event) {
	if o.Progress != nil {
		o.Progress(e)
	}
}

// part is how much of the stretch between two times a read has covered once
// it stands at a third. The read runs from start to end, in either direction.
func part(start, end, at time.Time) float64 {
	total := end.Sub(start)
	if total == 0 {
		return 1
	}
	p := float64(at.Sub(start)) / float64(total)
	switch {
	case p < 0:
		return 0
	case p > 1:
		return 1
	}
	return p
}

// tracker follows the step a scope is in, so every event of the step carries
// the same names.
type tracker struct {
	o      Options
	source string
	slot   int
	name   func() string

	step  Step
	read  int
	share float64
	at    time.Time
	began time.Time
}

func (t *tracker) emit(e Event) {
	e.Source, e.Slot, e.Scope = t.source, t.slot, t.name()
	t.o.emit(e)
}

func (t *tracker) start(s Step) {
	t.step, t.read, t.share, t.at, t.began = s, 0, 0, time.Time{}, time.Now()
	t.emit(Event{Kind: StepStarted, Step: s})
}

// advance tells that the step read n more records. A share below the one
// already told is left out: a step that reads its window in several passes
// never shows less than it did.
func (t *tracker) advance(n int, share float64, at time.Time) {
	t.read += n
	if share > t.share {
		t.share = share
	}
	if !at.IsZero() {
		t.at = at
	}
	t.emit(Event{Kind: StepAdvanced, Step: t.step, Read: t.read, Share: t.share, At: t.at})
}

// revise is advance for a step that learns how much work it has as it goes:
// the share is taken as told, also when it is below the one before.
func (t *tracker) revise(n int, share float64, at time.Time) {
	t.share = 0
	t.advance(n, share, at)
}

func (t *tracker) done(added, changed int, err error) {
	t.emit(Event{Kind: StepDone, Step: t.step, Read: t.read, Share: 1, Added: added, Changed: changed, Took: time.Since(t.began), Err: err})
}

func (t *tracker) note(detail string) {
	t.emit(Event{Kind: Noted, Step: t.step, Detail: detail})
}
