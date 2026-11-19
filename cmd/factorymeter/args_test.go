package main

import (
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]string{
		"marshal-org/runlease":                        "marshal-org/runlease",
		"  marshal-org/runlease/ ":                    "marshal-org/runlease",
		"marshal-org":                                 "marshal-org",
		"https://github.com/marshal-org/runlease":     "marshal-org/runlease",
		"https://github.com/marshal-org/runlease/":    "marshal-org/runlease",
		"https://github.com/marshal-org/runlease.git": "marshal-org/runlease",
		"http://github.com/marshal-org":               "marshal-org",
		"github.com/marshal-org/runlease":             "marshal-org/runlease",
		"acme/my.repo_name-2":                         "acme/my.repo_name-2",
	} {
		got, err := parseTarget(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v, want %q", in, got, err, want)
		}
	}
	for in, says := range map[string]string{
		"":      "no repository or owner given",
		"a/b/c": "Use a/b",
		"https://github.com/marshal-org/runlease/pulls": "Use marshal-org/runlease",
		"https://gitlab.com/acme/app":                   "on github.com",
		"bad name":                                      "Use owner/repo",
		"-acme":                                         "Use owner/repo",
		"acme/..":                                       "Use owner/repo",
		"acme/app?tab=1":                                "Use owner/repo",
	} {
		_, err := parseTarget(in)
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want an error that says %q", in, err, says)
		}
	}
}

func TestParseHost(t *testing.T) {
	for in, want := range map[string]string{
		"line.internal":  "line.internal",
		" Line.Internal": "line.internal",
		"line.internal.": "line.internal",
		"localhost":      "localhost",
		"10.0.0.5":       "10.0.0.5",
		"[::1]":          "::1",
		"fd00::5":        "fd00::5",
	} {
		got, err := parseHost(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v, want %q", in, got, err, want)
		}
	}
	for in, says := range map[string]string{
		"http://line.internal:7377": "use line.internal instead of http://line.internal:7377",
		"https://line.internal/":    "use line.internal instead",
		"line.internal:7377":        "use line.internal instead of line.internal:7377",
		"line.internal/page":        "with no path",
		"":                          "is empty",
		"line_internal":             "as a host name",
		"-line.internal":            "as a host name",
	} {
		_, err := parseHost(in)
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want an error that says %q", in, err, says)
		}
	}
}

func TestParseAddr(t *testing.T) {
	for in, want := range map[string]string{"127.0.0.1:7377": "127.0.0.1", ":0": "", "[::1]:80": "::1", "localhost:8080": "localhost"} {
		if got, err := parseAddr(in); err != nil || got != want {
			t.Errorf("%q: %q, %v, want %q", in, got, err, want)
		}
	}
	for in, says := range map[string]string{
		"http://127.0.0.1:7377": "use 127.0.0.1:7377 instead",
		"127.0.0.1":             "Use host:port",
		"127.0.0.1:http":        "the port",
		"127.0.0.1:70000":       "the port",
	} {
		_, err := parseAddr(in)
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want an error that says %q", in, err, says)
		}
	}
}

func TestParseKeys(t *testing.T) {
	if got, err := parseKeys("jira-project", []string{"ENG", " ops "}); err != nil || strings.Join(got, ",") != "ENG,ops" {
		t.Errorf("keys: %q, %v", got, err)
	}
	if _, err := parseKeys("linear-team", []string{"ENG,OPS"}); err == nil || !strings.Contains(err.Error(), "--linear-team ENG --linear-team OPS") {
		t.Errorf("a list in one flag: %v", err)
	}
	if _, err := parseKeys("jira-project", []string{"my project"}); err == nil {
		t.Error("a key with a space was taken")
	}
}
