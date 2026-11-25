package signature

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/model"
)

func mustDefault(t *testing.T) *Registry {
	t.Helper()
	r, err := Default()
	if err != nil {
		t.Fatalf("the built-in registry does not load: %v", err)
	}
	return r
}

func loadFixtures(t *testing.T) map[string]Fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "prs", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures under testdata/prs")
	}
	out := map[string]Fixture{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f Fixture
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out[filepath.Base(p)] = f
	}
	return out
}

// Every fixture is a real public pull request. The registry must keep
// classifying each one the way a person checked it.
func TestFixturesClassifyAsRecorded(t *testing.T) {
	reg := mustDefault(t)
	for name, f := range loadFixtures(t) {
		t.Run(name, func(t *testing.T) {
			if f.Note == "" {
				t.Error("the fixture has no note saying what it proves")
			}
			if f.PR.URL == "" || !strings.HasPrefix(f.PR.URL, "https://github.com/") {
				t.Errorf("the fixture does not name a real pull request: %q", f.PR.URL)
			}
			got := reg.ExpectFor(f.PR)
			if !reflect.DeepEqual(got, f.Expect) {
				t.Errorf("classification changed\n got: %+v\nwant: %+v", got, f.Expect)
			}
		})
	}
}

// A signature with no recorded real pull request is a guess. This test is what
// keeps guesses out of the registry.
func TestEverySignatureHasARealPullRequest(t *testing.T) {
	reg := mustDefault(t)
	hit := map[string][]string{}
	for name, f := range loadFixtures(t) {
		for _, m := range reg.Classify(f.PR).Matches {
			hit[m.Signature] = append(hit[m.Signature], name)
		}
		for _, rv := range f.PR.Reviews {
			for _, m := range reg.ClassifyReview(rv, f.PR.Author.Login).Matches {
				hit[m.Signature] = append(hit[m.Signature], name)
			}
		}
	}
	for _, id := range reg.SignatureIDs() {
		if len(hit[id]) == 0 {
			t.Errorf("signature %q matches no fixture. Record a real pull request for it with `factorymeter classify <pr> --record pkg/signature/testdata/prs`, or remove it", id)
		}
	}
}

// Each agent needs at least two real pull requests, and at least one fixture
// of every class must exist.
func TestFixtureSpread(t *testing.T) {
	reg := mustDefault(t)
	perAgent, perClass := map[string]int{}, map[Class]int{}
	possible := 0
	for _, f := range loadFixtures(t) {
		res := reg.Classify(f.PR)
		perClass[res.Class]++
		seen := map[string]bool{}
		for _, a := range res.Agents {
			seen[a] = true
		}
		// A review agent never opens pull requests: the reviews it wrote count.
		for _, rv := range f.PR.Reviews {
			if a := reg.ClassifyReview(rv, f.PR.Author.Login).Agent; a != "" {
				seen[a] = true
			}
		}
		for a := range seen {
			perAgent[a]++
		}
		if res.Class == ClassHuman && len(res.PossibleAgents) > 0 {
			possible++
		}
	}
	for _, a := range reg.Agents {
		if perAgent[a.ID] < 2 {
			t.Errorf("agent %q has %d fixtures, want at least 2", a.ID, perAgent[a.ID])
		}
	}
	for _, c := range []Class{ClassAgent, ClassHuman, ClassAutomation} {
		if perClass[c] == 0 {
			t.Errorf("no fixture of class %q", c)
		}
	}
	if possible == 0 {
		t.Error("no fixture of a human pull request that only a branch prefix points at")
	}
}

// Fixtures live in a public repository. They must not carry personal emails.
func TestFixturesCarryNoPersonalEmail(t *testing.T) {
	reg := mustDefault(t)
	for name, f := range loadFixtures(t) {
		check := func(where, s string) {
			for _, m := range bareEmailRe.FindAllString(s, -1) {
				local, domain, _ := strings.Cut(m, "@")
				if m != redactedEmail && !keepEmail(local, domain) {
					t.Errorf("%s: %s holds the address %q", name, where, m)
				}
			}
		}
		check("the description", f.PR.Body)
		for _, c := range f.PR.Commits {
			for _, a := range c.Authors {
				check("a commit author", a.Email)
				if a.Name != "" {
					t.Errorf("%s: a commit author name was kept", name)
				}
			}
			for _, tr := range c.Trailers {
				check("a trailer", tr)
			}
		}
		if f.PR.Title != "" || len(f.PR.CIRuns) != 0 {
			t.Errorf("%s: a title or CI runs were kept", name)
		}
		// A review keeps its text only when a review_body signature reads it;
		// any other text is a placeholder that keeps it non-empty.
		for _, rv := range f.PR.Reviews {
			check("a review", rv.Body)
			if rv.Body == "" || rv.Body == "[review text left out]" {
				continue
			}
			body := false
			for _, m := range reg.ClassifyReview(rv, f.PR.Author.Login).Matches {
				body = body || m.Kind == KindReviewBody
			}
			if !body {
				t.Errorf("%s: the text of a review by %s was kept, and no review_body signature reads it", name, rv.Author.Login)
			}
		}
	}
}

