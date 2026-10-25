package openrouter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// reply is one canned response of the fake OpenRouter server.
type reply struct {
	status int
	header map[string]string
	body   string
}

// recorded is a request the fake server received.
type recorded struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// fakeServer is an httptest stand-in for OpenRouter: it records every request
// and answers them with its replies in order, the last one repeating (a
// server with none must get no request).
type fakeServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recorded
	replies []reply
}

func newServer(t *testing.T, replies ...reply) *fakeServer {
	t.Helper()
	f := &fakeServer{replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{req.Method, req.URL.Path, req.Header.Clone(), body})
		rep := f.replies[min(len(f.reqs), len(f.replies))-1]
		f.mu.Unlock()
		for k, v := range rep.header {
			w.Header().Set(k, v)
		}
		if rep.status != 0 {
			w.WriteHeader(rep.status)
		}
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) URL() string { return f.srv.URL }

func (f *fakeServer) Count() int { return len(f.Requests()) }

func (f *fakeServer) Requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// body decodes the body of the i-th request (numbers as json.Number).
func (f *fakeServer) body(t *testing.T, i int) map[string]any {
	t.Helper()
	return testutil.Object(t, f.Requests()[i].Body)
}

// sse is a 200 event-stream reply of the given SSE lines.
func sse(lines ...string) reply {
	return reply{status: 200, header: map[string]string{"Content-Type": "text/event-stream"},
		body: strings.Join(lines, "\n\n") + "\n\n"}
}

func data(payload string) string { return "data: " + payload }

const done = "data: [DONE]"

// chunk is a chat.completion.chunk whose one choice carries delta and finish
// ("" for none).
func chunk(delta, finish string) string {
	f := "null"
	if finish != "" {
		f = fmt.Sprintf("%q", finish)
	}
	return data(fmt.Sprintf(`{"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"vendor/served",`+
		`"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`, delta, f))
}

func text(s string) string { return fmt.Sprintf(`{"content":%q}`, s) }

// toolDelta is a chunk with one tool-call fragment; an empty id, name or args
// is left out.
func toolDelta(index int, id, name, args string) string {
	fn := map[string]any{}
	if name != "" {
		fn["name"] = name
	}
	if args != "" {
		fn["arguments"] = args
	}
	tc := map[string]any{"index": index, "type": "function", "function": fn}
	if id != "" {
		tc["id"] = id
	}
	return chunk(fmt.Sprintf(`{"tool_calls":[%s]}`, mustJSON(tc)), "")
}

// usageChunk is the chunk OpenRouter sends after the finish: no choices, and
// the usage.
func usageChunk(usage string) string {
	return data(`{"id":"gen-1","model":"vendor/served","choices":[],"usage":` + usage + `}`)
}

// errorChunk is an error object inside a 200 stream.
func errorChunk(code int, message string) string {
	return data(fmt.Sprintf(`{"error":{"code":%d,"message":%q}}`, code, message))
}

// jsonError is an HTTP error reply with OpenRouter's error body, and a
// Retry-After header unless retryAfter is "".
func jsonError(code int, message, retryAfter string) reply {
	h := map[string]string{"Content-Type": "application/json"}
	if retryAfter != "" {
		h["Retry-After"] = retryAfter
	}
	return reply{status: code, header: h, body: fmt.Sprintf(`{"error":{"code":%d,"message":%q}}`, code, message)}
}

// okTurn is the shortest good stream: the text "ok" and a stop.
func okTurn(after ...string) reply {
	return sse(append(append([]string{chunk(text("ok"), ""), chunk(`{}`, "stop")}, after...), done)...)
}

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func testModel() core.Model {
	return core.Model{ID: "vendor/model", ContextWindow: 100000, MaxOutputTokens: 4096}
}

// pricedModel has a price on every counter.
func pricedModel() core.Model {
	m := testModel()
	m.InputCostPerMillion, m.OutputCostPerMillion = 3, 15
	m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion = 0.3, 3.75
	return m
}

