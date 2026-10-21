package openrouter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// ------------------------------------------------------------------ harness

// reply is one canned response of a replay server.
type reply struct {
	status int
	header map[string]string
	body   string
}

// recordedRequest is a request the replay server received.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// replay is an httptest server that records every request and answers them
// with canned replies in order; the last reply repeats. With no replies it
// answers an empty event stream.
type replay struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []recordedRequest
	replies []reply
}

func newReplay(t *testing.T, replies ...reply) *replay {
	t.Helper()
	r := &replay{replies: replies}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, recordedRequest{req.Method, req.URL.Path, req.Header.Clone(), body})
		n := len(r.reqs) - 1
		rep := reply{status: http.StatusOK, header: map[string]string{"Content-Type": "text/event-stream"}}
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
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *replay) URL() string { return r.srv.URL }

// Count is how many requests the server has received.
func (r *replay) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// Requests returns the requests received so far.
func (r *replay) Requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.reqs...)
}

// sse is a 200 event-stream reply whose body is the chunks as data lines, then
// the [DONE] sentinel.
func sse(chunks ...string) reply {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	return reply{status: 200, header: map[string]string{"Content-Type": "text/event-stream"}, body: b.String()}
}

// data renders a chunk payload as an SSE data line; done is the terminator.
func data(payload string) string { return "data: " + payload }

const done = "data: [DONE]"

// chunk builds a chat.completion.chunk whose only choice carries delta and
// finish ("" for none).
func chunk(delta, finish string) string {
	f := "null"
	if finish != "" {
		f = fmt.Sprintf("%q", finish)
	}
	return data(fmt.Sprintf(`{"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"vendor/served",`+
		`"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`, delta, f))
}

// text is a content delta.
func text(s string) string { return fmt.Sprintf(`{"content":%q}`, s) }

var fixedNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

// newProvider builds a provider for the test model against srv with a fixed
// clock; opts adjusts the Options first.
func newProvider(t *testing.T, srv *replay, opts ...func(*openrouter.Options)) core.ProviderClient {
	t.Helper()
	return newProviderFor(t, srv, testModel(), opts...)
}