// The review rules, one case each, independent of the recorded fixtures.
func TestClassifyReviewRules(t *testing.T) {
	reg := mustDefault(t)
	user := func(l string) model.Actor { return model.Actor{Login: l, Type: "User"} }
	bot := func(l string) model.Actor { return model.Actor{Login: l, Type: "Bot"} }
	cases := []struct {
		name  string
		rv    model.Review
		class ReviewClass
		agent string
		why   string
	}{
		{"another person", model.Review{Author: user("ana"), Via: "review"}, ReviewHuman, "", "person"},
		{"another person, no text", model.Review{Author: user("ana")}, ReviewHuman, "", "person"},
		{"a known review bot", model.Review{Author: bot("gemini-code-assist[bot]"), Via: "review"}, ReviewAgent, "gemini-code-assist", "signature"},
		{"an unknown bot", model.Review{Author: bot("acme-reviewer[bot]"), Via: "review"}, ReviewAgent, "", "bot"},
		{"the author, with text", model.Review{Author: user("dev"), Via: "review", Body: "## Review\nTwo findings."}, ReviewAgent, "", "self"},
		{"the author, a thread reply", model.Review{Author: user("dev"), Via: "review"}, ReviewNone, "", "self"},
		{"the author, with a Claude Code footer", model.Review{Author: user("dev"), Body: "Looks fine.\n\nGenerated with [Claude Code](https://claude.com/claude-code)"}, ReviewAgent, "claude-code", "signature"},
		{"a person's comment with a Claude Code footer", model.Review{Author: user("ana"), Via: "comment", Body: "x\nGenerated with [Claude Code](https://claude.com/claude-code)"}, ReviewAgent, "claude-code", "signature"},
		{"a comment by a release bot", model.Review{Author: bot("release-please[bot]"), Via: "comment", Body: "Release 1.2.0"}, ReviewNone, "", "comment"},
		{"a comment by a person", model.Review{Author: user("ana"), Via: "comment", Body: "LGTM"}, ReviewNone, "", "comment"},
		{"a deleted account", model.Review{Author: user("ghost")}, ReviewNone, "", "no account"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reg.ClassifyReview(c.rv, "dev")
			if got.Class != c.class || got.Agent != c.agent || got.Why != c.why {
				t.Errorf("got %s/%s/%s, want %s/%s/%s", got.Class, got.Agent, got.Why, c.class, c.agent, c.why)
			}
		})
	}
}

// A review signature never changes who wrote the pull request.
func TestReviewSignaturesLeaveThePullRequestClassAlone(t *testing.T) {
	reg := mustDefault(t)
	pr := model.PR{Author: model.Actor{Login: "dev", Type: "User"}, Body: "fix",
		Reviews: []model.Review{{Author: model.Actor{Login: "claude[bot]", Type: "Bot"}, Body: "Generated with [Claude Code](https://claude.com/claude-code)"}}}
	if got := reg.Classify(pr).Class; got != ClassHuman {
		t.Fatalf("class = %s, want human", got)
	}
}

