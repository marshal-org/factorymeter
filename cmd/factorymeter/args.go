package main

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The names GitHub allows: an owner is letters, digits and single hyphens
// inside, up to 39 of them. A repository name is letters, digits, dots,
// hyphens and underscores.
var (
	ownerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

const targetHint = "Use owner/repo, such as marshal-org/runlease, or an owner, such as marshal-org"

// parseTarget reads the repository or owner a command is about. It takes the
// address of the repository on GitHub as well, and turns away anything else
// with the form it wants, so a typo fails here and not later with a message
// about something else.
func parseTarget(arg string) (string, error) {
	s := strings.TrimSpace(arg)
	if s == "" {
		return "", fmt.Errorf("no repository or owner given. %s", targetHint)
	}
	rest := s
	if strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "github.com/") {
		u, err := url.Parse(s)
		if !strings.Contains(s, "://") {
			u, err = url.Parse("https://" + s)
		}
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("cannot read %q: an address must be of a repository or an owner on github.com. %s", s, targetHint)
		}
		rest = u.Path
	}
	rest = strings.TrimSuffix(strings.Trim(rest, "/"), ".git")
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 1 && ownerName.MatchString(parts[0]):
		return parts[0], nil
	case len(parts) == 2 && ownerName.MatchString(parts[0]) && repoName.MatchString(parts[1]) && parts[1] != "." && parts[1] != "..":
		return parts[0] + "/" + parts[1], nil
	case len(parts) > 2 && ownerName.MatchString(parts[0]) && repoName.MatchString(parts[1]):
		return "", fmt.Errorf("cannot read %q as a repository or an owner: it goes on after the repository. Use %s/%s", s, parts[0], parts[1])
	}
	return "", fmt.Errorf("cannot read %q as a repository or an owner. %s", s, targetHint)
}

// hostLabel is one part of a host name.
var hostLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// parseHost reads a host name for --allow-host. A request names the host in
// its Host header without a scheme, a path or a port, so a value with one of
// those would never match, and is turned away with the form that would.
func parseHost(v string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" {
		return "", fmt.Errorf("--allow-host is empty. Give a host name, such as line.internal")
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return "", fmt.Errorf("--allow-host takes a host name, not an address: use %s instead of %s", u.Hostname(), v)
		}
		return "", fmt.Errorf("--allow-host takes a host name, not an address, such as line.internal. %q is not one", v)
	}
	if strings.ContainsAny(s, "/?#@") {
		return "", fmt.Errorf("--allow-host takes a host name alone, with no path, such as line.internal. %q is not one", v)
	}
	if ip := net.ParseIP(strings.Trim(s, "[]")); ip != nil {
		return ip.String(), nil
	}
	if h, port, err := net.SplitHostPort(s); err == nil {
		return "", fmt.Errorf("--allow-host takes a host name with no port: use %s instead of %s. The port is the one of --addr", h, net.JoinHostPort(h, port))
	}
	name := strings.TrimSuffix(s, ".")
	if len(name) > 253 {
		return "", fmt.Errorf("--allow-host %q is longer than a host name can be", v)
	}
	for _, l := range strings.Split(name, ".") {
		if !hostLabel.MatchString(l) {
			return "", fmt.Errorf("cannot read --allow-host %q as a host name. Use letters, digits, hyphens and dots, such as line.internal", v)
		}
	}
	return name, nil
}

// parseAddr reads the address the server listens on, and returns its host.
func parseAddr(addr string) (string, error) {
	hint := fmt.Sprintf("cannot read --addr %q. Use host:port, such as 127.0.0.1:7377", addr)
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil && u.Host != "" {
			return "", fmt.Errorf("--addr takes host:port, not an address: use %s instead of %s", u.Host, addr)
		}
		return "", fmt.Errorf("%s", hint)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || strings.ContainsAny(host, "/?#@ ") {
		return "", fmt.Errorf("%s", hint)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("cannot read the port of --addr %q. Use a number from 1 to 65535, or 0 for a free port", addr)
	}
	return host, nil
}

// key reads the key of a Linear team or a Jira project, such as ENG.
var keyForm = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

func parseKeys(flag string, keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		switch {
		case strings.Contains(k, ","):
			return nil, fmt.Errorf("--%s takes one key: repeat the flag for each, such as --%s ENG --%s OPS", flag, flag, flag)
		case !keyForm.MatchString(k):
			return nil, fmt.Errorf("cannot read --%s %q as a key. A key is letters and digits, such as ENG", flag, k)
		}
		out = append(out, k)
	}
	return out, nil
}
