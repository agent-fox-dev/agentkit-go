package schema

import (
	"errors"
	"strings"
	"testing"
)

func strictErr(t *testing.T, s *Schema) *StrictRewriteError {
	t.Helper()
	_, err := StrictSubset(s)
	var se *StrictRewriteError
	if !errors.As(err, &se) || !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("StrictSubset error = %v, want a *StrictRewriteError matching ErrInvalidSchema", err)
	}
	if StrictSubsetOK(s) {
		t.Fatal("StrictSubsetOK must report the rejection")
	}
	return se
}

func TestStrictSubsetRejectsWhatTheSubsetCannotExpress(t *testing.T) {
	dict := &Schema{Type: TypeObject, AdditionalProperties: &AdditionalProperties{Allowed: true, Schema: String()}}
	for _, tc := range []struct {
		name          string
		s             *Schema
		keyword, path string
		says          string
	}{
		{"allOf", &Schema{AllOf: []*Schema{String()}}, "allOf", "(root)", "uses allOf, which the strict subset forbids"},
		{"oneOf", Object(Prop("v", OneOf(String()))), "oneOf", "/v", "forbids"},
		{"$ref in Extra", Object(Prop("v", String().WithExtra("$ref", "#/x"))), "$ref", "/v", "forbids"},
		{"not under items", Array(&Schema{Extra: Extra{{Key: "not", Value: []byte(`{}`)}}}), "not", "/items", "forbids"},
		{"forbidden under anyOf", AnyOf(String(), String().WithExtra("if", true)), "if", "/anyOf/1", "forbids"},
		{"a dictionary object", Object(Prop("name", String()), Prop("labels", dict)), "additionalProperties", "/labels", "dictionary"},
		{"an array without items", Object(Prop("tags", Array(nil))), "items", "/tags", "declares no items"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := strictErr(t, tc.s)
			if se.Keyword != tc.keyword || se.Path != tc.path || !strings.Contains(se.Error(), tc.says) {
				t.Fatalf("error = %+v (%q), want %s at %s saying %q", se, se.Error(), tc.keyword, tc.path, tc.says)
			}
			if tc.keyword == "items" && strings.Contains(se.Error(), "forbids") {
				t.Fatalf("Error() = %q reads as if items were forbidden; it is required", se.Error())
			}
		})
	}
	// The probe never mutates the caller's schema.
	s := Object(Prop("labels", dict))
	if se := strictErr(t, s); se.Path != "/labels" {
		t.Fatalf("error at %s, want /labels", se.Path)
	}
	if s.Properties["labels"].AdditionalProperties.Schema == nil {
		t.Fatal("the probe closed the caller's own schema")
	}
}

func TestStrictSubsetClosesObjectsAndRequiresEveryProperty(t *testing.T) {
	inner := Object(Opt("deep", Int()))
	s := Object(Prop("name", String()), Opt("note", String("a free-text note")), Opt("maybe", Int().Nullable_()),
		Opt("list", Array(inner)), Opt("alt", AnyOf(Object(Opt("x", Bool())))))
	s.AdditionalProperties = &AdditionalProperties{Allowed: true}
	before := marshal(t, s)

	out, err := StrictSubset(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"object","properties":{` +
		`"name":{"type":"string"},` +
		`"note":{"description":"a free-text note","anyOf":[{"type":"string"},{"type":"null"}]},` +
		`"maybe":{"type":["integer","null"]},` +
		`"list":{"anyOf":[{"type":"array","items":{"type":"object","properties":{"deep":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"required":["deep"],"additionalProperties":false}},{"type":"null"}]},` +
		`"alt":{"anyOf":[{"anyOf":[{"type":"object","properties":{"x":{"anyOf":[{"type":"boolean"},{"type":"null"}]}},"required":["x"],"additionalProperties":false}]},{"type":"null"}]}},` +
		`"required":["name","note","maybe","list","alt"],"additionalProperties":false}`
	if got := marshal(t, out); got != want {
		t.Fatalf("StrictSubset =\n%s\nwant\n%s", got, want)
	}
	if after := marshal(t, s); after != before {
		t.Fatalf("the rewrite changed the caller's schema:\n%s\n%s", before, after)
	}
}

func TestStrictSubsetTreatsATypelessSchemaWithPropertiesAsAnObject(t *testing.T) {
	s := &Schema{Properties: map[string]*Schema{"a": String()}}
	out, err := StrictSubset(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"object","properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["a"],"additionalProperties":false}`
	if got := marshal(t, out); got != want {
		t.Fatalf("StrictSubset = %s, want %s", got, want)
	}
	if s.Type != TypeNone {
		t.Fatal("the rewrite pinned the type on the caller's schema")
	}
}
