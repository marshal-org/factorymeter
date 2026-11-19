package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

func TestRulesCommand(t *testing.T) {
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := rulesCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}

	// Every rule, in the order of the registry.
	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	at := -1
	for _, r := range line.Rules {
		i := strings.Index(out, r.ID+"\n  "+r.Title+"\n  Fires when: "+r.Fires+"\n")
		if i <= at {
			t.Errorf("%s is missing or out of order", r.ID)
		}
		at = i
	}

	// One rule by id: how it is sized and the practice it links to.
	out, err = run("review.slow")
	if err != nil {
		t.Fatal(err)
	}
	want := "review.slow\n  Cut the wait for review\n  Fires when: The station is the constraint."
	if !strings.HasPrefix(out, want) || strings.Contains(out, "merge.slow") ||
		!strings.Contains(out, "  Reads: review.held.total, review.held.share, review.wait.p50, review.work.p50, review.queue.\n  Hours freed: The mean hours") ||
		!strings.HasSuffix(out, "  Practice: https://thesoftwarefactory.site/playbook/trust-is-earned-with-evidence/\n") {
		t.Errorf("one rule:\n%s", out)
	}

	// The rules of one station, as JSON.
	out, err = run("ci", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var list []line.Rule
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, " "); got != "ci.slow ci.failing ci.agents-fail-more" {
		t.Errorf("rules of ci: %s", got)
	}

	// Markdown is the generated reference page, whole.
	out, err = run("--format", "markdown")
	if err != nil || out != contract.RulesMarkdown() {
		t.Errorf("markdown is not the reference page: %v", err)
	}
	for args, want := range map[string]string{
		"spec":                     "is not a rule id",
		"review.nothing":           "is not a rule id",
		"ci --format markdown":     "prints the whole reference",
		"--format yaml":            "unknown format",
		"review.slow review.queue": "accepts at most 1 arg",
	} {
		if _, err := run(strings.Fields(args)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("rules %s: want an error with %q, got %v", args, want, err)
		}
	}
}
