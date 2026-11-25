package measure

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// cutCache says which parts of a line belong to which repository and week. It
// is built once, the first time a line is cut, and shared by the copies that
// WithReviews makes. A line is not changed after it is built, so it stays true.
type cutCache struct {
	once     sync.Once
	itemCell []figKey // by item; repo -1 for an item no segment holds
	items    map[figKey][]int32
	obs      map[string]map[figKey][]int32
	stays    map[string]map[figKey][]int32
	runs     map[figKey][]int32
	seen     map[string]map[figKey][]int32
	weeks    map[int32]map[int64]bool
}

// cellOf says which repository and week an observation of an item belongs to.
// It is the cell of the item, or when it has none, the repository of its key
// and the week of the moment.
func (c *cutCache) cellOf(item int32, k key, at time.Time) figKey {
	if item >= 0 && int(item) < len(c.itemCell) {
		return c.itemCell[item]
	}
	return figKey{k.repo, weekOf(at)}
}

func (l *Line) cutIndex() *cutCache {
	c := l.cuts
	c.once.Do(func() {
		c.weeks = map[int32]map[int64]bool{}
		note := func(k figKey) {
			if k.repo < 0 {
				return
			}
			if c.weeks[k.repo] == nil {
				c.weeks[k.repo] = map[int64]bool{}
			}
			c.weeks[k.repo][k.week] = true
		}
		c.itemCell = make([]figKey, len(l.items))
		c.items = map[figKey][]int32{}
		for i := range l.items {
			it := &l.items[i]
			k := figKey{it.repo, weekOf(it.at)}
			c.itemCell[i] = k
			if k.repo >= 0 {
				c.items[k] = append(c.items[k], int32(i))
				note(k)
			}
		}
		c.obs = map[string]map[figKey][]int32{}
		for s, list := range l.obs {
			m := map[figKey][]int32{}
			for i := range list {
				k := c.cellOf(list[i].item, list[i].k, list[i].at)
				if k.repo >= 0 {
					m[k] = append(m[k], int32(i))
					note(k)
				}
			}
			c.obs[s] = m
		}
		c.stays = map[string]map[figKey][]int32{}
		for s, list := range l.stays {
			m := map[figKey][]int32{}
			for i := range list {
				k := c.cellOf(list[i].item, list[i].k, list[i].from)
				if k.repo >= 0 {
					m[k] = append(m[k], int32(i))
					note(k)
				}
			}
			c.stays[s] = m
		}
		c.runs = map[figKey][]int32{}
		for i := range l.flowRuns {
			r := &l.flowRuns[i]
			if int(r.flow) >= len(l.flows) {
				continue
			}
			k := figKey{l.flows[r.flow].repo, weekOf(r.at)}
			if k.repo >= 0 {
				c.runs[k] = append(c.runs[k], int32(i))
				note(k)
			}
		}
		c.seen = map[string]map[figKey][]int32{}
		for s, list := range l.seen {
			m := map[figKey][]int32{}
			for i := range list {
				k := figKey{list[i].repo, weekOf(time.Unix(list[i].at, 0))}
				if k.repo >= 0 {
					m[k] = append(m[k], int32(i))
					note(k)
				}
			}
			c.seen[s] = m
		}
		for k := range l.figs {
			note(k)
		}
	})
	return c
}

func (l *Line) repoIndex(name string) int32 {
	for i := range l.repos {
		if strings.EqualFold(l.names[i], name) {
			return int32(i)
		}
	}
	return -1
}

// Weeks lists the UTC Monday weeks the repository has data in, oldest first.
// A repository the line does not know has none.
func (l *Line) Weeks(repo string) []time.Time {
	r := l.repoIndex(repo)
	if r < 0 {
		return nil
	}
	set := l.cutIndex().weeks[r]
	secs := make([]int64, 0, len(set))
	for w := range set {
		secs = append(secs, w)
	}
	sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
	out := make([]time.Time, len(secs))
	for i, w := range secs {
		out[i] = time.Unix(w, 0).UTC()
	}
	return out
}

