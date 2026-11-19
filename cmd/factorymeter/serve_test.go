package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
	"github.com/marshal-org/factorymeter/pkg/web"
)

func TestLines(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	dir := reportStore(t, now)
	reg, err := loadRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	src := &lines{dir: dir, target: "acme", reg: reg, now: func() time.Time { return now }}

	// An owner offers its repositories. One repository offers none, and a
	// summary names none.
	if got, err := src.Repos(); err != nil || len(got) != 1 || got[0] != "acme/app" {
		t.Errorf("repos of the owner = %v, %v", got, err)
	}
	for _, other := range []*lines{{dir: dir, target: "acme/app", reg: reg}, {dir: dir, target: "acme", reg: reg, summary: true}} {
		if got, err := other.Repos(); err != nil || got != nil {
			t.Errorf("repos of %q, summary %v = %v, %v", other.target, other.summary, got, err)
		}
	}

	rep, err := src.Report(web.Query{Weeks: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Target != "acme" || rep.Verdict.Constraint != line.Review || !rep.Window.Until.Equal(now) || !rep.GeneratedAt.Equal(now) {
		t.Errorf("report: target %q, constraint %q, until %v", rep.Target, rep.Verdict.Constraint, rep.Window.Until)
	}
	// Another window comes from the line that was read, not from the disk.
	first := src.kept["acme"].line
	if _, err := src.Report(web.Query{Weeks: 4}); err != nil || src.kept["acme"].line != first {
		t.Errorf("the line was read again with nothing new in the store, err %v", err)
	}
	// One repository is its own line.
	one, err := src.Report(web.Query{Weeks: 2, Repo: "acme/app"})
	if err != nil || one.Target != "acme/app" || len(src.kept) != 2 {
		t.Errorf("one repository: %v, kept %d", err, len(src.kept))
	}
	if _, err := src.Report(web.Query{Weeks: 2, Repo: "acme/none"}); !errors.Is(err, web.ErrNoData) {
		t.Errorf("a repository the store does not hold: %v", err)
	}

	// A sync that ran since shows on the next request.
	later := now.Add(time.Hour)
	s, err := store.OpenWrite(dir, "acme", later)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutScope(store.Scope{Source: record.GitHub, Name: "acme/app", From: now.Add(-4 * 7 * 24 * time.Hour), SyncedAt: later}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rep, err = src.Report(web.Query{Weeks: 2})
	if err != nil || src.kept["acme"].line == first || !rep.Window.Until.Equal(later) {
		t.Errorf("after a sync: err %v, read again %v, until %v", err, src.kept["acme"].line != first, rep.Window.Until)
	}

	// A summary carries no name, title or link.
	sum, err := (&lines{dir: dir, target: "acme/app", reg: reg, summary: true, now: func() time.Time { return now }}).Report(web.Query{Weeks: 2})
	if err != nil || !sum.Summary || sum.Target != "" {
		t.Errorf("summary: %v, target %q", err, sum.Target)
	}
}

// out collects what a command prints while the test reads it.
type out struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *out) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *out) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func TestServeCommand(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	dir := reportStore(t, now)

	// What cannot start says why, before it listens.
	for _, bad := range [][]string{
		{"acme", "--store", t.TempDir()},                       // no store
		{"acme/other", "--store", dir},                         // not in the store
		{"acme/app", "--store", dir, "--addr", "7377"},         // no host:port
		{"acme/app", "--store", dir, "--addr", "256.0.0.1:0"},  // no such address
		{"acme/app", "--store", dir, "--registry", "/no/such"}, // no registry
		{},
	} {
		cmd := serveCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(bad)
		cmd.SilenceUsage = true
		if err := cmd.ExecuteContext(context.Background()); err == nil {
			t.Errorf("%v must fail", bad)
		}
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var stdout, stderr out
	cmd := serveCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"acme/app", "--store", dir, "--addr", "127.0.0.1:0"})
	cmd.SilenceUsage = true
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	// Port 0 picks a free port, and the command says which.
	var base string
	said := regexp.MustCompile(`The line of acme/app is at (http://127\.0\.0\.1:\d+)\n`)
	for deadline := time.Now().Add(10 * time.Second); base == ""; time.Sleep(10 * time.Millisecond) {
		if m := said.FindStringSubmatch(stdout.String()); m != nil {
			base = m[1]
		}
		select {
		case err := <-done:
			t.Fatalf("the server stopped: %v\n%s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not say where it listens: %q", stdout.String())
		}
	}
	if stderr.String() != "" {
		t.Errorf("a server on this machine only warned: %q", stderr.String())
	}

	fetch := func(method, path, host string) (int, string) {
		req, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	code, body := fetch("GET", "/?since=2w", "")
	if code != 200 || !strings.Contains(body, "Review is the constraint.") || !strings.Contains(body, "Secret title 7") {
		t.Errorf("the page = %d: %.300s", code, body)
	}
	if code, body := fetch("GET", "/report.json", ""); code != 200 || !strings.Contains(body, `"schema": "factorymeter/v1"`) {
		t.Errorf("the json = %d: %.200s", code, body)
	}
	if code, body := fetch("GET", "/", "evil.example"); code != 403 || strings.Contains(body, "Secret") {
		t.Errorf("another host = %d: %.200s", code, body)
	}
	if code, _ := fetch("POST", "/", ""); code != 405 {
		t.Errorf("POST = %d", code)
	}

	// Ctrl-C stops it cleanly.
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
}
