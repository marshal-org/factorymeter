package measure

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
)

func weeksLine(t *testing.T) *Line {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-8 * week), SyncedAt: end})
	// end is a Monday. A change created on the Sunday before it and merged two
	// weeks later belongs to the week of its creation.
	sun := end.Add(-1 * h)
	c1 := change(1, sun, review(record.ReviewApproved, sun.Add(2*day)), merged(sun.Add(15*day)))
	c2 := change(2, end.Add(-3*week), review(record.ReviewApproved, end.Add(-3*week+h)), merged(end.Add(-3*week+2*h)))
	rb.Change(&c1)
	rb.Change(&c2)
	// A deploy belongs to the week it finished in, not the week of the change.
	d := deployment("production", "v1", c1.Commits[0].SHA, true, end.Add(13*day+23*h+30*time.Minute), h, "success")
	rb.Deploy(&d)
	rb.Done()
	b.Repo(RepoScope{Name: "acme/empty", From: end.Add(-8 * week), SyncedAt: end}).Done()
	return b.Line()
}

func TestSegmentWeeksAndFigures(t *testing.T) {
	l := weeksLine(t)
	weeks := l.Weeks(repo)
	if len(weeks) < 2 {
		t.Fatalf("weeks = %v", weeks)
	}
	for i, w := range weeks {
		if w.Weekday() != time.Monday || w.Location() != time.UTC || (i > 0 && !w.After(weeks[i-1])) {
			t.Errorf("week %v is not a UTC Monday in order", w)
		}
	}
	creation := weekOf(end.Add(-1 * h))
	has := func(ws []time.Time, w int64) bool {
		for _, x := range ws {
			if x.Unix() == w {
				return true
			}
		}
		return false
	}
	if !has(weeks, creation) {
		t.Errorf("the week of the creation of a change that spans weeks is missing: %v", weeks)
	}
	if !has(weeks, weekOf(end.Add(14*day+30*time.Minute))) {
		t.Errorf("the week of the deploy is missing: %v", weeks)
	}
	for _, it := range l.items {
		if it.kind == record.KindDeploy && weekOf(it.at) != weekOf(end.Add(14*day+30*time.Minute)) {
			t.Errorf("a deploy is placed in the week %v, not the week it finished in", time.Unix(weekOf(it.at), 0).UTC())
		}
	}
	if got := l.Weeks("acme/empty"); len(got) != 0 {
		t.Errorf("a repository with no data has weeks %v", got)
	}
	if got := l.Weeks("acme/nothing"); got != nil {
		t.Errorf("an unknown repository has weeks %v", got)
	}

	// Any instant of the week gives the segment of the week.
	a, err := l.Segment(strings.ToUpper(repo), time.Unix(creation, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Segment(repo, time.Unix(creation+6*86400+86399, 0).In(time.FixedZone("x", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("two instants of one week gave two segments")
	}
	if _, err := l.Segment("acme/nothing", end); err == nil {
		t.Error("a segment of an unknown repository was cut")
	}

	// Everything is in exactly one segment: the merged report is the loaded one.
	segs := cutAll(t, l)
	merged, err := Merge(segs...)
	if err != nil {
		t.Fatal(err)
	}
	if guardOutput(t, "weeks", merged, repo) != guardOutput(t, "weeks", l, repo) {
		t.Error("merged line differs from the loaded one")
	}
	// An empty week of a repository with data is an empty segment, and merging
	// it changes nothing.
	empty, err := l.Segment(repo, end.Add(-30*week))
	if err != nil {
		t.Fatal(err)
	}
	with, err := Merge(append(append([][]byte(nil), segs...), empty)...)
	if err != nil {
		t.Fatal(err)
	}
	if guardOutput(t, "weeks", with, repo) != guardOutput(t, "weeks", l, repo) {
		t.Error("an empty week changed the report")
	}
	// The same week twice is refused.
	if _, err := Merge(segs[0], segs[0]); err == nil {
		t.Error("a repeated segment was merged")
	}
	// Some of the repositories: the report says nothing of the others.
	one, err := l.Segment("acme/empty", end)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := Merge(one)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.repos) != 1 || sub.repos[0].name != "acme/empty" {
		t.Errorf("a subset merge holds %v", sub.names)
	}
	rep, err := sub.Report(Options{Target: "acme", Weeks: 4, History: 8, Now: end})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Coverage.Repos) != 1 {
		t.Errorf("coverage of a subset merge names %d repositories", len(rep.Coverage.Repos))
	}
}

// A line with tickets is cut without them and without an error.
func TestSegmentOfALineWithTickets(t *testing.T) {
	m := copyOf(t, StoreSource(linearStore(t, end)))
	l, err := LoadFromWith(m, registry(t), "acme", nil, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(cutAll(t, l)...)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.tickets) != 0 {
		t.Error("a merged line has tickets")
	}
}

// Issues of a repository belong to it in the line, but segments leave tickets
// out: a merged line has none of them and no spec data.
func TestSegmentLeavesIssuesOut(t *testing.T) {
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-8 * week), SyncedAt: end})
	c := change(1, end.Add(-2*day), merged(end.Add(-day)))
	rb.Change(&c)
	rb.Done()
	key := repo + "#7"
	done := end.Add(-2 * day)
	b.Ticket(&record.Ticket{Source: record.GitHub, ID: record.TicketID(record.GitHub, key), Key: key, Project: repo,
		Title: "An issue", State: record.TicketDone, CreatedAt: end.Add(-3 * day), DoneAt: &done, ClosedAt: &done})
	l := b.Line()
	if len(l.seenObs("spec.rework")) == 0 {
		t.Fatal("the line has no spec data: the test proves nothing")
	}
	m, err := Merge(cutAll(t, l)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range m.items {
		if it.kind == record.KindTicket {
			t.Error("a merged line holds an issue")
		}
	}
	for _, s := range []string{"spec.flow", "spec.wait", "spec.rework", "spec.queue"} {
		if n := len(m.seenObs(s)); n != 0 {
			t.Errorf("%s: %d observations of an issue in a merged line", s, n)
		}
	}
}
