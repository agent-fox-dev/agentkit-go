package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestCallNested_NeedsAToolThatReachesOthers(t *testing.T) {
	var err error
	loner := tool("loner", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		_, err = core.CallNested(ctx, nested("loner")...)
		return core.OKResult(nil)
	})
	mustRunner(t, Toolset{Tools: []core.Tool{loner}}).Call(context.Background(), "loner", nil)
	if !errors.Is(err, core.ErrNoNestedCaller) {
		t.Fatalf("CallNested from a tool reaching nothing = %v, want ErrNoNestedCaller", err)
	}
}

// TestCallNested_AnswersEachFailingCallWithAResultOfItsOwn sends one group of
// calls that fail in every way a nested call can; the siblings still run.
func TestCallNested_AnswersEachFailingCallWithAResultOfItsOwn(t *testing.T) {
	var events eventLog
	var got []core.ToolResult
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		got, _ = core.CallNested(ctx, core.ToolUseBlock{Name: "other"}, core.ToolUseBlock{Name: "fails"}, core.ToolUseBlock{Name: "panics"},
			core.ToolUseBlock{Name: "strict", Input: json.RawMessage(`{"n":"x"}`)}, core.ToolUseBlock{Name: "fails", Input: json.RawMessage(`[1]`)},
			core.ToolUseBlock{Name: "shim"}, core.ToolUseBlock{Name: "denied"}, core.ToolUseBlock{Name: "rewritten", Input: json.RawMessage(`{"v":"a"}`)},
			core.ToolUseBlock{Name: "broken"}, core.ToolUseBlock{Name: "ok"})
		return core.OKResult(nil)
	}, returning("fails", core.ErrResult("failed", "no")), tool("panics", func(context.Context, json.RawMessage) core.ToolResult { panic("kaboom") }),
		core.Tool{Name: "strict", InputSchema: schema.Object(schema.Prop("n", schema.Int())), Execute: child("strict").Execute},
		core.Tool{Name: "shim", InputSchema: schema.Object(), Execute: child("shim").Execute, PrepareArguments: func(map[string]any) map[string]any { panic("shim broke") }},
		child("denied"), child("rewritten"), child("broken"), child("ok"))
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		switch in.ToolName {
		case "denied":
			return core.BeforeToolCallDecision{Block: true}
		case "rewritten":
			return core.BeforeToolCallDecision{Arguments: map[string]any{"v": "b"}}
		case "broken":
			return core.BeforeToolCallDecision{Arguments: map[string]any{"v": math.Inf(1)}}
		}
		return core.BeforeToolCallDecision{}
	}
	mustRunner(t, Toolset{Tools: []core.Tool{w, child("other")}, Guard: guard, Events: events.Add}).Call(context.Background(), "wrap", nil)
	want := []struct{ code, detail string }{
		{"unknown_tool", `wrap cannot call "other": no tool named "other" is available in this run; available tools: broken, denied, fails, ok, panics, rewritten, shim, strict`},
		{"failed", "no"}, {"panic", `tool "panics" panicked: kaboom`}, {"invalid_arguments", ""}, {"invalid_arguments", ""},
		{"invalid_arguments", `argument preparation for "shim" panicked: shim broke`}, {core.BlockErrorCode, "blocked by policy"},
		{"", ""}, {"invalid_arguments", "BeforeToolCall returned arguments that are not JSON: "}, {"", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("%d results, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Error != w.code || !strings.HasPrefix(got[i].Detail, w.detail) {
			t.Errorf("call %d = %+v, want %q %q", i, got[i], w.code, w.detail)
		}
	}
	if got[7].Data["args"] != `{"v":"b"}` || !got[9].OK {
		t.Errorf("the rewritten call ran with %v, the last sibling %+v", got[7].Data["args"], got[9])
	}
	starts, ends, _ := executions(events.All())
	if len(starts) != 11 || len(ends) != 11 {
		t.Errorf("%d calls opened and %d closed, want all 11", len(starts), len(ends))
	}
}

