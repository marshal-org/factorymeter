// Package collector reads pull requests, reviews, commits, CI runs and deploys
// from GitHub for a repository or a whole owner over a time window.
package collector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/model"
	"github.com/marshal-org/factorymeter/pkg/signature"
)

// Options tune a collection run.
type Options struct {
	Since time.Time
	Until time.Time
	// PageSize is the number of pull requests per GraphQL page. The collector
	// halves it when GitHub times out on a page.
	PageSize int
	// MaxRuns caps the workflow runs read per repository. When the cap cuts
	// the window short, CI numbers only cover pull requests opened after the
	// oldest run that was read.
	MaxRuns int
	// SkipCI and SkipDeploys leave those calls out.
	SkipCI      bool
	SkipDeploys bool
	// IncludeForks and IncludeArchived widen an owner scan.
	IncludeForks    bool
	IncludeArchived bool
	Logf            func(format string, a ...any)
}

// Collector reads from one GitHub client.
type Collector struct {
	C   *github.Client
	Opt Options
}

func (k *Collector) logf(format string, a ...any) {
	if k.Opt.Logf != nil {
		k.Opt.Logf(format, a...)
	}
}

const prFields = `
fragment PRFields on PullRequest {
  number url title body state isDraft createdAt updatedAt mergedAt closedAt
  additions deletions changedFiles headRefName baseRefName
  author { __typename login }
  commits(first: 100) {
    totalCount
    nodes { commit {
      oid authoredDate committedDate message
      parents { totalCount }
      authors(first: 10) { nodes { name email user { login } } }
    } }
  }
  reviews(first: 100) { nodes { state submittedAt author { __typename login } } }
  timelineItems(first: 30, itemTypes: [READY_FOR_REVIEW_EVENT, CONVERT_TO_DRAFT_EVENT]) {
    nodes {
      __typename
      ... on ReadyForReviewEvent { createdAt }
      ... on ConvertToDraftEvent { createdAt }
    }
  }
}`

const listQuery = `
query($owner: String!, $name: String!, $n: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    isPrivate
    pullRequests(first: $n, after: $after, orderBy: {field: UPDATED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes { ...PRFields }
    }
  }
}` + prFields

const oneQuery = `
query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { ...PRFields } }
}` + prFields

type gqlActor struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

