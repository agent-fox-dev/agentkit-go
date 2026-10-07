package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/session"
	"github.com/agentfox/agentkit-go/stop"
)

// errBad is a sentinel error for the handler-error tool.
var errBad = errors.New("bad")

// ---------------------------------------------------------------- TS-04-42

// TestAfterToolCallReceivesHandlerToolResult_TS04_42 verifies that
// AfterToolCall receives the handler's ToolResult by value and a Result
// message already carrying its Metadata.
func TestAfterToolCallReceivesHandlerToolResult_TS04_42(t *testing.T) {
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

	type recorded struct {
		ToolResult core.ToolResult
		Result     *core.ToolResultMessage
	}
	var mu sync.Mutex
	recs := map[string]recorded{}

	var toolResultEvents []core.ToolResultMessage

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c_probe", "probe", `{}`),
			toolUse(t, "c_herr", "herr", `{}`),
		),
	}}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			mu.Lock()
			recs[in.ToolName] = recorded{ToolResult: in.ToolResult, Result: in.Result}
			mu.Unlock()
			return core.AfterToolCallDecision{}
		}
	})
	if err := a.RegisterTool(probeTool); err != nil {
		t.Fatal(err)
	}
	if err := a.RegisterTool(handlerErrTool); err != nil {
		t.Fatal(err)
	}

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

	// ---- probe assertions ----
	pr, ok := recs["probe"]
	if !ok {
		t.Fatal("no AfterToolCall record for probe")
	}
	// ToolResult fields (by value, so Terminate is excluded from the comparison).
	if pr.ToolResult.OK != probeResult.OK {
		t.Fatalf("probe ToolResult.OK = %v, want %v", pr.ToolResult.OK, probeResult.OK)
	}
	if pr.ToolResult.Error != probeResult.Error {
		t.Fatalf("probe ToolResult.Error = %q, want %q", pr.ToolResult.Error, probeResult.Error)
	}
	if pr.ToolResult.Text != probeResult.Text {
		t.Fatalf("probe ToolResult.Text = %q, want %q", pr.ToolResult.Text, probeResult.Text)
	}
	if !reflect.DeepEqual(pr.ToolResult.Data, probeResult.Data) {
		t.Fatalf("probe ToolResult.Data = %v, want %v", pr.ToolResult.Data, probeResult.Data)
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

	// Result message carries Metadata.
	if pr.Result == nil {
		t.Fatal("probe Result is nil")
	}
	if pr.Result.Metadata == nil {
		t.Fatal("probe Result.Metadata is nil")
	}
	if !reflect.DeepEqual(*pr.Result.Metadata, *probeMD) {
		t.Fatalf("probe Result.Metadata = %+v, want %+v", *pr.Result.Metadata, *probeMD)
	}

	// Result is the message later emitted in ToolResultEvent.
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
	// The emitted message should reflect the same Result (after any hook edits
	// and image normalization, which are identity here).
	if !reflect.DeepEqual(probeEvt.Content, pr.Result.Content) {
		t.Fatalf("ToolResultEvent content differs from Result")
	}

	// ---- handler-error assertions ----
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
}

// ---------------------------------------------------------------- TS-04-43

// TestMetadataEditedThroughResultPropagates_TS04_43 verifies that metadata
// edited through in.Result is what is emitted, persisted, seen by the stop
// policy and kept in history.
func TestMetadataEditedThroughResultPropagates_TS04_43(t *testing.T) {
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

	var stopResults []core.ToolResultMessage
	var toolResultEvents []core.ToolResultMessage

	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, _ := openTestSession(t, path)

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "probe", `{}`)),
	}}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.SessionStore = store
		c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			// Edit metadata through in.Result.
			in.Result.Metadata.Outcome = "edited"
			*in.Result.Metadata.ExitCode = 99
			return core.AfterToolCallDecision{}
		}
		c.StopPolicy = func(sc core.StopContext) bool {
			if len(sc.ToolResults) > 0 {
				stopResults = sc.ToolResults
			}
			return false
		}
	})
	if err := a.RegisterTool(probeTool); err != nil {
		t.Fatal(err)
	}

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

	checkEdited := func(label string, md *core.ToolMetadata) {
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

	// 1. ToolResultEvent
	if len(toolResultEvents) == 0 {
		t.Fatal("no ToolResultEvent")
	}
	checkEdited("ToolResultEvent", toolResultEvents[0].Metadata)

	// 2. StopContext
	if len(stopResults) == 0 {
		t.Fatal("no StopContext results")
	}
	checkEdited("StopContext", stopResults[0].Metadata)

	// 3. History
	for _, m := range a.History().Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			checkEdited("History", tr.Metadata)
		}
	}

	// 4. Persisted: close the store, reopen and decode.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	loaded, err := session.Load(path)
	if err != nil {
		t.Fatalf("session.Load: %v", err)
	}
	entries := loaded.Entries()
	var found bool
	for _, e := range entries {
		if e.Type != core.EntryMessage || e.Message == nil {
			continue
		}
		if tr, ok := e.Message.Message.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			checkEdited("Persisted", tr.Metadata)
			found = true
		}
	}
	if !found {
		t.Fatal("no persisted tool_result entry found")
	}
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
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			// Snapshot the Result before mutating the ToolResult copy.
			snapshot = in.Result.Clone().(core.ToolResultMessage)
			// Mutate the ToolResult copy — these should have no effect.
			in.ToolResult.Text = "y"
			in.ToolResult.Data["k"] = float64(2)
			in.ToolResult.Metadata.Outcome = "hacked"
			*in.ToolResult.Metadata.ExitCode = 7
			return core.AfterToolCallDecision{}
		}
		c.StopPolicy = func(sc core.StopContext) bool {
			if len(sc.ToolResults) > 0 {
				stopResults = sc.ToolResults
			}
			return false
		}
	})
	if err := a.RegisterTool(probeTool); err != nil {
		t.Fatal(err)
	}

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

	// The snapshot was taken before the ToolResult mutations.
	// The emitted message should equal the snapshot.
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

	// ToolResultEvent
	if len(toolResultEvents) == 0 {
		t.Fatal("no ToolResultEvent")
	}
	checkUnchanged("ToolResultEvent", toolResultEvents[0])

	// History
	for _, m := range a.History().Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			checkUnchanged("History", tr)
		}
	}

	// StopContext
	if len(stopResults) == 0 {
		t.Fatal("no StopContext results")
	}
	checkUnchanged("StopContext", stopResults[0])

	// Verify the snapshot itself has the original values.
	if snapshot.Metadata.Outcome != "exit" {
		t.Fatalf("snapshot Outcome = %q, want %q", snapshot.Metadata.Outcome, "exit")
	}
	if snapshot.Metadata.ExitCode == nil || *snapshot.Metadata.ExitCode != 2 {
		t.Fatalf("snapshot ExitCode = %v, want 2", snapshot.Metadata.ExitCode)
	}
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
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			rec = recorded{ToolResult: in.ToolResult, Result: in.Result}
			return core.AfterToolCallDecision{}
		}
	})
	if err := a.RegisterTool(panicTool); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	// ToolResult should be the panic-converted result.
	if rec.ToolResult.OK {
		t.Fatal("ToolResult.OK should be false after panic")
	}
	if rec.ToolResult.Error != "panic" {
		t.Fatalf("ToolResult.Error = %q, want %q", rec.ToolResult.Error, "panic")
	}
	if rec.ToolResult.Metadata != nil {
		t.Fatalf("ToolResult.Metadata = %+v, want nil", rec.ToolResult.Metadata)
	}

	// Result message should have nil Metadata and IsError true.
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

