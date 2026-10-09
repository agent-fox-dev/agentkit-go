package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// errBad is a sentinel error for the handler-error tool.
var errBad = errors.New("bad")

// ---------------------------------------------------------------- TS-04-42

// ts0442Recorded holds the AfterToolCall capture for TS-04-42.
type ts0442Recorded struct {
	ToolResult core.ToolResult
	Result     *core.ToolResultMessage
}

// runTS0442 sets up and runs the agent for TS-04-42, returning the captured
// AfterToolCall records and ToolResultEvent messages.
func runTS0442(t *testing.T) (map[string]ts0442Recorded, []core.ToolResultMessage) {
	t.Helper()
	two := 2
	probeMD := &core.ToolMetadata{ExitCode: &two, Outcome: "exit"}
	probeResult := core.ToolResult{
		OK:       false,
		Error:    "command_exit",
		Text:     "x",
		Data:     map[string]any{"k": float64(1)},
		Metadata: probeMD,
	}

	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return probeResult
		},
	}

	handlerErrTool := core.Tool{
		Name: "herr", Description: "handler err", InputSchema: schema.Object(),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			return nil, errBad
		},
	}

	var mu sync.Mutex
	recs := map[string]ts0442Recorded{}
	var toolResultEvents []core.ToolResultMessage

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c_probe", "probe", `{}`),
			toolUse(t, "c_herr", "herr", `{}`),
		),
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			mu.Lock()
			recs[in.ToolName] = ts0442Recorded{ToolResult: in.ToolResult, Result: in.Result}
			mu.Unlock()
			return core.AfterToolCallDecision{}
		}
	}, probeTool, handlerErrTool)

	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for e := range st.Events() {
		if tre, ok := e.(core.ToolResultEvent); ok {
			toolResultEvents = append(toolResultEvents, tre.Message)
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatal(err)
	}
	return recs, toolResultEvents
}

// TestAfterToolCallReceivesHandlerToolResult_TS04_42 verifies that
// AfterToolCall receives the handler's ToolResult by value and a Result
// message already carrying its Metadata.
func TestAfterToolCallReceivesHandlerToolResult_TS04_42(t *testing.T) {
	recs, toolResultEvents := runTS0442(t)

	two := 2
	probeMD := &core.ToolMetadata{ExitCode: &two, Outcome: "exit"}

	t.Run("probe_tool_result_fields", func(t *testing.T) {
		pr, ok := recs["probe"]
		if !ok {
			t.Fatal("no AfterToolCall record for probe")
		}
		if pr.ToolResult.OK {
			t.Fatalf("probe ToolResult.OK = true, want false")
		}
		if pr.ToolResult.Error != "command_exit" {
			t.Fatalf("probe ToolResult.Error = %q, want %q", pr.ToolResult.Error, "command_exit")
		}
		if pr.ToolResult.Text != "x" {
			t.Fatalf("probe ToolResult.Text = %q, want %q", pr.ToolResult.Text, "x")
		}
		if !reflect.DeepEqual(pr.ToolResult.Data, map[string]any{"k": float64(1)}) {
			t.Fatalf("probe ToolResult.Data = %v, want {k:1}", pr.ToolResult.Data)
		}
		if pr.ToolResult.Metadata == nil {
			t.Fatal("probe ToolResult.Metadata is nil")
		}
		if pr.ToolResult.Metadata.ExitCode == nil || *pr.ToolResult.Metadata.ExitCode != two {
			t.Fatalf("probe ToolResult.Metadata.ExitCode = %v, want %d", pr.ToolResult.Metadata.ExitCode, two)
		}
		if pr.ToolResult.Metadata.Outcome != "exit" {
			t.Fatalf("probe ToolResult.Metadata.Outcome = %q, want %q", pr.ToolResult.Metadata.Outcome, "exit")
		}
	})

	t.Run("probe_result_message_metadata", func(t *testing.T) {
		pr := recs["probe"]
		if pr.Result == nil {
			t.Fatal("probe Result is nil")
		}
		if pr.Result.Metadata == nil {
			t.Fatal("probe Result.Metadata is nil")
		}
		if !reflect.DeepEqual(*pr.Result.Metadata, *probeMD) {
			t.Fatalf("probe Result.Metadata = %+v, want %+v", *pr.Result.Metadata, *probeMD)
		}

		var probeEvt *core.ToolResultMessage
		for i := range toolResultEvents {
			if toolResultEvents[i].ToolName == "probe" {
				probeEvt = &toolResultEvents[i]
				break
			}
		}
		if probeEvt == nil {
			t.Fatal("no ToolResultEvent for probe")
		}
		if !reflect.DeepEqual(probeEvt.Content, pr.Result.Content) {
			t.Fatalf("ToolResultEvent content differs from Result")
		}
	})

	t.Run("handler_error_nil_metadata", func(t *testing.T) {
		hr, ok := recs["herr"]
		if !ok {
			t.Fatal("no AfterToolCall record for herr")
		}
		if hr.ToolResult.OK {
			t.Fatal("herr ToolResult.OK should be false")
		}
		if hr.ToolResult.Error != "handler_error" {
			t.Fatalf("herr ToolResult.Error = %q, want %q", hr.ToolResult.Error, "handler_error")
		}
		if hr.Result.Metadata != nil {
			t.Fatalf("herr Result.Metadata = %+v, want nil", hr.Result.Metadata)
		}
	})
}

