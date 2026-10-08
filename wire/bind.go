package wire

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Validator is REQ-SEC-12.3's hook: it runs as soon as each struct is filled,
// for constraints the Go type shape cannot express — minLength, minimum,
// literal unions.
//
// It runs INSIDE the bind rather than after it, so a nested struct that fails
// its own constraint is reported at its own path. Validating only the root
// leaves the caller to find which of forty elements was wrong.
type Validator interface {
	Validate() error
}

// SafeInteger is REQ-SEC-12.2's bound: the largest integer an IEEE-754 double
// represents exactly.
const SafeInteger = 1<<53 - 1

// Bind maps a decoded tree onto a Go value, strictly (REQ-SEC-12), with
// encoding/json/v2.
//
// Strict means an unknown property is a REJECTION. A peer that can smuggle
// extra fields past the parser reaches code paths the schema was meant to
// gate, and the smuggled field is invisible in every review of the struct that
// was supposed to describe the message.
//
// Field names match EXACTLY — no case folding, which is v2's default.
// encoding/json v1 matches case-insensitively, and on this surface that is a
// hole: `id` and `Id` are two distinct keys, so duplicate-key rejection does
// not catch them, and case-insensitive matching then binds both to one field
// with the last one winning — the last-wins REQ-SEC-11.3 exists to prevent,
// reintroduced one layer up.
//
// v2 does the walk; the binder's unmarshal hook adds what v2 does not have:
// REQ-SEC-12.2's number rules, the Validator, json.Number in untyped fields,
// and a null RawMessage binding as nil.
func Bind(v Value, target any) error {
	data, err := v.JSON()
	if err != nil {
		return failf(RuleType, "$", "%v", err)
	}
	b := &binder{pending: map[any]bool{}}
	b.opts = jsonv2.JoinOptions(jsonv2.RejectUnknownMembers(true),
		jsonv2.WithUnmarshalers(jsonv2.UnmarshalFromFunc(b.unmarshal)))
	return bindError(v, jsonv2.Unmarshal(data, target, b.opts))
}

