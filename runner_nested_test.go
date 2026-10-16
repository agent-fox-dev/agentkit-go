package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

var nestedIDPattern = regexp.MustCompile(`^nested_[0-9a-f]{16}$`)

// afterLog is an After interceptor that records the context of every call it
// sees, and votes through vote when that is set.
type afterLog struct {
	mu    sync.Mutex
	calls []core.AfterToolCallContext
	vote  func(core.AfterToolCallContext) core.AfterToolCallDecision
}

func (a *afterLog) after(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
	a.mu.Lock()
	a.calls = append(a.calls, in)
	a.mu.Unlock()
	if a.vote != nil {
		return a.vote(in)
	}
	return core.AfterToolCallDecision{}
}

func (a *afterLog) seen() []core.AfterToolCallContext {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]core.AfterToolCallContext(nil), a.calls...)
}

// nestedCalls makes the blocks a wrapper passes to core.CallNested.
func nestedCalls(names ...string) []core.ToolUseBlock {
	out := make([]core.ToolUseBlock, len(names))
	for i, n := range names {
		out[i] = core.ToolUseBlock{Name: n, Input: json.RawMessage(`{}`)}
	}
	return out
}

// TS-21-29: a wrapper called through Call has a nested caller. Each nested call
// gets a nested_ id; Guard and After see the wrapper as its parent; its events
// carry the parent id; no nested call emits a ToolResultEvent; and a nested
// call Guard blocks comes back to the wrapper as a blocked_by_policy result.
func TestNestedCallsThroughCall_TS21_29(t *testing.T) {
	var nested []core.ToolResult
	wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		res, err := core.CallNested(ctx, nestedCalls("child", "denied")...)
		if err != nil {
			return core.ErrResult("nested_failed", err.Error())
		}
		nested = res
		return core.OKResult(nil)
	}, childTool("child"), childTool("denied"))

	guards := guardLog{decide: func(in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{Block: in.ToolName == "denied", Reason: "not this one"}
	}}
	var afters afterLog
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{wrapper}, Guard: guards.guard, After: afters.after, Events: events.add})

	if res := r.Call(context.Background(), "wrapper", nil); !res.OK {
		t.Fatalf("Call = %+v", res)
	}

	var wrapperID string
	for _, g := range guards.seen() {
		if g.ToolName == "wrapper" {
			wrapperID = g.ToolUseID
		}
	}
	if wrapperID == "" {
		t.Fatal("Guard never saw the wrapper call")
	}
	nestedIDs := map[string]bool{}
	for _, g := range guards.seen() {
		if g.ToolName == "wrapper" {
			if g.ParentToolUseID != "" || g.ParentToolName != "" {
				t.Errorf("the wrapper has parent %q/%q, want none", g.ParentToolUseID, g.ParentToolName)
			}
			continue
		}
		nestedIDs[g.ToolUseID] = true
		if !nestedIDPattern.MatchString(g.ToolUseID) {
			t.Errorf("nested call %s has id %q, want nested_ and 16 lowercase hex characters", g.ToolName, g.ToolUseID)
		}
		if g.ParentToolUseID != wrapperID || g.ParentToolName != "wrapper" {
			t.Errorf("Guard saw nested %s with parent %q/%q, want %q/wrapper", g.ToolName, g.ParentToolUseID, g.ParentToolName, wrapperID)
		}
	}
	if len(nestedIDs) != 2 {
		t.Errorf("Guard saw %d nested calls, want 2", len(nestedIDs))
	}
	var childAfters int
	for _, a := range afters.seen() {
		if a.ToolName == "child" {
			childAfters++
			if a.ParentToolUseID != wrapperID || a.ParentToolName != "wrapper" {
				t.Errorf("After saw nested child with parent %q/%q, want %q/wrapper", a.ParentToolUseID, a.ParentToolName, wrapperID)
			}
		}
	}
	if childAfters != 1 {
		t.Errorf("After saw the nested child %d times, want 1 (the blocked call never ran)", childAfters)
	}

	var starts, ends, results int
	for _, e := range events.all() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if nestedIDs[ev.ToolUseID] {
				starts++
				if ev.ParentToolUseID != wrapperID {
					t.Errorf("nested start event has parent %q, want %q", ev.ParentToolUseID, wrapperID)
				}
			}
		case core.ToolExecutionEndEvent:
			if nestedIDs[ev.ToolUseID] {
				ends++
				if ev.ParentToolUseID != wrapperID {
					t.Errorf("nested end event has parent %q, want %q", ev.ParentToolUseID, wrapperID)
				}
			}
		case core.ToolResultEvent:
			results++
			if ev.Message.ToolUseID != wrapperID {
				t.Errorf("a ToolResultEvent is for %q; only the wrapper's belongs here", ev.Message.ToolUseID)
			}
		}
	}
	if starts != 2 || ends != 2 || results != 1 {
		t.Errorf("nested start/end events = %d/%d and ToolResultEvents = %d; want 2/2 and 1", starts, ends, results)
	}
	if len(nested) != 2 || !nested[0].OK || nested[1].OK || nested[1].Error != core.BlockErrorCode {
		t.Errorf("the wrapper's nested results = %+v; want an OK result then blocked_by_policy", nested)
	}
}

