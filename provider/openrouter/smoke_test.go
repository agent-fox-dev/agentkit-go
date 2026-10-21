package openrouter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// The reasoning fragments of the first turn as the vendor sends them, and the
// tool call's arguments in the two pieces they arrive in.
const (
	smokeFrag1 = `{"type":"reasoning.text","text":"plan the work","signature":"sig-1","format":"anthropic-claude-v1","index":0,"id":"rd-1"}`
	smokeFrag2 = `{"type":"reasoning.summary","summary":"short plan","index":1,"id":"rd-2","format":"openai-responses-v1"}`
	smokeFrag3 = `{"type":"reasoning.encrypted","data":"AAAA==","index":2,"id":"rd-3","format":"openai-responses-v1"}`
	smokeArgs1 = `{"path": "a.txt", `
	smokeArgs2 = ` "limit": 5}`
)

// smokeTool is a tool whose schema has several properties, in an order that is
// not alphabetical. It counts its runs, and returns the schema as it marshals.
func smokeTool(t *testing.T, runs *int) (core.Tool, []byte) {
	t.Helper()
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"path":{"type":"string"},` +
		`"limit":{"type":"integer","minimum":1,"maximum":100},"mode":{"type":"string","enum":["a","b"]}},` +
		`"required":["path"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	marshaled, _ := s.MarshalJSON()
	return core.Tool{
		Name: "read_file", Description: "reads a file", InputSchema: s,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			*runs++
			return json.RawMessage(`{"text":"file contents"}`), nil
		},
	}, marshaled
}

// smokeTurns are the two replies: reasoning and a tool call, then the answer.
func smokeTurns() []reply {
	return []reply{
		sse(
			chunk(`{"reasoning":"plan the work","reasoning_details":[`+smokeFrag1+`,`+smokeFrag2+`]}`, ""),
			chunk(`{"reasoning_details":[`+smokeFrag3+`]}`, ""),
			chunk(`{"tool_calls":[{"index":0,"id":"call_0a1b2c","type":"function","function":{"name":"read_file","arguments":`+jsonStr(smokeArgs1)+`}}]}`, ""),
			chunk(`{"tool_calls":[{"index":0,"function":{"arguments":`+jsonStr(smokeArgs2)+`}}]}`, ""),
			chunk(`{}`, "tool_calls"),
			usageChunk(`{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"cost":0.0002}`),
			done),
		sse(chunk(text("The file says: file contents."), ""), chunk(`{}`, "stop"), done),
	}
}

// checkNoForeignValues fails the test if a request carries an Anthropic or an
// Ollama value in its body or in a header.
func checkNoForeignValues(t *testing.T, srv *replay) {
	t.Helper()
	for i, r := range srv.Requests() {
		for _, other := range []string{"sk-ant-other-key", "ollama.internal"} {
			if bytes.Contains(r.Body, []byte(other)) {
				t.Errorf("request %d body carries %q", i, other)
			}
			for k, vs := range r.Header {
				for _, v := range vs {
					if strings.Contains(v, other) {
						t.Errorf("request %d header %s carries %q", i, k, other)
					}
				}
			}
		}
	}
}

