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

func intp(n int) *int { return &n }

// TS-12-22: a closed object refuses an undeclared property.
func TestClosedObjectRefusesExtraProperties_TS12_22(t *testing.T) {
	s := Object(Prop("name", String())).Closed()
	err := Validate(s, map[string]any{"name": "test", "extra": 123})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("Validate = %v, want a property-not-allowed error", err)
	}
	if err := Validate(s, map[string]any{"name": "test"}); err != nil {
		t.Fatalf("a declared property was refused: %v", err)
	}
}

// TS-12-23: an enum takes only its literals, by value and type.
func TestEnumTakesOnlyItsLiterals_TS12_23(t *testing.T) {
	s := Object(Prop("method", Enum("m", "GET", "POST", "PUT")))
	err := Validate(s, map[string]any{"method": "get"})
	if err == nil || !strings.Contains(err.Error(), "one of") {
		t.Fatalf("Validate = %v, want a one-of error", err)
	}
	n := &Schema{Enum: []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"1"`)}}
	if Validate(n, 1) != nil || Validate(n, "1") != nil || Validate(n, true) == nil {
		t.Fatal("an enum must match by value and type: 1 and \"1\" in, true out")
	}
}

// TS-12-24: numeric bounds and multipleOf are enforced.
func TestNumericBounds_TS12_24(t *testing.T) {
	s := Object(Prop("num", Int().Min(10).Max(50)))
	if err := Validate(s, map[string]any{"num": 5}); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("5 = %v, want an at-least error", err)
	}
	if err := Validate(s, map[string]any{"num": 60}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("60 = %v, want an at-most error", err)
	}
	if err := Validate(s, map[string]any{"num": 10}); err != nil {
		t.Fatalf("10 refused: %v", err)
	}
	lo, hi, step := 0.0, 1.0, 0.25
	x := &Schema{Type: TypeNumber, ExclusiveMinimum: &lo, ExclusiveMaximum: &hi, MultipleOf: &step}
	for v, want := range map[float64]string{0: "greater than", 1: "less than", 0.3: "multiple of"} {
		if err := Validate(x, v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v = %v, want %q", v, err, want)
		}
	}
	if err := Validate(x, 0.5); err != nil {
		t.Errorf("0.5 refused: %v", err)
	}
}

// TS-12-25: string length and pattern are enforced.
func TestStringBounds_TS12_25(t *testing.T) {
	s := &Schema{Type: TypeString, MinLength: intp(5), MaxLength: intp(10), Pattern: "^[a-z]+$"}
	if err := Validate(s, "abc"); err == nil || !strings.Contains(err.Error(), "at least 5 characters") {
		t.Fatalf("abc = %v, want a length error", err)
	}
	if err := Validate(s, "abcdefghijk"); err == nil || !strings.Contains(err.Error(), "at most 10 characters") {
		t.Fatalf("too long = %v, want a length error", err)
	}
	if err := Validate(s, "abc123"); err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("abc123 = %v, want a pattern error", err)
	}
	if err := Validate(s, "abcdef"); err != nil {
		t.Fatalf("abcdef refused: %v", err)
	}
}

// TS-12-26: array item counts and uniqueness are enforced.
func TestArrayBounds_TS12_26(t *testing.T) {
	s := Array(Int())
	unique := true
	s.MinItems, s.MaxItems, s.UniqueItems = intp(2), intp(4), &unique
	if err := Validate(s, []any{1, 1}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("[1,1] = %v, want a uniqueness error", err)
	}
	if err := Validate(s, []any{1}); err == nil || !strings.Contains(err.Error(), "at least 2 items") {
		t.Fatalf("[1] = %v, want a count error", err)
	}
	if err := Validate(s, []any{1, 2, 3, 4, 5}); err == nil || !strings.Contains(err.Error(), "at most 4 items") {
		t.Fatalf("five items = %v, want a count error", err)
	}
	if err := Validate(s, []any{1, 2}); err != nil {
		t.Fatalf("[1,2] refused: %v", err)
	}
}

// TS-12-27: a valid numeric or boolean string is coerced, and recorded.
func TestCoerceConvertsAndRecords_TS12_27(t *testing.T) {
	s := Object(Prop("n", Number()), Prop("b", Bool()))
	out, coercions := Coerce(s, map[string]any{"n": "42.5", "b": "true"})
	if out["n"] != 42.5 || out["b"] != true || len(coercions) != 2 {
		t.Fatalf("Coerce = %v, %v", out, coercions)
	}
}

// TS-12-28: a numeric string that is not a JSON number is left alone.
func TestCoerceRefusesNonJSONNumbers_TS12_28(t *testing.T) {
	s := Object(Prop("count", Int()))
	for _, bad := range []string{"NaN", "Inf", "+5", ".5", "5.", "1_0"} {
		out, coercions := Coerce(s, map[string]any{"count": bad})
		if out["count"] != bad || len(coercions) != 0 {
			t.Errorf("%q coerced to %v (%v)", bad, out["count"], coercions)
		}
	}
}
