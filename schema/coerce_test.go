package schema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCoerceConvertsAStringOnlyToTheDeclaredTypeAndOnlyWhenItIsThatType(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *Schema
		in   string // the JSON of property v
		want string // the JSON of v after coercion
		to   Type   // the recorded target, "" for no coercion
	}{
		{"number", Number(), `"42.5"`, `42.5`, TypeNumber},
		{"number with exponent", Number(), `"-1.5e3"`, `-1500`, TypeNumber},
		{"number with spaces", Number(), `" 7 "`, `7`, TypeNumber},
		{"number with a trailing separator", Number(), `"1, "`, `1`, TypeNumber},
		{"integer", Int(), `"5"`, `5`, TypeInteger},
		{"integral decimal stays verbatim", Int(), `"2.0"`, `2.0`, TypeInteger},
		{"integer past int64 stays verbatim", Int(), `"99999999999999999999"`, `99999999999999999999`, TypeInteger},
		{"fractional for an integer", Int(), `"1.5"`, `"1.5"`, ""},
		{"integer out of float range", Int(), `"1e400"`, `"1e400"`, ""},
		{"boolean", Bool(), `"true"`, `true`, TypeBoolean},
		{"not a boolean", Bool(), `"yes"`, `"yes"`, ""},
		{"number to string", String(), `5`, `"5"`, TypeString},
		{"through anyOf[T, null]", AnyOf(Int(), &Schema{Type: TypeNull}), `"5"`, `5`, TypeInteger},
		{"anyOf with two branches", AnyOf(Int(), Bool()), `"5"`, `"5"`, ""},
		{"undeclared type", &Schema{}, `"5"`, `"5"`, ""},
		{"string for a string", String(), `"5"`, `"5"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, log := Coerce(Object(Prop("v", tc.s)), obj(t, `{"v":`+tc.in+`}`))
			if got := marshal(t, out["v"]); got != tc.want {
				t.Errorf("v = %s, want %s", got, tc.want)
			}
			var want []Coercion
			if tc.to != "" {
				from := TypeString
				if tc.to == TypeString {
					from = TypeNumber
				}
				want = []Coercion{{Path: "v", From: from, To: tc.to}}
			}
			if !reflect.DeepEqual(log, want) {
				t.Errorf("coercions = %+v, want %+v", log, want)
			}
		})
	}
}

func TestCoerceLeavesAStringThatIsNotAJSONNumberForValidateToRefuse(t *testing.T) {
	s := Object(Prop("n", Number()), Opt("i", Int()))
	for _, bad := range []string{"NaN", "Inf", "-Inf", "+5", ".5", "5.", "1_0", "0x10"} {
		in := map[string]any{"n": bad, "i": bad}
		out, log := Coerce(s, in)
		if out["n"] != bad || out["i"] != bad || len(log) != 0 {
			t.Errorf("%q coerced to %v (%v)", bad, out, log)
		}
		if raw, err := json.Marshal(out); err != nil || !json.Valid(raw) {
			t.Errorf("%q: coerced arguments are not valid JSON: %s (%v)", bad, raw, err)
		}
		if Validate(s, out) == nil {
			t.Errorf("%q passed validation as a number", bad)
		}
	}
}

func TestCoerceWritesAnyGoNumberAsItsLiteralForAString(t *testing.T) {
	for in, want := range map[any]string{1.5: "1.5", float32(0.25): "0.25", 3: "3", int64(-4): "-4",
		uint8(7): "7", json.Number("1e3"): "1e3"} {
		out, _ := Coerce(Object(Prop("s", String())), map[string]any{"s": in})
		if out["s"] != want {
			t.Errorf("%#v coerced to %#v, want %q", in, out["s"], want)
		}
	}
}

func TestCoerceDescendsIntoArraysAndObjects(t *testing.T) {
	s := Object(Prop("l", Array(Bool())), Prop("o", Object(Prop("n", Int()))), Opt("free", nil))
	out, log := Coerce(s, obj(t, `{"l":["true","x","false"],"o":{"n":"3"},"free":"9"}`))
	if got := marshal(t, out); got != `{"free":"9","l":[true,"x",false],"o":{"n":3}}` {
		t.Fatalf("Coerce = %s", got)
	}
	// Each coercion is recorded at its full path, as Validate names it.
	if len(log) != 3 || log[0].Path != "l/0" || log[1].Path != "l/2" || log[2].Path != "o/n" {
		t.Fatalf("coercions = %+v", log)
	}
	if out, log := Coerce(nil, map[string]any{"a": "1"}); out["a"] != "1" || log != nil {
		t.Fatalf("Coerce without a schema = %v, %v", out, log)
	}
}

func TestDeleteOptionalNullsDropsOnlyOptionalNullsAtEveryDepth(t *testing.T) {
	item := Object(Opt("x", Int()), Prop("y", Int()))
	s := Object(Prop("req", String()), Opt("opt", String()), Opt("o", item), Opt("l", Array(item)))
	in := obj(t, `{"req":null,"opt":null,"undeclared":null,"o":{"x":null,"y":null},"l":[{"x":null,"y":1},2]}`)
	before := marshal(t, in)
	out := DeleteOptionalNulls(s, in)
	if got := marshal(t, out); got != `{"l":[{"y":1},2],"o":{"y":null},"req":null,"undeclared":null}` {
		t.Fatalf("DeleteOptionalNulls = %s", got)
	}
	if marshal(t, in) != before {
		t.Fatal("DeleteOptionalNulls changed its input; it must return a copy")
	}
	if got := marshal(t, DeleteOptionalNulls(nil, obj(t, `{"a":null,"b":[{"c":null}]}`))); got != `{"a":null,"b":[{"c":null}]}` {
		t.Fatalf("without a schema = %s, want every value kept", got)
	}
}
