package core

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/agent-fox-dev/agentkit-go/schema"
)

// PreparedArguments is the output of the argument pipeline (REQ-TOOL-11).
//
//	Raw   the bytes handed to the handler (REQ-GO-03's pinned signature)
//	Args  the decoded map, for interceptors and policies to read
//
// Raw is the model's own bytes whenever the pipeline changed nothing, so the
// handler — and any replay — sees exactly what the model wrote. When a step
// did change the value, Raw is rebuilt from the original bytes: every key the
// change left alone keeps its position and its bytes, and only changed or new
// keys are re-encoded. A map has no order, and re-encoding it whole would sort
// the keys (REQ-TOOL-12).
type PreparedArguments struct {
	Raw  json.RawMessage
	Args map[string]any

	// Coercions records what step 3 changed, for diagnostics.
	Coercions []schema.Coercion
}

// WithArgs replaces the arguments, as an interceptor may do (REQ-SEC-03.5),
// rebuilding Raw so the keys the interceptor kept keep their positions.
//
// It panics if a value is not representable in JSON (NaN, a channel);
// TryWithArgs reports that as an error instead.
func (p PreparedArguments) WithArgs(args map[string]any) PreparedArguments {
	out, err := p.TryWithArgs(args)
	if err != nil {
		panic(err)
	}
	return out
}

// TryWithArgs is WithArgs returning an error for a value that is not
// representable in JSON. An interceptor's replacement arguments are the
// embedder's code: one that produces NaN fails that call, not the run.
func (p PreparedArguments) TryWithArgs(args map[string]any) (PreparedArguments, error) {
	raw, err := encodeLike(p.Raw, args)
	if err != nil {
		return PreparedArguments{}, err
	}
	return PreparedArguments{Raw: raw, Args: args, Coercions: p.Coercions}, nil
}

// PrepareArguments runs REQ-TOOL-11's fixed pipeline before the handler. Every
// stage runs inside the caller's panic-recover boundary.
//
//  1. Tool.PrepareArguments — per-tool shape repair, runs FIRST, on a copy.
//  2. Delete explicit nulls for OPTIONAL properties.
//  3. Coerce primitives against the declared schema.
//  4. Validate; a failure echoes the model's own arguments back in the order
//     it wrote them, so the error is self-correcting.
//
// Step 2 is the non-obvious one: constrained sampling forces the model to emit
// every declared property, so optional fields arrive as explicit nulls.
// Treating them as present is a validation failure on well-formed output.
func PrepareArguments(t Tool, c ToolUseBlock) (PreparedArguments, error) {
	args, err := decodeObject(c.Input)
	if err != nil {
		return PreparedArguments{}, err
	}
	modified := false

	// ---- 1. Per-tool repair shim, on a copy: the shim may edit its map in
	// place, and the comparison below needs the original.
	if t.PrepareArguments != nil {
		if after := t.PrepareArguments(cloneArgs(args)); after != nil && !reflect.DeepEqual(after, args) {
			args, modified = after, true
		}
	}

	var coercions []schema.Coercion
	if t.InputSchema != nil {
		// ---- 2. Optional nulls. A null for a REQUIRED property is left in
		// place so validation reports it as the wrong type, rather than being
		// silently deleted into a "missing property" error that reads as a
		// different bug.
		if next := schema.DeleteOptionalNulls(t.InputSchema, args); !reflect.DeepEqual(next, args) {
			args, modified = next, true
		}

		// ---- 3. Coerce.
		var next map[string]any
		next, coercions = schema.Coerce(t.InputSchema, args)
		if len(coercions) > 0 {
			args, modified = next, true
		}

		// ---- 4. Validate.
		if err := schema.Validate(t.InputSchema, args); err != nil {
			// The error echoes the arguments, and it echoes the model's own
			// bytes, so the keys are in the order the model wrote them
			// (REQ-TOOL-12.3).
			var ve *schema.ValidationError
			if errors.As(err, &ve) {
				ve.Args = c.Input
				return PreparedArguments{}, err
			}
			return PreparedArguments{}, fmt.Errorf("%w\n\narguments as provided:\n%s", err, echoArguments(c.Input))
		}
	}

	raw := c.Input
	if modified {
		if raw, err = encodeLike(c.Input, args); err != nil {
			return PreparedArguments{}, err
		}
	}
	return PreparedArguments{Raw: raw, Args: args, Coercions: coercions}, nil
}

// echoArguments renders the model's own bytes back to it, indented.
//
// json.Indent is byte-preserving, so key order and numeric literals come free.
// Re-marshalling a decoded map here would sort the keys and show the model
// something it did not write, defeating the point of a self-correcting error
// (REQ-TOOL-12.3).
func echoArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// encodeLike encodes args as a JSON object laid out like orig: a key orig
// has keeps its position, and its original bytes when its value is
// unchanged; a key orig lacks follows, in lexical order — not Go's
// randomized map order, which would make the bytes irreproducible across
// runs and break every golden test that touches a repaired call.
func encodeLike(orig json.RawMessage, args map[string]any) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	n := 0
	write := func(member []byte) {
		if n > 0 {
			buf.WriteByte(',')
		}
		buf.Write(member)
		n++
	}
	encode := func(k string) error {
		b, err := json.Marshal(args[k])
		if err != nil {
			return fmt.Errorf("argument %q: %w", k, err)
		}
		kb, _ := json.Marshal(k)
		write(append(append(kb, ':'), b...))
		return nil
	}

	seen := make(map[string]bool, len(args))
	for _, m := range topLevelMembers(orig) {
		v, ok := args[m.key]
		if !ok {
			continue
		}
		seen[m.key] = true
		if reflect.DeepEqual(v, m.value) {
			write(m.raw)
			continue
		}
		if err := encode(m.key); err != nil {
			return nil, err
		}
	}
	added := make([]string, 0, len(args))
	for k := range args {
		if !seen[k] {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	for _, k := range added {
		if err := encode(k); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// rawMember is one top-level member of a JSON object: its name, its decoded
// value, and its bytes from the name to the end of the value.
type rawMember struct {
	key   string
	value any
	raw   []byte
}

// topLevelMembers splits a JSON object into its members, in order, each with
// the exact bytes it was written with. Invalid input yields none.
func topLevelMembers(raw json.RawMessage) []rawMember {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil
	}
	var out []rawMember
	for dec.PeekKind() != '}' {
		start := dec.InputOffset()
		name, err := dec.ReadToken()
		if err != nil {
			return nil
		}
		key := name.String() // a token is void after the decoder's next call
		val, err := dec.ReadValue()
		if err != nil {
			return nil
		}
		member := bytes.TrimLeft(raw[start:dec.InputOffset()], " \t\r\n,")
		var v any
		d := json.NewDecoder(bytes.NewReader(val))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return nil
		}
		out = append(out, rawMember{key: key, value: v, raw: member})
	}
	return out
}

// cloneArgs deep-copies a decoded argument map.
func cloneArgs(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneArgs(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = cloneValue(t[i])
		}
		return out
	}
	return v
}
