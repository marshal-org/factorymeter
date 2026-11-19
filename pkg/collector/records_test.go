package collector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/record"
)

func TestAPullRequestBecomesAChange(t *testing.T) {
	const in = `{
	  "number": 7, "url": "https://github.com/acme/app/pull/7", "title": "Fix it", "body": "Why.", "state": "MERGED",
	  "createdAt": "2026-08-03T10:00:00+02:00", "updatedAt": "2026-08-04T09:00:00Z", "mergedAt": "2026-08-04T09:00:00Z", "closedAt": "2026-08-04T09:00:00Z",
	  "additions": 12, "deletions": 3, "changedFiles": 2, "headRefName": "fix/it", "baseRefName": "main",
	  "author": {"__typename": "Bot", "login": "devin-ai-integration"},
	  "commits": {"totalCount": 140, "nodes": [{"commit": {"oid": "abc", "authoredDate": "2026-08-03T07:00:00Z", "committedDate": "2026-08-03T07:30:00Z",
	    "message": "fix: it\n\nSigned-off-by: Dev <dev@example.invalid>\nCo-Authored-By: Claude <noreply@anthropic.com>",
	    "parents": {"totalCount": 1}, "authors": {"nodes": [{"name": "Dev", "email": "dev@example.invalid", "user": {"login": "dev"}}, {"name": "Claude", "email": "noreply@anthropic.com"}]}}}]},
	  "reviews": {"nodes": [
	    {"state": "APPROVED", "submittedAt": "2026-08-04T08:00:00Z", "author": {"__typename": "User", "login": "lead"}},
	    {"state": "PENDING", "author": {"__typename": "User", "login": "lead"}},
	    {"state": "CHANGES_REQUESTED", "submittedAt": "2026-08-03T12:00:00Z", "author": null}]},
	  "timelineItems": {"nodes": []}
	}`
	var g gqlPR
	if err := json.Unmarshal([]byte(in), &g); err != nil {
		t.Fatal(err)
	}
	c := g.toRecord("acme/app")
	if c.ID != "github:acme/app#7" || c.Source != record.GitHub || c.Repo != "acme/app" || c.State != record.ChangeMerged {
		t.Errorf("identity: %+v", c)
	}
	if c.Author != (record.Actor{Login: "devin-ai-integration[bot]", Kind: record.ActorBot}) {
		t.Errorf("author: %+v", c.Author)
	}
	// Times are stored in UTC, whatever zone the source sent.
	if c.CreatedAt.Location().String() != "UTC" || c.CreatedAt.Hour() != 8 || c.ReadyAt == nil || !c.ReadyAt.Equal(c.CreatedAt) || c.MergedAt == nil {
		t.Errorf("times: created %v ready %v merged %v", c.CreatedAt, c.ReadyAt, c.MergedAt)
	}
	if c.CommitCount != 140 || len(c.Commits) != 1 {
		t.Fatalf("commits: %d of %d", len(c.Commits), c.CommitCount)
	}
	k := c.Commits[0]
	if k.SHA != "abc" || k.Parents != 1 || len(k.Authors) != 2 || k.Authors[0].Login != "dev" || k.Authors[1].Login != "" {
		t.Errorf("commit: %+v", k)
	}
	if len(k.Trailers) != 2 || k.Trailers[1] != (record.Trailer{Key: "Co-Authored-By", Value: "Claude <noreply@anthropic.com>"}) {
		t.Errorf("trailers: %+v", k.Trailers)
	}
	// A pending review was never submitted. The rest come in time order.
	if len(c.Reviews) != 2 || c.Reviews[0].State != record.ReviewChangesRequested || c.Reviews[0].Author.Login != "ghost" ||
		c.Reviews[1].State != record.ReviewApproved || c.Reviews[1].Author.Kind != record.ActorUser {
		t.Errorf("reviews: %+v", c.Reviews)
	}
}

func TestChangesPage(t *testing.T) {
	s := &server{graphql: func(r gqlReq) any {
		return obj{"data": obj{"repository": obj{"nameWithOwner": "Acme/App", "isPrivate": true, "pullRequests": obj{
			"pageInfo": obj{"hasNextPage": true, "endCursor": "c2"}, "nodes": []obj{node(1, day(1), day(2), "a")}}}}}
	}}
	k := s.collector(t, Options{})
	p, err := k.ChangesPage(context.Background(), PageQuery{Repo: "acme/app", After: "c1", Size: 10, OpenOnly: true, OldestFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	v := s.gql[0].Variables
	if v["after"] != "c1" || v["dir"] != "ASC" || v["n"] != float64(10) || len(v["states"].([]any)) != 1 {
		t.Errorf("variables: %v", v)
	}
	// Records carry the name as GitHub spells it.
	if p.Repo != "Acme/App" || !p.Private || !p.More || p.End != "c2" || len(p.Changes) != 1 || p.Changes[0].ID != "github:Acme/App#1" || p.Cut {
		t.Errorf("page: %+v", p)
	}
	if _, err = k.ChangesPage(context.Background(), PageQuery{Repo: "acme/app", Size: 10}); err != nil {
		t.Fatal(err)
	}
	if v := s.gql[1].Variables; v["after"] != nil || v["dir"] != "DESC" || v["states"] != nil {
		t.Errorf("default variables: %v", v)
	}

	s.graphql = func(gqlReq) any { return obj{"data": obj{"repository": nil}} }
	if _, err := k.ChangesPage(context.Background(), PageQuery{Repo: "acme/gone", Size: 10}); !errors.Is(err, github.ErrNotFound) {
		t.Errorf("a missing repository: %v", err)
	}
	s.graphql = func(gqlReq) any {
		return obj{"data": obj{"repository": nil}, "errors": []obj{{"type": "INVALID_CURSOR_ARGUMENTS", "message": "`x` does not appear to be a valid cursor."}}}
	}
	if _, err := k.ChangesPage(context.Background(), PageQuery{Repo: "acme/app", After: "x", Size: 10}); !errors.Is(err, github.ErrBadCursor) {
		t.Errorf("a bad cursor: %v", err)
	}
}
