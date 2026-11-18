// Package signature loads the signature registry and classifies pull requests
// against it. It works on plain model.PR values and never touches the network.
package signature

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/marshal-org/factorymeter/signatures"
)

// Confidence says how hard a signature is to produce by accident.
type Confidence string

const (
	High   Confidence = "high"
	Medium Confidence = "medium"
	Low    Confidence = "low"
)

// Rank orders confidence levels. A higher rank is a stronger signal.
func (c Confidence) Rank() int {
	switch c {
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	}
	return 0
}

// Kind names the field of a pull request a signature looks at.
type Kind string

const (
	KindPRAuthorLogin     Kind = "pr_author_login"
	KindCommitAuthorLogin Kind = "commit_author_login"
	KindCommitAuthorEmail Kind = "commit_author_email"
	KindCommitTrailer     Kind = "commit_trailer"
	KindPRBody            Kind = "pr_body"
	KindBranchPrefix      Kind = "branch_prefix"
)

var kinds = map[Kind]bool{
	KindPRAuthorLogin: true, KindCommitAuthorLogin: true, KindCommitAuthorEmail: true,
	KindCommitTrailer: true, KindPRBody: true, KindBranchPrefix: true,
}

// Signature is one way to detect one agent.
type Signature struct {
	ID         string     `yaml:"id"`
	Kind       Kind       `yaml:"kind"`
	Equals     []string   `yaml:"equals,omitempty"`
	Prefix     []string   `yaml:"prefix,omitempty"`
	Regex      string     `yaml:"regex,omitempty"`
	Confidence Confidence `yaml:"confidence"`
	Verified   string     `yaml:"verified,omitempty"`

	re *regexp.Regexp
}

// Agent is one coding agent and the signatures that detect it.
type Agent struct {
	ID         string      `yaml:"id"`
	Name       string      `yaml:"name"`
	Vendor     string      `yaml:"vendor,omitempty"`
	Kind       string      `yaml:"kind,omitempty"`
	Signatures []Signature `yaml:"signatures"`
}

// Automation lists accounts that open pull requests with no author to
// attribute, such as dependency bots.
type Automation struct {
	Logins []string `yaml:"logins"`
}

// Registry is the parsed registry file.
type Registry struct {
	Version    int        `yaml:"version"`
	Agents     []Agent    `yaml:"agents"`
	Automation Automation `yaml:"automation"`

	automation map[string]bool
}

// Default returns the registry embedded in this build.
func Default() (*Registry, error) { return Parse(signatures.YAML) }

// Parse reads and validates a registry. Unknown fields are an error, so a typo
// in the file fails loudly instead of silently dropping a signature.
func Parse(data []byte) (*Registry, error) {
	var r Registry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	if err := r.compile(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Registry) compile() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if r.Version != 1 {
		bad("version: want 1, got %d", r.Version)
	}
	if len(r.Agents) == 0 {
		bad("agents: the list is empty")
	}
	agentIDs, sigIDs := map[string]bool{}, map[string]bool{}
	for ai := range r.Agents {
		a := &r.Agents[ai]
		switch {
		case a.ID == "":
			bad("agents[%d]: id is empty", ai)
		case agentIDs[a.ID]:
			bad("agent %q: listed twice", a.ID)
		}
		agentIDs[a.ID] = true
		if a.Name == "" {
			bad("agent %q: name is empty", a.ID)
		}
		if len(a.Signatures) == 0 {
			bad("agent %q: no signatures", a.ID)
		}
		for si := range a.Signatures {
			s := &a.Signatures[si]
			where := fmt.Sprintf("agent %q signature %q", a.ID, s.ID)
			switch {
			case s.ID == "":
				bad("agent %q signatures[%d]: id is empty", a.ID, si)
			case sigIDs[s.ID]:
				bad("%s: id is used twice", where)
			}
			sigIDs[s.ID] = true
			if !kinds[s.Kind] {
				bad("%s: unknown kind %q", where, s.Kind)
			}
			if s.Confidence.Rank() == 0 {
				bad("%s: confidence must be high, medium or low, got %q", where, s.Confidence)
			}
			set := 0
			for _, on := range []bool{len(s.Equals) > 0, len(s.Prefix) > 0, s.Regex != ""} {
				if on {
					set++
				}
			}
			if set != 1 {
				bad("%s: set exactly one of equals, prefix, regex", where)
			}
			for _, v := range append(append([]string{}, s.Equals...), s.Prefix...) {
				if strings.TrimSpace(v) == "" {
					bad("%s: empty value in a matcher list", where)
				}
			}
			if s.Kind == KindBranchPrefix {
				if len(s.Prefix) == 0 {
					bad("%s: branch_prefix needs a prefix matcher", where)
				}
				// A branch name is free text anyone can type. Keeping it low is
				// what stops "claude/fix-typo" by hand counting as an agent.
				if s.Confidence != Low {
					bad("%s: a branch prefix must be low confidence", where)
				}
			}
			if s.Regex != "" {
				re, err := regexp.Compile(s.Regex)
				if err != nil {
					bad("%s: regex: %v", where, err)
					continue
				}
				if re.MatchString("") {
					bad("%s: regex matches the empty string", where)
				}
				s.re = re
			}
		}
	}
	r.automation = map[string]bool{}
	for _, l := range r.Automation.Logins {
		if strings.TrimSpace(l) == "" {
			bad("automation: empty login")
		}
		r.automation[strings.ToLower(l)] = true
	}
	return errors.Join(errs...)
}

// AgentName returns the display name for an agent id, or the id itself.
func (r *Registry) AgentName(id string) string {
	for _, a := range r.Agents {
		if a.ID == id {
			return a.Name
		}
	}
	return id
}

// SignatureIDs returns every signature id in file order.
func (r *Registry) SignatureIDs() []string {
	var ids []string
	for _, a := range r.Agents {
		for _, s := range a.Signatures {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

func (s *Signature) matches(v string) bool {
	if v == "" {
		return false
	}
	switch {
	case s.re != nil:
		return s.re.MatchString(v)
	case len(s.Equals) > 0:
		for _, e := range s.Equals {
			if strings.EqualFold(e, v) {
				return true
			}
		}
	case len(s.Prefix) > 0:
		lv := strings.ToLower(v)
		for _, p := range s.Prefix {
			if strings.HasPrefix(lv, strings.ToLower(p)) {
				return true
			}
		}
	}
	return false
}
