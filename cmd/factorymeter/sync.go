package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/ingest"
	"github.com/marshal-org/factorymeter/pkg/linear"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

// storeDir picks the store of a target: the --store directory when given,
// else the owner's default one.
func storeDir(flag, target string) (dir, owner string, err error) {
	owner, _, _ = strings.Cut(target, "/")
	if flag != "" {
		return flag, owner, nil
	}
	dir, err = store.DefaultDir(owner)
	return dir, owner, err
}

func syncCmd() *cobra.Command {
	var dir, since string
	var opt ingest.Options
	var quiet, withLinear bool
	var linearTeams []string
	cmd := &cobra.Command{
		Use:   "sync <owner/repo | owner>",
		Short: "Read what changed on GitHub into the local store",
		Long: `sync reads pull requests, reviews, commits, issues, GitHub Actions runs,
deployments and releases into a store on this machine.

The first sync of a repository reads its history back to --since, 26 weeks
by default. Every later sync reads only what changed since the one before, so
it is quick enough to run every week or every hour. A sync that is stopped
keeps what it stored, and the next one goes on from there.

Give owner/repo for one repository, or an owner for all of its active
repositories. One store holds one owner. It lives in the user cache directory
unless --store or FACTORYMETER_STORE names another place.

With --linear, sync also reads the tickets of a Linear workspace into the
same store, and the pull requests Linear has attached to them. The key comes
from LINEAR_API_KEY: a personal API key that can read the workspace. The
workspace is the one the key belongs to. --linear-team limits the read to
some teams. A report then counts the Linear tickets at the spec station, next
to GitHub issues.

The store is a directory of Parquet files. Nothing leaves this machine.`,
		Example: `  factorymeter sync marshal-org/runlease
  factorymeter sync my-org --since 13w --workers 8
  factorymeter sync my-org --store ./factorymeter-store
  LINEAR_API_KEY=... factorymeter sync my-org --linear
  LINEAR_API_KEY=... factorymeter sync my-org --linear-team ENG --linear-team OPS`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := strings.Trim(strings.TrimSpace(args[0]), "/")
			now := time.Now().UTC()
			floor, err := parseWhen(since, now)
			if err != nil {
				return err
			}
			// A whole day, so a sync later the same day asks for the same floor.
			opt.Since, opt.Now = floor.Truncate(24*time.Hour), now
			path, owner, err := storeDir(dir, target)
			if err != nil {
				return err
			}
			c, err := newClient(quiet)
			if err != nil {
				return err
			}
			// A missing key is an error before the first request, not after
			// the whole of GitHub is read.
			var lc *linear.Client
			if withLinear || len(linearTeams) > 0 {
				if lc, err = linearClient(quiet); err != nil {
					return err
				}
			}
			s, err := store.OpenWrite(path, owner, now)
			if err != nil {
				return err
			}
			defer s.Close()
			if !quiet {
				opt.Logf = progress
				progress("store: %s", path)
			}
			g := &ingest.GitHub{C: c, S: s, Opt: opt}
			res, serr := g.Sync(cmd.Context(), target)
			w := cmd.OutOrStdout()
			printSync(w, res, c.Calls, time.Since(now))
			switch {
			case lc == nil:
				if len(s.Scopes(record.Linear)) > 0 {
					fmt.Fprintln(w, "note: the store holds Linear tickets, and this sync did not read Linear. Pass --linear to read what changed on them.")
				}
				return serr
			case cmd.Context().Err() != nil, serr != nil && !anyRead(res):
				// Stopped, or nothing of GitHub could be read at all.
				return serr
			}
			began := time.Now()
			l := &ingest.Linear{C: lc, S: s, Opt: opt, Teams: linearTeams}
			lres, lerr := l.Sync(cmd.Context())
			printLinear(w, lres, lerr, lc.Calls, time.Since(began))
			return errors.Join(serr, lerr)
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "store", "", "store directory (default: the owner's directory in the user cache)")
	f.StringVar(&since, "since", "26w", "how far back the first sync reads: 26w, 90d or a date")
	f.IntVar(&opt.Workers, "workers", 4, "repositories read at the same time")
	f.IntVar(&opt.MaxRuns, "max-runs", 100000, "workflow runs read per repository in one sync")
	f.BoolVar(&opt.SkipCI, "skip-ci", false, "do not read GitHub Actions runs")
	f.BoolVar(&opt.SkipDeploys, "skip-deploys", false, "do not read deployments and releases")
	f.BoolVar(&opt.SkipTickets, "skip-tickets", false, "do not read issues")
	f.BoolVar(&opt.IncludeForks, "include-forks", false, "with an owner: include forks")
	f.BoolVar(&opt.IncludeArchived, "include-archived", false, "with an owner: include archived repositories")
	f.BoolVar(&withLinear, "linear", false, "also read the tickets of the Linear workspace that LINEAR_API_KEY belongs to")
	f.StringArrayVar(&linearTeams, "linear-team", nil, "read only this Linear team, by its key, such as ENG. Repeat for more. Implies --linear")
	f.BoolVar(&quiet, "quiet", false, "no progress on standard error")
	return cmd
}

