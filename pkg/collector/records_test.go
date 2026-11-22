package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marshal-org/factorymeter/pkg/github"
	"github.com/marshal-org/factorymeter/pkg/record"
)

func TestAPullRequestBecomesAChange(t *testing.T) {
	const in = `{
	  "number": 7, "url": "https://github.com/acme/app/pull/7", "title": "Fix it", "body": "Why.", "state": "MERGED",
	  "createdAt": "2026-08-03T10:00:00+02:00", "updatedAt": "2026-08-04T09:00:00Z", "mergedAt": "2026-08-04T09:00:00Z", "closedAt": "2026-08-04T09:00:00Z",
	  "additions": 12, "deletions": 3, "changedFiles": 2, "headRefName": "fix/it", "baseRefName": "main", "mergeCommit": {"oid": "m7"},
	  "author": {"__typename": "Bot", "login": "devin-ai-integration"},
	  "commits": {"totalCount": 140, "nodes": [{"commit": {"oid": "abc", "authoredDate": "2026-08-03T07:00:00Z", "committedDate": "2026-08-03T07:30:00Z",
	    "message": "fix: it\n\nSigned-off-by: Dev <dev@example.invalid>\nCo-Authored-By: Claude <noreply@anthropic.com>",
	    "parents": {"totalCount": 1}, "author": {"name": "Dev", "email": "dev@example.invalid", "user": {"login": "dev"}}}}]},
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
	// The commit the merge left on the base branch. On an open pull request
	// GitHub may name a test merge there, and that one is no merge.
	if c.MergeSHA != "m7" {
		t.Errorf("merge commit %q", c.MergeSHA)
	}
	g.State, g.MergedAt = "OPEN", nil
	if open := g.toRecord("acme/app"); open.MergeSHA != "" {
		t.Errorf("an open change has the merge commit %q", open.MergeSHA)
	}
	k := c.Commits[0]
	if k.SHA != "abc" || k.Parents != 1 || len(k.Authors) != 1 || k.Authors[0].Login != "dev" {
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
			"totalCount": 41, "pageInfo": obj{"hasNextPage": true, "endCursor": "c2"}, "nodes": []obj{node(1, day(1), day(2), "a")}}}}}
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
	// The page says how many pull requests the whole list holds, so a
	// display can tell how far a read is.
	if p.Total != 41 {
		t.Errorf("total = %d, want the 41 GitHub counted", p.Total)
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

func TestAnIssueBecomesATicket(t *testing.T) {
	read := func(in string) record.Ticket {
		t.Helper()
		var g gqlIssue
		if err := json.Unmarshal([]byte(in), &g); err != nil {
			t.Fatal(err)
		}
		return g.toRecord("acme/app")
	}
	open := read(`{"number": 7, "url": "https://github.com/acme/app/issues/7", "title": "Make it fast", "state": "OPEN", "stateReason": null,
	  "createdAt": "2026-08-03T10:00:00+02:00", "updatedAt": "2026-08-04T09:00:00Z", "closedAt": null,
	  "author": {"__typename": "User", "login": "dev"}, "labels": {"nodes": [{"name": "bug"}, {"name": "P1"}]}, "timelineItems": {"totalCount": 0}}`)
	if open.ID != "github:acme/app#7" || open.Key != "acme/app#7" || open.Project != "acme/app" || open.Source != record.GitHub ||
		open.URL != "https://github.com/acme/app/issues/7" || open.Title != "Make it fast" || open.Type != "" {
		t.Errorf("identity: %+v", open)
	}
	// An open issue is waiting. Nothing on an issue says that work began.
	if open.State != record.TicketUnstarted || open.RawState != "open" || open.StartedAt != nil || open.DoneAt != nil || open.ClosedAt != nil || open.Reopened != 0 {
		t.Errorf("an open issue: %+v", open)
	}
	if open.CreatedAt.Location().String() != "UTC" || open.CreatedAt.Hour() != 8 || open.UpdatedAt.Hour() != 9 {
		t.Errorf("times: %v %v", open.CreatedAt, open.UpdatedAt)
	}
	if len(open.Labels) != 2 || open.Labels[1] != "P1" || open.Author != (record.Actor{Login: "dev", Kind: record.ActorUser}) {
		t.Errorf("labels %v, author %+v", open.Labels, open.Author)
	}

	const closed = `{"number": 8, "state": "CLOSED", "stateReason": %s, "createdAt": "2026-08-03T08:00:00Z", "updatedAt": "2026-08-05T09:00:00Z",
	  "closedAt": "2026-08-05T11:00:00+02:00", "author": null, "labels": {"nodes": []}, "timelineItems": {"totalCount": 2}}`
	for reason, want := range map[string][2]string{
		`"COMPLETED"`:   {record.TicketDone, "closed completed"},
		`"NOT_PLANNED"`: {record.TicketCancelled, "closed not_planned"},
		`"DUPLICATE"`:   {record.TicketCancelled, "closed duplicate"},
		// Closed before GitHub kept a reason.
		`null`: {record.TicketDone, "closed"},
	} {
		got := read(fmt.Sprintf(closed, reason))
		if got.State != want[0] || got.RawState != want[1] || got.ClosedAt == nil || got.ClosedAt.Hour() != 9 || got.Reopened != 2 {
			t.Errorf("%s: %+v", reason, got)
		}
		// Only a finished ticket has a time it was done.
		if (got.DoneAt != nil) != (want[0] == record.TicketDone) {
			t.Errorf("%s: done at %v", reason, got.DoneAt)
		}
		// A ticket with no labels stores a list, not nothing, and a deleted
		// author is the ghost account.
		if got.Labels == nil || len(got.Labels) != 0 || got.Author.Login != "ghost" {
			t.Errorf("%s: labels %v, author %+v", reason, got.Labels, got.Author)
		}
	}
}

func TestAChangeLinksToTheIssuesItCloses(t *testing.T) {
	n := node(1, day(1), day(2), "a")
	n["closingIssuesReferences"] = obj{"nodes": []obj{
		{"number": 12, "repository": obj{"nameWithOwner": "Acme/App"}},
		{"number": 3, "repository": obj{"nameWithOwner": "acme/plans"}},
		{"number": 0, "repository": obj{"nameWithOwner": "acme/plans"}},
	}}
	s := &server{graphql: func(r gqlReq) any {
		return obj{"data": obj{"repository": obj{"nameWithOwner": "Acme/App", "pullRequests": obj{
			"pageInfo": obj{"hasNextPage": false, "endCursor": "c2"}, "nodes": []obj{n, node(2, day(1), day(2), "b")}}}}}
	}}
	p, err := s.collector(t, Options{}).ChangesPage(context.Background(), PageQuery{Repo: "acme/app", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The second issue is in another repository. The third has no number.
	want := []record.Link{
		{Kind: record.LinkCloses, From: "github:Acme/App#1", FromKind: record.KindChange, To: "github:Acme/App#12", ToKind: record.KindTicket, Found: "source"},
		{Kind: record.LinkCloses, From: "github:Acme/App#1", FromKind: record.KindChange, To: "github:acme/plans#3", ToKind: record.KindTicket, Found: "source"},
	}
	if len(p.Links) != len(want) || p.Links[0] != want[0] || p.Links[1] != want[1] {
		t.Errorf("links: %+v", p.Links)
	}
	if !strings.Contains(s.gql[0].Query, "closingIssuesReferences(first: 20)") {
		t.Error("the query does not ask for the issues a pull request closes")
	}
	// A read that leaves the links out does not ask, so a token that cannot
	// read issues is not refused over them.
	if _, err := s.collector(t, Options{}).ChangesPage(context.Background(), PageQuery{Repo: "acme/app", Size: 10, NoLinks: true}); err != nil {
		t.Fatal(err)
	}
	if q := s.gql[len(s.gql)-1].Query; strings.Contains(q, "closingIssuesReferences") || !strings.Contains(q, "fragment PRFields on PullRequest") {
		t.Errorf("the query without links: %s", q)
	}
}

func TestTicketsPage(t *testing.T) {
	s := &server{graphql: func(r gqlReq) any {
		return obj{"data": obj{"repository": obj{"nameWithOwner": "Acme/App", "issues": obj{
			"pageInfo": obj{"hasNextPage": true, "endCursor": "c2"}, "nodes": []obj{
				{"number": 4, "state": "OPEN", "createdAt": ts(day(1)), "updatedAt": ts(day(2)), "labels": obj{"nodes": []obj{}}, "timelineItems": obj{"totalCount": 0}}}}}}}
	}}
	k := s.collector(t, Options{})
	p, err := k.TicketsPage(context.Background(), TicketQuery{Repo: "acme/app", After: "c1", Size: 50, Since: day(1), OpenOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	v := s.gql[0].Variables
	if v["owner"] != "acme" || v["name"] != "app" || v["after"] != "c1" || v["n"] != float64(50) || v["since"] != ts(day(1)) || len(v["states"].([]any)) != 1 {
		t.Errorf("variables: %v", v)
	}
	// Tickets carry the name as GitHub spells it.
	if !p.More || p.End != "c2" || len(p.Tickets) != 1 || p.Tickets[0].ID != "github:Acme/App#4" || p.Tickets[0].Project != "Acme/App" {
		t.Errorf("page: %+v", p)
	}
	if _, err = k.TicketsPage(context.Background(), TicketQuery{Repo: "acme/app", Size: 50}); err != nil {
		t.Fatal(err)
	}
	if v := s.gql[1].Variables; v["after"] != nil || v["since"] != nil || v["states"] != nil {
		t.Errorf("default variables: %v", v)
	}

	s.graphql = func(gqlReq) any { return obj{"data": obj{"repository": nil}} }
	if _, err := k.TicketsPage(context.Background(), TicketQuery{Repo: "acme/gone", Size: 50}); !errors.Is(err, github.ErrNotFound) {
		t.Errorf("a missing repository: %v", err)
	}
	if _, err := k.TicketsPage(context.Background(), TicketQuery{Repo: "nonsense", Size: 50}); err == nil {
		t.Error("a name with no owner must fail")
	}
}
