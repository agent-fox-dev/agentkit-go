package codemode

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Data becomes Starlark values through JSON, dict keys sorted so a script
// iterates them the same way every run; an integer too big for int64 stays
// exact.
func TestToStarlarkConvertsWhatJSONCarries(t *testing.T) {
	type tagged struct {
		Name string `json:"name"`
	}
	for _, tc := range []struct {
		in   any
		want string
	}{
		{map[string]any{"b": 1, "a": []int{1, 2}, "c": nil, "d": true, "e": 1.5}, `{"a": [1, 2], "b": 1, "c": None, "d": True, "e": 1.5}`},
		{json.Number("123456789012345678901234567890"), "123456789012345678901234567890"},
		{tagged{Name: "x"}, `{"name": "x"}`},
		{"s", `"s"`},
	} {
		v, err := toStarlark(tc.in)
		if err != nil || v.String() != tc.want {
			t.Errorf("toStarlark(%v) = %v, %v; want %s", tc.in, v, err, tc.want)
		}
	}
	for _, bad := range []any{func() {}, json.Number("1e999")} {
		if v, err := toStarlark(bad); err == nil {
			t.Errorf("toStarlark(%v) = %v, want an error", bad, v)
		}
	}
}

// A Starlark value becomes the Go value JSON encodes it as; one JSON cannot
// carry is an error, a list or dict that contains itself among them, since
// the conversion recurses outside the script's step budget.
func TestToGoConvertsWhatJSONCanCarry(t *testing.T) {
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	d := starlark.NewDict(2)
	_ = d.SetKey(starlark.String("k"), starlark.Tuple{starlark.None, starlark.True, starlark.Float(0.5)})
	_ = d.SetKey(starlark.String("big"), starlark.MakeBigInt(huge))
	got, err := toGo(starlark.NewList([]starlark.Value{d, starlark.MakeInt(7), starlark.String("s"),
		NewToolError(core.ErrResult("nope", "gone"))}))
	must(t, err)
	want := `[{"big":123456789012345678901234567890,"k":[null,true,0.5]},7,"s",` +
		`{"data":null,"detail":"gone","error":"nope","is_error":true,"ok":false}]`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Fatalf("toGo = %s\nwant   %s", b, want)
	}

	intKey := starlark.NewDict(1)
	_ = intKey.SetKey(starlark.MakeInt(1), starlark.None)
	loop := starlark.NewList(nil)
	_ = loop.Append(loop)
	self := starlark.NewDict(1)
	_ = self.SetKey(starlark.String("self"), self)
	for _, tc := range []struct {
		in     starlark.Value
		detail string
	}{
		{starlark.Float(math.Inf(1)), "no infinities or NaN"},
		{starlark.Float(math.NaN()), "no infinities or NaN"},
		{intKey, "dict keys must be strings, got int"},
		{starlark.NewSet(0), "cannot pass a set"},
		{loop, "cannot pass a list that contains itself"},
		{self, "cannot pass a dict that contains itself"},
		{starlark.Tuple{loop}, "contains itself"},
	} {
		if v, err := toGo(tc.in); err == nil || !strings.Contains(err.Error(), tc.detail) {
			t.Errorf("toGo(%s) = %v, %v; want %q", tc.in.Type(), v, err, tc.detail)
		}
	}
	// The same list twice is not a cycle.
	shared := starlark.NewList([]starlark.Value{starlark.MakeInt(1)})
	if _, err := toGo(starlark.Tuple{shared, shared}); err != nil {
		t.Fatalf("a shared list: %v", err)
	}
}

// A cyclic value is refused as a return value as it is as an argument.
func TestACyclicReturnValueIsRefused(t *testing.T) {
	for _, script := range []string{"a = []\na.append(a)\nresult = a\n", "d = {}\nd['self'] = d\nresult = d\n"} {
		if res := runScript(t, context.Background(), Options{}, caller(nil), script); res.OK || !strings.Contains(res.Detail, "contains itself") {
			t.Fatalf("%q: result = %+v", script, res)
		}
	}
}
