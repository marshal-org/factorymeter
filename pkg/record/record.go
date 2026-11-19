// Package record defines what factorymeter keeps about a line: tickets,
// changes, CI runs, deploys and the links between them.
//
// A source adapter (GitHub, Linear, Jira) turns what it reads into these
// records and nothing else. Every metric is computed from records, so a new
// source needs no change to a metric and a new metric needs no change to a
// source.
package record

import (
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the version of the record layout. The store writes it down
// and refuses to read a store with a newer one. A new field does not raise
// it: a file written before the field reads with the field empty. It goes up
// when a field goes away or comes to mean something else.
const SchemaVersion = 1

// Sources.
const (
	GitHub = "github"
	Linear = "linear"
	Jira   = "jira"
)

// Record kinds. They match the names the metric registry uses.
const (
	KindTicket = "ticket"
	KindChange = "change"
	KindCIRun  = "ci_run"
	KindDeploy = "deploy"
	KindLink   = "link"
)

// Actor kinds.
const (
	ActorUser = "user"
	ActorBot  = "bot"
)

// Actor is an account on a source.
type Actor struct {
	Login string `json:"login" parquet:"login" doc:"The account name on the source. A GitHub bot keeps its [bot] suffix."`
	Kind  string `json:"kind" parquet:"kind" doc:"What the source says the account is." enum:"user,bot"`
}

// Change states.
const (
	ChangeOpen   = "open"
	ChangeClosed = "closed"
	ChangeMerged = "merged"
)

// Change is a pull request or a merge request.
type Change struct {
	Source       string     `json:"source" parquet:"source" doc:"Where the record came from." enum:"github"`
	ID           string     `json:"id" parquet:"id" doc:"Stable id, unique among changes: source:owner/repo#number."`
	Repo         string     `json:"repo" parquet:"repo" doc:"The repository, as owner/name."`
	Number       int        `json:"number" parquet:"number" doc:"The number of the change in its repository."`
	URL          string     `json:"url" parquet:"url" doc:"Link to the change on the source."`
	Title        string     `json:"title" parquet:"title" doc:"The title."`
	Body         string     `json:"body" parquet:"body" doc:"The description. It is read for agent signatures and for ticket keys."`
	Author       Actor      `json:"author" parquet:"author" doc:"The account that opened the change."`
	HeadRef      string     `json:"head_ref" parquet:"head_ref" doc:"The branch the change comes from."`
	BaseRef      string     `json:"base_ref" parquet:"base_ref" doc:"The branch the change goes to."`
	State        string     `json:"state" parquet:"state" doc:"Where the change stands now." enum:"open,closed,merged"`
	Draft        bool       `json:"draft" parquet:"draft" doc:"True while the change is a draft."`
	CreatedAt    time.Time  `json:"created_at" parquet:"created_at,timestamp(microsecond)" doc:"When the change was opened."`
	UpdatedAt    time.Time  `json:"updated_at" parquet:"updated_at,timestamp(microsecond)" doc:"When the source last saw the change move. The sync resumes from here."`
	ReadyAt      *time.Time `json:"ready_at" parquet:"ready_at,optional,timestamp(microsecond)" doc:"When the change first asked for review. Null while it has only ever been a draft."`
	MergedAt     *time.Time `json:"merged_at" parquet:"merged_at,optional,timestamp(microsecond)" doc:"When the change merged. Null when it has not."`
	MergeSHA     string     `json:"merge_sha" parquet:"merge_sha" doc:"The commit the merge left on the base branch. A deploy of that commit holds the change. Empty when the change has not merged or the source does not say."`
	ClosedAt     *time.Time `json:"closed_at" parquet:"closed_at,optional,timestamp(microsecond)" doc:"When the change closed, merged or not. Null while it is open."`
	Additions    int        `json:"additions" parquet:"additions" doc:"Lines added."`
	Deletions    int        `json:"deletions" parquet:"deletions" doc:"Lines deleted."`
	ChangedFiles int        `json:"changed_files" parquet:"changed_files" doc:"Files touched."`
	CommitCount  int        `json:"commit_count" parquet:"commit_count" doc:"Commits on the change. It can be larger than the list of commits, which holds the first 100."`
	Commits      []Commit   `json:"commits" parquet:"commits,list" doc:"The commits of the change, oldest first."`
	Reviews      []Review   `json:"reviews" parquet:"reviews,list" doc:"The submitted reviews, oldest first."`
}

// Commit is one commit of a change.
type Commit struct {
	SHA         string         `json:"sha" parquet:"sha" doc:"The commit id."`
	AuthoredAt  time.Time      `json:"authored_at" parquet:"authored_at,timestamp(microsecond)" doc:"When the commit was written."`
	CommittedAt time.Time      `json:"committed_at" parquet:"committed_at,timestamp(microsecond)" doc:"When the commit was last applied. A rebase moves it."`
	Parents     int            `json:"parents" parquet:"parents" doc:"How many parents the commit has. Two or more make it a merge commit."`
	Authors     []CommitAuthor `json:"authors" parquet:"authors,list" doc:"The author and every co-author."`
	Trailers    []Trailer      `json:"trailers" parquet:"trailers,list" doc:"The trailer lines of the commit message, such as Co-Authored-By."`
}

// CommitAuthor is an author or a co-author of a commit.
type CommitAuthor struct {
	Login string `json:"login" parquet:"login" doc:"The account the source matched the author to. Empty when it matched none."`
	Email string `json:"email" parquet:"email" doc:"The email on the commit."`
	Name  string `json:"name" parquet:"name" doc:"The name on the commit."`
}

// Trailer is one "Key: value" line at the end of a commit message.
type Trailer struct {
	Key   string `json:"key" parquet:"key" doc:"The trailer name, as written."`
	Value string `json:"value" parquet:"value" doc:"The trailer value."`
}

// Review states.
const (
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewCommented        = "commented"
	ReviewDismissed        = "dismissed"
)

// Review is one submitted review of a change.
type Review struct {
	Author      Actor     `json:"author" parquet:"author" doc:"The account that wrote the review."`
	State       string    `json:"state" parquet:"state" doc:"What the review said." enum:"approved,changes_requested,commented,dismissed"`
	SubmittedAt time.Time `json:"submitted_at" parquet:"submitted_at,timestamp(microsecond)" doc:"When the review was submitted."`
}

// CIRun is one run of a CI workflow on one commit.
type CIRun struct {
	Source     string    `json:"source" parquet:"source" doc:"Where the record came from." enum:"github"`
	ID         string    `json:"id" parquet:"id" doc:"Stable id, unique among CI runs: source:owner/repo/run/attempt."`
	Repo       string    `json:"repo" parquet:"repo" doc:"The repository, as owner/name."`
	Workflow   string    `json:"workflow" parquet:"workflow" doc:"The name of the workflow."`
	Event      string    `json:"event" parquet:"event" doc:"What started the run, in the source's words, such as pull_request or push."`
	SHA        string    `json:"sha" parquet:"sha" doc:"The commit the run ran on. It ties the run to a change."`
	Branch     string    `json:"branch" parquet:"branch" doc:"The branch the run ran on. A run of a merge queue is on a branch the queue made, and that name ties it to its change."`
	Attempt    int       `json:"attempt" parquet:"attempt" doc:"1 for the first run, more for a run that was started again."`
	Status     string    `json:"status" parquet:"status" doc:"Whether the run has finished, in the source's words."`
	Conclusion string    `json:"conclusion" parquet:"conclusion" doc:"How the run ended, in the source's words, such as success or failure. Empty while it runs."`
	CreatedAt  time.Time `json:"created_at" parquet:"created_at,timestamp(microsecond)" doc:"When the run was created."`
	StartedAt  time.Time `json:"started_at" parquet:"started_at,timestamp(microsecond)" doc:"When this attempt started."`
	UpdatedAt  time.Time `json:"updated_at" parquet:"updated_at,timestamp(microsecond)" doc:"When the run last changed. For a finished run this is when it finished."`
}

// Deploy outcomes.
const (
	DeploySuccess = "success"
	DeployFailure = "failure"
	DeployRunning = "running"
	DeployUnknown = "unknown"
)

// Deploy is one deployment to an environment, or one published release.
type Deploy struct {
	Source      string     `json:"source" parquet:"source" doc:"Where the record came from." enum:"github"`
	ID          string     `json:"id" parquet:"id" doc:"Stable id, unique among deploys: source:owner/repo/deployment/number or source:owner/repo/release/tag."`
	Repo        string     `json:"repo" parquet:"repo" doc:"The repository, as owner/name."`
	Via         string     `json:"via" parquet:"via" doc:"What kind of event this is on the source." enum:"deployment,release"`
	Environment string     `json:"environment" parquet:"environment" doc:"The environment name. Empty for a release."`
	Production  bool       `json:"production" parquet:"production" doc:"True when the environment is production. A release always is."`
	Ref         string     `json:"ref" parquet:"ref" doc:"The branch or tag that was deployed."`
	SHA         string     `json:"sha" parquet:"sha" doc:"The commit that was deployed, when the source gives it."`
	CreatedAt   time.Time  `json:"created_at" parquet:"created_at,timestamp(microsecond)" doc:"When the deploy started."`
	FinishedAt  *time.Time `json:"finished_at" parquet:"finished_at,optional,timestamp(microsecond)" doc:"When the deploy reported its outcome. Null when the source gives no status."`
	Outcome     string     `json:"outcome" parquet:"outcome" doc:"How the deploy ended." enum:"success,failure,running,unknown"`
}

// Ticket states. Every source maps its own workflow onto these five.
const (
	TicketBacklog   = "backlog"
	TicketUnstarted = "unstarted"
	TicketStarted   = "started"
	TicketDone      = "done"
	TicketCancelled = "cancelled"
)

// Ticket is an issue on GitHub, Linear or Jira.
type Ticket struct {
	Source    string     `json:"source" parquet:"source" doc:"Where the record came from." enum:"github,linear,jira"`
	ID        string     `json:"id" parquet:"id" doc:"Stable id, unique among tickets: the source, a colon, and the name the source never changes. That is the key on GitHub and the issue id on Linear, where a key changes when a ticket moves to another team."`
	Key       string     `json:"key" parquet:"key" doc:"The name people use for the ticket: owner/repo#12 on GitHub, ENG-123 on Linear or Jira."`
	Aliases   []string   `json:"aliases" parquet:"aliases,list" doc:"Keys the ticket had before, such as its key in the team it was moved out of. A change that names one is still linked to the ticket. Empty on GitHub."`
	Project   string     `json:"project" parquet:"project" doc:"The repository, team or project the ticket belongs to."`
	URL       string     `json:"url" parquet:"url" doc:"Link to the ticket on the source."`
	Title     string     `json:"title" parquet:"title" doc:"The title."`
	Type      string     `json:"type" parquet:"type" doc:"The kind of ticket in the source's words, such as bug or feature. Empty when the source has none."`
	Labels    []string   `json:"labels" parquet:"labels,list" doc:"The labels on the ticket."`
	Author    Actor      `json:"author" parquet:"author" doc:"The account that created the ticket."`
	State     string     `json:"state" parquet:"state" doc:"Where the ticket stands now, mapped onto five states." enum:"backlog,unstarted,started,done,cancelled"`
	RawState  string     `json:"raw_state" parquet:"raw_state" doc:"The state in the source's own words."`
	CreatedAt time.Time  `json:"created_at" parquet:"created_at,timestamp(microsecond)" doc:"When the ticket was created."`
	UpdatedAt time.Time  `json:"updated_at" parquet:"updated_at,timestamp(microsecond)" doc:"When the source last saw the ticket move. The sync resumes from here."`
	StartedAt *time.Time `json:"started_at" parquet:"started_at,optional,timestamp(microsecond)" doc:"When the ticket first moved to a started state. Null when it never did, or when the source keeps no history."`
	DoneAt    *time.Time `json:"done_at" parquet:"done_at,optional,timestamp(microsecond)" doc:"When the ticket was last finished. Null when it is not done."`
	ClosedAt  *time.Time `json:"closed_at" parquet:"closed_at,optional,timestamp(microsecond)" doc:"When the ticket closed, done or cancelled. Null while it is open."`
	Reopened  int        `json:"reopened" parquet:"reopened" doc:"How many times the ticket went from done back to an open state."`
}

// Link kinds.
const (
	// LinkCloses ties a change to the ticket it resolves.
	LinkCloses = "closes"
	// LinkMentions ties a change to a ticket it names without resolving it.
	LinkMentions = "mentions"
	// LinkReverts ties a change to the earlier change it reverts.
	LinkReverts = "reverts"
)

// Link ties two records together.
type Link struct {
	Kind     string `json:"kind" parquet:"kind" doc:"What the link means." enum:"closes,mentions,reverts"`
	From     string `json:"from" parquet:"from" doc:"The id of the change the link starts at."`
	FromKind string `json:"from_kind" parquet:"from_kind" doc:"The kind of the record the link starts at." enum:"change"`
	To       string `json:"to" parquet:"to" doc:"The id of the record the link points at."`
	ToKind   string `json:"to_kind" parquet:"to_kind" doc:"The kind of the record the link points at." enum:"ticket,change"`
	Found    string `json:"found" parquet:"found" doc:"Where the link was read." enum:"source,body,title,branch"`
}

// ChangeID builds the id of a change.
func ChangeID(source, repo string, number int) string {
	return fmt.Sprintf("%s:%s#%d", source, repo, number)
}

// CIRunID builds the id of one attempt of a CI run.
func CIRunID(source, repo string, run int64, attempt int) string {
	return fmt.Sprintf("%s:%s/%d/%d", source, repo, run, attempt)
}

// DeployID builds the id of a deploy. via is "deployment" or "release" and
// name is the deployment number or the release tag.
func DeployID(source, repo, via, name string) string {
	return fmt.Sprintf("%s:%s/%s/%s", source, repo, via, name)
}

// TicketID builds the id of a ticket from its key.
func TicketID(source, key string) string {
	return source + ":" + key
}

// SourceName is how a source is written for a reader.
func SourceName(source string) string {
	switch source {
	case GitHub:
		return "GitHub"
	case Linear:
		return "Linear"
	case Jira:
		return "Jira"
	}
	return source
}

// SourceOf reads the source back out of a record id.
func SourceOf(id string) string {
	s, _, _ := strings.Cut(id, ":")
	return s
}
