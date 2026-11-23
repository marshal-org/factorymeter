package measure

import (
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// Source is where a line reads its records from. The Parquet store of the
// command line is one Source. A program that keeps its records somewhere else,
// such as a database, implements this interface and calls LoadFrom.
//
// A Source hands out one scope at a time. A scope is one repository, or one
// ticket source such as a Linear workspace. For each kind of record it calls
// fn once per record, in any order, and stops at the first error.
//
// The window limits what is read. A zero From or To leaves that end open. A
// Source may return more than the window asks for, never less: the report
// cuts every record to its own window.
type Source interface {
	// Scopes lists every scope the source holds.
	Scopes() []Scope
	// Changes reads the changes of a scope, each with its reviews and commits.
	Changes(sc Scope, w Window, fn func(*record.Change)) error
	// CIRuns reads the workflow runs of a scope.
	CIRuns(sc Scope, w Window, fn func(*record.CIRun)) error
	// Deploys reads the deployments and releases of a scope.
	Deploys(sc Scope, w Window, fn func(*record.Deploy)) error
	// Tickets reads the tickets of a scope.
	Tickets(sc Scope, w Window, fn func(*record.Ticket)) error
	// Links reads the links between records of a scope.
	Links(sc Scope, w Window, fn func(*record.Link)) error
}

// Scope names one place records come from.
type Scope struct {
	// Source is the system the records came from, such as "github".
	Source string
	// Name is the scope's name on the source, such as "owner/repo".
	Name    string
	Private bool
	// From is the time the source holds this scope's records from.
	From time.Time
	// SyncedAt is when the last read of this scope finished. Zero while the
	// first one has not.
	SyncedAt time.Time
	// Notes are limits the last read ran into, in plain sentences.
	Notes []string
}

// Window is a span of time. A zero end is open.
type Window struct {
	From, To time.Time
}

// StoreSource reads from the Parquet store of the command line.
func StoreSource(s *store.Store) Source { return storeSource{s} }

type storeSource struct{ s *store.Store }

func (ss storeSource) Scopes() []Scope {
	var out []Scope
	for _, sc := range ss.s.Scopes("") {
		out = append(out, Scope{Source: sc.Source, Name: sc.Name, Private: sc.Private, From: sc.From, SyncedAt: sc.SyncedAt, Notes: sc.Notes})
	}
	return out
}

func (ss storeSource) Changes(sc Scope, w Window, fn func(*record.Change)) error {
	return eachIn(store.Changes, ss.s, sc, w, fn)
}

func (ss storeSource) CIRuns(sc Scope, w Window, fn func(*record.CIRun)) error {
	return eachIn(store.CIRuns, ss.s, sc, w, fn)
}

func (ss storeSource) Deploys(sc Scope, w Window, fn func(*record.Deploy)) error {
	return eachIn(store.Deploys, ss.s, sc, w, fn)
}

func (ss storeSource) Tickets(sc Scope, w Window, fn func(*record.Ticket)) error {
	return eachIn(store.Tickets, ss.s, sc, w, fn)
}

func (ss storeSource) Links(sc Scope, w Window, fn func(*record.Link)) error {
	return eachIn(store.Links, ss.s, sc, w, fn)
}