// ---------------------------------------------------------------- TS-04-43

// ts0443CheckEdited asserts that metadata has the edited values.
func ts0443CheckEdited(t *testing.T, label string, md *core.ToolMetadata) {
	t.Helper()
	if md == nil {
		t.Fatalf("%s: Metadata is nil", label)
	}
	if md.Outcome != "edited" {
		t.Fatalf("%s: Outcome = %q, want %q", label, md.Outcome, "edited")
	}
	if md.ExitCode == nil || *md.ExitCode != 99 {
		t.Fatalf("%s: ExitCode = %v, want 99", label, md.ExitCode)
	}
}

// ts0443Run sets up and runs the agent for TS-04-43, returning captured data.
func ts0443Run(t *testing.T) (
	toolResultEvents []core.ToolResultMessage,
	stopResults []core.ToolResultMessage,
	agent *Agent,
) {
	t.Helper()
	two := 2
	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       false,
				Error:    "command_exit",
				Text:     "x",
				Metadata: &core.ToolMetadata{ExitCode: &two, Outcome: "exit"},
			}
		},
	}

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "probe", `{}`)),
	}}
	agent = newTestAgent(t, s, func(c *Config) {
		c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			in.Result.Metadata.Outcome = "edited"
			*in.Result.Metadata.ExitCode = 99
			return core.AfterToolCallDecision{}
		}
	}, probeTool)

	st, err := agent.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for e := range st.Events() {
		if tre, ok := e.(core.ToolResultEvent); ok {
			toolResultEvents = append(toolResultEvents, tre.Message)
		}
		if te, ok := e.(core.TurnEndEvent); ok && len(te.ToolResults) > 0 {
			stopResults = te.ToolResults
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatal(err)
	}
	return toolResultEvents, stopResults, agent
}

// TestMetadataEditedThroughResultPropagates_TS04_43 verifies that metadata
// edited through in.Result is what is emitted, reported at the turn's end and
// kept in the transcript.
func TestMetadataEditedThroughResultPropagates_TS04_43(t *testing.T) {
	toolResultEvents, stopResults, a := ts0443Run(t)

	t.Run("event_and_stop_and_history", func(t *testing.T) {
		if len(toolResultEvents) == 0 {
			t.Fatal("no ToolResultEvent")
		}
		ts0443CheckEdited(t, "ToolResultEvent", toolResultEvents[0].Metadata)

		if len(stopResults) == 0 {
			t.Fatal("no TurnEndEvent results")
		}
		ts0443CheckEdited(t, "TurnEndEvent", stopResults[0].Metadata)

		for _, m := range a.Messages() {
			if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
				ts0443CheckEdited(t, "History", tr.Metadata)
			}
		}
	})
}

// ---------------------------------------------------------------- TS-04-44