func TestCallNested_RunsConcurrentlyUnlessASequentialToolIsCalledAndKeepsCallOrder(t *testing.T) {
	for _, mode := range []core.ExecutionMode{core.Parallel, core.Sequential} {
		wait := 5 * time.Second
		if mode == core.Sequential {
			wait = 20 * time.Millisecond
		}
		m := meet(3, wait)
		var got []core.ToolResult
		w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
			var group []core.ToolUseBlock
			for i, n := range []string{"par", "mode", "par"} {
				group = append(group, core.ToolUseBlock{Name: n, Input: json.RawMessage(fmt.Sprintf(`{"v":"%d"}`, i))})
			}
			got, _ = core.CallNested(ctx, group...)
			return core.OKResult(nil)
		}, attending("par", m, core.Parallel), attending("mode", m, mode))
		mustRunner(t, Toolset{Tools: []core.Tool{w}}).Call(context.Background(), "wrap", nil)
		want := int32(3)
		if mode == core.Sequential {
			want = 1
		}
		if m.Peak() != want || len(got) != 3 {
			t.Errorf("%v: %d ran at once, %d results; want %d", mode, m.Peak(), len(got), want)
		}
		for i, r := range got {
			if r.Data["args"] != fmt.Sprintf(`{"v":"%d"}`, i) {
				t.Errorf("%v: result %d is %v", mode, i, r.Data["args"])
			}
		}
	}
}

func TestCallNested_DropsAHandlersTerminateVoteAndSaysSo(t *testing.T) {
	var deep, top []core.ToolResult
	stop := returning("stop", core.ToolResult{OK: true, Terminate: true, Detail: "done"})
	inner := wrapper("inner", func(ctx context.Context) core.ToolResult {
		deep, _ = core.CallNested(ctx, nested("stop")...)
		return core.OKResult(nil)
	}, stop)
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		top, _ = core.CallNested(ctx, nested("inner", "stop")...)
		return core.ToolResult{OK: true, Detail: "wrapper finished"}
	}, inner, stop)
	res := mustRunner(t, Toolset{Tools: []core.Tool{w}}).Call(context.Background(), "wrap", nil)
	if len(deep) != 1 || deep[0].Terminate || deep[0].Detail != "done; terminate vote ignored: stop was called through inner" {
		t.Errorf("the vote two levels down = %+v", deep)
	}
	if len(top) != 2 || top[0].Detail != "nested terminate vote ignored" || top[1].Terminate ||
		top[1].Detail != "done; terminate vote ignored: stop was called through wrap" {
		t.Errorf("the wrapper's nested results = %+v", top)
	}
	if res.Terminate || res.Detail != "wrapper finished; nested terminate vote ignored" {
		t.Errorf("the wrapper's result = %+v", res)
	}
}

func TestCallNested_AbortsUnderACancelledContext(t *testing.T) {
	var ran atomic.Int32
	var before, during []core.ToolResult
	slow := tool("slow", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		ran.Add(1)
		<-ctx.Done()
		return core.OKResult(nil)
	})
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		cctx, cancel := context.WithCancel(ctx)
		go func() { testutil.Within(5*time.Second, func() bool { return ran.Load() > 0 }); cancel() }()
		during, _ = core.CallNested(cctx, nested("slow")...)
		before, _ = core.CallNested(cctx, nested("slow")...)
		return core.OKResult(nil)
	}, slow)
	mustRunner(t, Toolset{Tools: []core.Tool{w}}).Call(context.Background(), "wrap", nil)
	for _, got := range [][]core.ToolResult{during, before} {
		if len(got) != 1 || got[0].Error != "aborted" || got[0].Detail != "Operation aborted" {
			t.Errorf("result %+v, want aborted", got)
		}
	}
	if ran.Load() != 1 {
		t.Errorf("the handler started %d times; a call made after the cancel must not start", ran.Load())
	}
}

func TestCallNested_AnAfterHookMayCallNested(t *testing.T) {
	var reentered []core.ToolResult
	after := func(ctx context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		if in.ParentToolUseID != "" {
			reentered, _ = core.CallNested(ctx, nested("missing")...)
		}
		return core.AfterToolCallDecision{}
	}
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, nested("a")...)
		return core.OKResult(nil)
	}, child("a"))
	done := make(chan struct{})
	go func() {
		mustRunner(t, Toolset{Tools: []core.Tool{w}, After: after}).Call(context.Background(), "wrap", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a nested call made from After deadlocked")
	}
	if len(reentered) != 1 {
		t.Errorf("the re-entrant call returned %+v", reentered)
	}
}
