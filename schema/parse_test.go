package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TS-12-18: Parse keeps the authored property order.
func TestParseKeepsPropertyOrder_TS12_18(t *testing.T) {
	s, err := Parse([]byte(`{"type":"object","properties":{"zebra":{"type":"string"},"apple":{"type":"string"},"mango":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PropertyOrder, []string{"zebra", "apple", "mango"}) {
		t.Fatalf("PropertyOrder = %v", s.PropertyOrder)
	}
	if len(s.Properties) != 3 || s.Properties["apple"].Type != TypeString {
		t.Fatalf("Properties = %v", s.Properties)
	}
}

// TS-12-19: Parse decodes the standard keywords into their fields.
func TestParseDecodesTheKeywords_TS12_19(t *testing.T) {
	s, err := Parse([]byte(`{"type":"string","description":"test","title":"Test","enum":["a", "b"],"const":"a","nullable":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Type != TypeString || s.Description != "test" || s.Title != "Test" || !s.HasConst || string(s.Const) != `"a"` || !s.Nullable {
		t.Fatalf("schema = %+v", s)
	}
	if len(s.Enum) != 2 || string(s.Enum[0]) != `"a"` || string(s.Enum[1]) != `"b"` {
		t.Fatalf("Enum = %s", s.Enum)
	}
	o, err := Parse([]byte(`{"type":["object","null"],"required":["a"],"additionalProperties":false,
		"properties":{"a":{"type":"array","items":{"type":"integer","minimum":1,"maximum":9,"multipleOf":2},"minItems":1,"maxItems":3,"uniqueItems":true},
		"b":{"anyOf":[{"type":"string","minLength":2,"maxLength":5,"pattern":"^x"},{"type":"null"}]},
		"c":{"oneOf":[{"type":"integer"},{"type":"boolean"}]},"d":{"allOf":[{"type":"number","exclusiveMinimum":0,"exclusiveMaximum":1}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Type != TypeObject || !o.Nullable || !reflect.DeepEqual(o.Required, []string{"a"}) ||
		o.AdditionalProperties == nil || o.AdditionalProperties.Allowed || o.AdditionalProperties.Schema != nil {
		t.Fatalf("object = %+v", o)
	}
	a := o.Properties["a"]
	if a.Type != TypeArray || *a.MinItems != 1 || *a.MaxItems != 3 || !*a.UniqueItems ||
		a.Items.Type != TypeInteger || *a.Items.Minimum != 1 || *a.Items.Maximum != 9 || *a.Items.MultipleOf != 2 {
		t.Fatalf("a = %+v, items %+v", a, a.Items)
	}
	b := o.Properties["b"].AnyOf[0]
	if len(o.Properties["b"].AnyOf) != 2 || *b.MinLength != 2 || *b.MaxLength != 5 || b.Pattern != "^x" {
		t.Fatalf("b = %+v", b)
	}
	if len(o.Properties["c"].OneOf) != 2 || len(o.Properties["d"].AllOf) != 1 ||
		*o.Properties["d"].AllOf[0].ExclusiveMinimum != 0 || *o.Properties["d"].AllOf[0].ExclusiveMaximum != 1 {
		t.Fatalf("combinators = %+v %+v", o.Properties["c"], o.Properties["d"])
	}
	// What Parse reads, MarshalJSON writes back: the round trip is stable.
	out, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if out2, _ := json.Marshal(again); string(out2) != string(out) {
		t.Fatalf("round trip drifted:\n%s\n%s", out, out2)
	}
}

// TS-12-20: keywords Schema does not model are kept in Extra, in order.
func TestParseKeepsUnmodelledKeywords_TS12_20(t *testing.T) {
	s, err := Parse([]byte(`{"type":"object","$comment":"note","x-custom":"val","deprecated":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, k := range s.Extra {
		keys = append(keys, k.Key)
	}
	if strings.Join(keys, ",") != "$comment,x-custom,deprecated" {
		t.Fatalf("Extra keys = %v, want them in authored order", keys)
	}
	for k, want := range map[string]any{"$comment": "note", "x-custom": "val", "deprecated": true} {
		if got, ok := s.Extra.Get(k); !ok || got != want {
			t.Errorf("Extra[%s] = %v, want %v", k, got, want)
		}
	}
}

// TS-12-21: malformed JSON is an error and no schema.
func TestParseRejectsMalformedJSON_TS12_21(t *testing.T) {
	for _, bad := range []string{`{"type":"object","properties":{`, `{"type":`, `[1,2]`, `{"type":"object"} trailing`, `{"items":3}`} {
		if s, err := Parse([]byte(bad)); s != nil || err == nil {
			t.Errorf("Parse(%s) = %v, %v; want nil and an error", bad, s, err)
		}
	}
}
