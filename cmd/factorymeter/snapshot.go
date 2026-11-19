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
	"github.com/spf13/pflag"

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

// source is where the pull requests come from: GitHub, or a --save-raw file.
// The report and the trend share it.
type source struct {
	since, until, saveRaw, fromRaw          string
	quiet, noCI, noDeploys, forks, archived bool
	maxRuns                                 int
}

func (s *source) flags(f *pflag.FlagSet, since string) {
	f.StringVar(&s.since, "since", since, "start of the window: days (30d), weeks (12w) or a date")
	f.StringVar(&s.until, "until", "", "end of the window, same forms as --since (default now)")
	f.StringVar(&s.saveRaw, "save-raw", "", "write everything collected from GitHub to this file")
	f.StringVar(&s.fromRaw, "from-raw", "", "read a --save-raw file instead of calling GitHub. --since and --until then pick a part of it")
	f.BoolVar(&s.noCI, "no-ci", false, "do not read GitHub Actions runs")
	f.BoolVar(&s.noDeploys, "no-deploys", false, "do not read deployments and releases")
	f.IntVar(&s.maxRuns, "max-runs", 0, "most workflow runs to read per repository (default 30000)")
	f.BoolVar(&s.forks, "forks", false, "include forks when the target is an owner")
	f.BoolVar(&s.archived, "archived", false, "include archived repositories when the target is an owner")
	f.BoolVarP(&s.quiet, "quiet", "q", false, "do not print progress")
}

// args wants a target, unless a raw file supplies one.
func (s *source) args(cmd *cobra.Command, args []string) error {
	if s.fromRaw != "" && len(args) == 0 {
		return nil
	}
	return cobra.ExactArgs(1)(cmd, args)
}

// load collects from GitHub or reads the raw file. from and to are the window
// asked for. With a raw file they are zero unless the flag was given.
func (s *source) load(cmd *cobra.Command, args []string) (raw rawFile, now, from, to time.Time, err error) {
	now = time.Now().UTC()
	if s.fromRaw != "" {
		data, err := os.ReadFile(s.fromRaw)
		if err != nil {
			return raw, now, from, to, err
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return raw, now, from, to, fmt.Errorf("%s: %w", s.fromRaw, err)
		}
		if len(raw.Repos) == 0 {
			return raw, now, from, to, errors.New(s.fromRaw + " holds no repositories")
		}
		now = raw.Repos[0].Until
		if cmd.Flags().Changed("until") {
			if to, err = parseWhen(s.until, now); err != nil {
				return raw, now, from, to, err
			}
		}
		if cmd.Flags().Changed("since") {
			end := now
			if !to.IsZero() {
				end = to
			}
			if from, err = parseWhen(s.since, end); err != nil {
				return raw, now, from, to, err
			}
		}
	} else {
		if from, err = parseWhen(s.since, now); err != nil {
			return raw, now, from, to, err
		}
		to = now
		if s.until != "" {
			if to, err = parseWhen(s.until, now); err != nil {
				return raw, now, from, to, err
			}
		}
		if !from.Before(to) {
			return raw, now, from, to, errors.New("the window is empty: --since must be before --until")
		}
		c, err := newClient(s.quiet)
		if err != nil {
			return raw, now, from, to, err
		}
		k := &collector.Collector{C: c, Opt: collector.Options{Since: from, Until: to, MaxRuns: s.maxRuns, SkipCI: s.noCI, SkipDeploys: s.noDeploys,
			IncludeForks: s.forks, IncludeArchived: s.archived}}
		if !s.quiet {
			k.Opt.Logf = progress
		}
		if raw.Target, err = parseTarget(args[0]); err != nil {
			return raw, now, from, to, err
		}
		if raw.Repos, err = k.Target(cmd.Context(), raw.Target); err != nil {
			return raw, now, from, to, err
		}
		if !s.quiet {
			progress("%d GitHub API calls", c.Calls)
		}
	}
	if s.saveRaw != "" {
		err = writeFile(s.saveRaw, func(w io.Writer) error {
			enc := json.NewEncoder(w)
			enc.SetEscapeHTML(false)
			return enc.Encode(raw)
		})
	}
	return raw, now, from, to, err
}

func snapshotCmd() *cobra.Command {
	var format, htmlPath, jsonPath, registryPath string
	var detail bool
	var src source
	cmd := &cobra.Command{
		Use:   "snapshot <owner/repo | owner>",
		Short: "Read a window straight from GitHub and print the first-generation report",
		Long: `snapshot reads pull requests, reviews, commits, GitHub Actions runs and
deployments over a time window from GitHub and prints the first-generation
report. It keeps nothing. "sync" and "report" are the way to measure a line
week after week: they read GitHub once and report from a local store.

Give owner/repo for one repository, or an owner for all of its active
repositories. The report goes to standard output. Progress goes to standard
error.`,
		Example: `  factorymeter snapshot marshal-org/runlease
  factorymeter snapshot my-org --since 90d --html report.html --json report.json
  factorymeter snapshot owner/repo --format markdown >> "$GITHUB_STEP_SUMMARY"`,
		Args: src.args,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "markdown" && format != "json" && format != "none" {
				return fmt.Errorf("unknown format %q. Use text, markdown, json or none", format)
			}
			reg, err := loadRegistry(registryPath)
			if err != nil {
				return err
			}
			raw, now, from, to, err := src.load(cmd, args)
			if err != nil {
				return err
			}
			rep := metrics.Compute(raw.Repos, reg, metrics.Options{Target: raw.Target, Version: version, Now: now, Detail: detail, Since: from, Until: to})
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
	src.flags(f, "30d")
	f.StringVar(&format, "format", "text", "what to print: text, markdown, json or none")
	f.StringVar(&htmlPath, "html", "", "also write the one-page HTML report to this file")
	f.StringVar(&jsonPath, "json", "", "also write the JSON export to this file")
	f.BoolVar(&detail, "detail", false, "add one row per pull request to the JSON export")
	f.StringVar(&registryPath, "registry", "", "use this signature registry file instead of the built-in one")
	return cmd
}
