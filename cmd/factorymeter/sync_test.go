package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/ingest"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/store"
)

func TestStoreInfo(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (string, error) {
		cmd := storeCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	// No store yet: the error says what to run.
	if _, err := run("info", "acme", "--store", dir); !errors.Is(err, store.ErrNoStore) || !strings.Contains(err.Error(), `factorymeter sync acme`) {
		t.Fatalf("err = %v", err)
	}

	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s, err := store.OpenWrite(dir, "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	rows := []record.Change{{ID: "github:acme/app#1", Source: record.GitHub, Repo: "acme/app", Number: 1, CreatedAt: now.Add(-time.Hour), UpdatedAt: now}}
	if _, _, err := store.Changes.Put(s, record.GitHub, "acme/app", rows); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScope(store.Scope{Source: record.GitHub, Name: "acme/app", From: now.AddDate(0, 0, -182), SyncedAt: now, Notes: []string{"A limit."}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScope(store.Scope{Source: record.GitHub, Name: "acme/half"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	text, err := run("info", "acme", "--store", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acme: 2 repositories", "2026-05-04", "2026-11-02 12:00 UTC", "not finished", "note: acme/app: A limit."} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}
	js, err := run("info", "acme", "--store", dir, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var info storeInfo
	if err := json.Unmarshal([]byte(js), &info); err != nil {
		t.Fatal(err)
	}
	if info.Owner != "acme" || len(info.Scopes) != 2 || info.Scopes[0].Records["changes"] != 1 || info.Scopes[0].Records["ci_runs"] != 0 || info.Bytes == 0 {
		t.Errorf("json: %+v", info)
	}
	if _, err := run("info", "acme", "--store", dir, "--format", "yaml"); err == nil {
		t.Error("an unknown format must fail")
	}

	t.Setenv("FACTORYMETER_STORE", "")
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
	p, err := run("path", "Acme")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(p), filepath.Join("factorymeter", "acme")) {
		t.Errorf("path = %q %v", p, err)
	}
	if _, err := run("path", "../x"); err == nil {
		t.Error("a name that is no owner must fail")
	}
}

func TestPrintSync(t *testing.T) {
	var out bytes.Buffer
	printSync(&out, ingest.Result{Repos: []ingest.RepoResult{
		{Repo: "acme/app", Changes: ingest.Count{Added: 2, Changed: 1, Total: 40}, CIRuns: ingest.Count{Added: 5, Total: 90}, Deploys: ingest.Count{Total: 3},
			Tickets: ingest.Count{Added: 4, Total: 12},
			From:    time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC), Notes: []string{"A limit."}},
		{Repo: "acme/gone", Err: errors.New("not found")},
		{Repo: "acme/half", Err: fmt.Errorf("pull requests: %w", context.Canceled)},
	}}, 12, 3400*time.Millisecond)
	got := out.String()
	if strings.Contains(got, "failed: acme/half") || !strings.Contains(got, "stopped") {
		t.Errorf("an interrupted repository must read as stopped, not failed:\n%s", got)
	}
	for _, want := range []string{"40 (+2, 1 updated)", "90 (+5)", "  3  ", "TICKETS", "12 (+4)", "2026-05-04", "note: acme/app: A limit.", "failed: acme/gone: not found", "12 requests to GitHub in 3s."} {
		if !strings.Contains(got, want) {
			t.Errorf("the output lacks %q:\n%s", want, got)
		}
	}
}
