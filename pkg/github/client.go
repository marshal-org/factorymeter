// Package github is a small read-only GitHub API client. It sends GET requests
// to the REST API and queries to the GraphQL API, and it never sends a
// mutation. It waits out rate limits and retries server errors.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrServer marks a failure on GitHub's side that a smaller request may avoid,
// such as a timeout on a heavy GraphQL query.
var ErrServer = errors.New("github server error")

// ErrNotFound marks a 404, or a GraphQL NOT_FOUND. A token without access to a
// private repository also gets this.
var ErrNotFound = errors.New("not found")

// Client talks to one GitHub host.
type Client struct {
	Token string
	// BaseURL is the REST root, without a trailing slash.
	BaseURL string
	// GraphQLURL is the GraphQL endpoint.
	GraphQLURL string
	HTTP       *http.Client
	// MaxWait is the longest single rate-limit wait the client accepts before
	// it gives up with an error that names the reset time.
	MaxWait time.Duration
	// Logf receives progress lines, such as rate-limit waits. May be nil.
	Logf func(format string, a ...any)
	// UserAgent is sent with every request.
	UserAgent string

	// Calls counts HTTP requests sent, including retries.
	Calls int

	sleep func(context.Context, time.Duration) error
}

// New returns a client for github.com.
func New(token string) *Client {
	return &Client{
		Token:      token,
		BaseURL:    "https://api.github.com",
		GraphQLURL: "https://api.github.com/graphql",
		HTTP:       &http.Client{Timeout: 90 * time.Second},
		MaxWait:    20 * time.Minute,
		UserAgent:  "factorymeter",
		sleep:      sleepCtx,
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

// do sends one request built by mk and retries on rate limits and server
// errors. It returns the body of a 2xx response.
func (c *Client) do(ctx context.Context, mk func() (*http.Request, error)) ([]byte, http.Header, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := mk()
		if err != nil {
			return nil, nil, err
		}
		req = req.WithContext(ctx)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", c.UserAgent)
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		c.Calls++
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			lastErr = fmt.Errorf("%w: %v", ErrServer, err)
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return nil, nil, err
			}
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			lastErr = fmt.Errorf("%w: read body: %v", ErrServer, rerr)
			continue
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return body, resp.Header, nil
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, nil, errors.New("github rejected the token (401). Set GITHUB_TOKEN to a token that can read the repository")
		case resp.StatusCode == http.StatusNotFound:
			return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, req.URL.Path)
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
			wait, limited := rateLimitWait(resp.Header, body, attempt, time.Now())
			if !limited {
				return nil, nil, fmt.Errorf("github refused %s (403): %s", req.URL.Path, message(body))
			}
			if wait > c.MaxWait {
				return nil, nil, fmt.Errorf("github rate limit reached. It resets in %s. Run again then, or use a token with a higher limit", wait.Round(time.Second))
			}
			c.logf("rate limit reached, waiting %s", wait.Round(time.Second))
			lastErr = fmt.Errorf("rate limited: %s", message(body))
			if err := c.sleep(ctx, wait); err != nil {
				return nil, nil, err
			}
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%w: %d on %s", ErrServer, resp.StatusCode, req.URL.Path)
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("github returned %d on %s: %s", resp.StatusCode, req.URL.Path, message(body))
		}
	}
	return nil, nil, fmt.Errorf("gave up after %d attempts: %w", maxAttempts, lastErr)
}

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * time.Second }

// rateLimitWait decides whether a 403 or 429 is a rate limit and how long to
// wait. GitHub signals a primary limit with X-RateLimit-Remaining: 0 and a
// reset time, and a secondary limit with Retry-After or only a message.
func rateLimitWait(h http.Header, body []byte, attempt int, now time.Time) (time.Duration, bool) {
	if ra := h.Get("Retry-After"); ra != "" {
		if s, err := strconv.Atoi(ra); err == nil {
			return time.Duration(s+1) * time.Second, true
		}
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if ts, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if d := time.Unix(ts, 0).Sub(now) + 2*time.Second; d > 0 {
				return d, true
			}
			return 2 * time.Second, true
		}
	}
	if m := strings.ToLower(message(body)); strings.Contains(m, "rate limit") || strings.Contains(m, "abuse") {
		return time.Duration(attempt) * time.Minute, true
	}
	return 0, false
}

func message(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// REST sends a GET to path under the REST root and decodes the JSON response
// into out. It returns the response headers so the caller can read Link.
func (c *Client) REST(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	body, h, err := c.do(ctx, func() (*http.Request, error) { return http.NewRequest(http.MethodGet, u, nil) })
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return h, nil
}

type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// GraphQL runs a query and decodes the "data" object into out. Mutations are
// refused: this client is read-only by construction.
func (c *Client) GraphQL(ctx context.Context, query string, vars map[string]any, out any) error {
	if strings.HasPrefix(strings.TrimSpace(query), "mutation") {
		return errors.New("factorymeter never sends a GraphQL mutation")
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		body, _, err := c.do(ctx, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPost, c.GraphQLURL, bytes.NewReader(payload))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
			return req, err
		})
		if err != nil {
			return err
		}
		var env struct {
			Data   json.RawMessage `json:"data"`
			Errors []gqlError      `json:"errors"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return fmt.Errorf("%w: decode graphql response: %v", ErrServer, err)
		}
		if len(env.Errors) == 0 {
			return json.Unmarshal(env.Data, out)
		}
		e := env.Errors[0]
		switch e.Type {
		case "RATE_LIMITED":
			if attempt >= maxAttempts {
				return fmt.Errorf("graphql rate limit: %s", e.Message)
			}
			wait := c.graphQLReset(ctx)
			if wait > c.MaxWait {
				return fmt.Errorf("github rate limit reached. It resets in %s. Run again then, or use a token with a higher limit", wait.Round(time.Second))
			}
			c.logf("graphql rate limit reached, waiting %s", wait.Round(time.Second))
			if err := c.sleep(ctx, wait); err != nil {
				return err
			}
		case "NOT_FOUND":
			return fmt.Errorf("%w: %s", ErrNotFound, e.Message)
		case "FORBIDDEN", "INSUFFICIENT_SCOPES":
			return fmt.Errorf("github refused the query: %s", e.Message)
		default:
			// Timeouts and "Something went wrong" arrive as 200 with errors.
			return fmt.Errorf("%w: %s", ErrServer, e.Message)
		}
	}
}

// graphQLReset asks the REST rate limit endpoint, which costs nothing, when the
// GraphQL budget resets.
func (c *Client) graphQLReset(ctx context.Context) time.Duration {
	var rl struct {
		Resources struct {
			GraphQL struct {
				Reset int64 `json:"reset"`
			} `json:"graphql"`
		} `json:"resources"`
	}
	if _, err := c.REST(ctx, "/rate_limit", nil, &rl); err != nil || rl.Resources.GraphQL.Reset == 0 {
		return time.Minute
	}
	if d := time.Until(time.Unix(rl.Resources.GraphQL.Reset, 0)) + 2*time.Second; d > 0 {
		return d
	}
	return 2 * time.Second
}

// NextPage returns the URL of the next page from a Link header, or "".
func NextPage(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		seg := strings.Split(part, ";")
		if len(seg) < 2 || strings.TrimSpace(seg[1]) != `rel="next"` {
			continue
		}
		return strings.Trim(strings.TrimSpace(seg[0]), "<>")
	}
	return ""
}
