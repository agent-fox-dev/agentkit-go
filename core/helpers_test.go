package core

import (
	"encoding/json"
	"testing"
)

func toolUse(t *testing.T, id, name, args string) ToolUseBlock {
	t.Helper()
	b, err := NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("NewToolUse(%s): %v", args, err)
	}
	return b
}

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

// toolSet builds tools whose Description is "<name> tool", in the order given.
func toolSet(ns ...string) []Tool {
	ts := make([]Tool, len(ns))
	for i, n := range ns {
		ts[i] = Tool{Name: n, Description: n + " tool"}
	}
	return ts
}

// stubEncoder stands in for the session codec: enough to prove an event's
// envelope, not the message shape.
func stubEncoder(m Message) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"role": string(m.Role())})
}

func object(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %s", b)
	}
	return m
}
