// Package web serves a factorymeter/v1 report as a local page. It reads what a
// Source hands it and nothing else: it holds no token, calls no host, and
// answers only requests that name this machine.
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/view"
)

// The windows a page offers, in weeks, and the longest one a request may ask.
var windows = []int{4, 13, 26}

const (
	defaultWeeks = 13
	maxWeeks     = 104
)

// Query is what a request asks for.
type Query struct {
	// Weeks is the length of the window.
	Weeks int
	// Until ends the window. Zero means the last sync.
	Until time.Time
	// Repo narrows an owner to one repository. Empty means every one.
	Repo string
}

// Source builds the reports the server shows.
type Source interface {
	// Report builds the report a query asks for.
	Report(q Query) (*contract.Report, error)
	// Repos lists the repositories a query may name. It is empty when the
	// target is one repository, and when names must stay out of the page.
	Repos() ([]string, error)
}

// ErrNoData is the error a Source wraps when it holds nothing to report.
var ErrNoData = errors.New("nothing to report")

// Options sets who the server answers.
type Options struct {
	// Hosts are names, besides localhost and the loopback addresses, that a
	// request may carry in its Host header.
	Hosts []string
}

// Handler serves the page at / and the same report as JSON at /report.json.
func Handler(src Source, opt Options) http.Handler {
	return &server{src: src, hosts: opt.Hosts}
}

type server struct {
	src   Source
	hosts []string
}

// local says whether a Host header names this machine. A page in a browser
// can make it send requests to any address, so a request under another name
// is turned away: that is what stops a site from reading the line through a
// DNS name it points at 127.0.0.1.
func (s *server) local(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || slices.Contains(s.hosts, host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", view.CSP()+"; frame-ancestors 'none'")
	if !s.local(r.Host) {
		fail(w, http.StatusForbidden, "This server answers only under localhost. To reach it under another name, start it with --allow-host and that name.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		fail(w, http.StatusMethodNotAllowed, "This server only reads. Use GET.")
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/report.json" {
		fail(w, http.StatusNotFound, "There is no such page. The report is at / and at /report.json.")
		return
	}
	repos, err := s.src.Repos()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	q, err := parse(r.URL.Query(), repos)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := s.src.Report(q)
	switch {
	case errors.Is(err, ErrNoData):
		fail(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The answer is built whole before the first byte goes out, so a failure
	// never leaves half a page behind a 200.
	var buf bytes.Buffer
	if r.URL.Path == "/report.json" {
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		err = enc.Encode(rep)
		h.Set("Content-Type", "application/json; charset=utf-8")
	} else {
		err = view.HTML(&buf, rep, view.HTMLOptions{Nav: nav(q, repos)})
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// parse reads a query and turns away what it does not know, so that nothing
// a request carries reaches the report unread.
func parse(v url.Values, repos []string) (Query, error) {
	q := Query{Weeks: defaultWeeks}
	for k := range v {
		if k != "since" && k != "until" && k != "repo" {
			return q, fmt.Errorf("unknown parameter %q. Use since, until and repo", k)
		}
	}
	if s := v.Get("since"); s != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil || !strings.HasSuffix(s, "w") || n < 1 || n > maxWeeks {
			return q, fmt.Errorf("cannot read since=%q. Use whole weeks from 1w to %dw, such as 13w", s, maxWeeks)
		}
		q.Weeks = n
	}
	if s := v.Get("until"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return q, fmt.Errorf("cannot read until=%q. Use a date such as 2026-03-31", s)
		}
		// The window takes in the whole of the day it names.
		q.Until = t.Add(24 * time.Hour)
	}
	if s := v.Get("repo"); s != "" {
		if !slices.Contains(repos, s) {
			return q, fmt.Errorf("the store holds no repository %q", s)
		}
		q.Repo = s
	}
	return q, nil
}

// link is the address of the page for a query, with the defaults left out.
func link(path string, q Query) string {
	v := url.Values{}
	if q.Weeks != defaultWeeks {
		v.Set("since", strconv.Itoa(q.Weeks)+"w")
	}
	if !q.Until.IsZero() {
		v.Set("until", q.Until.Add(-24*time.Hour).Format("2006-01-02"))
	}
	if q.Repo != "" {
		v.Set("repo", q.Repo)
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func nav(q Query, repos []string) *view.Nav {
	n := &view.Nav{JSON: link("/report.json", q)}
	weeks := windows
	if !slices.Contains(weeks, q.Weeks) {
		weeks = append(slices.Clone(weeks), q.Weeks)
		slices.Sort(weeks)
	}
	for _, w := range weeks {
		to := q
		to.Weeks = w
		n.Windows = append(n.Windows, view.Link{Label: strconv.Itoa(w) + " weeks", URL: link("/", to), Current: w == q.Weeks})
	}
	if len(repos) > 1 {
		to := q
		to.Repo = ""
		n.Repos = append(n.Repos, view.Link{Label: "All repositories", URL: link("/", to), Current: q.Repo == ""})
		for _, r := range repos {
			to.Repo = r
			n.Repos = append(n.Repos, view.Link{Label: r, URL: link("/", to), Current: r == q.Repo})
		}
	}
	return n
}
