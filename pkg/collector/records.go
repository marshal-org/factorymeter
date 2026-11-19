package collector

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// This file reads GitHub into the record layout of the contract, one page or
// one time range at a time, so a sync can store what it has and pick up where
// it stopped.

const pageHead = `
query($owner: String!, $name: String!, $n: Int!, $after: String, $states: [PullRequestState!], $dir: OrderDirection!) {
  repository(owner: $owner, name: $name) {
    nameWithOwner isPrivate
    pullRequests(first: $n, after: $after, states: $states, orderBy: {field: UPDATED_AT, direction: $dir}) {
      pageInfo { hasNextPage endCursor }
      nodes { ...PRFields }
    }
  }
}`

const (
	pageQuery       = pageHead + prFieldsLinked
	pageQueryNoLink = pageHead + prFields
)

// PageQuery asks for one page of a repository's pull requests, ordered by the
// time they were last updated.
type PageQuery struct {
	Repo string
	// After is the cursor of the page before this one. Empty starts at the top.
	After string
	// Size is the number of pull requests asked for.
	Size int
	// OpenOnly leaves out merged and closed pull requests.
	OpenOnly bool
	// OldestFirst reads from the least recently updated up. The default is
	// from the most recently updated down.
	OldestFirst bool
	// NoLinks leaves out the issues the pull requests close.
	NoLinks bool
}

// Page is one page of changes.
type Page struct {
	Changes []record.Change
	// End is the cursor to pass as After for the next page.
	End  string
	More bool
	// Repo is the repository's name as GitHub spells it.
	Repo    string
	Private bool
	// Cut is set when a pull request has more commits than one page holds.
	Cut bool
	// Links are the tickets the changes of the page close, as GitHub lists them.
	Links []record.Link
}

