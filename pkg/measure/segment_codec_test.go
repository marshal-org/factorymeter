package measure

import (
	"bytes"
	"testing"
)

// A segment cut twice is the same bytes, and reading then writing it again
// gives the same bytes.
func TestSegmentBytesAreStable(t *testing.T) {
	a := weeksLine(t)
	b := weeksLine(t)
	for _, w := range a.Weeks(repo) {
		x, err := a.Segment(repo, w)
		if err != nil {
			t.Fatal(err)
		}
		y, _ := a.Segment(repo, w)
		z, _ := b.Segment(repo, w)
		if !bytes.Equal(x, y) || !bytes.Equal(x, z) {
			t.Errorf("week %v: the bytes of a segment are not stable", w)
		}
		d, err := decodeSegment(x)
		if err != nil {
			t.Fatal(err)
		}
		again, err := d.encode()
		if err != nil || !bytes.Equal(x, again) {
			t.Errorf("week %v: reading and writing a segment changed it (%v)", w, err)
		}
	}
}
