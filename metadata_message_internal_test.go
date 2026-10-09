package agentkit

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-04-41: Message Content is byte-identical whether or not the handler's
// result carries Metadata.
func TestContentIdenticalWithAndWithoutMetadata_TS04_41(t *testing.T) {
	ec := 2
	c, err := core.NewToolUse("c1", "probe", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	r := core.ToolResult{
		OK:     false,
		Error:  "command_exit",
		Text:   "boom\n[exit 2]",
		Blocks: core.Content{core.ImageBlock{Data: "abc", MimeType: "image/png"}},
	}
	rm := r
	rm.Metadata = &core.ToolMetadata{ExitCode: &ec, Outcome: "exit", TotalBytes: 5, DurationMS: 12}

	msgPlain := toolResultMessage(c, r)
	msgMeta := toolResultMessage(c, rm)

	// Content must be deep-equal.
	if !reflect.DeepEqual(msgPlain.Content, msgMeta.Content) {
		t.Fatalf("Content differs:\nplain: %v\nmeta:  %v", msgPlain.Content, msgMeta.Content)
	}

	// ToLLMMap must be identical and have no metadata key.
	mapPlain := r.ToLLMMap()
	mapMeta := rm.ToLLMMap()
	if !reflect.DeepEqual(mapPlain, mapMeta) {
		t.Fatalf("ToLLMMap differs:\nplain: %v\nmeta:  %v", mapPlain, mapMeta)
	}
	if _, has := mapMeta["metadata"]; has {
		t.Fatal("ToLLMMap must not contain a metadata key")
	}

	// LLMText must be identical.
	if r.LLMText() != rm.LLMText() {
		t.Fatalf("LLMText differs:\nplain: %q\nmeta:  %q", r.LLMText(), rm.LLMText())
	}

	// Also test without Text (JSON envelope path).
	r2 := core.ToolResult{OK: true, Data: map[string]any{"x": 1}}
	rm2 := r2
	rm2.Metadata = &core.ToolMetadata{TotalLines: 10}
	if r2.LLMText() != rm2.LLMText() {
		t.Fatalf("LLMText (envelope) differs:\nplain: %q\nmeta:  %q", r2.LLMText(), rm2.LLMText())
	}
	mapPlain2 := r2.ToLLMMap()
	mapMeta2 := rm2.ToLLMMap()
	if !reflect.DeepEqual(mapPlain2, mapMeta2) {
		t.Fatalf("ToLLMMap (envelope) differs:\nplain: %v\nmeta:  %v", mapPlain2, mapMeta2)
	}
}

// TS-04-37 (partial): the no_result backstop produces a message with nil
// Metadata. This is a package-internal test because it exercises the
// no_result path in executeBatch's final assertion.
func TestNoResultBackstopNilMetadata_TS04_37(t *testing.T) {
	c, err := core.NewToolUse("c1", "probe", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the no_result backstop: a call whose result slot was never
	// filled. The batch executor fills it with errorResult("no_result", ...).
	msg := errorResult(c, "no_result", "internal: the tool batch produced no result for this call")
	if msg.Metadata != nil {
		t.Fatal("no_result backstop must produce nil Metadata")
	}
	if !msg.IsError {
		t.Fatal("no_result backstop must be an error result")
	}
}
