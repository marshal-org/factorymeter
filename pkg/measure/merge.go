package measure

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Merge puts segments cut by Line.Segment together into a line. The report of
// the line is the report of a line built from all the records of the
// repositories and weeks the segments hold, in either review mode, and does
// not depend on the order of the segments.
//
// Merge refuses an empty list, bytes that are not a segment or are damaged,
// segments cut by another format or engine version or with another list of
// agent signatures, the same repository and week twice (repository names are
// compared without regard to case), and segments of one repository that
// disagree on what is true of the whole repository.
//
// The line has no ticket stations: segments hold none.
func Merge(segments ...[]byte) (*Line, error) {
	if len(segments) == 0 {
		return nil, errors.New("merge: no segments")
	}
	// Versions first, so a mismatch is said before any body is read.
	var engine int
	var digest string
	for i, b := range segments {
		e, dg, _, _, err := readHeader(b)
		if err != nil {
			return nil, fmt.Errorf("merge: segment %d: %w", i, err)
		}
		if i == 0 {
			engine, digest = e, dg
			continue
		}
		if e != engine {
			return nil, fmt.Errorf("merge: segment %d was cut by engine version %d and segment 0 by %d", i, e, engine)
		}
		if dg != digest {
			return nil, fmt.Errorf("merge: segment %d was cut with another list of agent signatures than segment 0", i)
		}
	}
	if engine != EngineVersion {
		return nil, fmt.Errorf("merge: the segments were cut by engine version %d and this library is version %d", engine, EngineVersion)
	}

	l := &Line{
		digest: digest, engine: engine,
		obs: map[string][]obs{}, stays: map[string][]stay{}, seen: map[string][]seenRec{},
		figs: map[figKey]*figs{}, sources: map[string]map[string]bool{},
	}
	repoAt := map[string]int32{}
	agentAt := map[string]int16{}
	flowAt := map[flow]int32{}
	cells := map[figKey]int{}
	for i, b := range segments {
		d, err := decodeSegment(b)
		if err != nil {
			return nil, fmt.Errorf("merge: segment %d: %w", i, err)
		}
		if weekOf(time.Unix(d.week, 0)) != d.week {
			return nil, fmt.Errorf("merge: segment %d: its week is not a UTC Monday", i)
		}
		lower := strings.ToLower(d.repo)
		r, ok := repoAt[lower]
		if !ok {
			r = int32(len(l.names))
			repoAt[lower] = r
			l.names = append(l.names, d.repo)
			l.repos = append(l.repos, repoInfo{name: d.repo, private: d.facts.private, from: d.facts.from,
				ciWait: d.facts.ciWait, viaRelease: d.facts.viaRelease, synced: d.facts.synced, notes: d.facts.notes})
		} else {
			info := &l.repos[r]
			f := d.facts
			switch {
			case info.name != d.repo:
				return nil, fmt.Errorf("merge: repository %q is also named %q: cut every week of it from one build", info.name, d.repo)
			case info.private != f.private:
				return nil, fmt.Errorf("merge: segments of %s disagree on whether it is private: cut every week of it from one build", d.repo)
			case info.ciWait != f.ciWait:
				return nil, fmt.Errorf("merge: segments of %s disagree on whether its runs tell when a runner picked them up: cut every week of it from one build", d.repo)
			case info.viaRelease != f.viaRelease:
				return nil, fmt.Errorf("merge: segments of %s disagree on whether its production deploys come from releases: cut every week of it from one build", d.repo)
			case !info.from.Equal(f.from):
				return nil, fmt.Errorf("merge: segments of %s disagree on the time its changes are held from: cut every week of it from one build", d.repo)
			}
			// The sync time is the latest, and the notes are those of the
			// segment that has it, the lesser of equal ones, so the order of
			// the segments decides nothing.
			if f.synced.After(info.synced) || (f.synced.Equal(info.synced) && strings.Join(f.notes, "\n") < strings.Join(info.notes, "\n")) {
				info.synced, info.notes = f.synced, f.notes
			}
		}
		cell := figKey{r, d.week}
		if j, dup := cells[cell]; dup {
			return nil, fmt.Errorf("merge: segments %d and %d are both of %s in the week of %s", j, i, d.repo, time.Unix(d.week, 0).UTC().Format("2006-01-02"))
		}
		cells[cell] = i

		agents := make([]int16, len(d.agents))
		for j, a := range d.agents {
			x, ok := agentAt[a]
			if !ok {
				if len(l.agents) >= 1<<15-1 {
					return nil, errors.New("merge: too many agents")
				}
				x = int16(len(l.agents))
				agentAt[a] = x
				l.agents = append(l.agents, a)
			}
			agents[j] = x
		}
		agentOf := func(a int32) int16 {
			if a < 0 {
				return -1
			}
			return agents[a]
		}
		base := int32(len(l.items))
		itemOf := func(it int32) int32 {
			if it < 0 {
				return -1
			}
			return base + it
		}
		for _, it := range d.items {
			l.items = append(l.items, item{kind: it.kind, key: it.key, url: it.url, title: it.title, class: it.class, at: it.at, repo: r})
		}
		for _, st := range d.obs {
			for _, o := range st.list {
				l.obs[st.name] = append(l.obs[st.name], obs{at: o.at, v: o.v, k: key{class: o.class, agent: agentOf(o.agent), repo: r}, item: itemOf(o.item), mode: o.mode})
			}
		}
		for _, st := range d.stays {
			for _, o := range st.list {
				l.stays[st.name] = append(l.stays[st.name], stay{from: o.from, to: o.to, k: key{class: o.class, agent: agentOf(o.agent), repo: r}, item: itemOf(o.item), mode: o.mode})
			}
		}
		flows := make([]int32, len(d.flows))
		for j, name := range d.flows {
			f := flow{repo: r, name: name}
			x, ok := flowAt[f]
			if !ok {
				x = int32(len(l.flows))
				flowAt[f] = x
				l.flows = append(l.flows, f)
			}
			flows[j] = x
		}
		for _, x := range d.runs {
			l.flowRuns = append(l.flowRuns, flowRun{at: x.at, flow: flows[x.flow], failed: x.failed})
		}
		for _, s := range d.seen {
			l.seen[s.kind] = append(l.seen[s.kind], seenRec{at: s.at, repo: r, src: s.src})
		}
		f := d.figs
		l.figs[cell] = &f
	}
	for i := range l.repos {
		l.repos[i].unfinished = l.repos[i].synced.IsZero()
	}
	canonicalize(l)
	return l, nil
}