// TS-21-30: a nested call to a tool outside the wrapper's reachable set is
// unknown_tool, and the tool does not run.
func TestNestedUnreachableToolIsUnknown_TS21_30(t *testing.T) {
	var otherRan atomic.Int32
	var nested []core.ToolResult
	wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		nested, _ = core.CallNested(ctx, nestedCalls("other")...)
		return core.OKResult(nil)
	}, childTool("echo"))
	r := mustRunner(t, Toolset{Tools: []core.Tool{wrapper, echoTool("other", &otherRan)}})

	if res := r.Call(context.Background(), "wrapper", nil); !res.OK {
		t.Fatalf("Call = %+v", res)
	}
	const want = `wrapper cannot call "other": no tool named "other" is available in this run; available tools: echo`
	if len(nested) != 1 || nested[0].OK || nested[0].Error != "unknown_tool" || nested[0].Detail != want {
		t.Fatalf("nested result = %+v; want unknown_tool with %q", nested, want)
	}
	if otherRan.Load() != 0 {
		t.Fatal("the tool outside the wrapper's reach ran")
	}
}

// TS-21-31: a nested block that votes Terminate, or an After that votes
// Terminate for a nested call, ends the wrapper: that call and every later
// CallNested returns ErrTerminated, and the wrapper's result carries the vote.
func TestNestedTerminatingInterceptorEndsTheWrapper_TS21_31(t *testing.T) {
	cases := []struct {
		name string
		ts   func(wrapper core.Tool) Toolset
	}{
		{"a guard block that terminates", func(w core.Tool) Toolset {
			return Toolset{Tools: []core.Tool{w}, Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
				return core.BeforeToolCallDecision{Block: in.ParentToolUseID != "", Terminate: true, Reason: "stop"}
			}}
		}},
		{"an After that terminates", func(w core.Tool) Toolset {
			return Toolset{Tools: []core.Tool{w}, After: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				vote := in.ParentToolUseID != ""
				if !vote {
					return core.AfterToolCallDecision{}
				}
				return core.AfterToolCallDecision{Terminate: &vote}
			}}
		}},
	}
	for _, c := range cases {
		var first, second error
		wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
			_, first = core.CallNested(ctx, nestedCalls("child")...)
			_, second = core.CallNested(ctx, nestedCalls("child")...)
			return core.OKResult(nil)
		}, childTool("child"))
		res := mustRunner(t, c.ts(wrapper)).Call(context.Background(), "wrapper", nil)
		if !errors.Is(first, core.ErrTerminated) || !errors.Is(second, core.ErrTerminated) {
			t.Errorf("%s: CallNested errors = %v, %v; want ErrTerminated twice", c.name, first, second)
		}
		if !res.Terminate {
			t.Errorf("%s: the wrapper's result = %+v; want Terminate", c.name, res)
		}
	}
}

