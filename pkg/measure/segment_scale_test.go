package measure_test

import (
	"bytes"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/synth"
)

// peakRSS is the most resident memory the process has held, in bytes.
func peakRSS() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return uint64(ru.Maxrss)
	}
	return uint64(ru.Maxrss) << 10
}

// TestMergeScaleGate merges the segments of 300 repositories by 26 weeks and
// reads a report, and checks it takes under 10 seconds and under 1 GB. It is
// large, so it runs only with FACTORYMETER_SCALE=1.
//
// The memory figure is the peak of the whole process, which also built the
// organization and cut the segments, so it is above what a host that only
// merges needs.
func TestMergeScaleGate(t *testing.T) {
	if os.Getenv("FACTORYMETER_SCALE") == "" {
		t.Skip("set FACTORYMETER_SCALE=1 to run the scale gate")
	}
	cfg := synth.Default()
	src := synth.New(cfg)
	full, err := measure.LoadFrom(src, synthRegistry(t), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	segs := cutEvery(t, full, synthNames(cfg), src.End())
	var total, largest int
	for _, s := range segs {
		total += len(s)
		largest = max(largest, len(s))
	}
	t.Logf("%d segments, %d bytes in all, the largest %d bytes", len(segs), total, largest)
	want := map[bool][]byte{false: reportBytes(t, full, src.End(), false), true: reportBytes(t, full, src.End(), true)}
	full = nil
	runtime.GC()

	start := time.Now()
	merged, err := measure.Merge(segs...)
	if err != nil {
		t.Fatal(err)
	}
	for _, human := range []bool{false, true} {
		got := reportBytes(t, merged, src.End(), human)
		if !bytes.Equal(want[human], got) {
			t.Errorf("people only %v: the merged report is not the full load", human)
		}
	}
	took := time.Since(start)
	rss := peakRSS()
	t.Logf("merge and both reports took %s, peak resident memory %d MiB", took.Round(time.Millisecond), rss>>20)
	if took > 10*time.Second {
		t.Errorf("merge and report took %s, over 10 s", took)
	}
	if rss > 1<<30 {
		t.Errorf("peak resident memory is %d MiB, over 1 GiB", rss>>20)
	}
}
