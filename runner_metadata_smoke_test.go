package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// ts0461Server creates an httptest.Server standing in for the Anthropic
// Messages API. Turn 1 returns a tool_use calling execute; turn 2 returns
// end_turn text. It records every request body.
func ts0461Server(t *testing.T) (*httptest.Server, *[][]byte, *sync.Mutex) {
	t.Helper()
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
			fmt.Fprint(w, sseResponse(
				`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"execute","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"printf boom; exit 2\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`,
				`{"type":"message_stop"}`,
			))
		} else {
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
	t.Cleanup(srv.Close)
	return srv, &requestBodies, &requestMu
}

// ts0461AfterRecord holds the AfterToolCall capture.
type ts0461AfterRecord struct {
	ToolResult core.ToolResult
	Result     *core.ToolResultMessage
}

// ts0461Tools builds the real tool set over a temp workspace.
func ts0461Tools(t *testing.T) []core.Tool {
	t.Helper()
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	allTools, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatalf("tools.All: %v", err)
	}
	return allTools
}

// ts0461Drain streams one run of a and returns the tool results its
// ToolResultEvents and TurnEndEvents carried.
func ts0461Drain(t *testing.T, a *Agent) (toolResultEvents, turnEndResults []core.ToolResultMessage) {
	t.Helper()
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolResultEvent:
			toolResultEvents = append(toolResultEvents, ev.Message)
		case core.TurnEndEvent:
			turnEndResults = append(turnEndResults, ev.ToolResults...)
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatalf("RunResult: %v", err)
	}
	return toolResultEvents, turnEndResults
}

// ts0461RunAgent sets up and runs the agent, returning all captured data.
func ts0461RunAgent(t *testing.T, srv *httptest.Server, requestBodies *[][]byte, requestMu *sync.Mutex) (
	afterRecs []ts0461AfterRecord,
	toolResultEvents []core.ToolResultMessage,
	turnEndResults []core.ToolResultMessage,
	stopResults []core.ToolResultMessage,
	model *core.Model,
) {
	t.Helper()

	model = &core.Model{
		ID: "claude-test", Name: "Claude Test", API: anthropic.API, Provider: "anthropic",
		ContextWindow: 200000, MaxTokens: 4096,
	}

	var (
		afterMu sync.Mutex
		eventMu sync.Mutex
	)

	getenv := func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-ant-test-key-smoke"
		}
		return ""
	}

	cfg := core.AgentConfig{
		Model: model,
		Providers: core.ProviderRegistry{
			anthropic.API: anthropic.Provider(anthropic.Options{
				BaseURL: srv.URL,
				Getenv:  getenv,
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
			afterRecs = append(afterRecs, ts0461AfterRecord{
				ToolResult: in.ToolResult,
				Result:     in.Result,
			})
			afterMu.Unlock()
			return core.AfterToolCallDecision{}
		},
	}

	a, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	for _, tool := range ts0461Tools(t) {
		if err := a.RegisterTool(tool); err != nil {
			t.Fatalf("RegisterTool(%s): %v", tool.Name, err)
		}
	}

	toolResultEvents, turnEndResults = ts0461Drain(t, a)

	return afterRecs, toolResultEvents, turnEndResults, stopResults, model
}

// TS-04-61 (smoke): A failing execute call's metadata reaches observers and
// never the provider request.
// ts0461Data holds the shared state from the TS-04-61 agent run.
type ts0461Data struct {
	afterRecs        []ts0461AfterRecord
	toolResultEvents []core.ToolResultMessage
	turnEndResults   []core.ToolResultMessage
	stopResults      []core.ToolResultMessage
	model            *core.Model
	srv              *httptest.Server
	requestBodies    *[][]byte
	requestMu        *sync.Mutex
}

func ts0461Setup(t *testing.T) ts0461Data {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh and unix shell commands")
	}
	if _, _, err := tools.ResolveShell(); err != nil {
		t.Skip("no shell available")
	}
	srv, requestBodies, requestMu := ts0461Server(t)
	afterRecs, toolResultEvents, turnEndResults, stopResults, model :=
		ts0461RunAgent(t, srv, requestBodies, requestMu)
	return ts0461Data{
		afterRecs: afterRecs, toolResultEvents: toolResultEvents,
		turnEndResults: turnEndResults, stopResults: stopResults,
		model: model,
		srv:   srv, requestBodies: requestBodies, requestMu: requestMu,
	}
}

func TestTS_04_61_AfterToolCall(t *testing.T) {
	d := ts0461Setup(t)
	if len(d.afterRecs) == 0 {
		t.Fatal("no AfterToolCall records")
	}
	rec := d.afterRecs[0]
	if rec.ToolResult.Metadata == nil {
		t.Fatal("ToolResult.Metadata is nil")
	}
	if rec.ToolResult.Metadata.ExitCode == nil || *rec.ToolResult.Metadata.ExitCode != 2 {
		t.Fatalf("ExitCode = %v, want 2", rec.ToolResult.Metadata.ExitCode)
	}
	if rec.ToolResult.Metadata.Outcome != "exit" {
		t.Fatalf("Outcome = %q, want exit", rec.ToolResult.Metadata.Outcome)
	}
	if rec.Result == nil || rec.Result.Metadata == nil {
		t.Fatal("Result.Metadata is nil")
	}
	if *rec.Result.Metadata.ExitCode != 2 {
		t.Fatalf("Result.Metadata.ExitCode = %d, want 2", *rec.Result.Metadata.ExitCode)
	}
}

func TestTS_04_61_Events(t *testing.T) {
	d := ts0461Setup(t)
	if len(d.toolResultEvents) == 0 {
		t.Fatal("no ToolResultEvent")
	}
	tre := d.toolResultEvents[0]
	if tre.Metadata == nil || tre.Metadata.ExitCode == nil || *tre.Metadata.ExitCode != 2 {
		t.Fatalf("ToolResultEvent: ExitCode = %v, want 2", tre.Metadata)
	}
	if len(d.turnEndResults) == 0 || d.turnEndResults[0].Metadata == nil || *d.turnEndResults[0].Metadata.ExitCode != 2 {
		t.Fatal("TurnEndEvent: ExitCode mismatch")
	}
	if len(d.stopResults) == 0 || d.stopResults[0].Metadata == nil || *d.stopResults[0].Metadata.ExitCode != 2 {
		t.Fatal("StopContext: ExitCode mismatch")
	}
}

func TestTS_04_61_RequestBodyNoMetadata(t *testing.T) {
	d := ts0461Setup(t)
	d.requestMu.Lock()
	if len(*d.requestBodies) < 2 {
		t.Fatalf("expected >= 2 request bodies, got %d", len(*d.requestBodies))
	}
	secondBody := (*d.requestBodies)[1]
	d.requestMu.Unlock()

	if !strings.Contains(string(secondBody), "boom") || !strings.Contains(string(secondBody), "[exit 2]") {
		t.Fatal("second request body missing tool result text")
	}
	var reqBody map[string]json.RawMessage
	if err := json.Unmarshal(secondBody, &reqBody); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	for _, s := range []string{`"metadata"`, `"exit_code"`, `"duration_ms"`, `"outcome"`} {
		if strings.Contains(string(reqBody["messages"]), s) {
			t.Fatalf("request messages contain %s", s)
		}
	}
}

// sseResponse renders a sequence of SSE data lines.
func sseResponse(events ...string) string {
	var b bytes.Buffer
	for _, e := range events {
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
