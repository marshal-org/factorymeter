package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const secret = "ghp_test_token_that_must_not_leak"

// fake returns a client pointed at a local server, and the waits it was asked
// to sleep. It never sleeps for real.
func fake(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(secret)
	c.BaseURL, c.GraphQLURL = srv.URL, srv.URL+"/graphql"
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return c, &slept
}

func TestRateLimitWait(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := strconv.FormatInt(now.Add(90*time.Second).Unix(), 10)
	past := strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)
	cases := []struct {
		name    string
		h       http.Header
		body    string
		attempt int
		want    time.Duration
		limited bool
	}{
		{"retry-after wins", http.Header{"Retry-After": {"30"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset}}, "", 1, 31 * time.Second, true},
		{"primary limit waits for the reset", http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset}}, "", 1, 92 * time.Second, true},
		{"a reset in the past still waits a little", http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {past}}, "", 1, 2 * time.Second, true},
		{"secondary limit by message backs off by attempt", http.Header{}, `{"message":"You have exceeded a secondary rate limit."}`, 3, 3 * time.Minute, true},
		{"abuse message", http.Header{}, `{"message":"You have triggered an abuse detection mechanism."}`, 1, time.Minute, true},
		{"a plain 403 is not a rate limit", http.Header{"X-Ratelimit-Remaining": {"4999"}}, `{"message":"Resource not accessible by personal access token"}`, 1, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, limited := rateLimitWait(c.h, []byte(c.body), c.attempt, now)
			if got != c.want || limited != c.limited {
				t.Errorf("got %v %v, want %v %v", got, limited, c.want, c.limited)
			}
		})
	}
}

func TestRESTSendsAReadOnlyRequest(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("User-Agent") != "factorymeter" || r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Errorf("headers: %v", r.Header)
		}
		if r.URL.Path != "/repos/o/r/releases" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("url: %s", r.URL)
		}
		w.Header().Set("Link", `<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=9>; rel="last"`)
		io.WriteString(w, `[{"tag_name":"v1"}]`)
	})
	var out []struct {
		TagName string `json:"tag_name"`
	}
	h, err := c.REST(context.Background(), "/repos/o/r/releases", map[string][]string{"per_page": {"100"}}, &out)
	if err != nil || len(out) != 1 || out[0].TagName != "v1" {
		t.Fatalf("got %v %v", out, err)
	}
	if NextPage(h) != "https://api.github.com/x?page=2" {
		t.Errorf("next page: %q", NextPage(h))
	}
	if NextPage(http.Header{"Link": {`<https://api.github.com/x?page=1>; rel="prev"`}}) != "" || NextPage(http.Header{}) != "" {
		t.Error("no next link must give an empty string")
	}
}

func TestRateLimitIsWaitedOutAndRetried(t *testing.T) {
	n := 0
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	})
	var logged []string
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	var out struct{ OK bool }
	if _, err := c.REST(context.Background(), "/x", nil, &out); err != nil || !out.OK {
		t.Fatalf("got %v %v", out, err)
	}
	if n != 2 || c.Calls != 2 || len(*slept) != 1 || (*slept)[0] != 8*time.Second {
		t.Errorf("requests=%d calls=%d slept=%v, want 2 requests and one 8 s wait", n, c.Calls, *slept)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "waiting 8s") {
		t.Errorf("log: %q", logged)
	}
}

