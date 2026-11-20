package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/jira"
)

// asker asks questions on a line each and reads the answers. At the end of
// the input every question takes its default, so the wizard also runs with
// no one at the keyboard.
type asker struct {
	in  *bufio.Reader
	out io.Writer
	eof bool
}

func (a *asker) say(format string, args ...any) { fmt.Fprintf(a.out, format+"\n", args...) }

// ask asks a question with a default, and asks again while check refuses the
// answer.
func (a *asker) ask(q, def string, check func(string) error) string {
	for {
		if def != "" {
			fmt.Fprintf(a.out, "  %s [%s] ", q, def)
		} else {
			fmt.Fprintf(a.out, "  %s ", q)
		}
		line := ""
		if !a.eof {
			l, err := a.in.ReadString('\n')
			if err != nil {
				a.eof = true
			}
			line = strings.TrimSpace(l)
		}
		if a.eof {
			fmt.Fprintln(a.out)
		}
		if line == "" {
			line = def
		}
		if check == nil {
			return line
		}
		err := check(line)
		if err == nil {
			return line
		}
		a.say("    %v", err)
		if a.eof {
			return ""
		}
	}
}

func (a *asker) yes(q string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	ans := strings.ToLower(a.ask(q, d, func(s string) error {
		switch strings.ToLower(s) {
		case "y", "yes", "n", "no", "y/n", "n/y":
			return nil
		}
		return errors.New("answer y or n")
	}))
	switch ans {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}

var remoteRe = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+?)(\.git)?/?$`)

// gitTarget is the owner/repo of the GitHub remote of the directory, if it
// has one.
func gitTarget(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// wizardAnswers are what the wizard learned, ready to be written.
type wizardAnswers struct {
	target, history string
	linear          bool
	teams           []string
	jiraSite        string
	jiraProjects    []string
}

func (w wizardAnswers) yaml() string {
	var b strings.Builder
	b.WriteString(`# Written by "factorymeter init". A flag on the command line wins over it.
# Keys and tokens are never kept here: they come from GITHUB_TOKEN,
# LINEAR_API_KEY, JIRA_EMAIL and JIRA_API_TOKEN. "factorymeter config init"
# shows every setting there is.

# The repository (owner/repo) or the owner whose active repositories to read.
target: ` + w.target + `

# How far back the first sync reads, and the window a report covers.
history: ` + w.history + `
window: 13w
`)
	list := func(v []string) string { return "[" + strings.Join(v, ", ") + "]" }
	if w.linear {
		b.WriteString("\n# The Linear workspace of LINEAR_API_KEY.\n")
		if len(w.teams) > 0 {
			b.WriteString("linear:\n  teams: " + list(w.teams) + "\n")
		} else {
			b.WriteString("linear: true   # every team the key can see\n")
		}
	}
	if w.jiraSite != "" {
		b.WriteString("\n# Jira Cloud, as the account of JIRA_EMAIL and JIRA_API_TOKEN.\njira:\n  site: " + w.jiraSite + "\n")
		if len(w.jiraProjects) > 0 {
			b.WriteString("  projects: " + list(w.jiraProjects) + "\n")
		}
	}
	return b.String()
}

// pickKeys reads keys separated by spaces or commas, and checks them against
// the keys there are, when they are known.
func pickKeys(answer string, known map[string]string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, k := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		k = strings.ToUpper(k)
		if !keyRe.MatchString(k) {
			return nil, fmt.Errorf("%q is not a key, such as ENG", k)
		}
		if known != nil {
			if _, ok := known[k]; !ok {
				return nil, fmt.Errorf("there is no %s here. Pick from the list, or press Enter for all", k)
			}
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

var keyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,31}$`)