// TestToolResultCopyChangesDoNotAffectMessage_TS04_44 verifies that changes
// a hook makes to its ToolResult copy, including through its Metadata pointer,
// leave the emitted message unchanged.
func TestToolResultCopyChangesDoNotAffectMessage_TS04_44(t *testing.T) {
	two := 2
	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       false,
				Error:    "command_exit",
				Text:     "x",
				Data:     map[string]any{"k": float64(1)},
				Metadata: &core.ToolMetadata{ExitCode: &two, Outcome: "exit"},
			}
		},
	}

	var snapshot core.ToolResultMessage
	var toolResultEvents []core.ToolResultMessage
	var stopResults []core.ToolResultMessage

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "probe", `{}`)),
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			snapshot = in.Result.Clone().(core.ToolResultMessage)
			in.ToolResult.Text = "y"
			in.ToolResult.Data["k"] = float64(2)
			in.ToolResult.Metadata.Outcome = "hacked"
			*in.ToolResult.Metadata.ExitCode = 7
			return core.AfterToolCallDecision{}
		}
	}, probeTool)

	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for e := range st.Events() {
		if tre, ok := e.(core.ToolResultEvent); ok {
			toolResultEvents = append(toolResultEvents, tre.Message)
		}
		if te, ok := e.(core.TurnEndEvent); ok && len(te.ToolResults) > 0 {
			stopResults = te.ToolResults
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatal(err)
	}

	checkUnchanged := func(label string, msg core.ToolResultMessage) {
		t.Helper()
		if !reflect.DeepEqual(msg.Content, snapshot.Content) {
			t.Fatalf("%s: Content differs from snapshot", label)
		}
		if msg.IsError != snapshot.IsError {
			t.Fatalf("%s: IsError = %v, want %v", label, msg.IsError, snapshot.IsError)
		}
		if msg.Metadata == nil || snapshot.Metadata == nil {
			t.Fatalf("%s: Metadata is nil (msg=%v, snap=%v)", label, msg.Metadata, snapshot.Metadata)
		}
		if !reflect.DeepEqual(*msg.Metadata, *snapshot.Metadata) {
			t.Fatalf("%s: Metadata = %+v, want %+v", label, *msg.Metadata, *snapshot.Metadata)
		}
	}

	t.Run("observers_match_snapshot", func(t *testing.T) {
		if len(toolResultEvents) == 0 {
			t.Fatal("no ToolResultEvent")
		}
		checkUnchanged("ToolResultEvent", toolResultEvents[0])

		for _, m := range a.Messages() {
			if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
				checkUnchanged("History", tr)
			}
		}

		if len(stopResults) == 0 {
			t.Fatal("no TurnEndEvent results")
		}
		checkUnchanged("TurnEndEvent", stopResults[0])

		if snapshot.Metadata.Outcome != "exit" {
			t.Fatalf("snapshot Outcome = %q, want %q", snapshot.Metadata.Outcome, "exit")
		}
		if snapshot.Metadata.ExitCode == nil || *snapshot.Metadata.ExitCode != 2 {
			t.Fatalf("snapshot ExitCode = %v, want 2", snapshot.Metadata.ExitCode)
		}
	})
}

// ---------------------------------------------------------------- TS-04-45

// TestPanicHandlerAfterToolCallNilMetadata_TS04_45 verifies that after a
// handler panic, AfterToolCall gets the panic-converted ToolResult and a
// Result with nil Metadata.
func TestPanicHandlerAfterToolCallNilMetadata_TS04_45(t *testing.T) {
	panicTool := core.Tool{
		Name: "panicker", Description: "panics", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			panic("kaboom")
		},
	}

	type recorded struct {
		ToolResult core.ToolResult
		Result     *core.ToolResultMessage
	}
	var rec recorded

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "panicker", `{}`)),
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			rec = recorded{ToolResult: in.ToolResult, Result: in.Result}
			return core.AfterToolCallDecision{}
		}
	}, panicTool)

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if rec.ToolResult.OK {
		t.Fatal("ToolResult.OK should be false after panic")
	}
	if rec.ToolResult.Error != "panic" {
		t.Fatalf("ToolResult.Error = %q, want %q", rec.ToolResult.Error, "panic")
	}
	if rec.ToolResult.Metadata != nil {
		t.Fatalf("ToolResult.Metadata = %+v, want nil", rec.ToolResult.Metadata)
	}
	if rec.Result == nil {
		t.Fatal("Result is nil")
	}
	if rec.Result.Metadata != nil {
		t.Fatalf("Result.Metadata = %+v, want nil", rec.Result.Metadata)
	}
	if !rec.Result.IsError {
		t.Fatal("Result.IsError should be true")
	}
}