func TestALongRateLimitGivesUpWithTheResetTime(t *testing.T) {
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(50*time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"API rate limit exceeded"}`)
	})
	_, err := c.REST(context.Background(), "/x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "rate limit reached. It resets in") || len(*slept) != 0 || c.Calls != 1 {
		t.Errorf("err=%v slept=%v calls=%d", err, *slept, c.Calls)
	}
}

func TestErrors(t *testing.T) {
	status := 0
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, `{"message":"nope"}`)
	})
	cases := []struct {
		status int
		is     error
		text   string
		calls  int
	}{
		{401, nil, "rejected the token", 1},
		{404, ErrNotFound, "/x", 1},
		{403, nil, "github refused /x (403): nope", 1},
		{422, nil, "github returned 422 on /x: nope", 1},
		{502, ErrServer, "gave up after 5 attempts", 5},
	}
	for _, k := range cases {
		status, c.Calls, *slept = k.status, 0, nil
		_, err := c.REST(context.Background(), "/x", nil, nil)
		if err == nil || !strings.Contains(err.Error(), k.text) || (k.is != nil && !errors.Is(err, k.is)) || c.Calls != k.calls {
			t.Errorf("%d: err=%v calls=%d", k.status, err, c.Calls)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%d: the token is in the error text", k.status)
		}
	}
	// Server errors back off 2, 4, 8, 16 and 32 seconds.
	if len(*slept) != 5 || (*slept)[0] != 2*time.Second || (*slept)[4] != 32*time.Second {
		t.Errorf("backoff: %v", *slept)
	}
}

// GitHub has answered a rate limit with a type the client did not know. The
// message tells too. With points left in the budget it is a call for a slower
// pace, so the wait is short and grows, and the query is asked again.
func TestGraphQLRateLimitIsKnownByItsMessage(t *testing.T) {
	replies := []string{
		`{"errors":[{"type":"RATE_LIMIT","code":"graphql_rate_limit","message":"API rate limit already exceeded for user ID 1."}]}`,
		`{"errors":[{"message":"You have exceeded a secondary rate limit."}]}`,
		`{"data":{"v":9}}`,
	}
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rate_limit" {
			fmt.Fprintf(w, `{"resources":{"graphql":{"remaining":4000,"reset":%d}}}`, time.Now().Add(50*time.Minute).Unix())
			return
		}
		io.WriteString(w, replies[0])
		replies = replies[1:]
	})
	var out struct{ V int }
	if err := c.GraphQL(context.Background(), "{ v }", nil, &out); err != nil || out.V != 9 {
		t.Fatalf("got %v %v", out, err)
	}
	if len(*slept) != 2 || (*slept)[0] != time.Minute || (*slept)[1] != 2*time.Minute {
		t.Errorf("the waits must be short and grow, not run to the reset: %v", *slept)
	}
}

func TestCancelStopsTheWait(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c.sleep = sleepCtx
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.REST(ctx, "/x", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

func TestGraphQLRefusesMutations(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) { t.Error("a mutation reached the network") })
	for _, q := range []string{"mutation { addStar(input: {}) { clientMutationId } }", "\n  mutation M($id: ID!) { x }"} {
		if err := c.GraphQL(context.Background(), q, nil, nil); err == nil || !strings.Contains(err.Error(), "never sends a GraphQL mutation") {
			t.Errorf("%q: got %v", q, err)
		}
	}
	if c.Calls != 0 {
		t.Errorf("calls: %d", c.Calls)
	}
}

func TestGraphQL(t *testing.T) {
	var replies []string
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rate_limit" {
			fmt.Fprintf(w, `{"resources":{"graphql":{"reset":%d}}}`, time.Now().Add(40*time.Second).Unix())
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" || !strings.Contains(string(body), `"variables":{"n":3}`) {
			t.Errorf("request: %s %s %s", r.Method, r.URL.Path, body)
		}
		io.WriteString(w, replies[0])
		replies = replies[1:]
	})
	var out struct{ V int }
	ask := func(r ...string) error {
		replies, out.V = r, 0
		return c.GraphQL(context.Background(), "query($n: Int!) { v }", map[string]any{"n": 3}, &out)
	}
	if err := ask(`{"data":{"v":7}}`); err != nil || out.V != 7 {
		t.Fatalf("got %v %v", out, err)
	}
	if err := ask(`{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`, `{"data":{"v":8}}`); err != nil || out.V != 8 {
		t.Fatalf("after a rate limit: %v %v", out, err)
	}
	if len(*slept) != 1 || (*slept)[0] < 39*time.Second || (*slept)[0] > 43*time.Second {
		t.Errorf("the wait must run to the reset the REST endpoint gave: %v", *slept)
	}
	if err := ask(`{"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`); !errors.Is(err, ErrNotFound) {
		t.Errorf("not found: %v", err)
	}
	if err := ask(`{"errors":[{"type":"FORBIDDEN","message":"SAML"}]}`); err == nil || errors.Is(err, ErrServer) || !strings.Contains(err.Error(), "refused the query: SAML") {
		t.Errorf("forbidden: %v", err)
	}
	// A timeout arrives as 200 with an untyped error. The collector retries
	// those with a smaller page, so they must be marked as server errors.
	if err := ask(`{"errors":[{"message":"Something went wrong while executing your query."}]}`); !errors.Is(err, ErrServer) {
		t.Errorf("timeout: %v", err)
	}
	if err := ask(`<html>502</html>`); !errors.Is(err, ErrServer) {
		t.Errorf("a body that is not JSON: %v", err)
	}
}

// A display that listens gets the wait as a length of time, for REST and for
// GraphQL, and the line for a log is then not written as well.
func TestAWaitForARateLimitIsToldToADisplay(t *testing.T) {
	n := 0
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		switch {
		case n == 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
		case n == 2:
			io.WriteString(w, `{"ok":true}`)
		case r.URL.Path == "/rate_limit":
			fmt.Fprintf(w, `{"resources":{"graphql":{"remaining":4000,"reset":%d}}}`, time.Now().Add(50*time.Minute).Unix())
		case n == 3:
			io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`)
		default:
			io.WriteString(w, `{"data":{"v":9}}`)
		}
	})
	var told []time.Duration
	var logged []string
	c.Waiting = func(d time.Duration) { told = append(told, d) }
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	if _, err := c.REST(context.Background(), "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	var out struct{ V int }
	if err := c.GraphQL(context.Background(), "query { v }", nil, &out); err != nil || out.V != 9 {
		t.Fatalf("got %v %v", out, err)
	}
	if len(told) != 2 || len(*slept) != 2 || told[0] != 8*time.Second || told[0] != (*slept)[0] || told[1] != (*slept)[1] || told[1] <= 0 {
		t.Errorf("told %v, slept %v: every wait must be told as it is slept", told, *slept)
	}
	if len(logged) != 0 {
		t.Errorf("a wait that was told was logged too: %q", logged)
	}
}

// A request that gets no answer is tried again, and the wait for it is said
// once, not once for each try.
func TestNoAnswerIsSaidOnce(t *testing.T) {
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {})
	c.BaseURL = "http://127.0.0.1:1"
	var logged []string
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	// Nothing listens there. That is no server error: a smaller page would
	// not be answered either, so the reader must not try one.
	_, err := c.REST(context.Background(), "/x", nil, nil)
	if !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrServer) {
		t.Fatalf("got %v, want it to say that GitHub cannot be reached", err)
	}
	if len(*slept) != maxAttempts {
		t.Errorf("slept %v, want one wait for each of %d tries", *slept, maxAttempts)
	}
	if len(logged) != 1 || logged[0] != "GitHub does not answer. Trying again for about a minute" {
		t.Errorf("logged %q, want the one line", logged)
	}
}