func TestClassifyRules(t *testing.T) {
	reg := mustDefault(t)
	human := model.Actor{Login: "someone", Type: "User"}
	commit := func(trailers ...string) []model.Commit {
		return []model.Commit{{SHA: "abc", Parents: 1, Authors: []model.CommitAuthor{{Login: "someone", Email: "someone@example.com"}}, Trailers: trailers}}
	}
	cases := []struct {
		name     string
		pr       model.PR
		class    Class
		agents   []string
		possible []string
		conf     Confidence
	}{
		{name: "plain human", pr: model.PR{Author: human, HeadRef: "fix/typo", Commits: commit("Signed-off-by: Some One <someone@example.com>")}, class: ClassHuman},
		{name: "branch prefix alone is only a possible agent", pr: model.PR{Author: human, HeadRef: "claude/fix-typo", Commits: commit()}, class: ClassHuman, possible: []string{"claude-code"}},
		{name: "a prefix must be a prefix", pr: model.PR{Author: human, HeadRef: "feature/claude/fix", Commits: commit()}, class: ClassHuman},
		{name: "trailer is medium", pr: model.PR{Author: human, HeadRef: "fix/typo", Commits: commit("Co-Authored-By: Claude <noreply@anthropic.com>")}, class: ClassAgent, agents: []string{"claude-code"}, conf: Medium},
		{name: "trailer key case does not matter", pr: model.PR{Author: human, Commits: commit("co-authored-by: Claude Opus <noreply@anthropic.com>")}, class: ClassAgent, agents: []string{"claude-code"}, conf: Medium},
		{name: "agent account is high", pr: model.PR{Author: model.Actor{Login: "devin-ai-integration[bot]", Type: "Bot"}, HeadRef: "devin/1-x"}, class: ClassAgent, agents: []string{"devin"}, conf: High},
		{name: "login match ignores case", pr: model.PR{Author: model.Actor{Login: "Copilot-SWE-Agent[bot]", Type: "Bot"}}, class: ClassAgent, agents: []string{"copilot"}, conf: High},
		{name: "two agents in one pull request", pr: model.PR{Author: human, Commits: commit("Co-authored-by: Cursor <cursoragent@cursor.com>", "Co-Authored-By: Claude <noreply@anthropic.com>")}, class: ClassAgent, agents: []string{"cursor", "claude-code"}, conf: Medium},
		{name: "listed automation", pr: model.PR{Author: model.Actor{Login: "dependabot[bot]", Type: "Bot"}, HeadRef: "dependabot/npm/x"}, class: ClassAutomation},
		{name: "unlisted bot is automation", pr: model.PR{Author: model.Actor{Login: "some-internal-sync[bot]", Type: "Bot"}}, class: ClassAutomation},
		{name: "an agent on a bot pull request still wins", pr: model.PR{Author: model.Actor{Login: "github-actions[bot]", Type: "Bot"}, Body: "Generated with [Claude Code](https://claude.com/claude-code)"}, class: ClassAgent, agents: []string{"claude-code"}, conf: Medium},
		{name: "prose that names a trailer is not a trailer", pr: model.PR{Author: human, Body: "We drop the Co-Authored-By: Claude line from now on.", Commits: commit()}, class: ClassHuman},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reg.Classify(c.pr)
			if got.Class != c.class || !reflect.DeepEqual(got.Agents, c.agents) || !reflect.DeepEqual(got.PossibleAgents, c.possible) || got.Confidence != c.conf {
				t.Errorf("got class=%s agents=%v possible=%v confidence=%q\nwant class=%s agents=%v possible=%v confidence=%q\nmatches: %+v",
					got.Class, got.Agents, got.PossibleAgents, got.Confidence, c.class, c.agents, c.possible, c.conf, got.Matches)
			}
		})
	}
}

func TestTrailers(t *testing.T) {
	msg := "feat: add a thing (#12)\r\n\r\n* first commit\r\n\r\nCo-authored-by: Cursor <cursoragent@cursor.com>\r\n\r\n* second commit\r\n\r\n" +
		"See https://example.com/a: it explains why.\nNote: this is not a trailer\nSigned-off-by: A Person <a@example.com>\n  Co-Authored-By: Claude <noreply@anthropic.com>  \nClaude-Session: https://claude.ai/code/session_01abc\nEmpty-Value:\n-Bad-Key: x\n"
	got := Trailers(msg)
	want := []string{
		"Co-authored-by: Cursor <cursoragent@cursor.com>",
		"Signed-off-by: A Person <a@example.com>",
		"Co-Authored-By: Claude <noreply@anthropic.com>",
		"Claude-Session: https://claude.ai/code/session_01abc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := Trailers("Co-authored-by: X <x@example.com>"); len(got) != 0 {
		t.Errorf("the subject line is never a trailer, got %q", got)
	}
}

