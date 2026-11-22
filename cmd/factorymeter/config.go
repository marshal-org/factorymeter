package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

// configNames are the files a command looks for in the directory it runs in,
// in this order. At most one of them may be there. When none is, the user
// config file is read: see userConfigFile.
var configNames = []string{
	"factorymeter.yml", "factorymeter.yaml",
	".factorymeter.yml", ".factorymeter.yaml",
	filepath.Join(".github", "factorymeter.yml"), filepath.Join(".github", "factorymeter.yaml"),
}

// readsConfig are the commands that read the config file.
var readsConfig = map[string]bool{"sync": true, "report": true, "serve": true, "store info": true, "store path": true, "store tickets": true, "config": true}

// fileConfig is a config file: what the flags of sync, report and serve would
// say, kept in a repository. A flag on the command line wins over the file.
// Keys and tokens are never in it: they come from the environment.
type fileConfig struct {
	Target   string       `yaml:"target" json:"target"`
	Repos    []string     `yaml:"repos" json:"repos"`
	Store    string       `yaml:"store" json:"store"`
	History  string       `yaml:"history" json:"history"`
	Window   string       `yaml:"window" json:"window"`
	Summary  *bool        `yaml:"summary" json:"summary"`
	Registry string       `yaml:"registry" json:"registry"`
	Sync     syncConfig   `yaml:"sync" json:"sync"`
	Linear   linearConfig `yaml:"linear" json:"linear"`
	Jira     jiraConfig   `yaml:"jira" json:"jira"`
	Serve    serveConfig  `yaml:"serve" json:"serve"`

	// path is where the file was read from.
	path string
}

type syncConfig struct {
	Workers         *int  `yaml:"workers" json:"workers"`
	MaxRuns         *int  `yaml:"max_runs" json:"max_runs"`
	SkipCI          *bool `yaml:"skip_ci" json:"skip_ci"`
	SkipDeploys     *bool `yaml:"skip_deploys" json:"skip_deploys"`
	SkipTickets     *bool `yaml:"skip_tickets" json:"skip_tickets"`
	IncludeForks    *bool `yaml:"include_forks" json:"include_forks"`
	IncludeArchived *bool `yaml:"include_archived" json:"include_archived"`
}

// linearConfig turns Linear on. "linear: true" reads every team the key can
// see, and "linear: {teams: [ENG]}" some of them.
type linearConfig struct {
	On    bool     `yaml:"-" json:"on"`
	Teams []string `yaml:"teams" json:"teams"`
}

func (l *linearConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var on bool
		if err := n.Decode(&on); err != nil {
			return fmt.Errorf("line %d: linear is true, false, or a list of teams under it", n.Line)
		}
		l.On = on
		return nil
	}
	// A decoder of a node does not refuse a field it does not know, so the
	// keys are checked here.
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Value != "teams" {
				return fmt.Errorf("line %d: field %s not found in linear. linear takes teams", k.Line, k.Value)
			}
		}
	}
	type plain linearConfig
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*l = linearConfig(p)
	l.On = true
	return nil
}

type jiraConfig struct {
	Site     string   `yaml:"site" json:"site"`
	Projects []string `yaml:"projects" json:"projects"`
}

type serveConfig struct {
	Addr       string   `yaml:"addr" json:"addr"`
	AllowHosts []string `yaml:"allow_hosts" json:"allow_hosts"`
}

// secretKey is a key name that holds a secret. Such a key is refused by name,
// so a token pasted into the file is never read, and never repeated.
var secretKey = regexp.MustCompile(`(?i)(token|secret|password|passwd|credential|api_?key|^key$|email)`)

// secretValue is a value that looks like a token of GitHub, Linear or Jira.
var secretValue = regexp.MustCompile(`^(lin_api_|lin_oauth_|ATATT|ATCTT|ghp_|gho_|ghu_|ghs_|ghr_|github_pat_)`)

