package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/measure"
	"github.com/marshal-org/factorymeter/pkg/record"
	"github.com/marshal-org/factorymeter/pkg/signature"
	"github.com/marshal-org/factorymeter/pkg/store"
	"github.com/marshal-org/factorymeter/pkg/web"
)

// lines builds reports from a store for the local page. It keeps the line of
// each target it has read, and reads it again only after a new sync.
type lines struct {
	dir, target string
	// only narrows an owner to some of its repositories, each owner/repo.
	only    []string
	reg     *signature.Registry
	summary bool
	now     func() time.Time

	mu   sync.Mutex
	kept map[string]keptLine
}

type keptLine struct {
	stamp string
	line  *measure.Line
}

// scopes reads the manifest of the store as it is now: a sync that ran since
// the last request shows up here. It returns the repositories of the target,
// and the scopes of other sources, which hold tickets for every target.
func (l *lines) scopes(target string, only []string) (repos, others []store.Scope, s *store.Store, err error) {
	s, err = store.Open(l.dir)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, sc := range s.Scopes("") {
		switch {
		case sc.Source != record.GitHub:
			others = append(others, sc)
		case measure.InTarget(sc.Name, target, only):
			repos = append(repos, sc)
		}
	}
	return repos, others, s, nil
}

func (l *lines) Repos() ([]string, error) {
	if l.summary || strings.Contains(l.target, "/") {
		return nil, nil
	}
	scopes, _, s, err := l.scopes(l.target, l.only)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	seen := map[string]bool{}
	var names []string
	for _, sc := range scopes {
		if !seen[sc.Name] {
			seen[sc.Name] = true
			names = append(names, sc.Name)
		}
	}
	return names, nil
}

func (l *lines) Report(q web.Query) (*contract.Report, error) {
	// One repository ticked is the line of that repository. More are the
	// owner narrowed to them.
	target, only := l.target, l.only
	switch {
	case strings.Contains(target, "/"):
		only = nil
	case len(q.Repos) == 1:
		target, only = q.Repos[0], nil
	case len(q.Repos) > 1:
		only = q.Repos
	}
	key := target + "|" + strings.Join(only, ",")
	l.mu.Lock()
	defer l.mu.Unlock()
	scopes, others, s, err := l.scopes(target, only)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	// The tickets of another source change the report of every target, so a
	// sync of them makes the kept line stale as well.
	var stamp strings.Builder
	for _, sc := range append(append([]store.Scope{}, scopes...), others...) {
		fmt.Fprintf(&stamp, "%s %s %d;", store.Key(sc.Source, sc.Name), sc.SyncedAt.UTC().Format(time.RFC3339Nano), len(sc.Notes))
	}
	k, ok := l.kept[key]
	if !ok || k.stamp != stamp.String() {
		line, err := measure.LoadOnly(s, l.reg, target, only)
		if err != nil {
			if len(scopes) == 0 {
				return nil, fmt.Errorf("%w: %v", web.ErrNoData, err)
			}
			return nil, err
		}
		k = keptLine{stamp: stamp.String(), line: line}
		if l.kept == nil {
			l.kept = map[string]keptLine{}
		}
		l.kept[key] = k
	}
	return k.line.Report(measure.Options{Target: target, Weeks: q.Weeks, Until: q.Until, Summary: l.summary,
		Now: l.now().UTC(), Version: version, Registry: strconv.Itoa(l.reg.Version)})
}

// openBrowser opens a page in the browser of this machine. Tests replace it.
var openBrowser = func(u string) error {
	var c *exec.Cmd
	switch {
	case os.Getenv("BROWSER") != "":
		c = exec.Command(os.Getenv("BROWSER"), u)
	case runtime.GOOS == "darwin":
		c = exec.Command("open", u)
	case runtime.GOOS == "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	if err := c.Start(); err != nil {
		return err
	}
	// The opener exits once the browser has the page. Wait reaps it.
	go func() { _ = c.Wait() }()
	return nil
}

