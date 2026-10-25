package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const (
	testKey         = "sk-ant-test-key"
	testGoogleToken = "ya29.test-google-token"
	toolArgs        = `{"zebra":1,"apple":2}`
)

func testModel() *core.Model {
	return &core.Model{
		ID: "claude-test", ContextWindow: 200000, MaxOutputTokens: 4096,
		InputCostPerMillion: 3, OutputCostPerMillion: 15,
		CacheReadCostPerMillion: 0.3, CacheWriteCostPerMillion: 3.75,
	}
}

func user(texts ...string) core.UserMessage {
	var c core.Content
	for _, s := range texts {
		c = append(c, core.TextBlock{Text: s})
	}
	return core.UserMessage{Content: c}
}

func toolUse(id, name, args string) core.ToolUseBlock {
	b, err := core.NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		panic(err)
	}
	return b
}

// reply is a turn of testModel's own.
func reply(blocks ...core.ContentBlock) core.AssistantMessage {
	return core.AssistantMessage{Content: blocks, StopReason: core.StopReasonToolUse, Model: "claude-test"}
}

func result(id, text string) core.ToolResultMessage {
	return core.ToolResultMessage{ToolUseID: id, ToolName: "tool", Content: core.Content{core.TextBlock{Text: text}}}
}

// sse renders event, data pairs as a text/event-stream body, adding the type
// to a data object that does not name it, as the API always does.
func sse(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		typ, data := pairs[i], pairs[i+1]
		if data == "{}" {
			data = `{"type":"` + typ + `"}`
		} else if strings.HasPrefix(data, "{") && !strings.HasPrefix(data, `{"type":`) {
			data = `{"type":"` + typ + `",` + data[1:]
		}
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", typ, data)
	}
	return b.String()
}

func messageStart(model string) string {
	return `{"message":{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[],` +
		`"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":900,"cache_creation_input_tokens":50}}}`
}

// streamFixture is a turn of text then a tool call, with usage in both
// message_start and message_delta.
func streamFixture() string {
	return sse(
		"message_start", messageStart("claude-test"),
		"content_block_start", `{"index":0,"content_block":{"type":"text","text":""}}`,
		"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		"content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		"content_block_stop", `{"index":0}`,
		"content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"edit_file","input":{}}}`,
		"content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"zebra\":"}}`,
		"content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"1,\"apple\":2}"}}`,
		"content_block_stop", `{"index":1}`,
		"message_delta", `{"delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`,
		"message_stop", `{}`,
	)
}

// wholeFixture is streamFixture's turn as a non-streaming response.
const wholeFixture = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test",` +
	`"content":[{"type":"text","text":"Hello"},{"type":"tool_use","id":"toolu_1","name":"edit_file","input":` + toolArgs + `}],` +
	`"stop_reason":"tool_use","stop_sequence":null,` +
	`"usage":{"input_tokens":100,"output_tokens":42,"cache_read_input_tokens":900,"cache_creation_input_tokens":50}}`

// exchange is one turn through the provider: what it produced and the
// request the transport saw (nil when none was sent).
type exchange struct {
	msg    *core.AssistantMessage
	err    error
	events []core.Event
	req    *http.Request
	body   []byte
}

// run sends req through a provider whose transport answers status and body.
// It never reads the process environment or this machine's credentials:
// with no Env the provider sees only an API key, and Vertex authorizes with
// a fixed Google token. Retries are off.
func run(t *testing.T, m *core.Model, req core.Request, opts anthropic.Options, status int, body string) exchange {
	t.Helper()
	var x exchange
	if opts.Transport == nil {
		opts.Transport = rtFunc(func(r *http.Request) (*http.Response, error) {
			x.req = r
			if r.Body != nil {
				x.body, _ = io.ReadAll(r.Body)
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Request: r,
				Body: io.NopCloser(strings.NewReader(body))}, nil
		})
	}
	if opts.Getenv == nil {
		opts.Getenv = func(string) string { return "" }
	}
	if opts.Env == nil && opts.Client == nil {
		opts.Env = map[string]string{anthropic.APIKeyVar: testKey}
	}
	if opts.VertexTokenSource == nil {
		opts.VertexTokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testGoogleToken})
	}
	if opts.MaxRetries == nil {
		zero := 0
		opts.MaxRetries = &zero
	}
	s := core.EventStreamOf(anthropic.Provider(*m, opts).Stream(context.Background(), req))
	for e := range s.Events() {
		x.events = append(x.events, e)
	}
	res := s.Wait()
	x.msg, x.err = res.Message, res.Err
	return x
}

// sent is the decoded request body.
func (x exchange) sent(t *testing.T) map[string]any {
	t.Helper()
	return decode(t, x.body)
}

func eventNames(events []core.Event) string {
	var out []string
	for _, e := range events {
		out = append(out, string(e.EventType()))
	}
	return strings.Join(out, ",")
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return out
}

// wireBody is the request body BuildRequestJSONWith produces, decoded.
func wireBody(t *testing.T, m *core.Model, req core.Request, opts anthropic.Options) map[string]any {
	t.Helper()
	raw, err := anthropic.BuildRequestJSONWith(req, *m, opts)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, raw)
}

// objects is a JSON array of objects.
func objects(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		o, _ := x.(map[string]any)
		out = append(out, o)
	}
	return out
}

// blocks is the content of each wire message.
func blocks(body map[string]any) [][]map[string]any {
	var out [][]map[string]any
	for _, m := range objects(body["messages"]) {
		out = append(out, objects(m["content"]))
	}
	return out
}

// ttl is a block's breakpoint: "-" for none, "5m" for the default and
// otherwise its ttl.
func ttl(b map[string]any) string {
	cc, _ := b["cache_control"].(map[string]any)
	switch {
	case cc == nil:
		return "-"
	case cc["ttl"] == nil:
		return "5m"
	}
	return fmt.Sprint(cc["ttl"])
}
