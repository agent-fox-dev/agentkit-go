package schema

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// obj decodes s the way core.PrepareArguments does: numbers as json.Number.
func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Issue #87 §1: coercion writes a number only when the string IS a JSON
// number. ParseFloat also accepts "NaN", "Inf", "+5", ".5", "5." and "1_0",
// and the raw string became an invalid literal in the arguments.
func TestCoerceWritesOnlyJSONNumbers(t *testing.T) {
	s := Object(Prop("n", Number()), Opt("i", Int()))
	for _, bad := range []string{"NaN", "Inf", "-Inf", "+5", ".5", "5.", "1_0", "0x10"} {
		in := obj(t, `{"n":`+mustJSON(bad)+`}`)
		out, _ := Coerce(s, in)
		raw, err := json.Marshal(out)
		if err != nil || !json.Valid(raw) {
			t.Errorf("%q: coerced arguments are not valid JSON: %s (%v)", bad, raw, err)
		}
		if Validate(s, out) == nil {
			t.Errorf("%q passed validation as a number", bad)
		}
	}
	// A coerced number is the value, as a float64: -1.5e3 is -1500.
	for good, want := range map[string]string{"5": "5", "-1.5e3": "-1500", " 7 ": "7", "1, ": "1"} {
		out, _ := Coerce(s, obj(t, `{"n":`+mustJSON(good)+`}`))
		if raw, _ := json.Marshal(out); string(raw) != `{"n":`+want+`}` {
			t.Errorf("%q coerced to %s, want %s", good, raw, want)
		}
	}
	// An integer field takes an integral value only.
	out, _ := Coerce(s, obj(t, `{"n":1,"i":"1.5"}`))
	if Validate(s, out) == nil {
		t.Error(`"1.5" was accepted for an integer`)
	}
	if err := Validate(s, obj(t, `{"n":1,"i":1.5}`)); err == nil {
		t.Error("1.5 was accepted for an integer")
	}
	if err := Validate(s, obj(t, `{"n":1,"i":2.0}`)); err != nil {
		t.Errorf("2.0 is an integral value: %v", err)
	}
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

// Issue #87 §2: Validate enforces what the schema declares.
func TestValidateEnforcesTheDeclaredConstraints(t *testing.T) {
	cases := []struct {
		name   string
		s      *Schema
		ok, no []string
		want   string // substring of the issue for the first `no` case
	}{
		{"additionalProperties false", Object(Prop("a", String())).Closed(),
			[]string{`{"a":"x"}`}, []string{`{"a":"x","b":1}`}, "not allowed"},
		{"enum", Object(Prop("m", Enum("method", "GET", "POST"))),
			[]string{`{"m":"GET"}`}, []string{`{"m":"get"}`}, "one of"},
		{"const", Object(Prop("k", Const("v1"))),
			[]string{`{"k":"v1"}`}, []string{`{"k":"v2"}`}, "must be"},
		{"minimum and maximum", Object(Prop("n", Int().Min(1).Max(5))),
			[]string{`{"n":1}`, `{"n":5}`}, []string{`{"n":0}`, `{"n":6}`}, "at least"},
		{"anyOf: any branch", Object(Prop("v", AnyOf(String(), Int()))),
			[]string{`{"v":"x"}`, `{"v":3}`}, []string{`{"v":true}`}, "anyOf"},
		{"StrictSubset's optional shape", Object(Prop("v", AnyOf(String(), &Schema{Type: TypeNull}))),
			[]string{`{"v":"x"}`, `{"v":null}`}, []string{`{"v":42}`}, "anyOf"},
		{"oneOf: exactly one", Object(Prop("v", OneOf(Int(), Number()))),
			[]string{}, []string{`{"v":3}`}, "oneOf"},
		{"nullable object", Object(Prop("o", Object(Prop("x", String())).Nullable_())),
			[]string{`{"o":null}`, `{"o":{"x":"y"}}`}, []string{`{"o":3}`}, "expected object"},
		{"nullable array", Object(Prop("a", Array(String()).Nullable_())),
			[]string{`{"a":null}`, `{"a":["x"]}`}, []string{`{"a":"x"}`}, "expected array"},
	}
	for _, c := range cases {
		for _, in := range c.ok {
			if err := Validate(c.s, obj(t, in)); err != nil {
				t.Errorf("%s: %s refused: %v", c.name, in, err)
			}
		}
		for i, in := range c.no {
			err := Validate(c.s, obj(t, in))
			if err == nil {
				t.Errorf("%s: %s accepted", c.name, in)
				continue
			}
			if i == 0 && !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %s refused with %q, want it to say %q", c.name, in, err, c.want)
			}
		}
	}
}

// The strict-mode optional shape coerces too: a string "5" for an
// anyOf[integer, null] property becomes 5, as it would for a plain integer.
func TestCoerceThroughASingleNonNullAnyOfBranch(t *testing.T) {
	s := Object(Prop("n", AnyOf(Int(), &Schema{Type: TypeNull})))
	out, _ := Coerce(s, obj(t, `{"n":"5"}`))
	if raw, _ := json.Marshal(out); string(raw) != `{"n":5}` {
		t.Fatalf("coerced to %s, want {\"n\":5}", raw)
	}
	if err := Validate(s, out); err != nil {
		t.Fatal(err)
	}
}
