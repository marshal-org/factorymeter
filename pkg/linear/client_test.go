package linear

import (
	"context"
	"encoding/json"
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

const secret = "lin_api_test_key_that_must_not_leak"

// fake returns a client pointed at a local server, and the waits it was asked
// to sleep. It never sleeps for real.
func fake(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(secret)
	c.URL = srv.URL
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return c, &slept
}

type org struct {
	Organization struct {
		URLKey string `json:"urlKey"`
	} `json:"organization"`
}

const orgQuery = `query { organization { urlKey } }`

func TestQuerySendsTheKeyAsItIsAndDecodesData(t *testing.T) {
	var got *http.Request
	var sent map[string]any
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		fmt.Fprint(w, `{"data":{"organization":{"urlKey":"acme"}}}`)
	})
	c.UserAgent = "factorymeter/test"
	var out org
	if err := c.Query(context.Background(), orgQuery, map[string]any{"after": "c1"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Organization.URLKey != "acme" {
		t.Errorf("urlKey = %q", out.Organization.URLKey)
	}
	// A personal API key goes in bare: Linear takes "Bearer" only for OAuth.
	if a := got.Header.Get("Authorization"); a != secret {
		t.Errorf("Authorization = %q, want the key as given", a)
	}
	if got.Method != http.MethodPost || got.Header.Get("Content-Type") != "application/json" || got.Header.Get("User-Agent") != "factorymeter/test" {
		t.Errorf("request = %s %v", got.Method, got.Header)
	}
	if sent["query"] != orgQuery || sent["variables"].(map[string]any)["after"] != "c1" {
		t.Errorf("payload = %v", sent)
	}
	if c.Calls != 1 {
		t.Errorf("calls = %d, want 1", c.Calls)
	}
}

func TestQueryRefusesToWrite(t *testing.T) {
	c, _ := fake(t, func(http.ResponseWriter, *http.Request) { t.Error("a request was sent") })
	for _, q := range []string{"mutation { issueCreate }", "  \n mutation M { x }", "subscription { issue }"} {
		if err := c.Query(context.Background(), q, nil, nil); err == nil {
			t.Errorf("%q was sent", q)
		}
	}
	if c.Calls != 0 {
		t.Errorf("calls = %d, want 0", c.Calls)
	}
}

func TestQueryErrors(t *testing.T) {
	gql := func(code, msg string) string {
		return fmt.Sprintf(`{"errors":[{"message":%q,"extensions":{"code":%q}}]}`, msg, code)
	}
	cases := []struct {
		name   string
		status int
		body   string
		is     error
		say    string
		calls  int
	}{
		{"a bad key by status", 401, gql("AUTHENTICATION_ERROR", "Authentication required, not authenticated"), nil, "LINEAR_API_KEY", 1},
		{"a bad key by code", 400, gql("AUTHENTICATION_ERROR", "It looks like you're trying to use an API key as a Bearer token"), nil, "LINEAR_API_KEY", 1},
		{"too complex is not tried again", 400, gql("INPUT_ERROR", "Query too complex"), ErrTooComplex, "", 1},
		{"forbidden", 403, gql("FORBIDDEN", "No access to team"), ErrForbidden, "No access to team", 1},
		{"a bad query is not tried again", 400, gql("GRAPHQL_VALIDATION_FAILED", `Cannot query field "nope" on type "Issue".`), nil, `(400): Cannot query field "nope"`, 1},
		{"errors with status 200 are a server error", 200, `{"data":{"organization":null},"errors":[{"message":"Internal server error","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, ErrServer, "Internal server error", 1},
		{"a body that is not JSON", 200, `<html>gateway</html>`, ErrServer, "decode", 1},
		{"a server error is tried five times", 502, `bad gateway`, ErrServer, "gave up after 5 attempts", 5},
		{"another status", 418, `teapot`, nil, "linear returned 418: teapot", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := fake(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			var out org
			err := c.Query(context.Background(), orgQuery, nil, &out)
			if err == nil {
				t.Fatal("no error")
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error %q is not %q", err, tc.is)
			}
			if tc.is == nil && (errors.Is(err, ErrServer) || errors.Is(err, ErrTooComplex) || errors.Is(err, ErrForbidden)) {
				t.Errorf("error %q is marked, and should not be", err)
			}
			if !strings.Contains(err.Error(), tc.say) {
				t.Errorf("error %q does not say %q", err, tc.say)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the key is in the error: %q", err)
			}
			if c.Calls != tc.calls {
				t.Errorf("calls = %d, want %d", c.Calls, tc.calls)
			}
		})
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ms := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).UnixMilli(), 10) }
	cases := []struct {
		name    string
		h       http.Header
		attempt int
		want    time.Duration
	}{
		// 30 seconds asked for, plus one.
		{"retry-after wins", http.Header{"Retry-After": {"30"}, "X-Ratelimit-Requests-Remaining": {"0"}, "X-Ratelimit-Requests-Reset": {ms(time.Hour)}}, 1, 31 * time.Second},
		// The request budget is empty: 90 seconds to its reset, plus two.
		{"no requests left waits for the request reset", http.Header{"X-Ratelimit-Requests-Remaining": {"0"}, "X-Ratelimit-Requests-Reset": {ms(90 * time.Second)}, "X-Ratelimit-Complexity-Reset": {ms(time.Hour)}}, 1, 92 * time.Second},
		// Requests are left, so the complexity budget ran out: 10 minutes, plus two seconds.
		{"requests left waits for the complexity reset", http.Header{"X-Ratelimit-Requests-Remaining": {"12"}, "X-Ratelimit-Requests-Reset": {ms(time.Hour)}, "X-Ratelimit-Complexity-Reset": {ms(10 * time.Minute)}}, 1, 10*time.Minute + 2*time.Second},
		{"a reset in seconds is read as seconds", http.Header{"X-Ratelimit-Complexity-Reset": {strconv.FormatInt(now.Add(40*time.Second).Unix(), 10)}}, 1, 42 * time.Second},
		{"a reset in the past still waits a little", http.Header{"X-Ratelimit-Complexity-Reset": {ms(-time.Hour)}}, 1, 2 * time.Second},
		{"no header backs off by attempt", http.Header{}, 3, 3 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rateLimitWait(c.h, c.attempt, now); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestQueryWaitsOutARateLimit(t *testing.T) {
	n := 0
	c, slept := fake(t, func(w http.ResponseWriter, _ *http.Request) {
		n++
		switch n {
		case 1:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED"}}]}`)
		case 2:
			// The same answer with status 400 and the code alone.
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"message":"Rate limit exceeded","extensions":{"code":"RATELIMITED"}}]}`)
		default:
			fmt.Fprint(w, `{"data":{"organization":{"urlKey":"acme"}}}`)
		}
	})
	var lines []string
	c.Logf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	var out org
	if err := c.Query(context.Background(), orgQuery, nil, &out); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 2 || (*slept)[0] != 8*time.Second || (*slept)[1] != 4*time.Second {
		t.Errorf("slept %v, want 8s then 4s", *slept)
	}
	if c.Calls != 3 || out.Organization.URLKey != "acme" {
		t.Errorf("calls = %d, urlKey = %q", c.Calls, out.Organization.URLKey)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "waiting 8s") {
		t.Errorf("log = %v", lines)
	}
}

func TestQueryGivesUpOnALongRateLimit(t *testing.T) {
	c, slept := fake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3000")
		w.WriteHeader(429)
	})
	err := c.Query(context.Background(), orgQuery, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "resets in 50m1s") {
		t.Fatalf("error = %v", err)
	}
	if len(*slept) != 0 || c.Calls != 1 {
		t.Errorf("slept %v, calls %d", *slept, c.Calls)
	}
}

func TestQueryStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := fake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
	c.sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	if err := c.Query(ctx, orgQuery, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if c.Calls != 1 {
		t.Errorf("calls = %d, want 1", c.Calls)
	}
}

func TestQueryRetriesAServerErrorThenAnswers(t *testing.T) {
	n := 0
	c, slept := fake(t, func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"data":{"organization":{"urlKey":"acme"}}}`)
	})
	var out org
	if err := c.Query(context.Background(), orgQuery, nil, &out); err != nil {
		t.Fatal(err)
	}
	// 2 seconds after the first failure, 4 after the second.
	if len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] != 4*time.Second {
		t.Errorf("slept %v", *slept)
	}
}
