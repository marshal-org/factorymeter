package measure

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/signature"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// Load reads a target out of a store: one repository for owner/repo, every
// repository of the store for an owner. It reads one repository at a time.
//
// A scope of GitHub is a repository. A scope of another source, such as a
// Linear workspace, holds tickets only. Its tickets are tied to changes by the
// links the source stores and by the ticket keys that the branch, the title
// and the description of a change name.
func Load(s *store.Store, reg *signature.Registry, target string) (*Line, error) {
	return LoadOnly(s, reg, target, nil)
}

// InTarget says whether a repository of the store belongs to the line of a
// target: the repository itself, or a repository of the owner that is in
// only, or any of them when only is empty.
func InTarget(name, target string, only []string) bool {
	if strings.Contains(target, "/") {
		return strings.EqualFold(name, target)
	}
	if len(only) == 0 {
		return true
	}
	for _, o := range only {
		if strings.EqualFold(o, name) {
			return true
		}
	}
	return false
}

// LoadOptions tune how records become a line.
type LoadOptions struct {
	// HumanReviewsOnly counts only reviews by people at the review station.
	// Agent reviews still show in who reviewed a change.
	HumanReviewsOnly bool
}

// ParseReviews reads the reviews option: "all" (the default) or "human".
func ParseReviews(v string) (LoadOptions, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "all":
		return LoadOptions{}, nil
	case "human":
		return LoadOptions{HumanReviewsOnly: true}, nil
	}
	return LoadOptions{}, fmt.Errorf("reviews must be all or human, got %q", v)
}

// LoadOnly is Load for an owner narrowed to some of its repositories, each
// owner/repo. An empty only reads every repository of the store.
func LoadOnly(s *store.Store, reg *signature.Registry, target string, only []string) (*Line, error) {
	return LoadFrom(StoreSource(s), reg, target, only)
}

// LoadOnlyWith is LoadOnly with options.
func LoadOnlyWith(s *store.Store, reg *signature.Registry, target string, only []string, o LoadOptions) (*Line, error) {
	return LoadFromWith(StoreSource(s), reg, target, only, o)
}

// LoadFrom is LoadOnly for any Source of records, not only the store of the
// command line.
func LoadFrom(src Source, reg *signature.Registry, target string, only []string) (*Line, error) {
	return LoadFromWith(src, reg, target, only, LoadOptions{})
}

// LoadFromWith is LoadFrom with options.
func LoadFromWith(src Source, reg *signature.Registry, target string, only []string, o LoadOptions) (*Line, error) {
	b := NewBuilder(reg)
	if o.HumanReviewsOnly {
		b.HumanReviewsOnly()
	}
	// A line of part of the owner counts only the tickets tied to its
	// changes, as a line of one repository does.
	one := strings.Contains(target, "/") || len(only) > 0
	var repos, sources []Scope
	for _, sc := range src.Scopes() {
		switch {
		case sc.Source != record.GitHub:
			sources = append(sources, sc)
		case InTarget(sc.Name, target, only):
			repos = append(repos, sc)
		}
	}
	if len(repos) == 0 {
		if len(only) > 0 && !strings.Contains(target, "/") {
			return nil, fmt.Errorf("the store holds none of the repositories picked for %s (%s). Run \"factorymeter sync\" first", target, strings.Join(only, ", "))
		}
		return nil, fmt.Errorf("the store holds nothing for %s. Run \"factorymeter sync %s\" first", target, target)
	}
	keys := ticketKeys{id: map[string]string{}}
	stored := map[string][]pullLink{}
	prefixes := make([][]string, len(sources))
	for i, sc := range sources {
		var all []record.Ticket
		err := src.Tickets(sc, Window{}, func(t *record.Ticket) {
			keys.add(t)
			all = append(all, record.Ticket{Key: t.Key, Aliases: t.Aliases})
		})
		if err != nil {
			return nil, err
		}
		prefixes[i] = record.KeyPrefixes(all)
		err = src.Links(sc, Window{}, func(l *record.Link) {
			if l.FromKind != record.KindChange || l.ToKind != record.KindTicket {
				return
			}
			if repo, num, ok := pullOf(l.From); ok {
				stored[repo] = append(stored[repo], pullLink{num: num, ticket: l.To})
			}
		})
		if err != nil {
			return nil, err
		}
	}
	keys.done()

	for _, sc := range repos {
		rb := b.Repo(RepoScope{Name: sc.Name, Private: sc.Private, From: sc.From, SyncedAt: sc.SyncedAt, Notes: sc.Notes})
		var named []pullLink
		err := src.Changes(sc, Window{}, func(c *record.Change) {
			rb.Change(c)
			for _, id := range keys.in(c.HeadRef, c.Title, c.Body) {
				named = append(named, pullLink{num: c.Number, ticket: id})
			}
		})
		if err != nil {
			return nil, err
		}
		if err := src.CIRuns(sc, Window{}, rb.CIRun); err != nil {
			return nil, err
		}
		if err := src.Deploys(sc, Window{}, rb.Deploy); err != nil {
			return nil, err
		}
		if err := src.Links(sc, Window{}, rb.Link); err != nil {
			return nil, err
		}
		for _, l := range stored[strings.ToLower(sc.Name)] {
			rb.linkTo(l.num, l.ticket)
		}
		for _, l := range named {
			rb.linkTo(l.num, l.ticket)
		}
		rb.Done()
	}
	// Tickets come last: a ticket starts when its first linked change opens,
	// and the links are known only once every repository is read.
	for _, sc := range repos {
		if err := src.Tickets(sc, Window{}, b.Ticket); err != nil {
			return nil, err
		}
	}
	for i, sc := range sources {
		ticket := b.TicketSource(SourceScope{Source: sc.Source, Name: sc.Name, Keys: prefixes[i], SyncedAt: sc.SyncedAt, Notes: sc.Notes, LinkedOnly: one})
		if err := src.Tickets(sc, Window{}, ticket); err != nil {
			return nil, err
		}
	}
	return b.Line(), nil
}

