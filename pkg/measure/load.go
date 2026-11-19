package measure

import (
	"fmt"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/signature"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// Load reads a target out of a store: one repository for owner/repo, every
// repository of the store for an owner. It reads one repository at a time.
func Load(s *store.Store, reg *signature.Registry, target string) (*Line, error) {
	b := NewBuilder(reg)
	var scopes []store.Scope
	for _, sc := range s.Scopes("") {
		if !strings.Contains(target, "/") || strings.EqualFold(sc.Name, target) {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("the store holds nothing for %s. Run \"factorymeter sync %s\" first", target, target)
	}
	for _, sc := range scopes {
		rb := b.Repo(RepoScope{Name: sc.Name, Private: sc.Private, From: sc.From, SyncedAt: sc.SyncedAt, Notes: sc.Notes})
		if err := each(store.Changes, s, sc, rb.Change); err != nil {
			return nil, err
		}
		if err := each(store.CIRuns, s, sc, rb.CIRun); err != nil {
			return nil, err
		}
		if err := each(store.Deploys, s, sc, rb.Deploy); err != nil {
			return nil, err
		}
		if err := each(store.Links, s, sc, rb.Link); err != nil {
			return nil, err
		}
		rb.Done()
	}
	// Tickets come last: a ticket starts when its first linked change opens,
	// and the links are known only once every repository is read.
	for _, sc := range scopes {
		if err := each(store.Tickets, s, sc, b.Ticket); err != nil {
			return nil, err
		}
	}
	return b.Line(), nil
}

func each[T any](t store.Table[T], s *store.Store, sc store.Scope, fn func(*T)) error {
	return t.Each(s, sc.Source, sc.Name, time.Time{}, time.Time{}, func(_ string, rows []T) error {
		for i := range rows {
			fn(&rows[i])
		}
		return nil
	})
}
