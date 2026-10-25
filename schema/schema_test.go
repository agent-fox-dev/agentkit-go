package schema

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestObjectKeepsFieldOrderAndRequiredOnce(t *testing.T) {
	s := Object(Prop("b", String()), Opt("a", Int()), Prop("b", Bool()), Prop("c", Number()))
	if !reflect.DeepEqual(s.PropertyOrder, []string{"b", "a", "c"}) || !reflect.DeepEqual(s.Required, []string{"b", "c"}) {
		t.Fatalf("order %v, required %v", s.PropertyOrder, s.Required)
	}
	if s.Type != TypeObject || s.Properties["b"].Type != TypeBoolean {
		t.Fatalf("a repeated field must keep its first position and take its last schema: %+v", s.Properties["b"])
	}
	if !s.IsRequired("b") || s.IsRequired("a") {
		t.Fatal("IsRequired disagrees with Prop and Opt")
	}
}

func TestBuildersAndModifiersMarshalToTheirKeywords(t *testing.T) {
	for _, tc := range []struct {
		s    *Schema
		want string
	}{
		{String(), `{"type":"string"}`},
		{String("a name").Nullable_(), `{"type":["string","null"],"description":"a name"}`},
		{Int().Describe("count").Min(1).Max(9), `{"type":"integer","description":"count","minimum":1,"maximum":9}`},
		{Number("n"), `{"type":"number","description":"n"}`},
		{Bool(), `{"type":"boolean"}`},
		{Array(String(), "tags").MinItemsN(1), `{"type":"array","description":"tags","items":{"type":"string"},"minItems":1}`},
		{Enum("m", "GET", "POST"), `{"type":"string","description":"m","enum":["GET","POST"]}`},
		{AnyOf(String(), Int()), `{"anyOf":[{"type":"string"},{"type":"integer"}]}`},
		{OneOf(Bool()), `{"oneOf":[{"type":"boolean"}]}`},
		{Const("v1"), `{"const":"v1"}`},
		{ConstNull(), `{"const":null}`},
		{Object().Closed(), `{"type":"object","properties":{},"additionalProperties":false}`},
		{String().WithExtra("x-a", 1).WithExtra("x-b", "two").WithExtra("x-a", true),
			`{"type":"string","x-a":true,"x-b":"two"}`},
	} {
		if got := marshal(t, tc.s); got != tc.want {
			t.Errorf("marshal = %s, want %s", got, tc.want)
		}
	}
}

func TestWithExtraPanicsOnAValueJSONCannotCarry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), `WithExtra("x")`) {
			t.Fatalf("recover() = %v, want a panic naming the keyword", r)
		}
	}()
	String().WithExtra("x", math.NaN())
}

func TestExtraGetDecodesAPresentKeyword(t *testing.T) {
	e := Extra{{Key: "a", Value: json.RawMessage(`{"b":[1]}`)}, {Key: "bad", Value: json.RawMessage(`{`)}}
	if v, ok := e.Get("a"); !ok || !reflect.DeepEqual(v, map[string]any{"b": []any{1.0}}) {
		t.Errorf("Get(a) = %v, %v", v, ok)
	}
	if _, ok := e.Get("bad"); ok {
		t.Error("an undecodable value reported present")
	}
	if _, ok := e.Get("missing"); ok {
		t.Error("a missing keyword reported present")
	}
}

func TestPropertyListCompletesTheOrderWithUnlistedPropertiesSorted(t *testing.T) {
	s := &Schema{
		Properties:    map[string]*Schema{"z": String(), "m": String(), "b": String(), "a": String()},
		PropertyOrder: []string{"m", "gone", "m", "z"},
	}
	if got := s.PropertyList(); !reflect.DeepEqual(got, []string{"m", "z", "a", "b"}) {
		t.Fatalf("PropertyList() = %v", got)
	}
	if (*Schema)(nil).PropertyList() != nil {
		t.Fatal("a nil schema has no properties")
	}
}

func TestCloneIsADeepCopy(t *testing.T) {
	orig := Object(Prop("a", Array(String())), Opt("b", AnyOf(Int())))
	orig.OneOf = []*Schema{Bool()}
	orig.AllOf = []*Schema{Number()}
	orig.WithExtra("x", 1)
	before := marshal(t, orig)

	c := orig.Clone()
	c.Properties["a"].Items.Type = TypeInteger
	c.Properties["b"].AnyOf[0].Type = TypeString
	c.OneOf[0].Type, c.AllOf[0].Type = TypeNull, TypeNull
	c.PropertyOrder[0], c.Required[0] = "q", "q"
	c.Extra[0].Value[0] = '2'
	c.Properties["new"] = String()
	if after := marshal(t, orig); after != before {
		t.Fatalf("mutating the clone changed the original:\n%s\n%s", before, after)
	}
	if (*Schema)(nil).Clone() != nil {
		t.Fatal("Clone of nil must be nil")
	}
}

func TestMarshalJSONWritesTheFixedKeywordOrderThenExtra(t *testing.T) {
	unique := true
	s := &Schema{
		Type: TypeObject, Title: "T", Description: "D",
		Properties:           map[string]*Schema{"b": nil, "a": {Type: TypeString, MinLength: intp(1), MaxLength: intp(2), Pattern: "^x", Format: "email"}},
		PropertyOrder:        []string{"b", "a"},
		Required:             []string{},
		AdditionalProperties: &AdditionalProperties{Allowed: true, Schema: Int()},
		Items:                &Schema{Type: TypeNumber, ExclusiveMinimum: floatp(0), ExclusiveMaximum: floatp(1.5), MultipleOf: floatp(0.25)},
		Enum:                 []json.RawMessage{json.RawMessage(`1`)},
		Const:                json.RawMessage(`1`), HasConst: true,
		AllOf:    []*Schema{Bool()},
		MinItems: intp(0), MaxItems: intp(3), UniqueItems: &unique,
		Extra: Extra{{Key: "x-z", Value: json.RawMessage(`0`)}, {Key: "$comment", Value: json.RawMessage(`"c"`)}},
	}
	want := `{"type":"object","title":"T","description":"D",` +
		`"properties":{"b":null,"a":{"type":"string","minLength":1,"maxLength":2,"pattern":"^x","format":"email"}},` +
		`"required":[],"additionalProperties":{"type":"integer"},` +
		`"items":{"type":"number","exclusiveMinimum":0,"exclusiveMaximum":1.5,"multipleOf":0.25},` +
		`"enum":[1],"const":1,"allOf":[{"type":"boolean"}],"minItems":0,"maxItems":3,"uniqueItems":true,` +
		`"x-z":0,"$comment":"c"}`
	if got := marshal(t, s); got != want {
		t.Fatalf("marshal =\n%s\nwant\n%s", got, want)
	}
	if b, _ := (*Schema)(nil).MarshalJSON(); string(b) != "null" {
		t.Fatalf("nil schema marshals as %s", b)
	}
	if got := marshal(t, &Schema{AdditionalProperties: &AdditionalProperties{Allowed: true}}); got != `{"additionalProperties":true}` {
		t.Fatalf("bare additionalProperties = %s", got)
	}
}