// pullLink ties a change, by its number in its repository, to a ticket.
type pullLink struct {
	num    int
	ticket string
}

// pullOf reads the repository, in lower case, and the number out of the ID of
// a change on GitHub.
func pullOf(id string) (repo string, num int, ok bool) {
	rest, found := strings.CutPrefix(id, record.GitHub+":")
	if !found {
		return "", 0, false
	}
	i := strings.LastIndex(rest, "#")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[i+1:])
	return strings.ToLower(rest[:i]), n, err == nil && n > 0
}

// ticketKeys finds the keys of tickets, such as ENG-123, in text.
type ticketKeys struct {
	// id holds the ID of a ticket by its key and by every key it had before,
	// in upper case.
	id       map[string]string
	prefixes map[string]bool
	re       *regexp.Regexp
}

func (k *ticketKeys) add(t *record.Ticket) {
	for _, key := range append([]string{t.Key}, t.Aliases...) {
		key = strings.ToUpper(key)
		prefix, ok := record.KeyPrefix(key)
		if !ok {
			continue
		}
		// A key a ticket has now wins over the same key another ticket had.
		if _, taken := k.id[key]; taken && key != strings.ToUpper(t.Key) {
			continue
		}
		k.id[key] = t.ID
		if k.prefixes == nil {
			k.prefixes = map[string]bool{}
		}
		k.prefixes[prefix] = true
	}
}

// done builds the search from the prefixes of the keys that were added. Only
// a prefix a ticket has can match, so "utf-8" is a key only where a team is
// called UTF.
func (k *ticketKeys) done() {
	if len(k.prefixes) == 0 {
		return
	}
	var list []string
	for p := range k.prefixes {
		list = append(list, regexp.QuoteMeta(p))
	}
	// Longest first: where one prefix ends another, the longer one is tried.
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	k.re = regexp.MustCompile(`(?i)(?:` + strings.Join(list, "|") + `)-[0-9]{1,9}`)
}

func alnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// in returns the IDs of the tickets whose keys the texts name, each once. A
// key counts when it stands alone: no letter or digit right before it or right
// after it. So ENG-12 counts in "eng-12-fix" and "(ENG-12)", and not in
// "ENG-12x" or "xENG-12".
func (k *ticketKeys) in(texts ...string) []string {
	if k.re == nil {
		return nil
	}
	var out []string
	for _, text := range texts {
		for _, m := range k.re.FindAllStringIndex(text, -1) {
			if m[0] > 0 && alnum(text[m[0]-1]) {
				continue
			}
			if m[1] < len(text) && alnum(text[m[1]]) {
				continue
			}
			id, ok := k.id[strings.ToUpper(text[m[0]:m[1]])]
			if !ok {
				continue
			}
			seen := false
			for _, o := range out {
				seen = seen || o == id
			}
			if !seen {
				out = append(out, id)
			}
		}
	}
	return out
}

func eachIn[T any](t store.Table[T], s *store.Store, sc Scope, w Window, fn func(*T)) error {
	return t.Each(s, sc.Source, sc.Name, w.From, w.To, func(_ string, rows []T) error {
		for i := range rows {
			fn(&rows[i])
		}
		return nil
	})
}
