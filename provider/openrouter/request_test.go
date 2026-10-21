package openrouter_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// ------------------------------------------------------------------ harness

// okTurn is the shortest good stream.
func okTurn() reply { return sse(chunk(text("ok"), ""), chunk(`{}`, "stop"), done) }

// sentBody decodes the body of the i-th recorded request.
func sentBody(t *testing.T, srv *replay, i int) map[string]any {
	t.Helper()
	reqs := srv.Requests()
	if i >= len(reqs) {
		t.Fatalf("the server received %d requests, want at least %d", len(reqs), i+1)
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[i].Body, &body); err != nil {
		t.Fatalf("request %d body is not JSON: %v\n%s", i, err, reqs[i].Body)
	}
	return body
}

// messagesOf returns the messages array of a sent body.
func messagesOf(body map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func roles(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m["role"].(string))
	}
	return out
}

func userMsg(s string) core.UserMessage {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

func toolCall(id, name, args string) core.ToolUseBlock {
	return core.ToolUseBlock{ID: id, Name: name, Input: json.RawMessage(args)}
}

func ptr[T any](v T) *T { return &v }

// jsonText renders a decoded JSON value as text.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// jsonUnmarshal decodes data into v, failing the test on an error.
func jsonUnmarshal(t *testing.T, data []byte, v any) bool {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Errorf("not JSON: %v\n%s", err, data)
		return false
	}
	return true
}

// ------------------------------------------------------------------- tests

// TS-27-2: a variable is read from Env, then Getenv, then the process
// environment, an empty value counts as unset, and only the two OPENROUTER
// names are asked for.
func TestVariablesResolveInOrder_TS27_2(t *testing.T) {
	t.Setenv(openrouter.APIKeyVar, "from-process")
	var asked []string
	getenv := func(key string) string {
		asked = append(asked, key)
		switch key {
		case openrouter.APIKeyVar:
			return "from-getenv"
		}
		return ""
	}
	srv := newReplay(t, okTurn())
	for _, tc := range []struct {
		name string
		opts openrouter.Options
		want string
	}{
		{"Env first", openrouter.Options{Env: map[string]string{openrouter.APIKeyVar: "from-env", openrouter.BaseURLVar: srv.URL()}, Getenv: getenv}, "from-env"},
		{"an empty Env value falls through to Getenv", openrouter.Options{Env: map[string]string{openrouter.APIKeyVar: ""},
			Getenv: func(key string) string {
				if key == openrouter.BaseURLVar {
					return srv.URL()
				}
				return getenv(key)
			}}, "from-getenv"},
		{"the process environment", openrouter.Options{Env: map[string]string{openrouter.BaseURLVar: srv.URL()}}, "from-process"},
	} {
		c, err := openrouter.New(testModel(), tc.opts)
		if err != nil {
			t.Fatalf("%s: New: %v", tc.name, err)
		}
		before := srv.Count()
		if o := run(t, c, userRequest("hi")); o.err != nil {
			t.Fatalf("%s: %v", tc.name, o.err)
		}
		reqs := srv.Requests()
		if got := reqs[before].Header.Get("Authorization"); got != "Bearer "+tc.want {
			t.Errorf("%s: Authorization = %q, want Bearer %s", tc.name, got, tc.want)
		}
	}
	for _, name := range asked {
		if name != openrouter.APIKeyVar && name != openrouter.BaseURLVar {
			t.Errorf("the provider asked Getenv for %q", name)
		}
	}
	if len(asked) == 0 {
		t.Error("Getenv was never asked, so the test checked nothing")
	}
}

