package contract

import (
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

// Field is one field of a struct of the contract, as the docs print it.
type Field struct {
	Name string
	Type string
	Doc  string
	Enum []string
}

// jsonName is the name a field has in JSON. It is empty for a field that is
// not written.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" || !f.IsExported() {
		return ""
	}
	if name == "" {
		return f.Name
	}
	return name
}

func enumOf(f reflect.StructField) []string {
	v, ok := f.Tag.Lookup("enum")
	if !ok {
		return nil
	}
	return strings.Split(v, ",")
}

// typeWords names a Go type the way the docs print it.
func typeWords(t reflect.Type) string {
	switch {
	case t == timeType:
		return "time"
	case t.Kind() == reflect.Pointer:
		return typeWords(t.Elem()) + " or null"
	case t.Kind() == reflect.Slice:
		return "list of " + typeWords(t.Elem())
	case t.Kind() == reflect.Struct:
		return t.Name()
	case t.Kind() == reflect.Bool:
		return "boolean"
	case t.Kind() == reflect.String:
		return "string"
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		return "number"
	}
	return "integer"
}

// Fields lists the fields of a struct in the order they are declared.
func Fields(v any) []Field { return fieldsOf(reflect.TypeOf(v)) }

func fieldsOf(t reflect.Type) []Field {
	var out []Field
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := jsonName(f)
		if name == "" {
			continue
		}
		out = append(out, Field{Name: name, Type: typeWords(f.Type), Doc: f.Tag.Get("doc"), Enum: enumOf(f)})
	}
	return out
}

// structsOf lists v's type and every struct type it reaches, v first, each
// one once, in the order they are first met.
func structsOf(v any) []reflect.Type {
	var out []reflect.Type
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t == timeType || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
		for i := 0; i < t.NumField(); i++ {
			if jsonName(t.Field(i)) != "" {
				walk(t.Field(i).Type)
			}
		}
	}
	walk(reflect.TypeOf(v))
	return out
}

type schemaGen struct{ defs map[string]any }

func (g *schemaGen) of(t reflect.Type) map[string]any {
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		inner := g.of(t.Elem())
		if name, ok := inner["type"].(string); ok {
			inner["type"] = []string{name, "null"}
			return inner
		}
		return map[string]any{"anyOf": []any{inner, map[string]any{"type": "null"}}}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": g.of(t.Elem())}
	case reflect.Struct:
		if _, done := g.defs[t.Name()]; !done {
			g.defs[t.Name()] = nil
			g.defs[t.Name()] = g.object(t)
		}
		return map[string]any{"$ref": "#/$defs/" + t.Name()}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	}
	return map[string]any{"type": "integer"}
}

func (g *schemaGen) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := jsonName(f)
		if name == "" {
			continue
		}
		s := g.of(f.Type)
		if doc := f.Tag.Get("doc"); doc != "" {
			s["description"] = doc
		}
		if e := enumOf(f); e != nil {
			s["enum"] = e
		}
		if p, ok := f.Tag.Lookup("pattern"); ok {
			s["pattern"] = p
		}
		props[name] = s
		required = append(required, name)
	}
	// additionalProperties is left open on purpose: a later release may add a
	// field, and a reader that validates must not reject it.
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// Schema builds the JSON Schema, draft 2020-12, of a struct of the contract.
// Field descriptions come from the doc tags and allowed values from the enum
// tags, so the schema cannot say something the code does not.
func Schema(v any, id, title, description string) map[string]any {
	g := &schemaGen{defs: map[string]any{}}
	t := reflect.TypeOf(v)
	root := g.object(t)
	delete(g.defs, t.Name())
	out := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         id,
		"title":       title,
		"description": description,
	}
	for k, val := range root {
		out[k] = val
	}
	if len(g.defs) > 0 {
		out["$defs"] = g.defs
	}
	return out
}
