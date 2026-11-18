package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/metrics"
	"github.com/marshal-org/factorymeter/pkg/model"
)

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"30d":                       "2026-03-01T12:00:00Z",
		"2w":                        "2026-03-17T12:00:00Z",
		" 1d ":                      "2026-03-30T12:00:00Z",
		"2026-01-31":                "2026-01-31T00:00:00Z",
		"2026-01-31T10:00:00+02:00": "2026-01-31T08:00:00Z",
	} {
		got, err := parseWhen(in, now)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("parseWhen(%q) = %v %v, want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "0d", "-3d", "3m", "yesterday", "31/01/2026"} {
		if _, err := parseWhen(bad, now); err == nil {
			t.Errorf("parseWhen(%q) must fail", bad)
		}
	}
}

// The whole path without the network: a saved collection goes in, and the
// HTML and JSON files come out.
func TestReportFromRaw(t *testing.T) {
	dir := t.TempDir()
	until := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	since := until.AddDate(0, 0, -30)
	merged := since.Add(50 * time.Hour)
	raw := rawFile{Target: "o/r", Repos: []model.RepoData{{Repo: "o/r", Since: since, Until: until, CISource: "none", DeploySource: "none",
		PRs: []model.PR{{Repo: "o/r", Number: 1, Author: model.Actor{Login: "claude[bot]", Type: "Bot"}, State: "MERGED", HeadRef: "claude/issue-1",
			CreatedAt: since.Add(48 * time.Hour), UpdatedAt: merged, MergedAt: &merged,
			Commits: []model.Commit{{SHA: "a", Parents: 1, CommittedAt: since.Add(48 * time.Hour)}}}}}}}
	data, _ := json.Marshal(raw)
	in := filepath.Join(dir, "raw.json")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	htmlPath, jsonPath := filepath.Join(dir, "r.html"), filepath.Join(dir, "r.json")
	cmd := reportCmd()
	cmd.SetArgs([]string{"--from-raw", in, "--format", "none", "--html", htmlPath, "--json", jsonPath, "--detail"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rep metrics.Report
	js, _ := os.ReadFile(jsonPath)
	if err := json.Unmarshal(js, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Target != "o/r" || rep.Agent.Merged != 1 || rep.Human.Merged != 0 || len(rep.PRs) != 1 || !rep.Until.Equal(until) || rep.Constraint == nil {
		t.Errorf("report: target=%q agent=%d human=%d rows=%d", rep.Target, rep.Agent.Merged, rep.Human.Merged, len(rep.PRs))
	}
	page, _ := os.ReadFile(htmlPath)
	if !strings.Contains(string(page), "<h1>"+rep.Headline+"</h1>") {
		t.Error("the HTML report does not carry the headline of the JSON export")
	}

	for _, args := range [][]string{
		{"--from-raw", filepath.Join(dir, "missing.json")},
		{"--from-raw", in, "--format", "pdf"},
		{"--from-raw", in, "--registry", filepath.Join(dir, "missing.yaml")},
		{},
		{"o/r", "--since", "2026-03-01", "--until", "2026-02-01"},
		{"o/r", "--since", "soon"},
	} {
		cmd := reportCmd()
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v must fail", args)
		}
	}
}
