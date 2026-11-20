// Package jira is a small read-only client of the Jira Cloud REST API. It
// sends GET requests and nothing else. It waits out rate limits and retries
// server errors.
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrServer marks a failure on Jira's side that a smaller request may avoid,
// such as a timeout on a heavy search.
var ErrServer = errors.New("jira server error")

// ErrNotFound marks a thing Jira does not have, or does not show the account.
var ErrNotFound = errors.New("jira did not find it")

// ErrForbidden marks a request the account is not allowed to make.
var ErrForbidden = errors.New("jira refused the request")

// Client talks to one Jira Cloud site.
type Client struct {
	// Email and Token are the account and its API token. They are sent as
	// HTTP basic authentication, to the site and to no other host.
	Email string
	Token string
	HTTP  *http.Client
	// MaxWait is the longest single rate-limit wait the client accepts before
	// it gives up with an error that names the wait.
	MaxWait time.Duration
	// Logf receives progress lines, such as rate-limit waits. May be nil.
	Logf func(format string, a ...any)
	// Waiting is told how long the client is about to wait for a rate limit.
	// A display that sets it shows the wait itself, so the wait is then not
	// sent to Logf as well. May be nil.
	Waiting func(time.Duration)
	// UserAgent is sent with every request.
	UserAgent string

	// Calls counts HTTP requests sent, including retries. Read it when no
	// request is in flight.
	Calls int

	// Sleep waits between tries. A test replaces it.
	Sleep func(context.Context, time.Duration) error

	base   *url.URL
	mu     sync.Mutex
	now    func() time.Time
	jitter func() float64
}

// New returns a client for a site, such as acme.atlassian.net. The site is
// asked over https. Plain http is only accepted for this machine, so the
// token never crosses a network in the clear.
func New(site, email, token string) (*Client, error) {
	base, err := parseSite(site)
	if err != nil {
		return nil, err
	}
	return &Client{
		Email: email,
		Token: token,
		// A redirect is not followed. The email and the token go to the host
		// that was named and to no other.
		HTTP: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		MaxWait:   20 * time.Minute,
		UserAgent: "factorymeter",
		base:      base,
		Sleep:     sleepCtx,
		now:       time.Now,
		jitter:    func() float64 { return 0.7 + 0.6*rand.Float64() },
	}, nil
}

// parseSite reads the host of a site. It never repeats what it was given in
// an error, in case a password was pasted into it.
func parseSite(site string) (*url.URL, error) {
	s := strings.TrimSpace(site)
	if s == "" {
		return nil, errors.New("no Jira site. Pass the host of the site, such as acme.atlassian.net")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Hostname() == "":
		return nil, errors.New("the Jira site cannot be read as a host. Pass the host of the site, such as acme.atlassian.net")
	case u.User != nil:
		return nil, errors.New("the Jira site must not carry a name or a password. Set JIRA_EMAIL and JIRA_API_TOKEN")
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("the Jira site %s must be a host and nothing after it, such as acme.atlassian.net", u.Host)
	case u.Scheme == "http" && !loopback(u.Hostname()):
		return nil, fmt.Errorf("the Jira site %s must be asked over https, so the token is not sent in the clear", u.Host)
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, errors.New("the Jira site must be asked over https")
	case !strings.Contains(u.Hostname(), ".") && !loopback(u.Hostname()):
		// A name alone, such as acme, is not a host on the internet. It is
		// most often the name of a site on atlassian.net. The name is only
		// repeated when it looks like one, and not like a pasted token.
		if siteName.MatchString(u.Hostname()) {
			return nil, fmt.Errorf("the Jira site %s is not a whole host. Pass the host of the site, such as %s.atlassian.net", u.Hostname(), u.Hostname())
		}
		return nil, errors.New("the Jira site is not a whole host. Pass the host of the site, such as acme.atlassian.net")
	}
	return &url.URL{Scheme: u.Scheme, Host: strings.ToLower(u.Host)}, nil
}

var siteName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Site is the host of the site, without a port and in lower case.
func (c *Client) Site() string { return c.base.Hostname() }

// Browse is the address of an issue for a person.
func (c *Client) Browse(key string) string {
	return c.base.Scheme + "://" + c.base.Host + "/browse/" + url.PathEscape(key)
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

// Requests is the number of HTTP requests sent so far. Unlike Calls it is
// safe to read while requests are in flight.
func (c *Client) Requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Calls
}

