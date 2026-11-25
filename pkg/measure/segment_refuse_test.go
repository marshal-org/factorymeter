package measure

import (
	"strings"
	"testing"
)

func oneRepoSegments(t *testing.T) ([][]byte, *Line) {
	t.Helper()
	l := weeksLine(t)
	var out [][]byte
	for _, w := range l.Weeks(repo) {
		b, err := l.Segment(repo, w)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out, l
}

// recut decodes a segment, changes it and writes it again.
func recut(t *testing.T, b []byte, edit func(*segData)) []byte {
	t.Helper()
	d, err := decodeSegment(b)
	if err != nil {
		t.Fatal(err)
	}
	edit(d)
	out, err := d.encode()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSegmentVersionsAndRefusals(t *testing.T) {
	segs, _ := oneRepoSegments(t)
	if len(segs) < 2 {
		t.Fatal("need two weeks")
	}
	refuse := func(name, want string, in ...[]byte) {
		t.Helper()
		l, err := Merge(in...)
		if err == nil || l != nil {
			t.Errorf("%s: merged", name)
			return
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not say %q", name, err, want)
		}
	}
	refuse("empty list", "no segments")
	refuse("engine", "engine version", segs[0], recut(t, segs[1], func(d *segData) { d.engine++ }))
	refuse("digest", "agent signatures", segs[0], recut(t, segs[1], func(d *segData) { d.digest = "other" }))
	refuse("repeat", "both of", segs[0], segs[0])
	refuse("repeat in another case", "also named", segs[0], recut(t, segs[1], func(d *segData) { d.repo = strings.ToUpper(d.repo); d.week = mustWeek(t, segs[0]) }))
	refuse("private", "private", segs[0], recut(t, segs[1], func(d *segData) { d.facts.private = !d.facts.private }))
	refuse("ci wait", "runner", segs[0], recut(t, segs[1], func(d *segData) { d.facts.ciWait = !d.facts.ciWait }))
	refuse("via release", "releases", segs[0], recut(t, segs[1], func(d *segData) { d.facts.viaRelease = !d.facts.viaRelease }))
	refuse("held from", "held from", segs[0], recut(t, segs[1], func(d *segData) { d.facts.from = d.facts.from.Add(1) }))
	refuse("not a monday", "Monday", recut(t, segs[0], func(d *segData) { d.week += 86400 }))
	refuse("not a segment", "not a segment", []byte("hello"))
	refuse("format", "format", append([]byte("FMSG\x09"), segs[0][5:]...))
	if _, err := Merge(segs...); err != nil {
		t.Errorf("good segments refused: %v", err)
	}
}

func mustWeek(t *testing.T, b []byte) int64 {
	t.Helper()
	d, err := decodeSegment(b)
	if err != nil {
		t.Fatal(err)
	}
	return d.week
}
