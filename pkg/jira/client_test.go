package jira

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	email  = "ada@example.com"
	secret = "jira_api_token_that_must_not_leak"
)

// fake returns a client pointed at a local server, and the waits it was asked
// to sleep. It never sleeps for real.
func fake(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, email, secret)
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	c.jitter = func() float64 { return 1 }
	return c, &slept
}

type me struct {
	AccountID string `json:"accountId"`
}

func noSecrets(t *testing.T, text string) {
	t.Helper()
	for _, s := range []string{secret, email, base64.StdEncoding.EncodeToString([]byte(email + ":" + secret))} {
		if strings.Contains(text, s) {
			t.Errorf("%q shows %q", text, s)
		}
	}
}

func TestGetSendsBasicAuthAndDecodes(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if u, p, ok := r.BasicAuth(); !ok || u != email || p != secret {
			t.Errorf("basic auth = %q, %v", u, ok)
		}
		if r.URL.Path != "/rest/api/3/search/jql" || r.URL.Query().Get("jql") != `project in ("ENG") AND updated >= "2026-08-01"` {
			t.Errorf("asked for %s", r.URL)
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") != "factorymeter" {
			t.Errorf("headers = %v", r.Header)
		}
		if r.ContentLength > 0 {
			t.Errorf("a GET carried a body")
		}
		fmt.Fprint(w, `{"accountId":"abc"}`)
	})
	var out me
	q := url.Values{"jql": {`project in ("ENG") AND updated >= "2026-08-01"`}}
	if err := c.Get(context.Background(), "/rest/api/3/search/jql", q, &out); err != nil {
		t.Fatal(err)
	}
	if out.AccountID != "abc" || c.Calls != 1 {
		t.Errorf("out = %+v, calls = %d", out, c.Calls)
	}
}

func TestGetDoesNotDecodeAnAnswerNobodyReads(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not json") })
	if err := c.Get(context.Background(), "/rest/api/3/myself", nil, nil); err != nil {
		t.Error(err)
	}
}

func TestGetErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		is     error
		want   string
		calls  int
	}{
		{"a wrong token", 401, `{"errorMessages":["Client must be authenticated"]}`, nil, "jira rejected the email and the API token (401). Set JIRA_EMAIL and JIRA_API_TOKEN", 1},
		{"not allowed", 403, `{"errorMessages":["You may not browse users.","second"],"errors":{}}`, ErrForbidden, "jira refused the request (403): You may not browse users.", 1},
		{"not there", 404, `{"errorMessages":["Issue does not exist"]}`, ErrNotFound, "jira did not find it (404): Issue does not exist", 1},
		{"a bad search", 400, `{"errorMessages":[],"errors":{"jql":"The value 'NOPE' does not exist for the field 'project'.","a":"first by name"}}`, nil, "jira refused the request (400): a: first by name", 1},
		{"an error that is not JSON", 400, "<html>no</html>", nil, "jira refused the request (400): <html>no</html>", 1},
		{"an answer that is not JSON", 200, "<html>", ErrServer, "decode response", 1},
		{"a server that stays down", 503, "", ErrServer, "gave up after 5 attempts: jira server error: status 503", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			var out me
			err := c.Get(context.Background(), "/rest/api/3/myself", nil, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to hold %q", err, tc.want)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Errorf("error = %v, want %v", err, tc.is)
			}
			for _, other := range []error{ErrServer, ErrNotFound, ErrForbidden} {
				if other != tc.is && errors.Is(err, other) {
					t.Errorf("error = %v, and it is also %v", err, other)
				}
			}
			if c.Calls != tc.calls {
				t.Errorf("calls = %d, want %d", c.Calls, tc.calls)
			}
			if tc.calls == 5 {
				want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}
				if !reflect.DeepEqual(*slept, want) {
					t.Errorf("slept %v", *slept)
				}
			}
			noSecrets(t, err.Error())
		})
	}
}