// userConfigFile is the config file of the person, read wherever they run
// factorymeter when the directory has no config file of its own. It is
// $FACTORYMETER_CONFIG, else factorymeter/config.yml under $XDG_CONFIG_HOME,
// else %AppData%\factorymeter\config.yml on Windows and
// ~/.config/factorymeter/config.yml elsewhere. macOS takes ~/.config too, as
// most command line tools do there.
func userConfigFile() (string, error) {
	if v := strings.TrimSpace(os.Getenv("FACTORYMETER_CONFIG")); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "factorymeter", "config.yml"), nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("no user config directory, set FACTORYMETER_CONFIG: %w", err)
		}
		return filepath.Join(base, "factorymeter", "config.yml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory, set FACTORYMETER_CONFIG: %w", err)
	}
	return filepath.Join(home, ".config", "factorymeter", "config.yml"), nil
}

// findConfig returns the config file to read: the one named, else the one in
// the directory, else the user config file when there is one, else none.
func findConfig(named string, dir string) (string, error) {
	if named != "" {
		return named, nil
	}
	local, err := findLocalConfig(dir)
	if err != nil || local != "" {
		return local, err
	}
	user, err := userConfigFile()
	if err != nil {
		// No home is no reason to stop a run that needs no file.
		return "", nil
	}
	if st, err := os.Stat(user); err == nil && !st.IsDir() {
		return user, nil
	}
	return "", nil
}

// findLocalConfig returns the config file of the directory, or none.
func findLocalConfig(dir string) (string, error) {
	var found []string
	for _, name := range configNames {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return filepath.Join(dir, found[0]), nil
	}
	return "", fmt.Errorf("there are %d config files here: %s. Keep one, or name one with --config", len(found), strings.Join(found, ", "))
}

// readConfig reads and checks a config file.
func readConfig(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the config file: %w", err)
	}
	c, err := parseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.path = path
	return c, nil
}

func parseConfig(data []byte) (*fileConfig, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("not YAML: %w", err)
	}
	// Secrets first, before a field is read, so an error never repeats one.
	if err := noSecrets(&root, ""); err != nil {
		return nil, err
	}
	c := &fileConfig{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w. `factorymeter config init` writes a file with every setting", err)
	}
	return c, nil
}

// noSecrets fails on a key that names a secret and on a value that looks like
// a token. It names the key or the line, never the value.
func noSecrets(n *yaml.Node, path string) error {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := noSecrets(c, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if secretKey.MatchString(k) {
				return fmt.Errorf("line %d: %q holds a secret, and a config file must not. Keys and tokens come from the environment: GITHUB_TOKEN, LINEAR_API_KEY, JIRA_EMAIL and JIRA_API_TOKEN", n.Content[i].Line, k)
			}
			if err := noSecrets(n.Content[i+1], path+"."+k); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if secretValue.MatchString(strings.TrimSpace(n.Value)) {
			return fmt.Errorf("line %d: the value of %s looks like a token, and a config file must not hold one. Keys and tokens come from the environment: GITHUB_TOKEN, LINEAR_API_KEY, JIRA_EMAIL and JIRA_API_TOKEN", n.Line, strings.TrimPrefix(path, "."))
		}
	}
	return nil
}

// configState is the config file of this run, if there is one, and the
// flags it set, so a command can tell them from flags on the command line.
type configState struct {
	file *fileConfig
	set  map[string]bool
}

var config configState

// loadConfig reads the config file for a command and sets the flags it holds
// that the command line did not set.
func loadConfig(cmd *cobra.Command, named string, off bool) error {
	config = configState{set: map[string]bool{}}
	if off {
		if named != "" {
			return errors.New("--config and --no-config cannot go together")
		}
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	path, err := findConfig(named, dir)
	if err != nil || path == "" {
		return err
	}
	// A file of this directory is named as the person would name it.
	if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
		path = rel
	}
	c, err := readConfig(path)
	if err != nil {
		return err
	}
	config.file = c
	return c.apply(cmd)
}