// linearTeams lists the teams the key can see, by key and name.
func linearTeams(ctx context.Context) ([][2]string, error) {
	c, err := linearClient(true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Teams struct {
			Nodes []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := c.Query(ctx, `query { teams(first: 100) { nodes { key name } } }`, nil, &out); err != nil {
		return nil, err
	}
	var teams [][2]string
	for _, t := range out.Teams.Nodes {
		teams = append(teams, [2]string{strings.ToUpper(t.Key), t.Name})
	}
	return teams, nil
}

// jiraProjectList lists the projects the account can browse, by key and name.
func jiraProjectList(ctx context.Context, site string) ([][2]string, error) {
	c, err := jiraClient(site, true)
	if err != nil {
		return nil, err
	}
	var me struct {
		AccountID string `json:"accountId"`
	}
	if err := c.Get(ctx, "/rest/api/3/myself", nil, &me); err != nil {
		return nil, err
	}
	var projects [][2]string
	for start := 0; start < 1000; start += 50 {
		var out struct {
			Values []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"values"`
			IsLast bool `json:"isLast"`
		}
		q := url.Values{"maxResults": {"50"}, "startAt": {fmt.Sprint(start)}}
		if err := c.Get(ctx, "/rest/api/3/project/search", q, &out); err != nil {
			return nil, err
		}
		for _, p := range out.Values {
			projects = append(projects, [2]string{strings.ToUpper(p.Key), p.Name})
		}
		if out.IsLast || len(out.Values) == 0 {
			break
		}
	}
	return projects, nil
}

func initCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Answer a few questions and get a factorymeter.yml, then the first sync",
		Long: `init asks what to read, whether your tickets live in Linear or Jira, and how
far back to go. It writes the answers to factorymeter.yml in this directory,
and offers to run the first sync. From then on "factorymeter sync",
"factorymeter report" and "factorymeter serve" need no arguments.

When LINEAR_API_KEY is set it lists the teams of your Linear workspace, and
when JIRA_EMAIL and JIRA_API_TOKEN are set it lists the projects of your Jira
site, so you pick from what is there. Keys are never written to the file.

Press Enter to take the answer in brackets. With no one at the keyboard, every
question takes its answer in brackets.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			a := &asker{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout()}
			const path = "factorymeter.yml"
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if found, err := findConfig("", dir); err != nil {
				return err
			} else if found != "" && !force {
				a.say("")
				a.say("  There is a config file here already: %s.", relTo(dir, found))
				if !a.yes("Write a new factorymeter.yml in its place?", false) {
					a.say("  Nothing was written. \"factorymeter config\" shows what it sets.")
					return nil
				}
				if relTo(dir, found) != path {
					// Keep one file in the directory.
					if err := os.Remove(found); err != nil {
						return err
					}
				}
			}

			a.say("")
			a.say("  factorymeter init")
			a.say("")
			a.say("  A few questions, then factorymeter.yml is written here. Press Enter to")
			a.say("  take the answer in [brackets].")
			a.say("")

			// What to read.
			def := gitTarget(ctx)
			a.say("  1. What should it read? owner/repo for one repository, or an owner for")
			a.say("     every active repository of it.")
			if def != "" {
				owner, _, _ := strings.Cut(def, "/")
				a.say("     This directory is %s.", def)
				a.say("     For every repository of %s, answer %s.", owner, owner)
			}
			ans := wizardAnswers{}
			ans.target = a.ask(">", def, func(s string) error {
				if s == "" {
					return errors.New("give owner/repo, or an owner")
				}
				_, err := parseTarget(s)
				return err
			})
			if ans.target == "" {
				return errors.New("no target was given")
			}
			ans.target, _ = parseTarget(ans.target)
			switch {
			case os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GH_TOKEN") != "":
				a.say("     A GitHub token is set. Good.")
			case ghLoggedIn(ctx):
				a.say("     The GitHub CLI is logged in. Its token will be used. Good.")
			default:
				a.say("     No GitHub token yet. Before sync, set GITHUB_TOKEN to a token that can")
				a.say("     read %s, or log in with \"gh auth login\". A public repository", ans.target)
				a.say("     needs a token with no permissions at all.")
			}

			a.say("")
			a.say("  2. How far back should the first sync read? A report compares 13 weeks")
			a.say("     with the 13 before, so 26 weeks fills both.")
			ans.history = a.ask(">", "26w", func(s string) error {
				_, err := parseWhen(s, time.Now())
				return err
			})

			a.say("")
			a.say("  3. Do your tickets live in Linear?")
			if ans.linear = a.yes(">", false); ans.linear {
				var known map[string]string
				if strings.TrimSpace(os.Getenv("LINEAR_API_KEY")) == "" {
					a.say("     Set LINEAR_API_KEY to a personal API key with the Read permission")
					a.say("     before sync. Linear, Settings, Security and access, Personal API keys.")
				} else if teams, err := linearTeams(ctx); err != nil {
					a.say("     Could not list the teams: %v", err)
				} else {
					known = map[string]string{}
					a.say("     The teams of your workspace:")
					for _, t := range teams {
						known[t[0]] = t[1]
						a.say("       %-8s %s", t[0], t[1])
					}
				}
				var picked []string
				a.ask("Which teams? Keys separated by spaces, or Enter for all:", "all", func(s string) error {
					if strings.EqualFold(s, "all") {
						picked = nil
						return nil
					}
					var err error
					picked, err = pickKeys(s, known)
					return err
				})
				ans.teams = picked
			}

			a.say("")
			a.say("  4. Do your tickets live in Jira Cloud?")
			if a.yes(">", false) {
				ans.jiraSite = a.ask("The host of the site, such as acme.atlassian.net:", "", func(s string) error {
					if s == "" {
						return errors.New("give the host of the site, such as acme.atlassian.net")
					}
					_, err := jiraSiteOf(s)
					return err
				})
				if ans.jiraSite != "" {
					ans.jiraSite, _ = jiraSiteOf(ans.jiraSite)
					var known map[string]string
					if strings.TrimSpace(os.Getenv("JIRA_EMAIL")) == "" || strings.TrimSpace(os.Getenv("JIRA_API_TOKEN")) == "" {
						a.say("     Set JIRA_EMAIL and JIRA_API_TOKEN before sync: the email of your")
						a.say("     account and an API token without scopes, from id.atlassian.com,")
						a.say("     Security, API tokens, Create API token.")
					} else if projects, err := jiraProjectList(ctx, ans.jiraSite); err != nil {
						a.say("     Could not list the projects: %v", err)
					} else {
						known = map[string]string{}
						a.say("     The projects your account can browse:")
						for _, p := range projects {
							known[p[0]] = p[1]
							a.say("       %-8s %s", p[0], p[1])
						}
					}
					var picked []string
					a.ask("Which projects? Keys separated by spaces, or Enter for all:", "all", func(s string) error {
						if strings.EqualFold(s, "all") {
							picked = nil
							return nil
						}
						var err error
						picked, err = pickKeys(s, known)
						return err
					})
					ans.jiraProjects = picked
				}
			}

			text := ans.yaml()
			// The file is read back as sync will read it, before it is written.
			if _, err := parseConfig([]byte(text)); err != nil {
				return fmt.Errorf("the answers do not make a config file: %w", err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return err
			}
			a.say("")
			a.say("  Wrote %s:", path)
			a.say("")
			for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
				if !strings.HasPrefix(l, "#") && l != "" {
					a.say("    %s", l)
				}
			}
			a.say("")
			a.say("  Pull requests are tied to tickets by the keys of the tickets, such as")
			a.say("  ENG-12, in their branch, title or description. There is nothing to list:")
			a.say("  the keys are read from the tickets.")
			a.say("")
			a.say("  5. Run the first sync now?")
			if !a.yes(">", true) {
				a.say("")
				a.say("  Later: factorymeter sync, then factorymeter serve.")
				return nil
			}
			root := newRoot()
			root.SetArgs([]string{"sync"})
			root.SetIn(cmd.InOrStdin())
			root.SetOut(cmd.OutOrStdout())
			root.SetErr(cmd.ErrOrStderr())
			if err := root.ExecuteContext(ctx); err != nil {
				return err
			}
			a.say("")
			a.say("  Next: factorymeter serve, to see the line in the browser.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "write over a config file that is already here without asking")
	return cmd
}

func relTo(dir, p string) string {
	if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// jiraSiteOf checks a site the way sync will, and returns it as it goes in
// the file: the host, with the scheme only when it is not https.
func jiraSiteOf(s string) (string, error) {
	if _, err := jira.New(s, "", ""); err != nil {
		return "", err
	}
	s = strings.TrimRight(strings.TrimPrefix(strings.TrimSpace(s), "https://"), "/")
	return strings.ToLower(s), nil
}

// ghLoggedIn says whether the GitHub CLI has a token, without printing it.
func ghLoggedIn(ctx context.Context) bool {
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(c, "gh", "auth", "token").Run() == nil
}
