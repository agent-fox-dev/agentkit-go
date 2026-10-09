package codemode

import (
	"fmt"

	"go.starlark.net/starlark"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// ToolError is what a bound tool returns to a script when the call failed:
// the tool's error, a block by an interceptor, an unknown or invalid call.
// Starlark has no exceptions, so a failure is a value the script checks —
// is_error(r), r.ok, r.is_error — and reads: r.error is the code, r.detail
// the message, r.data any structured payload the tool returned. It can also
// be indexed like a dict: r["error"].
type ToolError struct {
	code, detail string
	data         starlark.Value
	goData       map[string]any
}

// NewToolError is the ToolError for a failed result.
func NewToolError(res core.ToolResult) *ToolError {
	e := &ToolError{code: res.Error, detail: res.Detail, data: starlark.None, goData: res.Data}
	if res.Data != nil {
		if v, err := toStarlark(res.Data); err == nil {
			e.data = v
		}
	}
	return e
}

var (
	_ starlark.HasAttrs = (*ToolError)(nil)
	_ starlark.Mapping  = (*ToolError)(nil)
)

// toolErrorAttrs is sorted, as AttrNames must be.
var toolErrorAttrs = []string{"data", "detail", "error", "is_error", "ok"}

func (e *ToolError) String() string {
	return fmt.Sprintf("tool_error(error=%q, detail=%q)", e.code, e.detail)
}
func (e *ToolError) Type() string { return "tool_error" }
func (e *ToolError) Freeze() {
	if e.data != nil {
		e.data.Freeze()
	}
}

// Truth is False, so `if not r:` reads as "the call failed".
func (e *ToolError) Truth() starlark.Bool { return starlark.False }
func (e *ToolError) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: tool_error")
}

func (e *ToolError) Attr(name string) (starlark.Value, error) {
	switch name {
	case "ok":
		return starlark.False, nil
	case "is_error":
		return starlark.True, nil
	case "error":
		return starlark.String(e.code), nil
	case "detail":
		return starlark.String(e.detail), nil
	case "data":
		return e.data, nil
	}
	return nil, nil
}

func (e *ToolError) AttrNames() []string { return append([]string(nil), toolErrorAttrs...) }

// Get is dict-style access to the same fields.
func (e *ToolError) Get(k starlark.Value) (starlark.Value, bool, error) {
	key, ok := k.(starlark.String)
	if !ok {
		return nil, false, nil
	}
	v, _ := e.Attr(string(key))
	return v, v != nil, nil
}

// goValue is the ToolError as JSON sees it, for a script that returns one.
func (e *ToolError) goValue() any {
	return map[string]any{"ok": false, "is_error": true, "error": e.code, "detail": e.detail, "data": e.goData}
}

// isError is the is_error(value) builtin.
func isError(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var v starlark.Value
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &v); err != nil {
		return nil, err
	}
	_, ok := v.(*ToolError)
	return starlark.Bool(ok), nil
}
