package collector

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
)

// A display tells how far a read of CI runs is from the number GitHub counts
// for the stretch. Every batch carries that number.
func TestRunsTellHowManyTheStretchHolds(t *testing.T) {
	s := &server{graphql: onePage}
	id := 0
	s.rest = func(path string, q url.Values) any {
		n := 100
		if q.Get("page") == "2" {
			n = 50
		}
		runs := make([]obj, n)
		for i := range runs {
			id++
			runs[i] = run(id, "a", "pull_request", day(1))
		}
		return obj{"total_count": 150, "workflow_runs": runs}
	}
	var sizes, totals []int
	read, cut, err := s.collector(t, Options{}).Runs(context.Background(), "o/r", "pull_request", since, until, 1000, func(runs []record.CIRun, total int) {
		sizes, totals = append(sizes, len(runs)), append(totals, total)
	})
	if err != nil || cut || read != 150 {
		t.Fatalf("read %d, cut %v, error %v", read, cut, err)
	}
	if len(sizes) != 2 || sizes[0] != 100 || sizes[1] != 50 || totals[0] != 150 || totals[1] != 150 {
		t.Errorf("batches of %v with totals %v, want 100 and 50 of 150", sizes, totals)
	}
}

// A stretch with more runs than one filter returns is read in halves. The
// number told is the one of the whole stretch, not the one of a half.
func TestRunsOfASplitStretchKeepTheWholeCount(t *testing.T) {
	s := &server{graphql: onePage}
	id := 0
	s.rest = func(path string, q url.Values) any {
		from, to := created(t, q)
		if to.Sub(from) > 10*24*time.Hour {
			return obj{"total_count": 1500, "workflow_runs": []obj{run(9999, "a", "pull_request", day(1))}}
		}
		n := 100
		if q.Get("page") == "2" {
			n = 50
		}
		runs := make([]obj, n)
		for i := range runs {
			id++
			runs[i] = run(id, "a", "pull_request", from.Add(time.Minute))
		}
		return obj{"total_count": 150, "workflow_runs": runs}
	}
	batches := 0
	read, cut, err := s.collector(t, Options{}).Runs(context.Background(), "o/r", "pull_request", since, until, 5000, func(runs []record.CIRun, total int) {
		batches++
		if total != 1500 {
			t.Errorf("batch %d was told %d runs, want the 1500 of the whole stretch", batches, total)
		}
		for _, r := range runs {
			if r.ID == record.CIRunID(record.GitHub, "o/r", 9999, 1) {
				t.Errorf("the run of the page over the cap was passed on")
			}
		}
	})
	if err != nil || cut || read != 300 || batches != 4 {
		t.Errorf("read %d in %d batches, cut %v, error %v, want 300 in 4", read, batches, cut, err)
	}
}
