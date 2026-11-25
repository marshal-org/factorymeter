package signature

import (
	"regexp"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/model"
)

// Fixture is a recorded real pull request with the classification a person
// checked by hand. The files under testdata/prs are fixtures.
type Fixture struct {
	// Note says what the fixture proves, in one line.
	Note     string   `json:"note"`
	Recorded string   `json:"recorded"`
	Expect   Expect   `json:"expect"`
	PR       model.PR `json:"pr"`
}

// Expect is the classification the fixture must keep producing.
type Expect struct {
	Class          Class    `json:"class"`
	Agents         []string `json:"agents,omitempty"`
	PossibleAgents []string `json:"possible_agents,omitempty"`
	// Signatures lists every signature id that must fire, in registry order.
	Signatures []string `json:"signatures,omitempty"`
	// Reviews is the class of every review the fixture keeps, in order.
	Reviews []ReviewExpect `json:"reviews,omitempty"`
}

// ReviewExpect is the classification one review of a fixture must keep.
type ReviewExpect struct {
	Author     string      `json:"author"`
	Class      ReviewClass `json:"class"`
	Agent      string      `json:"agent,omitempty"`
	Why        string      `json:"why"`
	Signatures []string    `json:"signatures,omitempty"`
}

// ExpectOf turns a result into the expectation stored in a fixture. It does
// not cover reviews; ExpectFor does.
func ExpectOf(r Result) Expect {
	e := Expect{Class: r.Class, Agents: r.Agents, PossibleAgents: r.PossibleAgents}
	for _, m := range r.Matches {
		e.Signatures = append(e.Signatures, m.Signature)
	}
	return e
}

// ExpectFor is the whole expectation of a pull request: its class and the
// class of each of its reviews.
func (r *Registry) ExpectFor(pr model.PR) Expect {
	e := ExpectOf(r.Classify(pr))
	for _, rv := range pr.Reviews {
		res := r.ClassifyReview(rv, pr.Author.Login)
		x := ReviewExpect{Author: rv.Author.Login, Class: res.Class, Agent: res.Agent, Why: res.Why}
		for _, m := range res.Matches {
			x.Signatures = append(x.Signatures, m.Signature)
		}
		e.Reviews = append(e.Reviews, x)
	}
	return e
}

// bodyTail is how much of the end of a pull request description a fixture
// keeps. Agent footers and badges sit at the end.
const bodyTail = 2000