func TestParseRejectsBadRegistries(t *testing.T) {
	base := func(sig string) string {
		return "version: 1\nagents:\n  - id: a\n    name: A\n    signatures:\n" + sig
	}
	cases := map[string]struct{ yaml, want string }{
		"wrong version":       {"version: 2\nagents:\n  - id: a\n    name: A\n    signatures:\n      - {id: s, kind: pr_body, regex: 'x', confidence: high}\n", "version"},
		"no agents":           {"version: 1\nagents: []\n", "empty"},
		"unknown field":       {base("      - {id: s, kind: pr_body, regex: 'x', confidence: high, weight: 3}\n"), "weight"},
		"unknown kind":        {base("      - {id: s, kind: commit_message, regex: 'x', confidence: high}\n"), "unknown kind"},
		"bad confidence":      {base("      - {id: s, kind: pr_body, regex: 'x', confidence: certain}\n"), "confidence"},
		"two matchers":        {base("      - {id: s, kind: pr_body, regex: 'x', equals: [y], confidence: high}\n"), "exactly one"},
		"no matcher":          {base("      - {id: s, kind: pr_body, confidence: high}\n"), "exactly one"},
		"bad regex":           {base("      - {id: s, kind: pr_body, regex: '(', confidence: high}\n"), "regex"},
		"regex matches all":   {base("      - {id: s, kind: pr_body, regex: 'x*', confidence: high}\n"), "empty string"},
		"duplicate id":        {base("      - {id: s, kind: pr_body, regex: 'x', confidence: high}\n      - {id: s, kind: pr_body, regex: 'y', confidence: high}\n"), "twice"},
		"strong branch":       {base("      - {id: s, kind: branch_prefix, prefix: ['a/'], confidence: medium}\n"), "low confidence"},
		"branch needs prefix": {base("      - {id: s, kind: branch_prefix, equals: ['a/'], confidence: low}\n"), "prefix matcher"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatal("the registry was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestRegistryDatesAreReal(t *testing.T) {
	reg := mustDefault(t)
	for _, a := range reg.Agents {
		for _, s := range a.Signatures {
			if _, err := time.Parse("2006-01-02", s.Verified); err != nil {
				t.Errorf("signature %q: verified %q is not a date", s.ID, s.Verified)
			}
		}
	}
}

func TestRedact(t *testing.T) {
	when := time.Date(2026, 11, 3, 14, 30, 0, 0, time.UTC)
	pr := model.PR{
		Repo: "o/r", Number: 7, URL: "https://github.com/o/r/pull/7", Title: "Secret plan",
		Author: model.Actor{Login: "someone", Type: "User"}, HeadRef: "cursor/x", CreatedAt: when,
		Body:    strings.Repeat("x", 3000) + " mail me at jane@corp.example " + emDash + " thanks. Generated with [Claude Code](https://claude.com/claude-code)",
		Reviews: []model.Review{{Author: model.Actor{Login: "r"}, State: "APPROVED"}},
		CIRuns:  []model.CIRun{{ID: 1}},
		Commits: []model.Commit{{
			SHA: "abc", AuthoredAt: when, CommittedAt: when, Parents: 1,
			Authors: []model.CommitAuthor{
				{Login: "someone", Email: "jane@corp.example", Name: "Jane Doe"},
				{Login: "cursoragent", Email: "cursoragent@cursor.com", Name: "Cursor Agent"},
				{Login: "ghstaff", Email: "ghstaff@github.com", Name: "Staff"},
				{Login: "someone", Email: "123+someone@users.noreply.github.com"},
				{Login: "Copilot", Email: "198982749+Copilot@users.noreply.github.com"},
			},
			Trailers: []string{
				"Co-authored-by: Jane Doe <jane@corp.example>",
				"Co-authored-by: Cursor <cursoragent@cursor.com>",
				"Signed-off-by: jane@corp.example",
				"Change-Id: I0123",
			},
		}},
	}
	got := Redact(pr)
	if got.Title != "" || got.Reviews != nil || got.CIRuns != nil {
		t.Error("title, reviews or CI runs survived")
	}
	if len(got.Body) > bodyTail+100 || strings.Contains(got.Body, "jane@") || strings.ContainsAny(got.Body, LongDashes) || !strings.Contains(got.Body, "[Claude Code]") {
		t.Errorf("the description was not cut and cleaned: %q", got.Body[len(got.Body)-140:])
	}
	if !got.CreatedAt.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the time of day survived: %s", got.CreatedAt)
	}
	wantEmails := []string{redactedEmail, "cursoragent@cursor.com", redactedEmail, redactedEmail, "198982749+Copilot@users.noreply.github.com"}
	for i, a := range got.Commits[0].Authors {
		if a.Email != wantEmails[i] || a.Name != "" {
			t.Errorf("author %d: got email %q name %q, want email %q and no name", i, a.Email, a.Name, wantEmails[i])
		}
	}
	wantTrailers := []string{
		"Co-authored-by: redacted <" + redactedEmail + ">",
		"Co-authored-by: Cursor <cursoragent@cursor.com>",
		"Signed-off-by: redacted <" + redactedEmail + ">",
		"Change-Id: I0123",
	}
	if !reflect.DeepEqual(got.Commits[0].Trailers, wantTrailers) {
		t.Errorf("trailers\n got: %q\nwant: %q", got.Commits[0].Trailers, wantTrailers)
	}
}
