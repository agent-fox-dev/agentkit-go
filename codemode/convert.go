package codemode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"

	"go.starlark.net/starlark"
)

// toStarlark converts a tool result's Data, or any Go value JSON can carry,
// into Starlark values: dicts, lists, strings, ints, floats, bools and None.
//
// It goes through JSON because Data holds whatever the tool put there —
// typed slices, structs with json tags, json.Number — and JSON is the shape
// the tool's output schema describes. Dict keys come out sorted, so a script
// that iterates a dict sees the same order every run.
func toStarlark(v any) (starlark.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return fromJSON(generic)
}

func fromJSON(v any) (starlark.Value, error) {
	switch x := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(x), nil
	case string:
		return starlark.String(x), nil
	case json.Number:
		if i, ok := new(big.Int).SetString(string(x), 10); ok {
			return starlark.MakeBigInt(i), nil
		}
		f, err := x.Float64()
		if err != nil {
			return nil, err
		}
		return starlark.Float(f), nil
	case []any:
		elems := make([]starlark.Value, len(x))
		for i, e := range x {
			sv, err := fromJSON(e)
			if err != nil {
				return nil, err
			}
			elems[i] = sv
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := starlark.NewDict(len(x))
		for _, k := range keys {
			sv, err := fromJSON(x[k])
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), sv); err != nil {
				return nil, err
			}
		}
		return d, nil
	}
	return nil, fmt.Errorf("cannot convert %T", v)
}

// toGo converts a Starlark value into the Go value JSON encodes it as: the
// arguments a script passes to a tool, and the value it returns. A value JSON
// cannot carry — a function, a set, a list or dict that contains itself — is
// an error. The cycle check matters: the conversion recurses in Go, outside
// the script's step budget, and a Go stack overflow cannot be recovered.
func toGo(v starlark.Value) (any, error) { return toGoIn(v, map[starlark.Value]bool{}) }

func toGoIn(v starlark.Value, open map[starlark.Value]bool) (any, error) {
	switch v.(type) {
	case *starlark.List, *starlark.Dict:
		if open[v] {
			return nil, fmt.Errorf("cannot pass a %s that contains itself", v.Type())
		}
		open[v] = true
		defer delete(open, v)
	}
	switch x := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(x), nil
	case starlark.String:
		return string(x), nil
	case starlark.Int:
		if i, ok := x.Int64(); ok {
			return i, nil
		}
		return json.Number(x.String()), nil
	case starlark.Float:
		f := float64(x)
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("cannot pass %s: JSON has no infinities or NaN", x.String())
		}
		return f, nil
	case *starlark.List:
		return seqToGo(x, open)
	case starlark.Tuple:
		return seqToGo(x, open)
	case *starlark.Dict:
		out := make(map[string]any, x.Len())
		for _, item := range x.Items() {
			k, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings, got %s", item[0].Type())
			}
			gv, err := toGoIn(item[1], open)
			if err != nil {
				return nil, err
			}
			out[string(k)] = gv
		}
		return out, nil
	case goValuer:
		return x.goValue(), nil
	}
	return nil, fmt.Errorf("cannot pass a %s to a tool or return it", v.Type())
}

// goValuer is a codemode value that knows its own Go form (ToolError).
type goValuer interface{ goValue() any }

func seqToGo(it starlark.Indexable, open map[starlark.Value]bool) ([]any, error) {
	out := make([]any, it.Len())
	for i := range out {
		gv, err := toGoIn(it.Index(i), open)
		if err != nil {
			return nil, err
		}
		out[i] = gv
	}
	return out, nil
}