// apply sets the flags of a command from the file, where the command has the
// flag and the command line did not set it.
func (c *fileConfig) apply(cmd *cobra.Command) error {
	fs := cmd.Flags()
	set := func(key, flag string, vals ...string) error {
		f := fs.Lookup(flag)
		if f == nil || f.Changed || len(vals) == 0 {
			return nil
		}
		for _, v := range vals {
			if err := setFlag(fs, f, v); err != nil {
				return fmt.Errorf("%s: %s: %w", c.path, key, err)
			}
		}
		config.set[flag] = true
		return nil
	}
	str := func(v string) []string {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	}
	boolean := func(v *bool) []string {
		if v == nil {
			return nil
		}
		return []string{strconv.FormatBool(*v)}
	}
	number := func(v *int) []string {
		if v == nil {
			return nil
		}
		return []string{strconv.Itoa(*v)}
	}
	since := str(c.History)
	sinceKey := "history"
	if cmd.Name() == "report" {
		since, sinceKey = str(c.Window), "window"
	}
	steps := []struct {
		key, flag string
		vals      []string
	}{
		{"store", "store", str(c.Store)},
		{"repos", "repo", c.Repos},
		{sinceKey, "since", since},
		{"summary", "summary", boolean(c.Summary)},
		{"registry", "registry", str(c.Registry)},
		{"sync.workers", "workers", number(c.Sync.Workers)},
		{"sync.max_runs", "max-runs", number(c.Sync.MaxRuns)},
		{"sync.skip_ci", "skip-ci", boolean(c.Sync.SkipCI)},
		{"sync.skip_deploys", "skip-deploys", boolean(c.Sync.SkipDeploys)},
		{"sync.skip_tickets", "skip-tickets", boolean(c.Sync.SkipTickets)},
		{"sync.include_forks", "include-forks", boolean(c.Sync.IncludeForks)},
		{"sync.include_archived", "include-archived", boolean(c.Sync.IncludeArchived)},
		{"jira.site", "jira", str(c.Jira.Site)},
		{"jira.projects", "jira-project", c.Jira.Projects},
		{"serve.addr", "addr", str(c.Serve.Addr)},
		{"serve.allow_hosts", "allow-host", c.Serve.AllowHosts},
	}
	if c.Linear.On {
		steps = append(steps,
			struct {
				key, flag string
				vals      []string
			}{"linear", "linear", []string{"true"}},
			struct {
				key, flag string
				vals      []string
			}{"linear.teams", "linear-team", c.Linear.Teams})
	}
	for _, s := range steps {
		if err := set(s.key, s.flag, s.vals...); err != nil {
			return err
		}
	}
	return nil
}

// setFlag sets one value. A list flag takes each value as one item, so a host
// or a team never splits on a comma.
func setFlag(fs *pflag.FlagSet, f *pflag.Flag, v string) error {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		if err := sv.Append(v); err != nil {
			return err
		}
		f.Changed = true
		return nil
	}
	return fs.Set(f.Name, v)
}

// onCommandLine says whether the command line set any of these flags. A flag
// the config file set does not count.
func onCommandLine(cmd *cobra.Command, flags ...string) bool {
	for _, name := range flags {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed && !config.set[name] {
			return true
		}
	}
	return false
}

// configPath is the config file of this run, or empty.
func configPath() string {
	if config.file == nil {
		return ""
	}
	return config.file.path
}

// fromConfig says whether the config file set a flag, and the command line
// did not.
func fromConfig(flags ...string) bool {
	for _, f := range flags {
		if config.set[f] {
			return true
		}
	}
	return false
}

// targetOf is the target of a command: its argument, else the target of the
// config file.
func targetOf(args []string) (string, error) {
	if len(args) > 0 {
		return parseTarget(args[0])
	}
	if c := config.file; c != nil && strings.TrimSpace(c.Target) != "" {
		t, err := parseTarget(c.Target)
		if err != nil {
			return "", fmt.Errorf("%s: target: %w", c.path, err)
		}
		return t, nil
	}
	if config.file != nil {
		return "", fmt.Errorf("no target. Give one, such as my-org or my-org/my-repo, or set target in %s", config.file.path)
	}
	return "", errors.New(`no target. Give one, such as my-org or my-org/my-repo, or run "factorymeter init" to keep one in a config file`)
}