// newProviderFor is newProvider for the model m.
func newProviderFor(t *testing.T, srv *replay, m core.Model, opts ...func(*openrouter.Options)) core.ProviderClient {
	t.Helper()
	o := openrouter.Options{
		Env: map[string]string{openrouter.APIKeyVar: "sk-or-test-key", openrouter.BaseURLVar: srv.URL()},
		Now: func() time.Time { return fixedNow },
	}
	for _, f := range opts {
		f(&o)
	}
	c, err := openrouter.New(m, o)
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
func drain(t *testing.T, ch <-chan core.StreamEvent) outcome {
	t.Helper()
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
	return drain(t, ch)
}

// kinds renders an event sequence as one short word per event, with the block
// index where the event has one: "start", "think+0", "text~1", "tool-0".
func kinds(events []core.Event) []string {
	var out []string
	for _, e := range events {
		switch v := e.(type) {
		case core.MessageStartEvent:
			out = append(out, "start")
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

func jsonEqual(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Errorf("not JSON: %q (%v)", got, err)
		return false
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation %q: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

// ------------------------------------------------------------------- tests

// TS-27-7: Stream refuses a context that is already done, without a request.
func TestStreamRefusesACancelledContext_TS27_7(t *testing.T) {
	srv := newReplay(t)
	c := newProvider(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := c.Stream(ctx, userRequest("hi"))
	if ch != nil || err != context.Canceled {
		t.Errorf("Stream = %v, %v; want a nil channel and context.Canceled", ch, err)
	}
	if srv.Count() != 0 {
		t.Errorf("the server received %d requests, want 0", srv.Count())
	}
}

// TS-27-8: concurrent Stream calls on one provider each complete on their own;
// run it under -race.
func TestConcurrentStreamsAreIndependent_TS27_8(t *testing.T) {
	srv := newReplay(t, sse(chunk(text("ok"), ""), chunk(`{}`, "stop"), done))
	c := newProvider(t, srv)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := c.Stream(context.Background(), userRequest("hi"))
			if err != nil {
				t.Errorf("Stream: %v", err)
				return
			}
			o := drain(t, ch)
			if o.err != nil || o.msg.Content.Text() != "ok" || o.msg.StopReason != core.StopReasonStop {
				t.Errorf("turn = %q %q, %v; want the text ok and a stop", o.msg.Content.Text(), o.msg.StopReason, o.err)
			}
			if k := kinds(o.events); k[len(k)-1] != "end" {
				t.Errorf("events end with %q, want the message end", k[len(k)-1])
			}
		}()
	}
	wg.Wait()
}

// TS-27-26: content deltas become a text start and text deltas at one block
// index, and the end event carries the whole text.
func TestTextDeltasShareABlock_TS27_26(t *testing.T) {
	srv := newReplay(t, sse(chunk(text("Hel"), ""), chunk(text("lo"), ""), chunk(`{}`, "stop"), done))
	o := run(t, newProvider(t, srv), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	want := []string{"start", "text+0", "text~0", "text~0", "text-0", "end"}
	if got := kinds(o.events); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	var deltas []string
	for _, e := range o.events {
		if d, ok := e.(core.TextDeltaEvent); ok {
			deltas = append(deltas, d.Delta)
		}
		if end, ok := e.(core.TextEndEvent); ok && end.Text != "Hello" {
			t.Errorf("TextEndEvent.Text = %q, want Hello", end.Text)
		}
	}
	if !reflect.DeepEqual(deltas, []string{"Hel", "lo"}) {
		t.Errorf("deltas = %v, want [Hel lo]", deltas)
	}
}

// streamWith is a stream of reasoning, text and a tool call. With ignorable it
// also holds the input a decoder must not count as content: keep-alive
// comments, a role-only delta, an empty delta, and a choice of index 1.
func streamWith(ignorable bool) reply {
	var parts []string
	add := func(p ...string) { parts = append(parts, p...) }
	if ignorable {
		add(": OPENROUTER PROCESSING")
		add(chunk(`{"role":"assistant"}`, ""))
	}
	add(chunk(`{"reasoning":"think"}`, ""))
	if ignorable {
		add(": OPENROUTER PROCESSING")
		add(chunk(`{}`, ""))
		add(data(`{"id":"gen-1","model":"vendor/served","choices":[{"index":1,"delta":{"content":"NOPE"},"finish_reason":null}]}`))
	}
	add(chunk(text("hi"), ""))
	add(chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"p\":1}"}}]}`, ""))
	if ignorable {
		add(": OPENROUTER PROCESSING")
	}
	add(chunk(`{}`, "tool_calls"), done)
	return sse(parts...)
}

// TS-27-25: block indices follow first appearance, and keep-alive comments,
// role-only and empty deltas and other choices produce no event.
func TestBlockIndicesAndIgnorableInput_TS27_25(t *testing.T) {
	noisy := run(t, newProvider(t, newReplay(t, streamWith(true))), userRequest("hi"))
	clean := run(t, newProvider(t, newReplay(t, streamWith(false))), userRequest("hi"))
	if noisy.err != nil || clean.err != nil {
		t.Fatalf("errors: %v, %v", noisy.err, clean.err)
	}
	want := []string{"start", "think+0", "think~0", "text+1", "text~1", "tool+2", "tool~2",
		"think-0", "text-1", "tool-2", "end"}
	if got := kinds(noisy.events); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(noisy.events, clean.events) {
		t.Errorf("the noisy stream's events differ from the clean stream's:\n%v\n%v", noisy.events, clean.events)
	}
	if strings.Contains(noisy.msg.Content.Text(), "NOPE") {
		t.Errorf("the index-1 choice reached the message: %q", noisy.msg.Content.Text())
	}
}

// TS-27-27: reasoning fragments become the thinking block's fragment array and
// visible text, and never text events.
func TestReasoningFragmentsBecomeThinking_TS27_27(t *testing.T) {
	fragA := `{"type":"reasoning.text","text":"think A","signature":"sigA","format":"anthropic-claude-v1","index":0,"id":"rd-1"}`
	fragB := `{"type":"reasoning.text","text":"B","index":1,"id":"rd-2","format":"anthropic-claude-v1"}`
	fragS := `{"type":"reasoning.summary","summary":"S","index":2,"id":"rd-3","format":"openai-responses-v1"}`
	fragE := `{"type":"reasoning.encrypted","data":"AAAA==","index":3,"id":"rd-4","format":"openai-responses-v1"}`
	srv := newReplay(t, sse(
		chunk(`{"reasoning":"think A","reasoning_details":[`+fragA+`]}`, ""),
		chunk(`{"reasoning_details":[`+fragB+`,`+fragS+`,`+fragE+`]}`, ""),
		chunk(`{}`, "stop"), done))
	o := run(t, newProvider(t, srv), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	for _, k := range kinds(o.events) {
		if strings.HasPrefix(k, "text") {
			t.Errorf("a text event %q was emitted for reasoning", k)
		}
	}
	var tb core.ThinkingBlock
	for _, b := range o.msg.Content {
		if v, ok := b.(core.ThinkingBlock); ok {
			tb = v
		}
	}
	if tb.Thinking != "think ABS" {
		t.Errorf("Thinking = %q, want think ABS (reasoning, then the text and summary fragments)", tb.Thinking)
	}
	if tb.Redacted {
		t.Error("Redacted = true, want false")
	}
	if !jsonEqual(t, tb.Signature, "["+fragA+","+fragB+","+fragS+","+fragE+"]") {
		t.Errorf("Signature = %s, want the four fragments in arrival order with every field", tb.Signature)
	}

	// No fragments: the signature is empty.
	only := run(t, newProvider(t, newReplay(t, sse(chunk(`{"reasoning":"just text"}`, ""), chunk(`{}`, "stop"), done))), userRequest("hi"))
	tb = only.msg.Content[0].(core.ThinkingBlock)
	if tb.Thinking != "just text" || tb.Signature != "" {
		t.Errorf("block = %+v, want the text and an empty Signature", tb)
	}
}

// toolCallsOf returns the tool-use blocks of a message.
func toolCallsOf(m core.AssistantMessage) []core.ToolUseBlock { return core.ExtractToolUse(&m) }

// TS-27-28: fragments accumulate by index; arguments are the byte-for-byte
// concatenation; calls without an id get distinct call_<hex> ids.
func TestToolCallFragmentsAccumulate_TS27_28(t *testing.T) {
	a1, a2, a3 := `{"path": `, `"a.txt",`+"\n  ", `"n": [1, 2]}`
	b1, b2 := `{ "k" :`, ` "v" }`
	srv := newReplay(t, sse(
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":`+jsonStr(a1)+`}}]}`, ""),
		chunk(`{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"write","arguments":`+jsonStr(b1)+`}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":`+jsonStr(a2)+`}}]}`, ""),
		chunk(`{"tool_calls":[{"index":1,"function":{"arguments":`+jsonStr(b2)+`}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":`+jsonStr(a3)+`}}]}`, ""),
		chunk(`{}`, "tool_calls"), done))
	o := run(t, newProvider(t, srv), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	calls := toolCallsOf(o.msg)
	if len(calls) != 2 || calls[0].ID != "call_a" || calls[0].Name != "read" || calls[1].ID != "call_b" || calls[1].Name != "write" {
		t.Fatalf("calls = %+v, want call_a read and call_b write", calls)
	}
	if got, want := string(calls[0].Input), a1+a2+a3; got != want {
		t.Errorf("call_a input = %q, want %q", got, want)
	}
	if got, want := string(calls[1].Input), b1+b2; got != want {
		t.Errorf("call_b input = %q, want %q", got, want)
	}
	var deltas, starts []string
	for _, e := range o.events {
		switch v := e.(type) {
		case core.ToolInputDeltaEvent:
			deltas = append(deltas, v.ToolUseID+"|"+v.Delta)
		case core.ToolCallStartEvent:
			starts = append(starts, fmt.Sprintf("%d|%s|%s", v.BlockIndex, v.ToolUseID, v.Name))
		}
	}
	if !reflect.DeepEqual(starts, []string{"0|call_a|read", "1|call_b|write"}) {
		t.Errorf("starts = %v", starts)
	}
	wantDeltas := []string{"call_a|" + a1, "call_b|" + b1, "call_a|" + a2, "call_b|" + b2, "call_a|" + a3}
	if !reflect.DeepEqual(deltas, wantDeltas) {
		t.Errorf("input deltas = %q, want %q", deltas, wantDeltas)
	}

	// Two calls with no id, and a call with empty arguments.
	srv = newReplay(t, sse(
		chunk(`{"tool_calls":[{"index":0,"function":{"name":"x","arguments":"{}"}},{"index":1,"function":{"name":"y"}}]}`, ""),
		chunk(`{}`, "tool_calls"), done))
	o = run(t, newProvider(t, srv), userRequest("hi"))
	calls = toolCallsOf(o.msg)
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want 2", calls)
	}
	idForm := regexp.MustCompile(`^call_[0-9a-f]+$`)
	if !idForm.MatchString(calls[0].ID) || !idForm.MatchString(calls[1].ID) || calls[0].ID == calls[1].ID {
		t.Errorf("ids = %q and %q, want two distinct call_<hex> ids", calls[0].ID, calls[1].ID)
	}
	if string(calls[1].Input) != "{}" {
		t.Errorf("empty arguments gave %q, want {}", calls[1].Input)
	}
}