func TestMessageIsCut(t *testing.T) {
	long := strings.Repeat("x", 500)
	if got := message([]byte(`{"errorMessages":["` + long + `"]}`)); len(got) != 200 {
		t.Errorf("message has %d characters", len(got))
	}
	// An address with no site behind it answers with one message.
	if got := message([]byte(`{"errorMessage": "Site temporarily unavailable", "errorCode": "OTHER"}`)); got != "Site temporarily unavailable" {
		t.Errorf("message = %q", got)
	}
	if got := message([]byte("  plain  ")); got != "plain" {
		t.Errorf("message = %q", got)
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	cases := []struct {
		name    string
		h       http.Header
		attempt int
		jitter  float64
		want    time.Duration
	}{
		{"retry-after in seconds, and one more", h("Retry-After", "30"), 1, 1, 31 * time.Second},
		{"retry-after of zero", h("Retry-After", " 0 "), 1, 1, time.Second},
		{"retry-after wins over the reset time", h("Retry-After", "5", "X-RateLimit-Reset", "2026-11-02T12:10:00Z"), 1, 1, 6 * time.Second},
		{"the reset time", h("X-RateLimit-Reset", "2026-11-02T12:10:00Z"), 1, 1, 10*time.Minute + time.Second},
		{"the reset time without seconds", h("X-RateLimit-Reset", "2026-11-02T12:10Z"), 1, 1, 10*time.Minute + time.Second},
		{"the reset time with an offset", h("X-RateLimit-Reset", "2026-11-02T14:10:00+02:00"), 1, 1, 10*time.Minute + time.Second},
		{"a reset time that has passed", h("X-RateLimit-Reset", "2026-11-02T11:00:00Z"), 1, 1, time.Second},
		{"a negative retry-after falls to the reset time", h("Retry-After", "-4", "X-RateLimit-Reset", "2026-11-02T12:00:09Z"), 1, 1, 10 * time.Second},
		{"neither, first try", h(), 1, 1, 2 * time.Second},
		{"neither, third try", h(), 3, 1, 8 * time.Second},
		{"neither, spread down", h(), 2, 0.75, 3 * time.Second},
		{"headers that cannot be read", h("Retry-After", "soon", "X-RateLimit-Reset", "later"), 2, 1.25, 5 * time.Second},
	}
	for _, tc := range cases {
		if got := rateLimitWait(tc.h, tc.attempt, now, tc.jitter); got != tc.want {
			t.Errorf("%s: wait = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestTheSpreadOfAWaitStaysNearIt(t *testing.T) {
	c, err := New("acme.atlassian.net", email, secret)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if j := c.jitter(); j < 0.7 || j >= 1.3 {
			t.Fatalf("jitter = %v", j)
		}
	}
	if c.MaxWait != 20*time.Minute || c.HTTP.Timeout != 90*time.Second {
		t.Errorf("max wait = %s, timeout = %s", c.MaxWait, c.HTTP.Timeout)
	}
}

func TestGetWaitsOutARateLimit(t *testing.T) {
	var n atomic.Int64
	var lines []string
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"accountId":"abc"}`)
	})
	c.Logf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	var out me
	if err := c.Get(context.Background(), "/rest/api/3/myself", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.AccountID != "abc" || c.Calls != 3 {
		t.Errorf("out = %+v, calls = %d", out, c.Calls)
	}
	if want := []time.Duration{8 * time.Second, 8 * time.Second}; !reflect.DeepEqual(*slept, want) {
		t.Errorf("slept %v", *slept)
	}
	if len(lines) != 2 || lines[0] != "jira rate limit reached, retrying in 8s" {
		t.Errorf("log = %v", lines)
	}
}

func TestGetGivesUpOnALongRateLimit(t *testing.T) {
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	err := c.Get(context.Background(), "/rest/api/3/myself", nil, nil)
	if err == nil || err.Error() != "jira rate limit reached. It resets in 1h0m1s. Run again then. What was read is stored" {
		t.Fatalf("error = %v", err)
	}
	if len(*slept) != 0 || c.Calls != 1 {
		t.Errorf("slept %v, calls = %d", *slept, c.Calls)
	}
	// A wait of just the limit is waited out.
	c.MaxWait = time.Hour + time.Second
	err = c.Get(context.Background(), "/rest/api/3/myself", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "gave up after 5 attempts: jira rate limit reached") || len(*slept) != 5 {
		t.Errorf("error = %v, slept %v", err, *slept)
	}
}

func TestGetStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	c.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	if err := c.Get(ctx, "/rest/api/3/myself", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if c.Calls != 1 {
		t.Errorf("calls = %d", c.Calls)
	}
	// A context that is done before the request leaves is not a server error.
	if err := c.Get(ctx, "/rest/api/3/myself", nil, nil); !errors.Is(err, context.Canceled) || errors.Is(err, ErrServer) {
		t.Errorf("error = %v", err)
	}
}

func TestSleepStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Error(err)
	}
}

func TestGetRetriesAServerErrorThenAnswers(t *testing.T) {
	var n atomic.Int64
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"accountId":"abc"}`)
	})
	var out me
	if err := c.Get(context.Background(), "/rest/api/3/myself", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.AccountID != "abc" || c.Calls != 2 || !reflect.DeepEqual(*slept, []time.Duration{2 * time.Second}) {
		t.Errorf("out = %+v, calls = %d, slept %v", out, c.Calls, *slept)
	}
}

func TestGetRetriesAConnectionThatFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := New(srv.URL, email, secret)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	err = c.Get(context.Background(), "/rest/api/3/myself", nil, nil)
	if !errors.Is(err, ErrServer) || c.Calls != 5 || len(slept) != 5 {
		t.Fatalf("error = %v, calls = %d, slept %v", err, c.Calls, slept)
	}
	noSecrets(t, err.Error())
}

