package wire

import (
	"encoding/json"
	"strings"
)

// Kind is a JSON value's type.
type Kind uint8

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

func (k Kind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindBool:
		return "bool"
	case KindNumber:
		return "number"
	case KindString:
		return "string"
	case KindArray:
		return "array"
	}
	return "object"
}

// Value is one decoded JSON value.
//
// Numbers are held as json.Number — the VERBATIM literal — so 1, 1.0 and 1e3
// stay distinguishable. REQ-SEC-12.2 needs that distinction to implement
// cross-language number semantics correctly, and NFR-TEST-06.4 needs it to
// diff a request body without laundering it through a float64.
type Value struct {
	Kind   Kind
	Bool   bool
	Number json.Number
	String string
	Array  []Value
	// Object holds members. Duplicate keys are REJECTED at decode
	// (REQ-SEC-11.3), so a map is safe here: there is never a second value to
	// lose.
	Object map[string]Value
	// Keys is insertion order, for deterministic error messages and for a
	// caller that needs to know what the peer actually sent.
	Keys []string
}

// Get returns a member.
func (v Value) Get(key string) (Value, bool) {
	if v.Kind != KindObject {
		return Value{}, false
	}
	m, ok := v.Object[key]
	return m, ok
}

// Any materializes the tree as ordinary Go values: map[string]any, []any,
// json.Number, string, bool, nil.
//
// Numbers come back as json.Number, never float64. A tool argument of
// 9007199254740993 that round-trips through a float64 comes back as
// 9007199254740992, and nothing downstream can tell.
func (v Value) Any() (any, error) {
	switch v.Kind {
	case KindNull:
		return nil, nil
	case KindBool:
		return v.Bool, nil
	case KindNumber:
		return v.Number, nil
	case KindString:
		return v.String, nil
	case KindArray:
		out := make([]any, len(v.Array))
		for i := range v.Array {
			a, err := v.Array[i].Any()
			if err != nil {
				return nil, err
			}
			out[i] = a
		}
		return out, nil
	case KindObject:
		out := make(map[string]any, len(v.Keys))
		for _, k := range v.Keys {
			a, err := v.Object[k].Any()
			if err != nil {
				return nil, err
			}
			out[k] = a
		}
		return out, nil
	}
	return nil, failf(RuleType, "$", "unknown kind %d", v.Kind)
}

// JSON re-encodes the tree, preserving member order and number literals.
func (v Value) JSON() ([]byte, error) {
	var b strings.Builder
	if err := v.writeJSON(&b); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func (v Value) writeJSON(b *strings.Builder) error {
	switch v.Kind {
	case KindNull:
		b.WriteString("null")
	case KindBool:
		if v.Bool {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case KindNumber:
		if v.Number == "" {
			b.WriteString("0")
			return nil
		}
		b.WriteString(string(v.Number))
	case KindString:
		enc, err := json.Marshal(v.String)
		if err != nil {
			return err
		}
		b.Write(enc)
	case KindArray:
		b.WriteByte('[')
		for i := range v.Array {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := v.Array[i].writeJSON(b); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case KindObject:
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			enc, err := json.Marshal(k)
			if err != nil {
				return err
			}
			b.Write(enc)
			b.WriteByte(':')
			if err := v.Object[k].writeJSON(b); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	}
	return nil
}
