package main

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// terminalOf says whether a person may be watching what is written to w: w
// is a terminal that understands the codes that move the cursor.
func terminalOf(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" {
		return nil, false
	}
	if !term.IsTerminal(int(f.Fd())) || !enableCodes(f) {
		return nil, false
	}
	return f, true
}

// sizeOf is the width and height of a terminal, with a guess when it does not
// tell.
func sizeOf(f *os.File) func() (int, int) {
	return func() (int, int) {
		w, h, err := term.GetSize(int(f.Fd()))
		if err != nil || w <= 0 || h <= 0 {
			return 80, 24
		}
		return w, h
	}
}

// style is how the terminal may be written to.
type style struct {
	// color allows colors. NO_COLOR turns it off.
	color bool
	// plain keeps to ASCII, for a terminal that is not set to UTF-8.
	plain bool
}

func styleOf(env func(string) string, windows bool) style {
	st := style{color: env("NO_COLOR") == ""}
	if !windows {
		locale := env("LC_ALL")
		if locale == "" {
			locale = env("LC_CTYPE")
		}
		if locale == "" {
			locale = env("LANG")
		}
		l := strings.ToLower(locale)
		st.plain = !strings.Contains(l, "utf-8") && !strings.Contains(l, "utf8")
	}
	return st
}

func (st style) paint(code, s string) string {
	if !st.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (st style) bold(s string) string   { return st.paint("1", s) }
func (st style) dim(s string) string    { return st.paint("2", s) }
func (st style) green(s string) string  { return st.paint("32", s) }
func (st style) yellow(s string) string { return st.paint("33", s) }
func (st style) red(s string) string    { return st.paint("31", s) }
func (st style) cyan(s string) string   { return st.paint("36", s) }

func (st style) pick(fancy, ascii string) string {
	if st.plain {
		return ascii
	}
	return fancy
}

func (st style) ok() string   { return st.green(st.pick("✓", "+")) }
func (st style) bad() string  { return st.red(st.pick("✗", "x")) }
func (st style) warn() string { return st.yellow("!") }
func (st style) todo() string { return st.dim(st.pick("○", ".")) }
func (st style) sep() string  { return st.dim(st.pick(" · ", " | ")) }

// held marks a row that waits, such as for a rate limit.
func (st style) held() string { return st.yellow(st.pick("‖", "=")) }

// more marks a line that was cut, so a cut number is not read as a whole one.
func (st style) more() string { return st.pick("…", "...") }

var (
	spinFancy = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinASCII = []string{"|", "/", "-", "\\"}
)

func (st style) spin(tick int) string {
	frames := spinFancy
	if st.plain {
		frames = spinASCII
	}
	return st.cyan(frames[tick%len(frames)])
}

// bar draws how much is done as a line of n cells.
func (st style) bar(share float64, n int) string {
	full := int(share*float64(n) + 0.5)
	full = max(0, min(n, full))
	return st.cyan(strings.Repeat(st.pick("━", "#"), full)) + st.dim(strings.Repeat(st.pick("─", "-"), n-full))
}

// visible is the number of cells a line takes, without its color codes.
func visible(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case esc:
			esc = r != 'm'
		case r == '\x1b':
			esc = true
		default:
			n++
		}
	}
	return n
}

// clip cuts a line to a width, so it never wraps. Color codes take no room
// and are kept. A cut line closes its colors, ends with more, the mark of a
// cut, and leaves the last cell of the width free.
func clip(s string, width int, more string) string {
	if visible(s) <= width {
		return s
	}
	keep := max(0, width-1-visible(more))
	var b strings.Builder
	n, esc, colored := 0, false, false
	for _, r := range s {
		switch {
		case esc:
			esc = r != 'm'
		case r == '\x1b':
			esc, colored = true, true
		default:
			if n == keep {
				if colored {
					b.WriteString("\x1b[0m")
				}
				b.WriteString(more)
				return b.String()
			}
			n++
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fold breaks a line that is wider than the terminal into rows that fit, at
// a space where it can. The rows after the first are indented, and a color
// that is open at a break goes on in the next row. A line the terminal wraps
// by itself takes two rows, and the redraw of the lines under it counts one.
func fold(s string, width int) []string {
	limit := width - 1
	if limit < 20 || visible(s) <= limit {
		return []string{s}
	}
	const indent = "      "
	var (
		rows   []string
		row    []rune
		n      int    // cells in the row
		cut    = -1   // place in the row after its last space
		open   string // the color that is on
		cutOn  string // the color that was on at the cut
		code   []rune
		inCode bool
	)
	for _, r := range s {
		switch {
		case inCode:
			code, row = append(code, r), append(row, r)
			if r == 'm' {
				inCode = false
				if open = string(code); open == "\x1b[0m" {
					open = ""
				}
			}
			continue
		case r == '\x1b':
			inCode, code, row = true, []rune{r}, append(row, r)
			continue
		}
		if n == limit {
			// A space that would be the first cell over the edge is the
			// break itself. Else the row ends after its last space.
			at, on := len(row), open
			if r != ' ' && cut > len(indent) {
				at, on = cut, cutOn
			}
			head, tail := strings.TrimRight(string(row[:at]), " "), string(row[at:])
			if on != "" {
				head += "\x1b[0m"
			}
			rows = append(rows, head)
			row = []rune(indent + on + tail)
			n, cut = len(indent)+visible(tail), -1
			if r == ' ' {
				continue
			}
		}
		row, n = append(row, r), n+1
		if r == ' ' {
			cut, cutOn = len(row), open
		}
	}
	return append(rows, string(row))
}

// pad fills a line with spaces up to a width.
func pad(s string, width int) string {
	if n := visible(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}