// The email and the token go to the host that was named and to no other. A
// client that followed a redirect would hand them to wherever it points.
func TestARedirectIsNotFollowed(t *testing.T) {
	var elsewhere atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		fmt.Fprint(w, `{"accountId":"stolen"}`)
	}))
	defer other.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/rest/api/3/myself?secret=path", status)
		})
		var out me
		err := c.Get(context.Background(), "/rest/api/3/myself", nil, &out)
		host := strings.TrimPrefix(other.URL, "http://")
		want := fmt.Sprintf("answered with a redirect (%d) to %s, and a redirect is not followed", status, host)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%d: error = %v", status, err)
		}
		if strings.Contains(err.Error(), "secret=path") || out.AccountID != "" || c.Calls != 1 || len(*slept) != 0 {
			t.Errorf("%d: error = %v, out = %+v, calls = %d", status, err, out, c.Calls)
		}
		noSecrets(t, err.Error())
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the other host got %d requests", n)
	}
	// A redirect that names no host is still an error, and says so plainly.
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	})
	err := c.Get(context.Background(), "/rest/api/3/myself", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "answered with a redirect (302), and a redirect is not followed") {
		t.Errorf("error = %v", err)
	}
}

func TestNewReadsTheSite(t *testing.T) {
	good := []struct{ in, site, browse string }{
		{"acme.atlassian.net", "acme.atlassian.net", "https://acme.atlassian.net/browse/ENG-1"},
		{"  https://Acme.Atlassian.net/  ", "acme.atlassian.net", "https://acme.atlassian.net/browse/ENG-1"},
		{"https://acme.atlassian.net", "acme.atlassian.net", "https://acme.atlassian.net/browse/ENG-1"},
		{"jira.example.com:8443", "jira.example.com", "https://jira.example.com:8443/browse/ENG-1"},
		{"http://127.0.0.1:8080", "127.0.0.1", "http://127.0.0.1:8080/browse/ENG-1"},
		{"http://localhost:8080/", "localhost", "http://localhost:8080/browse/ENG-1"},
		{"http://[::1]:8080", "::1", "http://[::1]:8080/browse/ENG-1"},
	}
	for _, tc := range good {
		c, err := New(tc.in, email, secret)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if c.Site() != tc.site || c.Browse("ENG-1") != tc.browse {
			t.Errorf("%q: site = %q, browse = %q", tc.in, c.Site(), c.Browse("ENG-1"))
		}
	}
	bad := []struct{ in, want string }{
		{"", "no Jira site"},
		{"   ", "no Jira site"},
		{"https://", "cannot be read as a host"},
		{"https://acme.atlassian.net:port", "cannot be read as a host"},
		{"https://ada:hunter2secret@acme.atlassian.net", "must not carry a name or a password"},
		{"ada:hunter2secret@acme.atlassian.net", "must not carry a name or a password"},
		{"https://ada@acme.atlassian.net", "must not carry a name or a password"},
		{"https://acme.atlassian.net/jira", "the Jira site acme.atlassian.net must be a host and nothing after it"},
		{"acme.atlassian.net/browse/ENG-1", "must be a host and nothing after it"},
		{"https://acme.atlassian.net?token=hunter2secret", "must be a host and nothing after it"},
		{"https://acme.atlassian.net/#hunter2secret", "must be a host and nothing after it"},
		{"http://acme.atlassian.net", "the Jira site acme.atlassian.net must be asked over https, so the token is not sent in the clear"},
		{"http://10.0.0.5:8080", "must be asked over https"},
		{"http://localhost.example.com", "must be asked over https"},
		{"ftp://acme.atlassian.net", "the Jira site must be asked over https"},
		// A site name alone is not a host. A name that could be a pasted token
		// is not repeated.
		{"acme", "the Jira site acme is not a whole host. Pass the host of the site, such as acme.atlassian.net"},
		{"https://acme-dev", "such as acme-dev.atlassian.net"},
		{"ATATT3xFhunter2secretB7D8", "the Jira site is not a whole host. Pass the host of the site, such as acme.atlassian.net"},
	}
	for _, tc := range bad {
		c, err := New(tc.in, email, secret)
		if err == nil || c != nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error = %v, want it to hold %q", tc.in, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "hunter2secret") || strings.Contains(err.Error(), "ada") {
			t.Errorf("%q: the error repeats what it was given: %v", tc.in, err)
		}
	}
}

func TestBrowseEscapesTheKey(t *testing.T) {
	c, err := New("acme.atlassian.net", email, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Browse("ENG-1/../x?y"); got != "https://acme.atlassian.net/browse/ENG-1%2F..%2Fx%3Fy" {
		t.Errorf("browse = %q", got)
	}
}

// A display that listens gets the wait as a length of time, and the line for
// a log is then not written as well.
func TestAWaitForARateLimitIsToldToADisplay(t *testing.T) {
	var n atomic.Int64
	c, slept := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"accountId":"abc"}`)
	})
	var told []time.Duration
	var lines []string
	c.Waiting = func(d time.Duration) { told = append(told, d) }
	c.Logf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	var out me
	if err := c.Get(context.Background(), "/rest/api/3/myself", nil, &out); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{8 * time.Second}; !reflect.DeepEqual(told, want) || !reflect.DeepEqual(*slept, want) {
		t.Errorf("told %v, slept %v", told, *slept)
	}
	if len(lines) != 0 {
		t.Errorf("a wait that was told was logged too: %q", lines)
	}
}
