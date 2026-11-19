package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/ingest"
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
	var quiet bool
	cmd := &cobra.Command{
		Use:   "sync <owner/repo | owner>",
		Short: "Read what changed on GitHub into the local store",
		Long: `sync reads pull requests, reviews, commits, GitHub Actions runs, deployments
and releases into a store on this machine.

The first sync of a repository reads its history back to --since, 26 weeks
by default. Every later sync reads only what changed since the one before, so
it is quick enough to run every week or every hour. A sync that is stopped
keeps what it stored, and the next one goes on from there.

Give owner/repo for one repository, or an owner for all of its active
repositories. One store holds one owner. It lives in the user cache directory
unless --store or FACTORYMETER_STORE names another place.

The store is a directory of Parquet files. Nothing leaves this machine.`,
		Example: `  factorymeter sync marshal-org/runlease
  factorymeter sync my-org --since 13w --workers 8
  factorymeter sync my-org --store ./factorymeter-store`,
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
			printSync(cmd.OutOrStdout(), res, c.Calls, time.Since(now))
			return serr
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "store", "", "store directory (default: the owner's directory in the user cache)")
	f.StringVar(&since, "since", "26w", "how far back the first sync reads: 26w, 90d or a date")
	f.IntVar(&opt.Workers, "workers", 4, "repositories read at the same time")
	f.IntVar(&opt.MaxRuns, "max-runs", 100000, "workflow runs read per repository in one sync")
	f.BoolVar(&opt.SkipCI, "skip-ci", false, "do not read GitHub Actions runs")
	f.BoolVar(&opt.SkipDeploys, "skip-deploys", false, "do not read deployments and releases")
	f.BoolVar(&opt.IncludeForks, "include-forks", false, "with an owner: include forks")
	f.BoolVar(&opt.IncludeArchived, "include-archived", false, "with an owner: include archived repositories")
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

func printSync(w io.Writer, res ingest.Result, calls int, took time.Duration) {
	if len(res.Repos) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "REPOSITORY\tCHANGES\tCI RUNS\tDEPLOYS\tFROM\t")
	for _, r := range res.Repos {
		from := "-"
		if !r.From.IsZero() {
			from = r.From.Format("2006-01-02")
		}
		state := ""
		if r.Err != nil {
			state = "failed"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Repo, delta(r.Changes), delta(r.CIRuns), delta(r.Deploys), from, state)
	}
	tw.Flush()
	for _, r := range res.Repos {
		for _, n := range r.Notes {
			fmt.Fprintf(w, "note: %s: %s\n", r.Repo, n)
		}
		if r.Err != nil {
			fmt.Fprintf(w, "failed: %s: %v\n", r.Repo, r.Err)
		}
	}
	fmt.Fprintf(w, "%d requests to GitHub in %s.\n", calls, took.Round(time.Second))
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
has, how far back they go and when each was last synced. It reads the store
only. It does not need a token and makes no request.`,
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
			fmt.Fprintf(w, "%s: %d repositories, %s, in %s\n", out.Owner, len(out.Scopes), size(out.Bytes), out.Dir)
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "REPOSITORY\tCHANGES\tCI RUNS\tDEPLOYS\tFROM\tSYNCED\t")
			for _, si := range out.Scopes {
				from, synced := "-", "not finished"
				if !si.From.IsZero() {
					from = si.From.Format("2006-01-02")
				}
				if !si.SyncedAt.IsZero() {
					synced = si.SyncedAt.Format("2006-01-02 15:04") + " UTC"
				}
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%s\t\n", si.Name, si.Records["changes"], si.Records["ci_runs"], si.Records["deploys"], from, synced)
			}
			tw.Flush()
			for _, si := range out.Scopes {
				for _, n := range si.Notes {
					fmt.Fprintf(w, "note: %s: %s\n", si.Name, n)
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