func delta(c ingest.Count) string {
	switch {
	case c.Added == 0 && c.Changed == 0:
		return fmt.Sprintf("%d", c.Total)
	case c.Changed == 0:
		return fmt.Sprintf("%d (+%d)", c.Total, c.Added)
	default:
		return fmt.Sprintf("%d (+%d, %d updated)", c.Total, c.Added, c.Changed)
	}
}

// stopped tells a repository the sync was interrupted on from one that failed.
func stopped(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func printSync(w io.Writer, res ingest.Result, calls int, took time.Duration) {
	if len(res.Repos) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "REPOSITORY\tCHANGES\tCI RUNS\tDEPLOYS\tTICKETS\tFROM\t")
	for _, r := range res.Repos {
		from := "-"
		if !r.From.IsZero() {
			from = r.From.Format("2006-01-02")
		}
		state := ""
		switch {
		case stopped(r.Err):
			state = "stopped"
		case r.Err != nil:
			state = "failed"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Repo, delta(r.Changes), delta(r.CIRuns), delta(r.Deploys), delta(r.Tickets), from, state)
	}
	tw.Flush()
	for _, r := range res.Repos {
		for _, n := range r.Notes {
			fmt.Fprintf(w, "note: %s: %s\n", r.Repo, n)
		}
		if r.Err != nil && !stopped(r.Err) {
			fmt.Fprintf(w, "failed: %s: %v\n", r.Repo, r.Err)
		}
	}
	fmt.Fprintf(w, "%d requests to GitHub in %s.\n", calls, took.Round(time.Second))
}

// printLinear prints what a sync of a Linear workspace did. It comes after the
// anyRead says whether the sync read at least one repository to the end.
// When none was read there is no pull request to tie a ticket to.
func anyRead(res ingest.Result) bool {
	for _, r := range res.Repos {
		if r.Err == nil {
			return true
		}
	}
	return false
}

// lines of GitHub, which other tools read.
func printLinear(w io.Writer, r ingest.SourceResult, err error, calls int, took time.Duration) {
	name := "Linear"
	if r.Name != "" {
		name = "Linear " + r.Name
		from := "-"
		if !r.From.IsZero() {
			from = r.From.Format("2006-01-02")
		}
		state := ""
		switch {
		case stopped(err):
			state = ", stopped"
		case err != nil:
			state = ", failed"
		}
		fmt.Fprintf(w, "%s: tickets %s, links to pull requests %s, from %s%s\n", name, delta(r.Tickets), delta(r.Links), from, state)
	}
	if r.Again {
		fmt.Fprintf(w, "note: %s: the list of teams changed, so the tickets were read again from the start.\n", name)
	}
	if err != nil && !stopped(err) {
		fmt.Fprintf(w, "failed: %s: %v\n", name, err)
	}
	fmt.Fprintf(w, "%d requests to Linear in %s.\n", calls, took.Round(time.Second))
}

// storeInfo is what "store info --format json" prints.
type storeInfo struct {
	Dir       string      `json:"dir"`
	Owner     string      `json:"owner"`
	CreatedAt time.Time   `json:"created_at"`
	Bytes     int64       `json:"bytes"`
	Scopes    []scopeInfo `json:"scopes"`
}

