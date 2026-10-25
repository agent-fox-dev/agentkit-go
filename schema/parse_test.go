package schema

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Schema {
	t.Helper()
	s, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse(%s): %v", src, err)
	}
	return s
}

func TestParseKeepsTheAuthoredPropertyOrder(t *testing.T) {
	s := mustParse(t, `{"type":"object","properties":{"zebra":{"type":"string"},"apple":{"type":"string"},"mango":{}}}`)
	if !reflect.DeepEqual(s.PropertyOrder, []string{"zebra", "apple", "mango"}) {
		t.Fatalf("PropertyOrder = %v", s.PropertyOrder)
	}
	if len(s.Properties) != 3 || s.Properties["apple"].Type != TypeString {
		t.Fatalf("Properties = %v", s.Properties)
	}
}

func TestParseDecodesEveryModelledKeyword(t *testing.T) {
	s := mustParse(t, `{"type":"string","description":"test","title":"Test","enum":["a", "b"],"const":{ "a" : 1 },"nullable":true,"format":"uri"}`)
	if s.Type != TypeString || s.Description != "test" || s.Title != "Test" || !s.Nullable || s.Format != "uri" ||
		!s.HasConst || string(s.Const) != `{"a":1}` {
		t.Fatalf("schema = %+v", s)
	}
	if len(s.Enum) != 2 || string(s.Enum[0]) != `"a"` || string(s.Enum[1]) != `"b"` {
		t.Fatalf("Enum = %s", s.Enum)
	}
	o := mustParse(t, `{"type":["object","null"],"required":["a"],"additionalProperties":false,
		"properties":{"a":{"type":"array","items":{"type":"integer","minimum":1,"maximum":9,"multipleOf":2},"minItems":1,"maxItems":3,"uniqueItems":true},
		"b":{"anyOf":[{"type":"string","minLength":2,"maxLength":5,"pattern":"^x"},{"type":"null"}]},
		"c":{"oneOf":[{"type":"integer"},{"type":"boolean"}]},"d":{"allOf":[{"type":"number","exclusiveMinimum":0,"exclusiveMaximum":1}]},
		"e":{"additionalProperties":{"type":"integer"}},"f":{"additionalProperties":true},"n":{"type":["null"]}}}`)
	if o.Type != TypeObject || !o.Nullable || !reflect.DeepEqual(o.Required, []string{"a"}) ||
		o.AdditionalProperties == nil || o.AdditionalProperties.Allowed || o.AdditionalProperties.Schema != nil {
		t.Fatalf("object = %+v", o)
	}
	a := o.Properties["a"]
	if a.Type != TypeArray || *a.MinItems != 1 || *a.MaxItems != 3 || !*a.UniqueItems ||
		a.Items.Type != TypeInteger || *a.Items.Minimum != 1 || *a.Items.Maximum != 9 || *a.Items.MultipleOf != 2 {
		t.Fatalf("a = %+v, items %+v", a, a.Items)
	}
	if b := o.Properties["b"].AnyOf[0]; len(o.Properties["b"].AnyOf) != 2 || *b.MinLength != 2 || *b.MaxLength != 5 || b.Pattern != "^x" {
		t.Fatalf("b = %+v", b)
	}
	if d := o.Properties["d"].AllOf; len(o.Properties["c"].OneOf) != 2 || len(d) != 1 ||
		*d[0].ExclusiveMinimum != 0 || *d[0].ExclusiveMaximum != 1 {
		t.Fatalf("combinators = %+v %+v", o.Properties["c"], d)
	}
	if e := o.Properties["e"].AdditionalProperties; !e.Allowed || e.Schema == nil || e.Schema.Type != TypeInteger {
		t.Fatalf("e.additionalProperties = %+v", e)
	}
	if f := o.Properties["f"].AdditionalProperties; !f.Allowed || f.Schema != nil {
		t.Fatalf("f.additionalProperties = %+v", f)
	}
	if n := o.Properties["n"]; n.Type != TypeNull || n.Nullable {
		t.Fatalf(`["null"] alone is the null type, not a nullable nothing: %+v`, n)
	}
	// What Parse reads, MarshalJSON writes back: the round trip is stable.
	out := marshal(t, o)
	if again := marshal(t, mustParse(t, out)); again != out {
		t.Fatalf("round trip drifted:\n%s\n%s", out, again)
	}
}

func TestParseKeepsUnmodelledKeywordsCompactedInAuthoredOrder(t *testing.T) {
	s := mustParse(t, `{"type":"object","$comment":"note","x-custom":[ 1, 2 ],"deprecated":true}`)
	var keys []string
	for _, k := range s.Extra {
		keys = append(keys, k.Key)
	}
	if strings.Join(keys, ",") != "$comment,x-custom,deprecated" {
		t.Fatalf("Extra keys = %v, want them in authored order", keys)
	}
	if got := marshal(t, s); got != `{"type":"object","properties":{},"$comment":"note","x-custom":[1,2],"deprecated":true}` {
		t.Fatalf("marshal = %s", got)
	}
}

func TestParseRejectsMalformedOrMistypedSchemas(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"truncated", `{"type":"object","properties":{`, ""},
		{"truncated value", `{"type":`, ""},
		{"not an object", `[1,2]`, "want an object"},
		{"trailing data", `{"type":"object"} trailing`, "trailing data"},
		{"trailing value", `{} {}`, "trailing data"},
		{"items not a schema", `{"items":3}`, "items: want an object"},
		{"description not a string", `{"description":3}`, "description"},
		{"two types besides null", `{"type":["string","integer"]}`, "one type besides null"},
		{"type neither name nor list", `{"type":3}`, "want a type name"},
		{"enum not an array", `{"enum":"a"}`, "enum: want an array"},
		{"properties not an object", `{"properties":[]}`, "properties: want an object"},
		{"property not a schema", `{"properties":{"a":1}}`, "properties/a"},
		{"anyOf not an array", `{"anyOf":{}}`, "anyOf: want an array"},
		{"anyOf element not a schema", `{"oneOf":[{},1]}`, "oneOf/1"},
		{"additionalProperties not a schema", `{"additionalProperties":1}`, "additionalProperties"},
		{"duplicate key", `{"type":"string","type":"string"}`, "duplicate"},
		{"bad const", `{"const":}`, ""},
		{"bad extra", `{"x":}`, ""},
		{"bad enum element", `{"enum":[,]}`, ""},
		{"bad property name", `{"properties":{1:{}}}`, ""},
		{"bad type value", `{"type":}`, ""},
		{"bad keyword value", `{"minimum":}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Parse([]byte(tc.src))
			if s != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%s) = %v, %v; want nil and an error mentioning %q", tc.src, s, err, tc.want)
			}
		})
	}
}
