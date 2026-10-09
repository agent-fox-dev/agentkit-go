package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// TS-10-34 (smoke, 10-PATH-1): Vertex resolves offline from an Env, and the
// client it returns sends a Vertex-shaped request — inspected before it
// leaves the process, so no network and no Google credential is touched.
func TestSmokeVertexResolvesOffline_TS10_34(t *testing.T) {
	client, src, err := anthropic.Resolve(anthropic.MapEnv{
		"CLAUDE_CODE_USE_VERTEX":      "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "test-project",
		"CLOUD_ML_REGION":             "us-central1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if src != anthropic.SourceVertex {
		t.Fatalf("want SourceVertex, got %v", src)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	r := inspect(t, client)
	if r.URL.Host != "us-central1-aiplatform.googleapis.com" ||
		!strings.HasPrefix(r.URL.Path, "/v1/projects/test-project/locations/us-central1/publishers/anthropic/models/") {
		t.Fatalf("request = %s, want the regional Vertex endpoint for test-project", r.URL)
	}
}

// TS-10-36 (smoke, 10-PATH-3): a tool_use streamed through the SDK client
// and the provider's translator is replayed on the next request with the
// argument bytes the model streamed.
func TestSmokeStreamedToolInputReplaysVerbatim_TS10_36(t *testing.T) {
	raw := `{"path": "foo/bar.go",   "query": "func Test"}`
	stream := sseBody(
		[2]string{"message_start", `{"message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":3}}}`},
		[2]string{"content_block_start", `{"index":0,"content_block":{"type":"tool_use","id":"call_1","name":"search","input":{}}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":` + jsonString(raw[:17]) + `}}`},
		[2]string{"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":` + jsonString(raw[17:]) + `}}`},
		[2]string{"content_block_stop", `{"index":0}`},
		[2]string{"message_delta", `{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`},
		[2]string{"message_stop", `{}`},
	)
	client := sdk.NewClient(option.WithoutEnvironmentDefaults(), option.WithAPIKey("sk-ant-test"),
		option.WithMaxRetries(0),
		option.WithMiddleware(func(r *http.Request, _ option.MiddlewareNext) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Request: r,
				Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body:   io.NopCloser(strings.NewReader(stream))}, nil
		}))
	model, _ := catalog.Lookup("claude-opus-5-5")
	msg := anthropic.Provider(anthropic.Options{Client: &client}).Stream(context.Background(), &model,
		core.Request{Messages: userTurn()}, core.ProviderStreamOptions{}).Result()
	if msg == nil || msg.StopReason != core.StopReasonToolUse {
		t.Fatalf("streamed message = %+v", msg)
	}

	out, err := anthropic.BuildRequestJSON(core.Request{Messages: core.Messages{
		core.UserMessage{Content: core.Content{core.TextBlock{Text: "find the tests"}}},
		*msg,
		core.ToolResultMessage{ToolUseID: "call_1", ToolName: "search", Content: core.Content{core.TextBlock{Text: "ok"}}},
	}}, model)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"input":`+raw) {
		t.Fatalf("the replayed input is not the streamed bytes %s:\n%s", raw, out)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