type gqlPR struct {
	Number       int        `json:"number"`
	URL          string     `json:"url"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	State        string     `json:"state"`
	IsDraft      bool       `json:"isDraft"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	MergedAt     *time.Time `json:"mergedAt"`
	ClosedAt     *time.Time `json:"closedAt"`
	Additions    int        `json:"additions"`
	Deletions    int        `json:"deletions"`
	ChangedFiles int        `json:"changedFiles"`
	HeadRefName  string     `json:"headRefName"`
	BaseRefName  string     `json:"baseRefName"`
	Author       *gqlActor  `json:"author"`
	Commits      struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Commit struct {
				OID           string    `json:"oid"`
				AuthoredDate  time.Time `json:"authoredDate"`
				CommittedDate time.Time `json:"committedDate"`
				Message       string    `json:"message"`
				Parents       struct {
					TotalCount int `json:"totalCount"`
				} `json:"parents"`
				Authors struct {
					Nodes []struct {
						Name  string `json:"name"`
						Email string `json:"email"`
						User  *struct {
							Login string `json:"login"`
						} `json:"user"`
					} `json:"nodes"`
				} `json:"authors"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		Nodes []struct {
			State       string     `json:"state"`
			SubmittedAt *time.Time `json:"submittedAt"`
			Author      *gqlActor  `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`
	TimelineItems struct {
		Nodes []struct {
			Typename  string    `json:"__typename"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"nodes"`
	} `json:"timelineItems"`
}

func actor(a *gqlActor) model.Actor {
	if a == nil {
		return model.Actor{Login: "ghost"}
	}
	// GraphQL drops the "[bot]" suffix that REST and the web UI show. Put it
	// back so a bot never shares a login with a person of the same name.
	login := a.Login
	if a.Typename == "Bot" && !strings.HasSuffix(login, "[bot]") {
		login += "[bot]"
	}
	return model.Actor{Login: login, Type: a.Typename}
}

func (g gqlPR) toModel(repo string) model.PR {
	pr := model.PR{
		Repo: repo, Number: g.Number, URL: g.URL, Title: g.Title, Author: actor(g.Author),
		HeadRef: g.HeadRefName, BaseRef: g.BaseRefName, Body: g.Body, State: g.State, IsDraft: g.IsDraft,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt, MergedAt: g.MergedAt, ClosedAt: g.ClosedAt,
		Additions: g.Additions, Deletions: g.Deletions, ChangedFiles: g.ChangedFiles,
		CommitCount: g.Commits.TotalCount,
	}
	for _, n := range g.Commits.Nodes {
		c := model.Commit{
			SHA: n.Commit.OID, AuthoredAt: n.Commit.AuthoredDate, CommittedAt: n.Commit.CommittedDate,
			Parents: n.Commit.Parents.TotalCount, Trailers: signature.Trailers(n.Commit.Message),
		}
		for _, a := range n.Commit.Authors.Nodes {
			ca := model.CommitAuthor{Name: a.Name, Email: a.Email}
			if a.User != nil {
				ca.Login = a.User.Login
			}
			c.Authors = append(c.Authors, ca)
		}
		pr.Commits = append(pr.Commits, c)
	}
	for _, r := range g.Reviews.Nodes {
		if r.SubmittedAt == nil || r.State == "PENDING" {
			continue
		}
		pr.Reviews = append(pr.Reviews, model.Review{Author: actor(r.Author), State: r.State, SubmittedAt: *r.SubmittedAt})
	}
	sort.SliceStable(pr.Reviews, func(i, j int) bool { return pr.Reviews[i].SubmittedAt.Before(pr.Reviews[j].SubmittedAt) })
	pr.ReadyAt = readyAt(g)
	return pr
}

// readyAt works out when the pull request first asked for review. A pull
// request opened as a draft has a "ready for review" event before any "convert
// to draft" event. One opened ready has no such event, or was converted to a
// draft later, and counts from its creation.
func readyAt(g gqlPR) *time.Time {
	ev := g.TimelineItems.Nodes
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].CreatedAt.Before(ev[j].CreatedAt) })
	if len(ev) > 0 && ev[0].Typename == "ReadyForReviewEvent" {
		t := ev[0].CreatedAt
		return &t
	}
	if len(ev) == 0 && g.IsDraft {
		return nil
	}
	t := g.CreatedAt
	return &t
}

// SplitRepo splits "owner/name".
func SplitRepo(full string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(full, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("want owner/repo, got %q", full)
	}
	return owner, name, nil
}

var prURL = regexp.MustCompile(`^(?:https?://)?github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// ParsePRRef reads "https://github.com/owner/repo/pull/12" or "owner/repo#12".
func ParsePRRef(s string) (repo string, number int, err error) {
	if m := prURL.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[3])
		return m[1] + "/" + m[2], n, nil
	}
	if r, n, ok := strings.Cut(s, "#"); ok {
		if num, err := strconv.Atoi(n); err == nil {
			if _, _, err := SplitRepo(r); err == nil {
				return r, num, nil
			}
		}
	}
	return "", 0, fmt.Errorf("want a pull request URL or owner/repo#number, got %q", s)
}

// PullRequest fetches one pull request.
func (k *Collector) PullRequest(ctx context.Context, repo string, number int) (model.PR, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return model.PR{}, err
	}
	var out struct {
		Repository struct {
			PullRequest *gqlPR `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := k.C.GraphQL(ctx, oneQuery, map[string]any{"owner": owner, "name": name, "number": number}, &out); err != nil {
		return model.PR{}, err
	}
	if out.Repository.PullRequest == nil {
		return model.PR{}, fmt.Errorf("%w: %s#%d", github.ErrNotFound, repo, number)
	}
	return out.Repository.PullRequest.toModel(repo), nil
}

// Repo collects one repository over the window.
func (k *Collector) Repo(ctx context.Context, repo string) (model.RepoData, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return model.RepoData{}, err
	}
	rd := model.RepoData{Repo: repo, Since: k.Opt.Since, Until: k.Opt.Until, DeploySource: "none", CISource: "none"}
	size := k.Opt.PageSize
	if size <= 0 {
		size = 25
	}
	var after *string
	pages := 0
	for {
		var out struct {
			Repository struct {
				IsPrivate    bool `json:"isPrivate"`
				PullRequests struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []gqlPR `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}
		err := k.C.GraphQL(ctx, listQuery, map[string]any{"owner": owner, "name": name, "n": size, "after": after}, &out)
		if errors.Is(err, github.ErrServer) && size > 1 {
			// A page of big pull requests can time out. A smaller page of the
			// same cursor asks for less and usually goes through.
			size = (size + 1) / 2
			k.logf("%s: github timed out on a page, retrying with %d per page", repo, size)
			continue
		}
		if err != nil {
			return rd, fmt.Errorf("%s: pull requests: %w", repo, err)
		}
		pages++
		rd.Private = out.Repository.IsPrivate
		done := false
		for _, g := range out.Repository.PullRequests.Nodes {
			// Pages run from the most recently updated pull request down. The
			// first one last touched before the window ends the scan: nothing
			// older can have merged or closed inside the window.
			if g.UpdatedAt.Before(k.Opt.Since) {
				done = true
				break
			}
			if g.CreatedAt.After(k.Opt.Until) {
				continue
			}
			pr := g.toModel(repo)
			if pr.CommitCount > len(pr.Commits) {
				rd.Notes = appendOnce(rd.Notes, "Some pull requests have more than 100 commits. Only the first 100 were read.")
			}
			rd.PRs = append(rd.PRs, pr)
		}
		pi := out.Repository.PullRequests.PageInfo
		if done || !pi.HasNextPage {
			break
		}
		after = &pi.EndCursor
		if pages%4 == 0 {
			k.logf("%s: %d pull requests read", repo, len(rd.PRs))
		}
	}
	k.logf("%s: %d pull requests touched in the window", repo, len(rd.PRs))
	if !k.Opt.SkipCI && len(rd.PRs) > 0 {
		if err := k.ciRuns(ctx, &rd); err != nil {
			rd.Notes = append(rd.Notes, "CI runs could not be read: "+err.Error())
		}
	}
	if !k.Opt.SkipDeploys {
		if err := k.deploys(ctx, &rd); err != nil {
			rd.Notes = append(rd.Notes, "Deploys could not be read: "+err.Error())
		}
	}
	return rd, nil
}

