package ollama_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// The turn opens with a MessageStartEvent carrying the model, the effort and
// the clock's time; thinking and content stream as separate blocks, each closed
// with a message update when the kind switches; the done chunk ends the blocks
// in order and the message, with the response model and the raw reason.
func TestChunksBecomeBlocksAndTheDoneChunkEndsTheTurn(t *testing.T) {
	tool := toolChunk("call_1", "lookup", `{"q":1}`)
	for _, tc := range []struct {
		name, reason string
		chunks       []string
		stop         core.StopReason
		events       string
	}{
		{"thinking then text", "stop", []string{chunk("", "pon"), chunk("", "der"), chunk("ans", ""), chunk("wer", "")}, core.StopReasonStop,
			"start think+0 think~0 think~0 update text+1 text~1 text~1 update think-0 text-1 end"},
		{"a tool call", "stop", []string{chunk("t", ""), tool}, core.StopReasonToolUse,
			"start text+0 text~0 update tool+1 tool~1 update text-0 tool-1 end"},
		{"length wins over a tool call", "length", []string{chunk("t", ""), tool}, core.StopReasonLength,
			"start text+0 text~0 update tool+1 tool~1 update text-0 tool-1 end"},
		{"one chunk with both", "stop", []string{`{"model":"m","message":{"role":"assistant","content":"c","thinking":"p"},"done":false}`}, core.StopReasonStop,
			"start think+0 think~0 update text+1 text~1 update think-0 text-1 end"},
		{"no content", "", nil, core.StopReasonStop, "start end"},
	} {
		req := userRequest("hi")
		req.Effort = core.EffortMedium
		o := run(t, newClient(t, newServer(t, lines(append(tc.chunks, doneChunk("resp-model", tc.reason))...)), testModel()), req)
		if got := kinds(o.events); o.err != nil || got != tc.events {
			t.Errorf("%s: events = %s, %v\nwant %s", tc.name, got, o.err, tc.events)
			continue
		}
		m := o.msg
		if m.StopReason != tc.stop || m.RawStopReason != tc.reason || m.ResponseModel != "resp-model" {
			t.Errorf("%s: message = %q raw %q model %q, want %q", tc.name, m.StopReason, m.RawStopReason, m.ResponseModel, tc.stop)
		}
		if tc.name == "thinking then text" && (!reflect.DeepEqual(m.Content, core.Content{core.ThinkingBlock{Thinking: "ponder"}, core.TextBlock{Text: "answer"}}) ||
			len(o.events[4].(core.MessageUpdateEvent).Message.Content) != 1) {
			t.Errorf("content = %#v, want ponder and answer, the first update holding the thinking", m.Content)
		}
		if s := o.events[0].(core.MessageStartEvent).Message; s.Model != "test-model:1b" || s.Effort != core.EffortMedium || !s.Timestamp.Equal(fixedNow) {
			t.Errorf("%s: start = %q %q %v", tc.name, s.Model, s.Effort, s.Timestamp)
		}
	}
}

// A tool call, which Ollama sends whole, keeps the vendor's id and the wire's
// key order and is announced by a start and one delta holding all its
// arguments; a call without arguments has the input {}.
func TestAToolCallKeepsItsIDAndKeyOrder(t *testing.T) {
	srv := newServer(t, lines(toolChunk("call_9", "lookup", `{"zeta":1,"alpha":2,"mid":3}`), toolChunk("call_10", "noargs", ""), doneChunk("m", "stop")))
	o := run(t, newClient(t, srv, testModel()), userRequest("hi"))
	if got := kinds(o.events); got != "start tool+0 tool~0 update tool+1 tool~1 update tool-0 tool-1 end" {
		t.Errorf("events = %s", got)
	}
	calls := core.ExtractToolUse(&o.msg)
	if len(calls) != 2 || calls[0].ID != "call_9" || calls[0].Name != "lookup" || string(calls[1].Input) != "{}" {
		t.Fatalf("calls = %+v", calls)
	}
	if string(calls[0].Input) != `{"zeta":1,"alpha":2,"mid":3}` {
		t.Errorf("input = %s, want the wire's key order", calls[0].Input)
	}
	if s, d := o.events[1].(core.ToolCallStartEvent), o.events[2].(core.ToolInputDeltaEvent); s.ToolUseID != "call_9" || s.Name != "lookup" ||
		d.Delta != string(calls[0].Input) {
		t.Errorf("start %+v, delta %+v; want call_9 lookup and all the arguments", s, d)
	}
}

// A call without an id, or with an id already used in the response, gets an
// id unique within the response, across the Client's responses and between
// Clients.
func TestSynthesizedToolCallIDsAreUnique(t *testing.T) {
	turn := lines(toolChunk("", "f", ""), toolChunk("call_1", "g", ""), toolChunk("call_1", "h", ""), toolChunk("", "i", ""), doneChunk("m", "stop"))
	seen := map[string]bool{}
	for client := range 3 {
		c := newClient(t, newServer(t, turn), testModel())
		for range 3 {
			calls := core.ExtractToolUse(ptr(run(t, c, userRequest("hi")).msg))
			if len(calls) != 4 || calls[1].ID != "call_1" {
				t.Fatalf("calls = %+v, want four with the vendor's call_1 kept once", calls)
			}
			for _, b := range calls {
				if b.ID == "" || seen[b.ID] && b.ID != "call_1" {
					t.Errorf("client %d: id %q is empty or used before", client, b.ID)
				}
				if b.ID != "call_1" && !strings.HasPrefix(b.ID, "ollama_") {
					t.Errorf("synthesized id %q", b.ID)
				}
				seen[b.ID] = true
			}
		}
	}
	if len(seen) != 1+3*3*3 {
		t.Errorf("%d distinct ids, want %d", len(seen), 1+3*3*3)
	}
}
