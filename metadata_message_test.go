package agentkit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// ---------------------------------------------------------------- helpers

type mdScripted struct {
	mu    sync.Mutex
	turns []core.AssistantMessage
	calls int
}

func (s *mdScripted) stream(_ context.Context, m *core.Model, req core.Request, _ core.ProviderStreamOptions) *core.EventStream {
	st := core.NewEventStream(core.StreamOptions{})
	s.mu.Lock()
	i := s.calls
	s.calls++
	var msg core.AssistantMessage
	if i < len(s.turns) {
		msg = s.turns[i]
	} else {
		msg = core.AssistantMessage{
			Content:    core.Content{core.TextBlock{Text: "done"}},
			StopReason: core.StopReasonStop,
		}
	}
	s.mu.Unlock()
	msg.Model = m.ID
	go func() {
		st.Push(core.MessageStartEvent{Message: msg})
		st.Push(core.MessageEndEvent{Message: msg})
		st.End(core.StreamResult{Message: &msg})
	}()
	return st
}

func mdToolUse(t *testing.T, id, name, args string) core.ToolUseBlock {
	t.Helper()
	b, err := core.NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("NewToolUse: %v", err)
	}
	return b
}

func mdAssistantWithTools(reason core.StopReason, blocks ...core.ContentBlock) core.AssistantMessage {
	return core.AssistantMessage{Content: core.Content(blocks), StopReason: reason}
}

func mdNewTestAgent(t *testing.T, s *mdScripted, mutate func(*agentkit.Config), tools ...core.Tool) *agentkit.Agent {
	t.Helper()
	cfg := agentkit.Config{
		Provider: core.ClientFunc(s.stream),
		Model:    "md-test-model",
		MaxTurns: 10,
		Tools:    tools,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := agentkit.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func mdFindToolResult(t *testing.T, msgs core.Messages, id string) core.ToolResultMessage {
	t.Helper()
	for _, m := range msgs {
		if r, ok := m.(core.ToolResultMessage); ok && r.ToolUseID == id {
			return r
		}
	}
	t.Fatalf("no tool result for %q in %d messages", id, len(msgs))
	return core.ToolResultMessage{}
}

func mdCollectToolResultEvents(t *testing.T, a *agentkit.Agent, s *mdScripted, prompt string) (core.RunResult, []core.ToolResultMessage) {
	t.Helper()
	st, err := a.Stream(context.Background(), prompt)
	if err != nil {
		t.Fatal(err)
	}
	var events []core.ToolResultMessage
	for e := range st.Events() {
		if tre, ok := e.(core.ToolResultEvent); ok {
			events = append(events, tre.Message)
		}
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}
	return res, events
}

// ---------------------------------------------------------------- TS-04-33

// TestNonEmptyHandlerMetadataCopiedOntoMessage_TS04_33 verifies that a tool's
// non-empty Metadata is deep-copied onto the ToolResultMessage with its own
// ExitCode pointer.
func TestNonEmptyHandlerMetadataCopiedOntoMessage_TS04_33(t *testing.T) {
	two := 2
	md := &core.ToolMetadata{ExitCode: &two, Outcome: "exit", TotalBytes: 5, DurationMS: 12}

	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       false,
				Error:    "command_exit",
				Text:     "boom\n[exit 2]",
				Metadata: md,
			}
		},
	}

	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse, mdToolUse(t, "c1", "probe", `{}`)),
	}}
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, probeTool)

	res, events := mdCollectToolResultEvents(t, a, s, "go")

	// Check ToolResultEvent message.
	if len(events) == 0 {
		t.Fatal("no ToolResultEvent captured")
	}
	msg := events[0]
	if msg.Metadata == nil {
		t.Fatal("msg.Metadata is nil, want non-nil")
	}
	if !reflect.DeepEqual(*msg.Metadata, *md) {
		t.Fatalf("msg.Metadata = %+v, want %+v", *msg.Metadata, *md)
	}
	// Pointer identity must differ.
	if msg.Metadata == md {
		t.Fatal("msg.Metadata must be a different pointer than the handler's md")
	}
	if msg.Metadata.ExitCode == md.ExitCode {
		t.Fatal("msg.Metadata.ExitCode must be a different pointer than md.ExitCode")
	}

	// Also check RunResult.Messages.
	rmsg := mdFindToolResult(t, res.Messages, "c1")
	if rmsg.Metadata == nil {
		t.Fatal("RunResult message Metadata is nil")
	}
	if *rmsg.Metadata.ExitCode != 2 {
		t.Fatalf("RunResult ExitCode = %d, want 2", *rmsg.Metadata.ExitCode)
	}

	// ExitCode-0-only variant.
	zero := 0
	probeTool2 := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       true,
				Data:     map[string]any{"ok": true},
				Metadata: &core.ToolMetadata{ExitCode: &zero},
			}
		},
	}
	s2 := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse, mdToolUse(t, "c1", "probe", `{}`)),
	}}
	a2 := mdNewTestAgent(t, s2, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, probeTool2)
	_, events2 := mdCollectToolResultEvents(t, a2, s2, "go")
	if len(events2) == 0 {
		t.Fatal("no ToolResultEvent for ExitCode-0 variant")
	}
	msg0 := events2[0]
	if msg0.Metadata == nil {
		t.Fatal("ExitCode-0 variant: msg.Metadata is nil")
	}
	if msg0.Metadata.ExitCode == nil || *msg0.Metadata.ExitCode != 0 {
		t.Fatalf("ExitCode-0 variant: ExitCode = %v, want pointer to 0", msg0.Metadata.ExitCode)
	}
}

