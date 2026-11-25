package synth_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/signature"
	"github.com/marshal-org/factorymeter/pkg/synth"
)

var _ measure.Source = (*synth.Source)(nil)

func small(seed int64) synth.Config {
	return synth.Config{Seed: seed, Repos: 3, ChangesPerWeek: 45, Weeks: 4}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func registry(t *testing.T) *signature.Registry {
	t.Helper()
	reg, err := signature.Default()
	must(t, err)
	return reg
}

// digest hashes every record of a source, in the order the source gives them.
func digest(t *testing.T, s measure.Source) (sum [32]byte, n int) {
	t.Helper()
	h := sha256.New()
	put := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(len(b)))
		h.Write(l[:])
		h.Write(b)
		n++
	}
	w := measure.Window{}
	for _, sc := range s.Scopes() {
		put(sc)
		must(t, s.Changes(sc, w, func(c *record.Change) { put(c) }))
		must(t, s.CIRuns(sc, w, func(c *record.CIRun) { put(c) }))
		must(t, s.Deploys(sc, w, func(d *record.Deploy) { put(d) }))
		must(t, s.Tickets(sc, w, func(x *record.Ticket) { put(x) }))
		must(t, s.Links(sc, w, func(l *record.Link) { put(l) }))
	}
	copy(sum[:], h.Sum(nil))
	return sum, n
}

func TestSameSeedSameRecords(t *testing.T) {
	a, n := digest(t, synth.New(small(7)))
	b, m := digest(t, synth.New(small(7)))
	if a != b || n != m {
		t.Fatalf("the same seed gave different records (%d and %d)", n, m)
	}
	if n < 100 {
		t.Fatalf("only %d records", n)
	}
}

func TestDifferentSeedDifferentRecords(t *testing.T) {
	a, _ := digest(t, synth.New(small(7)))
	b, _ := digest(t, synth.New(small(8)))
	if a == b {
		t.Fatal("two seeds gave the same records")
	}
}

// A repository does not depend on which others were read before it.
func TestRepoDoesNotDependOnOrder(t *testing.T) {
	cfg := small(3)
	cfg.Repos = 6
	one, two := synth.New(cfg), synth.New(cfg)
	scopes := one.Scopes()
	ids := func(s *synth.Source, sc measure.Scope) string {
		var out []string
		must(t, s.Changes(sc, measure.Window{}, func(c *record.Change) { out = append(out, c.ID+c.CreatedAt.String()) }))
		return strings.Join(out, ",")
	}
	want := ids(one, scopes[4])
	ids(two, scopes[1])
	ids(two, scopes[0])
	got := ids(two, scopes[4])
	if want != got || want == "" {
		t.Fatal("repository 4 changed with the order the repositories were read in")
	}
}

func TestWindowCutsByCreationTime(t *testing.T) {
	s := synth.New(small(5))
	sc := s.Scopes()[0]
	cfg := s.Config()
	w := measure.Window{From: cfg.Start.Add(7 * 24 * time.Hour), To: cfg.Start.Add(14 * 24 * time.Hour)}
	var all, cut int
	must(t, s.Changes(sc, measure.Window{}, func(*record.Change) { all++ }))
	must(t, s.Changes(sc, w, func(c *record.Change) {
		cut++
		if c.CreatedAt.Before(w.From) || c.CreatedAt.After(w.To) {
			t.Errorf("change %s created %v is outside the window", c.ID, c.CreatedAt)
		}
	}))
	must(t, s.CIRuns(sc, w, func(x *record.CIRun) {
		if x.CreatedAt.Before(w.From) || x.CreatedAt.After(w.To) {
			t.Errorf("run %s is outside the window", x.ID)
		}
	}))
	must(t, s.Deploys(sc, w, func(d *record.Deploy) {
		if d.CreatedAt.Before(w.From) || d.CreatedAt.After(w.To) {
			t.Errorf("deploy %s is outside the window", d.ID)
		}
	}))
	if cut == 0 || cut >= all {
		t.Fatalf("the window kept %d of %d changes", cut, all)
	}
}

func TestUnknownScopeIsAnError(t *testing.T) {
	s := synth.New(small(1))
	for _, name := range []string{"nope", "acme-000/svc-099", "acme-009/svc-001", "acme-000/svc-"} {
		if err := s.Changes(measure.Scope{Source: record.GitHub, Name: name}, measure.Window{}, func(*record.Change) {}); err == nil {
			t.Errorf("%q: no error", name)
		}
	}
}

// reviewClass says who wrote a review the way the builder does.
func reviewClass(reg *signature.Registry, c *record.Change, r *record.Review) signature.ReviewClass {
	rv := model.Review{Author: model.Actor{Login: r.Author.Login, Type: "User"}, State: r.State, Via: r.Via, Body: r.Body}
	if r.Author.Kind == record.ActorBot {
		rv.Author.Type = "Bot"
	}
	return reg.ClassifyReview(rv, c.Author.Login).Class
}

