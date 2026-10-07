package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/jsonx"
)

// TS-04-51: A session log written before this change loads with nil Metadata
// and re-encodes byte-identically, and new metadata stays losslessly readable
// by an older build.
func TestPreChangeLogLoadsWithNilMetadataAndReencodesIdentically_TS04_51(t *testing.T) {
	// The pre-change tool_result line from the golden (no metadata key).
	const preToolResult = `{"role":"tool_result","tool_use_id":"call_1","tool_name":"find_files","content":[{"type":"text","text":"{\"ok\":true,\"data\":{\"entries\":[\"main.go\"]}}"}]}`

	// Decode the pre-change line.
	m, err := DecodeMessage(json.RawMessage(preToolResult))
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	tr := m.(core.ToolResultMessage)
	if tr.Metadata != nil {
		t.Fatalf("pre-change tool result decoded with non-nil Metadata: %+v", tr.Metadata)
	}

	// Re-encode must produce the same bytes.
	b, err := EncodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != preToolResult {
		t.Fatalf("re-encode mismatch:\ngot:  %s\nwant: %s", b, preToolResult)
	}

	// A full pre-change session log round-trips byte-identically.
	const preLog = `{"type":"session","version":1,"id":"sess-1","timestamp":"2024-03-01T12:00:00Z","cwd":"/work"}
{"id":"e1","parent_id":"","type":"message","timestamp":"2024-03-01T12:00:00Z","message":{"role":"tool_result","tool_use_id":"call_1","tool_name":"find_files","content":[{"type":"text","text":"hello"}]}}
`
	path := writeLog(t, strings.Split(strings.TrimSuffix(preLog, "\n"), "\n")...)
	loaded := mustLoad(t, path)
	for _, e := range loaded.Entries() {
		if e.Message != nil {
			if tr, ok := e.Message.Message.(core.ToolResultMessage); ok {
				if tr.Metadata != nil {
					t.Fatalf("loaded pre-change tool result has non-nil Metadata: %+v", tr.Metadata)
				}
			}
		}
	}

	// Now test that new metadata is a top-level key and survives an older build.
	ec := 0
	newMsg := core.ToolResultMessage{
		ToolUseID: "call_1",
		ToolName:  "probe",
		Content:   core.Content{core.TextBlock{Text: "x"}},
		Metadata:  &core.ToolMetadata{ExitCode: &ec, Outcome: "ok"},
		Timestamp: fixedTime,
	}
	newBytes, err := EncodeMessage(newMsg)
	if err != nil {
		t.Fatal(err)
	}

	// metadata must be a top-level key of the tool_result object.
	v, err := jsonx.DecodeOrdered(newBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.Object.Get("metadata"); !ok {
		t.Fatal("metadata is not a top-level key in the new encoding")
	}

	// Simulate an older build that does not know "metadata": decode with
	// toolResultKnown minus "metadata".
	oldKnown := make([]string, 0, len(toolResultKnown))
	for _, k := range toolResultKnown {
		if k != "metadata" {
			oldKnown = append(oldKnown, k)
		}
	}

	// Decode the new bytes, but compute Unknown using the old known list.
	decoded, err := DecodeMessage(newBytes)
	if err != nil {
		t.Fatal(err)
	}
	// The current build decodes metadata properly. Now simulate old build:
	// re-decode with rest() using oldKnown.
	oldUnknown := rest(v.Object, oldKnown...)
	if oldUnknown.Index("metadata") < 0 {
		t.Fatal("with old known list, metadata should land in Unknown")
	}

	// An old build would re-encode with appendRest, preserving the metadata key.
	// Verify the decoded message re-encodes to the same bytes.
	reencoded, err := EncodeMessage(decoded)
	if err != nil {
		t.Fatal(err)
	}
	// The current build knows metadata, so it re-encodes it properly.
	if !strings.Contains(string(reencoded), `"metadata":{`) {
		t.Fatalf("re-encoded does not contain metadata: %s", reencoded)
	}
}
