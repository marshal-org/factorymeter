package measure

import (
	"encoding/json"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// memSource holds records in memory, the way a program with a database would
// hand them to LoadFrom.
type memSource struct {
	scopes  []Scope
	changes map[string][]record.Change
	ci      map[string][]record.CIRun
	deploys map[string][]record.Deploy
	tickets map[string][]record.Ticket
	links   map[string][]record.Link
}

func scopeKey(sc Scope) string { return store.Key(sc.Source, sc.Name) }

func (m *memSource) Scopes() []Scope { return m.scopes }

func (m *memSource) Changes(sc Scope, _ Window, fn func(*record.Change)) error {
	rows := m.changes[scopeKey(sc)]
	for i := range rows {
		fn(&rows[i])
	}
	return nil
}

func (m *memSource) CIRuns(sc Scope, _ Window, fn func(*record.CIRun)) error {
	rows := m.ci[scopeKey(sc)]
	for i := range rows {
		fn(&rows[i])
	}
	return nil
}

func (m *memSource) Deploys(sc Scope, _ Window, fn func(*record.Deploy)) error {
	rows := m.deploys[scopeKey(sc)]
	for i := range rows {
		fn(&rows[i])
	}
	return nil
}

func (m *memSource) Tickets(sc Scope, _ Window, fn func(*record.Ticket)) error {
	rows := m.tickets[scopeKey(sc)]
	for i := range rows {
		fn(&rows[i])
	}
	return nil
}

func (m *memSource) Links(sc Scope, _ Window, fn func(*record.Link)) error {
	rows := m.links[scopeKey(sc)]
	for i := range rows {
		fn(&rows[i])
	}
	return nil
}

// copyOf reads every record of a source into memory.
func copyOf(t *testing.T, src Source) *memSource {
	t.Helper()
	m := &memSource{scopes: src.Scopes(), changes: map[string][]record.Change{}, ci: map[string][]record.CIRun{},
		deploys: map[string][]record.Deploy{}, tickets: map[string][]record.Ticket{}, links: map[string][]record.Link{}}
	for _, sc := range m.scopes {
		k := scopeKey(sc)
		for _, err := range []error{
			src.Changes(sc, Window{}, func(c *record.Change) { m.changes[k] = append(m.changes[k], *c) }),
			src.CIRuns(sc, Window{}, func(c *record.CIRun) { m.ci[k] = append(m.ci[k], *c) }),
			src.Deploys(sc, Window{}, func(d *record.Deploy) { m.deploys[k] = append(m.deploys[k], *d) }),
			src.Tickets(sc, Window{}, func(x *record.Ticket) { m.tickets[k] = append(m.tickets[k], *x) }),
			src.Links(sc, Window{}, func(l *record.Link) { m.links[k] = append(m.links[k], *l) }),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return m
}

// A line read from memory is the line read from the store the records came from.
func TestLoadFromAnotherSourceMatchesTheStore(t *testing.T) {
	s := linearStore(t, end)
	for _, target := range []string{"acme", repo} {
		want, err := Load(s, registry(t), target)
		if err != nil {
			t.Fatal(err)
		}
		got, err := LoadFrom(copyOf(t, StoreSource(s)), registry(t), target, nil)
		if err != nil {
			t.Fatal(err)
		}
		opts := Options{Target: target, Weeks: 2, History: 4, Now: end}
		a, err := want.Report(opts)
		if err != nil {
			t.Fatal(err)
		}
		b, err := got.Report(opts)
		if err != nil {
			t.Fatal(err)
		}
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s: the report from memory differs from the report from the store", target)
		}
	}
}

func TestLoadFromAnEmptySourceSaysSo(t *testing.T) {
	if _, err := LoadFrom(&memSource{}, registry(t), "acme/none", nil); err == nil {
		t.Fatal("an empty source gave a line")
	}
}

// reversed returns a copy of m with every list of records in the opposite
// order, as a database that does not return store order would hand them out.
func reversed(m *memSource) *memSource {
	r := &memSource{scopes: append([]Scope(nil), m.scopes...), changes: map[string][]record.Change{}, ci: map[string][]record.CIRun{},
		deploys: map[string][]record.Deploy{}, tickets: map[string][]record.Ticket{}, links: map[string][]record.Link{}}
	for k, v := range m.changes {
		for i := len(v) - 1; i >= 0; i-- {
			r.changes[k] = append(r.changes[k], v[i])
		}
	}
	for k, v := range m.ci {
		for i := len(v) - 1; i >= 0; i-- {
			r.ci[k] = append(r.ci[k], v[i])
		}
	}
	for k, v := range m.deploys {
		for i := len(v) - 1; i >= 0; i-- {
			r.deploys[k] = append(r.deploys[k], v[i])
		}
	}
	for k, v := range m.tickets {
		for i := len(v) - 1; i >= 0; i-- {
			r.tickets[k] = append(r.tickets[k], v[i])
		}
	}
	for k, v := range m.links {
		for i := len(v) - 1; i >= 0; i-- {
			r.links[k] = append(r.links[k], v[i])
		}
	}
	return r
}

// The Source says records come in any order, so the report and the ticket
// list must not depend on it.
func TestTheOrderOfRecordsChangesNothing(t *testing.T) {
	s := linearStore(t, end)
	fwd := copyOf(t, StoreSource(s))
	rev := reversed(fwd)
	for _, target := range []string{"acme", repo} {
		var out [2]string
		for i, src := range []Source{fwd, rev} {
			l, err := LoadFrom(src, registry(t), target, nil)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := l.Report(Options{Target: target, Weeks: 2, History: 4, Now: end})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(rep)
			out[i] = string(b)
		}
		if out[0] != out[1] {
			t.Errorf("%s: the report depends on the order of the records", target)
		}
		a, err := TicketsFrom(fwd, target)
		if err != nil {
			t.Fatal(err)
		}
		b, err := TicketsFrom(rev, target)
		if err != nil {
			t.Fatal(err)
		}
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s: the ticket list depends on the order of the records", target)
		}
	}
}

// Tickets over a Source lists what Tickets over the store lists.
func TestTicketsFromASourceMatchesTheStore(t *testing.T) {
	s := linearStore(t, end)
	for _, target := range []string{"acme", repo} {
		want, err := Tickets(s, target)
		if err != nil {
			t.Fatal(err)
		}
		got, err := TicketsFrom(copyOf(t, StoreSource(s)), target)
		if err != nil {
			t.Fatal(err)
		}
		jw, _ := json.Marshal(want)
		jg, _ := json.Marshal(got)
		if string(jw) != string(jg) || len(want) == 0 {
			t.Errorf("%s: %d rows from the store, %d from the source", target, len(want), len(got))
		}
	}
}