type scopeInfo struct {
	Source   string           `json:"source"`
	Name     string           `json:"name"`
	Private  bool             `json:"private"`
	From     time.Time        `json:"from"`
	SyncedAt time.Time        `json:"synced_at"`
	Records  map[string]int64 `json:"records"`
	Notes    []string         `json:"notes"`
}

// label is how a scope is written in a table: a repository by its name, a
// scope of another source with the source after it.
func (si scopeInfo) label() string {
	if si.Source == record.GitHub {
		return si.Name
	}
	return si.Name + " (" + record.SourceName(si.Source) + ")"
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
	}
}

func storeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Look at the local store",
	}
	var dir, format string
	info := &cobra.Command{
		Use:   "info <owner>",
		Short: "Print what the store holds",
		Long: `info prints the repositories in an owner's store, how many records each
has, how far back they go and when each was last synced. A Linear workspace
is listed after them, with its name followed by "(Linear)". It reads the
store only. It does not need a token and makes no request.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("unknown format %q. Use text or json", format)
			}
			path, _, err := storeDir(dir, args[0])
			if err != nil {
				return err
			}
			s, err := store.Open(path)
			if errors.Is(err, store.ErrNoStore) {
				return fmt.Errorf("%w. Run \"factorymeter sync %s\" first", err, args[0])
			}
			if err != nil {
				return err
			}
			out := storeInfo{Dir: path, Owner: s.Owner(), CreatedAt: s.CreatedAt(), Scopes: []scopeInfo{}}
			if out.Bytes, err = s.Size(); err != nil {
				return err
			}
			for _, sc := range s.Scopes("") {
				si := scopeInfo{Source: sc.Source, Name: sc.Name, Private: sc.Private, From: sc.From, SyncedAt: sc.SyncedAt, Notes: sc.Notes, Records: map[string]int64{}}
				for _, t := range store.TableNames {
					n, err := s.Count(t, sc.Source, sc.Name)
					if err != nil {
						return err
					}
					si.Records[t] = n
				}
				out.Scopes = append(out.Scopes, si)
			}
			w := cmd.OutOrStdout()
			if format == "json" {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			// A scope of GitHub is a repository. Any other scope holds the
			// tickets of another system.
			repos, others := 0, ""
			for _, si := range out.Scopes {
				if si.Source == record.GitHub {
					repos++
				} else if name := record.SourceName(si.Source) + " tickets"; !strings.Contains(others, name) {
					others += ", " + name
				}
			}
			fmt.Fprintf(w, "%s: %d repositories%s, %s, in %s\n", out.Owner, repos, others, size(out.Bytes), out.Dir)
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "REPOSITORY\tCHANGES\tCI RUNS\tDEPLOYS\tTICKETS\tFROM\tSYNCED\t")
			for _, si := range out.Scopes {
				from, synced := "-", "not finished"
				if !si.From.IsZero() {
					from = si.From.Format("2006-01-02")
				}
				if !si.SyncedAt.IsZero() {
					synced = si.SyncedAt.Format("2006-01-02 15:04") + " UTC"
				}
				if si.Source != record.GitHub {
					fmt.Fprintf(tw, "%s\t-\t-\t-\t%d\t%s\t%s\t\n", si.label(), si.Records["tickets"], from, synced)
					continue
				}
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\t%s\t\n", si.Name, si.Records["changes"], si.Records["ci_runs"], si.Records["deploys"], si.Records["tickets"], from, synced)
			}
			tw.Flush()
			for _, si := range out.Scopes {
				for _, n := range si.Notes {
					fmt.Fprintf(w, "note: %s: %s\n", si.label(), n)
				}
			}
			return nil
		},
	}
	info.Flags().StringVar(&dir, "store", "", "store directory (default: the owner's directory in the user cache)")
	info.Flags().StringVar(&format, "format", "text", "text or json")
	path := &cobra.Command{
		Use:   "path <owner>",
		Short: "Print the directory of an owner's store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, err := storeDir("", args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), p)
			return nil
		},
	}
	cmd.AddCommand(info, path)
	return cmd
}