var (
	emailRe     = regexp.MustCompile(`<([^<>@\s]+)@([^<>\s]+)>`)
	bareEmailRe = regexp.MustCompile(`[A-Za-z0-9._%+\[\]-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// Emails on these domains belong to an agent vendor. They are what
	// signatures match on, so fixtures keep them.
	keepDomains = map[string]bool{
		"anthropic.com": true, "openai.com": true, "cursor.com": true,
	}
	dashes = strings.NewReplacer(emDash, "-", enDash, "-")
)

// LongDashes holds the em dash and the en dash. This project keeps both out
// of everything it stores and prints, so they are spelled as escapes here.
const (
	emDash     = "\u2014"
	enDash     = "\u2013"
	LongDashes = emDash + enDash
)

const (
	redactedEmail = "redacted@example.invalid"
	noreplyDomain = "users.noreply.github.com"
)

// keepEmail reports whether an address may stay in a fixture. Personal
// addresses never do. A GitHub no-reply address stays only when it belongs to
// a bot or to Copilot, because for a person it spells out their login.
func keepEmail(local, domain string) bool {
	domain = strings.ToLower(domain)
	if domain == "github.com" {
		// noreply@github.com signs commits made in the web editor. Any other
		// address there belongs to a person who works at GitHub.
		return strings.EqualFold(local, "noreply")
	}
	if keepDomains[domain] {
		return true
	}
	if domain == noreplyDomain {
		l := strings.ToLower(local)
		return strings.HasSuffix(l, "[bot]") || strings.HasSuffix(l, "+copilot") || l == "copilot"
	}
	return false
}

// Redact strips a pull request down to what classification needs before it is
// stored in the repository. It drops the title, CI runs, commit author names
// and personal emails, and keeps only the end of the description. Of the
// reviews it keeps the ones that count (agent or human) and the ones a review
// signature matches; the text of a review stays only when a review_body
// signature reads it, else it becomes a placeholder that keeps it non-empty.
func Redact(pr model.PR) model.PR {
	return redact(pr, nil)
}

// RedactWith is Redact with the registry, so it can keep the reviews that
// prove a review signature.
func (r *Registry) RedactWith(pr model.PR) model.PR {
	return redact(pr, r)
}

func cleanText(body string) string {
	return bareEmailRe.ReplaceAllStringFunc(dashes.Replace(strings.ToValidUTF8(body, "")), func(m string) string {
		if local, domain, ok := strings.Cut(m, "@"); ok && keepEmail(local, domain) {
			return m
		}
		return redactedEmail
	})
}

func redact(pr model.PR, reg *Registry) model.PR {
	out := model.PR{
		Repo: pr.Repo, Number: pr.Number, URL: pr.URL, Author: pr.Author, HeadRef: pr.HeadRef,
		State: pr.State, CreatedAt: pr.CreatedAt.UTC().Truncate(24 * time.Hour), UpdatedAt: pr.CreatedAt.UTC().Truncate(24 * time.Hour),
		CommitCount: pr.CommitCount,
	}
	body := pr.Body
	if len(body) > bodyTail {
		body = "[...] " + body[len(body)-bodyTail:]
	}
	// Long dashes become hyphens so the repository can hold to one punctuation
	// rule. No signature reads them. Emails in the description are redacted
	// like the ones in commits.
	out.Body = bareEmailRe.ReplaceAllStringFunc(dashes.Replace(strings.ToValidUTF8(body, "")), func(m string) string {
		if local, domain, ok := strings.Cut(m, "@"); ok && keepEmail(local, domain) {
			return m
		}
		return redactedEmail
	})
	for _, c := range pr.Commits {
		rc := model.Commit{SHA: c.SHA, AuthoredAt: c.AuthoredAt.UTC().Truncate(24 * time.Hour), CommittedAt: c.CommittedAt.UTC().Truncate(24 * time.Hour), Parents: c.Parents}
		for _, a := range c.Authors {
			ra := model.CommitAuthor{Login: a.Login}
			if local, domain, ok := strings.Cut(a.Email, "@"); ok && keepEmail(local, domain) {
				ra.Email = a.Email
			} else if a.Email != "" {
				ra.Email = redactedEmail
			}
			rc.Authors = append(rc.Authors, ra)
		}
		for _, t := range c.Trailers {
			rc.Trailers = append(rc.Trailers, redactTrailer(t))
		}
		out.Commits = append(out.Commits, rc)
	}
	if reg != nil {
		for _, rv := range pr.Reviews {
			res := reg.ClassifyReview(rv, pr.Author.Login)
			if res.Class == ReviewNone && len(res.Matches) == 0 {
				continue
			}
			keep := model.Review{Author: rv.Author, State: rv.State, Via: rv.Via, SubmittedAt: rv.SubmittedAt.UTC().Truncate(24 * time.Hour)}
			bodySig := false
			for _, m := range res.Matches {
				bodySig = bodySig || m.Kind == KindReviewBody
			}
			switch {
			case bodySig:
				body := rv.Body
				if len(body) > bodyTail {
					body = "[...] " + body[len(body)-bodyTail:]
				}
				keep.Body = cleanText(body)
			case strings.TrimSpace(rv.Body) != "":
				keep.Body = "[review text left out]"
			}
			out.Reviews = append(out.Reviews, keep)
		}
	}
	return out
}

func redactTrailer(t string) string {
	key, _, _ := strings.Cut(t, ":")
	m := emailRe.FindStringSubmatch(t)
	switch {
	case m != nil && keepEmail(m[1], m[2]):
		return t
	case m != nil || strings.Contains(t, "@"):
		return key + ": redacted <" + redactedEmail + ">"
	}
	return t
}
