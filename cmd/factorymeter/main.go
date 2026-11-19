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
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/jira"
	"github.com/marshal-org/factorymeter/pkg/linear"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := &cobra.Command{
		Use:   "factorymeter",
		Short: "Measure your software delivery line, split by agent and human pull requests",
		Long: `factorymeter reads a GitHub repository's or owner's history and reports how the
delivery line performs: throughput, wait for first review, CI time, rework and
merge rate, each split by agent-written and human-written pull requests. It
names the stage where work waits the longest.

It only reads. It needs a token that can read the repository, taken from
GITHUB_TOKEN or GH_TOKEN, or from "gh auth token" when the GitHub CLI is
logged in. "sync --linear" also reads the tickets of a Linear workspace, with
the key in LINEAR_API_KEY. Nothing is sent anywhere except to the GitHub API
and, when you ask for it, the Linear API.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.AddCommand(reportCmd(), serveCmd(), syncCmd(), snapshotCmd(), trendCmd(), storeCmd(), classifyCmd(), signaturesCmd(), metricsCmd(), rulesCmd(), contractCmd())
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "factorymeter:", err)
		os.Exit(1)
	}
}

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
