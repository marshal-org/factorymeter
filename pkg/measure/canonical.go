package measure

import (
	"cmp"
	"sort"
	"strings"
	"time"
)

// canonical puts the line in its one canonical form, so what a report says
// does not depend on the order the records, or the segments, came in.
func (b *Builder) canonical() { canonicalize(b.l) }

// canonicalize renumbers the repositories, agents, workflows and items by what
// they are, orders every stream totally, writes times in UTC, and derives the
// repository figures and the sources of the records from their parts. It can
// run again on its own output and changes nothing.
func canonicalize(l *Line) {
	// Repositories by name. Names past the repositories, the projects of
	// tickets, keep their place after them.
	n := len(l.repos)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := l.names[order[i]], l.names[order[j]]
		if c := cmp.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c < 0
		}
		return a < b
	})
	newRepo := make([]int32, len(l.names))
	for i := range newRepo {
		newRepo[i] = int32(i)
	}
	names := make([]string, len(l.names))
	copy(names, l.names)
	repos := make([]repoInfo, n)
	for pos, old := range order {
		newRepo[old] = int32(pos)
		names[pos] = l.names[old]
		repos[pos] = l.repos[old]
	}
	l.names, l.repos = names, repos
	rp := func(i int32) int32 {
		if i < 0 || int(i) >= len(newRepo) {
			return i
		}
		return newRepo[i]
	}

	// Agents by id.
	ao := make([]int, len(l.agents))
	for i := range ao {
		ao[i] = i
	}
	sort.SliceStable(ao, func(i, j int) bool { return l.agents[ao[i]] < l.agents[ao[j]] })
	newAgent := make([]int16, len(l.agents))
	agents := make([]string, len(l.agents))
	for pos, old := range ao {
		newAgent[old] = int16(pos)
		agents[pos] = l.agents[old]
	}
	l.agents = agents
	ap := func(i int16) int16 {
		if i < 0 || int(i) >= len(newAgent) {
			return i
		}
		return newAgent[i]
	}

	// Workflows by repository and name.
	fo := make([]int, len(l.flows))
	for i := range fo {
		fo[i] = i
	}
	sort.SliceStable(fo, func(i, j int) bool {
		a, b := l.flows[fo[i]], l.flows[fo[j]]
		if ra, rb := rp(a.repo), rp(b.repo); ra != rb {
			return ra < rb
		}
		return a.name < b.name
	})
	newFlow := make([]int32, len(l.flows))
	flows := make([]flow, len(l.flows))
	for pos, old := range fo {
		newFlow[old] = int32(pos)
		flows[pos] = flow{repo: rp(l.flows[old].repo), name: l.flows[old].name}
	}
	l.flows = flows

	// Items by what they say and where they sit.
	for i := range l.items {
		l.items[i].repo = rp(l.items[i].repo)
		l.items[i].at = l.items[i].at.UTC()
	}
	io := make([]int, len(l.items))
	for i := range io {
		io[i] = i
	}
	sort.SliceStable(io, func(i, j int) bool { return itemCmp(&l.items[io[i]], &l.items[io[j]]) < 0 })
	newItem := make([]int32, len(l.items))
	items := make([]item, len(l.items))
	for pos, old := range io {
		newItem[old] = int32(pos)
		items[pos] = l.items[old]
	}
	l.items = items
	ip := func(i int32) int32 {
		if i < 0 || int(i) >= len(newItem) {
			return i
		}
		return newItem[i]
	}
	kp := func(k key) key { return key{class: k.class, agent: ap(k.agent), repo: rp(k.repo)} }

	for stream, list := range l.obs {
		for i := range list {
			o := &list[i]
			o.at, o.k, o.item = o.at.UTC(), kp(o.k), ip(o.item)
		}
		sort.Slice(list, func(i, j int) bool { return obsCmp(&list[i], &list[j]) < 0 })
		l.obs[stream] = list
	}
	for queue, list := range l.stays {
		for i := range list {
			s := &list[i]
			s.from, s.to, s.k, s.item = s.from.UTC(), s.to.UTC(), kp(s.k), ip(s.item)
		}
		sort.Slice(list, func(i, j int) bool { return stayCmp(&list[i], &list[j]) < 0 })
		l.stays[queue] = list
	}
	for i := range l.flowRuns {
		x := &l.flowRuns[i]
		x.at, x.flow = x.at.UTC(), newFlow[x.flow]
	}
	sort.Slice(l.flowRuns, func(i, j int) bool {
		a, b := &l.flowRuns[i], &l.flowRuns[j]
		switch {
		case !a.at.Equal(b.at):
			return a.at.Before(b.at)
		case a.flow != b.flow:
			return a.flow < b.flow
		}
		return !a.failed && b.failed
	})

	// Coverage: the records seen, and where they came from.
	l.sources = map[string]map[string]bool{}
	for kind, list := range l.seen {
		for i := range list {
			list[i].repo = rp(list[i].repo)
		}
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i], list[j]
			switch {
			case a.at != b.at:
				return a.at < b.at
			case a.repo != b.repo:
				return a.repo < b.repo
			}
			return a.src < b.src
		})
		for _, r := range list {
			if l.sources[kind] == nil {
				l.sources[kind] = map[string]bool{}
			}
			l.sources[kind][r.src] = true
		}
	}

	// The figures of a repository are the sum of its weeks.
	figures := make(map[figKey]*figs, len(l.figs))
	for k, f := range l.figs {
		figures[figKey{rp(k.repo), k.week}] = f
	}
	l.figs = figures
	for i := range l.repos {
		r := &l.repos[i]
		r.from, r.synced = r.from.UTC(), r.synced.UTC()
		r.changes, r.merged, r.cut, r.ciRuns, r.ciJoined = 0, 0, 0, 0, 0
		r.prodDeploys, r.ships, r.shipsByCommit, r.verifyJoined = 0, 0, 0, 0
		r.environments = nil
	}
	envs := make([]map[string]bool, len(l.repos))
	for k, f := range l.figs {
		if k.repo < 0 || int(k.repo) >= len(l.repos) {
			continue
		}
		r := &l.repos[k.repo]
		r.changes += f.changes
		r.merged += f.merged
		r.cut += f.cut
		r.ciRuns += f.ciRuns
		r.ciJoined += f.ciJoined
		r.prodDeploys += f.prodDeploys
		r.ships += f.ships
		r.shipsByCommit += f.shipsByCommit
		r.verifyJoined += f.verif
		for e := range f.envs {
			if envs[k.repo] == nil {
				envs[k.repo] = map[string]bool{}
			}
			envs[k.repo][e] = true
		}
	}
	for i, set := range envs {
		for e := range set {
			l.repos[i].environments = append(l.repos[i].environments, e)
		}
		sort.Strings(l.repos[i].environments)
	}
	for i := range l.tickets {
		l.tickets[i].synced = l.tickets[i].synced.UTC()
	}
	l.obsPart, l.stayPart = map[string]*parts[obs]{}, map[string]*parts[stay]{}
}

