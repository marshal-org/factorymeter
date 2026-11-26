package measure_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/signature"
	"github.com/marshal-org/factorymeter/pkg/synth"
)

func synthRegistry(t testing.TB) *signature.Registry {
	t.Helper()
	reg, err := signature.Default()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// cutEvery cuts a segment for every repository and week that holds data, and an
// empty one for a repository that holds none.
func cutEvery(t testing.TB, l *measure.Line, names []string, week time.Time) [][]byte {
	t.Helper()
	var out [][]byte
	for _, n := range names {
		weeks := l.Weeks(n)
		if len(weeks) == 0 {
			weeks = []time.Time{week}
		}
		for _, w := range weeks {
			b, err := l.Segment(n, w)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
	}
	return out
}

func reportBytes(t testing.TB, l *measure.Line, now time.Time, human bool) []byte {
	t.Helper()
	l = l.WithReviews(human)
	var all []any
	for _, o := range []measure.Options{
		{Weeks: 4, History: 12, Now: now},
		{Weeks: 13, History: 26, Now: now, Summary: true},
	} {
		rep, err := l.Report(o)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rep)
	}
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func synthNames(cfg synth.Config) []string {
	var out []string
	for i := 0; i < cfg.Repos; i++ {
		out = append(out, synth.RepoName(i))
	}
	return out
}

func TestSegmentParitySynth(t *testing.T) {
	cfg := synth.Config{Seed: 7, Repos: 12, ChangesPerWeek: 60, Weeks: 8}
	src := synth.New(cfg)
	full, err := measure.LoadFrom(src, synthRegistry(t), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := measure.Merge(cutEvery(t, full, synthNames(cfg), src.End())...)
	if err != nil {
		t.Fatal(err)
	}
	for _, human := range []bool{false, true} {
		if want, got := reportBytes(t, full, src.End(), human), reportBytes(t, merged, src.End(), human); !bytes.Equal(want, got) {
			t.Errorf("people only %v: the merged report is not the full load", human)
		}
	}
}
