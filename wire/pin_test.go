package wire_test

import (
	"encoding/json"
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

// ------------------------------------------------------------------ Bind

type myInt int

type pinned struct {
	Name   string            `json:"name"`
	N8     int8              `json:"n8"`
	U      uint              `json:"u"`
	U8     uint8             `json:"u8"`
	F32    float32           `json:"f32"`
	F64    float64           `json:"f64"`
	Mine   myInt             `json:"mine"`
	Ints   map[string]int    `json:"ints"`
	List   []versioned       `json:"list"`
	Inner  *versioned        `json:"inner"`
	Raw    json.RawMessage   `json:"raw"`
	Strs   []string          `json:"strs"`
	Nested struct{ K int }   `json:"nested"`
	Iface  fmt.Stringer      `json:"iface"`
	Deep   map[string][]bool `json:"deep"`
}

func TestBindRejectionsNameTheirRuleAndPath(t *testing.T) {
	cases := []struct {
		in   string
		rule wire.Rule
		path string
	}{
		{`{"elevate":true}`, wire.RuleUnknownField, "$.elevate"},
		{`{"Name":"x"}`, wire.RuleUnknownField, "$.Name"},
		{`{"nested":{"k":1}}`, wire.RuleUnknownField, "$.nested.k"},
		{`{"list":[{"version":"2.0"},{"version":"2.0","x":1}]}`, wire.RuleUnknownField, "$.list[1].x"},
		{`{"name":5}`, wire.RuleType, "$.name"},
		{`{"name":{}}`, wire.RuleType, "$.name"},
		{`{"n8":"5"}`, wire.RuleType, "$.n8"},
		{`{"n8":1.5}`, wire.RuleType, "$.n8"},
		{`{"n8":300}`, wire.RuleRange, "$.n8"},
		{`{"n8":-129}`, wire.RuleRange, "$.n8"},
		{`{"u":-1}`, wire.RuleRange, "$.u"},
		{`{"u8":256.0}`, wire.RuleRange, "$.u8"},
		{`{"u":1e19}`, wire.RuleRange, "$.u"},
		{`{"u":18446744073709551615}`, wire.RuleRange, "$.u"},
		{`{"mine":1.5}`, wire.RuleType, "$.mine"},
		{`{"mine":-1e16}`, wire.RuleRange, "$.mine"},
		{`{"f32":1e300}`, wire.RuleRange, "$.f32"},
		{`{"f64":1e400}`, wire.RuleType, "$.f64"},
		{`{"f64":"1"}`, wire.RuleType, "$.f64"},
		{`{"ints":{"a":1,"b":"x"}}`, wire.RuleType, "$.ints.b"},
		{`{"ints":[]}`, wire.RuleType, "$.ints"},
		{`{"strs":["a",1]}`, wire.RuleType, "$.strs[1]"},
		{`{"strs":{}}`, wire.RuleType, "$.strs"},
		{`{"deep":{"x":[true,1]}}`, wire.RuleType, "$.deep.x[1]"},
		{`{"list":{}}`, wire.RuleType, "$.list"},
		{`{"list":[1]}`, wire.RuleType, "$.list[0]"},
		{`{"iface":"x"}`, wire.RuleType, "$.iface"},
		{`[]`, wire.RuleType, "$"},
		{`"x"`, wire.RuleType, "$"},
		{`{"inner":{"version":"1.0"}}`, wire.RuleValidator, "$.inner"},
		{`{"list":[{"version":"2.0"},{"version":"1.0"}]}`, wire.RuleValidator, "$.list[1]"},
	}
	for _, c := range cases {
		v, err := wire.Parse([]byte(c.in), wire.Limits{})
		if err != nil {
			t.Fatalf("Parse(%s): %v", c.in, err)
		}
		var p pinned
		err = wire.Bind(v, &p)
		if err == nil {
			t.Errorf("Bind(%s) accepted", c.in)
			continue
		}
		we := wireErr(t, err)
		if we.Rule != c.rule || we.Path != c.path {
			t.Errorf("Bind(%s) = %s at %s, want %s at %s (%v)", c.in, we.Rule, we.Path,
				c.rule, c.path, err)
		}
	}
}

func TestBindAcceptsWhatTheContractAllows(t *testing.T) {
	const in = `{"name":"x","n8":-128,"u":9007199254740991,"u8":2.55e2,"f32":2,"f64":-1.5e-3,` +
		`"mine":3e1,"ints":{"a":1.0,"b":-2},"list":[{"version":"2.0"}],"inner":{"version":"2.0"},` +
		`"raw":[1, 2.50],"strs":["\ud800"],"nested":{"K":4},"deep":{"x":[true]}}`
	v, err := wire.Parse([]byte(in), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var p pinned
	if err := wire.Bind(v, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "x" || p.N8 != -128 || p.U != 9007199254740991 || p.U8 != 255 || p.F32 != 2 ||
		p.F64 != -1.5e-3 || p.Mine != 30 || p.Ints["a"] != 1 || p.Ints["b"] != -2 ||
		len(p.List) != 1 || p.Inner == nil || p.Inner.Version != "2.0" || string(p.Raw) != `[1,2.50]` ||
		p.Strs[0] != "�" || p.Nested.K != 4 || !p.Deep["x"][0] {
		t.Fatalf("bound %+v", p)
	}
}

// TestBindNullZeroesEveryField: a pre-filled target is overwritten by
// what the peer sent, and an explicit null zeroes every kind of field.
func TestBindNullZeroesEveryField(t *testing.T) {
	v, err := wire.Parse([]byte(`{"name":null,"n8":null,"f64":null,"mine":null,"ints":null,`+
		`"list":null,"inner":null,"raw":null,"strs":null,"deep":null}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	p := pinned{Name: "a", N8: 1, F64: 1, Mine: 1, Ints: map[string]int{"a": 1},
		List: []versioned{{}}, Inner: &versioned{}, Raw: json.RawMessage(`1`), Strs: []string{"a"},
		Deep: map[string][]bool{}}
	if err := wire.Bind(v, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "" || p.N8 != 0 || p.F64 != 0 || p.Mine != 0 || p.Ints != nil || p.List != nil ||
		p.Inner != nil || p.Raw != nil || p.Strs != nil || p.Deep != nil {
		t.Fatalf("null left %+v", p)
	}
	var root *versioned
	if err := wire.Bind(wire.Value{Kind: wire.KindNull}, &root); err != nil || root != nil {
		t.Fatalf("null root: %v, %v", root, err)
	}
}

func TestBindIntoUntypedTargets(t *testing.T) {
	v, err := wire.Parse([]byte(`{"a":[1,1.0,"s",true,null,{"b":{}}]}`), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var target any
	if err := wire.Bind(v, &target); err != nil {
		t.Fatal(err)
	}
	m := target.(map[string]any)
	arr := m["a"].([]any)
	if arr[0] != json.Number("1") || arr[1] != json.Number("1.0") || arr[2] != "s" ||
		arr[3] != true || arr[4] != nil || fmt.Sprint(arr[5]) != "map[b:map[]]" {
		t.Fatalf("got %#v", arr)
	}
	// An untyped field deep inside a message bound with wider limits than the
	// defaults still binds: the untyped projection is not re-bounded.
	deep := strings.Repeat(`[`, 80) + strings.Repeat(`]`, 80)
	v, err = wire.Parse([]byte(`{"x":`+deep+`}`), wire.Limits{MaxDepth: 100})
	if err != nil {
		t.Fatal(err)
	}
	var holder struct {
		X any `json:"x"`
	}
	if err := wire.Bind(v, &holder); err != nil {
		t.Fatalf("deep untyped value: %v", err)
	}
}

// ---- Validator timing (REQ-SEC-12.3)

type order struct {
	Tag string    `json:"tag"`
	Sub *orderSub `json:"sub"`
}

type orderSub struct {
	Tag string `json:"tag"`
}

var validationLog []string

func (o *order) Validate() error {
	validationLog = append(validationLog, "order:"+o.Tag)
	if o.Tag == "bad" {
		return errors.New("order is bad")
	}
	return nil
}

func (o orderSub) Validate() error {
	validationLog = append(validationLog, "sub:"+o.Tag)
	if o.Tag == "bad" {
		return errors.New("sub is bad")
	}
	return nil
}

func bindOrder(t *testing.T, in string, target any) error {
	t.Helper()
	validationLog = nil
	v, err := wire.Parse([]byte(in), wire.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return wire.Bind(v, target)
}

func TestTheValidatorRunsOncePerFilledStructInnermostFirst(t *testing.T) {
	var o order
	if err := bindOrder(t, `{"sub":{"tag":"s"},"tag":"o"}`, &o); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(validationLog, ","); got != "sub:s,order:o" {
		t.Fatalf("validation order = %s; each struct is validated once, after its own fields", got)
	}

	// The inner failure is reported at its path and the outer never runs.
	err := bindOrder(t, `{"sub":{"tag":"bad"},"tag":"bad"}`, &o)
	we := wireErr(t, err)
	if we.Rule != wire.RuleValidator || we.Path != "$.sub" || !strings.Contains(we.Msg, "sub is bad") {
		t.Fatalf("got %v", err)
	}
	if got := strings.Join(validationLog, ","); got != "sub:bad" {
		t.Fatalf("validation log = %s", got)
	}
	if !errors.Is(err, wire.ErrRejected) || errors.Unwrap(err) == nil ||
		errors.Unwrap(err).Error() != "sub is bad" {
		t.Fatalf("the validator's own error must be wrapped: %v", errors.Unwrap(err))
	}

	// The root failure is at $.
	we = wireErr(t, bindOrder(t, `{"tag":"bad"}`, &o))
	if we.Rule != wire.RuleValidator || we.Path != "$" {
		t.Fatalf("got %v", we)
	}

	// A validator failure earlier in the message wins over a type error later.
	var list struct {
		L []orderSub `json:"l"`
	}
	we = wireErr(t, bindOrder(t, `{"l":[{"tag":"bad"},{"tag":5}]}`, &list))
	if we.Rule != wire.RuleValidator || we.Path != "$.l[0]" {
		t.Fatalf("got %v", we)
	}
	// And a type error inside a struct wins over that struct's validator.
	we = wireErr(t, bindOrder(t, `{"tag":"bad","sub":{"tag":5}}`, &o))
	if we.Rule != wire.RuleType || we.Path != "$.sub.tag" {
		t.Fatalf("got %v", we)
	}
}

func TestTheValidatorDoesNotRunForAbsentOrNullStructs(t *testing.T) {
	var o order
	if err := bindOrder(t, `{"tag":"o"}`, &o); err != nil {
		t.Fatal(err)
	}
	if err := bindOrder(t, `{"tag":"o","sub":null}`, &o); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(validationLog, ","); got != "order:o" {
		t.Fatalf("validation log = %s; a null or absent struct is not validated", got)
	}
	var e envelope
	if err := bindOrder(t, `{}`, &e); err != nil {
		t.Fatalf("an absent struct is not validated: %v", err)
	}
	if err := bindOrder(t, `{"inner":null}`, &e); err != nil {
		t.Fatalf("a null struct is not validated: %v", err)
	}
}
