package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

func TestWeeksOf(t *testing.T) {
	for in, want := range map[string]int{"13w": 13, "26w": 26, " 1w ": 1, "14d": 2, "91d": 13} {
		if got, err := weeksOf(in); err != nil || got != want {
			t.Errorf("weeksOf(%q) = %d, %v. Want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "w", "0w", "-2w", "10d", "3m", "13", "two weeks"} {
		if _, err := weeksOf(in); err == nil {
			t.Errorf("weeksOf(%q) must fail", in)
		}
	}
}

// reportStore writes a store that holds four weeks of acme/app: six changes
// by a person, each approved after 2 hours and merged an hour later, and one
// that has waited 30 hours for a review.
func reportStore(t *testing.T, now time.Time) string {
	t.Helper()
	dir := t.TempDir()
	s, err := store.OpenWrite(dir, "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	var rows []record.Change
	for i := 1; i <= 6; i++ {
		created := now.Add(-time.Duration(i) * 24 * time.Hour)
		ok, merged := created.Add(2*time.Hour), created.Add(3*time.Hour)
		rows = append(rows, record.Change{Source: record.GitHub, ID: record.ChangeID(record.GitHub, "acme/app", i), Repo: "acme/app", Number: i,
			URL: fmt.Sprintf("https://example.invalid/%d", i), Title: fmt.Sprintf("Secret title %d", i),
			Author: record.Actor{Login: "dev", Kind: record.ActorUser}, HeadRef: "feat", BaseRef: "main", State: record.ChangeMerged,
			CreatedAt: created, UpdatedAt: merged, ReadyAt: &created, MergedAt: &merged, ClosedAt: &merged, Additions: 3, CommitCount: 1,
			Commits: []record.Commit{{SHA: fmt.Sprintf("sha%d", i), AuthoredAt: created, CommittedAt: created, Parents: 1}},
			Reviews: []record.Review{{Author: record.Actor{Login: "rev", Kind: record.ActorUser}, State: record.ReviewApproved, SubmittedAt: ok}}})
	}
	opened := now.Add(-30 * time.Hour)
	rows = append(rows, record.Change{Source: record.GitHub, ID: record.ChangeID(record.GitHub, "acme/app", 7), Repo: "acme/app", Number: 7,
		URL: "https://example.invalid/7", Title: "Secret title 7", Author: record.Actor{Login: "dev", Kind: record.ActorUser},
		HeadRef: "feat", BaseRef: "main", State: record.ChangeOpen, CreatedAt: opened, UpdatedAt: opened, ReadyAt: &opened, CommitCount: 1,
		Commits: []record.Commit{{SHA: "sha7", AuthoredAt: opened, CommittedAt: opened, Parents: 1}}})
	if _, _, err := store.Changes.Put(s, record.GitHub, "acme/app", rows); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScope(store.Scope{Source: record.GitHub, Name: "acme/app", From: now.Add(-4 * 7 * 24 * time.Hour), SyncedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReportCommand(t *testing.T) {
	run := func(args ...string) (string, error) {
		cmd := reportCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		cmd.SilenceUsage = true
		err := cmd.Execute()
		return out.String(), err
	}
	// No store: the error says what to run.
	if _, err := run("acme", "--store", t.TempDir()); !errors.Is(err, store.ErrNoStore) {
		t.Fatalf("err = %v", err)
	}

	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	dir := reportStore(t, now)

	text, err := run("acme/app", "--store", dir, "--since", "2w")
	if err != nil {
		t.Fatal(err)
	}
	// Review holds 2 hours of each change and merge 1.
	for _, want := range []string{"DELIVERY LINE: ACME/APP", "2 weeks, 2026-10-19 to 2026-11-02.", "Review is the constraint. It holds 67 percent of the hours changes spend on the line.",
		"THE LINE", "EACH STATION", "COVERAGE", "LIMITS", "  * acme/app#7, standing 30.0h, human: Secret title 7"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}

	// An owner reads every repository of the store. The default window is
	// 13 weeks, and the store holds 4.
	js, err := run("https://github.com/acme/", "--store", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rep contract.Report
	if err := json.Unmarshal([]byte(js), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Schema != contract.Version || rep.Target != "acme" || !rep.Window.Until.Equal(now) || len(rep.Window.Periods) != 4 ||
		rep.Window.Previous != nil || rep.Verdict.Constraint != line.Review || len(rep.Stations) != 7 || rep.Tool.Name != "factorymeter" {
		t.Errorf("json: %+v", rep)
	}

	// A summary to a file, and nothing printed.
	path := filepath.Join(t.TempDir(), "line.json")
	out, err := run("acme/app", "--store", dir, "--summary", "--json", path, "--format", "none")
	if err != nil || out != "" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, not := range []string{"acme", "Secret", "example.invalid"} {
		if strings.Contains(string(raw), not) {
			t.Errorf("the summary holds %q", not)
		}
	}
	if !strings.Contains(string(raw), `"schema": "factorymeter/v1"`) || !strings.Contains(string(raw), `"summary": true`) {
		t.Errorf("the summary file: %.300s", raw)
	}

	// The page: printed, and written to a file beside another format. It is
	// the page "serve" shows, with nothing that needs a server.
	web, err := run("acme/app", "--store", dir, "--since", "2w", "--format", "html")
	if err != nil || !strings.HasPrefix(web, "<!doctype html>") {
		t.Fatalf("html, err %v: %.200s", err, web)
	}
	for _, want := range []string{"<title>Delivery line: acme/app</title>", "<h1>Review is the constraint. It holds 67 percent of the hours changes spend on the line.</h1>", `id="station-review" data-name="Review" data-constraint`,
		`<a href="https://example.invalid/7" rel="noreferrer noopener">acme/app#7</a>`} {
		if !strings.Contains(web, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, not := range []string{"report.json", `class="seg"`, `class="menu"`} {
		if strings.Contains(web, not) {
			t.Errorf("the page holds %q", not)
		}
	}
	path = filepath.Join(t.TempDir(), "line.html")
	out, err = run("acme/app", "--store", dir, "--summary", "--html", path, "--format", "none")
	if err != nil || out != "" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if raw, err = os.ReadFile(path); err != nil || !strings.HasPrefix(string(raw), "<!doctype html>") {
		t.Fatalf("the html file, err %v: %.100s", err, raw)
	}
	for _, not := range []string{"acme", "Secret", "example.invalid"} {
		if strings.Contains(string(raw), not) {
			t.Errorf("the summary page holds %q", not)
		}
	}

	md, err := run("acme/app", "--store", dir, "--format", "markdown", "--until", "2026-11-01")
	if err != nil || !strings.Contains(md, "# Delivery line: acme/app") || !strings.Contains(md, "to 2026-11-01.") {
		t.Errorf("markdown, err %v:\n%.400s", err, md)
	}

	for _, bad := range [][]string{
		{"acme/app", "--store", dir, "--format", "yaml"},
		{"acme/app", "--store", dir, "--since", "10d"},
		{"acme/other", "--store", dir}, // not in the store
		{},
	} {
		if _, err := run(bad...); err == nil {
			t.Errorf("%v must fail", bad)
		}
	}
}