// ---------------------------------------------------------------- TS-04-34

// TestMetadataReuseDoesNotAlias_TS04_34 verifies that a tool reusing its
// metadata value or ExitCode pointer cannot alias any message, in sequential
// turns or a parallel batch.
func TestMetadataReuseDoesNotAlias_TS04_34(t *testing.T) {
	// Run with -race to detect data races.
	var mu sync.Mutex
	var retained []*core.ToolMetadata
	callIdx := 0

	reusingTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			mu.Lock()
			idx := callIdx
			callIdx++
			mu.Unlock()

			md := &core.ToolMetadata{
				ExitCode: new(int),
				Outcome:  fmt.Sprintf("call-%d", idx),
			}
			*md.ExitCode = idx

			mu.Lock()
			retained = append(retained, md)
			mu.Unlock()

			return core.ToolResult{
				OK:       true,
				Data:     map[string]any{"idx": idx},
				Metadata: md,
			}
		},
	}

	// 2 sequential turns, each with 2 parallel calls.
	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse,
			mdToolUse(t, "c1", "probe", `{}`),
			mdToolUse(t, "c2", "probe", `{}`),
		),
		mdAssistantWithTools(core.StopReasonToolUse,
			mdToolUse(t, "c3", "probe", `{}`),
			mdToolUse(t, "c4", "probe", `{}`),
		),
	}}
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, reusingTool)

	res, events := mdCollectToolResultEvents(t, a, s, "go")

	// After Run returns, mutate all retained metadata.
	mu.Lock()
	for _, md := range retained {
		md.Outcome = "mutated"
		*md.ExitCode = 999
	}
	mu.Unlock()

	// Check that every message still carries the metadata recorded when its
	// handler returned.
	checkMsg := func(label string, msg core.ToolResultMessage) {
		t.Helper()
		if msg.Metadata == nil {
			t.Fatalf("%s: Metadata is nil", label)
		}
		if msg.Metadata.ExitCode == nil {
			t.Fatalf("%s: ExitCode is nil", label)
		}
		ec := *msg.Metadata.ExitCode
		wantOutcome := fmt.Sprintf("call-%d", ec)
		if msg.Metadata.Outcome != wantOutcome {
			t.Fatalf("%s: Outcome = %q, want %q (ExitCode=%d)", label, msg.Metadata.Outcome, wantOutcome, ec)
		}
	}

	for _, msg := range events {
		checkMsg("ToolResultEvent/"+msg.ToolUseID, msg)
	}
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok {
			checkMsg("RunResult/"+tr.ToolUseID, tr)
		}
	}
	for _, m := range a.Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok {
			checkMsg("History/"+tr.ToolUseID, tr)
		}
	}
}

// ---------------------------------------------------------------- TS-04-35

