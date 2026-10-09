package wire_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/wire"
)

// The tests in this file pin the Rule and Path of every rejection, and the
// timing of the Validator hook, so a change of decoder underneath cannot move
// either without a test noticing.

func wireErr(t *testing.T, err error) *wire.Error {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) {
		t.Fatalf("err = %v (%T), want a *wire.Error", err, err)
	}
	if !errors.Is(err, wire.ErrRejected) {
		t.Fatalf("%v does not match ErrRejected", err)
	}
	return we
}

func TestDecodeRejectionsNameTheirRuleAndPath(t *testing.T) {
	deepObj := strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70)
	deepArr := strings.Repeat(`[`, 70) + strings.Repeat(`]`, 70)
	cases := []struct {
		in   string
		lim  wire.Limits
		rule wire.Rule
		path string // "" means the path is not pinned
	}{
		{`{"a":1,"a":2}`, wire.Limits{}, wire.RuleDuplicateKey, "$.a"},
		{`{"x":{"dup":1,"dup":2}}`, wire.Limits{}, wire.RuleDuplicateKey, "$.x.dup"},
		{`[{"a":1},{"b":1,"b":2}]`, wire.Limits{}, wire.RuleDuplicateKey, "$[1].b"},
		{deepObj, wire.Limits{MaxDepth: 64}, wire.RuleDepth, "$" + strings.Repeat(".a", 64)},
		{deepArr, wire.Limits{MaxDepth: 64}, wire.RuleDepth, "$" + strings.Repeat("[0]", 64)},
		{`[1,2,3]`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$"},
		{`{"a":1,"b":2,"c":3}`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$"},
		{`{"x":[1,2,3]}`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$.x"},
		{`[[1,2,3],1,2]`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$[0]"},
		{`"` + strings.Repeat("x", 64) + `"`, wire.Limits{MaxMessageBytes: 16}, wire.RuleMessageBytes, "$"},
		{`[1,2,3,4,5,6,7,8,9,10,11]`, wire.Limits{MaxNodes: 10}, wire.RuleNodes, "$[9]"},
		{`{"a":[1,2,3],"b":[4,5,6],"c":{"d":7}}`, wire.Limits{MaxNodes: 10}, wire.RuleNodes, "$.c.d"},
		// Nodes are counted before the value's container is entered.
		{`[[[[1]]]]`, wire.Limits{MaxNodes: 2, MaxDepth: 2}, wire.RuleNodes, "$[0][0]"},
		{"{\"k\":\"\xff\"}", wire.Limits{}, wire.RuleSyntax, "$.k"},
		{"{\"\xff\":1}", wire.Limits{}, wire.RuleSyntax, "$"},
		{"[\"ok\",\"a\\n\xff\"]", wire.Limits{}, wire.RuleSyntax, "$[1]"},
		{`{"a":"\q"}`, wire.Limits{}, wire.RuleSyntax, "$.a"},
		{`{"a":"\u00"}`, wire.Limits{}, wire.RuleSyntax, "$.a"},
		{`{"a":1}{"b":2}`, wire.Limits{}, wire.RuleSyntax, "$"},
		{`01`, wire.Limits{}, wire.RuleSyntax, "$"},
		{`1 2`, wire.Limits{}, wire.RuleSyntax, "$"},
		{``, wire.Limits{}, wire.RuleSyntax, "$"},
		{`  `, wire.Limits{}, wire.RuleSyntax, "$"},
		{`{`, wire.Limits{}, wire.RuleSyntax, ""},
		{`[1,]`, wire.Limits{}, wire.RuleSyntax, ""},
		{`{"a":1,}`, wire.Limits{}, wire.RuleSyntax, ""},
		{`{"a" 1}`, wire.Limits{}, wire.RuleSyntax, ""},
		{`[1 2]`, wire.Limits{}, wire.RuleSyntax, ""},
		{`{1:2}`, wire.Limits{}, wire.RuleSyntax, ""},
		{`nul`, wire.Limits{}, wire.RuleSyntax, ""},
		{`-`, wire.Limits{}, wire.RuleSyntax, ""},
		{`1.`, wire.Limits{}, wire.RuleSyntax, ""},
		{`+1`, wire.Limits{}, wire.RuleSyntax, ""},
		{`NaN`, wire.Limits{}, wire.RuleSyntax, ""},
		{"\"a\x01\"", wire.Limits{}, wire.RuleSyntax, ""},
	}
	for _, c := range cases {
		gerr := wire.Guard([]byte(c.in), c.lim)
		_, perr := wire.Parse([]byte(c.in), c.lim)
		for name, err := range map[string]error{"Guard": gerr, "Parse": perr} {
			if err == nil {
				t.Errorf("%s accepted %q", name, clip(c.in))
				continue
			}
			we := wireErr(t, err)
			if we.Rule != c.rule || (c.path != "" && we.Path != c.path) {
				t.Errorf("%s(%q) = %s at %s, want %s at %s (%v)", name, clip(c.in),
					we.Rule, we.Path, c.rule, c.path, err)
			}
		}
	}
}

func TestDecodeAcceptsWhatTheProtocolAllows(t *testing.T) {
	for _, in := range []string{
		` {"a" : [ 1 , -0.5e+3 , true , false , null , "\/\b\f\n\r\t\"\\" ] } `,
		`{"":1,"\ud83d":2}`, `[]`, `{}`, `-0`, `1E-2`, "\" \"",
		// A raw U+FFFD is valid UTF-8 and must not be confused with a mangled byte.
		"\"\xef\xbf\xbd\"",
	} {
		if err := wire.Guard([]byte(in), wire.Limits{}); err != nil {
			t.Errorf("Guard(%q): %v", in, err)
		}
		if _, err := wire.Parse([]byte(in), wire.Limits{}); err != nil {
			t.Errorf("Parse(%q): %v", in, err)
		}
	}
	// Exactly at each bound is legal.
	at := wire.Limits{MaxContainerLen: 3, MaxDepth: 2, MaxNodes: 7, MaxMessageBytes: 19}
	if err := wire.Guard([]byte(`[[1,2,3],{"a":1}]`), at); err != nil {
		t.Fatalf("a message exactly at every bound was rejected: %v", err)
	}
}

func TestParseDecodesEscapesAndSurrogates(t *testing.T) {
	v, err := wire.Parse([]byte(`{"kA":"😀|\ud800x|\udc00|\ud800A"}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.Get("kA")
	if !ok {
		t.Fatalf("escaped member name not decoded: %v", v.Keys)
	}
	if want := "\U0001F600|�x|�|�A"; got.String != want {
		t.Fatalf("got %q, want %q", got.String, want)
	}
}

func TestParseBuildsTheTree(t *testing.T) {
	v, err := wire.Parse([]byte(`{"s":"x","n":-1.50,"b":false,"z":null,"a":[{}, []],"o":{"k":true}}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != wire.KindObject || strings.Join(v.Keys, ",") != "s,n,b,z,a,o" || len(v.Object) != 6 {
		t.Fatalf("root = %+v", v)
	}
	if m, _ := v.Get("n"); m.Kind != wire.KindNumber || m.Number != "-1.50" {
		t.Fatalf("n = %+v", m)
	}
	if m, _ := v.Get("b"); m.Kind != wire.KindBool || m.Bool {
		t.Fatalf("b = %+v", m)
	}
	if m, _ := v.Get("z"); m.Kind != wire.KindNull {
		t.Fatalf("z = %+v", m)
	}
	a, _ := v.Get("a")
	if a.Kind != wire.KindArray || len(a.Array) != 2 || a.Array[0].Kind != wire.KindObject ||
		a.Array[1].Kind != wire.KindArray {
		t.Fatalf("a = %+v", a)
	}
	if o, _ := v.Get("o"); o.Object["k"].Kind != wire.KindBool || !o.Object["k"].Bool {
		t.Fatalf("o = %+v", o)
	}
	if _, ok := a.Get("x"); ok {
		t.Fatal("Get on a non-object must report absence")
	}
	any, err := v.Any()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(any); got != "map[a:[map[] []] b:false n:-1.50 o:map[k:true] s:x z:<nil>]" {
		t.Fatalf("Any() = %s", got)
	}
}
