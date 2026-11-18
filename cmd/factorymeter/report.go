package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/collector"
	"github.com/marshal-org/factorymeter/pkg/metrics"
	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/report"
)

// rawFile is what --save-raw writes and --from-raw reads: everything collected
// from GitHub, before any classification or arithmetic.
type rawFile struct {
	Target string           `json:"target"`
	Repos  []model.RepoData `json:"repos"`
}

// parseWhen reads "30d", "12w" or a date.
func parseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'w') {
		if v, err := strconv.Atoi(s[:n-1]); err == nil && v > 0 {
			days := v
			if s[n-1] == 'w' {
				days = v * 7
			}
			return now.AddDate(0, 0, -days), nil
		}
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a window. Use a number of days or weeks such as 30d or 12w, or a date such as 2026-01-31", s)
}

func writeFile(path string, write func(io.Writer) error) error {
	if path == "-" {
		return write(os.Stdout)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func reportCmd() *cobra.Command {
	var since, until, format, htmlPath, jsonPath, saveRaw, fromRaw, registryPath string
	var quiet, detail, noCI, noDeploys, forks, archived bool
	var maxRuns int
	cmd := &cobra.Command{
		Use:   "report <owner/repo | owner>",
		Short: "Measure a repository or every repository of an owner",
		Long: `report reads pull requests, reviews, commits, GitHub Actions runs and
deployments over a time window and prints the report.

Give owner/repo for one repository, or an owner for all of its active
repositories. The report goes to standard output. Progress goes to standard
error.`,
		Example: `  factorymeter report marshal-org/runlease
  factorymeter report my-org --since 90d --html report.html --json report.json
  factorymeter report owner/repo --format markdown >> "$GITHUB_STEP_SUMMARY"`,
		Args: func(cmd *cobra.Command, args []string) error {
			if fromRaw != "" && len(args) == 0 {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "markdown" && format != "json" && format != "none" {
				return fmt.Errorf("unknown format %q. Use text, markdown, json or none", format)
			}
			reg, err := loadRegistry(registryPath)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			var raw rawFile
			if fromRaw != "" {
				data, err := os.ReadFile(fromRaw)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &raw); err != nil {
					return fmt.Errorf("%s: %w", fromRaw, err)
				}
				if len(raw.Repos) == 0 {
					return errors.New(fromRaw + " holds no repositories")
				}
				now = raw.Repos[0].Until
			} else {
				from, err := parseWhen(since, now)
				if err != nil {
					return err
				}
				to := now
				if until != "" {
					if to, err = parseWhen(until, now); err != nil {
						return err
					}
				}
				if !from.Before(to) {
					return errors.New("the window is empty: --since must be before --until")
				}
				c, err := newClient(quiet)
				if err != nil {
					return err
				}
				k := &collector.Collector{C: c, Opt: collector.Options{Since: from, Until: to, MaxRuns: maxRuns, SkipCI: noCI, SkipDeploys: noDeploys,
					IncludeForks: forks, IncludeArchived: archived}}
				if !quiet {
					k.Opt.Logf = progress
				}
				raw.Target = strings.TrimSuffix(strings.TrimPrefix(args[0], "https://github.com/"), "/")
				if raw.Repos, err = k.Target(cmd.Context(), raw.Target); err != nil {
					return err
				}
				if !quiet {
					progress("%d GitHub API calls", c.Calls)
				}
			}
			if saveRaw != "" {
				if err := writeFile(saveRaw, func(w io.Writer) error {
					enc := json.NewEncoder(w)
					enc.SetEscapeHTML(false)
					return enc.Encode(raw)
				}); err != nil {
					return err
				}
			}
			rep := metrics.Compute(raw.Repos, reg, metrics.Options{Target: raw.Target, Version: version, Now: now, Detail: detail})
			if htmlPath != "" {
				if err := writeFile(htmlPath, func(w io.Writer) error { return report.HTML(w, &rep) }); err != nil {
					return err
				}
			}
			if jsonPath != "" {
				if err := writeFile(jsonPath, func(w io.Writer) error { return report.JSON(w, &rep) }); err != nil {
					return err
				}
			}
			switch format {
			case "markdown":
				return report.Markdown(os.Stdout, &rep)
			case "json":
				return report.JSON(os.Stdout, &rep)
			case "none":
				return nil
			}
			return report.Text(os.Stdout, &rep)
		},
	}
	f := cmd.Flags()
	f.StringVar(&since, "since", "30d", "start of the window: days (30d), weeks (12w) or a date")
	f.StringVar(&until, "until", "", "end of the window, same forms as --since (default now)")
	f.StringVar(&format, "format", "text", "what to print: text, markdown, json or none")
	f.StringVar(&htmlPath, "html", "", "also write the one-page HTML report to this file")
	f.StringVar(&jsonPath, "json", "", "also write the JSON export to this file")
	f.BoolVar(&detail, "detail", false, "add one row per pull request to the JSON export")
	f.StringVar(&saveRaw, "save-raw", "", "write everything collected from GitHub to this file")
	f.StringVar(&fromRaw, "from-raw", "", "build the report from a --save-raw file instead of calling GitHub")
	f.StringVar(&registryPath, "registry", "", "use this signature registry file instead of the built-in one")
	f.BoolVar(&noCI, "no-ci", false, "do not read GitHub Actions runs")
	f.BoolVar(&noDeploys, "no-deploys", false, "do not read deployments and releases")
	f.IntVar(&maxRuns, "max-runs", 0, "most workflow runs to read per repository (default 30000)")
	f.BoolVar(&forks, "forks", false, "include forks when the target is an owner")
	f.BoolVar(&archived, "archived", false, "include archived repositories when the target is an owner")
	f.BoolVarP(&quiet, "quiet", "q", false, "do not print progress")
	return cmd
}
