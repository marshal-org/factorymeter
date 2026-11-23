package measure

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// How a pull request was tied to a ticket.
const (
	ByAttachment = "attached" // the ticket system attached it, as Linear does
	ByCloses     = "closes"   // GitHub says it closes the issue
	ByKey        = "key"      // its branch, title or description names the key
)

// TicketPull is a pull request tied to a ticket.
type TicketPull struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Opened time.Time `json:"opened"`
	By     []string  `json:"by"`
}

// TicketRow is a ticket of the store with the pull requests tied to it, as
// the report sees them.
type TicketRow struct {
	Source   string     `json:"source"`
	Key      string     `json:"key"`
	Aliases  []string   `json:"aliases"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	RawState string     `json:"raw_state"`
	Created  time.Time  `json:"created"`
	Started  *time.Time `json:"started"`
	Done     *time.Time `json:"done"`
	Closed   *time.Time `json:"closed"`
	Reopened int        `json:"reopened"`
	// Start is when the spec station takes the ticket to start. Nil is a
	// ticket that never started.
	Start *time.Time   `json:"start"`
	Pulls []TicketPull `json:"pulls"`
}

// Tickets lists the tickets of a store with the pull requests tied to them,
// by the same rules as the report: the links the ticket systems store, and
// the keys that the branch, title and description of a change name. For
// owner/repo only that repository's pull requests are looked at, and only
// tickets of other systems that one of them is tied to are listed.
func Tickets(s *store.Store, target string) ([]TicketRow, error) {
	return TicketsFrom(StoreSource(s), target)
}

// TicketsFrom is Tickets over any Source. It reads every record through the
// Source and does not depend on the order the Source returns them in.
func TicketsFrom(src Source, target string) ([]TicketRow, error) {
	one := strings.Contains(target, "/")
	var repos, sources []Scope
	for _, sc := range src.Scopes() {
		switch {
		case sc.Source != record.GitHub:
			sources = append(sources, sc)
		case !one || strings.EqualFold(sc.Name, target):
			repos = append(repos, sc)
		}
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("the store holds nothing for %s. Run \"factorymeter sync %s\" first", target, target)
	}

	var all []record.Ticket
	keys := ticketKeys{id: map[string]string{}}
	type tie struct {
		change string
		by     string
	}
	ties := map[string][]tie{} // ticket id -> changes
	for _, sc := range sources {
		err := src.Tickets(sc, Window{}, func(t *record.Ticket) {
			all = append(all, *t)
			keys.add(t)
		})
		if err != nil {
			return nil, err
		}
		err = src.Links(sc, Window{}, func(l *record.Link) {
			if l.FromKind == record.KindChange && l.ToKind == record.KindTicket {
				ties[l.To] = append(ties[l.To], tie{strings.ToLower(l.From), ByAttachment})
			}
		})
		if err != nil {
			return nil, err
		}
	}
	keys.done()

	opened := map[string]time.Time{} // change id, in lower case -> opened
	inRepo := map[string]bool{}      // ticket ids of the repositories' own issues
	for _, sc := range repos {
		err := src.Changes(sc, Window{}, func(c *record.Change) {
			id := strings.ToLower(record.ChangeID(record.GitHub, c.Repo, c.Number))
			opened[id] = c.CreatedAt
			for _, tid := range keys.in(c.HeadRef, c.Title, c.Body) {
				ties[tid] = append(ties[tid], tie{id, ByKey})
			}
		})
		if err != nil {
			return nil, err
		}
		err = src.Links(sc, Window{}, func(l *record.Link) {
			if l.ToKind == record.KindTicket && l.Kind != record.LinkReverts {
				ties[l.To] = append(ties[l.To], tie{strings.ToLower(l.From), ByCloses})
			}
		})
		if err != nil {
			return nil, err
		}
		err = src.Tickets(sc, Window{}, func(t *record.Ticket) {
			all = append(all, *t)
			inRepo[t.ID] = true
		})
		if err != nil {
			return nil, err
		}
	}

	var rows []TicketRow
	for i := range all {
		t := &all[i]
		byChange := map[string]*TicketPull{}
		var pulls []*TicketPull
		for _, x := range ties[t.ID] {
			at, ok := opened[x.change]
			if !ok {
				continue // a pull request of a repository that is not read
			}
			p := byChange[x.change]
			if p == nil {
				repo, num, _ := pullOf(x.change)
				p = &TicketPull{Repo: repo, Number: num, Opened: at}
				byChange[x.change] = p
				pulls = append(pulls, p)
			}
			if !has(p.By, x.by) {
				p.By = append(p.By, x.by)
			}
		}
		if one && !inRepo[t.ID] && len(pulls) == 0 {
			continue
		}
		sort.Slice(pulls, func(a, b int) bool { return pulls[a].Opened.Before(pulls[b].Opened) })
		var first time.Time
		row := TicketRow{Source: t.Source, Key: t.Key, Aliases: t.Aliases, Title: t.Title, State: t.State, RawState: t.RawState,
			Created: t.CreatedAt, Started: t.StartedAt, Done: t.DoneAt, Closed: t.ClosedAt, Reopened: t.Reopened, Pulls: []TicketPull{}}
		for _, p := range pulls {
			sort.Strings(p.By)
			row.Pulls = append(row.Pulls, *p)
		}
		if len(pulls) > 0 {
			first = pulls[0].Opened
		}
		if start := SpecStart(t, first); !start.IsZero() {
			row.Start = &start
		}
		if row.Aliases == nil {
			row.Aliases = []string{}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].Source != rows[b].Source {
			return rows[a].Source < rows[b].Source
		}
		return keyLess(rows[a].Key, rows[b].Key)
	})
	return rows, nil
}

// keyLess orders keys by prefix, then by number: ENG-2 before ENG-10.
func keyLess(a, b string) bool {
	pa, na := splitKey(a)
	pb, nb := splitKey(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitKey(k string) (string, int) {
	i := strings.LastIndexAny(k, "-#")
	if i < 0 {
		return k, 0
	}
	n := 0
	for _, c := range k[i+1:] {
		if c < '0' || c > '9' {
			return k, 0
		}
		n = n*10 + int(c-'0')
	}
	return k[:i], n
}