// ChangesPage reads one page of pull requests as changes.
func (k *Collector) ChangesPage(ctx context.Context, q PageQuery) (Page, error) {
	owner, name, err := SplitRepo(q.Repo)
	if err != nil {
		return Page{}, err
	}
	vars := map[string]any{"owner": owner, "name": name, "n": q.Size, "after": nil, "states": nil, "dir": "DESC"}
	if q.After != "" {
		vars["after"] = q.After
	}
	if q.OpenOnly {
		vars["states"] = []string{"OPEN"}
	}
	if q.OldestFirst {
		vars["dir"] = "ASC"
	}
	var out struct {
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
			IsPrivate     bool   `json:"isPrivate"`
			PullRequests  struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []gqlPR `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	}
	query := pageQuery
	if q.NoLinks {
		query = pageQueryNoLink
	}
	if err := k.C.GraphQL(ctx, query, vars, &out); err != nil {
		return Page{}, err
	}
	if out.Repository == nil {
		return Page{}, fmt.Errorf("%w: %s", github.ErrNotFound, q.Repo)
	}
	r := out.Repository
	p := Page{End: r.PullRequests.PageInfo.EndCursor, More: r.PullRequests.PageInfo.HasNextPage, Repo: r.NameWithOwner, Private: r.IsPrivate}
	if p.Repo == "" {
		p.Repo = q.Repo
	}
	for _, g := range r.PullRequests.Nodes {
		c := g.toRecord(p.Repo)
		if c.CommitCount > len(c.Commits) {
			p.Cut = true
		}
		p.Changes = append(p.Changes, c)
		p.Links = append(p.Links, g.links(c.ID)...)
	}
	return p, nil
}

// links are the issues a pull request closes: the ones its text names with a
// closing word, and the ones linked to it by hand. The issue may be in another
// repository.
func (g gqlPR) links(from string) []record.Link {
	var out []record.Link
	for _, n := range g.ClosingIssues.Nodes {
		if n.Number <= 0 || n.Repository.NameWithOwner == "" {
			continue
		}
		out = append(out, record.Link{
			Kind: record.LinkCloses, From: from, FromKind: record.KindChange,
			To: record.TicketID(record.GitHub, ticketKey(n.Repository.NameWithOwner, n.Number)), ToKind: record.KindTicket,
			Found: "source",
		})
	}
	return out
}

// ticketKey is the name people use for an issue: owner/repo#12.
func ticketKey(repo string, number int) string {
	return repo + "#" + strconv.Itoa(number)
}

const ticketQuery = `
query($owner: String!, $name: String!, $n: Int!, $after: String, $states: [IssueState!], $since: DateTime) {
  repository(owner: $owner, name: $name) {
    nameWithOwner
    issues(first: $n, after: $after, states: $states, filterBy: {since: $since}, orderBy: {field: UPDATED_AT, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number url title state stateReason createdAt updatedAt closedAt
        author { __typename login }
        labels(first: 20) { nodes { name } }
        timelineItems(first: 1, itemTypes: [REOPENED_EVENT]) { totalCount }
      }
    }
  }
}`

// TicketQuery asks for one page of a repository's issues, from the least
// recently updated up. Reading in that order lets a sync write down the update
// time it has reached and go on from there.
type TicketQuery struct {
	Repo string
	// After is the cursor of the page before this one. Empty starts at Since.
	After string
	// Size is the number of issues asked for.
	Size int
	// Since leaves out issues last updated before it. Zero reads from the start.
	Since time.Time
	// OpenOnly leaves out closed issues.
	OpenOnly bool
}

// TicketPage is one page of tickets.
type TicketPage struct {
	Tickets []record.Ticket
	// End is the cursor to pass as After for the next page.
	End  string
	More bool
}

type gqlIssue struct {
	Number      int        `json:"number"`
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	StateReason string     `json:"stateReason"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	ClosedAt    *time.Time `json:"closedAt"`
	Author      *gqlActor  `json:"author"`
	Labels      struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	TimelineItems struct {
		TotalCount int `json:"totalCount"`
	} `json:"timelineItems"`
}

// toRecord turns an issue into a ticket. GitHub Issues has two states, open
// and closed, and a reason for closing. An open issue is unstarted: nothing on
// an issue says work began, so the line takes the first linked change as the
// start. An issue closed as not planned or as a duplicate is cancelled. Any
// other closed issue is done, which covers the issues closed before GitHub
// kept a reason.
func (g gqlIssue) toRecord(repo string) record.Ticket {
	key := ticketKey(repo, g.Number)
	t := record.Ticket{
		Source: record.GitHub, ID: record.TicketID(record.GitHub, key), Key: key, Project: repo,
		URL: g.URL, Title: g.Title, Aliases: []string{}, Labels: []string{}, Author: recordActor(g.Author),
		State: record.TicketUnstarted, RawState: strings.ToLower(g.State),
		CreatedAt: g.CreatedAt.UTC(), UpdatedAt: g.UpdatedAt.UTC(), Reopened: g.TimelineItems.TotalCount,
	}
	for _, l := range g.Labels.Nodes {
		t.Labels = append(t.Labels, l.Name)
	}
	if g.State != "CLOSED" {
		return t
	}
	if g.StateReason != "" {
		t.RawState += " " + strings.ToLower(g.StateReason)
	}
	t.ClosedAt = utc(g.ClosedAt)
	switch g.StateReason {
	case "NOT_PLANNED", "DUPLICATE":
		t.State = record.TicketCancelled
	default:
		t.State, t.DoneAt = record.TicketDone, t.ClosedAt
	}
	return t
}

// TicketsPage reads one page of issues as tickets. A repository with Issues
// turned off has none.
func (k *Collector) TicketsPage(ctx context.Context, q TicketQuery) (TicketPage, error) {
	owner, name, err := SplitRepo(q.Repo)
	if err != nil {
		return TicketPage{}, err
	}
	vars := map[string]any{"owner": owner, "name": name, "n": q.Size, "after": nil, "states": nil, "since": nil}
	if q.After != "" {
		vars["after"] = q.After
	}
	if q.OpenOnly {
		vars["states"] = []string{"OPEN"}
	}
	if !q.Since.IsZero() {
		vars["since"] = q.Since.UTC().Format(time.RFC3339)
	}
	var out struct {
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
			Issues        struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []gqlIssue `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}
	if err := k.C.GraphQL(ctx, ticketQuery, vars, &out); err != nil {
		return TicketPage{}, err
	}
	if out.Repository == nil {
		return TicketPage{}, fmt.Errorf("%w: %s", github.ErrNotFound, q.Repo)
	}
	r := out.Repository
	repo := r.NameWithOwner
	if repo == "" {
		repo = q.Repo
	}
	p := TicketPage{End: r.Issues.PageInfo.EndCursor, More: r.Issues.PageInfo.HasNextPage}
	for _, g := range r.Issues.Nodes {
		p.Tickets = append(p.Tickets, g.toRecord(repo))
	}
	return p, nil
}

func recordActor(a *gqlActor) record.Actor {
	m := actor(a)
	kind := record.ActorUser
	if m.Type == "Bot" {
		kind = record.ActorBot
	}
	return record.Actor{Login: m.Login, Kind: kind}
}

// toRecord turns a pull request into a change. Lists are never nil, so a
// change reads the same before and after it is stored.
func (g gqlPR) toRecord(repo string) record.Change {
	c := record.Change{
		Source: record.GitHub, ID: record.ChangeID(record.GitHub, repo, g.Number), Repo: repo, Number: g.Number,
		URL: g.URL, Title: g.Title, Body: g.Body, Author: recordActor(g.Author),
		HeadRef: g.HeadRefName, BaseRef: g.BaseRefName, State: strings.ToLower(g.State), Draft: g.IsDraft,
		CreatedAt: g.CreatedAt.UTC(), UpdatedAt: g.UpdatedAt.UTC(), ReadyAt: utc(readyAt(g)), MergedAt: utc(g.MergedAt), ClosedAt: utc(g.ClosedAt),
		Additions: g.Additions, Deletions: g.Deletions, ChangedFiles: g.ChangedFiles, CommitCount: g.Commits.TotalCount,
		Commits: []record.Commit{}, Reviews: []record.Review{},
	}
	if g.MergeCommit != nil && c.MergedAt != nil {
		c.MergeSHA = g.MergeCommit.OID
	}
	for _, n := range g.Commits.Nodes {
		k := record.Commit{
			SHA: n.Commit.OID, AuthoredAt: n.Commit.AuthoredDate.UTC(), CommittedAt: n.Commit.CommittedDate.UTC(),
			Parents: n.Commit.Parents.TotalCount, Authors: []record.CommitAuthor{}, Trailers: []record.Trailer{},
		}
		for _, a := range n.Commit.Authors.Nodes {
			ca := record.CommitAuthor{Name: a.Name, Email: a.Email}
			if a.User != nil {
				ca.Login = a.User.Login
			}
			k.Authors = append(k.Authors, ca)
		}
		for _, line := range signature.Trailers(n.Commit.Message) {
			key, value, _ := strings.Cut(line, ":")
			k.Trailers = append(k.Trailers, record.Trailer{Key: strings.TrimSpace(key), Value: strings.TrimSpace(value)})
		}
		c.Commits = append(c.Commits, k)
	}
	for _, r := range g.Reviews.Nodes {
		if r.SubmittedAt == nil || r.State == "PENDING" {
			continue
		}
		c.Reviews = append(c.Reviews, record.Review{Author: recordActor(r.Author), State: strings.ToLower(r.State), SubmittedAt: r.SubmittedAt.UTC()})
	}
	sort.SliceStable(c.Reviews, func(i, j int) bool { return c.Reviews[i].SubmittedAt.Before(c.Reviews[j].SubmittedAt) })
	return c
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// Runs reads the GitHub Actions workflow runs of a repository created between
// from and to, newest first, and passes them to fn in batches. An empty event
// reads runs of every event. It stops once max runs were read and reports
// that as cut.
func (k *Collector) Runs(ctx context.Context, repo, event string, from, to time.Time, max int, fn func([]record.CIRun)) (read int, cut bool, err error) {
	err = k.runsRange(ctx, repo, event, from, to, max, &read, &cut, func(runs []restRun) {
		read += len(runs)
		out := make([]record.CIRun, 0, len(runs))
		for _, r := range runs {
			attempt := r.RunAttempt
			if attempt < 1 {
				attempt = 1
			}
			started := r.RunStartedAt
			if started.IsZero() {
				started = r.CreatedAt
			}
			out = append(out, record.CIRun{
				Source: record.GitHub, ID: record.CIRunID(record.GitHub, repo, r.ID, attempt), Repo: repo,
				Workflow: r.Name, Event: r.Event, SHA: r.HeadSHA, Branch: r.HeadBranch, Attempt: attempt, Status: r.Status, Conclusion: r.Conclusion,
				CreatedAt: r.CreatedAt.UTC(), StartedAt: started.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
			})
		}
		fn(out)
	})
	return read, cut, err
}

const deployQuery = `
query($owner: String!, $name: String!, $deps: Boolean!, $rels: Boolean!, $da: String, $ra: String) {
  repository(owner: $owner, name: $name) {
    deployments(first: 100, after: $da, orderBy: {field: CREATED_AT, direction: DESC}) @include(if: $deps) {
      pageInfo { hasNextPage endCursor }
      nodes {
        databaseId environment createdAt commitOid ref { name }
        latestStatus { state createdAt }
        statuses(first: 30) { nodes { state createdAt } }
      }
    }
    releases(first: 100, after: $ra, orderBy: {field: CREATED_AT, direction: DESC}) @include(if: $rels) {
      pageInfo { hasNextPage endCursor }
      nodes { tagName isDraft isPrerelease createdAt publishedAt tagCommit { oid } }
    }
  }
}`

type gqlStatus struct {
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
}

type gqlDeployment struct {
	DatabaseID  int64     `json:"databaseId"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"createdAt"`
	CommitOID   string    `json:"commitOid"`
	Ref         *struct {
		Name string `json:"name"`
	} `json:"ref"`
	LatestStatus *gqlStatus `json:"latestStatus"`
	Statuses     struct {
		Nodes []gqlStatus `json:"nodes"`
	} `json:"statuses"`
}

type gqlRelease struct {
	TagName      string     `json:"tagName"`
	IsDraft      bool       `json:"isDraft"`
	IsPrerelease bool       `json:"isPrerelease"`
	CreatedAt    time.Time  `json:"createdAt"`
	PublishedAt  *time.Time `json:"publishedAt"`
	TagCommit    *struct {
		OID string `json:"oid"`
	} `json:"tagCommit"`
}

// toRecord turns a deployment and its statuses into a deploy. A deployment
// succeeded at its first success status, even if it was later replaced and
// marked inactive. It failed at its first failure or error when it never
// succeeded.
func (d gqlDeployment) toRecord(repo string) record.Deploy {
	out := record.Deploy{
		Source: record.GitHub, ID: record.DeployID(record.GitHub, repo, "deployment", strconv.FormatInt(d.DatabaseID, 10)), Repo: repo,
		Via: "deployment", Environment: d.Environment, Production: prodEnv.MatchString(d.Environment),
		SHA: d.CommitOID, CreatedAt: d.CreatedAt.UTC(), Outcome: record.DeployUnknown,
	}
	if d.Ref != nil {
		out.Ref = d.Ref.Name
	}
	var ok, bad *time.Time
	for _, s := range d.Statuses.Nodes {
		t := s.CreatedAt.UTC()
		switch s.State {
		case "SUCCESS":
			if ok == nil || t.Before(*ok) {
				ok = &t
			}
		case "FAILURE", "ERROR":
			if bad == nil || t.Before(*bad) {
				bad = &t
			}
		}
	}
	switch {
	case ok != nil:
		out.Outcome, out.FinishedAt = record.DeploySuccess, ok
	case bad != nil:
		out.Outcome, out.FinishedAt = record.DeployFailure, bad
	case d.LatestStatus != nil:
		switch d.LatestStatus.State {
		case "PENDING", "QUEUED", "IN_PROGRESS", "WAITING":
			out.Outcome = record.DeployRunning
		}
	}
	return out
}

// Deploys reads the deployments created since depSince and the releases
// created since relSince. A release counts from the time it was published.
// Drafts and prereleases are left out.
func (k *Collector) Deploys(ctx context.Context, repo string, depSince, relSince time.Time) ([]record.Deploy, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	var out []record.Deploy
	deps, rels := true, true
	var da, ra *string
	for deps || rels {
		var res struct {
			Repository *struct {
				Deployments *struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []gqlDeployment `json:"nodes"`
				} `json:"deployments"`
				Releases *struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []gqlRelease `json:"nodes"`
				} `json:"releases"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "deps": deps, "rels": rels, "da": da, "ra": ra}
		if err := k.C.GraphQL(ctx, deployQuery, vars, &res); err != nil {
			return out, err
		}
		if res.Repository == nil {
			return out, fmt.Errorf("%w: %s", github.ErrNotFound, repo)
		}
		if d := res.Repository.Deployments; deps && d != nil {
			for _, n := range d.Nodes {
				if n.CreatedAt.Before(depSince) {
					deps = false
					break
				}
				out = append(out, n.toRecord(repo))
			}
			if !d.PageInfo.HasNextPage {
				deps = false
			}
			da = &d.PageInfo.EndCursor
		} else {
			deps = false
		}
		if r := res.Repository.Releases; rels && r != nil {
			for _, n := range r.Nodes {
				if n.CreatedAt.Before(relSince) {
					rels = false
					break
				}
				// A draft has not shipped, and a prerelease did not ship to
				// production.
				if n.IsDraft || n.IsPrerelease || n.PublishedAt == nil {
					continue
				}
				at := n.PublishedAt.UTC()
				d := record.Deploy{
					Source: record.GitHub, ID: record.DeployID(record.GitHub, repo, "release", n.TagName), Repo: repo,
					Via: "release", Production: true, Ref: n.TagName,
					CreatedAt: at, FinishedAt: &at, Outcome: record.DeploySuccess,
				}
				if n.TagCommit != nil {
					d.SHA = n.TagCommit.OID
				}
				out = append(out, d)
			}
			if !r.PageInfo.HasNextPage {
				rels = false
			}
			ra = &r.PageInfo.EndCursor
		} else {
			rels = false
		}
	}
	return out, nil
}