// count tallies what an organization holds.
func count(t *testing.T, s *synth.Source) map[string]int {
	t.Helper()
	reg := registry(t)
	k := map[string]int{}
	w := measure.Window{}
	for _, sc := range s.Scopes() {
		must(t, s.Changes(sc, w, func(c *record.Change) {
			k["change"]++
			k["state:"+c.State]++
			if c.Draft {
				k["draft"]++
			}
			if c.ReadyAt != nil && c.ReadyAt.After(c.CreatedAt) {
				k["draft then ready"]++
			}
			if c.ReadyAt == nil {
				k["never ready"]++
			}
			if strings.Contains(c.Body, "Reverts ") {
				k["revert"]++
			}
			class, _ := measure.Classify(reg, c)
			k["author:"+class]++
			if c.CreatedAt.Nanosecond()%1000 != 0 {
				k["nanoseconds"]++
			}
			var firstReview time.Time
			for i, rv := range c.Reviews {
				k["review:"+rv.State]++
				if rv.Via == record.ReviewViaComment {
					k["review via comment"]++
				}
				if i == 0 {
					firstReview = rv.SubmittedAt
				} else if rv.SubmittedAt.Equal(c.Reviews[i-1].SubmittedAt) {
					k["tie between reviews"]++
				}
				if c.ClosedAt != nil && rv.SubmittedAt.After(*c.ClosedAt) {
					k["review after close"]++
				}
			}
			for _, cm := range c.Commits {
				if cm.Parents >= 2 {
					k["merge commit"]++
				}
				if !firstReview.IsZero() && cm.CommittedAt.After(firstReview) && cm.Parents < 2 {
					k["commit after first review"]++
				}
				if firstReview.Equal(cm.CommittedAt) {
					k["review at commit time"]++
				}
			}
			if c.CommitCount > len(c.Commits) {
				k["commit list cut"]++
			}
		}))
		must(t, s.CIRuns(sc, w, func(x *record.CIRun) {
			k["ci run"]++
			k["ci event:"+x.Event]++
			k["ci conclusion:"+x.Conclusion]++
			if x.StartedAt.After(x.CreatedAt) {
				k["ci run with runner wait"]++
			} else {
				k["ci run without runner wait"]++
			}
			if x.Attempt > 1 {
				k["ci rerun"]++
			}
		}))
		must(t, s.Deploys(sc, w, func(d *record.Deploy) {
			k["deploy:"+d.Via]++
			k["deploy outcome:"+d.Outcome]++
			if d.Production {
				k["production "+d.Via]++
			} else {
				k["non-production "+d.Via]++
			}
		}))
		must(t, s.Links(sc, w, func(l *record.Link) { k["link:"+l.Kind]++ }))
	}
	return k
}

func TestEveryKindOfRecordAppears(t *testing.T) {
	k := count(t, synth.New(synth.Config{Seed: 11, Repos: 4, ChangesPerWeek: 80, Weeks: 6}))
	for _, name := range []string{
		"change", "state:open", "state:closed", "state:merged", "draft", "draft then ready", "never ready", "revert",
		"author:human", "author:agent", "author:automation", "nanoseconds",
		"merge commit", "commit after first review", "commit list cut",
		"review:approved", "review:changes_requested", "review:commented", "review via comment", "review after close",
		"tie between reviews", "review at commit time",
		"ci run", "ci event:pull_request", "ci event:merge_group", "ci event:push", "ci conclusion:success", "ci conclusion:failure",
		"ci run with runner wait", "ci run without runner wait", "ci rerun",
		"deploy:deployment", "deploy:release", "production deployment", "production release", "non-production deployment",
		"deploy outcome:success", "deploy outcome:failure", "link:reverts",
	} {
		if k[name] == 0 {
			t.Errorf("no %q in a small organization: %v", name, k)
		}
	}
}

func TestReviewsComeFromPeopleAndAgents(t *testing.T) {
	s := synth.New(synth.Config{Seed: 2, Repos: 4, ChangesPerWeek: 80, Weeks: 6})
	reg := registry(t)
	var people, agents, none int
	for _, sc := range s.Scopes() {
		must(t, s.Changes(sc, measure.Window{}, func(c *record.Change) {
			for i := range c.Reviews {
				switch reviewClass(reg, c, &c.Reviews[i]) {
				case signature.ReviewHuman:
					people++
				case signature.ReviewAgent:
					agents++
				default:
					none++
				}
			}
		}))
	}
	if people == 0 || agents == 0 || none == 0 {
		t.Fatalf("reviews by people %d, by agents %d, not reviews %d", people, agents, none)
	}
}

func TestAgentChangesAreRecognized(t *testing.T) {
	s := synth.New(synth.Config{Seed: 4, Repos: 3, ChangesPerWeek: 60, Weeks: 4})
	reg := registry(t)
	named := map[string]int{}
	for _, sc := range s.Scopes() {
		must(t, s.Changes(sc, measure.Window{}, func(c *record.Change) {
			if class, agent := measure.Classify(reg, c); class == string(signature.ClassAgent) {
				if agent == "" {
					t.Errorf("%s is an agent change that no agent claims", c.ID)
				}
				named[agent]++
			}
		}))
	}
	if len(named) < 3 {
		t.Fatalf("only %d agents write changes: %v", len(named), named)
	}
}