// Segment cuts what the line holds for one repository and the UTC Monday week
// that holds week, as bytes a host can store and later merge with Merge.
//
// A change belongs to the week it was created in, with everything that
// happened to it, and a deploy to the week it finished in. The tickets of the
// line are not cut: a merged line has no ticket stations. A repository that
// has no data in the week gives an empty segment, and one that the line does
// not know is an error.
//
// The same line gives the same bytes every time.
func (l *Line) Segment(repo string, week time.Time) ([]byte, error) {
	r := l.repoIndex(repo)
	if r < 0 {
		return nil, fmt.Errorf("segment: the line has no repository %q", repo)
	}
	wk := weekOf(week)
	cell := figKey{r, wk}
	c := l.cutIndex()
	info := l.repos[r]
	d := &segData{engine: l.engine, digest: l.digest, repo: l.names[r], week: wk}
	d.facts = segFacts{private: info.private, ciWait: info.ciWait, viaRelease: info.viaRelease,
		from: info.from, synced: info.synced, notes: info.notes}

	agentAt := map[int16]int32{}
	agent := func(a int16) int32 {
		if a < 0 || int(a) >= len(l.agents) {
			return -1
		}
		i, ok := agentAt[a]
		if !ok {
			i = int32(len(d.agents))
			agentAt[a] = i
			d.agents = append(d.agents, l.agents[a])
		}
		return i
	}
	itemAt := map[int32]int32{}
	// The items of the cell come first, in the order of the line.
	for _, i := range c.items[cell] {
		it := &l.items[i]
		itemAt[i] = int32(len(d.items))
		d.items = append(d.items, segItem{kind: it.kind, key: it.key, url: it.url, title: it.title, class: it.class, at: it.at})
	}
	local := func(it int32) (int32, error) {
		if it < 0 {
			return -1, nil
		}
		i, ok := itemAt[it]
		if !ok {
			return 0, errors.New("segment: an observation is about an item of another week")
		}
		return i, nil
	}
	own := func(k key) error {
		if k.repo != r {
			return errors.New("segment: an observation is of another repository than the one it is placed in")
		}
		return nil
	}

	for _, s := range sortedKeys(c.obs) {
		idx := c.obs[s][cell]
		if len(idx) == 0 {
			continue
		}
		st := segStream[segObs]{name: s}
		for _, i := range idx {
			o := &l.obs[s][i]
			if err := own(o.k); err != nil {
				return nil, err
			}
			it, err := local(o.item)
			if err != nil {
				return nil, err
			}
			st.list = append(st.list, segObs{at: o.at, v: o.v, class: o.k.class, agent: agent(o.k.agent), item: it, mode: o.mode})
		}
		d.obs = append(d.obs, st)
	}
	for _, s := range sortedKeys(c.stays) {
		idx := c.stays[s][cell]
		if len(idx) == 0 {
			continue
		}
		st := segStream[segStay]{name: s}
		for _, i := range idx {
			o := &l.stays[s][i]
			if err := own(o.k); err != nil {
				return nil, err
			}
			it, err := local(o.item)
			if err != nil {
				return nil, err
			}
			st.list = append(st.list, segStay{from: o.from, to: o.to, class: o.k.class, agent: agent(o.k.agent), item: it, mode: o.mode})
		}
		d.stays = append(d.stays, st)
	}
	flowAt := map[int32]int32{}
	for _, i := range c.runs[cell] {
		x := &l.flowRuns[i]
		f, ok := flowAt[x.flow]
		if !ok {
			f = int32(len(d.flows))
			flowAt[x.flow] = f
			d.flows = append(d.flows, l.flows[x.flow].name)
		}
		d.runs = append(d.runs, segRun{at: x.at, flow: f, failed: x.failed})
	}
	for _, kind := range sortedKeys(c.seen) {
		for _, i := range c.seen[kind][cell] {
			s := l.seen[kind][i]
			d.seen = append(d.seen, segSeen{kind: kind, at: s.at, src: s.src})
		}
	}
	if f := l.figs[cell]; f != nil {
		d.figs = *f
	}
	return d.encode()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
