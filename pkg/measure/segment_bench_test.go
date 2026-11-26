package measure_test

import (
	"testing"

	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/synth"
)

// BenchmarkMergeReport merges the segments of a synthetic organization and
// reads a report from them. Raise the size with the config below to see how
// the cost grows; the scale gate runs the full 300 by 26.
func BenchmarkMergeReport(b *testing.B) {
	cfg := synth.Config{Seed: 3, Repos: 60, ChangesPerWeek: 300, Weeks: 12}
	src := synth.New(cfg)
	full, err := measure.LoadFrom(src, synthRegistry(b), "", nil)
	if err != nil {
		b.Fatal(err)
	}
	segs := cutEvery(b, full, synthNames(cfg), src.End())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l, err := measure.Merge(segs...)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := l.Report(measure.Options{Weeks: 13, History: 12, Now: src.End()}); err != nil {
			b.Fatal(err)
		}
	}
}
