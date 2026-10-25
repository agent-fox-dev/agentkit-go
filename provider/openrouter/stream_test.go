package openrouter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestStreamRefusesAContextAlreadyDone(t *testing.T) {
	srv := newServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := newProvider(t, srv, testModel()).Stream(ctx, userRequest("hi"))
	if ch != nil || !errors.Is(err, context.Canceled) || srv.Count() != 0 {
		t.Errorf("Stream = %v, %v after %d requests; want no channel, context.Canceled and none", ch, err, srv.Count())
	}
}

func TestConcurrentStreamsOnOneProviderAreIndependent(t *testing.T) {
	c := newProvider(t, newServer(t, okTurn()), testModel())
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			ch, err := c.Stream(context.Background(), userRequest("hi"))
			if err != nil {
				t.Errorf("Stream: %v", err)
				return
			}
			o := drain(ch)
			if o.err != nil || o.msg.Content.Text() != "ok" || kinds(o.events) != "start text+0 text~0 text-0 end" {
				t.Errorf("turn = %q %s, %v", o.msg.Content.Text(), kinds(o.events), o.err)
			}
		})
	}
	wg.Wait()
}

// Block indices follow first appearance, deltas of one kind extend one block,
// and keep-alive comments, role-only and empty deltas and a choice other than
// 0 make no event.
func TestBlocksAreIndexedByFirstAppearanceAndNoiseMakesNoEvent(t *testing.T) {
	stream := func(noisy bool) reply {
		var lines []string
		add := func(noise bool, l ...string) {
			if noisy || !noise {
				lines = append(lines, l...)
			}
		}
		add(true, ": OPENROUTER PROCESSING", chunk(`{"role":"assistant"}`, ""))
		add(false, chunk(`{"reasoning":"think"}`, ""))
		add(true, chunk(`{}`, ""), data(`{"id":"gen-1","choices":[{"index":1,"delta":{"content":"NOPE"},"finish_reason":null}]}`))
		add(false, chunk(text("Hel"), ""), chunk(text("lo"), ""), toolDelta(0, "call_a", "read", `{"p":1}`))
		add(true, ": OPENROUTER PROCESSING")
		add(false, chunk(`{}`, "tool_calls"), done)
		return sse(lines...)
	}
	noisy := run(t, newProvider(t, newServer(t, stream(true)), testModel()), userRequest("hi"))
	clean := run(t, newProvider(t, newServer(t, stream(false)), testModel()), userRequest("hi"))
	want := "start think+0 think~0 text+1 text~1 text~1 tool+2 tool~2 think-0 text-1 tool-2 end"
	if got := kinds(noisy.events); noisy.err != nil || got != want || noisy.msg.Content.Text() != "Hello" {
		t.Errorf("events = %s, %v\nwant     %s", got, noisy.err, want)
	}
	if mustJSON(noisy.events) != mustJSON(clean.events) || strings.Contains(noisy.msg.Content.Text(), "NOPE") {
		t.Errorf("the noise changed the turn:\n%v\n%v", noisy.events, clean.events)
	}
}

// Reasoning becomes a thinking block, never text: the visible text is
// delta.reasoning, else the text and summary fragments; every fragment is kept
// whole in the signature, which is empty when none arrived.
func TestReasoningBecomesAThinkingBlockKeepingEveryFragment(t *testing.T) {
	fragA := `{"type":"reasoning.text","text":"think A","signature":"sigA","format":"anthropic-claude-v1","index":0,"id":"rd-1"}`
	fragB := `{"type":"reasoning.text","text":"B","index":1,"id":"rd-2","format":"anthropic-claude-v1"}`
	fragS := `{"type":"reasoning.summary","summary":"S","index":2,"id":"rd-3","format":"openai-responses-v1"}`
	fragE := `{"type":"reasoning.encrypted","data":"AAAA==","index":3,"id":"rd-4","format":"openai-responses-v1"}`
	for _, tc := range []struct {
		name            string
		deltas          []string
		thinking, frags string
		events          string
	}{
		{"fragments", []string{chunk(`{"reasoning":"think A","reasoning_details":[`+fragA+`]}`, ""), chunk(`{"reasoning_details":[`+fragB+`,`+fragS+`,`+fragE+`]}`, "")},
			"think ABS", "[" + fragA + "," + fragB + "," + fragS + "," + fragE + "]", "start think+0 think~0 think~0 think-0 end"},
		{"an encrypted fragment alone", []string{chunk(`{"reasoning_details":[`+fragE+`]}`, "")}, "", "[" + fragE + "]", "start think+0 think-0 end"},
		{"text only", []string{chunk(`{"reasoning":"just text"}`, "")}, "just text", "", "start think+0 think~0 think-0 end"},
	} {
		o := run(t, newProvider(t, newServer(t, sse(append(tc.deltas, chunk(`{}`, "stop"), done)...)), testModel()), userRequest("hi"))
		if got := kinds(o.events); o.err != nil || got != tc.events {
			t.Errorf("%s: events = %s, %v; want %s", tc.name, got, o.err, tc.events)
			continue
		}
		tb, ok := o.msg.Content[0].(core.ThinkingBlock)
		if !ok || len(o.msg.Content) != 1 || tb.Thinking != tc.thinking || tb.Redacted {
			t.Errorf("%s: content = %+v, want one thinking block %q", tc.name, o.msg.Content, tc.thinking)
		}
		if tc.frags == "" && tb.Signature != "" || tc.frags != "" && !testutil.SameJSON([]byte(tb.Signature), []byte(tc.frags)) {
			t.Errorf("%s: signature = %s, want %s", tc.name, tb.Signature, tc.frags)
		}
	}
}

