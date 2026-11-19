package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/metrics"
	"github.com/marshal-org/factorymeter/pkg/report"
)

// parseEvery reads the length of one period: "1w", "14d".
func parseEvery(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'w') {
		if v, err := strconv.Atoi(s[:n-1]); err == nil && v > 0 {
			if s[n-1] == 'w' {
				v *= 7
			}
			return time.Duration(v) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("cannot read %q as a period. Use a number of days or weeks such as 1w or 14d", s)
}

func trendCmd() *cobra.Command {
	var every, format, htmlPath, jsonPath, registryPath string
	var src source
	cmd := &cobra.Command{
		Use:   "trend <owner/repo | owner>",
		Short: "Show how the numbers of a snapshot changed over time",
		Long: `trend reads the same history as snapshot, once, and cuts it into periods of
the same length. It measures each period the way snapshot measures the whole
window, so every number of the snapshot becomes a line over time.

trend and snapshot read GitHub on every run and keep nothing. "sync" and
"report" are the way to follow a line week after week: they keep the history
in a local store and report by station.

The periods are counted back from the end of the window, so each one is
whole. The trend compares the first third of the periods with the last third
and says what changed.`,
		Example: `  factorymeter trend marshal-org/runlease
  factorymeter trend my-org --since 26w --every 2w --html trend.html
  factorymeter trend --from-raw raw.json --every 1w --format markdown`,
		Args: src.args,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "markdown" && format != "json" && format != "none" {
				return fmt.Errorf("unknown format %q. Use text, markdown, json or none", format)
			}
			step, err := parseEvery(every)
			if err != nil {
				return err
			}
			reg, err := loadRegistry(registryPath)
			if err != nil {
				return err
			}
			raw, now, from, to, err := src.load(cmd, args)
			if err != nil {
				return err
			}
			t, err := metrics.ComputeTrend(raw.Repos, reg, metrics.TrendOptions{
				Options: metrics.Options{Target: raw.Target, Version: version, Now: now, Since: from, Until: to}, Every: step})
			if err != nil {
				return err
			}
			if htmlPath != "" {
				if err := writeFile(htmlPath, func(w io.Writer) error { return report.TrendHTML(w, &t) }); err != nil {
					return err
				}
			}
			if jsonPath != "" {
				if err := writeFile(jsonPath, func(w io.Writer) error { return report.TrendJSON(w, &t) }); err != nil {
					return err
				}
			}
			switch format {
			case "markdown":
				return report.TrendMarkdown(os.Stdout, &t)
			case "json":
				return report.TrendJSON(os.Stdout, &t)
			case "none":
				return nil
			}
			return report.TrendText(os.Stdout, &t)
		},
	}
	f := cmd.Flags()
	src.flags(f, "12w")
	f.StringVar(&every, "every", "1w", "length of one period: days (14d) or weeks (1w)")
	f.StringVar(&format, "format", "text", "what to print: text, markdown, json or none")
	f.StringVar(&htmlPath, "html", "", "also write the one-page HTML trend to this file")
	f.StringVar(&jsonPath, "json", "", "also write the JSON export to this file")
	f.StringVar(&registryPath, "registry", "", "use this signature registry file instead of the built-in one")
	return cmd
}
