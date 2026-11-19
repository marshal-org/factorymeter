package main

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestStyleFollowsTheEnvironment(t *testing.T) {
	for _, c := range []struct {
		name    string
		env     map[string]string
		windows bool
		want    style
	}{
		{"a UTF-8 terminal", map[string]string{"LANG": "en_US.UTF-8"}, false, style{color: true}},
		{"no color asked", map[string]string{"LANG": "en_US.utf8", "NO_COLOR": "1"}, false, style{}},
		{"a C locale", map[string]string{"LANG": "C"}, false, style{color: true, plain: true}},
		{"LC_ALL wins", map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "POSIX"}, false, style{color: true, plain: true}},
		{"no locale at all", nil, false, style{color: true, plain: true}},
		{"windows has no locale", nil, true, style{color: true}},
	} {
		if got := styleOf(env(c.env), c.windows); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestStyleWithoutColorWritesNoCodes(t *testing.T) {
	st := style{}
	for _, s := range []string{st.bold("x"), st.ok(), st.bad(), st.warn(), st.todo(), st.sep(), st.spin(3), st.bar(0.5, 10)} {
		if strings.Contains(s, "\x1b") {
			t.Errorf("%q carries a color code", s)
		}
	}
	if got := st.bar(0.5, 10); got != "━━━━━─────" {
		t.Errorf("bar: %q", got)
	}
	plain := style{plain: true}
	if got := plain.bar(0.26, 8) + plain.ok() + plain.bad() + plain.todo() + plain.spin(5); got != "##------+x./" {
		t.Errorf("ASCII marks: %q", got)
	}
	// A share outside 0 to 1 never draws more cells than the bar has.
	if got := st.bar(1.7, 4) + st.bar(-1, 4); got != "━━━━────" {
		t.Errorf("bar out of range: %q", got)
	}
}

func TestVisibleClipAndPadCountCellsNotCodes(t *testing.T) {
	st := style{color: true}
	s := "ab" + st.green("cd") + "ef"
	if visible(s) != 6 {
		t.Errorf("visible: %d", visible(s))
	}
	if got := pad(s, 8); got != s+"  " || pad(s, 3) != s {
		t.Errorf("pad: %q", got)
	}
	if clip(s, 6, "…") != s {
		t.Errorf("a line that fits was cut: %q", clip(s, 6, "…"))
	}
	// A cut line leaves the last cell free, closes its color, and ends with
	// the mark of a cut: half a number must not read as a whole one.
	if got := clip(s, 5, "…"); got != "ab\x1b[32mc\x1b[0m…" || visible(got) != 4 {
		t.Errorf("clip: %q", got)
	}
	if got := clip("héllo wörld", 7, "…"); got != "héllo…" {
		t.Errorf("clip counts bytes, not letters: %q", got)
	}
	if got := clip("CI runs +319", 12, "..."); got != "CI runs +319" {
		t.Errorf("a line as wide as the room was cut: %q", got)
	}
	if got := clip("CI runs +319", 11, "..."); got != "CI runs..." {
		t.Errorf("clip with an ASCII mark: %q", got)
	}
	if got := clip("abc", 2, "..."); got != "..." {
		t.Errorf("clip with no room: %q", got)
	}
}

func TestFoldKeepsEveryRowInsideTheTerminal(t *testing.T) {
	st := style{color: true}
	long := "  ! " + st.yellow("github timed out on a page of acme/app, retrying with 13 per page, and then with 7 per page")
	for _, c := range []struct {
		name  string
		line  string
		width int
		rows  int
	}{
		{"a line that fits", "  short", 40, 1},
		{"a line as wide as the room", strings.Repeat("x", 39), 40, 1},
		{"one cell too wide", strings.Repeat("x ", 20) + "x", 40, 2},
		{"a space is the first cell over the edge", strings.Repeat("x", 39) + " tail", 40, 2},
		{"a colored line", long, 40, 3},
		{"no space to break at", "  " + strings.Repeat("x", 100), 40, 3},
		{"a terminal too narrow to fold for", strings.Repeat("x ", 20), 12, 1},
		{"a space at the end and nothing after it", strings.Repeat("x", 39) + " ", 40, 2},
	} {
		rows := fold(c.line, c.width)
		if len(rows) != c.rows {
			t.Errorf("%s: %d rows, want %d: %q", c.name, len(rows), c.rows, rows)
		}
		if c.rows == 1 {
			if rows[0] != c.line {
				t.Errorf("%s: the line changed: %q", c.name, rows[0])
			}
			continue
		}
		for i, r := range rows {
			if visible(r) > c.width-1 {
				t.Errorf("%s: row %d takes %d cells of %d: %q", c.name, i, visible(r), c.width, r)
			}
			if i > 0 && !strings.HasPrefix(r, "      ") {
				t.Errorf("%s: row %d is not indented: %q", c.name, i, r)
			}
		}
	}

	// Nothing is lost, and a color that is open at a break is closed there
	// and opened again in the next row.
	rows := fold(long, 40)
	var text []string
	for i, r := range rows {
		if strings.Count(r, "\x1b[33m") != 1 || !strings.HasSuffix(r, "\x1b[0m") {
			t.Errorf("row %d does not open and close its color: %q", i, r)
		}
		text = append(text, strings.Fields(strip(r))...)
	}
	if got, want := strings.Join(text, " "), strings.Join(strings.Fields(strip(long)), " "); got != want {
		t.Errorf("folded text:\n got %q\nwant %q", got, want)
	}

	// A word that is longer than a row is cut inside, and none of it is lost.
	word := strings.Repeat("x", 100)
	if got := strings.ReplaceAll(strings.Join(fold("  "+word, 40), ""), " ", ""); got != word {
		t.Errorf("a long word lost letters: %d of 100", len(got))
	}
}

// strip takes the color codes out of a line.
func strip(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			esc = r != 'm'
		case r == '\x1b':
			esc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// A build by "go install" of a release names the release. A build from a
// checkout, which carries a made-up version, stays "dev".
func TestVersionOfAnInstalledRelease(t *testing.T) {
	for module, want := range map[string]string{
		"v0.1.0":                             "v0.1.0",
		"v1.12.3":                            "v1.12.3",
		"":                                   "dev",
		"(devel)":                            "dev",
		"v0.1.1-0.20261119165016-fe84567abc": "dev",
		"v0.1.0+dirty":                       "dev",
	} {
		if got := released(module, "dev"); got != want {
			t.Errorf("module version %q: %q, want %q", module, got, want)
		}
	}
}