func appendOnce(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

type restRun struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Event        string    `json:"event"`
	HeadSHA      string    `json:"head_sha"`
	RunAttempt   int       `json:"run_attempt"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	CreatedAt    time.Time `json:"created_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ciRuns reads GitHub Actions workflow runs in the window and attaches each to
// the pull requests that contain its head commit.
func (k *Collector) ciRuns(ctx context.Context, rd *model.RepoData) error {
	bySHA := map[string][]int{}
	earliest := k.Opt.Until
	for i, pr := range rd.PRs {
		for _, c := range pr.Commits {
			bySHA[c.SHA] = append(bySHA[c.SHA], i)
		}
		if pr.CreatedAt.Before(earliest) {
			earliest = pr.CreatedAt
		}
	}
	// Runs older than the window still belong to a pull request that merged
	// inside it, so read back to the oldest pull request, but no further than
	// one extra window.
	from := k.Opt.Since.Add(-k.Opt.Until.Sub(k.Opt.Since))
	if earliest.After(from) {
		from = earliest
	}
	max := k.Opt.MaxRuns
	if max <= 0 {
		max = 30000
	}
	read, joined, truncated := 0, 0, false
	attach := func(runs []restRun) {
		for _, r := range runs {
			read++
			idx, ok := bySHA[r.HeadSHA]
			if !ok {
				continue
			}
			joined++
			run := model.CIRun{ID: r.ID, Workflow: r.Name, Event: r.Event, SHA: r.HeadSHA, Attempt: r.RunAttempt,
				Status: r.Status, Conclusion: r.Conclusion, CreatedAt: r.CreatedAt, StartedAt: r.RunStartedAt, UpdatedAt: r.UpdatedAt}
			for _, i := range idx {
				rd.PRs[i].CIRuns = append(rd.PRs[i].CIRuns, run)
			}
		}
	}
	// Runs triggered by pull request events are the usual CI. Repositories
	// that only build on push have none of those, so fall back to every event.
	for _, events := range [][]string{{"pull_request", "pull_request_target"}, {""}} {
		for _, ev := range events {
			if err := k.runsRange(ctx, rd.Repo, ev, from, k.Opt.Until, max, &read, &truncated, attach); err != nil {
				return err
			}
		}
		if joined > 0 {
			break
		}
	}
	if joined > 0 {
		rd.CISource = "actions"
	}
	if truncated {
		rd.Notes = append(rd.Notes, fmt.Sprintf("The repository has more than %d workflow runs in the window. CI numbers use the most recent ones.", max))
	}
	k.logf("%s: %d workflow runs read, %d on pull request commits", rd.Repo, read, joined)
	return nil
}

// runsRange lists runs created between from and to. The API returns at most
// 1,000 results for one filter, so a range with more is split in half until
// each half fits. Newer halves go first so a cap drops the oldest runs.
func (k *Collector) runsRange(ctx context.Context, repo, event string, from, to time.Time, max int, read *int, truncated *bool, attach func([]restRun)) error {
	if *read >= max {
		*truncated = true
		return nil
	}
	q := url.Values{}
	q.Set("per_page", "100")
	q.Set("created", from.UTC().Format(time.RFC3339)+".."+to.UTC().Format(time.RFC3339))
	if event != "" {
		q.Set("event", event)
	}
	var page struct {
		TotalCount   int       `json:"total_count"`
		WorkflowRuns []restRun `json:"workflow_runs"`
	}
	q.Set("page", "1")
	if _, err := k.C.REST(ctx, "/repos/"+repo+"/actions/runs", q, &page); err != nil {
		return err
	}
	if page.TotalCount > 1000 && to.Sub(from) > time.Minute {
		mid := from.Add(to.Sub(from) / 2)
		if err := k.runsRange(ctx, repo, event, mid.Add(time.Second), to, max, read, truncated, attach); err != nil {
			return err
		}
		return k.runsRange(ctx, repo, event, from, mid, max, read, truncated, attach)
	}
	attach(page.WorkflowRuns)
	for p := 2; len(page.WorkflowRuns) == 100 && p <= 10; p++ {
		if *read >= max {
			*truncated = true
			return nil
		}
		q.Set("page", strconv.Itoa(p))
		page.WorkflowRuns = nil
		if _, err := k.C.REST(ctx, "/repos/"+repo+"/actions/runs", q, &page); err != nil {
			return err
		}
		attach(page.WorkflowRuns)
	}
	return nil
}

var prodEnv = regexp.MustCompile(`(?i)(^|[^a-z])(prod|production|live)([^a-z]|$)`)

// deploys counts production deploys in the window. It prefers the Deployments
// API with an environment that looks like production. Without one it falls
// back to published releases, then to GitHub Pages deployments.
func (k *Collector) deploys(ctx context.Context, rd *model.RepoData) error {
	type dep struct {
		Environment string    `json:"environment"`
		Ref         string    `json:"ref"`
		CreatedAt   time.Time `json:"created_at"`
	}
	byEnv := map[string][]model.Deploy{}
	q := url.Values{"per_page": {"100"}}
	for p := 1; p <= 30; p++ {
		q.Set("page", strconv.Itoa(p))
		var deps []dep
		if _, err := k.C.REST(ctx, "/repos/"+rd.Repo+"/deployments", q, &deps); err != nil {
			return err
		}
		old := false
		for _, d := range deps {
			if d.CreatedAt.Before(k.Opt.Since) {
				old = true
				continue
			}
			if d.CreatedAt.After(k.Opt.Until) {
				continue
			}
			byEnv[d.Environment] = append(byEnv[d.Environment], model.Deploy{At: d.CreatedAt, Environment: d.Environment, Ref: d.Ref})
		}
		if old || len(deps) < 100 {
			break
		}
	}
	best := ""
	for env, list := range byEnv {
		if prodEnv.MatchString(env) && (best == "" || len(list) > len(byEnv[best]) || len(list) == len(byEnv[best]) && env < best) {
			best = env
		}
	}
	if best != "" {
		rd.Deploys, rd.DeploySource = byEnv[best], "deployments:"+best
		return nil
	}
	type rel struct {
		TagName     string     `json:"tag_name"`
		Draft       bool       `json:"draft"`
		Prerelease  bool       `json:"prerelease"`
		PublishedAt *time.Time `json:"published_at"`
	}
	q = url.Values{"per_page": {"100"}}
	for p := 1; p <= 10; p++ {
		q.Set("page", strconv.Itoa(p))
		var rels []rel
		if _, err := k.C.REST(ctx, "/repos/"+rd.Repo+"/releases", q, &rels); err != nil {
			return err
		}
		old := false
		for _, r := range rels {
			if r.Draft || r.Prerelease || r.PublishedAt == nil {
				continue
			}
			if r.PublishedAt.Before(k.Opt.Since) {
				old = true
				continue
			}
			if r.PublishedAt.After(k.Opt.Until) {
				continue
			}
			rd.Deploys = append(rd.Deploys, model.Deploy{At: *r.PublishedAt, Ref: r.TagName})
		}
		if old || len(rels) < 100 {
			break
		}
	}
	if len(rd.Deploys) > 0 {
		rd.DeploySource = "releases"
		return nil
	}
	if list := byEnv["github-pages"]; len(list) > 0 {
		rd.Deploys, rd.DeploySource = list, "deployments:github-pages"
	}
	return nil
}

// Repos lists the repositories of an owner that were pushed to in the window.
func (k *Collector) Repos(ctx context.Context, owner string) ([]string, error) {
	const q = `
query($owner: String!, $after: String) {
  repositoryOwner(login: $owner) {
    repositories(first: 100, after: $after, orderBy: {field: PUSHED_AT, direction: DESC}, ownerAffiliations: [OWNER]) {
      pageInfo { hasNextPage endCursor }
      nodes { nameWithOwner isArchived isFork pushedAt }
    }
  }
}`
	var repos []string
	var after *string
	for {
		var out struct {
			RepositoryOwner *struct {
				Repositories struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						NameWithOwner string     `json:"nameWithOwner"`
						IsArchived    bool       `json:"isArchived"`
						IsFork        bool       `json:"isFork"`
						PushedAt      *time.Time `json:"pushedAt"`
					} `json:"nodes"`
				} `json:"repositories"`
			} `json:"repositoryOwner"`
		}
		if err := k.C.GraphQL(ctx, q, map[string]any{"owner": owner, "after": after}, &out); err != nil {
			return nil, err
		}
		if out.RepositoryOwner == nil {
			return nil, fmt.Errorf("%w: owner %q", github.ErrNotFound, owner)
		}
		done := false
		for _, n := range out.RepositoryOwner.Repositories.Nodes {
			if n.PushedAt == nil || n.PushedAt.Before(k.Opt.Since) {
				done = true
				break
			}
			if n.IsArchived && !k.Opt.IncludeArchived || n.IsFork && !k.Opt.IncludeForks {
				continue
			}
			repos = append(repos, n.NameWithOwner)
		}
		pi := out.RepositoryOwner.Repositories.PageInfo
		if done || !pi.HasNextPage {
			break
		}
		after = &pi.EndCursor
	}
	return repos, nil
}

// Target collects a repository ("owner/name") or every active repository of an
// owner ("owner").
func (k *Collector) Target(ctx context.Context, target string) ([]model.RepoData, error) {
	if strings.Contains(target, "/") {
		rd, err := k.Repo(ctx, target)
		if err != nil {
			return nil, err
		}
		return []model.RepoData{rd}, nil
	}
	repos, err := k.Repos(ctx, target)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("%s has no repository with a push since %s that this token can read", target, k.Opt.Since.Format("2006-01-02"))
	}
	k.logf("%s: %d repositories pushed to in the window", target, len(repos))
	var out []model.RepoData
	for _, r := range repos {
		rd, err := k.Repo(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, rd)
	}
	return out, nil
}
