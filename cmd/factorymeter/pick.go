package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// pickState is a list to tick items in: the items, which are ticked, the row
// the cursor is on and the first row on the screen.
type pickState struct {
	items    []string
	on       []bool
	cur, top int
	rows     int
	// warn is a line under the list, such as "Pick at least one".
	warn string
}

type pickKey int

const (
	keyNone pickKey = iota
	keyUp
	keyDown
	keyToggle
	keyAll
	keyEnter
	keyStop
)

func newPick(items []string, rows int) *pickState {
	p := &pickState{items: items, on: make([]bool, len(items)), rows: max(1, min(rows, len(items)))}
	for i := range p.on {
		p.on[i] = true
	}
	return p
}

func (p *pickState) picked() []int {
	var out []int
	for i, on := range p.on {
		if on {
			out = append(out, i)
		}
	}
	return out
}

// press takes one key. It says whether the pick is done.
func (p *pickState) press(k pickKey) bool {
	p.warn = ""
	switch k {
	case keyUp:
		if p.cur > 0 {
			p.cur--
		}
	case keyDown:
		if p.cur < len(p.items)-1 {
			p.cur++
		}
	case keyToggle:
		p.on[p.cur] = !p.on[p.cur]
	case keyAll:
		// All ticked turns all off; anything else ticks all.
		all := len(p.picked()) == len(p.items)
		for i := range p.on {
			p.on[i] = !all
		}
	case keyEnter:
		if len(p.picked()) == 0 {
			p.warn = "Pick at least one: space ticks the repository under the cursor, a ticks all."
			return false
		}
		return true
	}
	if p.cur < p.top {
		p.top = p.cur
	}
	if p.cur >= p.top+p.rows {
		p.top = p.cur - p.rows + 1
	}
	return false
}

// lines is the list as it is drawn: a line of help, the rows on the screen,
// and a line under them that is empty when there is nothing to say.
func (p *pickState) lines(st style) []string {
	out := []string{"     Up and down move, space ticks, a ticks all or none, Enter takes them."}
	for i := p.top; i < p.top+p.rows && i < len(p.items); i++ {
		box, mark := "[ ]", "  "
		if p.on[i] {
			box = "[x]"
		}
		if i == p.cur {
			mark = "> "
		}
		row := fmt.Sprintf("   %s%s %s", mark, box, p.items[i])
		if i == p.cur {
			row = st.paint("1", row)
		}
		out = append(out, row)
	}
	under := fmt.Sprintf("     %d of %d ticked", len(p.picked()), len(p.items))
	if more := len(p.items) - p.rows; more > 0 {
		under += fmt.Sprintf(", %d more above or below", more)
	}
	if p.warn != "" {
		under = "     " + st.paint("33", p.warn)
	}
	return append(out, under)
}

// keyOf reads one key off the front of b. It says how many bytes it took;
// zero means b holds only the start of a key.
func keyOf(b []byte) (pickKey, int) {
	if len(b) == 0 {
		return keyNone, 0
	}
	switch b[0] {
	case 0x1b:
		if len(b) < 3 {
			return keyNone, 0
		}
		if b[1] == '[' || b[1] == 'O' {
			switch b[2] {
			case 'A':
				return keyUp, 3
			case 'B':
				return keyDown, 3
			}
			return keyNone, 3
		}
		return keyNone, 1
	case 0x03, 0x04:
		return keyStop, 1
	case '\r', '\n':
		return keyEnter, 1
	case ' ', 'x':
		return keyToggle, 1
	case 'a', 'A':
		return keyAll, 1
	case 'k':
		return keyUp, 1
	case 'j':
		return keyDown, 1
	}
	return keyNone, 1
}

// pickMany asks which of the items to take, all ticked to start with. At a
// terminal it is a list to tick with the keys; else a line of numbers or
// names. It returns the indexes picked, or nil for all of them.
func (a *asker) pickMany(items []string) []int {
	if in, ok := a.in.(*os.File); ok && term.IsTerminal(int(in.Fd())) {
		if out, ok := terminalOf(a.out); ok {
			if picked, ok := a.pickKeys(in, out, items); ok {
				return picked
			}
		}
	}
	for i, it := range items {
		a.say("       %2d  %s", i+1, it)
	}
	var picked []int
	a.ask("Which? Numbers or names separated by spaces, such as 1 3 5-7, or Enter for all:", "all", func(s string) error {
		var err error
		picked, err = parsePicks(s, items)
		return err
	})
	return picked
}

// pickKeys runs the list in raw mode. It says false when the terminal cannot
// go raw, and the line question is asked instead.
func (a *asker) pickKeys(in, out *os.File, items []string) ([]int, bool) {
	_, h := sizeOf(out)()
	p := newPick(items, min(15, h-8))
	st := styleOf(os.Getenv, onWindows)
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, false
	}
	defer term.Restore(int(in.Fd()), old)
	drawn := 0
	draw := func() {
		var b strings.Builder
		if drawn > 0 {
			fmt.Fprintf(&b, "\r\x1b[%dA", drawn)
		}
		ls := p.lines(st)
		for _, l := range ls {
			b.WriteString("\r\x1b[2K" + l + "\r\n")
		}
		drawn = len(ls)
		fmt.Fprint(a.out, b.String())
	}
	draw()
	for {
		k, n := keyOf(a.pending)
		if n == 0 {
			if a.inErr != nil {
				break
			}
			a.more()
			continue
		}
		a.pending = a.pending[n:]
		if k == keyStop {
			panic(errAskStopped)
		}
		done := p.press(k)
		draw()
		if done {
			break
		}
	}
	picked := p.picked()
	if len(picked) == len(items) || len(picked) == 0 {
		return nil, true
	}
	return picked, true
}

// parsePicks reads numbers, ranges such as 3-5, and names of items. "all"
// and an empty answer are every item, as nil.
func parsePicks(answer string, items []string) ([]int, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || strings.EqualFold(answer, "all") {
		return nil, nil
	}
	seen := map[int]bool{}
	var out []int
	add := func(i int) {
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	for _, f := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if lo, hi, ok := strings.Cut(f, "-"); ok {
			a, err1 := strconv.Atoi(lo)
			b, err2 := strconv.Atoi(hi)
			if err1 == nil && err2 == nil {
				if a < 1 || b > len(items) || a > b {
					return nil, fmt.Errorf("%s is not a range of 1 to %d", f, len(items))
				}
				for i := a; i <= b; i++ {
					add(i - 1)
				}
				continue
			}
		}
		if n, err := strconv.Atoi(f); err == nil {
			if n < 1 || n > len(items) {
				return nil, fmt.Errorf("%d is not on the list: pick 1 to %d", n, len(items))
			}
			add(n - 1)
			continue
		}
		found := false
		for i, it := range items {
			_, short, _ := strings.Cut(it, "/")
			if strings.EqualFold(f, it) || strings.EqualFold(f, short) {
				add(i)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%q is not on the list. Give its number or its name", f)
		}
	}
	if len(out) == len(items) {
		return nil, nil
	}
	return out, nil
}
