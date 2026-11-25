package measure

import (
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/record"
)

// tiesLine builds a line from changes that have exactly the same moments, in
// the order given.
func tiesLine(t *testing.T, order []int) *Line {
	t.Helper()
	b := NewBuilder(registry(t))
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
	at := end.Add(-6 * day)
	all := []record.Change{
		change(1, at, review(record.ReviewApproved, at.Add(2*h)), merged(at.Add(4*h))),
		change(2, at, review(record.ReviewApproved, at.Add(2*h)), merged(at.Add(4*h))),
		change(3, at, agentReview(record.ReviewApproved, at.Add(2*h)), review(record.ReviewApproved, at.Add(2*h)), merged(at.Add(4*h)), fromAgent),
		change(4, at, agentReview(record.ReviewApproved, at.Add(2*h)), merged(at.Add(4*h)), fromAgent),
		change(5, at.Add(-day)),
	}
	for _, i := range order {
		c := all[i]
		rb.Change(&c)
	}
	rb.CIRun(ptrRun(run("a", 1, at, at.Add(h), at.Add(2*h), "success")))
	rb.CIRun(ptrRun(run("b", 1, at, at.Add(h), at.Add(2*h), "failure")))
	d1 := deployment("production", "v1", "x1", true, at.Add(5*h), h, "success")
	d2 := deployment("production", "v2", "x2", true, at.Add(5*h), h, "success")
	rb.Deploy(&d1)
	rb.Deploy(&d2)
	rb.Done()
	return b.Line()
}

func ptrRun(r record.CIRun) *record.CIRun { return &r }

// Equal moments are read in an order that does not depend on the order of the
// records, loaded or merged.
func TestTiesAreTotal(t *testing.T) {
	orders := [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {2, 0, 4, 1, 3}, {3, 4, 1, 2, 0}}
	var want string
	for i, o := range orders {
		l := tiesLine(t, o)
		for _, human := range []bool{false, true} {
			got := guardOutput(t, "ties", l.WithReviews(human), repo)
			merged, err := Merge(cutAll(t, l)...)
			if err != nil {
				t.Fatal(err)
			}
			if m := guardOutput(t, "ties", merged.WithReviews(human), repo); m != got {
				t.Errorf("order %v, people only %v: merged differs from loaded", o, human)
			}
			if i == 0 && !human {
				want = got
			}
			if !human && got != want {
				t.Errorf("order %v: the report depends on the order of the records", o)
			}
		}
	}
	_ = time.Second
}