// ---------------------------------------------------------------- TS-04-46

// ts0446Tools returns the terminating tool and metadata used by TS-04-46 subtests.
func ts0446Tools() (core.Tool, *core.ToolMetadata) {
	ec := 0
	md := &core.ToolMetadata{ExitCode: &ec, Outcome: "ok", DurationMS: 5}
	terminatingTool := core.Tool{
		Name: "term", Description: "terminates", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:        true,
				Data:      map[string]any{"done": true},
				Terminate: true,
				Metadata:  md,
			}
		},
	}
	return terminatingTool, md
}

// TestTerminationVotesWithMetadata_TS04_46_NilAndFalseVote verifies that
// nil and false termination votes work correctly with metadata-carrying tools.
func TestTerminationVotesWithMetadata_TS04_46_NilAndFalseVote(t *testing.T) {
	terminatingTool, _ := ts0446Tools()

	t.Run("nil_vote_ends_on_tool_vote", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "term", `{}`)),
			{Content: core.Content{core.TextBlock{Text: "should not reach"}}, StopReason: core.StopReasonStop},
		}}
		a := newTestAgent(t, s, func(c *Config) {
			c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				return core.AfterToolCallDecision{Terminate: nil}
			}
		}, terminatingTool)
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		if s.turnsRun() != 1 {
			t.Fatalf("turnsRun = %d, want 1", s.turnsRun())
		}
		for _, m := range res.Messages {
			if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
				if tr.Metadata == nil {
					t.Fatal("Metadata is nil on terminated tool result")
				}
			}
		}
	})

	t.Run("false_vote_continues", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "term", `{}`)),
			{Content: core.Content{core.TextBlock{Text: "continued"}}, StopReason: core.StopReasonStop},
		}}
		f := false
		a := newTestAgent(t, s, func(c *Config) {
			c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				return core.AfterToolCallDecision{Terminate: &f}
			}
		}, terminatingTool)
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if s.turnsRun() != 2 {
			t.Fatalf("turnsRun = %d, want 2", s.turnsRun())
		}
	})
}

// TestImageNormalizationPreservesMetadata_TS04_46 verifies that metadata
// survives image normalization.
func TestImageNormalizationPreservesMetadata_TS04_46(t *testing.T) {
	_, md := ts0446Tools()

	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       true,
				Data:     map[string]any{"ok": true},
				Metadata: md,
			}
		},
	}

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "probe", `{}`)),
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.After = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			in.Result.Content = append(in.Result.Content, core.ImageBlock{
				Data:     "aGVsbG8=",
				MimeType: "image/png",
			})
			return core.AfterToolCallDecision{}
		}
	}, probeTool)

	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			if tr.Metadata == nil {
				t.Fatal("Metadata is nil after image normalization")
			}
			if tr.Metadata.Outcome != "ok" {
				t.Fatalf("Metadata.Outcome = %q, want %q", tr.Metadata.Outcome, "ok")
			}
			if tr.Metadata.ExitCode == nil || *tr.Metadata.ExitCode != 0 {
				t.Fatalf("Metadata.ExitCode = %v, want 0", tr.Metadata.ExitCode)
			}
			return
		}
	}
	t.Fatal("no tool result found in RunResult")
}

// TestBatchAndSemanticsWithMetadata_TS04_46 verifies AND semantics with
// metadata-carrying tools.
func TestBatchAndSemanticsWithMetadata_TS04_46(t *testing.T) {
	terminatingTool, md := ts0446Tools()

	nonTermTool := core.Tool{
		Name: "nterm", Description: "non-terminating", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:        true,
				Data:      map[string]any{"ok": true},
				Terminate: false,
				Metadata:  md,
			}
		},
	}

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "term", `{}`),
			toolUse(t, "c2", "nterm", `{}`),
		),
		{Content: core.Content{core.TextBlock{Text: "continued"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.MaxTurns = 10
	}, terminatingTool, nonTermTool)
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if s.turnsRun() != 2 {
		t.Fatalf("turnsRun = %d, want 2 (AND semantics)", s.turnsRun())
	}
}
