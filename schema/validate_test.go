package schema

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestValidateEnforcesWhatTheSchemaDeclares(t *testing.T) {
	unique := true
	str := &Schema{Type: TypeString, MinLength: intp(5), MaxLength: intp(10), Pattern: "^[a-z]+$"}
	arr := &Schema{Type: TypeArray, Items: Int(), MinItems: intp(2), MaxItems: intp(4), UniqueItems: &unique}
	frac := &Schema{Type: TypeNumber, ExclusiveMinimum: floatp(0), ExclusiveMaximum: floatp(1), MultipleOf: floatp(0.25)}
	dict := &Schema{Type: TypeObject, AdditionalProperties: &AdditionalProperties{Allowed: true, Schema: Int()}}
	open := &Schema{Type: TypeObject, AdditionalProperties: &AdditionalProperties{Allowed: true}}
	closed := Object(Prop("a", String())).Closed()
	for _, tc := range []struct {
		name string
		s    *Schema
		in   string
		want string // a substring of the error; "" means valid
	}{
		{"closed object, declared key", closed, `{"a":"x"}`, ""},
		{"closed object, undeclared key", closed, `{"a":"x","b":1}`, "b: property not allowed"},
		{"open object, undeclared key", open, `{"k":"x"}`, ""},
		{"dictionary value checked", dict, `{"k":"x"}`, "k: expected integer, got string"},
		{"required missing", Object(Prop("a", String())), `{}`, "a: required property is missing"},
		{"typeless object schema", &Schema{Properties: map[string]*Schema{"a": Int()}}, `{"a":"x"}`, "a: expected integer"},
		{"object given a number", Object(), `3`, "expected object, got number"},
		{"object given a string", Object(), `"{}"`, `expected object, got string: pass the value itself, not a JSON string (received "{}")`},
		{"enum member", Enum("m", "GET", "POST"), `"GET"`, ""},
		{"enum non-member", Enum("m", "GET", "POST"), `"get"`, `must be one of ["GET", "POST"], got "get"`},
		{"enum by value", &Schema{Enum: []json.RawMessage{[]byte(`1`), []byte(`"1"`)}}, `1.0`, ""},
		{"enum by type", &Schema{Enum: []json.RawMessage{[]byte(`1`), []byte(`"1"`)}}, `true`, "got true"},
		{"const", Const("v1"), `"v2"`, `must be "v1"`},
		{"const by meaning", Const(map[string]int{"a": 1, "b": 2}), `{"b":2,"a":1.0}`, ""},
		{"const null", ConstNull(), `0`, "must be null"},
		{"const null holds", ConstNull(), `null`, ""},
		{"minimum", Int().Min(1).Max(5), `0`, "must be at least 1, got 0"},
		{"maximum", Int().Min(1).Max(5), `6`, "must be at most 5, got 6"},
		{"bounds inclusive", Int().Min(1).Max(5), `5`, ""},
		{"exclusive minimum", frac, `0`, "must be greater than 0"},
		{"exclusive maximum", frac, `1`, "must be less than 1"},
		{"multipleOf", frac, `0.3`, "must be a multiple of 0.25, got 0.3"},
		{"multipleOf holds", frac, `0.5`, ""},
		{"decimal multipleOf is exact", &Schema{Type: TypeNumber, MultipleOf: floatp(0.1)}, `0.7`, ""},
		{"integer given a fraction", Int(), `1.5`, "expected integer, got 1.5"},
		{"integer given an integral decimal", Int(), `2.0`, ""},
		{"number given a string", Number(), `"120"`, `expected number, got string "120"; pass a number, e.g. 120`},
		{"number given a boolean", Number(), `true`, "expected number, got boolean"},
		{"string too short", str, `"abc"`, "expected at least 5 characters, got 3"},
		{"string too long", str, `"abcdefghijk"`, "expected at most 10 characters, got 11"},
		{"string off pattern", str, `"abc123"`, `must match pattern "^[a-z]+$", got "abc123"`},
		{"string fits", str, `"abcdef"`, ""},
		{"length counts characters", &Schema{Type: TypeString, MaxLength: intp(5)}, `"ééééé"`, ""},
		{"pattern that does not compile", &Schema{Type: TypeString, Pattern: "("}, `"x"`, "pattern does not compile"},
		{"string given a number", String(), `1`, "expected string, got number"},
		{"string given null", String(), `null`, "expected string, got null"},
		{"array repeats", arr, `[1,1.0]`, "items must be unique: item 1 repeats item 0"},
		{"array too short", arr, `[1]`, "expected at least 2 items, got 1"},
		{"array too long", arr, `[1,2,3,4,5]`, "expected at most 4 items, got 5"},
		{"array fits", arr, `[1,2]`, ""},
		{"array item checked", Object(Prop("a", Array(Int()))), `{"a":[1,"x"]}`, "a/1: expected integer, got string"},
		{"array given a string", Array(String()), `"[\"x\"]"`, `expected array, got string: pass the value itself`},
		{"nullable object", Object().Nullable_(), `null`, ""},
		{"nullable array", Array(String()).Nullable_(), `null`, ""},
		{"nullable object given a number", Object().Nullable_(), `3`, "expected object"},
		{"anyOf branch", AnyOf(String(), Int()), `3`, ""},
		{"anyOf no branch", AnyOf(String(), Int()), `true`, "matches no anyOf alternative (got boolean)"},
		{"anyOf null branch", AnyOf(String(), &Schema{Type: TypeNull}), `null`, ""},
		{"anyOf null branch given a number", AnyOf(String(), &Schema{Type: TypeNull}), `42`, "anyOf"},
		{"oneOf matching two", OneOf(Int(), Number()), `3`, "must match exactly one oneOf alternative, matches 2"},
		{"oneOf matching one", OneOf(Int(), String()), `"x"`, ""},
		{"oneOf matching none", OneOf(Int(), String()), `true`, "matches 0"},
		{"allOf every branch", &Schema{AllOf: []*Schema{Int().Min(1), Number().Max(5)}}, `6`, "must be at most 5"},
		{"allOf holds", &Schema{AllOf: []*Schema{Int().Min(1), Number().Max(5)}}, `3`, ""},
		{"boolean given a string", Bool(), `"true"`, "expected boolean, got string"},
		{"null type given a number", &Schema{Type: TypeNull}, `1`, "expected null, got number"},
		{"no schema", nil, `{"anything":[1]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.s, decode(t, tc.in))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate(%s) = %v, want valid", tc.in, err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("Validate(%s) = %v, want an error containing %q", tc.in, err, tc.want)
			}
		})
	}
}

func TestValidateComparesNumbersExactly(t *testing.T) {
	if err := Validate(Int().Max(9007199254740992), int64(9007199254740993)); err == nil {
		t.Error("2^53+1 passed a maximum of 2^53")
	}
	if err := Validate(&Schema{Type: TypeNumber, MultipleOf: floatp(0.1)}, 0.3); err != nil {
		t.Errorf("0.3 is a multiple of 0.1: %v", err)
	}
	for _, v := range []any{math.NaN(), math.Inf(1)} {
		if Validate(Number(), v) == nil || Validate(Int(), v) == nil {
			t.Errorf("%v passed as a number", v)
		}
	}
	for _, v := range []any{3, int8(3), uint(3), float32(3)} {
		if err := Validate(Int(), v); err != nil {
			t.Errorf("%T 3 refused as an integer: %v", v, err)
		}
	}
	if err := Validate(Number(), struct{}{}); err == nil || !strings.Contains(err.Error(), "got object") {
		t.Errorf("a struct passed as a number: %v", err)
	}
}

func TestValidationErrorReportsEveryIssueAndEchoesTheArguments(t *testing.T) {
	s := Object(Prop("a", Int()), Prop("b", String()), Prop("c", Bool()))
	long := strings.Repeat("é", 100)
	err := Validate(s, map[string]any{"b": 1, "a": "x", strings.Repeat("k", 90): long})
	var ve *ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, ErrArgumentValidation) {
		t.Fatalf("err = %v, want a *ValidationError matching ErrArgumentValidation", err)
	}
	if len(ve.Issues) != 3 || ve.Issues[0].Path != "c" || ve.Issues[1].Path != "a" || ve.Issues[2].Path != "b" {
		t.Fatalf("issues = %+v, want the missing property, then each key in order", ve.Issues)
	}
	// A caller holding the model's own bytes replaces Args, so the echo keeps
	// their order; a long value is abbreviated, a long key is not.
	ve.Args = json.RawMessage(`{"b":1,"a":"x","` + strings.Repeat("k", 90) + `":"` + long + `"}`)
	want := "Invalid arguments:\n" +
		"  - c: required property is missing\n" +
		"  - a: expected integer, got string \"x\"; pass a number, e.g. 120\n" +
		"  - b: expected string, got number\n" +
		"Arguments received:\n" +
		`{"b":1,"a":"x","` + strings.Repeat("k", 90) + `":"` + strings.Repeat("é", 80) + `…"}`
	if got := ve.Error(); got != want {
		t.Fatalf("Error() =\n%s\nwant\n%s", got, want)
	}
}
