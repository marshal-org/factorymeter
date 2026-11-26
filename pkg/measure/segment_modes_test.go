package measure

import (
	"testing"

	"github.com/marshal-org/factorymeter/pkg/record"
)

func modesBuild(t *testing.T, human bool) *Line {
	t.Helper()
	b := NewBuilder(registry(t))
	if human {
		b.HumanReviewsOnly()
	}
	rb := b.Repo(RepoScope{Name: repo, From: end.Add(-4 * week), SyncedAt: end})
	at := end.Add(-6 * day)
	// Only an agent reviewed this one.
	c1 := change(1, at, agentReview(record.ReviewApproved, at.Add(2*h)), merged(at.Add(5*h)), fromAgent)
	// A person and an agent reviewed this one.
	c2 := change(2, at, agentReview(record.ReviewApproved, at.Add(h)), review(record.ReviewApproved, at.Add(3*h)), merged(at.Add(6*h)))
	c3 := change(3, at, review(record.ReviewApproved, at.Add(4*h)), merged(at.Add(7*h)))
	for _, c := range []record.Change{c1, c2, c3} {
		c := c
		rb.Change(&c)
	}
	rb.Done()
	return b.Line()
}

// One merged line answers both review modes, as a build in each mode does.
func TestSegmentReviewModes(t *testing.T) {
	all := modesBuild(t, false)
	merged, err := Merge(cutAll(t, all)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, human := range []bool{false, true} {
		want := guardOutput(t, "modes", modesBuild(t, human), repo)
		if got := guardOutput(t, "modes", merged.WithReviews(human), repo); got != want {
			t.Errorf("people only %v: the merged line is not the build in that mode", human)
		}
	}
	if guardOutput(t, "modes", merged.WithReviews(true), repo) == guardOutput(t, "modes", merged.WithReviews(false), repo) {
		t.Error("the two review modes read the same, so the test shows nothing")
	}
}