func itemCmp(a, b *item) int {
	if c := cmp.Compare(a.kind, b.kind); c != 0 {
		return c
	}
	if c := cmp.Compare(a.key, b.key); c != 0 {
		return c
	}
	if c := cmp.Compare(a.url, b.url); c != 0 {
		return c
	}
	if c := cmp.Compare(a.title, b.title); c != 0 {
		return c
	}
	if c := cmp.Compare(a.class, b.class); c != 0 {
		return c
	}
	if c := cmp.Compare(a.repo, b.repo); c != 0 {
		return c
	}
	return a.at.Compare(b.at)
}

func keyCmp(a, b key) int {
	if c := cmp.Compare(a.class, b.class); c != 0 {
		return c
	}
	if c := cmp.Compare(a.agent, b.agent); c != 0 {
		return c
	}
	return cmp.Compare(a.repo, b.repo)
}

func obsCmp(a, b *obs) int {
	if c := a.at.Compare(b.at); c != 0 {
		return c
	}
	if c := cmp.Compare(a.v, b.v); c != 0 {
		return c
	}
	if c := keyCmp(a.k, b.k); c != 0 {
		return c
	}
	if c := cmp.Compare(a.item, b.item); c != 0 {
		return c
	}
	return cmp.Compare(a.mode, b.mode)
}

func stayCmp(a, b *stay) int {
	if c := cmpZeroLast(a.from, b.from); c != 0 {
		return c
	}
	if c := cmpZeroLast(a.to, b.to); c != 0 {
		return c
	}
	if c := keyCmp(a.k, b.k); c != 0 {
		return c
	}
	if c := cmp.Compare(a.item, b.item); c != 0 {
		return c
	}
	return cmp.Compare(a.mode, b.mode)
}

// cmpZeroLast orders times, a zero time (still standing) after the others.
func cmpZeroLast(a, b time.Time) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	}
	return a.Compare(b)
}
