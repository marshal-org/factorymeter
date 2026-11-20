// Command factorymeter reads a GitHub repository's or owner's history with a
// read-only token and reports how the delivery line performs, split by
// agent-written and human-written pull requests.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/jira"
	"github.com/marshal-org/factorymeter/pkg/linear"
)

// version is set at build time with -ldflags "-X main.version=...". A build
// by "go install" of a release has no such flag and takes the version of the
// module it was built from.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		version = released(bi.Main.Version, version)
	}
}

// released is the version of the module when that is a release, such as
// v0.1.0, else the fallback. A build from a checkout is not a release.
func released(module, fallback string) string {
	if strings.HasPrefix(module, "v") && !strings.ContainsAny(module, "-+") {
		return module
	}
	return fallback
}

func main() {
	// A closed terminal stops a sync the same way Ctrl-C does: what was read
	// is stored and the lock on the store is let go.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := newRoot().ExecuteContext(ctx); err != nil {
		if !errors.As(err, new(toldErr)) {
			fmt.Fprintln(os.Stderr, "factorymeter:", err)
		}
		os.Exit(1)
	}
}

// newRoot is the factorymeter command with every command under it.
func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "factorymeter",
		Short: "Measure your software delivery line, split by agent and human pull requests",
		Long: `factorymeter reads a GitHub repository's or owner's history and shows its
delivery line: seven stations from spec to release, how much leaves each a
week, how long changes wait, and how often they go back. It names the station
that holds the line, the one next in line, and where to invest, with agents
next to people.

  factorymeter init             a few questions, a factorymeter.yml, the first sync
  factorymeter sync my-org      read the history into a store on this machine
  factorymeter serve my-org     see the line as a page in the browser
  factorymeter report my-org    print it, or write it as Markdown, JSON or HTML

It only reads. It needs a token that can read the repository, taken from
GITHUB_TOKEN or GH_TOKEN, or from "gh auth token" when the GitHub CLI is
logged in. "sync --linear" and "sync --jira" also read tickets, with keys from
the environment. Nothing is sent anywhere except to the API it reads.
"factorymeter upgrade" replaces this binary with the newest release.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	var configPath string
	var noConfig bool
	root.PersistentFlags().StringVar(&configPath, "config", "", "read this config file instead of the factorymeter.yml in this directory")
	root.PersistentFlags().BoolVar(&noConfig, "no-config", false, "read no config file")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Only the commands that take a setting from it read the file. So a
		// broken file never stops "metrics" or "rules", and "config init" can
		// write over it.
		if !readsConfig[strings.TrimPrefix(cmd.CommandPath(), "factorymeter ")] {
			return nil
		}
		return loadConfig(cmd, configPath, noConfig)
	}
	root.AddCommand(reportCmd(), serveCmd(), syncCmd(), snapshotCmd(), trendCmd(), storeCmd(), classifyCmd(), signaturesCmd(), metricsCmd(), rulesCmd(), contractCmd(), configCmd(), upgradeCmd(), initCmd())
	return root
}

// toldErr is an error the screen has already told the person. The run still
// ends as a failure. The error is not printed a second time.
type toldErr struct{ error }

func (s toldErr) Unwrap() error { return s.error }

// token finds a GitHub token without ever printing it.
func token() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v, nil
		}
	}
	return "", errors.New("no GitHub token. Set GITHUB_TOKEN to a read-only token, or log in with the GitHub CLI")
}

func newClient(quiet bool) (*github.Client, error) {
	t, err := token()
	if err != nil {
		return nil, err
	}
	c := github.New(t)
	c.UserAgent = "factorymeter/" + version
	if v := strings.TrimRight(os.Getenv("GITHUB_API_URL"), "/"); v != "" {
		c.BaseURL = v
	}
	if v := os.Getenv("GITHUB_GRAPHQL_URL"); v != "" {
		c.GraphQLURL = v
	}
	if !quiet {
		c.Logf = progress
	}
	return c, nil
}

// linearClient builds a client for Linear. The key comes from the environment
// only, never from a flag, so it does not end up in the shell history or in
// the list of processes. It is never printed.
func linearClient(quiet bool) (*linear.Client, error) {
	key := strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
	if key == "" {
		return nil, errors.New("no Linear key. Set LINEAR_API_KEY to a personal API key that can read the workspace")
	}
	c := linear.New(key)
	c.UserAgent = "factorymeter/" + version
	if v := strings.TrimSpace(os.Getenv("LINEAR_API_URL")); v != "" {
		c.URL = v
	}
	if !quiet {
		c.Logf = progress
	}
	return c, nil
}

// jiraClient builds a client for a Jira Cloud site. The site is not a secret
// and comes from a flag. The email and the token come from the environment
// only, never from a flag, so they do not end up in the shell history or in
// the list of processes. Neither is ever printed.
func jiraClient(site string, quiet bool) (*jira.Client, error) {
	email, tok := strings.TrimSpace(os.Getenv("JIRA_EMAIL")), strings.TrimSpace(os.Getenv("JIRA_API_TOKEN"))
	switch {
	case email == "" && tok == "":
		return nil, errors.New("no Jira account. Set JIRA_EMAIL to the email of an account that can read the site, and JIRA_API_TOKEN to an API token of that account")
	case email == "":
		return nil, errors.New("no Jira email. Set JIRA_EMAIL to the email of the account the API token belongs to")
	case tok == "":
		return nil, errors.New("no Jira token. Set JIRA_API_TOKEN to an API token of the account that can read the site")
	}
	c, err := jira.New(site, email, tok)
	if err != nil {
		return nil, err
	}
	c.UserAgent = "factorymeter/" + version
	if !quiet {
		c.Logf = progress
	}
	return c, nil
}

func progress(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "  "+format+"\n", a...)
}