// The first lines of a config file, by where it is.
const (
	localHeader = `# factorymeter reads this file when it runs in this directory. A flag on the
# command line wins over it.`
	userHeader = `# factorymeter reads this file wherever it runs, unless the directory it runs
# in has a factorymeter.yml of its own. A flag on the command line wins over it.`
)

// configHeader is the first lines of a config file at this path.
func configHeader(user bool) string {
	if user {
		return userHeader
	}
	return localHeader
}

// configTemplate is what "config init" writes, after a header.
const configTemplate = `%s
# Keys and tokens are never kept here: they come from GITHUB_TOKEN,
# LINEAR_API_KEY, JIRA_EMAIL and JIRA_API_TOKEN.
#
# Every setting is optional. "factorymeter config" shows what was read.

# The repository (owner/repo) or the owner whose active repositories to read.
target: %s

# For an owner, the repositories to read. Leave it out for every active one.
# repos: [app, api]

# How far back the first sync reads: weeks (26w), days (180d) or a date.
# history: 26w

# The window a report covers, in whole weeks.
# window: 13w

# Leave out every repository name, title and link, as for a public page.
# summary: false

# Where the store is. The default is the user cache directory. A relative
# path is taken from the directory factorymeter runs in.
# store: .factorymeter-store

# sync:
#   workers: 4
#   skip_ci: false
#   skip_deploys: false
#   include_forks: false
#   include_archived: false

# Linear: the workspace of LINEAR_API_KEY. "linear: true" reads every team.
# A pull request is tied to a ticket when Linear attached it, or when its
# branch, title or description names the key of the ticket, such as ENG-12.
# The key prefixes come from the tickets themselves: nothing to list here.
# linear:
#   teams: [ENG, OPS]

# Jira Cloud, as the account of JIRA_EMAIL and JIRA_API_TOKEN.
# jira:
#   site: acme.atlassian.net
#   projects: [ENG, OPS]

# serve:
#   addr: 127.0.0.1:7377
#   allow_hosts: [meter.internal]
`

func configCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the config file a run here uses, and what it sets",
		Long: `factorymeter reads one config file. It is the first of:

  1. the file --config names
  2. factorymeter.yml, .factorymeter.yml or .github/factorymeter.yml (or .yaml)
     in the directory it runs in
  3. your user config file: $FACTORYMETER_CONFIG, else
     ~/.config/factorymeter/config.yml ($XDG_CONFIG_HOME is followed, and on
     Windows it is %AppData%\factorymeter\config.yml)

--no-config reads none. A flag on the command line wins over the file.

The file holds the target, the history and window, and the settings of sync,
Linear, Jira and serve. It never holds a key or a token: those come from the
environment, and a file that holds one is refused.

"factorymeter config init" writes your user config file with every setting,
commented out. With --here it writes a factorymeter.yml in this directory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			c := config.file
			if format == "json" {
				out := struct {
					Path string `json:"path"`
					*fileConfig
				}{}
				if c != nil {
					out.Path, out.fileConfig = c.path, c
				} else {
					out.fileConfig = &fileConfig{}
				}
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			if format != "text" {
				return fmt.Errorf("--format is text or json, not %q", format)
			}
			if c == nil {
				msg := "No config file. \"factorymeter config init\" writes one."
				if user, err := userConfigFile(); err == nil {
					msg = fmt.Sprintf("No config file: none in this directory, and none at %s.\n\"factorymeter config init\" writes one there.", user)
				}
				fmt.Fprintln(w, msg)
				return nil
			}
			fmt.Fprintf(w, "Config file: %s\n\n", c.path)
			rows := c.rows()
			if len(rows) == 0 {
				fmt.Fprintln(w, "It sets nothing.")
				return nil
			}
			for _, r := range rows {
				fmt.Fprintf(w, "  %-22s %s\n", r[0], r[1])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text or json")
	cmd.AddCommand(configInitCmd())
	return cmd
}

// rows are the settings the file sets, as key and value.
func (c *fileConfig) rows() [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	b := func(v *bool) string {
		if v == nil {
			return ""
		}
		return strconv.FormatBool(*v)
	}
	n := func(v *int) string {
		if v == nil {
			return ""
		}
		return strconv.Itoa(*v)
	}
	list := func(v []string) string { return strings.Join(v, ", ") }
	add("target", c.Target)
	add("repos", list(c.Repos))
	add("history", c.History)
	add("window", c.Window)
	add("summary", b(c.Summary))
	add("store", c.Store)
	add("registry", c.Registry)
	add("sync.workers", n(c.Sync.Workers))
	add("sync.max_runs", n(c.Sync.MaxRuns))
	add("sync.skip_ci", b(c.Sync.SkipCI))
	add("sync.skip_deploys", b(c.Sync.SkipDeploys))
	add("sync.skip_tickets", b(c.Sync.SkipTickets))
	add("sync.include_forks", b(c.Sync.IncludeForks))
	add("sync.include_archived", b(c.Sync.IncludeArchived))
	if c.Linear.On {
		teams := list(c.Linear.Teams)
		if teams == "" {
			teams = "every team"
		}
		add("linear", "on, "+teams)
	}
	add("jira.site", c.Jira.Site)
	add("jira.projects", list(c.Jira.Projects))
	add("serve.addr", c.Serve.Addr)
	add("serve.allow_hosts", list(c.Serve.AllowHosts))
	return out
}

func configInitCmd() *cobra.Command {
	var force, here bool
	cmd := &cobra.Command{
		Use:   "init [owner/repo | owner]",
		Short: "Write your config file with every setting, commented out",
		Long: `config init writes your user config file, ~/.config/factorymeter/config.yml,
