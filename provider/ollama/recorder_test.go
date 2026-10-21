package ollama_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// ------------------------------------------------------------------ server

// reply is one canned response of the recording server.
type reply struct {
	status int
	header map[string]string
	body   string
	// hold keeps the response open after the body is written, until the request
	// is cancelled or the test ends.
	hold bool
}

// recordedRequest is a request the recording server received.
type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// recorder is an httptest server that records every request and answers them
// with canned replies in order; the last reply repeats. With no replies it
// answers an empty 200, which is a stream with no chunk.
type recorder struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recordedRequest
	replies []reply
	release chan struct{}
}

func newRecorder(t *testing.T, replies ...reply) *recorder {
	t.Helper()
	r := &recorder{replies: replies, release: make(chan struct{})}
	r.srv = httptest.NewServer(http.HandlerFunc(r.handle))
	t.Cleanup(r.srv.Close)
	// Cleanups run last in, first out: a handler that holds its response open
	// returns before Close waits for it.
	t.Cleanup(func() { close(r.release) })
	return r
}

func (r *recorder) handle(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, recordedRequest{req.Method, req.URL.Path, req.URL.RawQuery, req.Header.Clone(), body})
	n := len(r.reqs) - 1
	rep := reply{status: http.StatusOK}
	if len(r.replies) > 0 {
		rep = r.replies[min(n, len(r.replies)-1)]
	}
	r.mu.Unlock()
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	if rep.status != 0 {
		w.WriteHeader(rep.status)
	}
	_, _ = io.WriteString(w, rep.body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if rep.hold {
		select {
		case <-r.release:
		case <-req.Context().Done():
		}
	}
}

func (r *recorder) URL() string { return r.srv.URL }

// Count is how many requests the server has received.
func (r *recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// Requests returns the requests received so far.
func (r *recorder) Requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.reqs...)
}

// ------------------------------------------------------------------ replies

// lines is a 200 reply whose body is the lines, one per line.
func lines(ls ...string) reply {
	return reply{status: http.StatusOK, header: map[string]string{"Content-Type": "application/x-ndjson"},
		body: strings.Join(ls, "\n") + "\n"}
}

// chunk is a chunk of the response that is not the last, holding content and
// thinking ("" for none).
func chunk(content, thinking string) string {
	think := ""
	if thinking != "" {
		think = fmt.Sprintf(`,"thinking":%q`, thinking)
	}
	return fmt.Sprintf(`{"model":"served-model","created_at":"2026-10-21T10:00:00Z",`+
		`"message":{"role":"assistant","content":%q%s},"done":false}`, content, think)
}

// toolChunk is a chunk holding one tool call. args is the arguments' JSON as the
// server wrote it, "" for a call with no arguments, and id "" for a call with no
// id.
func toolChunk(id, name, args string) string {
	var call strings.Builder
	call.WriteString("{")
	if id != "" {
		fmt.Fprintf(&call, `"id":%q,`, id)
	}
	fmt.Fprintf(&call, `"function":{"index":0,"name":%q`, name)
	if args != "" {
		call.WriteString(`,"arguments":` + args)
	}
	call.WriteString("}}")
	return `{"model":"served-model","created_at":"2026-10-21T10:00:00Z",` +
		`"message":{"role":"assistant","content":"","tool_calls":[` + call.String() + `]},"done":false}`
}

// doneChunk is the last chunk: done is true and the reason is as given.
func doneChunk(model, reason string) string {
	return fmt.Sprintf(`{"model":%q,"created_at":"2026-10-21T10:00:00Z",`+
		`"message":{"role":"assistant","content":""},"done":true,"done_reason":%q}`, model, reason)
}

// --------------------------------------------------------------- the client

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// testModel is a model with a window and a cap of its own.
func testModel() core.Model {
	return core.Model{ID: "test-model:1b", ContextWindow: 8192, MaxOutputTokens: 4096}
}

// newClient builds a provider for m against srv with a fixed clock; opts adjusts
// the Options first.
func newClient(t *testing.T, srv *recorder, m core.Model, opts ...func(*ollama.Options)) *ollama.Client {
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

func userRequest(s string) core.Request {
	return core.Request{Messages: core.Messages{core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}}}
}

// outcome is a drained turn: every event, the final message and the error.
type outcome struct {
	events []core.Event
	msg    core.AssistantMessage
	err    error
}

// drain reads a provider channel to its close.
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

// run streams one request to completion.
func run(t *testing.T, c core.ProviderClient, req core.Request) outcome {
	t.Helper()
	ch, err := c.Stream(context.Background(), req)
	if err != nil || ch == nil {
		t.Fatalf("Stream = %v, %v", ch, err)
	}
	return drain(ch)
}

// kinds renders an event sequence as one short word per event, with the block
// index where the event has one: "start", "think+0", "text~1", "tool-2".
func kinds(events []core.Event) []string {
	var out []string
	for _, e := range events {
		switch v := e.(type) {
		case core.MessageStartEvent:
			out = append(out, "start")
		case core.MessageUpdateEvent:
			out = append(out, "update")
		case core.MessageEndEvent:
			out = append(out, "end")
		case core.TextStartEvent:
			out = append(out, fmt.Sprintf("text+%d", v.BlockIndex))
		case core.TextDeltaEvent:
			out = append(out, fmt.Sprintf("text~%d", v.BlockIndex))
		case core.TextEndEvent:
			out = append(out, fmt.Sprintf("text-%d", v.BlockIndex))
		case core.ThinkingStartEvent:
			out = append(out, fmt.Sprintf("think+%d", v.BlockIndex))
		case core.ThinkingDeltaEvent:
			out = append(out, fmt.Sprintf("think~%d", v.BlockIndex))
		case core.ThinkingEndEvent:
			out = append(out, fmt.Sprintf("think-%d", v.BlockIndex))
		case core.ToolCallStartEvent:
			out = append(out, fmt.Sprintf("tool+%d", v.BlockIndex))
		case core.ToolInputDeltaEvent:
			out = append(out, fmt.Sprintf("tool~%d", v.BlockIndex))
		case core.ToolCallEndEvent:
			out = append(out, fmt.Sprintf("tool-%d", v.BlockIndex))
		case core.ErrorEvent:
			out = append(out, "error")
		default:
			out = append(out, fmt.Sprintf("%T", e))
		}
	}
	return out
}