// TestNilOrZeroMetadataLeavesMessageNil_TS04_35 verifies that nil or all-zero
// handler metadata leaves the message's Metadata nil.
func TestNilOrZeroMetadataLeavesMessageNil_TS04_35(t *testing.T) {
	cases := []struct {
		name string
		md   *core.ToolMetadata
	}{
		{"nil", nil},
		{"empty", &core.ToolMetadata{}},
		{"duration_zero", &core.ToolMetadata{DurationMS: 0}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md := tc.md
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
			s := &mdScripted{turns: []core.AssistantMessage{
				mdAssistantWithTools(core.StopReasonToolUse, mdToolUse(t, "c1", "probe", `{}`)),
			}}
			a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
				c.Guard = guard.AllowAll
			}, probeTool)

			_, events := mdCollectToolResultEvents(t, a, s, "go")
			if len(events) == 0 {
				t.Fatal("no ToolResultEvent")
			}
			if events[0].Metadata != nil {
				t.Fatalf("msg.Metadata = %+v, want nil", events[0].Metadata)
			}

			// Also check history.
			for _, m := range a.Messages() {
				if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
					if tr.Metadata != nil {
						t.Fatalf("history Metadata = %+v, want nil", tr.Metadata)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------- TS-04-36

// runTS0436 sets up and runs the agent for TS-04-36, returning the run result
// and captured ToolResultEvent messages.
func runTS0436(t *testing.T) (core.RunResult, []core.ToolResultMessage) {
	t.Helper()
	handlerOK := core.Tool{
		Name: "handler_ok", Description: "ok", InputSchema: schema.Object(),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"x":1}`), nil
		},
	}
	handlerErr := core.Tool{
		Name: "handler_err", Description: "err", InputSchema: schema.Object(),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			return nil, fmt.Errorf("something broke")
		},
	}
	panicTool := core.Tool{
		Name: "panicker", Description: "panics", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			panic("boom")
		},
	}
	strictTool := core.Tool{
		Name: "strict", Description: "strict",
		InputSchema: schema.Object(schema.Prop("required_field", schema.String())),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}

	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse,
			mdToolUse(t, "c_a", "handler_ok", `{}`),
			mdToolUse(t, "c_b", "handler_err", `{}`),
			mdToolUse(t, "c_c", "panicker", `{}`),
			mdToolUse(t, "c_d", "nonexistent", `{}`),
			mdToolUse(t, "c_e", "strict", `{}`),
		),
	}}
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, handlerOK, handlerErr, panicTool, strictTool)
	return mdCollectToolResultEvents(t, a, s, "go")
}

// TestHandlerStyleAndPanicAndUnknownAndInvalidNilMetadata_TS04_36 verifies
// that Handler-style tools, handler panics, unknown tools and invalid
// arguments produce messages with nil Metadata and unchanged content.
func TestHandlerStyleAndPanicAndUnknownAndInvalidNilMetadata_TS04_36(t *testing.T) {
	res, events := runTS0436(t)

	t.Run("all_nil_metadata", func(t *testing.T) {
		for _, msg := range events {
			if msg.Metadata != nil {
				t.Fatalf("ToolResultEvent %q: Metadata = %+v, want nil", msg.ToolUseID, msg.Metadata)
			}
		}
		for _, m := range res.Messages {
			if tr, ok := m.(core.ToolResultMessage); ok {
				if tr.Metadata != nil {
					t.Fatalf("RunResult %q: Metadata = %+v, want nil", tr.ToolUseID, tr.Metadata)
				}
			}
		}
		if len(events) != 5 {
			t.Fatalf("got %d ToolResultEvents, want 5", len(events))
		}
	})

	t.Run("content_shapes", func(t *testing.T) {
		msgA := mdFindToolResult(t, res.Messages, "c_a")
		if msgA.IsError {
			t.Fatal("handler_ok should not be an error")
		}
		if !strings.Contains(msgA.Content.Text(), `"ok":true`) {
			t.Fatalf("handler_ok content = %q, want to contain ok:true", msgA.Content.Text())
		}

		msgB := mdFindToolResult(t, res.Messages, "c_b")
		if !msgB.IsError {
			t.Fatal("handler_err should be an error")
		}
		if !strings.Contains(msgB.Content.Text(), "handler_error") {
			t.Fatalf("handler_err content = %q, want handler_error", msgB.Content.Text())
		}

		msgC := mdFindToolResult(t, res.Messages, "c_c")
		if !msgC.IsError {
			t.Fatal("panicker should be an error")
		}
		if !strings.Contains(msgC.Content.Text(), "panic") {
			t.Fatalf("panicker content = %q, want panic", msgC.Content.Text())
		}

		msgD := mdFindToolResult(t, res.Messages, "c_d")
		if !msgD.IsError {
			t.Fatal("unknown tool should be an error")
		}
		if !strings.Contains(msgD.Content.Text(), "unknown_tool") {
			t.Fatalf("unknown tool content = %q, want unknown_tool", msgD.Content.Text())
		}

		msgE := mdFindToolResult(t, res.Messages, "c_e")
		if !msgE.IsError {
			t.Fatal("invalid args should be an error")
		}
		if !strings.Contains(msgE.Content.Text(), "invalid_arguments") {
			t.Fatalf("invalid args content = %q, want invalid_arguments", msgE.Content.Text())
		}
	})
}

// ---------------------------------------------------------------- TS-04-37

// ts0437MetaTool returns a tool that records whether it ran and returns
// non-empty metadata.
func ts0437MetaTool(ran *bool) core.Tool {
	return core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			*ran = true
			ec := 0
			return core.ToolResult{
				OK:       true,
				Data:     map[string]any{"ok": true},
				Metadata: &core.ToolMetadata{ExitCode: &ec, Outcome: "ok"},
			}
		},
	}
}

// TestBlockedNilMetadata_TS04_37 verifies that a BeforeToolCall block
// produces a message with nil Metadata.
func TestBlockedNilMetadata_TS04_37(t *testing.T) {
	var ran bool
	metaTool := ts0437MetaTool(&ran)

	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse, mdToolUse(t, "c1", "probe", `{}`)),
	}}
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = func(_ context.Context, _ core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Block: true, Reason: "no"}
		}
	}, metaTool)
	res, events := mdCollectToolResultEvents(t, a, s, "go")
	if ran {
		t.Fatal("handler should not have run")
	}
	if len(events) == 0 {
		t.Fatal("no ToolResultEvent")
	}
	if events[0].Metadata != nil {
		t.Fatalf("blocked call Metadata = %+v, want nil", events[0].Metadata)
	}
	msg := mdFindToolResult(t, res.Messages, "c1")
	if !msg.IsError {
		t.Fatal("blocked call should be an error")
	}
	if !strings.Contains(msg.Content.Text(), "blocked_by_policy") {
		t.Fatalf("blocked content = %q, want blocked_by_policy", msg.Content.Text())
	}
}

// TestAbortedNilMetadata_TS04_37 verifies that a batch abort produces
// a message with nil Metadata.
func TestAbortedNilMetadata_TS04_37(t *testing.T) {
	var ran bool
	metaTool := ts0437MetaTool(&ran)

	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse, mdToolUse(t, "c1", "probe", `{}`)),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, metaTool)
	// Cancelled before the run: the scripted provider ignores ctx, so the
	// batch is reached and aborted.
	cancel()
	st, err := a.Stream(ctx, "go")
	if err != nil {
		t.Fatal(err)
	}
	var abortEvents []core.ToolResultMessage
	for e := range st.Events() {
		if tre, ok := e.(core.ToolResultEvent); ok {
			abortEvents = append(abortEvents, tre.Message)
		}
	}
	if ran {
		t.Fatal("handler should not have run on abort")
	}
	if len(abortEvents) == 0 {
		t.Fatal("no ToolResultEvent on abort")
	}
	if abortEvents[0].Metadata != nil {
		t.Fatalf("aborted call Metadata = %+v, want nil", abortEvents[0].Metadata)
	}
	if !abortEvents[0].IsError {
		t.Fatal("aborted call should be an error")
	}
	if !strings.Contains(abortEvents[0].Content.Text(), "aborted") {
		t.Fatalf("aborted content = %q, want aborted", abortEvents[0].Content.Text())
	}
}

// ---------------------------------------------------------------- TS-04-38

// TestMaxTokensSynthesizedAndRepairSynthesizedNilMetadata_TS04_38 verifies
// that max_tokens-synthesized and RepairTranscript-synthesized results carry
// nil Metadata.
func TestMaxTokensSynthesizedAndRepairSynthesizedNilMetadata_TS04_38(t *testing.T) {
	// max_tokens: a StopReasonLength turn with tool calls.
	var probeRan bool
	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			probeRan = true
			ec := 0
			return core.ToolResult{OK: true, Metadata: &core.ToolMetadata{ExitCode: &ec}}
		},
	}

	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonLength, mdToolUse(t, "c1", "probe", `{}`)),
	}}
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, probeTool)

	_, events := mdCollectToolResultEvents(t, a, s, "go")
	if probeRan {
		t.Fatal("handler should not run on max_tokens turn")
	}
	if len(events) == 0 {
		t.Fatal("no ToolResultEvent for max_tokens")
	}
	msg := events[0]
	if msg.Metadata != nil {
		t.Fatalf("max_tokens synthesized Metadata = %+v, want nil", msg.Metadata)
	}
	if !msg.IsError {
		t.Fatal("max_tokens synthesized should be an error")
	}

	// RepairTranscript: a transcript with an unanswered tool call.
	tu, err := core.NewToolUse("call_1", "read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	damaged := core.Messages{
		core.AssistantMessage{
			Content:    core.Content{tu},
			StopReason: core.StopReasonToolUse,
			Model:      "md-test-model",
		},
		// No tool result for call_1.
	}
	target := anthropic.Target{Model: "md-test-model"}
	out, rep := anthropic.RepairTranscript(damaged, target)
	if rep.SyntheticResults != 1 {
		t.Fatalf("SyntheticResults = %d, want 1", rep.SyntheticResults)
	}
	// Find the synthetic result.
	for _, m := range out {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "call_1" {
			if tr.Metadata != nil {
				t.Fatalf("RepairTranscript synthetic Metadata = %+v, want nil", tr.Metadata)
			}
			if tr.Content.Text() != anthropic.SyntheticResultText {
				t.Fatalf("synthetic text = %q, want %q", tr.Content.Text(), anthropic.SyntheticResultText)
			}
			return
		}
	}
	t.Fatal("no synthetic result found in RepairTranscript output")
}

// ---------------------------------------------------------------- TS-04-40

// TestMetadataReachesEveryObserver_TS04_40 verifies that a failing execute
// call's metadata reaches ToolResultEvent, TurnEndEvent, the transcript and
// RunResult.
func TestMetadataReachesEveryObserver_TS04_40(t *testing.T) {
	ec := 2
	md := &core.ToolMetadata{ExitCode: &ec, Outcome: "exit", TotalBytes: 5, DurationMS: 12}

	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       false,
				Error:    "command_exit",
				Text:     "boom\n[exit 2]",
				Metadata: md,
			}
		},
	}

	var (
		toolResultEvt  *core.ToolResultMessage
		turnEndResults []core.ToolResultMessage
	)

	s := &mdScripted{turns: []core.AssistantMessage{
		mdAssistantWithTools(core.StopReasonToolUse, mdToolUse(t, "c1", "probe", `{}`)),
	}}
	a := mdNewTestAgent(t, s, func(c *agentkit.Config) {
		c.Guard = guard.AllowAll
	}, probeTool)

	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for e := range st.Events() {
		if tre, ok := e.(core.ToolResultEvent); ok {
			msg := tre.Message
			toolResultEvt = &msg
		}
		if te, ok := e.(core.TurnEndEvent); ok && len(te.ToolResults) > 0 {
			turnEndResults = te.ToolResults
		}
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}

	// 1. ToolResultEvent
	if toolResultEvt == nil {
		t.Fatal("no ToolResultEvent captured")
	}
	checkMD := func(label string, m *core.ToolMetadata) {
		t.Helper()
		if m == nil {
			t.Fatalf("%s: Metadata is nil", label)
		}
		if m.ExitCode == nil || *m.ExitCode != 2 {
			t.Fatalf("%s: ExitCode = %v, want 2", label, m.ExitCode)
		}
		if m.Outcome != "exit" {
			t.Fatalf("%s: Outcome = %q, want %q", label, m.Outcome, "exit")
		}
	}
	checkMD("ToolResultEvent", toolResultEvt.Metadata)

	// 2. TurnEndEvent
	if len(turnEndResults) == 0 {
		t.Fatal("no TurnEndEvent results")
	}
	checkMD("TurnEndEvent", turnEndResults[0].Metadata)

	// 3. The transcript
	for _, m := range a.Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			checkMD("History", tr.Metadata)
		}
	}

	// 4. RunResult.Messages
	rmsg := mdFindToolResult(t, res.Messages, "c1")
	checkMD("RunResult", rmsg.Metadata)
}
