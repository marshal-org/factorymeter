package view

import (
	"fmt"

	"github.com/marshal-org/factorymeter/pkg/contract"
	"github.com/marshal-org/factorymeter/pkg/line"
)

// The health of a station is read from three signals, each against a fixed
// bar, so the same numbers always give the same color:
//
//   - load: the station's share of the hours changes spend on the line.
//   - rework: the share of the station's items that failed or went back.
//   - wait: whether the median wait got worse than in the window before.
//
// The worst signal sets the color. A signal drawn from too few items is left
// out.
const (
	loadWatch, loadStrained     = 0.20, 0.40
	reworkWatch, reworkStrained = 0.20, 0.40
)

// Health levels, from the best to the worst. Off is a station with no data.
const (
	levelNone = iota
	levelGood
	levelWatch
	levelBad
)

var levels = []struct{ class, label, icon string }{
	levelNone:  {"none", "Too little data", "·"},
	levelGood:  {"good", "Flowing", "✓"},
	levelWatch: {"warn", "Watch", "!"},
	levelBad:   {"bad", "Strained", "✕"},
}

// health is the grade of one station and the reasons for it.
type health struct {
	Class, Label, Icon string
	Reasons            []string
	level              int
}

// HealthRule says in words how a station is graded. The page shows it next
// to the grades.
var HealthRule = fmt.Sprintf("Each station gets the worst of three grades. Load: the share of the hours changes spend on the line that the station holds. %.0f%% or more is watch, %.0f%% or more is strained. Rework: the share of its items that failed or were sent back. %.0f%% or more is watch, %.0f%% or more is strained. Wait: a median wait that got worse than in the window before is watch. A number drawn from too few items is left out.",
	loadWatch*100, loadStrained*100, reworkWatch*100, reworkStrained*100)

func gradeOf(v, watch, bad float64) int {
	switch {
	case v >= bad:
		return levelBad
	case v >= watch:
		return levelWatch
	}
	return levelGood
}

// grade reads the health of a station from the report.
func grade(st contract.Station, b *book, weeks int) health {
	if st.Status == contract.StatusNotConnected {
		return health{Class: "off", Label: "Not connected", Icon: "○"}
	}
	whole := contract.Slice{}
	lvl := levelNone
	var why []string
	raise := func(l int) {
		if l > lvl {
			lvl = l
		}
	}
	if st.HeldShare != nil {
		raise(gradeOf(*st.HeldShare, loadWatch, loadStrained))
		why = append(why, fmt.Sprintf("Load: holds %s of the hours changes spend on the line.", Number(string(line.Share), st.HeldShare)))
	}
	if s := b.get(st.ID+".rework", whole); s != nil && s.Value != nil && !s.Thin {
		raise(gradeOf(*s.Value, reworkWatch, reworkStrained))
		name := st.ID + ".rework"
		if m, ok := line.ByID(name); ok {
			name = m.Name
		}
		why = append(why, fmt.Sprintf("Rework: %s, %s.", lower(name), Number(s.Unit, s.Value)))
	}
	if s := b.get(st.ID+".wait.p50", whole); s != nil && s.Value != nil && s.Previous != nil && !s.Thin {
		was := fmt.Sprintf("%s, from %s in the %s before", Number(s.Unit, s.Value), Number(s.Unit, s.Previous), plural(weeks, "week"))
		switch s.Trend {
		case contract.TrendWorse:
			raise(levelWatch)
			why = append(why, "Wait: the median got worse: "+was+".")
		case contract.TrendBetter:
			raise(levelGood)
			why = append(why, "Wait: the median got better: "+was+".")
		default:
			raise(levelGood)
			why = append(why, "Wait: the median held: "+was+".")
		}
	}
	l := levels[lvl]
	return health{Class: l.class, Label: l.label, Icon: l.icon, Reasons: why, level: lvl}
}

// lower starts a name with a small letter, unless it starts with a word in
// capitals, such as CI.
func lower(s string) string {
	if len(s) < 2 || s[0] < 'A' || s[0] > 'Z' || (s[1] >= 'A' && s[1] <= 'Z') {
		return s
	}
	return string(s[0]+'a'-'A') + s[1:]
}
