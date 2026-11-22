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
	// From, when set, starts the window on that day; the window then runs
	// over whole weeks back from its end, and Weeks is not read.
	From time.Time
	// Repos narrows an owner to some of its repositories. Empty means
	// every one.
	Repos []string
}

// Source builds the reports the server shows.
type Source interface {
	// Report builds the report a query asks for.
	Report(q Query) (*contract.Report, error)
	// Repos lists the repositories a query may name. It is empty when the
	// target is one repository, and when names must stay out of the page.
	Repos() ([]string, error)
	// Drill lists the items one number of the report counted, over the
	// window, or over one week of its charts when week is 0 or more.
	Drill(q Query, metric string, week int) (*view.DrillPage, error)
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
	h.Set("Content-Security-Policy", view.ServedCSP()+"; frame-ancestors 'none'")
	if !s.local(r.Host) {
		fail(w, http.StatusForbidden, "This server answers only under localhost. To reach it under another name, start it with --allow-host and that name.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		fail(w, http.StatusMethodNotAllowed, "This server only reads. Use GET.")
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/report.json" && r.URL.Path != "/items" {
		fail(w, http.StatusNotFound, "There is no such page. The report is at / and at /report.json.")
		return
	}
	repos, err := s.src.Repos()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	v := r.URL.Query()
	var metric string
	week := -1
	if r.URL.Path == "/items" {
		metric = v.Get("metric")
		if metric == "" {
			fail(w, http.StatusBadRequest, "Name the number with metric=, such as metric=review.wait.p50.")
			return
		}
		if s := v.Get("week"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n > maxWeeks {
				fail(w, http.StatusBadRequest, fmt.Sprintf("cannot read week=%q. Use the index of a week of the charts, from 0", s))
				return
			}
			week = n
		}
		v.Del("metric")
		v.Del("week")
	}
	q, err := parse(v, repos)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.URL.Path == "/items" {
		s.items(w, r, q, metric, week)
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
		err = view.HTML(&buf, rep, view.HTMLOptions{Nav: nav(q, repos, rep)})
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

// items answers with the page of the items behind one number.
func (s *server) items(w http.ResponseWriter, r *http.Request, q Query, metric string, week int) {
	page, err := s.src.Drill(q, metric, week)
	switch {
	case errors.Is(err, ErrNoData):
		fail(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, ErrBadQuery):
		fail(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if page.Back == "" {
		page.Back = link("/", q)
	}
	var buf bytes.Buffer
	if err := view.WriteDrill(&buf, page); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

// ErrBadQuery is the error a Source wraps for a number or week that is not
// there.
var ErrBadQuery = errors.New("bad query")

// Link is the address of the page for a query, for a Source that writes
// links back to the line.
func Link(path string, q Query) string { return link(path, q) }

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
		if k != "since" && k != "until" && k != "repo" && k != "from" {
			return q, fmt.Errorf("unknown parameter %q. Use since, from, until and repo", k)
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
	if s := v.Get("from"); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return q, fmt.Errorf("cannot read from=%q. Use a date such as 2026-01-31", s)
		}
		if !q.Until.IsZero() && !t.Before(q.Until) {
			return q, fmt.Errorf("from=%s is not before the end of the window", s)
		}
		if !q.Until.IsZero() && q.Until.Sub(t) > maxWeeks*7*24*time.Hour {
			return q, fmt.Errorf("from=%s reaches back more than %d weeks", s, maxWeeks)
		}
		q.From = t
	}
	for _, s := range v["repo"] {
		if s == "" || slices.Contains(q.Repos, s) {
			continue
		}
		if !slices.Contains(repos, s) {
			return q, fmt.Errorf("the store holds no repository %q", s)
		}
		q.Repos = append(q.Repos, s)
	}
	// Every repository ticked is the whole owner.
	if len(q.Repos) == len(repos) {
		q.Repos = nil
	}
	slices.Sort(q.Repos)
	return q, nil
}

// link is the address of the page for a query, with the defaults left out.
func link(path string, q Query) string {
	v := url.Values{}
	if !q.From.IsZero() {
		v.Set("from", q.From.Format("2006-01-02"))
	} else if q.Weeks != defaultWeeks {
		v.Set("since", strconv.Itoa(q.Weeks)+"w")
	}
	if !q.Until.IsZero() {
		v.Set("until", q.Until.Add(-24*time.Hour).Format("2006-01-02"))
	}
	for _, r := range q.Repos {
		v.Add("repo", r)
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

func nav(q Query, repos []string, rep *contract.Report) *view.Nav {
	n := &view.Nav{JSON: link("/report.json", q), KeepRepos: q.Repos}
	// The items behind a number: the address of the page with its settings,
	// to which the page adds the metric.
	n.Drill = link("/items", q)
	if rep != nil && !rep.Window.Since.IsZero() {
		n.From = rep.Window.Since.UTC().Format("2006-01-02")
		// The window ends at a moment; the form names the day it is in.
		n.Until = rep.Window.Until.UTC().Add(-time.Second).Format("2006-01-02")
	}
	weeks := windows
	if !slices.Contains(weeks, q.Weeks) {
		weeks = append(slices.Clone(weeks), q.Weeks)
		slices.Sort(weeks)
	}
	for _, w := range weeks {
		to := q
		to.Weeks, to.From = w, time.Time{}
		n.Windows = append(n.Windows, view.Link{Label: strconv.Itoa(w) + " weeks", URL: link("/", to), Current: q.From.IsZero() && w == q.Weeks})
	}
	if len(repos) > 1 {
		to := q
		to.Repos = nil
		n.AllRepos = link("/", to)
		for _, r := range repos {
			// The address is of the repository alone, for the rows of the
			// page that name it.
			to.Repos = []string{r}
			n.Repos = append(n.Repos, view.Link{Label: r, URL: link("/", to), Current: len(q.Repos) == 0 || slices.Contains(q.Repos, r)})
		}
		switch len(q.Repos) {
		case 0:
			n.RepoLabel = "All repositories"
		case 1:
			n.RepoLabel = q.Repos[0]
		default:
			n.RepoLabel = fmt.Sprintf("%d of %d repositories", len(q.Repos), len(repos))
		}
		// The form sends the window again, so a pick keeps it.
		if !q.From.IsZero() {
			n.Keep = append(n.Keep, view.Field{Name: "from", Value: q.From.Format("2006-01-02")})
		} else if q.Weeks != defaultWeeks {
			n.Keep = append(n.Keep, view.Field{Name: "since", Value: strconv.Itoa(q.Weeks) + "w"})
		}
		if !q.Until.IsZero() {
			n.Keep = append(n.Keep, view.Field{Name: "until", Value: q.Until.Add(-24 * time.Hour).Format("2006-01-02")})
		}
	}
	return n
}
