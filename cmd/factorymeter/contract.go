package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

// pick narrows the registry to a station, the whole line or one metric.
func pick(arg string) ([]line.Metric, error) {
	if arg == "" {
		return line.Registry, nil
	}
	if m, ok := line.ByID(arg); ok {
		return []line.Metric{m}, nil
	}
	if list := line.ForStation(arg); len(list) > 0 {
		return list, nil
	}
	ids := []string{}
	for _, s := range line.Stations {
		ids = append(ids, s.ID)
	}
	return nil, fmt.Errorf("%q is not a station or a metric id. Stations: %s, and %s for the whole line", arg, strings.Join(ids, ", "), line.Whole)
}

func better(g line.Good) string {
	switch g {
	case line.Up:
		return "higher is better"
	case line.Down:
		return "lower is better"
	}
	return "neither way is better"
}

func metricsText(w io.Writer, list []line.Metric, define bool) {
	width := 0
	for _, m := range list {
		width = max(width, len(m.ID))
	}
	station := ""
	for _, m := range list {
		if m.Station != station && len(list) > 1 {
			if station != "" {
				fmt.Fprintln(w)
			}
			station = m.Station
			if s, ok := line.StationByID(station); ok {
				fmt.Fprintf(w, "%s: %s\n", s.Name, s.Does)
			} else {
				fmt.Fprintln(w, "The whole line")
			}
		}
		if !define {
			fmt.Fprintf(w, "  %-*s  %-8s  %s\n", width, m.ID, m.Unit, m.Name)
			continue
		}
		fmt.Fprintf(w, "  %s\n    %s. Unit %s, %s.\n    %s\n", m.ID, m.Name, m.Unit, better(m.Good), m.Definition)
		if m.MinSample > 0 {
			fmt.Fprintf(w, "    Marked thin when drawn from fewer than %d items.\n", m.MinSample)
		}
		fmt.Fprintf(w, "    Cut by: %s. Needs: %s.\n", strings.Join(m.Slices, ", "), strings.Join(m.Needs, ", "))
	}
}

func metricsCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "metrics [station | metric-id]",
		Short: "List every number factorymeter reports, with its exact definition",
		Long: `metrics prints the metric registry: the id, unit and definition of every number
in a report. It reads nothing and needs no token.

Give a station (spec, code, ci, verify, review, merge, release, or line for the
whole line) to see its metrics with their definitions, or a metric id such as
review.wait.p50 to see one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			list, err := pick(arg)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch format {
			case "text":
				metricsText(out, list, arg != "")
			case "json":
				var v any = list
				if arg == "" {
					v = contract.NewCatalog()
				}
				b, err := contract.JSON(v)
				if err != nil {
					return err
				}
				_, err = out.Write(b)
				return err
			case "markdown":
				if arg != "" {
					return fmt.Errorf("--format markdown prints the whole reference. Leave out %q", arg)
				}
				fmt.Fprint(out, contract.MetricsMarkdown())
			default:
				return fmt.Errorf("unknown format %q. Use text, markdown or json", format)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text, markdown or json")
	return cmd
}

// pickRules narrows the rules to a station or to one rule.
func pickRules(arg string) ([]line.Rule, error) {
	if arg == "" {
		return line.Rules, nil
	}
	if r, ok := line.RuleByID(arg); ok {
		return []line.Rule{r}, nil
	}
	var list []line.Rule
	for _, r := range line.Rules {
		if r.Station == arg {
			list = append(list, r)
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%q is not a rule id, and no rule belongs to a station with that id. Run \"factorymeter rules\" to see them all", arg)
	}
	return list, nil
}

func rulesText(w io.Writer, list []line.Rule) {
	for i, r := range list {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s\n  %s\n  Fires when: %s\n  Reads: %s.\n", r.ID, r.Title, r.Fires, strings.Join(r.Reads, ", "))
		if r.Sized != "" {
			fmt.Fprintf(w, "  Hours freed: %s\n", r.Sized)
		}
		if url := r.PlayURL(); url != "" {
			fmt.Fprintf(w, "  Practice: %s\n", url)
		}
	}
}

func rulesCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "rules [station | rule-id]",
		Short: "List every recommendation factorymeter can make, and when it makes it",
		Long: `rules prints the rules behind "where to invest" in a report: when each one
fires, the numbers it reads and how it sizes its gain. It reads nothing and
needs no token.

Give a station (code, ci, verify, review, merge, release) to see its rules, or
a rule id such as review.slow to see one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			list, err := pickRules(arg)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch format {
			case "text":
				rulesText(out, list)
			case "json":
				b, err := contract.JSON(list)
				if err != nil {
					return err
				}
				_, err = out.Write(b)
				return err
			case "markdown":
				if arg != "" {
					return fmt.Errorf("--format markdown prints the whole reference. Leave out %q", arg)
				}
				fmt.Fprint(out, contract.RulesMarkdown())
			default:
				return fmt.Errorf("unknown format %q. Use text, markdown or json", format)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text, markdown or json")
	return cmd
}

func contractCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Print the JSON Schema of the report, or write the generated reference docs",
		Long: `The contract is what a program that reads factorymeter can rely on: the records
kept in the store, the metric ids and their definitions, and the layout of the
report. It is defined in code, and the reference files are generated from it.`,
	}
	schema := &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema of the report (" + contract.Version + ")",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := contract.JSON(contract.ReportSchema())
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(b)
			return err
		},
	}
	write := &cobra.Command{
		Use:   "write <dir>",
		Short: "Write the generated reference files into a directory",
		Long: `write generates the reference files from the definitions in code:
metrics.md, metrics.json, records.md, report.md and the JSON Schema of the
report. In this repository the directory is ` + contract.Dir + `, and a test fails when
the files there differ from what the code generates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := contract.Files()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(args[0], 0o755); err != nil {
				return err
			}
			names := make([]string, 0, len(files))
			for name := range files {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				path := filepath.Join(args[0], name)
				if err := os.WriteFile(path, files[name], 0o644); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)
			}
			return nil
		},
	}
	cmd.AddCommand(schema, write)
	return cmd
}