// browserURL is the address to open for a server listening on shown: an
// address of every interface is opened on this machine's loopback.
func browserURL(shown string) string {
	host, port, err := net.SplitHostPort(shown)
	if err != nil {
		return "http://" + shown + "/"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// loopback says whether an address is only reachable from this machine.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serveCmd() *cobra.Command {
	var dir, addr, registryPath string
	var hosts, repos []string
	var summary, noOpen bool
	cmd := &cobra.Command{
		Use:   "serve [owner/repo | owner]",
		Short: "Show the line as a local page, with charts, from the local store",
		Long: `serve reads the local store that "sync" fills and shows the delivery line as
a page in the browser: the constraint, the seven stations in order, where to
invest, agents next to people, and each station number by number with its
weekly history. The page can switch the window, and for an owner it can narrow
to one repository.

It reads no network and sends nothing. The page carries its own style and
script and loads nothing from any host. A sync that runs while the server is
up shows on the next reload.

When it runs at a terminal it opens the page in your browser. --no-open
leaves that out. $BROWSER, when set, names the browser to use.

The server listens on this machine only, and answers only requests made to
localhost. To reach it from another machine, give --addr an address that
machine can reach. A request must name that address, or a host given with
--allow-host. It has no login: everyone who can reach the address can read
the line.

For a page to keep or to publish from a job, use
"factorymeter report --html line.html" instead: it writes the same page.`,
		Example: `  factorymeter sync my-org && factorymeter serve my-org
  factorymeter serve owner/repo --addr 127.0.0.1:8080
  factorymeter serve my-org --addr 0.0.0.0:7377 --allow-host line.internal`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry(registryPath)
			if err != nil {
				return err
			}
			target, err := targetOf(args)
			if err != nil {
				return err
			}
			host, err := parseAddr(addr)
			if err != nil {
				return err
			}
			for i, h := range hosts {
				if hosts[i], err = parseHost(h); err != nil {
					return err
				}
			}
			only, err := reposOf(target, repos)
			if err != nil {
				return err
			}
			path, _, err := storeDir(dir, target)
			if err != nil {
				return err
			}
			src := &lines{dir: path, target: target, only: only, reg: reg, summary: summary, now: time.Now}
			// Fail now, with the hint to sync, and not on the first request.
			if _, err := src.Report(web.Query{Weeks: 13}); err != nil {
				return err
			}
			if s, err := store.Open(path); err == nil {
				for _, n := range keysMissing(s) {
					fmt.Fprintln(cmd.ErrOrStderr(), "note: "+n)
				}
				s.Close()
			}
			// An address of this machine that was asked for by name is one the
			// page is meant to be reached under.
			if ip := net.ParseIP(host); host != "" && !loopback(host) && (ip == nil || !ip.IsUnspecified()) {
				hosts = append(hosts, strings.ToLower(host))
			}
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return err
			}
			errw := cmd.ErrOrStderr()
			shown := ln.Addr().String()
			if !loopback(host) {
				fmt.Fprintf(errw, "warning: %s is reachable from other machines, and the page has no login.\n", shown)
			}
			if host == "localhost" {
				_, port, _ := net.SplitHostPort(shown)
				shown = net.JoinHostPort(host, port)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "The line of %s is at http://%s\nPress Ctrl-C to stop.\n", target, shown)
			// A person at a terminal gets the page in the browser. A job, or
			// output to a file, does not.
			if _, term := terminalOf(cmd.OutOrStdout()); term && !noOpen {
				if err := openBrowser(browserURL(shown)); err != nil {
					fmt.Fprintf(errw, "Could not open a browser (%v). Open the address above.\n", err)
				}
			}

			srv := &http.Server{
				Handler:           web.Handler(src, web.Options{Hosts: hosts}),
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				IdleTimeout:       2 * time.Minute,
				MaxHeaderBytes:    16 << 10,
			}
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ln) }()
			select {
			case err := <-done:
				return err
			case <-cmd.Context().Done():
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "store", "", "store directory (default: the owner's directory in the user cache)")
	f.StringSliceVar(&repos, "repo", nil, "for an owner, show only this repository. Repeat for more")
	f.StringVar(&addr, "addr", "127.0.0.1:7377", "address to listen on. Port 0 picks a free port")
	f.StringSliceVar(&hosts, "allow-host", nil, "also answer requests made to this host name. Repeat for more")
	f.BoolVar(&summary, "summary", false, "leave out every repository name, title and link")
	f.BoolVar(&noOpen, "no-open", false, "do not open the page in the browser")
	f.StringVar(&registryPath, "registry", "", "signature registry file to use instead of the built-in one")
	return cmd
}