// TS-21-32: a nested handler's own Terminate vote is dropped, and both results
// record that it was.
func TestNestedHandlerVoteIsDroppedAndAnnotated_TS21_32(t *testing.T) {
	child := core.Tool{Name: "child", InputSchema: childTool("child").InputSchema,
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			r := core.OKResult(nil)
			r.Terminate, r.Detail = true, "done"
			return r
		}}
	var nested []core.ToolResult
	wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		nested, _ = core.CallNested(ctx, nestedCalls("child")...)
		r := core.OKResult(nil)
		r.Detail = "wrapper finished"
		return r
	}, child)
	res := mustRunner(t, Toolset{Tools: []core.Tool{wrapper}}).Call(context.Background(), "wrapper", nil)

	if len(nested) != 1 || nested[0].Terminate || !strings.Contains(nested[0].LLMText(), "terminate vote ignored: ") {
		t.Errorf("the nested result = %+v; want the vote dropped and noted", nested)
	}
	if res.Terminate || !strings.Contains(res.LLMText(), "nested terminate vote ignored") {
		t.Errorf("the wrapper's result = %+v; want no vote and a note", res)
	}
}

// TS-21-33: a nested call made after the context is cancelled is aborted, and
// its handler does not run.
func TestNestedCallUnderCancelledContextIsAborted_TS21_33(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ran atomic.Int32
	var nested []core.ToolResult
	wrapper := wrapperTool("wrapper", func(c context.Context) core.ToolResult {
		cancel()
		nested, _ = core.CallNested(c, nestedCalls("echo")...)
		return core.OKResult(nil)
	}, echoTool("echo", &ran))
	mustRunner(t, Toolset{Tools: []core.Tool{wrapper}}).Call(ctx, "wrapper", nil)

	if len(nested) != 1 || nested[0].OK || nested[0].Error != "aborted" || nested[0].Detail != "Operation aborted" {
		t.Fatalf("the nested result = %+v; want aborted with %q", nested, "Operation aborted")
	}
	if ran.Load() != 0 {
		t.Fatal("the nested handler ran under a cancelled context")
	}
}

// TS-21-34: a code_mode tool built over tools.For and called through
// Runner.Call runs a script that reads a workspace file, with no agent and no
// provider. The nested read_file call reaches Toolset.Events with the
// code_mode call as its parent.
func TestCodeModeThroughCallReadsAWorkspaceFile_TS21_34(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "a.txt"), []byte("alpha content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var readFile core.Tool
	for _, tl := range all {
		if tl.Name == "read_file" {
			readFile = tl
		}
	}
	cm, _, err := codemode.New([]core.Tool{readFile}, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{cm}, Events: events.add})

	script, _ := json.Marshal(map[string]string{"script": "res = read_file(path=\"a.txt\")\nprint(res[\"content\"].strip())\n"})
	res := r.Call(context.Background(), cm.Name, script)
	if !res.OK || !strings.Contains(res.LLMText(), "alpha content") {
		t.Fatalf("Call = %+v; want OK with the file's content", res)
	}

	var cmID string
	var nestedStart, nestedEnd, results int
	for _, e := range events.all() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.Name == cm.Name {
				cmID = ev.ToolUseID
			}
		case core.ToolResultEvent:
			results++
		}
	}
	for _, e := range events.all() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.Name == "read_file" && ev.ParentToolUseID == cmID {
				nestedStart++
			}
		case core.ToolExecutionEndEvent:
			if ev.Name == "read_file" && ev.ParentToolUseID == cmID {
				nestedEnd++
			}
		}
	}
	if cmID == "" || nestedStart != 1 || nestedEnd != 1 || results != 1 {
		t.Errorf("code_mode id %q, nested read_file start/end events %d/%d, ToolResultEvents %d; want one of each and a single result event",
			cmID, nestedStart, nestedEnd, results)
	}
}

// TS-21-35: a tool that declares no ReachableTools gets no nested caller.
func TestToolWithoutReachableToolsHasNoNestedCaller_TS21_35(t *testing.T) {
	var nestedErr error
	loner := core.Tool{Name: "loner", InputSchema: childTool("x").InputSchema,
		Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
			_, nestedErr = core.CallNested(ctx, nestedCalls("loner")...)
			return core.OKResult(nil)
		}}
	mustRunner(t, Toolset{Tools: []core.Tool{loner}}).Call(context.Background(), "loner", nil)
	if !errors.Is(nestedErr, core.ErrNoNestedCaller) {
		t.Fatalf("CallNested from a tool with no ReachableTools = %v, want ErrNoNestedCaller", nestedErr)
	}
}
