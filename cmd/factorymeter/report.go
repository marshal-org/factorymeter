package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/store"
	"github.com/marshal-org/factorymeter/pkg/view"
)

// weeksOf reads a window length such as 13w or 90d as whole weeks.
func weeksOf(s string) (int, error) {
	s = strings.TrimSpace(s)
	if n := len(s); n > 1 {
		if v, err := strconv.Atoi(s[:n-1]); err == nil && v > 0 {
			switch s[n-1] {
			case 'w':
				return v, nil
			case 'd':
				if v%7 == 0 {
					return v / 7, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("cannot read %q as a window. Use whole weeks, such as 13w or 26w", s)
}

func reportCmd() *cobra.Command {
	var dir, since, until, format, jsonPath, registryPath string
	var summary bool
	cmd := &cobra.Command{
		Use:   "report <owner/repo | owner>",
		Short: "Report how the line performs, station by station, from the local store",
		Long: `report reads the local store that "sync" fills and prints how the delivery
line performs: the constraint, then each station with its flow, wait, work,
rework and queue, split by agents and people, each with its weekly history.

It reads no network. Run "factorymeter sync" first, and again whenever the
report should be fresh.

The window is the last 13 weeks before the last sync. Every number is compared
with the 13 weeks before, and carries one point per week for up to 26 weeks.

--json writes the report as factorymeter/v1 JSON, the layout that
docs/contract.md describes. --summary leaves out every repository name, title
and link, so the file holds station numbers only and is safe to share.`,
		Example: `  factorymeter sync my-org && factorymeter report my-org
  factorymeter report owner/repo --since 26w
  factorymeter report my-org --format markdown >> "$GITHUB_STEP_SUMMARY"
  factorymeter report my-org --summary --json line.json --format none`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch format {
			case "text", "markdown", "json", "none":
			default:
				return fmt.Errorf("unknown format %q. Use text, markdown, json or none", format)
			}
			weeks, err := weeksOf(since)
			if err != nil {
				return err
			}
			reg, err := loadRegistry(registryPath)
			if err != nil {
				return err
			}
			target := strings.TrimSuffix(strings.TrimPrefix(args[0], "https://github.com/"), "/")
			path, _, err := storeDir(dir, target)
			if err != nil {
				return err
			}
			s, err := store.Open(path)
			if err != nil {
				return err
			}
			defer s.Close()
			l, err := measure.Load(s, reg, target)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			opt := measure.Options{Target: target, Weeks: weeks, Summary: summary, Now: now, Version: version, Registry: strconv.Itoa(reg.Version)}
			if until != "" {
				if opt.Until, err = parseWhen(until, now); err != nil {
					return err
				}
			}
			rep, err := l.Report(opt)
			if err != nil {
				return err
			}
			if jsonPath != "" {
				if err := writeFile(jsonPath, func(w io.Writer) error { return writeJSON(w, rep) }); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			switch format {
			case "json":
				return writeJSON(out, rep)
			case "markdown":
				return view.Markdown(out, rep)
			case "text":
				return view.Text(out, rep)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "store", "", "store directory (default: the owner's directory in the user cache)")
	f.StringVar(&since, "since", "13w", "length of the window in whole weeks, such as 13w or 26w")
	f.StringVar(&until, "until", "", "end of the window: a date, or days or weeks back (default: the last sync)")
	f.StringVar(&format, "format", "text", "what to print: text, markdown, json or none")
	f.StringVar(&jsonPath, "json", "", "also write the report as JSON to this file")
	f.BoolVar(&summary, "summary", false, "leave out every repository name, title and link")
	f.StringVar(&registryPath, "registry", "", "signature registry file to use instead of the built-in one")
	return cmd
}

func writeJSON(w io.Writer, rep *contract.Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
