package measure

import (
	"testing"

	"github.com/marshal-org/factorymeter/pkg/record"
)

// cutAll cuts every segment of every repository of the line.
func cutAll(t *testing.T, l *Line) [][]byte {
	t.Helper()
	var out [][]byte
	for i := range l.repos {
		name := l.names[i]
		weeks := l.Weeks(name)
		if len(weeks) == 0 {
			b, err := l.Segment(name, end)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
		for _, w := range weeks {
			b, err := l.Segment(name, w)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
	}
	return out
}

// parityScenarios are the lines of the guard, loaded without tickets, which a
// segment does not hold.
func parityScenarios(t *testing.T) map[string]func() (*Line, string) {
	t.Helper()
	out := map[string]func() (*Line, string){}
	for _, target := range []string{"acme", repo} {
		out["linear/"+target] = func() (*Line, string) {
			m := copyOf(t, StoreSource(linearStore(t, end)))
			m.tickets, m.links = map[string][]record.Ticket{}, map[string][]record.Link{}
			var keep []Scope
			for _, sc := range m.scopes {
				if sc.Source == record.GitHub {
					keep = append(keep, sc)
				}
			}
			m.scopes = keep
			l, err := LoadFromWith(m, registry(t), target, nil, LoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return l, target
		}
	}
	out["scenario"] = func() (*Line, string) { return scenarioOf(t, false), repo }
	return out
}

func TestSegmentParityScenarios(t *testing.T) {
	for name, build := range parityScenarios(t) {
		full, target := build()
		merged, err := Merge(cutAll(t, full)...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, human := range []bool{false, true} {
			want := guardOutput(t, name, full.WithReviews(human), target)
			got := guardOutput(t, name, merged.WithReviews(human), target)
			if want != got {
				t.Errorf("%s (people only %v): the merged report is not the full load", name, human)
			}
		}
	}
}
