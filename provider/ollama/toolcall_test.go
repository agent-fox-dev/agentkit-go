package ollama_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// keyOrder returns the top-level keys of a JSON object, in the order they are
// written.
func keyOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %s", raw)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// toolBlocks returns the tool-use blocks of a message.
func toolBlocks(m core.AssistantMessage) []core.ToolUseBlock {
	return core.ExtractToolUse(&m)
}

// TS-28-28: a tool call keeps the vendor's id and the wire's key order, and is
// announced by a start event and one delta that carries all the arguments.
func TestToolCallKeepsIDAndKeyOrder_TS28_28(t *testing.T) {
	srv := newRecorder(t, lines(
		toolChunk("call_9", "lookup", `{"zeta":1,"alpha":2,"mid":3}`),
		toolChunk("call_10", "noargs", ""),
		doneChunk("served-model", "stop")))
	o := run(t, newClient(t, srv, testModel()), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}

	want := []string{"start", "tool+0", "tool~0", "update", "tool+1", "tool~1", "update", "tool-0", "tool-1", "end"}
	if got := kinds(o.events); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v\nwant     %v", got, want)
	}
	blocks := toolBlocks(o.msg)
	if len(blocks) != 2 {
		t.Fatalf("the message holds %d tool calls, want 2", len(blocks))
	}
	if blocks[0].ID != "call_9" || blocks[0].Name != "lookup" {
		t.Errorf("first call = %q %q, want call_9 lookup", blocks[0].ID, blocks[0].Name)
	}
	if got := keyOrder(t, blocks[0].Input); !reflect.DeepEqual(got, []string{"zeta", "alpha", "mid"}) {
		t.Errorf("Input key order = %v, want zeta, alpha, mid", got)
	}
	if string(blocks[1].Input) != "{}" {
		t.Errorf("a call without arguments has Input %s, want {}", blocks[1].Input)
	}
	for _, e := range o.events {
		switch v := e.(type) {
		case core.ToolCallStartEvent:
			if v.Name == "" || v.ToolUseID == "" {
				t.Errorf("start event %+v lacks the id or the name", v)
			}
		case core.ToolInputDeltaEvent:
			if v.ToolUseID == "call_9" && v.Delta != string(blocks[0].Input) {
				t.Errorf("the delta is %s, want all the arguments %s", v.Delta, blocks[0].Input)
			}
		}
	}
}

// TS-28-29 (property): the ids synthesized for tool calls that arrive without
// one are unique within a response and across the responses of one Client, and a
// vendor id used twice in a response is not kept twice.
func TestSynthesizedIDsAreUnique_TS28_29(t *testing.T) {
	r := rand.New(rand.NewSource(29))
	var replies []reply
	for range 30 {
		var chunks []string
		for range r.Intn(6) {
			id := ""
			if r.Intn(4) == 0 {
				id = fmt.Sprintf("call_%d", r.Intn(3)) // a vendor id that other calls repeat
			}
			chunks = append(chunks, toolChunk(id, "f", `{"n":1}`))
		}
		replies = append(replies, lines(append(chunks, doneChunk("served-model", "stop"))...))
	}
	srv := newRecorder(t, replies...)
	c := newClient(t, srv, testModel())

	for i := range replies {
		seen := map[string]bool{}
		for _, b := range toolBlocks(run(t, c, userRequest("hi")).msg) {
			if b.ID == "" {
				t.Fatalf("response %d: a tool call has no id", i)
			}
			if seen[b.ID] {
				t.Fatalf("response %d: id %q is used twice in the response", i, b.ID)
			}
			seen[b.ID] = true
		}
	}
}

// The ids are unique across responses of one Client as well as within them.
func TestSynthesizedIDsAreUniqueAcrossResponses_TS28_29(t *testing.T) {
	var replies []reply
	for range 20 {
		replies = append(replies, lines(toolChunk("", "f", ""), toolChunk("", "g", ""), doneChunk("served-model", "stop")))
	}
	srv := newRecorder(t, replies...)
	c := newClient(t, srv, testModel())
	seen := map[string]bool{}
	for range replies {
		for _, b := range toolBlocks(run(t, c, userRequest("hi")).msg) {
			if seen[b.ID] {
				t.Fatalf("id %q was produced twice by one Client", b.ID)
			}
			seen[b.ID] = true
		}
	}
	if len(seen) != 40 {
		t.Errorf("%d distinct ids, want 40", len(seen))
	}
}

// Two Clients that write tool calls into one transcript, as a session resumed in
// another process does, never produce the same id.
func TestSynthesizedIDsDifferBetweenClients_TS28_29(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		srv := newRecorder(t, lines(toolChunk("", "f", ""), doneChunk("served-model", "stop")))
		for _, b := range toolBlocks(run(t, newClient(t, srv, testModel()), userRequest("hi")).msg) {
			if seen[b.ID] {
				t.Fatalf("id %q was produced by two Clients", b.ID)
			}
			seen[b.ID] = true
		}
	}
}