func TestDeploysCarryMergeCommits(t *testing.T) {
	s := synth.New(synth.Config{Seed: 9, Repos: 6, ChangesPerWeek: 120, Weeks: 6})
	var matched, total int
	for _, sc := range s.Scopes() {
		merges := map[string]bool{}
		must(t, s.Changes(sc, measure.Window{}, func(c *record.Change) {
			if c.State == record.ChangeMerged && c.MergeSHA != "" {
				merges[c.MergeSHA] = true
			}
		}))
		must(t, s.Deploys(sc, measure.Window{}, func(d *record.Deploy) {
			if d.Production {
				total++
				if merges[d.SHA] {
					matched++
				}
			}
		}))
	}
	if total == 0 || matched == 0 || matched == total {
		t.Fatalf("%d of %d production deploys match a merge commit", matched, total)
	}
}

func TestRecordsHoldTogether(t *testing.T) {
	s := synth.New(synth.Config{Seed: 6, Repos: 3, ChangesPerWeek: 60, Weeks: 4})
	end := s.End()
	for _, sc := range s.Scopes() {
		seen := map[string]bool{}
		must(t, s.Changes(sc, measure.Window{}, func(c *record.Change) {
			if seen[c.ID] {
				t.Errorf("%s twice", c.ID)
			}
			seen[c.ID] = true
			if c.ID != record.ChangeID(record.GitHub, c.Repo, c.Number) || c.Repo != sc.Name {
				t.Errorf("%s: wrong id or repo", c.ID)
			}
			if (c.State == record.ChangeMerged) != (c.MergedAt != nil && c.MergeSHA != "") {
				t.Errorf("%s: state %s with merged_at %v and sha %q", c.ID, c.State, c.MergedAt, c.MergeSHA)
			}
			if (c.State == record.ChangeOpen) != (c.ClosedAt == nil) {
				t.Errorf("%s: state %s with closed_at %v", c.ID, c.State, c.ClosedAt)
			}
			if (c.ClosedAt != nil && c.ClosedAt.After(end)) || c.UpdatedAt.Before(c.CreatedAt) || len(c.Commits) == 0 {
				t.Errorf("%s: bad times or no commits", c.ID)
			}
			for i := 1; i < len(c.Reviews); i++ {
				if c.Reviews[i].SubmittedAt.Before(c.Reviews[i-1].SubmittedAt) {
					t.Errorf("%s: reviews are not oldest first", c.ID)
				}
			}
			for i := 1; i < len(c.Commits); i++ {
				if c.Commits[i].CommittedAt.Before(c.Commits[i-1].CommittedAt) {
					t.Errorf("%s: commits are not oldest first", c.ID)
				}
			}
		}))
		runs := map[string]bool{}
		must(t, s.CIRuns(sc, measure.Window{}, func(x *record.CIRun) {
			if runs[x.ID] {
				t.Errorf("run %s twice", x.ID)
			}
			runs[x.ID] = true
			if x.StartedAt.Before(x.CreatedAt) || x.UpdatedAt.Before(x.StartedAt) {
				t.Errorf("run %s: times out of order", x.ID)
			}
		}))
	}
}

// A line built from a small organization reports, in both review modes.
func TestSmallOrgLoadsAndReports(t *testing.T) {
	s := synth.New(synth.Config{Seed: 1, Repos: 3, ChangesPerWeek: 60, Weeks: 4})
	reg := registry(t)
	for _, o := range []measure.LoadOptions{{}, {HumanReviewsOnly: true}} {
		l, err := measure.LoadFromWith(s, reg, "acme", nil, o)
		must(t, err)
		rep, err := l.Report(measure.Options{Target: "acme", Weeks: 4, History: 4, Now: s.End()})
		must(t, err)
		b, err := json.Marshal(rep)
		if err != nil || len(b) < 1000 {
			t.Fatalf("report of %d bytes, %v", len(b), err)
		}
	}
	name := synth.RepoName(1)
	one, err := measure.LoadFrom(s, reg, name, nil)
	must(t, err)
	_, err = one.Report(measure.Options{Target: name, Weeks: 4, History: 4, Now: s.End()})
	must(t, err)
}

// The default organization is read one repository at a time. Nothing here
// holds the records of more than the repository being read.
func TestDefaultOrgStreamsRepoByRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("reads 300 repositories")
	}
	s := synth.New(synth.Default())
	scopes := s.Scopes()
	if len(scopes) != 300 {
		t.Fatalf("%d repositories", len(scopes))
	}
	var changes int
	w := measure.Window{}
	for _, sc := range scopes {
		must(t, s.Changes(sc, w, func(*record.Change) { changes++ }))
		must(t, s.CIRuns(sc, w, func(*record.CIRun) {}))
		must(t, s.Deploys(sc, w, func(*record.Deploy) {}))
	}
	want := 1500 * 26
	if changes < want*7/10 || changes > want*13/10 {
		t.Fatalf("%d changes, about %d expected", changes, want)
	}
}