// checkSecondRequest checks the second request of the loop: the model as
// written, the tool's schema, the first turn's call id and arguments byte for
// byte, the result paired to that id and the reasoning fragments as received.
func checkSecondRequest(t *testing.T, srv *replay, wantParams []byte) {
	t.Helper()
	var second struct {
		Model string `json:"model"`
		Tools []struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(srv.Requests()[1].Body, &second); err != nil {
		t.Fatal(err)
	}
	if second.Model != "qwen/qwen3.8-flash" {
		t.Errorf("model = %q, want the catalog id as written", second.Model)
	}
	if len(second.Tools) != 1 || second.Tools[0].Function.Name != "read_file" || string(second.Tools[0].Function.Parameters) != string(wantParams) {
		t.Errorf("tools = %+v, want read_file with the schema as it marshals", second.Tools)
	}

	var assistant, toolMsg map[string]any
	for _, m := range second.Messages {
		switch m["role"] {
		case "assistant":
			assistant = m
		case "tool":
			toolMsg = m
		}
	}
	if assistant == nil || toolMsg == nil {
		t.Fatalf("messages = %v, want an assistant message and a tool result", second.Messages)
	}
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "call_0a1b2c" || fn["arguments"] != smokeArgs1+smokeArgs2 || fn["name"] != "read_file" {
		t.Errorf("tool call = %v, want id call_0a1b2c and the arguments byte for byte", call)
	}
	if toolMsg["tool_call_id"] != "call_0a1b2c" || !strings.Contains(toolMsg["content"].(string), "file contents") {
		t.Errorf("tool message = %v, want the result paired to call_0a1b2c", toolMsg)
	}
	details, _ := json.Marshal(assistant["reasoning_details"])
	if !jsonEqual(t, string(details), "["+smokeFrag1+","+smokeFrag2+","+smokeFrag3+"]") {
		t.Errorf("reasoning_details = %s, want the three fragments as received", details)
	}
}

// TS-27-56 (smoke, 27-PATH-1): a two-turn tool loop runs on OpenRouter against
// a recording server: the second request carries the model id as written, the
// tool's full schema, the first turn's call id and argument string byte for
// byte, the result paired to that id, and the reasoning fragments as received.
func TestSmokeTwoTurnToolLoop_TS27_56(t *testing.T) {
	const key = "sk-or-smoke-key-7a1"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-other-key")
	t.Setenv("OLLAMA_HOST", "http://ollama.internal:11434")

	sel, err := catalog.Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	var ran int
	tool, wantParams := smokeTool(t, &ran)
	turns := smokeTurns()
	srv := newReplay(t, turns...)

	p, err := openrouter.New(sel.Model, openrouter.Options{
		Env: map[string]string{openrouter.APIKeyVar: key, openrouter.BaseURLVar: srv.URL()},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentkit.New(agentkit.Config{
		Provider: p, Model: sel.Model.ID, Effort: sel.Effort,
		Toolset: agentkit.Toolset{Tools: []core.Tool{tool}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "read a.txt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.FinalText() != "The file says: file contents." || res.StopReason != core.RunStopEndTurn || ran != 1 {
		t.Fatalf("run = %q (%v), tool ran %d times; want the turn-2 text, a normal end and one tool call", res.FinalText(), res.StopReason, ran)
	}
	if srv.Count() != 2 {
		t.Fatalf("the server holds %d requests, want 2", srv.Count())
	}
	checkNoForeignValues(t, srv)
	checkSecondRequest(t, srv, wantParams)

	first := sentBody(t, srv, 0)
	if _, has := first["messages"].([]any)[0].(map[string]any)["reasoning_details"]; has {
		t.Error("the first request already carried reasoning_details")
	}
	if r, _ := first["reasoning"].(map[string]any); r["effort"] != "medium" {
		t.Errorf("the first request's reasoning = %v, want the tier's medium effort", r)
	}
}

// TS-27-57 (smoke, 27-PATH-2): a missing API key fails construction before any
// request, and only the two OPENROUTER variables are looked up.
func TestSmokeMissingKey_TS27_57(t *testing.T) {
	srv := newReplay(t)
	var asked []string
	_, err := openrouter.New(testModel(), openrouter.Options{
		Env: map[string]string{openrouter.BaseURLVar: srv.URL()},
		Getenv: func(name string) string {
			asked = append(asked, name)
			return ""
		},
	})
	if !errors.Is(err, openrouter.ErrNoAPIKey) || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error = %v, want one wrapping ErrNoAPIKey that names OPENROUTER_API_KEY", err)
	}
	if len(asked) == 0 {
		t.Error("Getenv was never asked, so the test checked nothing")
	}
	for _, name := range asked {
		if name != openrouter.APIKeyVar && name != openrouter.BaseURLVar {
			t.Errorf("the provider asked for %q", name)
		}
	}
	if srv.Count() != 0 {
		t.Errorf("the server received %d requests, want 0", srv.Count())
	}
}

// TS-27-58 (smoke, 27-PATH-3): an in-stream 429 before content is retried, and
// the turn reports netted usage and the reported cost.
func TestSmokeInStreamRetryAndUsage_TS27_58(t *testing.T) {
	srv := newReplay(t,
		withRetryAfter(sse(errorChunk(429, "slow down"), done), "0"),
		sse(chunk(text("ok"), ""), chunk(`{}`, "stop"),
			usageChunk(`{"prompt_tokens":354,"completion_tokens":120,"total_tokens":474,"cost":0.00012,`+
				`"prompt_tokens_details":{"cached_tokens":256},"completion_tokens_details":{"reasoning_tokens":80}}`),
			done))
	o := run(t, newProvider(t, srv), userRequest("hi"))
	if o.err != nil || srv.Count() != 2 {
		t.Fatalf("err = %v after %d requests, want success on the second", o.err, srv.Count())
	}
	if got, want := kinds(o.events), []string{"start", "text+0", "text~0", "text-0", "end"}; !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	u := o.msg.Usage
	if u.InputTokens != 98 || u.CacheReadTokens != 256 || u.OutputTokens != 120 || u.ReasoningTokens != 80 ||
		u.CostUSD != 0.00012 || !u.Has(core.UsageCostUSD) || u.Requests != 1 {
		t.Errorf("usage = %+v, want 98/256/120/80, cost 0.00012 with its bit, and one request", u)
	}
}

// TS-27-59 (smoke, 27-PATH-4): a stream that ends without a finish_reason fails
// the turn and leaves no tool call to execute.
func TestSmokeTruncatedStream_TS27_59(t *testing.T) {
	srv := newReplay(t, sse(
		chunk(text("some text"), ""),
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{\"pa"}}]}`, "")))
	o := run(t, newProvider(t, srv), userRequest("hi"))

	want := []string{"start", "text+0", "text~0", "tool+1", "tool~1", "error", "end"}
	if got := kinds(o.events); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if errEv, ok := o.events[len(o.events)-2].(core.ErrorEvent); !ok || !errEv.Terminal {
		t.Errorf("the event before the end = %#v, want a terminal ErrorEvent", o.events[len(o.events)-2])
	}
	if !errors.Is(o.err, openrouter.ErrStreamTruncated) {
		t.Errorf("error = %v, want ErrStreamTruncated", o.err)
	}
	if o.msg.StopReason != core.StopReasonError || o.msg.Content.Text() != "some text" || len(core.ExtractToolUse(&o.msg)) != 0 {
		t.Errorf("message = %q %q with %d tool calls, want an error turn with the text and none", o.msg.StopReason,
			o.msg.Content.Text(), len(core.ExtractToolUse(&o.msg)))
	}
}

// TS-27-60 (smoke, 27-PATH-5): a 401 ends the turn at once, and the key the
// vendor echoed is redacted from the error.
func TestSmokeAuthFailureRedactsTheKey_TS27_60(t *testing.T) {
	const key = "sk-or-echoed-key-5e9"
	srv := newReplay(t, jsonError(401, "invalid key "+key))
	c := newProvider(t, srv, func(o *openrouter.Options) {
		o.Env = map[string]string{openrouter.APIKeyVar: key, openrouter.BaseURLVar: srv.URL()}
	})
	o := run(t, c, userRequest("hi"))
	se := statusError(t, o.err)
	if srv.Count() != 1 || se.Code != 401 {
		t.Errorf("%d requests, code %d; want 1 and 401", srv.Count(), se.Code)
	}
	for _, s := range builtText(o, nil) {
		if strings.Contains(s, key) {
			t.Errorf("the key appears in %q", s)
		}
	}
	for _, part := range []string{"openrouter", "OPENROUTER_API_KEY", "[redacted]"} {
		if !strings.Contains(se.Text, part) {
			t.Errorf("Text %q lacks %q", se.Text, part)
		}
	}
}