// TS-27-6: every request is a POST to <base>/chat/completions with the key only
// in the Authorization header, the default base is openrouter.ai/api/v1, and
// trailing slashes of a configured base are dropped.
func TestEndpointAndAuthorization_TS27_6(t *testing.T) {
	const key = "sk-or-secret-key-9f2c"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-other-key")
	t.Setenv("OLLAMA_HOST", "http://ollama.internal:11434")
	t.Setenv("OPENROUTER_HTTP_REFERER", "https://example.invalid/app")
	t.Setenv("OPENROUTER_X_TITLE", "An App")

	checkNoLeak := func(label string, h http.Header, body []byte) {
		t.Helper()
		for k, vs := range h {
			for _, v := range vs {
				if k != "Authorization" && strings.Contains(v, key) {
					t.Errorf("%s: header %s carries the key", label, k)
				}
				for _, other := range []string{"sk-ant-other-key", "ollama.internal"} {
					if strings.Contains(v, other) {
						t.Errorf("%s: header %s carries %q", label, k, other)
					}
				}
			}
		}
		for _, name := range []string{"HTTP-Referer", "X-Title"} {
			if h.Get(name) != "" {
				t.Errorf("%s: header %s was sent", label, name)
			}
		}
		for _, other := range []string{key, "sk-ant-other-key", "ollama.internal"} {
			if bytes.Contains(body, []byte(other)) {
				t.Errorf("%s: the body carries %q", label, other)
			}
		}
	}

	// The default base URL, through a transport that never touches the network.
	var seen *http.Request
	var seenBody []byte
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Clone(r.Context())
		seenBody, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(okTurn().body))}, nil
	})
	c, err := openrouter.New(testModel(), openrouter.Options{
		Env:        map[string]string{openrouter.APIKeyVar: key},
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if o := run(t, c, userRequest("hi")); o.err != nil {
		t.Fatal(o.err)
	}
	if seen == nil || seen.Method != "POST" || seen.URL.Host != "openrouter.ai" || seen.URL.Path != "/api/v1/chat/completions" {
		t.Fatalf("request = %v, want POST openrouter.ai/api/v1/chat/completions", seen)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer "+key {
		t.Errorf("Authorization = %q, want Bearer <key>", got)
	}
	checkNoLeak("default base", seen.Header, seenBody)

	// A configured base, with trailing slashes.
	srv := newReplay(t, okTurn())
	c = newProvider(t, srv, func(o *openrouter.Options) {
		o.Env = map[string]string{openrouter.APIKeyVar: key, openrouter.BaseURLVar: srv.URL() + "//"}
	})
	if o := run(t, c, userRequest("hi")); o.err != nil {
		t.Fatal(o.err)
	}
	got := srv.Requests()[0]
	if got.Method != "POST" || got.Path != "/chat/completions" {
		t.Errorf("request = %s %s, want POST /chat/completions", got.Method, got.Path)
	}
	if got.Header.Get("Authorization") != "Bearer "+key {
		t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
	}
	checkNoLeak("configured base", got.Header, got.Body)
}

// TS-27-9: the body carries the model id as written, the stream flags, and the
// system blocks as one leading message.
func TestBodyModelFlagsAndSystem_TS27_9(t *testing.T) {
	srv := newReplay(t, okTurn())
	m := testModel()
	m.ID = "Vendor/Model:Free-v1.2"
	c := newProviderFor(t, srv, m)

	req := userRequest("hi")
	req.System = []core.ContentBlock{core.TextBlock{Text: "A"}, core.TextBlock{Text: "B"}}
	run(t, c, req)
	run(t, c, userRequest("hi"))

	body := sentBody(t, srv, 0)
	if body["model"] != "Vendor/Model:Free-v1.2" || body["stream"] != true {
		t.Errorf("model = %v, stream = %v", body["model"], body["stream"])
	}
	if so, _ := body["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", body["stream_options"])
	}
	msgs := messagesOf(body)
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "A\n\nB" {
		t.Errorf("messages[0] = %v, want the system message A\\n\\nB", msgs[0])
	}
	if n := strings.Count(strings.Join(roles(msgs), ","), "system"); n != 1 {
		t.Errorf("%d system messages, want 1", n)
	}
	if got := roles(messagesOf(sentBody(t, srv, 1))); strings.Contains(strings.Join(got, ","), "system") {
		t.Errorf("roles = %v, want no system message without system text", got)
	}
}