// Fragments accumulate by index into arguments that are the byte-for-byte
// concatenation; a call starts at its first named fragment, with the
// arguments that came before; a call without an id gets a distinct call_<hex>;
// arguments that are not a JSON object become {}; one never named is dropped.
// Both are warned of.
func TestToolCallFragmentsAccumulateByIndex(t *testing.T) {
	a1, a2, a3 := `{"path": `, `"a.txt",`+"\n  ", `"n": [1, 2]}`
	b1, b2 := `{ "k" :`, ` "v" }`
	warn, warns := collectWarnings()
	o := run(t, newProvider(t, newServer(t, sse(
		toolDelta(0, "call_a", "read", a1),
		toolDelta(1, "", "", b1),
		toolDelta(0, "", "", a2),
		toolDelta(1, "", "write", b2),
		toolDelta(0, "", "", a3),
		toolDelta(2, "", "x", ""),
		toolDelta(3, "", "y", "{}"),
		toolDelta(4, "call_lost", "", "{}"),
		toolDelta(5, "call_x", "bad", `[1,2`),
		chunk(`{}`, "tool_calls"), done)), testModel(), warn), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	calls := core.ExtractToolUse(&o.msg)
	if len(calls) != 5 || string(calls[4].Input) != "{}" || calls[0].ID != "call_a" || calls[0].Name != "read" || calls[1].Name != "write" || calls[2].Name != "x" {
		t.Fatalf("calls = %+v, want read, write, x, y and bad with input {}", calls)
	}
	if string(calls[0].Input) != a1+a2+a3 || string(calls[1].Input) != b1+b2 || string(calls[2].Input) != "{}" {
		t.Errorf("inputs = %q, %q, %q", calls[0].Input, calls[1].Input, calls[2].Input)
	}
	idForm := regexp.MustCompile(`^call_[0-9a-f]{16}$`)
	ids := map[string]bool{}
	for _, c := range calls[1:4] {
		if !idForm.MatchString(c.ID) || ids[c.ID] {
			t.Errorf("id %q, want a distinct call_<hex>", c.ID)
		}
		ids[c.ID] = true
	}
	var trace []string
	for _, e := range o.events {
		switch v := e.(type) {
		case core.ToolCallStartEvent:
			trace = append(trace, fmt.Sprintf("+%d:%s", v.BlockIndex, v.Name))
		case core.ToolInputDeltaEvent:
			trace = append(trace, fmt.Sprintf("~%d:%s", v.BlockIndex, v.Delta))
		}
	}
	want := []string{"+0:read", "~0:" + a1, "~0:" + a2, "+1:write", "~1:" + b1, "~1:" + b2, "~0:" + a3, "+2:x", "+3:y", "~3:{}", "+5:bad", "~5:[1,2"}
	if mustJSON(trace) != mustJSON(want) {
		t.Errorf("tool events = %q\nwant          %q", trace, want)
	}
	if len(*warns) != 2 || !strings.Contains((*warns)[0], "never named") || !strings.Contains((*warns)[0], "index 4") ||
		!strings.Contains((*warns)[1], "call_x (bad)") {
		t.Errorf("warnings = %q, want the unnamed call at index 4 and call_x's arguments", *warns)
	}
}

// Every finish reason maps to a stop reason and is kept raw, a tool call
// survives any of them, and the message carries the response id and model and
// the model, effort and time the turn was asked with.
func TestFinishReasonsMapToStopReasons(t *testing.T) {
	for raw, want := range map[string]core.StopReason{
		"stop": core.StopReasonStop, "length": core.StopReasonLength, "tool_calls": core.StopReasonToolUse,
		"content_filter": core.StopReasonRefusal, "weird": core.StopReasonStop,
	} {
		req := userRequest("hi")
		req.Effort = core.EffortHigh
		o := run(t, newProvider(t, newServer(t, sse(toolDelta(0, "call_1", "t", "{}"), chunk(`{}`, raw), done)), testModel()), req)
		m := o.msg
		if o.err != nil || m.StopReason != want || m.RawStopReason != raw || len(core.ExtractToolUse(&m)) != 1 {
			t.Errorf("%s: stop = %q raw %q with %d calls, %v; want %q", raw, m.StopReason, m.RawStopReason, len(core.ExtractToolUse(&m)), o.err, want)
		}
		if m.ResponseID != "gen-1" || m.ResponseModel != "vendor/served" || m.Model != "vendor/model" ||
			m.Effort != core.EffortHigh || !m.Timestamp.Equal(fixedNow) {
			t.Errorf("%s: message = %q %q %q %q %v", raw, m.ResponseID, m.ResponseModel, m.Model, m.Effort, m.Timestamp)
		}
	}
}

// Cancelling while the request is in flight, in the middle of the stream or
// during the wait before a retry ends the turn as aborted.
func TestCancellingEndsTheTurnAsAborted(t *testing.T) {
	for _, mode := range []string{"request", "stream", "retry wait"} {
		entered := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if mode == "retry wait" {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(503)
				return
			}
			if mode == "stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, chunk(text("partial"), "")+"\n\n")
				w.(http.Flusher).Flush()
			}
			entered <- struct{}{}
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithCancel(context.Background())
		o := options(srv.URL)
		// The clock is read at the turn's start and again just before the wait.
		var reads atomic.Int32
		o.Now = func() time.Time {
			if reads.Add(1) == 2 && mode == "retry wait" {
				cancel()
			}
			return fixedNow
		}
		c, err := openrouter.New(testModel(), o)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := c.Stream(ctx, userRequest("hi"))
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "request":
			<-entered
			cancel()
		case "stream":
			for ev := range ch {
				if _, ok := ev.Event.(core.TextDeltaEvent); ok {
					break
				}
			}
			cancel()
		}
		ended := make(chan outcome, 1)
		go func() { ended <- drain(ch) }()
		select {
		case out := <-ended:
			if out.msg.StopReason != core.StopReasonAborted || out.msg.ErrorMessage != "Request was aborted" || !errors.Is(out.err, core.ErrAborted) {
				t.Errorf("%s: message = %q %q, err = %v; want an aborted turn", mode, out.msg.StopReason, out.msg.ErrorMessage, out.err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the turn did not end after the cancellation", mode)
		}
		srv.Close()
		cancel()
	}
}

// A two-turn tool loop through the agent: the second request carries the
// model id as the catalog writes it, the tool's schema, the first turn's call
// id and argument bytes, the result paired to that id, and the reasoning
// fragments as received.
func TestATwoTurnToolLoopReplaysTheFirstTurn(t *testing.T) {
	frag1 := `{"type":"reasoning.text","text":"plan","signature":"sig-1","format":"anthropic-claude-v1","index":0,"id":"rd-1"}`
	frag2 := `{"type":"reasoning.encrypted","data":"AAAA==","index":1,"id":"rd-2","format":"openai-responses-v1"}`
	args1, args2 := `{"path": "a.txt", `, ` "limit": 5}`
	srv := newServer(t,
		sse(chunk(`{"reasoning":"plan","reasoning_details":[`+frag1+`]}`, ""), chunk(`{"reasoning_details":[`+frag2+`]}`, ""),
			toolDelta(0, "call_0a1b2c", "read_file", args1), toolDelta(0, "", "", args2), chunk(`{}`, "tool_calls"),
			usageChunk(`{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"cost":0.0002}`), done),
		sse(chunk(text("The file says: file contents."), ""), chunk(`{}`, "stop"), done))
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"]}`))
	if err != nil {
		t.Fatal(err)
	}
	wantParams, _ := s.MarshalJSON()
	ran := 0
	tool := core.Tool{Name: "read_file", Description: "reads a file", InputSchema: s,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			ran++
			return json.RawMessage(`{"text":"file contents"}`), nil
		}}
	sel, err := catalog.Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := openrouter.New(sel.Model, options(srv.URL()))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentkit.New(agentkit.Config{Client: p, Model: sel.Model.ID, Effort: sel.Effort,
		Toolset: agentkit.Toolset{Tools: []core.Tool{tool}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "read a.txt")
	if err != nil || res.FinalText() != "The file says: file contents." || res.StopReason != core.RunStopEndTurn || ran != 1 || srv.Count() != 2 {
		t.Fatalf("run = %q %v, %v; tool ran %d times over %d requests", res.FinalText(), res.StopReason, err, ran, srv.Count())
	}
	if raw := srv.Requests()[1].Body; !bytes.Contains(raw, []byte(`"model":"`+sel.Model.ID+`"`)) || !bytes.Contains(raw, []byte(`"parameters":`+string(wantParams))) {
		t.Errorf("second body = %s, want the model id as written and the schema as it marshals", raw)
	}
	msgs := messagesOf(srv.body(t, 1))
	assistant, toolMsg := msgs[len(msgs)-2], msgs[len(msgs)-1]
	if assistant["role"] != "assistant" || toolMsg["role"] != "tool" {
		t.Fatalf("messages = %v, want the assistant turn and its result last", msgs)
	}
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "call_0a1b2c" || call["function"].(map[string]any)["arguments"] != args1+args2 {
		t.Errorf("tool call = %v, want call_0a1b2c and the arguments byte for byte", call)
	}
	if toolMsg["tool_call_id"] != "call_0a1b2c" || !strings.Contains(toolMsg["content"].(string), "file contents") {
		t.Errorf("tool message = %v", toolMsg)
	}
	if got := mustJSON(assistant["reasoning_details"]); !testutil.SameJSON([]byte(got), []byte("["+frag1+","+frag2+"]")) {
		t.Errorf("reasoning_details = %s, want the fragments as received", got)
	}
}