// jsonStr renders s as a JSON string literal.
func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// TS-27-29: arguments that are not a JSON object become {} and the warning
// names the call.
func TestNonObjectArgumentsBecomeEmpty_TS27_29(t *testing.T) {
	for _, args := range []string{`[1,2`, `"str"`} {
		srv := newReplay(t, sse(
			chunk(`{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"read","arguments":`+jsonStr(args)+`}}]}`, ""),
			chunk(`{}`, "tool_calls"), done))
		var warns []string
		c := newProvider(t, srv, func(o *openrouter.Options) {
			o.Warnf = func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }
		})
		o := run(t, c, userRequest("hi"))
		calls := toolCallsOf(o.msg)
		if len(calls) != 1 || string(calls[0].Input) != "{}" {
			t.Errorf("arguments %q: calls = %+v, want one call with input {}", args, calls)
		}
		if len(warns) != 1 || !strings.Contains(warns[0], "call_x") || !strings.Contains(warns[0], "read") {
			t.Errorf("arguments %q: warnings = %q, want one naming call_x and read", args, warns)
		}
	}
}

// TS-27-31: every finish reason maps to its stop reason and is kept raw, tool
// calls survive any of them, and the response id and model are set.
func TestFinishReasonsMapToStopReasons_TS27_31(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want core.StopReason
	}{
		{"stop", core.StopReasonStop},
		{"length", core.StopReasonLength},
		{"tool_calls", core.StopReasonToolUse},
		{"content_filter", core.StopReasonRefusal},
		{"weird", core.StopReasonStop},
	} {
		srv := newReplay(t, sse(
			chunk(`{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"t","arguments":"{}"}}]}`, ""),
			chunk(`{}`, tc.raw), done))
		req := userRequest("hi")
		req.Effort = core.EffortHigh
		o := run(t, newProvider(t, srv), req)
		m := o.msg
		if o.err != nil || m.StopReason != tc.want || m.RawStopReason != tc.raw {
			t.Errorf("finish %q: stop = %q raw %q, %v; want %q", tc.raw, m.StopReason, m.RawStopReason, o.err, tc.want)
		}
		if len(toolCallsOf(m)) != 1 {
			t.Errorf("finish %q: the message lost its tool call: %+v", tc.raw, m.Content)
		}
		if m.ResponseID != "gen-1" || m.ResponseModel != "vendor/served" {
			t.Errorf("finish %q: response id/model = %q/%q, want gen-1 and vendor/served", tc.raw, m.ResponseID, m.ResponseModel)
		}
		if m.Model != "vendor/model" || m.Effort != core.EffortHigh || !m.Timestamp.Equal(fixedNow) {
			t.Errorf("finish %q: model %q effort %q at %v, want vendor/model, high, the clock", tc.raw, m.Model, m.Effort, m.Timestamp)
		}
	}
}

// A cancelled caller ends the turn as aborted. (The abort path is built with
// the stream plumbing; the spec's own cancellation test, TS-27-42, covers the
// request and the wait as well.)
func TestCancellingMidStreamAborts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chunk(text("partial"), "")+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c, err := openrouter.New(testModel(), openrouter.Options{
		Env: map[string]string{openrouter.APIKeyVar: "sk-or-test-key", openrouter.BaseURLVar: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Stream(ctx, userRequest("hi"))
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if _, ok := ev.Event.(core.TextDeltaEvent); ok {
			cancel()
			break
		}
	}
	done := make(chan outcome, 1)
	go func() { done <- drain(t, ch) }()
	select {
	case o := <-done:
		if o.msg.StopReason != core.StopReasonAborted || o.msg.ErrorMessage != "Request was aborted" {
			t.Errorf("message = %q %q, want an aborted turn", o.msg.StopReason, o.msg.ErrorMessage)
		}
		if o.err == nil || !strings.Contains(o.err.Error(), core.ErrAborted.Error()) {
			t.Errorf("error = %v, want core.ErrAborted", o.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end within 5 seconds of the cancellation")
	}
}
