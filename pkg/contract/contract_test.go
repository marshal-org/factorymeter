package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marshal-org/factorymeter/pkg/line"
	"github.com/marshal-org/factorymeter/pkg/record"
)

// The committed reference files must be exactly what the code generates.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", filepath.FromSlash(Dir))
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v. Run: go run ./cmd/factorymeter contract write %s", name, err, Dir)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s/%s is out of date. Run: go run ./cmd/factorymeter contract write %s", Dir, name, Dir)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := files[e.Name()]; !ok {
			t.Errorf("%s/%s is not generated. Hand-written docs belong one level up", Dir, e.Name())
		}
	}
}

var snake = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func contractTypes() []reflect.Type {
	var out []reflect.Type
	seen := map[reflect.Type]bool{}
	roots := []any{Report{}}
	for _, r := range Records {
		roots = append(roots, r.Value)
	}
	for _, r := range roots {
		for _, t := range structsOf(r) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func TestEveryFieldIsDocumented(t *testing.T) {
	for _, typ := range contractTypes() {
		if typ.NumField() != len(fieldsOf(typ)) {
			t.Errorf("%s: a field is not written to JSON. Every field of the contract is", typ.Name())
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := f.Tag.Get("json")
			if !snake.MatchString(tag) {
				t.Errorf("%s.%s: json name %q must be snake_case with no options, so a field is never left out", typ.Name(), f.Name, tag)
			}
			doc := f.Tag.Get("doc")
			if doc == "" || !strings.HasSuffix(doc, ".") {
				t.Errorf("%s.%s: the doc tag must be a sentence that ends with a full stop, got %q", typ.Name(), f.Name, doc)
			}
			if _, has := f.Tag.Lookup("enum"); has && f.Type.Kind() != reflect.String {
				t.Errorf("%s.%s: an enum tag only fits a string", typ.Name(), f.Name)
			}
		}
	}
}

func enum(t *testing.T, v any, field string) []string {
	t.Helper()
	for _, f := range Fields(v) {
		if f.Name == field {
			out := append([]string(nil), f.Enum...)
			sort.Strings(out)
			return out
		}
	}
	t.Fatalf("%T has no field %q", v, field)
	return nil
}

func sorted(list ...string) []string {
	sort.Strings(list)
	return list
}

// The allowed values in the tags are written by hand. They must agree with
// the constants the code uses.
func TestEnumsMatchTheCode(t *testing.T) {
	var stations, units []string
	for _, s := range line.Stations {
		stations = append(stations, s.ID)
	}
	for _, u := range line.Units {
		units = append(units, string(u))
	}
	kinds := sorted(line.NeedTicket, line.NeedChange, line.NeedCIRun, line.NeedDeploy)
	classes := sorted("agent", "human", "automation", "")
	cases := []struct {
		name      string
		got, want []string
	}{
		{"Report.schema", enum(t, Report{}, "schema"), []string{Version}},
		{"Station.id", enum(t, Station{}, "id"), sorted(stations...)},
		{"Station.status", enum(t, Station{}, "status"), sorted(StatusMeasured, StatusPartial, StatusNotConnected)},
		{"Series.unit", enum(t, Series{}, "unit"), sorted(units...)},
		{"Series.trend", enum(t, Series{}, "trend"), sorted(TrendBetter, TrendWorse, TrendFlat, TrendUnknown)},
		{"Slice.class", enum(t, Slice{}, "class"), classes},
		{"Item.class", enum(t, Item{}, "class"), classes},
		{"Item.kind", enum(t, Item{}, "kind"), sorted(record.KindTicket, record.KindChange)},
		{"SourceCoverage.kind", enum(t, SourceCoverage{}, "kind"), kinds},
		{"Recommendation.confidence", enum(t, Recommendation{}, "confidence"), sorted(ConfidenceHigh, ConfidenceMedium, ConfidenceLow)},
		{"Actor.kind", enum(t, record.Actor{}, "kind"), sorted(record.ActorUser, record.ActorBot)},
		{"Change.state", enum(t, record.Change{}, "state"), sorted(record.ChangeOpen, record.ChangeClosed, record.ChangeMerged)},
		{"Review.state", enum(t, record.Review{}, "state"), sorted(record.ReviewApproved, record.ReviewChangesRequested, record.ReviewCommented, record.ReviewDismissed)},
		{"Deploy.outcome", enum(t, record.Deploy{}, "outcome"), sorted(record.DeploySuccess, record.DeployFailure, record.DeployRunning, record.DeployUnknown)},
		{"Ticket.source", enum(t, record.Ticket{}, "source"), sorted(record.GitHub, record.Linear, record.Jira)},
		{"Ticket.state", enum(t, record.Ticket{}, "state"), sorted(record.TicketBacklog, record.TicketUnstarted, record.TicketStarted, record.TicketDone, record.TicketCancelled)},
		{"Link.kind", enum(t, record.Link{}, "kind"), sorted(record.LinkCloses, record.LinkMentions, record.LinkReverts)},
		{"Link.to_kind", enum(t, record.Link{}, "to_kind"), sorted(record.KindTicket, record.KindChange)},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: the tag allows %q, the code has %q", c.name, c.got, c.want)
		}
	}
	for _, f := range []struct {
		v     any
		field string
	}{{Series{}, "metric"}, {Evidence{}, "metric"}} {
		sf, _ := reflect.TypeOf(f.v).FieldByName("Metric")
		if got := sf.Tag.Get("pattern"); got != line.IDPattern {
			t.Errorf("%T.%s: pattern %q is not line.IDPattern", f.v, f.field, got)
		}
	}
	for _, r := range Records {
		if r.Kind == "" || !strings.HasSuffix(r.Does, ".") {
			t.Errorf("record %q needs a kind and a sentence", r.Kind)
		}
	}
}

// walk visits every schema object under v.
func walk(v any, path string, visit func(path string, node map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		visit(path, x)
		for k, child := range x {
			walk(child, path+"/"+k, visit)
		}
	case []any:
		for _, child := range x {
			walk(child, path, visit)
		}
	}
}

func TestSchemaIsSound(t *testing.T) {
	raw, err := JSON(ReportSchema())
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s["$id"] != SchemaID || s["type"] != "object" {
		t.Errorf("bad root: id %v, type %v", s["$id"], s["type"])
	}
	defs, _ := s["$defs"].(map[string]any)
	for _, typ := range structsOf(Report{})[1:] {
		if _, ok := defs[typ.Name()]; !ok {
			t.Errorf("no definition of %s", typ.Name())
		}
	}
	walk(s, "", func(path string, node map[string]any) {
		if ref, ok := node["$ref"].(string); ok {
			if _, found := defs[strings.TrimPrefix(ref, "#/$defs/")]; !found {
				t.Errorf("%s: %s points nowhere", path, ref)
			}
		}
		if _, closed := node["additionalProperties"]; closed {
			t.Errorf("%s: the schema must stay open to added fields", path)
		}
		props, ok := node["properties"].(map[string]any)
		if !ok {
			return
		}
		required, _ := node["required"].([]any)
		if len(required) != len(props) {
			t.Errorf("%s: %d properties, %d required. Every field is always written", path, len(props), len(required))
		}
		for name, p := range props {
			if d, _ := p.(map[string]any)["description"].(string); d == "" {
				t.Errorf("%s/%s: no description", path, name)
			}
		}
	})
	root := s["properties"].(map[string]any)
	if got := root["generated_at"].(map[string]any)["format"]; got != "date-time" {
		t.Errorf("a time must be a date-time string, got %v", got)
	}
	prev := defs["Window"].(map[string]any)["properties"].(map[string]any)["previous"].(map[string]any)
	if _, ok := prev["anyOf"]; !ok {
		t.Errorf("a pointer to a struct must allow null: %v", prev)
	}
	value := defs["Series"].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)
	if !reflect.DeepEqual(value["type"], []any{"number", "null"}) {
		t.Errorf("a pointer to a number must allow null: %v", value["type"])
	}
}

