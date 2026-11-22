package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/contract"
)

// fake hands out one small report and remembers what it was asked.
type fake struct {
	repos []string
	err   error
	asked []Query
}

func (f *fake) Repos() ([]string, error) { return f.repos, nil }

func (f *fake) Report(q Query) (*contract.Report, error) {
	f.asked = append(f.asked, q)
	if f.err != nil {
		return nil, f.err
	}
	return &contract.Report{Schema: contract.Version, Target: "acme", Verdict: contract.Verdict{Headline: "Review is the constraint."}}, nil
}

func get(h http.Handler, method, url string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, url, nil))
	return w
}

func TestPage(t *testing.T) {
	src := &fake{}
	h := Handler(src, Options{})

	w := get(h, "GET", "http://localhost:7377/")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("code %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "<!doctype html>") || !strings.Contains(body, "<h1>Review is the constraint.</h1>") {
		t.Errorf("the page: %.200s", body)
	}
	if got := w.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length %q for %d bytes", got, len(body))
	}
	// A served page can ask for another window and for the JSON.
	for _, want := range []string{`<a href="/?since=4w">4 weeks</a>`, `<a href="/" class="on" aria-current="true">13 weeks</a>`, `<a href="/?since=26w">26 weeks</a>`,
		`<a href="/report.json">the same report as JSON</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// One repository, or none to name: no menu.
	if strings.Contains(body, `class="menu"`) {
		t.Error("the page offers a repository menu with nothing to choose")
	}
	if len(src.asked) != 1 || src.asked[0].Weeks != 13 || len(src.asked[0].Repos) != 0 || !src.asked[0].Until.IsZero() {
		t.Errorf("asked %+v", src.asked)
	}

	// Every answer carries the headers, the refusals too.
	for _, url := range []string{"http://localhost/", "http://localhost/report.json", "http://localhost/nope", "http://evil.example/"} {
		hd := get(h, "GET", url).Header()
		csp := hd.Get("Content-Security-Policy")
		if !strings.HasPrefix(csp, "default-src 'none'; ") || !strings.HasSuffix(csp, "; frame-ancestors 'none'") {
			t.Errorf("%s: policy %q", url, csp)
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" || hd.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", url, hd)
		}
	}

	w = get(h, "GET", "http://localhost/report.json?since=4w")
	var rep contract.Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || rep.Schema != contract.Version || rep.Target != "acme" {
		t.Errorf("json, err %v: %.200s", err, w.Body.String())
	}
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("code %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}

	// HEAD answers as GET does, with no body.
	w = get(h, "HEAD", "http://localhost/")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("HEAD: code %d, %d bytes, length %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}
}

func TestRefusals(t *testing.T) {
	src := &fake{repos: []string{"acme/app", "acme/lib"}}
	h := Handler(src, Options{})
	cases := []struct {
		method, url string
		code        int
		says        string
	}{
		{"POST", "http://localhost/", 405, "only reads"},
		{"DELETE", "http://localhost/report.json", 405, "only reads"},
		{"GET", "http://localhost/favicon.ico", 404, "no such page"},
		{"GET", "http://localhost/report.json/", 404, "no such page"},
		{"GET", "http://localhost//", 404, "no such page"},
		{"GET", "http://localhost/?since=0w", 400, "whole weeks from 1w to 104w"},
		{"GET", "http://localhost/?since=105w", 400, "cannot read since"},
		{"GET", "http://localhost/?since=13", 400, "cannot read since"},
		{"GET", "http://localhost/?since=-3w", 400, "cannot read since"},
		{"GET", "http://localhost/?since=1.5w", 400, "cannot read since"},
		{"GET", "http://localhost/?until=tomorrow", 400, "cannot read until"},
		{"GET", "http://localhost/?until=2026-13-01", 400, "cannot read until"},
		{"GET", "http://localhost/?repo=acme/none", 400, `the store holds no repository "acme/none"`},
		{"GET", "http://localhost/?repo=ACME/app", 400, "the store holds no repository"},
		{"GET", "http://localhost/?format=xml", 400, `unknown parameter "format"`},
	}
	for _, c := range cases {
		w := get(h, c.method, c.url)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.says) || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s %s = %d %q, want %d and %q", c.method, c.url, w.Code, w.Body.String(), c.code, c.says)
		}
	}
	if got := get(h, "POST", "http://localhost/").Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q", got)
	}
	// Nothing that was turned away reached the source.
	if len(src.asked) != 0 {
		t.Errorf("the source was asked %+v", src.asked)
	}

	// Nothing in the store is a 404 that says what to do. Anything else is a 500.
	src.err = fmt.Errorf("%w: run factorymeter sync", ErrNoData)
	if w := get(h, "GET", "http://localhost/"); w.Code != 404 || !strings.Contains(w.Body.String(), "run factorymeter sync") {
		t.Errorf("no data = %d %q", w.Code, w.Body.String())
	}
	src.err = errors.New("the disk is gone")
	if w := get(h, "GET", "http://localhost/report.json"); w.Code != 500 || !strings.Contains(w.Body.String(), "the disk is gone") {
		t.Errorf("failure = %d %q", w.Code, w.Body.String())
	}
}

// A page in a browser can make it send a request to any address. The Host
// check is what keeps a site from reading the line through a name it points
// at this machine.
func TestHost(t *testing.T) {
	plain := Handler(&fake{}, Options{})
	named := Handler(&fake{}, Options{Hosts: []string{"line.internal", "10.0.0.5", "fe80::1"}})
	cases := []struct {
		h    http.Handler
		host string
		code int
	}{
		{plain, "localhost", 200},
		{plain, "localhost:7377", 200},
		{plain, "LOCALHOST:7377", 200},
		{plain, "127.0.0.1:7377", 200},
		{plain, "127.0.0.2", 200},
		{plain, "[::1]:7377", 200},
		{plain, "evil.example", 403},
		{plain, "evil.example:7377", 403},
		{plain, "localhost.evil.example", 403},
		{plain, "127.0.0.1.evil.example", 403},
		{plain, "line.internal", 403},
		{plain, "10.0.0.5:7377", 403},
		{plain, "", 403},
		{named, "line.internal:7377", 200},
		{named, "Line.Internal", 200},
		{named, "10.0.0.5:7377", 200},
		{named, "[fe80::1]:7377", 200},
		{named, "localhost", 200},
		{named, "other.internal", 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "http://localhost/report.json", nil)
		r.Host = c.host
		w := httptest.NewRecorder()
		c.h.ServeHTTP(w, r)
		if w.Code != c.code {
			t.Errorf("Host %q = %d, want %d", c.host, w.Code, c.code)
		}
		if c.code == 403 && (!strings.Contains(w.Body.String(), "--allow-host") || strings.Contains(w.Body.String(), "factorymeter/v1")) {
			t.Errorf("Host %q was turned away with %q", c.host, w.Body.String())
		}
	}
}

func TestQueryAndLinks(t *testing.T) {
	src := &fake{repos: []string{"acme/app", "acme/lib"}}
	h := Handler(src, Options{})

	w := get(h, "GET", "http://localhost/?since=26w&repo=acme%2Fapp&until=2026-11-01")
	if w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body.String())
	}
	// The window takes in the whole of the day it names.
	want := Query{Weeks: 26, Repos: []string{"acme/app"}, Until: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	if len(src.asked) != 1 || !src.asked[0].Until.Equal(want.Until) || src.asked[0].Weeks != 26 || !slices.Equal(src.asked[0].Repos, want.Repos) {
		t.Errorf("asked %+v, want %+v", src.asked, want)
	}
	body := w.Body.String()
	for _, want := range []string{
		// A window link keeps the repository and the end. The default window
		// is left out of the address.
		`<a href="/?repo=acme%2Fapp&amp;until=2026-11-01">13 weeks</a>`,
		`<a href="/?repo=acme%2Fapp&amp;since=26w&amp;until=2026-11-01" class="on" aria-current="true">26 weeks</a>`,
		// The menu is a form of ticks that sends the window again.
		`<summary>acme/app</summary>`,
		`<form class="menu-list picks" method="get" action="/"><input type="hidden" name="since" value="26w"><input type="hidden" name="until" value="2026-11-01">`,
		`<a class="all" href="/?since=26w&amp;until=2026-11-01">All repositories</a>`,
		`<label><input type="checkbox" name="repo" value="acme/app" checked>acme/app</label>`,
		`<label><input type="checkbox" name="repo" value="acme/lib">acme/lib</label>`,
		`<a href="/report.json?repo=acme%2Fapp&amp;since=26w&amp;until=2026-11-01">the same report as JSON</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// Several repositories ticked: the query names them all, the window
	// links keep them, and the menu says how many.
	src.asked = nil
	body = get(h, "GET", "http://localhost/?repo=acme%2Flib&repo=acme%2Fapp&repo=acme%2Flib").Body.String()
	if len(src.asked) != 1 || len(src.asked[0].Repos) != 0 {
		t.Errorf("every repository ticked is the whole owner, asked %+v", src.asked)
	}
	three := &fake{repos: []string{"acme/app", "acme/api", "acme/lib"}}
	body = get(Handler(three, Options{}), "GET", "http://localhost/?repo=acme%2Flib&repo=acme%2Fapp").Body.String()
	if len(three.asked) != 1 || !slices.Equal(three.asked[0].Repos, []string{"acme/app", "acme/lib"}) {
		t.Errorf("asked %+v", three.asked)
	}
	for _, want := range []string{`<summary>2 of 3 repositories</summary>`,
		`value="acme/app" checked>`, `value="acme/api">`, `value="acme/lib" checked>`,
		`<a href="/?repo=acme%2Fapp&amp;repo=acme%2Flib&amp;since=4w">4 weeks</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// A window the page does not offer is still shown as the one in use.
	body = get(h, "GET", "http://localhost/?since=8w").Body.String()
	for _, want := range []string{`<a href="/?since=4w">4 weeks</a><a href="/?since=8w" class="on" aria-current="true">8 weeks</a><a href="/">13 weeks</a>`,
		`<summary>All repositories</summary>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}
