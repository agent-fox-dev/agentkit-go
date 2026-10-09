package codemode

import (
	"fmt"

	"go.starlark.net/starlark"

	"github.com/agentfox/agentkit-go/core"
)

// callDescriptor is call(tool, **kwargs): a call to make later, in parallel.
type callDescriptor struct {
	tool   *starlark.Builtin
	kwargs *starlark.Dict
}

var _ starlark.HasAttrs = (*callDescriptor)(nil)

func (c *callDescriptor) String() string {
	return fmt.Sprintf("call(%s, **%s)", c.tool.Name(), c.kwargs)
}
func (c *callDescriptor) Type() string         { return "call_descriptor" }
func (c *callDescriptor) Freeze()              { c.kwargs.Freeze() }
func (c *callDescriptor) Truth() starlark.Bool { return starlark.True }
func (c *callDescriptor) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: call_descriptor")
}
func (c *callDescriptor) AttrNames() []string { return []string{"kwargs", "tool"} }
func (c *callDescriptor) Attr(name string) (starlark.Value, error) {
	switch name {
	case "tool":
		return c.tool, nil
	case "kwargs":
		return c.kwargs, nil
	}
	return nil, nil
}

// call is the call(tool, **kwargs) builtin.
func (r *runner) call(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 1 {
		return nil, argErrorf("call takes one positional argument, the tool, then keyword arguments: call(read_file, path=p)")
	}
	fn, err := r.boundTool(args[0])
	if err != nil {
		return nil, argErrorf("call: %v", err)
	}
	d := starlark.NewDict(len(kwargs))
	for _, kv := range kwargs {
		_ = d.SetKey(kv[0], kv[1])
	}
	return &callDescriptor{tool: fn, kwargs: d}, nil
}

// boundTool resolves a tool function, or a tool's name, to the bound tool.
func (r *runner) boundTool(v starlark.Value) (*starlark.Builtin, error) {
	switch x := v.(type) {
	case *starlark.Builtin:
		if fn, ok := r.funcs[x.Name()]; ok && fn == x {
			return fn, nil
		}
		return nil, fmt.Errorf("%s is not a bound tool", x.Name())
	case starlark.String:
		if fn, ok := r.funcs[string(x)]; ok {
			return fn, nil
		}
		return nil, fmt.Errorf("%q is not a bound tool", string(x))
	}
	return nil, fmt.Errorf("want a bound tool, got %s", v.Type())
}

// parallel is the parallel(calls) builtin. Each call is call(tool, ...), a
// (tool, kwargs) pair with the tool as a function or a name, or
// {"tool": tool, "args": kwargs}. Every call is checked before any runs.
// They are dispatched in batches of at most MaxConcurrentCalls, each batch
// one core.CallNested that runs its calls concurrently, and the results come
// back as a list in the order of calls (08-REQ-6).
func (r *runner) parallel(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var seq starlark.Value
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &seq); err != nil {
		return nil, err
	}
	list, ok := seq.(starlark.Indexable)
	if _, isStr := seq.(starlark.String); !ok || isStr {
		return nil, argErrorf("parallel takes a list of calls, got %s", seq.Type())
	}
	blocks := make([]core.ToolUseBlock, list.Len())
	for i := range blocks {
		name, kw, err := r.callSpec(list.Index(i))
		if err != nil {
			return nil, argErrorf("parallel: call %d: %v", i, err)
		}
		if blocks[i], err = r.block(name, kw); err != nil {
			return nil, argErrorf("parallel: call %d: %v", i, err)
		}
	}
	// The whole list is checked against the budget before any batch runs.
	if err := r.takeCalls(len(blocks)); err != nil {
		return nil, err
	}
	out := make([]starlark.Value, 0, len(blocks))
	for start := 0; start < len(blocks); start += r.opts.MaxConcurrentCalls {
		end := min(start+r.opts.MaxConcurrentCalls, len(blocks))
		res, err := r.dispatch(blocks[start:end]...)
		if err != nil {
			return nil, err
		}
		for _, one := range res {
			v, err := r.value(one)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	return starlark.NewList(out), nil
}

// callSpec reads one entry of parallel's list as a tool name and its keyword
// arguments.
func (r *runner) callSpec(v starlark.Value) (string, []starlark.Tuple, error) {
	var tool, args starlark.Value
	switch x := v.(type) {
	case *callDescriptor:
		tool, args = x.tool, x.kwargs
	case *starlark.Dict:
		t, found, _ := x.Get(starlark.String("tool"))
		a, foundArgs, _ := x.Get(starlark.String("args"))
		if !found || !foundArgs {
			return "", nil, fmt.Errorf(`a dict call needs "tool" and "args"`)
		}
		tool, args = t, a
	case starlark.Indexable:
		if _, isStr := v.(starlark.String); isStr || x.Len() != 2 {
			return "", nil, fmt.Errorf("want call(tool, ...), (tool, kwargs) or {\"tool\": tool, \"args\": kwargs}, got %s", v.String())
		}
		tool, args = x.Index(0), x.Index(1)
	default:
		return "", nil, fmt.Errorf("want call(tool, ...), (tool, kwargs) or {\"tool\": tool, \"args\": kwargs}, got %s", v.Type())
	}
	fn, err := r.boundTool(tool)
	if err != nil {
		return "", nil, err
	}
	d, ok := args.(*starlark.Dict)
	if !ok {
		return "", nil, fmt.Errorf("the arguments must be a dict, got %s", args.Type())
	}
	kw := make([]starlark.Tuple, 0, d.Len())
	for _, item := range d.Items() {
		if _, ok := item[0].(starlark.String); !ok {
			return "", nil, fmt.Errorf("argument names must be strings, got %s", item[0].Type())
		}
		kw = append(kw, starlark.Tuple{item[0], item[1]})
	}
	return fn.Name(), kw, nil
}