// A report written by the Go types has exactly the fields the schema lists.
func TestReportMatchesSchemaFields(t *testing.T) {
	one := 1.0
	r := Report{
		Schema: Version, GeneratedAt: time.Unix(0, 0).UTC(),
		Window:          Window{Periods: []Period{{}}, Previous: &Period{}},
		Coverage:        Coverage{Repos: []RepoCoverage{{}}, Sources: []SourceCoverage{{}}},
		Stations:        []Station{{HeldShare: &one, Oldest: []Item{{}}}},
		Metrics:         []Series{{Value: &one, Points: []Point{{}}}},
		Recommendations: []Recommendation{{Evidence: []Evidence{{}}, Items: []Item{{}}}},
		Limits:          []string{"x"},
	}
	raw, err := JSON(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schema := ReportSchema()
	defs := schema["$defs"].(map[string]any)
	var check func(path string, v any, s map[string]any)
	check = func(path string, v any, s map[string]any) {
		if ref, ok := s["$ref"].(string); ok {
			s = defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		}
		if options, ok := s["anyOf"].([]any); ok {
			s = options[0].(map[string]any)
			if ref, ok := s["$ref"].(string); ok {
				s = defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			}
		}
		switch x := v.(type) {
		case map[string]any:
			props, _ := s["properties"].(map[string]any)
			if len(props) != len(x) {
				t.Errorf("%s: the report has %d fields, the schema %d", path, len(x), len(props))
			}
			for k, child := range x {
				p, ok := props[k].(map[string]any)
				if !ok {
					t.Errorf("%s/%s: not in the schema", path, k)
					continue
				}
				check(path+"/"+k, child, p)
			}
		case []any:
			items, _ := s["items"].(map[string]any)
			for _, child := range x {
				check(path+"[]", child, items)
			}
		}
	}
	check("", doc, schema)
}

func TestDocsNameEveryMetricAndRecord(t *testing.T) {
	md := MetricsMarkdown()
	for _, m := range line.Registry {
		if strings.Count(md, "`"+m.ID+"`") < 2 {
			t.Errorf("metrics.md must list %s in a table and define it", m.ID)
		}
	}
	rec := RecordsMarkdown()
	for _, name := range []string{"## Ticket", "## Change", "### Commit", "### Review", "## CIRun", "## Deploy", "## Link", "### Actor"} {
		if !strings.Contains(rec, name+"\n") {
			t.Errorf("records.md has no section %q", name)
		}
	}
	if strings.Count(rec, "### Actor\n") != 1 {
		t.Error("records.md must describe a shared type once")
	}
	rules := RulesMarkdown()
	for _, r := range line.Rules {
		// Once in the table, once under its own heading.
		if strings.Count(rules, "`"+r.ID+"`") != 2 || !strings.Contains(rules, "## "+r.Title+"\n") || !strings.Contains(rules, "- Fires when: "+r.Fires+"\n") {
			t.Errorf("rules.md must list %s in the table and say when it fires", r.ID)
		}
		for _, id := range r.Reads {
			if !strings.Contains(rules, "`"+id+"`") {
				t.Errorf("rules.md must name %s, which %s reads", id, r.ID)
			}
		}
		if r.Play != "" && !strings.Contains(rules, "("+r.PlayURL()+")") {
			t.Errorf("rules.md must link the practice of %s", r.ID)
		}
	}
	for _, line := range HowRecommended {
		if !strings.Contains(rules, line) {
			t.Errorf("rules.md lacks %q", line)
		}
	}
	for _, s := range []string{md, rec, ReportMarkdown(), rules} {
		if strings.ContainsAny(s, string([]rune{0x2013, 0x2014})) {
			t.Error("a generated page holds a dash the repository does not allow")
		}
	}
	var c Catalog
	raw, err := JSON(NewCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.RecommendationRules) != len(line.Rules) || c.RecommendationRules[0].ID != line.Rules[0].ID || c.RecommendationRules[0].Fires != line.Rules[0].Fires {
		t.Errorf("metrics.json lost the rules: %d of %d", len(c.RecommendationRules), len(line.Rules))
	}
	if len(c.Metrics) != len(line.Registry) || len(c.Stations) != len(line.Stations) || c.Schema != Version {
		t.Errorf("metrics.json lost something: %d metrics, %d stations, schema %q", len(c.Metrics), len(c.Stations), c.Schema)
	}
}
