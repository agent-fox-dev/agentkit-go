package toml

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/agentfox/agentkit-go/internal/diag"
)

// Diagnostic and Severity are the shared report type (internal/diag), aliased
// so a caller of this package needs one import rather than two.
type (
	Diagnostic = diag.Diagnostic
	Severity   = diag.Severity
)

const (
	SeverityWarning = diag.SeverityWarning
	SeverityError   = diag.SeverityError
)

const bomPrefix = diag.BOMPrefix

// ParseTOML reads TOML with github.com/pelletier/go-toml/v2/unstable, which
// owns the grammar (strings and escapes, numbers, comments, headers, dotted
// keys, nesting bound). This file only folds the parser's expression stream
// into an ordered Table, because the decoders built on go-toml cannot report
// the line of a value, keep written key order, or let a duplicate key warn
// and win instead of failing the file. The version is pinned in go.mod:
// `unstable` is outside go-toml's semver promise.
//
// VALUES a manifest reader receives: strings (basic and literal), booleans,
// decimal integers, floats (including inf and nan) and arrays of strings,
// plus tables and arrays of tables.
//
// Every other well-formed value is a DIAGNOSTIC THAT SKIPS THE KEY rather than
// a failure of the file. REQ-SKILL-10: a manifest is authored content whose
// consumer is a language model, and a value form we do not read must not
// delete the whole skill.
//   - dates, times and datetimes
//   - multi-line strings (""" and ''')
//   - inline tables { }
//   - non-decimal integers (0x, 0o, 0b)
//   - arrays that are not arrays of strings (numbers, nested arrays, tables)
//
// A grammar error is a HARD ERROR (*SyntaxError with a line), because after
// one the file's structure is unknown and every later key would be filed
// under the wrong table — a silently misplaced key is worse than a rejected
// manifest.

// ValueKind enumerates the value types listed above.
type ValueKind uint8

const (
	KindString ValueKind = iota
	KindBool
	KindInt
	KindStringArray
	KindFloat
)

// Value is one parsed TOML scalar or string array. It is a tagged struct
// rather than an `any`, so a manifest reader that asks for the wrong type gets
// a diagnostic instead of a panic.
type Value struct {
	Kind  ValueKind
	Str   string
	Bool  bool
	Int   int64
	Float float64
	Array []string
	// Line is the 1-based line the value was written on, so a diagnostic can
	// point the manifest author at it.
	Line int
}

// Table is a TOML table. It preserves key insertion order, which is what makes
// the unknown-key diagnostics of REQ-SKILL-10 deterministic: a map iteration
// would reorder the warnings between runs and turn a golden test into a flake.
type Table struct {
	name      string
	line      int
	keys      []string
	vals      map[string]Value
	arrays    map[string][]*Table
	arrayKeys []string
	subKeys   []string
	subs      map[string]*Table
}

func newTable(name string, line int) *Table {
	return &Table{name: name, line: line, vals: map[string]Value{}, subs: map[string]*Table{}}
}

// Name is the dotted path of the table, empty for the root table.
func (t *Table) Name() string { return t.name }

// Line is the 1-based line of the table's header, 0 for the root table.
func (t *Table) Line() int { return t.line }

// Keys returns the table's own keys in the order they were written.
func (t *Table) Keys() []string { return append([]string(nil), t.keys...) }

// SubTables returns the names of the table's sub-tables in written order.
func (t *Table) SubTables() []string { return append([]string(nil), t.subKeys...) }

// Get returns the value written for key.
func (t *Table) Get(key string) (Value, bool) {
	if t == nil {
		return Value{}, false
	}
	v, ok := t.vals[key]
	return v, ok
}

// Sub returns the named sub-table.
func (t *Table) Sub(key string) (*Table, bool) {
	if t == nil {
		return nil, false
	}
	s, ok := t.subs[key]
	return s, ok
}

// Array returns the elements of an array of tables ([[key]]), in document
// order.
func (t *Table) Array(key string) ([]*Table, bool) {
	if t == nil {
		return nil, false
	}
	a, ok := t.arrays[key]
	return a, ok
}

// ArrayKeys returns the names of the arrays of tables declared here.
func (t *Table) ArrayKeys() []string {
	if t == nil {
		return nil
	}
	return append([]string(nil), t.arrayKeys...)
}

// appendArray adds one element to an array of tables.
func (t *Table) appendArray(key, qualified string, line int) *Table {
	if t.arrays == nil {
		t.arrays = map[string][]*Table{}
	}
	if _, exists := t.arrays[key]; !exists {
		t.arrayKeys = append(t.arrayKeys, key)
	}
	el := newTable(qualified, line)
	t.arrays[key] = append(t.arrays[key], el)
	return el
}

// Qualify renders a key with its table prefix, for a diagnostic that has to
// name where in the document the problem is.
func (t *Table) Qualify(key string) string {
	if t.name == "" {
		return key
	}
	return t.name + "." + key
}

// set records a value, reporting false when the key was already present.
func (t *Table) set(key string, v Value) bool {
	if _, dup := t.vals[key]; dup {
		t.vals[key] = v
		return false
	}
	t.vals[key] = v
	t.keys = append(t.keys, key)
	return true
}

