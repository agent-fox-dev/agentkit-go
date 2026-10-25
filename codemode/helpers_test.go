package codemode

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// leaf is a bindable tool that does nothing.
func leaf(name string) core.Tool {
	return core.Tool{Name: name, Description: "the " + name + " tool",
		InputSchema: schema.Object(schema.Opt("v", schema.String())),
		Execute:     func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
}

type answer func(core.ToolUseBlock) (core.ToolResult, error)

func okTool(core.ToolUseBlock) (core.ToolResult, error) {
	return core.OKResult(map[string]any{"ok": true}), nil
}

func failing(code, detail string) answer {
	return func(core.ToolUseBlock) (core.ToolResult, error) { return core.ErrResult(code, detail), nil }
}

// echoArgs answers with the call's arguments.
func echoArgs(b core.ToolUseBlock) (core.ToolResult, error) {
	var m map[string]any
	_ = json.Unmarshal(b.Input, &m)
	return core.OKResult(m), nil
}

// fakeCaller stands in for the agent's nested dispatcher: it answers each
// call with the function registered for its tool, and records the calls.
type fakeCaller struct {
	mu    sync.Mutex
	fn    map[string]answer
	calls []core.ToolUseBlock
	// concurrent runs one Call's blocks on goroutines, as the agent's
	// dispatcher does with ParallelTools on.
	concurrent bool
	// maxBatch is the most blocks one Call received.
	maxBatch int
}

func caller(fn map[string]answer) *fakeCaller { return &fakeCaller{fn: fn} }

func (f *fakeCaller) Call(_ context.Context, blocks ...core.ToolUseBlock) ([]core.ToolResult, error) {
	f.mu.Lock()
	f.maxBatch = max(f.maxBatch, len(blocks))
	f.mu.Unlock()
	out := make([]core.ToolResult, len(blocks))
	errs := make([]error, len(blocks))
	one := func(i int, b core.ToolUseBlock) {
		f.mu.Lock()
		f.calls = append(f.calls, b)
		fn := f.fn[b.Name]
		f.mu.Unlock()
		if fn == nil {
			out[i] = core.ErrResult("unknown_tool", b.Name)
			return
		}
		out[i], errs[i] = fn(b)
	}
	if f.concurrent {
		var wg sync.WaitGroup
		for i, b := range blocks {
			wg.Go(func() { one(i, b) })
		}
		wg.Wait()
	} else {
		for i, b := range blocks {
			if one(i, b); errs[i] != nil {
				break
			}
		}
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// runScript executes script in a code-mode tool bound to tools, with fc (when
// not nil) as the nested dispatcher.
func runScript(t *testing.T, ctx context.Context, opts Options, fc *fakeCaller, script string, tools ...core.Tool) core.ToolResult {
	t.Helper()
	cm, _, err := New(tools, opts)
	if err != nil {
		t.Fatal(err)
	}
	if fc != nil {
		ctx = core.WithNestedCaller(ctx, fc)
	}
	in, _ := json.Marshal(map[string]string{"script": script})
	return cm.Execute(ctx, in)
}

// rv renders a finished script's return value as JSON.
func rv(t *testing.T, res core.ToolResult) string {
	t.Helper()
	if !res.OK {
		t.Fatalf("script failed: %s: %s", res.Error, res.Detail)
	}
	b, err := json.Marshal(res.Data["return_value"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ledger is a result's calls_completed as JSON.
func ledger(t *testing.T, res core.ToolResult) string {
	t.Helper()
	b, err := json.Marshal(res.Data["calls_completed"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