func (c *Client) logf(format string, a ...any) {
	if c.Logf != nil {
		c.Logf(format, a...)
	}
}

// pause tells that the client waits for a rate limit: to Waiting when a
// display listens, else as a line to Logf.
func (c *Client) pause(what string, wait time.Duration) {
	if c.Waiting != nil {
		c.Waiting(wait)
		return
	}
	c.logf("%s, waiting %s", what, wait.Round(time.Second))
}

const maxAttempts = 5

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * time.Second }

// Get sends a GET request to a path of the site and decodes the JSON answer
// into out. The client has no other verb: it is read-only by construction.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	u := *c.base
	u.Path, u.RawQuery = path, query.Encode()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.UserAgent)
		req.SetBasicAuth(c.Email, c.Token)
		c.mu.Lock()
		c.Calls++
		c.mu.Unlock()
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The error of a transport can carry the address. It never
			// carries a header, so the token cannot be in it.
			lastErr = fmt.Errorf("%w: %v", ErrServer, err)
			if err := c.Sleep(ctx, backoff(attempt)); err != nil {
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
		switch code := resp.StatusCode; {
		case code == http.StatusUnauthorized:
			return errors.New("jira rejected the email and the API token (401). Set JIRA_EMAIL and JIRA_API_TOKEN to an account that can read the site")
		case code == http.StatusTooManyRequests:
			wait := rateLimitWait(resp.Header, attempt, c.now(), c.jitter())
			if wait > c.MaxWait {
				return fmt.Errorf("jira rate limit reached. It resets in %s. Run again then. What was read is stored", wait.Round(time.Second))
			}
			c.pause("jira rate limit reached", wait)
			lastErr = errors.New("jira rate limit reached")
			if err := c.Sleep(ctx, wait); err != nil {
				return err
			}
		case code >= 500:
			lastErr = fmt.Errorf("%w: status %d", ErrServer, code)
			if err := c.Sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
		case code >= 300 && code < 400:
			to := ""
			if l, err := url.Parse(resp.Header.Get("Location")); err == nil && l.Host != "" {
				to = " to " + l.Host
			}
			return fmt.Errorf("the Jira site %s answered with a redirect (%d)%s, and a redirect is not followed. Pass the host the site lives at", c.base.Host, code, to)
		case code == http.StatusForbidden:
			return fmt.Errorf("%w (403): %s", ErrForbidden, message(body))
		case code == http.StatusNotFound:
			return fmt.Errorf("%w (404): %s", ErrNotFound, message(body))
		case code < 200 || code >= 300:
			return fmt.Errorf("jira refused the request (%d): %s", code, message(body))
		default:
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("%w: decode response: %v", ErrServer, err)
			}
			return nil
		}
	}
	return fmt.Errorf("gave up after %d attempts: %w", maxAttempts, lastErr)
}

// message is what Jira says went wrong: the first message of its error list,
// else the start of the body.
func message(body []byte) string {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		// An address with no site behind it answers with one message.
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(body, &e) == nil {
		if len(e.ErrorMessages) > 0 {
			return short(e.ErrorMessages[0])
		}
		if e.ErrorMessage != "" {
			return short(e.ErrorMessage)
		}
		keys := make([]string, 0, len(e.Errors))
		for k := range e.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			return short(keys[0] + ": " + e.Errors[keys[0]])
		}
	}
	return short(string(body))
}

// rateLimitWait says how long to wait after a rate-limited answer. Jira sends
// Retry-After in seconds, and X-RateLimit-Reset as a time. Without either the
// wait doubles from two seconds, spread a little so that many clients do not
// come back at once.
func rateLimitWait(h http.Header, attempt int, now time.Time, jitter float64) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		return time.Duration(s+1) * time.Second
	}
	// The time comes with or without seconds.
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00"} {
		at, err := time.Parse(layout, strings.TrimSpace(h.Get("X-RateLimit-Reset")))
		if err != nil {
			continue
		}
		if d := at.Sub(now) + time.Second; d > time.Second {
			return d
		}
		return time.Second
	}
	return time.Duration(float64(backoff(attempt)) * jitter)
}

func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