type binder struct {
	opts jsonv2.Options
	// pending holds the structs whose Validate runs once v2 has filled them.
	// The hook re-enters itself to have v2 fill the struct, and a pending
	// struct is how the re-entry knows to fall back to v2's default.
	pending map[any]bool
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// unboundedLimits re-decodes a value Parse already bounded, under whatever
// limits the caller chose then.
var unboundedLimits = Limits{MaxMessageBytes: math.MaxInt64, MaxContainerLen: math.MaxInt,
	MaxDepth: math.MaxInt, MaxNodes: math.MaxInt}

// unmarshal is consulted for every Go value v2 fills; p is always a non-nil
// pointer to it. errors.ErrUnsupported hands the value back to v2's default.
func (b *binder) unmarshal(dec *jsontext.Decoder, p any) error {
	rv := reflect.ValueOf(p).Elem()
	kind := dec.PeekKind()
	switch rv.Kind() {
	case reflect.Interface:
		// An untyped field takes the tree as ordinary Go values, numbers as
		// json.Number: v2 would launder them through a float64. It is the one
		// place strictness does not apply, because there is no schema to be
		// strict against — which is why REQ-SEC-12 also wants a Validator.
		if rv.NumMethod() != 0 || kind == 'n' {
			return errors.ErrUnsupported
		}
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		tree, err := decode(raw, unboundedLimits, true)
		if err != nil {
			return err
		}
		a, err := tree.Any()
		if err == nil {
			rv.Set(reflect.ValueOf(a))
		}
		return err

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if kind != '0' {
			return errors.ErrUnsupported
		}
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		n, err := integerFrom(tok.String())
		switch {
		case err != nil:
			return err
		case rv.CanInt() && rv.OverflowInt(n):
			return failf(RuleRange, "", "%d overflows %s", n, rv.Type())
		case rv.CanInt():
			rv.SetInt(n)
		case n < 0:
			return failf(RuleRange, "", "%d is negative for %s", n, rv.Type())
		case rv.OverflowUint(uint64(n)):
			return failf(RuleRange, "", "%d overflows %s", n, rv.Type())
		default:
			rv.SetUint(uint64(n))
		}
		return nil

	case reflect.Float32, reflect.Float64:
		// REQ-SEC-12.2's other direction: an INTEGER satisfies a float field.
		if kind != '0' {
			return errors.ErrUnsupported
		}
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		f, err := strconv.ParseFloat(tok.String(), 64)
		if err != nil {
			return failf(RuleType, "", "malformed number %q", tok.String())
		}
		if rv.OverflowFloat(f) {
			return failf(RuleRange, "", "%v overflows %s", f, rv.Type())
		}
		rv.SetFloat(f)
		return nil

	case reflect.Slice:
		// REQ-SEC-12.4: null zeroes, for RawMessage too; v2 would keep `null`.
		if rv.Type() != rawMessageType || kind != 'n' {
			return errors.ErrUnsupported
		}
		_, err := dec.ReadToken()
		rv.SetZero()
		return err

	case reflect.Struct:
		// REQ-SEC-12.3: the hook runs as soon as THIS struct is filled, so a
		// nested failure is reported at its own path. A null or absent struct
		// was not filled and is not validated.
		val, ok := p.(Validator)
		if !ok || kind != '{' || b.pending[p] {
			return errors.ErrUnsupported
		}
		b.pending[p] = true
		err := jsonv2.UnmarshalDecode(dec, p, b.opts)
		delete(b.pending, p)
		if err != nil {
			return err
		}
		if err := val.Validate(); err != nil {
			return &Error{Rule: RuleValidator, Msg: err.Error(), Err: err}
		}
		return nil
	}
	return errors.ErrUnsupported
}

// integerFrom implements REQ-SEC-12.2's integer direction.
//
// An INTEGRAL FLOAT satisfies an integer field — a peer whose runtime has one
// number type emits 1.0 for 1 — but only inside the IEEE-754 safe-integer
// range. Outside it a double no longer represents every integer, so `1e19`
// does not mean any particular integer and accepting it would invent one.
func integerFrom(lit string) (int64, error) {
	if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return 0, failf(RuleType, "", "malformed number %q", lit)
	}
	if math.Trunc(f) != f {
		return 0, failf(RuleType, "", "%s is not an integer", lit)
	}
	if f > SafeInteger || f < -SafeInteger {
		return 0, failf(RuleRange, "",
			"%s is outside the IEEE-754 safe-integer range, so it does not name an exact integer", lit)
	}
	return int64(f), nil
}

// bindError maps a v2 failure onto a Rule, and its JSON Pointer onto this
// package's path syntax, using the tree to tell array indices from names.
func bindError(root Value, err error) error {
	if err == nil {
		return nil
	}
	var se *jsonv2.SemanticError
	if !errors.As(err, &se) {
		return &Error{Rule: RuleType, Path: "$", Msg: err.Error(), Err: err}
	}
	path, v := "$", root
	for tok := range se.JSONPointer.Tokens() {
		if v.Kind == KindArray {
			path += "[" + tok + "]"
			if i, err := strconv.Atoi(tok); err == nil && i < len(v.Array) {
				v = v.Array[i]
			}
		} else {
			path, v = path+"."+tok, v.Object[tok]
		}
	}
	var we *Error
	switch {
	case errors.As(err, &we):
		we.Path = path
		return we
	case errors.Is(err, jsonv2.ErrUnknownName):
		// REQ-SEC-12.1.
		return failf(RuleUnknownField, path, "unknown property %q; %s declares %s",
			se.JSONPointer.LastToken(), se.GoType, declared(se.GoType))
	}
	return &Error{Rule: RuleType, Path: path, Msg: strings.TrimPrefix(se.Error(), "json: "), Err: err}
}

// declared lists a struct's JSON names, sorted, so an unknown-property
// rejection shows the peer's author the difference.
func declared(t reflect.Type) string {
	var names []string
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch {
			case name == "-":
			case f.Anonymous && name == "" && ft.Kind() == reflect.Struct:
				walk(ft)
			case !f.IsExported():
			case name == "":
				names = append(names, f.Name)
			default:
				names = append(names, name)
			}
		}
	}
	if t != nil && t.Kind() == reflect.Struct {
		walk(t)
	}
	if len(names) == 0 {
		return "no properties"
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), ", ")
}
