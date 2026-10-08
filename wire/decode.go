package wire

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

// Parse decodes a bounded tree. It is the REQ-SEC-12 entry point.
func Parse(data []byte, l Limits) (Value, error) { return decode(data, l, true) }

// Guard validates a message against REQ-SEC-11's bounds WITHOUT building a
// tree.
//
// It exists because the bounds and the strict binding have different scopes.
// Every untrusted surface needs the bounds — a provider response included —
// but a provider response must stay tolerant of fields a vendor added last
// week, so it is guarded and then decoded with the ordinary lenient path. One
// extra linear scan is the whole cost, and it buys duplicate-key rejection on
// a surface where last-wins would let a compromised gateway choose which of
// two stop_reasons AgentKit sees.
func Guard(data []byte, l Limits) error {
	_, err := decode(data, l, false)
	return err
}

// container is an object or array whose members are still arriving.
type container struct {
	Value        // the members so far; only Kind is set when not building
	path  string // this container's own path
	key   string // the member name whose value comes next
	named bool   // key has been read
	n     int    // members or elements completed
}

// child is the path of the value about to be read inside c. It is computed
// only when a path is needed — an error, or a nested container — because a
// scalar member is the common case and nobody reads its path.
func child(c *container) string {
	switch {
	case c == nil:
		return "$"
	case c.Kind == KindArray:
		return c.path + "[" + strconv.Itoa(c.n) + "]"
	}
	return join(c.path, c.key)
}

// join builds a JSON path segment. key "" names the parent itself.
func join(parent, key string) string {
	if key == "" {
		return parent
	}
	return parent + "." + key
}

// decode is the one walk behind Parse and Guard, so the two cannot disagree
// about what is legal. jsontext does the grammar, the unescaping and the
// duplicate-name rejection (REQ-SEC-11.3); this loop adds REQ-SEC-11's
// bounds, each checked BEFORE the token that would exceed it is read.
func decode(data []byte, l Limits, build bool) (Value, error) {
	l = l.withDefaults()
	// int64 throughout: REQ-SEC-11.1 is about a peer-declared length narrowed
	// to int on a 32-bit build, and the package keeps one width for sizes.
	if int64(len(data)) > l.MaxMessageBytes {
		return Value{}, failf(RuleMessageBytes, "$", "message is %d bytes, limit is %d",
			len(data), l.MaxMessageBytes)
	}
	// AllowInvalidUTF8 so an unpaired surrogate escape decodes to U+FFFD, as
	// encoding/json does: a peer with a UTF-16 runtime emits them for
	// legitimate reasons. It would also mangle RAW invalid bytes silently, so
	// those are rejected below, per string token.
	dec := jsontext.NewDecoder(bytes.NewBuffer(data), jsontext.AllowInvalidUTF8(true))
	var stack []*container
	nodes := 0
	for {
		var top *container
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		kind := dec.PeekKind()
		isName := top != nil && top.Kind == KindObject && !top.named
		if kind != 0 && kind != '}' && kind != ']' && !isName {
			// Counted on entry, before the value is appended to its parent:
			// the bound on how far a short message can amplify into a tree.
			if nodes++; nodes > l.MaxNodes {
				return Value{}, failf(RuleNodes, child(top), "message has more than %d values",
					l.MaxNodes)
			}
			if (kind == '{' || kind == '[') && len(stack) >= l.MaxDepth {
				return Value{}, failf(RuleDepth, child(top), "nesting exceeds %d", l.MaxDepth)
			}
		}
		start := dec.InputOffset()
		tok, err := dec.ReadToken()
		if err != nil {
			return Value{}, syntaxError(err, stack)
		}
		// RFC 8259 §8.1: JSON text is UTF-8, and an invalid byte is a
		// REJECTION. The fuzz fixed point (NFR-TEST-09.3) found that an
		// accepted 0xFF re-encodes differently on the second pass, so a
		// message accepted here would be one AgentKit could not stably write.
		if kind == '"' && !utf8.Valid(data[start:dec.InputOffset()]) {
			at := child(top)
			if isName {
				at = top.path
			}
			return Value{}, failf(RuleSyntax, at, "invalid UTF-8 in string")
		}

		var v Value
		switch kind {
		case '"':
			if isName {
				top.key, top.named = tok.String(), true
				continue
			}
			v.Kind = KindString
			if build {
				v.String = tok.String()
			}
		case '{', '[':
			// NOT sized to anything: JSON declares no length, and sizing to
			// MaxContainerLen "just in case" is the allocation REQ-SEC-11.2
			// rules out. Members are appended as they actually arrive.
			c := &container{path: child(top)}
			c.Kind = KindArray
			if kind == '{' {
				c.Kind = KindObject
				if build {
					c.Object = map[string]Value{}
				}
			}
			stack = append(stack, c)
			continue
		case '}', ']':
			stack = stack[:len(stack)-1]
			v, top = top.Value, nil
			if len(stack) > 0 {
				top = stack[len(stack)-1]
			}
		case 'n':
			v.Kind = KindNull
		case 't', 'f':
			v.Kind, v.Bool = KindBool, kind == 't'
		case '0':
			// jsontext reads `01` as two tokens and only rejects the second,
			// which would let a container bound fire on a message that is not
			// JSON. A digit straight after a number is a leading zero.
			if end := dec.InputOffset(); end < int64(len(data)) && data[end] >= '0' && data[end] <= '9' {
				return Value{}, failf(RuleSyntax, child(top), "number has a leading zero")
			}
			v.Kind = KindNumber
			if build {
				v.Number = json.Number(tok.String()) // the verbatim literal
			}
		}

		if top == nil {
			if _, err := dec.ReadToken(); err != io.EOF {
				return Value{}, failf(RuleSyntax, "$", "trailing bytes after the JSON value")
			}
			return v, nil
		}
		if top.n++; top.n > l.MaxContainerLen {
			what := "array has more than %d elements"
			if top.Kind == KindObject {
				what = "object has more than %d members"
			}
			return Value{}, failf(RuleContainerLen, top.path, what, l.MaxContainerLen)
		}
		if build && top.Kind == KindArray {
			top.Array = append(top.Array, v)
		} else if build {
			top.Object[top.key] = v
			top.Keys = append(top.Keys, top.key)
		}
		top.named = false
	}
}

// syntaxError maps a jsontext failure onto a Rule, and its JSON Pointer onto
// this package's path syntax. The pointer alone cannot tell an array index
// from a numeric member name; the decoder's own stack can.
func syntaxError(err error, stack []*container) error {
	var se *jsontext.SyntacticError
	if !errors.As(err, &se) {
		return &Error{Rule: RuleSyntax, Path: "$", Msg: "unexpected end of input", Err: err}
	}
	path, i := "$", 0
	for tok := range se.JSONPointer.Tokens() {
		if i < len(stack) && stack[i].Kind == KindArray {
			path += "[" + tok + "]"
		} else {
			path = join(path, tok)
		}
		i++
	}
	rule := RuleSyntax
	if errors.Is(se.Err, jsontext.ErrDuplicateName) {
		rule = RuleDuplicateKey
	}
	return &Error{Rule: rule, Path: path, Msg: se.Err.Error(), Err: err}
}
