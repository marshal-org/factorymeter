package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/collector"
	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

func classifyCmd() *cobra.Command {
	var asJSON bool
	var record, note, registryPath string
	cmd := &cobra.Command{
		Use:   "classify <pull-request>...",
		Short: "Show which agent signatures match a pull request, and why",
		Long: `classify fetches one or more pull requests and prints every signature that
matches. Give a pull request URL or owner/repo#number.

With --record it also writes a redacted fixture of the pull request, for the
signature tests. Read the pull request yourself before you commit a fixture:
the fixture states what the right answer is.`,
		Example: "  factorymeter classify https://github.com/owner/repo/pull/12\n  factorymeter classify owner/repo#12 --record pkg/signature/testdata/prs --note \"Copilot opened it\"",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry(registryPath)
			if err != nil {
				return err
			}
			c, err := newClient(true)
			if err != nil {
				return err
			}
			k := &collector.Collector{C: c}
			for _, ref := range args {
				repo, number, err := collector.ParsePRRef(ref)
				if err != nil {
					return err
				}
				pr, err := k.PullRequest(cmd.Context(), repo, number)
				if err != nil {
					return err
				}
				res := reg.Classify(pr)
				if asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					if err := enc.Encode(map[string]any{"url": pr.URL, "result": res}); err != nil {
						return err
					}
				} else {
					printResult(pr.URL, pr.Author.Login, pr.HeadRef, res)
				}
				if record != "" {
					red := reg.RedactWith(pr)
					fx := signature.Fixture{Note: note, Recorded: time.Now().UTC().Format("2006-01-02"), Expect: reg.ExpectFor(red), PR: red}
					if got, want := fmt.Sprint(fx.Expect.Class, fx.Expect.Agents, fx.Expect.Signatures, fx.Expect.Reviews), fmt.Sprint(reg.ExpectFor(pr).Class, reg.ExpectFor(pr).Agents, reg.ExpectFor(pr).Signatures, keptReviews(reg, pr)); got != want {
						return fmt.Errorf("%s: redaction changed the result (%s, was %s). The fixture would not prove the signature", pr.URL, got, want)
					}
					name := strings.NewReplacer("/", "_", ".", "-").Replace(repo) + fmt.Sprintf("_%d.json", number)
					var buf bytes.Buffer
					enc := json.NewEncoder(&buf)
					enc.SetIndent("", "  ")
					enc.SetEscapeHTML(false)
					if err := enc.Encode(fx); err != nil {
						return err
					}
					path := filepath.Join(record, strings.ToLower(name))
					if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
						return err
					}
					fmt.Fprintln(os.Stderr, "recorded", path)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().StringVar(&record, "record", "", "write a redacted fixture into this directory")
	cmd.Flags().StringVar(&note, "note", "", "one line on what the fixture proves")
	cmd.Flags().StringVar(&registryPath, "registry", "", "use this registry file instead of the built-in one")
	return cmd
}

func printResult(url, author, branch string, res signature.Result) {
	fmt.Printf("%s\n  author %s, branch %s\n  class: %s", url, author, branch, res.Class)
	if len(res.Agents) > 0 {
		fmt.Printf(" (%s, %s confidence)", strings.Join(res.Agents, " + "), res.Confidence)
	}
	if len(res.PossibleAgents) > 0 {
		fmt.Printf(", possible: %s (low confidence only)", strings.Join(res.PossibleAgents, ", "))
	}
	fmt.Println()
	for _, m := range res.Matches {
		fmt.Printf("  %-6s %-24s %s\n", m.Confidence, m.Signature, m.Value)
	}
	if len(res.Matches) == 0 {
		fmt.Println("  no signature matched")
	}
}

func loadRegistry(path string) (*signature.Registry, error) {
	if path == "" {
		return signature.Default()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return signature.Parse(data)
}

func signaturesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "signatures", Short: "Inspect the signature registry"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Print every signature in the built-in registry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := signature.Default()
			if err != nil {
				return err
			}
			for _, a := range reg.Agents {
				fmt.Printf("%s (%s)\n", a.Name, a.ID)
				for _, s := range a.Signatures {
					match := s.Regex
					if len(s.Equals) > 0 {
						match = strings.Join(s.Equals, ", ")
					} else if len(s.Prefix) > 0 {
						match = strings.Join(s.Prefix, ", ")
					}
					fmt.Printf("  %-6s %-20s %-24s %s\n", s.Confidence, s.Kind, s.ID, match)
				}
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "validate [file]",
		Short: "Check a registry file, or the built-in one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			reg, err := loadRegistry(path)
			if err != nil {
				return err
			}
			fmt.Printf("ok: %d agents, %d signatures, %d automation logins\n", len(reg.Agents), len(reg.SignatureIDs()), len(reg.Automation.Logins))
			return nil
		},
	})
	return cmd
}

// keptReviews is the review expectation of the reviews a fixture keeps, read
// from the pull request before redaction: redaction must not change it.
func keptReviews(reg *signature.Registry, pr model.PR) []signature.ReviewExpect {
	var out []signature.ReviewExpect
	for _, x := range reg.ExpectFor(pr).Reviews {
		if x.Class == signature.ReviewNone && len(x.Signatures) == 0 {
			continue
		}
		out = append(out, x)
	}
	return out
}