with every setting commented out. factorymeter reads it wherever it runs.

With --here it writes factorymeter.yml in this directory instead, to keep the
settings next to the code. A file here wins over the user config file.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "my-org"
			if len(args) > 0 {
				t, err := parseTarget(args[0])
				if err != nil {
					return err
				}
				target = t
			}
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			dest, err := configDest(dir, here)
			if err != nil {
				return err
			}
			if dest.exists && !force {
				return fmt.Errorf("%s is already there. --force writes over it", dest.show)
			}
			if err := writeConfig(dest.path, fmt.Sprintf(configTemplate, configHeader(!here), target)); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Wrote %s. Every setting in it is optional.\n", dest.show)
			if dest.shadow != "" {
				fmt.Fprintf(w, "note: %s in this directory wins over it here.\n", dest.shadow)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "write over a config file that is already there")
	cmd.Flags().BoolVar(&here, "here", false, "write factorymeter.yml in this directory, not your user config file")
	return cmd
}

// configTarget is where a command that writes a config file writes it.
type configTarget struct {
	// path is the file to write, and show is how to name it to the person.
	path, show string
	// exists is set when there is a file at path.
	exists bool
	// shadow is a config file of the directory that wins over the user
	// config file, when one is written there.
	shadow string
}

// configDest picks the file "init" and "config init" write: the user config
// file, or with here the config file of the directory. A directory keeps one
// config file, so with here it is the file that is there, when there is one.
func configDest(dir string, here bool) (configTarget, error) {
	local, err := findLocalConfig(dir)
	if err != nil {
		return configTarget{}, err
	}
	if here {
		if local == "" {
			return configTarget{path: filepath.Join(dir, "factorymeter.yml"), show: "factorymeter.yml"}, nil
		}
		return configTarget{path: local, show: relTo(dir, local), exists: true}, nil
	}
	user, err := userConfigFile()
	if err != nil {
		return configTarget{}, err
	}
	t := configTarget{path: user, show: user}
	if st, err := os.Stat(user); err == nil && !st.IsDir() {
		t.exists = true
	}
	if local != "" {
		t.shadow = relTo(dir, local)
	}
	return t, nil
}

// writeConfig writes a config file, and the directory it goes in.
func writeConfig(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
