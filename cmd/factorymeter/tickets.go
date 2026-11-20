package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

func storeTicketsCmd() *cobra.Command {
	var dir, format string
	cmd := &cobra.Command{
		Use:   "tickets [owner/repo | owner]",
		Short: "List the tickets in the store and the pull requests tied to each",
		Long: `tickets lists every ticket in the store: GitHub issues, and the tickets of
Linear and Jira. For each it shows the state, when the spec station takes it to
start, how often it was opened again, and the pull requests tied to it, with
how each one was tied:

  attached  the ticket system attached the pull request, as Linear does
  closes    GitHub says the pull request closes the issue
  key       the branch, title or description of the pull request names the key

For owner/repo only the pull requests of that repository are looked at, and
only the tickets of Linear and Jira that one of them is tied to are listed,
the same as in its report. It reads the store only and makes no request.`,
		Example: `  factorymeter store tickets my-org
  factorymeter store tickets my-org/app --format json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("unknown format %q. Use text or json", format)
			}
			target, err := targetOf(args)
			if err != nil {
				return err
			}
			path, _, err := storeDir(dir, target)
			if err != nil {
				return err
			}
			s, err := store.Open(path)
			if err != nil {
				return err
			}
			defer s.Close()
			rows, err := measure.Tickets(s, target)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if format == "json" {
				if rows == nil {
					rows = []measure.TicketRow{}
				}
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(w, "No tickets in the store.")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SOURCE\tTICKET\tSTATE\tSTART\tREOPENED\tPULL REQUESTS")
			one := strings.Contains(target, "/")
			for _, r := range rows {
				start := "-"
				if r.Start != nil {
					start = r.Start.UTC().Format("2006-01-02 15:04")
				}
				var pulls []string
				for _, p := range r.Pulls {
					name := fmt.Sprintf("%s#%d", p.Repo, p.Number)
					if one {
						name = fmt.Sprintf("#%d", p.Number)
					}
					pulls = append(pulls, name+" ("+strings.Join(p.By, ", ")+")")
				}
				key := r.Key
				if len(r.Aliases) > 0 {
					key += " (was " + strings.Join(r.Aliases, ", ") + ")"
				}
				reopened := "-"
				if r.Reopened > 0 {
					reopened = fmt.Sprint(r.Reopened)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", record.SourceName(r.Source), key, r.State, start, reopened, strings.Join(pulls, ", "))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(w, "\nTimes are UTC. START is when the spec station takes a ticket to start: its first move to a started state, or its first pull request, whichever is earlier.")
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "store", "", "store directory (default: the owner's directory in the user cache)")
	cmd.Flags().StringVar(&format, "format", "text", "text or json")
	return cmd
}