func (t *Table) ensure(path []string, line int) *Table {
	cur := t
	for _, part := range path {
		// A path segment naming an array of tables descends into its MOST
		// RECENT element. That is what [[a]] followed by [a.b] means in TOML:
		// b belongs to the a that was just declared, not to a fourth table
		// hanging off the root. Walking past the array instead files every key
		// under [a.b] in a table nobody reads — silently, since both spellings
		// parse.
		if arr, ok := cur.arrays[part]; ok && len(arr) > 0 {
			cur = arr[len(arr)-1]
			continue
		}
		nxt, ok := cur.subs[part]
		if !ok {
			nxt = newTable(cur.Qualify(part), line)
			cur.subs[part] = nxt
			cur.subKeys = append(cur.subKeys, part)
		}
		cur = nxt
	}
	return cur
}

// SyntaxError is a structural failure: the parser cannot know where the
// remaining keys belong, so it stops.
type SyntaxError struct {
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ParseTOML returns the root table, the diagnostics for keys it deliberately
// skipped, and an error only for a grammar failure.
func ParseTOML(src []byte) (*Table, []Diagnostic, error) {
	// A BOM at the head of a manifest is common on Windows editors and is not
	// a key (REQ-CTX-02 requires the same strip for context files).
	src = []byte(strings.TrimPrefix(string(src), bomPrefix))

	var p unstable.Parser
	p.Reset(src)
	lineOf := func(r unstable.Range) int { return p.Shape(r).Start.Line }
	root := newTable("", 0)
	cur := root
	var diags []Diagnostic
	warnf := func(line int, f string, a ...any) {
		diags = append(diags, Diagnostic{Severity: SeverityWarning, Line: line, Message: fmt.Sprintf(f, a...)})
	}
	for p.NextExpression() {
		e := p.Expression()
		var path []string
		line := 0
		for it := e.Key(); it.Next(); {
			if line == 0 {
				line = lineOf(it.Node().Raw)
			}
			path = append(path, string(it.Node().Data))
		}
		last := path[len(path)-1]
		switch e.Kind {
		case unstable.Table:
			cur = root.ensure(path, line)
		case unstable.ArrayTable:
			parent := root.ensure(path[:len(path)-1], line)
			cur = parent.appendArray(last, parent.Qualify(last), line)
		case unstable.KeyValue:
			tbl := cur.ensure(path[:len(path)-1], line)
			v, why := convert(&p, e.Value())
			v.Line = line
			switch {
			case why != "":
				warnf(line, "key %q skipped: %s", tbl.Qualify(last), why)
			case !tbl.set(last, v):
				warnf(line, "duplicate key %q; the last value wins", tbl.Qualify(last))
			}
		}
	}
	if err := p.Error(); err != nil {
		se := &SyntaxError{Msg: err.Error()}
		if perr := (*unstable.ParserError)(nil); errors.As(err, &perr) && perr.Highlight != nil {
			se.Line = lineOf(p.Range(perr.Highlight))
		}
		return nil, diags, se
	}
	return root, diags, nil
}

// convert maps one value node to a Value, or names why it is not read.
func convert(p *unstable.Parser, n *unstable.Node) (Value, string) {
	raw := string(p.Raw(n.Raw))
	switch n.Kind {
	case unstable.String:
		if strings.HasPrefix(raw, `"""`) || strings.HasPrefix(raw, "'''") {
			return Value{}, "multi-line strings are not supported"
		}
		return Value{Kind: KindString, Str: string(n.Data)}, ""
	case unstable.Bool:
		return Value{Kind: KindBool, Bool: string(n.Data) == "true"}, ""
	case unstable.Integer:
		if body := strings.TrimLeft(raw, "+-"); len(body) > 1 && body[0] == '0' {
			return Value{}, "only decimal integers are supported"
		}
		i, err := strconv.ParseInt(strings.ReplaceAll(raw, "_", ""), 10, 64)
		if err != nil {
			return Value{}, fmt.Sprintf("unrecognized value %q", raw)
		}
		return Value{Kind: KindInt, Int: i}, ""
	case unstable.Float:
		f, err := strconv.ParseFloat(strings.ReplaceAll(string(n.Data), "_", ""), 64)
		if err != nil {
			return Value{}, fmt.Sprintf("unrecognized value %q", n.Data)
		}
		return Value{Kind: KindFloat, Float: f}, ""
	case unstable.Array:
		out := []string{}
		for it := n.Children(); it.Next(); {
			ev, why := convert(p, it.Node())
			if why == "" && ev.Kind != KindString {
				why = "only arrays of strings are supported"
			}
			if why != "" {
				return Value{}, why
			}
			out = append(out, ev.Str)
		}
		return Value{Kind: KindStringArray, Array: out}, ""
	case unstable.InlineTable:
		return Value{}, "inline tables are not supported"
	default: // LocalDate, LocalTime, LocalDateTime, DateTime
		return Value{}, "dates and times are not supported"
	}
}