// options are the Options of a provider against base, with the test key, a
// fixed clock and no process environment.
func options(base string) openrouter.Options {
	return openrouter.Options{
		Env:    map[string]string{openrouter.APIKeyVar: "sk-or-test-key", openrouter.BaseURLVar: base},
		Getenv: func(string) string { return "" },
		Now:    func() time.Time { return fixedNow },
	}
}

// newProvider builds a provider for m against srv; opts adjust the Options.
func newProvider(t *testing.T, srv *fakeServer, m core.Model, opts ...func(*openrouter.Options)) core.ProviderClient {
	t.Helper()
	o := options(srv.URL())
	for _, f := range opts {
		f(&o)
	}
	c, err := openrouter.New(m, o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// collectWarnings returns an option that records warnings, and the record.
func collectWarnings() (func(*openrouter.Options), *[]string) {
	warns := &[]string{}
	return func(o *openrouter.Options) {
		o.Warnf = func(format string, a ...any) { *warns = append(*warns, fmt.Sprintf(format, a...)) }
	}, warns
}

func userMsg(s string) core.UserMessage {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

func userRequest(s string) core.Request { return core.Request{Messages: core.Messages{userMsg(s)}} }

func toolCall(id, name, args string) core.ToolUseBlock {
	return core.ToolUseBlock{ID: id, Name: name, Input: []byte(args)}
}

func result(id, name, text string) core.ToolResultMessage {
	return core.ToolResultMessage{ToolUseID: id, ToolName: name, Content: core.Content{core.TextBlock{Text: text}}}
}

func ptr[T any](v T) *T { return &v }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// outcome is a drained turn: every event, the final message and the error.
type outcome struct {
	events []core.Event
	msg    core.AssistantMessage
	err    error
}

func drain(ch <-chan core.StreamEvent) outcome {
	var o outcome
	for ev := range ch {
		if ev.Event != nil {
			o.events = append(o.events, ev.Event)
			if end, ok := ev.Event.(core.MessageEndEvent); ok {
				o.msg = end.Message
			}
		}
		if ev.Err != nil {
			o.err = ev.Err
		}
	}
	return o
}

// run streams one request to its end.
func run(t *testing.T, c core.ProviderClient, req core.Request) outcome {
	t.Helper()
	ch, err := c.Stream(context.Background(), req)
	if err != nil || ch == nil {
		t.Fatalf("Stream = %v, %v", ch, err)
	}
	return drain(ch)
}

// eventWords are the short names kinds gives the events.
var eventWords = map[string]string{
	"MessageStartEvent": "start", "MessageEndEvent": "end", "ErrorEvent": "error",
	"TextStartEvent": "text+", "TextDeltaEvent": "text~", "TextEndEvent": "text-",
	"ThinkingStartEvent": "think+", "ThinkingDeltaEvent": "think~", "ThinkingEndEvent": "think-",
	"ToolCallStartEvent": "tool+", "ToolInputDeltaEvent": "tool~", "ToolCallEndEvent": "tool-",
}

// kinds renders events one short word each, with the block index where there
// is one: "start", "think+0" (start), "text~1" (delta), "tool-2" (end).
func kinds(events []core.Event) string {
	var out []string
	for _, e := range events {
		v := reflect.ValueOf(e)
		w, ok := eventWords[v.Type().Name()]
		if !ok {
			w = v.Type().Name()
		}
		if f := v.FieldByName("BlockIndex"); f.IsValid() {
			w += strconv.Itoa(int(f.Int()))
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// statusError is the *StatusError err is, failing the test when it is none.
func statusError(t *testing.T, err error) *openrouter.StatusError {
	t.Helper()
	var se *openrouter.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v (%T), want a *openrouter.StatusError", err, err)
	}
	return se
}

// messagesOf is the messages array of a sent body.
func messagesOf(body map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

// roles is the roles of the messages, comma-joined.
func roles(msgs []map[string]any) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m["role"].(string))
	}
	return strings.Join(out, ",")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
