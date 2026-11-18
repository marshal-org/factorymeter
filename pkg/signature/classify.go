package signature

import (
	"sort"
	"strings"

	"github.com/marshal-org/factorymeter/pkg/model"
)

// Class is the bucket a pull request is reported in.
type Class string

const (
	ClassAgent      Class = "agent"
	ClassHuman      Class = "human"
	ClassAutomation Class = "automation"
)

// Match is one signature that fired on a pull request.
type Match struct {
	Agent      string     `json:"agent"`
	Signature  string     `json:"signature"`
	Kind       Kind       `json:"kind"`
	Confidence Confidence `json:"confidence"`
	// Value is the text that matched, cut short for display.
	Value string `json:"value"`
}

// Result is the classification of one pull request.
type Result struct {
	Class Class `json:"class"`
	// Agents lists the agents that matched at medium or high, strongest first.
	Agents []string `json:"agents,omitempty"`
	// Confidence is the strongest match behind Class. Empty for human and
	// automation.
	Confidence Confidence `json:"confidence,omitempty"`
	// PossibleAgents lists agents that matched only at low confidence. They do
	// not change the class.
	PossibleAgents []string `json:"possible_agents,omitempty"`
	Matches        []Match  `json:"matches,omitempty"`
}

// Classify checks one pull request against every signature.
//
// The class is "agent" when any signature matches at medium or high. If not,
// it is "automation" when the author is a listed automation account or any
// other bot. Everything else is "human", including pull requests that only
// match a low signature such as a branch prefix.
func (r *Registry) Classify(pr model.PR) Result {
	var res Result
	best := map[string]Confidence{}
	for ai := range r.Agents {
		a := &r.Agents[ai]
		for si := range a.Signatures {
			s := &a.Signatures[si]
			v, ok := firstMatch(s, pr)
			if !ok {
				continue
			}
			res.Matches = append(res.Matches, Match{Agent: a.ID, Signature: s.ID, Kind: s.Kind, Confidence: s.Confidence, Value: clip(v, 120)})
			if s.Confidence.Rank() > best[a.ID].Rank() {
				best[a.ID] = s.Confidence
			}
		}
	}
	for _, a := range r.Agents { // file order keeps the output stable
		c, ok := best[a.ID]
		switch {
		case !ok:
		case c.Rank() >= Medium.Rank():
			res.Agents = append(res.Agents, a.ID)
			if c.Rank() > res.Confidence.Rank() {
				res.Confidence = c
			}
		default:
			res.PossibleAgents = append(res.PossibleAgents, a.ID)
		}
	}
	sort.SliceStable(res.Agents, func(i, j int) bool { return best[res.Agents[i]].Rank() > best[res.Agents[j]].Rank() })
	switch {
	case len(res.Agents) > 0:
		res.Class = ClassAgent
	case r.automation[strings.ToLower(pr.Author.Login)] || pr.Author.IsBot():
		res.Class = ClassAutomation
	default:
		res.Class = ClassHuman
	}
	return res
}

// firstMatch returns the first value on the pull request that the signature
// matches.
func firstMatch(s *Signature, pr model.PR) (string, bool) {
	try := func(v string) (string, bool) {
		if s.matches(v) {
			return v, true
		}
		return "", false
	}
	switch s.Kind {
	case KindPRAuthorLogin:
		return try(pr.Author.Login)
	case KindBranchPrefix:
		return try(pr.HeadRef)
	case KindPRBody:
		if s.re != nil {
			if m := s.re.FindString(pr.Body); m != "" {
				return m, true
			}
			return "", false
		}
		return try(pr.Body)
	case KindCommitAuthorLogin, KindCommitAuthorEmail:
		for _, c := range pr.Commits {
			// Only the first author wrote the commit. The others are
			// co-authors that GitHub parsed from trailers, and those are the
			// commit_trailer kind's job at its own confidence.
			if len(c.Authors) == 0 {
				continue
			}
			v := c.Authors[0].Login
			if s.Kind == KindCommitAuthorEmail {
				v = c.Authors[0].Email
			}
			if s.matches(v) {
				return v, true
			}
		}
	case KindCommitTrailer:
		for _, c := range pr.Commits {
			for _, t := range c.Trailers {
				if s.matches(t) {
					return t, true
				}
			}
		}
	}
	return "", false
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Trailers returns the trailer lines of a commit message: every line after
// the subject that looks like "Some-Key: value" with a hyphenated key, such as
// Co-authored-by or Signed-off-by. Squash merges stack several messages into
// one, so trailers can sit anywhere in the body, not only in the last
// paragraph.
func Trailers(message string) []string {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	var out []string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if isTrailer(line) {
			out = append(out, line)
		}
	}
	return out
}

func isTrailer(line string) bool {
	k, v, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(v) == "" || !strings.Contains(k, "-") {
		return false
	}
	for _, part := range strings.Split(k, "-") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}
