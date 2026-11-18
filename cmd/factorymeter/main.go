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
logged in. Nothing is sent anywhere except to the GitHub API.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.AddCommand(reportCmd(), trendCmd(), classifyCmd(), signaturesCmd())
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

func progress(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "  "+format+"\n", a...)
}
