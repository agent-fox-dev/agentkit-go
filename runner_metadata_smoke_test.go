package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/guard"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/stop"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-04-61 (smoke): A failing execute call's metadata reaches observers and
// the session log, never the provider request, and survives resume.
func TestTS_04_61_ExecuteMetadataReachesObserversAndSessionLogNeverProvider(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh and unix shell commands")
	}
	if _, _, err := tools.ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	// ---- httptest server standing in for the Anthropic Messages API ----
	//
	// Turn 1: returns a tool_use calling execute with {"command":"printf boom; exit 2"}
	// Turn 2: returns end_turn text
	var (
		requestBodies [][]byte
		requestMu     sync.Mutex
		turnCount     int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestMu.Lock()
		requestBodies = append(requestBodies, body)
		turn := turnCount
		turnCount++
		requestMu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		if turn == 0 {
			// Turn 1: tool_use calling execute
			fmt.Fprint(w, sseResponse(
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"execute","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"printf boom; exit 2\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`,
				`{"type":"message_stop"}`,
			))
		} else {
			// Turn 2: end_turn text
			fmt.Fprint(w, sseResponse(
				`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
				`{"type":"message_stop"}`,
			))
		}
	}))
	defer srv.Close()

	// ---- Set up the agent ----
	workspace := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")

	ws, err := tools.NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	allTools, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatalf("tools.All: %v", err)
	}

	model := &core.Model{
		ID: "claude-test", Name: "Claude Test", API: anthropic.API, Provider: "anthropic",
		ContextWindow: 200000, MaxTokens: 4096,
	}

	store, _ := openTestSession(t, sessionPath)

	// Observers: AfterToolCall hook, event listener, StopPolicy.
	type afterRecord struct {
		ToolResult core.ToolResult
		Result     *core.ToolResultMessage
	}
	var (
		afterMu   sync.Mutex
		afterRecs []afterRecord
	)

	var (
		toolResultEvents []core.ToolResultMessage
		turnEndResults   []core.ToolResultMessage
		stopResults      []core.ToolResultMessage
		eventMu          sync.Mutex
	)

	cfg := core.AgentConfig{
		Model: model,
		Providers: core.ProviderRegistry{
			anthropic.API: anthropic.Provider(anthropic.Options{
				BaseURL: srv.URL,
				Getenv: func(k string) string {
					if k == "ANTHROPIC_API_KEY" {
						return "sk-ant-test-key-smoke"
					}
					return ""
				},
			}),
		},
		StopPolicy: func(sc core.StopContext) bool {
			if len(sc.ToolResults) > 0 {
				eventMu.Lock()
				stopResults = append(stopResults, sc.ToolResults...)
				eventMu.Unlock()
			}
			return false
		},
		BeforeToolCall: guard.AllowAll,
		AfterToolCall: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			afterMu.Lock()
			afterRecs = append(afterRecs, afterRecord{
				ToolResult: in.ToolResult,
				Result:     in.Result,
			})
			afterMu.Unlock()
			return core.AfterToolCallDecision{}
		},
		SessionStore: store,
	}

	a, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	for _, tool := range allTools {
		if err := a.RegisterTool(tool); err != nil {
			t.Fatalf("RegisterTool(%s): %v", tool.Name, err)
		}
	}

	// ---- Run the agent ----
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolResultEvent:
			eventMu.Lock()
			toolResultEvents = append(toolResultEvents, ev.Message)
			eventMu.Unlock()
		case core.TurnEndEvent:
			eventMu.Lock()
			turnEndResults = append(turnEndResults, ev.ToolResults...)
			eventMu.Unlock()
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatalf("RunResult: %v", err)
	}

	// ---- AfterToolCall assertions ----
	afterMu.Lock()
	if len(afterRecs) == 0 {
		t.Fatal("no AfterToolCall records")
	}
	rec := afterRecs[0]
	afterMu.Unlock()

	// in.ToolResult.Metadata
	if rec.ToolResult.Metadata == nil {
		t.Fatal("AfterToolCall: ToolResult.Metadata is nil")
	}
	if rec.ToolResult.Metadata.ExitCode == nil || *rec.ToolResult.Metadata.ExitCode != 2 {
		t.Fatalf("AfterToolCall: ToolResult.Metadata.ExitCode = %v, want 2", rec.ToolResult.Metadata.ExitCode)
	}
	if rec.ToolResult.Metadata.Outcome != "exit" {
		t.Fatalf("AfterToolCall: ToolResult.Metadata.Outcome = %q, want %q", rec.ToolResult.Metadata.Outcome, "exit")
	}

	// in.Result.Metadata
	if rec.Result == nil {
		t.Fatal("AfterToolCall: Result is nil")
	}
	if rec.Result.Metadata == nil {
		t.Fatal("AfterToolCall: Result.Metadata is nil")
	}
	if rec.Result.Metadata.ExitCode == nil || *rec.Result.Metadata.ExitCode != 2 {
		t.Fatalf("AfterToolCall: Result.Metadata.ExitCode = %v, want 2", rec.Result.Metadata.ExitCode)
	}
	if rec.Result.Metadata.Outcome != "exit" {
		t.Fatalf("AfterToolCall: Result.Metadata.Outcome = %q, want %q", rec.Result.Metadata.Outcome, "exit")
	}

	// ---- ToolResultEvent assertions ----
	eventMu.Lock()
	if len(toolResultEvents) == 0 {
		t.Fatal("no ToolResultEvent")
	}
	tre := toolResultEvents[0]
	eventMu.Unlock()
	if tre.Metadata == nil {
		t.Fatal("ToolResultEvent: Metadata is nil")
	}
	if tre.Metadata.ExitCode == nil || *tre.Metadata.ExitCode != 2 {
		t.Fatalf("ToolResultEvent: Metadata.ExitCode = %v, want 2", tre.Metadata.ExitCode)
	}
	if tre.Metadata.Outcome != "exit" {
		t.Fatalf("ToolResultEvent: Metadata.Outcome = %q, want %q", tre.Metadata.Outcome, "exit")
	}

	// ---- TurnEndEvent.ToolResults assertions ----
	eventMu.Lock()
	if len(turnEndResults) == 0 {
		t.Fatal("no TurnEndEvent tool results")
	}
	ter := turnEndResults[0]
	eventMu.Unlock()
	if ter.Metadata == nil {
		t.Fatal("TurnEndEvent: Metadata is nil")
	}
	if ter.Metadata.ExitCode == nil || *ter.Metadata.ExitCode != 2 {
		t.Fatalf("TurnEndEvent: Metadata.ExitCode = %v, want 2", ter.Metadata.ExitCode)
	}

	// ---- StopContext.ToolResults assertions ----
	eventMu.Lock()
	if len(stopResults) == 0 {
		t.Fatal("no StopContext tool results")
	}
	sr := stopResults[0]
	eventMu.Unlock()
	if sr.Metadata == nil {
		t.Fatal("StopContext: Metadata is nil")
	}
	if sr.Metadata.ExitCode == nil || *sr.Metadata.ExitCode != 2 {
		t.Fatalf("StopContext: Metadata.ExitCode = %v, want 2", sr.Metadata.ExitCode)
	}

	// ---- Session log assertions ----
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	logData, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("reading session log: %v", err)
	}
	logStr := string(logData)

	// Find the tool_result line.
	var toolResultLine string
	for _, line := range strings.Split(logStr, "\n") {
		if strings.Contains(line, `"tool_result"`) && strings.Contains(line, `"metadata"`) {
			toolResultLine = line
			break
		}
	}
	if toolResultLine == "" {
		t.Fatal("session log has no tool_result line with metadata")
	}

	// The metadata object should contain exit_code:2 and outcome:exit.
	if !strings.Contains(toolResultLine, `"exit_code":2`) {
		t.Fatalf("session log tool_result line missing \"exit_code\":2:\n%s", toolResultLine)
	}
	if !strings.Contains(toolResultLine, `"outcome":"exit"`) {
		t.Fatalf("session log tool_result line missing \"outcome\":\"exit\":\n%s", toolResultLine)
	}

	// metadata should appear before the message's own timestamp.
	// The entry has an outer "timestamp" and the message object has an inner
	// one. We need to check that "metadata" appears before the LAST
	// "timestamp" (which is the message's timestamp inside the message object).
	metaIdx := strings.Index(toolResultLine, `"metadata"`)
	lastTsIdx := strings.LastIndex(toolResultLine, `"timestamp"`)
	if metaIdx < 0 || lastTsIdx < 0 || metaIdx >= lastTsIdx {
		t.Fatalf("metadata should appear before the message timestamp in the session log line")
	}

	// ---- Second request body assertions ----
	// The second request body (turn 2) carries the tool result text but must
	// NOT contain metadata, exit_code, duration_ms or outcome as keys.
	requestMu.Lock()
	if len(requestBodies) < 2 {
		t.Fatalf("expected at least 2 request bodies, got %d", len(requestBodies))
	}
	secondBody := string(requestBodies[1])
	requestMu.Unlock()

	// The tool result text should be present.
	if !strings.Contains(secondBody, "boom") {
		t.Fatalf("second request body does not contain 'boom':\n%s", secondBody)
	}
	if !strings.Contains(secondBody, "[exit 2]") {
		t.Fatalf("second request body does not contain '[exit 2]':\n%s", secondBody)
	}

	// Metadata keys must NOT appear in the request body.
	for _, forbidden := range []string{`"metadata"`, `"exit_code"`, `"duration_ms"`, `"outcome"`} {
		// Parse the body as JSON to check the messages array specifically.
		var reqBody map[string]json.RawMessage
		if err := json.Unmarshal(requestBodies[1], &reqBody); err != nil {
			t.Fatalf("parsing second request body: %v", err)
		}
		// Check the messages array for forbidden keys.
		messagesRaw := reqBody["messages"]
		messagesStr := string(messagesRaw)
		if strings.Contains(messagesStr, forbidden) {
			t.Fatalf("second request body messages contain %s (metadata should never reach the provider):\n%s",
				forbidden, messagesStr)
		}
	}

	// ---- Resume assertions ----
	// Reopen the same session file to resume.
	store2, resume := openTestSession(t, sessionPath)
	defer store2.Close()

	cfg2 := core.AgentConfig{
		Model:      model,
		StopPolicy: stop.AfterTurns(5),
		Providers: core.ProviderRegistry{anthropic.API: anthropic.Provider(anthropic.Options{
			BaseURL: srv.URL,
			Getenv: func(k string) string {
				if k == "ANTHROPIC_API_KEY" {
					return "sk-ant-test-key-smoke"
				}
				return ""
			},
		})},
		SessionStore: store2,
	}
	a2, err := NewAgentFromSession(cfg2, resume,
		func(provider string, api core.API, modelID string) (*core.Model, error) {
			return model, nil
		})
	if err != nil {
		t.Fatalf("NewAgentFromSession: %v", err)
	}

	// Check the resumed agent's history for the tool result metadata.
	var resumedMD *core.ToolMetadata
	for _, m := range a2.History().Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolName == "execute" {
			resumedMD = tr.Metadata
		}
	}
	if resumedMD == nil {
		t.Fatal("resumed agent: tool result has nil Metadata")
	}
	if resumedMD.ExitCode == nil || *resumedMD.ExitCode != 2 {
		t.Fatalf("resumed agent: ExitCode = %v, want 2", resumedMD.ExitCode)
	}
	if resumedMD.Outcome != "exit" {
		t.Fatalf("resumed agent: Outcome = %q, want %q", resumedMD.Outcome, "exit")
	}
}

// sseResponse renders a sequence of SSE data lines.
func sseResponse(events ...string) string {
	var b bytes.Buffer
	for _, e := range events {
		// Determine the event type from the JSON.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(e), &obj); err == nil {
			if t, ok := obj["type"]; ok {
				var typ string
				json.Unmarshal(t, &typ)
				fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", typ, e)
				continue
			}
		}
		fmt.Fprintf(&b, "data: %s\n\n", e)
	}
	return b.String()
}
