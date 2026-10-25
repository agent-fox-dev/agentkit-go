package wire_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/wire"
)

// duplicateAfter is an object of n distinct members followed by a repeat of
// the first, so a duplicate is caught however many members came before it.
func duplicateAfter(n int) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		fmt.Fprintf(&b, `"k%d":%d,`, i, i)
	}
	b.WriteString(`"k0":999}`)
	return b.String()
}

func TestGuardAndParseRejectWithTheRuleAndPathOfTheViolation(t *testing.T) {
	deepObj := strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70)
	deepArr := strings.Repeat(`[`, 70) + strings.Repeat(`]`, 70)
	cases := []struct {
		in   string
		lim  wire.Limits
		rule wire.Rule
		path string // "" leaves the path unchecked
	}{
		{`{"a":1,"a":2}`, wire.Limits{}, wire.RuleDuplicateKey, "$.a"},
		{`{"x":{"dup":1,"dup":2}}`, wire.Limits{}, wire.RuleDuplicateKey, "$.x.dup"},
		{`[{"a":1},{"b":1,"b":2}]`, wire.Limits{}, wire.RuleDuplicateKey, "$[1].b"},
		{duplicateAfter(16), wire.Limits{}, wire.RuleDuplicateKey, "$.k0"},
		{duplicateAfter(64), wire.Limits{}, wire.RuleDuplicateKey, "$.k0"},
		{deepObj, wire.Limits{MaxDepth: 64}, wire.RuleDepth, "$" + strings.Repeat(".a", 64)},
		{deepArr, wire.Limits{MaxDepth: 64}, wire.RuleDepth, "$" + strings.Repeat("[0]", 64)},
		{`[1,2,3]`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$"},
		{`{"a":1,"b":2,"c":3}`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$"},
		{`{"x":[1,2,3]}`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$.x"},
		{`[[1,2,3],1,2]`, wire.Limits{MaxContainerLen: 2}, wire.RuleContainerLen, "$[0]"},
		{`{"a":"` + strings.Repeat("x", 64) + `"}`, wire.Limits{MaxMessageBytes: 16}, wire.RuleMessageBytes, "$"},
		{`[1,2,3,4,5,6,7,8,9,10,11]`, wire.Limits{MaxNodes: 10}, wire.RuleNodes, "$[9]"},
		{`{"a":[1,2,3],"b":[4,5,6],"c":{"d":7}}`, wire.Limits{MaxNodes: 10}, wire.RuleNodes, "$.c.d"},
		{`[[[[1]]]]`, wire.Limits{MaxNodes: 2, MaxDepth: 2}, wire.RuleNodes, "$[0][0]"},
		{"{\"k\":\"\xff\"}", wire.Limits{}, wire.RuleSyntax, "$.k"},
		{"{\"\xff\":1}", wire.Limits{}, wire.RuleSyntax, "$"},
		{"[\"ok\",\"a\\n\xff\"]", wire.Limits{}, wire.RuleSyntax, "$[1]"},
		{"[\"\xc3\"]", wire.Limits{}, wire.RuleSyntax, "$[0]"},
		{`{"a":"\q"}`, wire.Limits{}, wire.RuleSyntax, "$.a"},
		{`{"a":"\u00"}`, wire.Limits{}, wire.RuleSyntax, "$.a"},
		{`{"a":1}{"b":2}`, wire.Limits{}, wire.RuleSyntax, "$"},
		{`01`, wire.Limits{}, wire.RuleSyntax, "$"},
		{`1 2`, wire.Limits{}, wire.RuleSyntax, "$"},
		{``, wire.Limits{}, wire.RuleSyntax, "$"},
		{`  `, wire.Limits{}, wire.RuleSyntax, "$"},
	}
	for _, s := range []string{`{`, `[`, `{"a"`, `{"a":}`, `{,}`, `[,]`, `tru`, `nul`, `-`, `1.`, `1e`,
		`"unterminated`, "\x00", "{\"a\":\"\x01\"}", `[1,]`, `{"a":1,}`, `{"a" 1}`, `[1 2]`, `{1:2}`,
		`+1`, `NaN`} {
		cases = append(cases, struct {
			in   string
			lim  wire.Limits
			rule wire.Rule
			path string
		}{s, wire.Limits{}, wire.RuleSyntax, ""})
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

func TestGuardAndParseAcceptWhatTheGrammarAllowsAndValuesExactlyAtEachBound(t *testing.T) {
	for _, c := range []struct {
		in  string
		lim wire.Limits
	}{
		{` {"a" : [ 1 , -0.5e+3 , true , false , null , "\/\b\f\n\r\t\"\\" ] } `, wire.Limits{}},
		{`{"":1,"\ud83d":2}`, wire.Limits{}},
		{`[]`, wire.Limits{}}, {`{}`, wire.Limits{}}, {`-0`, wire.Limits{}}, {`1E-2`, wire.Limits{}},
		{`{"a":"é😀é"}`, wire.Limits{}},
		{"\"\xef\xbf\xbd\"", wire.Limits{}}, // a raw U+FFFD is valid UTF-8
		{strings.Repeat(`{"a":`, 64) + `1` + strings.Repeat(`}`, 64), wire.Limits{MaxDepth: 64}},
		{`[1,2,3,4,5,6,7,8,9]`, wire.Limits{MaxNodes: 10}},
		{`[[1,2,3],{"a":1}]`, wire.Limits{MaxContainerLen: 3, MaxDepth: 2, MaxNodes: 7, MaxMessageBytes: 19}},
	} {
		if err := wire.Guard([]byte(c.in), c.lim); err != nil {
			t.Errorf("Guard(%q): %v", clip(c.in), err)
		}
		if _, err := wire.Parse([]byte(c.in), c.lim); err != nil {
			t.Errorf("Parse(%q): %v", clip(c.in), err)
		}
	}
}

// The node bound is not implied by the others: arrays at the default container
// length, within the default depth and size, still pass the default node count.
func TestGuardBoundsTheTotalNodeCountUnderTheOtherDefaults(t *testing.T) {
	d := wire.Defaults()
	per, arrays := d.MaxContainerLen, d.MaxNodes/d.MaxContainerLen+1
	inner := "[" + strings.Repeat("0,", per-1) + "0]"
	data := []byte("[" + strings.Repeat(inner+",", arrays-1) + inner + "]")
	if int64(len(data)) > d.MaxMessageBytes {
		t.Fatalf("the fixture is %d bytes, over MaxMessageBytes", len(data))
	}
	if we := wireErr(t, wire.Guard(data, wire.Limits{})); we.Rule != wire.RuleNodes {
		t.Fatalf("rule = %s, want nodes", we.Rule)
	}
}

func TestParseBuildsTheTreeWithVerbatimNumbersAndMemberOrder(t *testing.T) {
	v, err := wire.Parse([]byte(`{"zebra":1024,"apple":1024.0,"pear":1e3,"b":false,"t":true,"z":null,"a":[{},[]]}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != wire.KindObject || strings.Join(v.Keys, ",") != "zebra,apple,pear,b,t,z,a" || len(v.Object) != 7 {
		t.Fatalf("root = %+v", v)
	}
	for k, want := range map[string]string{"zebra": "1024", "apple": "1024.0", "pear": "1e3"} {
		if m := v.Object[k]; m.Kind != wire.KindNumber || string(m.Number) != want {
			t.Errorf("%s = %+v, want the literal %q", k, m, want)
		}
	}
	if b, tr, z := v.Object["b"], v.Object["t"], v.Object["z"]; b.Kind != wire.KindBool || b.Bool ||
		tr.Kind != wire.KindBool || !tr.Bool || z.Kind != wire.KindNull {
		t.Errorf("b = %+v, t = %+v, z = %+v", b, tr, z)
	}
	if a := v.Object["a"]; a.Kind != wire.KindArray || len(a.Array) != 2 ||
		a.Array[0].Kind != wire.KindObject || a.Array[1].Kind != wire.KindArray {
		t.Errorf("a = %+v", a)
	}
}

func TestParseUnescapesStringsAndReplacesUnpairedSurrogates(t *testing.T) {
	for in, want := range map[string]string{
		`"plain"`:           "plain",
		`"tab\there"`:       "tab\there",
		`"quote\"inside"`:   `quote"inside`,
		`"back\\slash"`:     `back\slash`,
		`"\u0041é"`:         "Aé",
		`"\ud83d\ude00"`:    "\U0001F600",
		`"\ud800x|\udc00|"`: "\uFFFDx|\uFFFD|",
		`"\ud83d"`:          "\uFFFD",
	} {
		v, err := wire.Parse([]byte(in), wire.Limits{})
		if err != nil || v.String != want {
			t.Errorf("Parse(%s) = %q, %v; want %q", in, v.String, err, want)
		}
	}
	v, err := wire.Parse([]byte(`{"k\u0041":"v"}`), wire.Limits{})
	if _, ok := v.Get("kA"); err != nil || !ok {
		t.Errorf("escaped member name not decoded: %v, %v", v.Keys, err)
	}
}

// FuzzGuardNeverPanics: no input panics, Guard and Parse agree, and a parsed
// tree re-encodes to a fixed point that parses again.
func FuzzGuardNeverPanics(f *testing.F) {
	for _, s := range []string{
		`{"a":1}`, `[1,2,3]`, `"x"`, `null`, `{"a":{"b":[1,{"c":null}]}}`,
		`{"a":1,"a":2}`, `{"a":"😀"}`, `{"a":"\ud800"}`, `1e309`,
		"\x00", `{`, `[`, `{"a":`, `"\u00"`, strings.Repeat(`[`, 100),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		l := wire.Limits{MaxMessageBytes: 1 << 16, MaxContainerLen: 1000, MaxDepth: 32}
		gerr := wire.Guard(b, l)
		v, perr := wire.Parse(b, l)
		if (gerr == nil) != (perr == nil) {
			t.Fatalf("Guard and Parse disagree on %q: guard=%v parse=%v", b, gerr, perr)
		}
		if perr != nil {
			return
		}
		out, err := v.JSON()
		if err != nil {
			t.Fatalf("re-encoding %q failed: %v", b, err)
		}
		v2, err := wire.Parse(out, l)
		if err != nil {
			t.Fatalf("re-encoded %q -> %q which no longer parses: %v", b, out, err)
		}
		out2, err := v2.JSON()
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("re-encoding is not a fixed point:\n once: %s\ntwice: %s (%v)", out, out2, err)
		}
		if _, err := v.Any(); err != nil {
			t.Fatalf("Any() on a parsed tree failed: %v", err)
		}
	})
}

var benchPayload = []byte(`{"type":"content_block_delta","index":0,"delta":` +
	`{"type":"text_delta","text":"a chunk of streamed model output, about this long"}}`)

// BenchmarkGuard is the cost of the extra scan a provider pays per SSE event.
func BenchmarkGuard(b *testing.B) {
	b.SetBytes(int64(len(benchPayload)))
	b.ReportAllocs()
	for b.Loop() {
		if err := wire.Guard(benchPayload, wire.Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUnmarshalSamePayload is the encoding/json baseline for BenchmarkGuard.
func BenchmarkUnmarshalSamePayload(b *testing.B) {
	b.SetBytes(int64(len(benchPayload)))
	b.ReportAllocs()
	for b.Loop() {
		var v struct {
			Type  string                `json:"type"`
			Delta struct{ Text string } `json:"delta"`
		}
		if err := json.Unmarshal(benchPayload, &v); err != nil {
			b.Fatal(err)
		}
	}
}
