package measure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
)

// guardFile holds the marshalled reports and drills of the scenarios below, as
// they were before the line was made mergeable. A change to any of them is a
// change to a report that was not meant to change, and has to be looked at and
// listed in the pull request. Run with FACTORYMETER_GUARD_UPDATE=1 to record.
const guardFile = "testdata/guard.json"

// guardScenarios are lines read from records, with and without people only.
func guardScenarios(t *testing.T) map[string]func() (*Line, string) {
	t.Helper()
	out := map[string]func() (*Line, string){}
	for _, target := range []string{"acme", repo} {
		for _, human := range []bool{false, true} {
			name := "linear/" + target
			if human {
				name += "/human"
			}
			out[name] = func() (*Line, string) {
				m := copyOf(t, StoreSource(linearStore(t, end)))
				l, err := LoadFromWith(m, registry(t), target, nil, LoadOptions{HumanReviewsOnly: human})
				if err != nil {
					t.Fatal(err)
				}
				return l, target
			}
		}
	}
	for _, human := range []bool{false, true} {
		human := human
		name := "scenario"
		if human {
			name += "/human"
		}
		out[name] = func() (*Line, string) {
			return scenarioOf(t, human), repo
		}
	}
	return out
}

func guardOutput(t *testing.T, l *Line, target string) string {
	t.Helper()
	var all []any
	for _, o := range []Options{
		{Target: target, Weeks: 2, History: 4, Now: end},
		{Target: target, Weeks: 4, History: 6, Now: end, Summary: true},
		{Target: target, Weeks: 13, History: 26, Now: end},
	} {
		rep, err := l.Report(o)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rep)
		var ids []string
		for _, m := range line.Registry {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		for _, id := range ids {
			for _, wk := range []int{-1, 0, 1, 3} {
				d, err := l.Drill(o, id, wk)
				if err != nil {
					continue
				}
				all = append(all, d)
			}
		}
	}
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestGuardExistingReportsDoNotChange(t *testing.T) {
	got := map[string]string{}
	for name, build := range guardScenarios(t) {
		l, target := build()
		got[name] = guardOutput(t, l, target)
	}
	if os.Getenv("FACTORYMETER_GUARD_UPDATE") != "" {
		b, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(guardFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(guardFile)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for name, g := range got {
		if want[name] != g {
			t.Errorf("%s: the report or a drill changed", name)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s: recorded and no longer run", name)
		}
	}
}

var _ = record.GitHub
