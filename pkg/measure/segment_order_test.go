package measure

import (
	"math/rand/v2"
	"testing"
)

// The order the segments are merged in changes nothing.
func TestMergeOrderChangesNothing(t *testing.T) {
	for name, build := range parityScenarios(t) {
		full, target := build()
		segs := cutAll(t, full)
		base, err := Merge(segs...)
		if err != nil {
			t.Fatal(err)
		}
		want := guardOutput(t, name, base, target)
		r := rand.New(rand.NewPCG(1, 2))
		for round := 0; round < 12; round++ {
			shuffled := append([][]byte(nil), segs...)
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			if round == 0 {
				for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
					shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
				}
			}
			l, err := Merge(shuffled...)
			if err != nil {
				t.Fatal(err)
			}
			if got := guardOutput(t, name, l, target); got != want {
				t.Fatalf("%s: round %d: another order of the segments gave another report", name, round)
			}
		}
	}
}
