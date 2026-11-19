// Package linear is a small read-only client of the Linear GraphQL API. It
// sends queries and never a mutation. It waits out rate limits and retries
// server errors.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrServer marks a failure on Linear's side that a smaller request may
// avoid, such as a timeout on a heavy query.
var ErrServer = errors.New("linear server error")

// ErrTooComplex marks a query that costs more than Linear allows for one
// request. A smaller page costs less.
var ErrTooComplex = errors.New("linear found the query too complex")

// ErrForbidden marks a query the key is not allowed to make.
var ErrForbidden = errors.New("linear refused the query")

// Client talks to one Linear API endpoint.
type Client struct {
	// Key is sent as the Authorization header as it is. A personal API key
	// goes in bare. An OAuth token goes in with its "Bearer " prefix.
	Key string
	// URL is the GraphQL endpoint.
	URL  string
	HTTP *http.Client
	// MaxWait is the longest single rate-limit wait the client accepts before
	// it gives up with an error that names the wait.
	MaxWait time.Duration
	// Logf receives progress lines, such as rate-limit waits. May be nil.
	Logf func(format string, a ...any)
	// UserAgent is sent with every request.
	UserAgent string

	// Calls counts HTTP requests sent, including retries. Read it when no
	// request is in flight.
	Calls int

	mu    sync.Mutex
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

// DefaultURL is the endpoint of linear.app.
const DefaultURL = "https://api.linear.app/graphql"

// New returns a client for linear.app.
func New(key string) *Client {
	return &Client{
		Key:       key,
		URL:       DefaultURL,
		HTTP:      &http.Client{Timeout: 90 * time.Second},
		MaxWait:   20 * time.Minute,
		UserAgent: "factorymeter",
		sleep:     sleepCtx,
		now:       time.Now,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) logf(format string, a ...any) {
	if c.Logf != nil {
		c.Logf(format, a...)
	}
}

const maxAttempts = 5

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * time.Second }

type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// The error codes of Linear this client tells apart.
const (
	codeAuth      = "AUTHENTICATION_ERROR"
	codeLimited   = "RATELIMITED"
	codeForbidden = "FORBIDDEN"
)

// Query runs a query and decodes the "data" object into out. Mutations and
// subscriptions are refused: this client is read-only by construction.
//
// Linear answers a query that partly failed with status 200, an "errors" list
// and what data it has. Such an answer is an error here, so half a page is
// never taken for a whole one.
func (c *Client) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	if q := strings.TrimSpace(query); strings.HasPrefix(q, "mutation") || strings.HasPrefix(q, "subscription") {
		return errors.New("factorymeter never sends a GraphQL mutation")
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", c.UserAgent)
		if c.Key != "" {
			req.Header.Set("Authorization", c.Key)
		}
		c.mu.Lock()
		c.Calls++
		c.mu.Unlock()
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The error of a transport can carry the request. It never carries
			// a header, so the key cannot be in it.
			lastErr = fmt.Errorf("%w: %v", ErrServer, err)
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			lastErr = fmt.Errorf("%w: read body: %v", ErrServer, rerr)
			continue
		}
		var env struct {
			Data   json.RawMessage `json:"data"`
			Errors []gqlError      `json:"errors"`
		}
		decodeErr := json.Unmarshal(body, &env)
		var first gqlError
		if len(env.Errors) > 0 {
			first = env.Errors[0]
		}
		has := func(code string) bool {
			for _, e := range env.Errors {
				if e.Extensions.Code == code {
					return true
				}
			}
			return false
		}
		ok := resp.StatusCode >= 200 && resp.StatusCode < 300

		switch {
		case resp.StatusCode == http.StatusUnauthorized || has(codeAuth):
			return errors.New("linear rejected the key (401). Set LINEAR_API_KEY to a personal API key that can read the workspace")
		case resp.StatusCode == http.StatusTooManyRequests || has(codeLimited):
			wait := rateLimitWait(resp.Header, attempt, c.now())
			if wait > c.MaxWait {
				return fmt.Errorf("linear rate limit reached. It resets in %s. Run again then. What was read is stored", wait.Round(time.Second))
			}
			c.logf("linear rate limit reached, waiting %s", wait.Round(time.Second))
			lastErr = errors.New("linear rate limit reached")
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%w: status %d", ErrServer, resp.StatusCode)
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
		case len(env.Errors) > 0 && tooComplex(env.Errors):
			return fmt.Errorf("%w: %s", ErrTooComplex, first.Message)
		case has(codeForbidden):
			return fmt.Errorf("%w: %s", ErrForbidden, first.Message)
		case len(env.Errors) > 0 && !ok:
			return fmt.Errorf("linear refused the query (%d): %s", resp.StatusCode, first.Message)
		case len(env.Errors) > 0:
			// A timeout inside Linear arrives as 200 with errors.
			return fmt.Errorf("%w: %s", ErrServer, first.Message)
		case !ok:
			return fmt.Errorf("linear returned %d: %s", resp.StatusCode, short(body))
		case decodeErr != nil:
			return fmt.Errorf("%w: decode response: %v", ErrServer, decodeErr)
		default:
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("%w: decode data: %v", ErrServer, err)
			}
			return nil
		}
	}
	return fmt.Errorf("gave up after %d attempts: %w", maxAttempts, lastErr)
}

func tooComplex(errs []gqlError) bool {
	for _, e := range errs {
		if strings.Contains(strings.ToLower(e.Message), "too complex") {
			return true
		}
	}
	return false
}

// rateLimitWait says how long to wait after a rate-limited answer. Linear
// sends Retry-After in seconds. Without it, the reset time of the budget that
// ran out is used: the request budget when none is left, else the complexity
// budget. Linear sends reset times in milliseconds since 1970.
func rateLimitWait(h http.Header, attempt int, now time.Time) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		return time.Duration(s+1) * time.Second
	}
	name := "X-RateLimit-Complexity-Reset"
	if h.Get("X-RateLimit-Requests-Remaining") == "0" {
		name = "X-RateLimit-Requests-Reset"
	}
	if ts, err := strconv.ParseInt(strings.TrimSpace(h.Get(name)), 10, 64); err == nil && ts > 0 {
		at := time.Unix(ts, 0)
		if ts > 1e11 {
			at = time.UnixMilli(ts)
		}
		if d := at.Sub(now) + 2*time.Second; d > 2*time.Second {
			return d
		}
		return 2 * time.Second
	}
	return time.Duration(attempt) * time.Minute
}

func short(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
