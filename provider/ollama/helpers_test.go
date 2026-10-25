package ollama_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// reply is one canned response of the fake Ollama server.
type reply struct {
	status int
	body   string
	// hold keeps the response open after the body, until the request is
	// cancelled or the test ends.
	hold bool
}

// recorded is a request the fake server received.
type recorded struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   []byte
}

// fakeServer is an httptest stand-in for Ollama: it records every request and
// answers them with its replies in order, the last one repeating (a server
// with none must get no request).
type fakeServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recorded
	replies []reply
}

func newServer(t *testing.T, replies ...reply) *fakeServer {
	t.Helper()
	f := &fakeServer{replies: replies}
	release := make(chan struct{})
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{req.Method, req.URL.Path, req.URL.RawQuery, req.Header.Clone(), body})
		rep := f.replies[min(len(f.reqs), len(f.replies))-1]
		f.mu.Unlock()
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
		w.(http.Flusher).Flush()
		if rep.hold {
			select {
			case <-release:
			case <-req.Context().Done():
			}
		}
	}))
	// Cleanups run last first: a held handler returns before Close waits on it.
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(release) })
	return f
}

func (f *fakeServer) URL() string { return f.srv.URL }

func (f *fakeServer) Count() int { return len(f.Requests()) }

func (f *fakeServer) Requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// body decodes the body of the i-th request.
func (f *fakeServer) body(t *testing.T, i int) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(f.Requests()[i].Body, &body); err != nil {
		t.Fatalf("request %d body: %v", i, err)
	}
	return body
}

// lines is a 200 reply of NDJSON lines.
func lines(ls ...string) reply {
	return reply{status: http.StatusOK, body: strings.Join(ls, "\n") + "\n"}
}

// errorReply is an error status with Ollama's error body, or none when msg is "".
func errorReply(status int, msg string) reply {
	if msg == "" {
		return reply{status: status}
	}
	return reply{status: status, body: fmt.Sprintf(`{"error":%q}`, msg)}
}

// chunk is a chunk that is not the last, with content and thinking ("" for none).
func chunk(content, thinking string) string {
	return fmt.Sprintf(`{"model":"served-model","message":{"role":"assistant","content":%q,"thinking":%q},"done":false}`, content, thinking)
}

// toolChunk is a chunk with one tool call; an empty id or args is left out.
func toolChunk(id, name, args string) string {
	call := fmt.Sprintf(`"function":{"index":0,"name":%q`, name)
	if args != "" {
		call += `,"arguments":` + args
	}
	if id != "" {
		call = fmt.Sprintf(`"id":%q,`, id) + call
	}
	return `{"model":"served-model","message":{"role":"assistant","content":"","tool_calls":[{` + call + `}}]},"done":false}`
}

// doneChunk is the last chunk, with the served model and the done reason.
func doneChunk(model, reason string) string {
	return fmt.Sprintf(`{"model":%q,"message":{"role":"assistant","content":""},"done":true,"done_reason":%q}`, model, reason)
}

// usageDone is the last chunk with counts; a cached count below 0 is left out.
func usageDone(prompt, cached, eval int) string {
	c := ""
	if cached >= 0 {
		c = fmt.Sprintf(`,"prompt_eval_cached_count":%d`, cached)
	}
	return fmt.Sprintf(`{"model":"served-model","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop",`+
		`"prompt_eval_count":%d%s,"eval_count":%d}`, prompt, c, eval)
}

// okTurn is one text turn.
func okTurn() reply { return lines(chunk("ok", ""), doneChunk("served-model", "stop")) }

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func testModel() core.Model {
	return core.Model{ID: "test-model:1b", ContextWindow: 8192, MaxOutputTokens: 4096}
}

// newClient builds a provider for m against srv with a fixed clock; opts adjust
// the Options first.
func newClient(t *testing.T, srv *fakeServer, m core.Model, opts ...func(*ollama.Options)) *ollama.Client {
	t.Helper()
	o := ollama.Options{Host: srv.URL(), Now: func() time.Time { return fixedNow }}
	for _, f := range opts {
		f(&o)
	}
	c, err := ollama.New(m, o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// collectWarnings returns an option that records warnings, and the record.
func collectWarnings() (func(*ollama.Options), *[]string) {
	warns := &[]string{}
	return func(o *ollama.Options) {
		o.Warnf = func(format string, a ...any) { *warns = append(*warns, fmt.Sprintf(format, a...)) }
	}, warns
}

func userText(s string) core.UserMessage {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

func userRequest(s string) core.Request { return core.Request{Messages: core.Messages{userText(s)}} }

func call(id, name, input string) core.ToolUseBlock {
	return core.ToolUseBlock{ID: id, Name: name, Input: json.RawMessage(input)}
}

func toolResult(id, name, text string, isError bool) core.ToolResultMessage {
	return core.ToolResultMessage{ToolUseID: id, ToolName: name, Content: core.Content{core.TextBlock{Text: text}}, IsError: isError}
}

func ptr[T any](v T) *T { return &v }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func mustSchema(t *testing.T, doc string) *schema.Schema {
	t.Helper()
	s, err := schema.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("schema %s: %v", doc, err)
	}
	return s
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
	"MessageStartEvent": "start", "MessageUpdateEvent": "update", "MessageEndEvent": "end", "ErrorEvent": "error",
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

// wireMessage is a message of a request body, with the arguments as written.
type wireMessage struct {
	Role, Content, Thinking string
	ToolName                string `json:"tool_name"`
	ToolCallID              string `json:"tool_call_id"`
	ToolCalls               []struct {
		ID       string
		Function struct {
			Index     int
			Name      string
			Arguments json.RawMessage
		}
	} `json:"tool_calls"`
}

// sentMessages streams req on c and returns the messages of its request body.
func sentMessages(t *testing.T, c *ollama.Client, srv *fakeServer, req core.Request) []wireMessage {
	t.Helper()
	if o := run(t, c, req); o.err != nil {
		t.Fatal(o.err)
	}
	reqs := srv.Requests()
	var body struct{ Messages []wireMessage }
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
		t.Fatal(err)
	}
	return body.Messages
}

// roles is the roles of the messages, comma-joined.
func roles(msgs []wireMessage) string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return strings.Join(out, ",")
}

// failedTurn checks that a turn failed, with a terminal ErrorEvent, an error
// turn whose ErrorMessage is the error's text and a text starting "ollama:";
// it returns the error.
func failedTurn(t *testing.T, o outcome) error {
	t.Helper()
	if o.err == nil {
		t.Fatal("the turn did not fail")
	}
	if !strings.HasPrefix(o.err.Error(), "ollama:") || o.msg.StopReason != core.StopReasonError || o.msg.ErrorMessage != o.err.Error() {
		t.Errorf("error %q, message %q %q; want an ollama: error turn", o.err, o.msg.StopReason, o.msg.ErrorMessage)
	}
	if ev, ok := o.events[len(o.events)-2].(core.ErrorEvent); !ok || !ev.Terminal || ev.Err != o.err {
		t.Errorf("events %s: want a terminal ErrorEvent carrying the error before the end", kinds(o.events))
	}
	return o.err
}

// closedPortHost is a loopback address nothing listens on.
func closedPortHost(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr
}

// setSecrets puts other providers' keys in the environment and returns them.
func setSecrets(t *testing.T) []string {
	secrets := []string{"sk-ant-secret-1a2b", "sk-or-secret-3c4d", "ollama-secret-5e6f"}
	for i, name := range []string{"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OLLAMA_API_KEY"} {
		t.Setenv(name, secrets[i])
	}
	return secrets
}