// TS-27-10: the prefix comes first, a text-less user message is skipped, each
// tool result is its own tool message with the call's id, and no cache marker
// is sent.
func TestMessageOrderAndToolResults_TS27_10(t *testing.T) {
	srv := newReplay(t, okTurn())
	c := newProvider(t, srv)
	okResult := core.ToolResultMessage{ToolUseID: "c1", ToolName: "read", Content: core.Content{core.TextBlock{Text: "contents"}}}
	errResult := core.ToolResultMessage{ToolUseID: "c2", ToolName: "write", IsError: true, Content: core.Content{core.TextBlock{Text: "denied"}}}
	req := core.Request{
		Prefix: core.Messages{userMsg("prefix")},
		Messages: core.Messages{
			core.UserMessage{},
			core.AssistantMessage{Content: core.Content{toolCall("c1", "read", `{}`), toolCall("c2", "write", `{}`)}, StopReason: core.StopReasonToolUse},
			okResult, errResult,
			userMsg("last"),
		},
	}
	run(t, c, req)

	msgs := messagesOf(sentBody(t, srv, 0))
	if got, want := strings.Join(roles(msgs), ","), "user,assistant,tool,tool,user"; got != want {
		t.Fatalf("roles = %s, want %s", got, want)
	}
	if msgs[0]["content"] != "prefix" || msgs[4]["content"] != "last" {
		t.Errorf("user contents = %v and %v, want the prefix first and the last message last", msgs[0]["content"], msgs[4]["content"])
	}
	for i, r := range []core.ToolResultMessage{okResult, errResult} {
		m := msgs[2+i]
		if m["tool_call_id"] != r.ToolUseID || m["content"] != provider.ToolResultText(r) {
			t.Errorf("tool message %d = %v, want call %s and content %q", i, m, r.ToolUseID, provider.ToolResultText(r))
		}
	}
	if !strings.HasPrefix(msgs[3]["content"].(string), "Error: ") {
		t.Errorf("the error result's content is %q, want an Error: prefix", msgs[3]["content"])
	}
	if bytes.Contains(srv.Requests()[0].Body, []byte("cache_control")) {
		t.Error("the body contains cache_control")
	}
}

// TS-27-11: an assistant message replays its tool call's arguments byte for
// byte, leaves out the content when it has no text, and an empty assistant
// message is not sent.
func TestAssistantMessagesReplayArguments_TS27_11(t *testing.T) {
	srv := newReplay(t, okTurn())
	c := newProvider(t, srv)
	const args = `{"b": 1,  "a":[1, 2]}`
	req := core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "reading"}, toolCall("c1", "read", args)}, StopReason: core.StopReasonToolUse},
		core.ToolResultMessage{ToolUseID: "c1", ToolName: "read", Content: core.Content{core.TextBlock{Text: "r"}}},
		core.AssistantMessage{StopReason: core.StopReasonStop},
		core.AssistantMessage{Content: core.Content{toolCall("c2", "read", `{}`)}, StopReason: core.StopReasonToolUse},
		core.ToolResultMessage{ToolUseID: "c2", ToolName: "read", Content: core.Content{core.TextBlock{Text: "r2"}}},
	}}
	run(t, c, req)

	msgs := messagesOf(sentBody(t, srv, 0))
	if got, want := strings.Join(roles(msgs), ","), "user,assistant,tool,assistant,tool"; got != want {
		t.Fatalf("roles = %s, want %s (the empty assistant message is skipped)", got, want)
	}
	call := msgs[1]["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "c1" || call["type"] != "function" || fn["name"] != "read" || fn["arguments"] != args {
		t.Errorf("tool call = %v, want id c1, type function, name read and the arguments as recorded", call)
	}
	if msgs[1]["content"] != "reading" {
		t.Errorf("content = %v, want the text", msgs[1]["content"])
	}
	if _, has := msgs[3]["content"]; has {
		t.Errorf("a message with only tool calls has content %v", msgs[3]["content"])
	}
}

