// Package model holds the plain data types shared by the collector, the
// signature classifier, the metrics and the report. Nothing in here talks to
// the network.
package model

import "time"

// Actor is a GitHub account as seen on a pull request, review or commit.
type Actor struct {
	Login string `json:"login"`
	// Type is the GraphQL type name: "User", "Bot", "Mannequin",
	// "Organization" or "EnterpriseUserAccount". Empty when the account is
	// gone.
	Type string `json:"type,omitempty"`
}

// IsBot reports whether GitHub marks the account as a bot.
func (a Actor) IsBot() bool { return a.Type == "Bot" }

// CommitAuthor is one author of a commit. GitHub lists the commit author first
// and then every co-author it parsed from the trailers.
type CommitAuthor struct {
	Login string `json:"login,omitempty"`
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

// Commit is one commit on a pull request branch.
type Commit struct {
	SHA         string         `json:"sha"`
	AuthoredAt  time.Time      `json:"authored_at"`
	CommittedAt time.Time      `json:"committed_at"`
	Parents     int            `json:"parents"`
	Authors     []CommitAuthor `json:"authors,omitempty"`
	// Trailers are the "Key: value" lines of the commit message body, for
	// example "Co-authored-by: Name <email>". A squash commit stacks several
	// messages, so every body line is read, not only the last paragraph.
	Trailers []string `json:"trailers,omitempty"`
}

// Review is one submitted pull request review.
type Review struct {
	Author      Actor     `json:"author"`
	State       string    `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	SubmittedAt time.Time `json:"submitted_at"`
}

// CIRun is one GitHub Actions workflow run that ran on a commit of the pull
// request.
type CIRun struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"`
	Event      string    `json:"event"`
	SHA        string    `json:"sha"`
	Attempt    int       `json:"attempt"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PR is one pull request with everything the classifier and the metrics need.
type PR struct {
	Repo    string `json:"repo"` // owner/name
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Author  Actor  `json:"author"`
	HeadRef string `json:"head_ref"`
	BaseRef string `json:"base_ref,omitempty"`
	Body    string `json:"body,omitempty"`
	State   string `json:"state"` // OPEN, CLOSED, MERGED
	IsDraft bool   `json:"is_draft,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	MergedAt  *time.Time `json:"merged_at,omitempty"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	// ReadyAt is when the pull request first asked for review: the first
	// "ready for review" event when it was opened as a draft, otherwise the
	// time it was created. Nil while it is still a draft.
	ReadyAt *time.Time `json:"ready_at,omitempty"`

	Additions    int `json:"additions,omitempty"`
	Deletions    int `json:"deletions,omitempty"`
	ChangedFiles int `json:"changed_files,omitempty"`

	// CommitCount is the total on GitHub. Commits holds at most the first 100.
	CommitCount int      `json:"commit_count"`
	Commits     []Commit `json:"commits,omitempty"`
	Reviews     []Review `json:"reviews,omitempty"`
	CIRuns      []CIRun  `json:"ci_runs,omitempty"`
}

// Deploy is one production deployment or release.
type Deploy struct {
	At          time.Time `json:"at"`
	Environment string    `json:"environment,omitempty"`
	Ref         string    `json:"ref,omitempty"`
}

// RepoData is everything collected for one repository over a time window.
type RepoData struct {
	Repo    string    `json:"repo"`
	Private bool      `json:"private,omitempty"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	PRs     []PR      `json:"prs"`
	Deploys []Deploy  `json:"deploys,omitempty"`
	// DeploySource says where Deploys came from: "deployments:<environment>",
	// "releases" or "none".
	DeploySource string `json:"deploy_source"`
	// CISource is "actions" when workflow runs were read, "none" when the
	// repository has none in the window or the token could not read them.
	CISource string   `json:"ci_source"`
	Notes    []string `json:"notes,omitempty"`
}
