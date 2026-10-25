package wire_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/wire"
)

func TestKindStringNamesEachKind(t *testing.T) {
	for k, want := range map[wire.Kind]string{wire.KindNull: "null", wire.KindBool: "bool",
		wire.KindNumber: "number", wire.KindString: "string", wire.KindArray: "array", wire.KindObject: "object"} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

func TestValueGetFindsAMemberOnlyOnAnObject(t *testing.T) {
	v, err := wire.Parse([]byte(`{"a":[1]}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := v.Get("a")
	if !ok || a.Kind != wire.KindArray {
		t.Fatalf("Get(a) = %+v, %v", a, ok)
	}
	if _, ok := v.Get("missing"); ok {
		t.Error("Get of a missing member reported it present")
	}
	if _, ok := a.Get("x"); ok {
		t.Error("Get on a non-object reported a member")
	}
}

func TestValueAnyMaterializesGoValuesWithNumbersVerbatim(t *testing.T) {
	v, err := wire.Parse([]byte(`{"s":"x","n":9007199254740993,"b":false,"z":null,"a":[{},[]],"o":{"k":true}}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Any()
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprint(got); s != "map[a:[map[] []] b:false n:9007199254740993 o:map[k:true] s:x z:<nil>]" {
		t.Fatalf("Any() = %s", s)
	}
	if n := got.(map[string]any)["n"]; n != json.Number("9007199254740993") {
		t.Fatalf("n = %#v, want the json.Number literal", n)
	}
	bad := wire.Value{Kind: wire.KindArray, Array: []wire.Value{{Kind: wire.Kind(99)}}}
	if _, err := bad.Any(); wireErr(t, err).Rule != wire.RuleType {
		t.Fatalf("Any() of an unknown kind = %v, want a type rejection", err)
	}
	bad = wire.Value{Kind: wire.KindObject, Keys: []string{"k"}, Object: map[string]wire.Value{"k": {Kind: wire.Kind(99)}}}
	if _, err := bad.Any(); err == nil {
		t.Fatal("Any() of an object holding an unknown kind succeeded")
	}
}

func TestValueJSONPreservesMemberOrderAndNumberLiterals(t *testing.T) {
	const in = `{"zebra":1024,"apple":[1,2.50,3e2],"nested":{"b":null,"a":true,"c":false},"s":"q\"<"}`
	v, err := wire.Parse([]byte(in), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := v.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"zebra":1024,"apple":[1,2.50,3e2],"nested":{"b":null,"a":true,"c":false},"s":"q\"\u003c"}`; string(out) != want {
		t.Fatalf("JSON() = %s, want %s", out, want)
	}
	// A hand-built number with no literal encodes as 0, not as an empty token.
	out, err = wire.Value{Kind: wire.KindArray, Array: []wire.Value{{Kind: wire.KindNumber}}}.JSON()
	if err != nil || string(out) != "[0]" {
		t.Fatalf("JSON() of an empty number = %s, %v", out, err)
	}
}