// TS-27-12: a tool's parameters are its schema exactly as it marshals, a tool
// without a schema is an empty object, and no strict flag is sent.
func TestToolsAreDeclaredWithTheirSchema_TS27_12(t *testing.T) {
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"zeta":{"type":"string","format":"uri"},` +
		`"alpha":{"type":"integer","minimum":1,"maximum":9,"description":"a<b && c>d"},"mid":{"type":"array","items":{"type":"string"},"maxItems":3}},` +
		`"required":["zeta"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	srv := newReplay(t, okTurn())
	req := userRequest("hi")
	req.Tools = []core.ToolWire{
		{Name: "fetch", Description: "fetches a thing", InputSchema: s},
		{Name: "ping", Description: "pings"},
	}
	run(t, newProvider(t, srv), req)

	var sent struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(srv.Requests()[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 2 {
		t.Fatalf("%d tools sent, want 2", len(sent.Tools))
	}
	if string(sent.Tools[0].Function.Parameters) != string(want) {
		t.Errorf("parameters = %s\nwant       %s", sent.Tools[0].Function.Parameters, want)
	}
	if !jsonEqual(t, string(sent.Tools[1].Function.Parameters), `{"type":"object","properties":{}}`) {
		t.Errorf("a tool without a schema has parameters %s", sent.Tools[1].Function.Parameters)
	}
	for i, tool := range sent.Tools {
		if tool.Type != "function" || tool.Function.Name != req.Tools[i].Name || tool.Function.Description != req.Tools[i].Description {
			t.Errorf("tool %d = %+v", i, tool)
		}
	}
	if bytes.Contains(srv.Requests()[0].Body, []byte(`"strict"`)) {
		t.Error("a strict flag was sent")
	}
}

// TS-27-13: tool choice auto and none are sent, and an unset choice is not.
func TestToolChoice_TS27_13(t *testing.T) {
	srv := newReplay(t, okTurn())
	c := newProvider(t, srv)
	for _, choice := range []core.ToolChoice{core.ToolChoiceAuto, core.ToolChoiceNone, core.ToolChoiceUnset} {
		req := userRequest("hi")
		req.ToolChoice = choice
		run(t, c, req)
	}
	for i, want := range []any{"auto", "none", nil} {
		got, has := sentBody(t, srv, i)["tool_choice"]
		if want == nil && has || want != nil && got != want {
			t.Errorf("request %d: tool_choice = %v (present %v), want %v", i, got, has, want)
		}
	}
}

// TS-27-14: max_completion_tokens is the request's bound capped at the model's
// output cap, the cap when the request has none, or absent.
func TestMaxCompletionTokens_TS27_14(t *testing.T) {
	for _, tc := range []struct {
		maxTokens *int
		cap       int
		want      any
	}{
		{ptr(200000), 131072, float64(131072)},
		{ptr(4096), 131072, float64(4096)},
		{nil, 32768, float64(32768)},
		{nil, 0, nil},
		{ptr(4096), 0, float64(4096)},
	} {
		srv := newReplay(t, okTurn())
		m := testModel()
		m.MaxOutputTokens = tc.cap
		req := userRequest("hi")
		req.MaxTokens = tc.maxTokens
		run(t, newProviderFor(t, srv, m), req)
		got, has := sentBody(t, srv, 0)["max_completion_tokens"]
		if tc.want == nil && has || tc.want != nil && got != tc.want {
			t.Errorf("MaxTokens %v with cap %d: max_completion_tokens = %v (present %v), want %v", tc.maxTokens, tc.cap, got, has, tc.want)
		}
	}
}

// TS-27-15: temperature, top_p and stop are sent when set, a zero included, and
// otherwise left out.
func TestSamplingAndStop_TS27_15(t *testing.T) {
	srv := newReplay(t, okTurn())
	c := newProvider(t, srv)
	req := userRequest("hi")
	req.Temperature, req.TopP, req.StopSequences = ptr(0.0), ptr(0.0), []string{"END"}
	run(t, c, req)
	run(t, c, userRequest("hi"))

	b := sentBody(t, srv, 0)
	if v, has := b["temperature"]; !has || v != float64(0) {
		t.Errorf("temperature = %v (present %v), want 0", v, has)
	}
	if v, has := b["top_p"]; !has || v != float64(0) {
		t.Errorf("top_p = %v (present %v), want 0", v, has)
	}
	if stop, _ := b["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop = %v, want [END]", b["stop"])
	}
	b = sentBody(t, srv, 1)
	for _, k := range []string{"temperature", "top_p", "stop"} {
		if _, has := b[k]; has {
			t.Errorf("the second body has %s", k)
		}
	}
}
