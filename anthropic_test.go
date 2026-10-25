package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// canonicalTools are the golden request's tools: find_files with out as its
// output schema, and a terminating tool that reaches a child. Neither the
// output schema nor the reach may reach the wire.
func canonicalTools(out *schema.Schema) []core.Tool {
	noop := func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }
	return []core.Tool{
		{Name: "find_files", Description: "Find files by glob pattern.", OutputSchema: out, Execute: noop,
			InputSchema: schema.Object(schema.Prop("pattern", schema.String("Glob pattern")), schema.Opt("limit", schema.Int("Maximum results")))},
		{Name: "submit_answer", Description: "Submit the final answer.", Terminating: true, Execute: noop,
			InputSchema:    schema.Object(schema.Prop("answer", schema.String("The answer"))),
			OutputSchema:   schema.Object(schema.Prop("accepted", schema.Bool())),
			ReachableTools: []core.Tool{{Name: "reachable_child", InputSchema: schema.Object(), Execute: noop}}},
	}
}

// canonicalRequest exercises everything the wire encodes: two system blocks,
// a prefix, a replayed tool call whose input keeps the model's own spacing,
// its result with metadata that must not be sent, two tools and an effort on
// an adaptive model.
func canonicalRequest(out *schema.Schema) core.Request {
	const model = "claude-opus-5-5"
	two, maxTok := 2, 1024
	text := func(s string) core.Content { return core.Content{core.TextBlock{Text: s}} }
	return core.Request{
		System: []core.ContentBlock{core.TextBlock{Text: "You are a release engineer."}, core.TextBlock{Text: "Answer from the repository, not from memory."}},
		Prefix: core.Messages{core.UserMessage{Content: text("Project: agentkit-go. Branch: main.")},
			core.AssistantMessage{Content: text("Understood."), StopReason: core.StopReasonStop, Model: model}},
		Messages: core.Messages{
			core.UserMessage{Content: text("Which Go files changed?")},
			core.AssistantMessage{Model: model, StopReason: core.StopReasonToolUse, Content: core.Content{core.TextBlock{Text: "Let me look."},
				core.ToolUseBlock{ID: "call_1", Name: "find_files", Input: json.RawMessage(`{"pattern": "**/*.go",  "limit": 5}`)}}},
			core.ToolResultMessage{ToolUseID: "call_1", ToolName: "find_files", Content: text(`{"ok":true,"data":{"entries":["main.go"]}}`),
				Metadata: &core.ToolMetadata{Truncated: true, TruncatedBy: "bytes", TotalBytes: 123, SpillPath: "/tmp/agentkit-golden-sentinel.log",
					DurationMS: 42, ExitCode: &two, Outcome: "exit", LineEnding: "lf"}},
		},
		Tools: core.ToolWires(canonicalTools(out)), ToolChoice: core.ToolChoiceAuto, MaxTokens: &maxTok, Effort: core.EffortHigh,
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sent is the body the Anthropic provider sends for req.
func sent(t *testing.T, req core.Request) []byte {
	t.Helper()
	m, _ := catalog.Lookup("claude-opus-5-5")
	var body []byte
	p := anthropic.Provider(m, anthropic.Options{BaseURL: "https://example.invalid", Getenv: noEnv,
		Env: map[string]string{"ANTHROPIC_API_KEY": "test-key"}, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			body, _ = io.ReadAll(r.Body)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})})
	core.EventStreamOf(p.Stream(context.Background(), req)).Result()
	return body
}

// TestCanonicalRequestEncodesToTheGolden pins the Anthropic request body byte
// for byte: what the encoder builds and what the provider sends, whatever
// output schema a tool declares. Run with -update and review the diff.
func TestCanonicalRequestEncodesToTheGolden(t *testing.T) {
	const golden = "testdata/golden/request_anthropic.json"
	m, _ := catalog.Lookup("claude-opus-5-5")
	built, err := anthropic.BuildRequestJSON(canonicalRequest(nil), m)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, golden, built)
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	outs := []*schema.Schema{nil, schema.Array(schema.OneOf(schema.Enum("out-sentinel", "a"), schema.Object(schema.Opt("out_sentinel", schema.Number()))))}
	for _, tl := range all {
		if tl.Name == "find_files" {
			outs = append(outs, tl.OutputSchema)
		}
	}
	for i, out := range outs {
		if body := sent(t, canonicalRequest(out)); string(body) != string(built) {
			t.Errorf("output schema %d: the provider sent\n%s\nwant the golden", i, body)
		}
	}
	if len(outs) != 3 || strings.Contains(string(built), "sentinel") || strings.Contains(string(built), "reachable_child") {
		t.Errorf("the golden carries what must not be sent, or find_files has no output schema")
	}
}

// TestAnthropicProvider_SerializesToolSchemasOncePerToolList checks the
// provider an agent builds caches its tool definitions without being asked.
func TestAnthropicProvider_SerializesToolSchemasOncePerToolList(t *testing.T) {
	var marshalled []int
	p := anthropic.Provider(core.Model{ID: "claude-x", MaxOutputTokens: 4096}, anthropic.Options{Getenv: func(string) string { return "k" },
		OnToolPrefixSync: func(r provider.SyncReport) { marshalled = append(marshalled, r.Marshalled) },
		Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		})})
	req := core.Request{Tools: core.ToolWires(canonicalTools(nil))}
	for range 2 {
		core.EventStreamOf(p.Stream(context.Background(), req)).Result()
	}
	if len(marshalled) != 2 || marshalled[0] != 2 || marshalled[1] != 0 {
		t.Fatalf("schemas serialized per request = %v, want [2 0]", marshalled)
	}
}

func TestAnthropicProvider_EmitsTheFirstTokenBeforeTheResponseEnds(t *testing.T) {
	pr, pw := io.Pipe()
	sawFirst := make(chan struct{})
	event := func(typ, data string) { fmt.Fprintf(pw, "event: %s\ndata: %s\n\n", typ, data) }
	go func() {
		event("message_start", `{"type":"message_start","message":{"id":"m","model":"claude-x","usage":{"input_tokens":1}}}`)
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`)
		select {
		case <-sawFirst:
		case <-time.After(5 * time.Second):
			pw.CloseWithError(errors.New("the first delta was never seen while the body was open"))
			return
		}
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" and rest"}}`)
		event("content_block_stop", `{"type":"content_block_stop","index":0}`)
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
		event("message_stop", `{"type":"message_stop"}`)
		_ = pw.Close()
	}()
	p := anthropic.Provider(core.Model{ID: "claude-x", MaxOutputTokens: 64}, anthropic.Options{Getenv: func(string) string { return "k" },
		Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: pr}, nil
		})})
	var text string
	for e := range core.EventStreamOf(p.Stream(context.Background(), core.Request{})).Events() {
		if d, ok := e.(core.TextDeltaEvent); ok {
			if text == "" {
				close(sawFirst)
			}
			text += d.Delta
		}
	}
	if text != "first and rest" {
		t.Fatalf("the stream delivered %q, want the first token while the body was open and then the rest", text)
	}
}