// TestTerminationVotesAndImageNormalizationWithMetadata_TS04_46 verifies that
// termination votes and post-hook image normalization behave as before when
// tools carry metadata.
func TestTerminationVotesAndImageNormalizationWithMetadata_TS04_46(t *testing.T) {
	ec := 0
	md := &core.ToolMetadata{ExitCode: &ec, Outcome: "ok", DurationMS: 5}

	// A tool that returns Terminate=true and metadata.
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

	// (a) With AfterToolCall returning nil Terminate (no opinion), the tool's
	// vote should end the run after the batch.
	t.Run("nil_vote_ends_on_tool_vote", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "term", `{}`)),
			// If the run continues, this turn would fire.
			{Content: core.Content{core.TextBlock{Text: "should not reach"}}, StopReason: core.StopReasonStop},
		}}
		a := newTestAgent(t, s, func(c *core.AgentConfig) {
			c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				return core.AfterToolCallDecision{Terminate: nil}
			}
		})
		if err := a.RegisterTool(terminatingTool); err != nil {
			t.Fatal(err)
		}
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		// The run should have ended after 1 model turn (the tool use turn).
		if s.turnsRun() != 1 {
			t.Fatalf("turnsRun = %d, want 1 (tool's Terminate should end the run)", s.turnsRun())
		}
		// Metadata should be on the result.
		for _, m := range res.Messages {
			if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
				if tr.Metadata == nil {
					t.Fatal("Metadata is nil on terminated tool result")
				}
			}
		}
	})

	// (b) With AfterToolCall returning Terminate=false, the run should
	// continue to the next turn.
	t.Run("false_vote_continues", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "term", `{}`)),
			{Content: core.Content{core.TextBlock{Text: "continued"}}, StopReason: core.StopReasonStop},
		}}
		f := false
		a := newTestAgent(t, s, func(c *core.AgentConfig) {
			c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				return core.AfterToolCallDecision{Terminate: &f}
			}
		})
		if err := a.RegisterTool(terminatingTool); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if s.turnsRun() != 2 {
			t.Fatalf("turnsRun = %d, want 2 (hook's false vote should override tool's Terminate)", s.turnsRun())
		}
	})

	// (c) Verify that metadata survives image normalization: a hook that
	// appends an ImageBlock to in.Result.Content should have the image
	// normalized but the metadata unchanged.
	t.Run("image_normalization_preserves_metadata", func(t *testing.T) {
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
		a := newTestAgent(t, s, func(c *core.AgentConfig) {
			c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				// Append a small image block (not oversized, just to verify
				// normalization runs and metadata is preserved).
				in.Result.Content = append(in.Result.Content, core.ImageBlock{
					Data:     "aGVsbG8=", // base64 "hello"
					MimeType: "image/png",
				})
				return core.AfterToolCallDecision{}
			}
		})
		if err := a.RegisterTool(probeTool); err != nil {
			t.Fatal(err)
		}

		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}

		// Find the tool result in history.
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
	})

	// (d) Verify AND semantics with metadata-carrying tools.
	t.Run("batch_and_semantics_with_metadata", func(t *testing.T) {
		// Two tools in a batch: one terminates, one does not. AND semantics
		// means the batch does NOT terminate.
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
		a := newTestAgent(t, s, func(c *core.AgentConfig) {
			c.StopPolicy = stop.AfterTurns(10)
		})
		if err := a.RegisterTool(terminatingTool); err != nil {
			t.Fatal(err)
		}
		if err := a.RegisterTool(nonTermTool); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		// AND semantics: one false vote means the batch does not terminate.
		if s.turnsRun() != 2 {
			t.Fatalf("turnsRun = %d, want 2 (AND semantics: one non-terminating tool should prevent batch termination)", s.turnsRun())
		}
	})
}
