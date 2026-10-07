package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/jsonx"
)

// goldenMetadata is the one metadata object the session-log golden gained
// with tool metadata. TS-04-48 pins it as the golden's only change, so
// removing it gives back the golden exactly as it was before the change.
const goldenMetadata = `,"metadata":{"total_lines":1,"duration_ms":3}`

// preChangeGoldenLog returns testdata/golden/session_log.jsonl as of the
// commit before tool metadata: a log with no metadata key.
func preChangeGoldenLog(t *testing.T) []byte {
	t.Helper()
	golden, err := os.ReadFile(filepath.Join("..", "testdata", "golden", "session_log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(golden), goldenMetadata); n != 1 {
		t.Fatalf("golden holds %d copies of %s, want 1", n, goldenMetadata)
	}
	return []byte(strings.Replace(string(golden), goldenMetadata, "", 1))
}

// TS-04-51: A session log written before this change loads with nil Metadata
// and re-encodes byte-identically.
func TestPreChangeLogLoadsWithNilMetadataAndReencodesIdentically_TS04_51(t *testing.T) {
	pre := preChangeGoldenLog(t)
	lines := strings.Split(strings.TrimSuffix(string(pre), "\n"), "\n")

	loaded := LoadBytes("pre.jsonl", pre)
	if len(loaded.Repairs) != 0 {
		t.Fatalf("pre-change log loaded with repairs: %+v", loaded.Repairs)
	}

	header, err := EncodeHeader(loaded.Header)
	if err != nil {
		t.Fatal(err)
	}
	if string(header) != lines[0] {
		t.Fatalf("header re-encode mismatch:\ngot:  %s\nwant: %s", header, lines[0])
	}

	entries := loaded.Entries()
	if len(entries) != len(lines)-1 {
		t.Fatalf("loaded %d entries, want %d", len(entries), len(lines)-1)
	}
	toolResults := 0
	for i, e := range entries {
		if e.Message != nil {
			if tr, ok := e.Message.Message.(core.ToolResultMessage); ok {
				toolResults++
				if tr.Metadata != nil {
					t.Fatalf("pre-change tool result decoded with non-nil Metadata: %+v", tr.Metadata)
				}
			}
		}
		// Drop the verbatim bytes so the entry is rebuilt through the codec
		// rather than passed through.
		e.Raw = nil
		b, err := EncodeEntry(e)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != lines[i+1] {
			t.Fatalf("entry %d re-encode mismatch:\ngot:  %s\nwant: %s", i, b, lines[i+1])
		}
	}
	if toolResults != 1 {
		t.Fatalf("pre-change log holds %d tool results, want 1", toolResults)
	}
}

// decodeAsOldBuild decodes a tool_result the way a build without tool
// metadata does: the same decoder, with "metadata" missing from the known
// key list, so the key lands in Unknown.
func decodeAsOldBuild(t *testing.T, raw []byte) core.ToolResultMessage {
	t.Helper()
	v, err := jsonx.DecodeOrdered(raw)
	if err != nil {
		t.Fatal(err)
	}
	oldKnown := make([]string, 0, len(toolResultKnown))
	for _, k := range toolResultKnown {
		if k != "metadata" {
			oldKnown = append(oldKnown, k)
		}
	}
	m, err := DecodeMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	tr := m.(core.ToolResultMessage)
	tr.Metadata = nil
	tr.Unknown = rest(v.Object, oldKnown...)
	return tr
}

// TS-04-51: New metadata is a top-level key of the tool_result object, and a
// build without this change keeps it in Unknown and loses none of it.
//
// Byte-identity through an older build holds only for a message with no
// timestamp: REQ-9.1 writes metadata before timestamp, and an older build
// appends Unknown keys after its modelled ones, so it moves metadata behind
// timestamp. See docs/errata/04_runner_and_tool_metadata.md.
func TestNewMetadataSurvivesABuildWithoutIt_TS04_51(t *testing.T) {
	ec := 2
	md := &core.ToolMetadata{DurationMS: 7, ExitCode: &ec, Outcome: "exit"}
	cases := []struct {
		name      string
		timestamp time.Time
		new       string
		old       string
	}{
		{
			name: "no timestamp",
			new:  `{"role":"tool_result","tool_use_id":"call_1","tool_name":"execute","content":[{"type":"text","text":"x"}],"metadata":{"duration_ms":7,"exit_code":2,"outcome":"exit"}}`,
			old:  `{"role":"tool_result","tool_use_id":"call_1","tool_name":"execute","content":[{"type":"text","text":"x"}],"metadata":{"duration_ms":7,"exit_code":2,"outcome":"exit"}}`,
		},
		{
			name:      "timestamped",
			timestamp: fixedTime,
			new:       `{"role":"tool_result","tool_use_id":"call_1","tool_name":"execute","content":[{"type":"text","text":"x"}],"metadata":{"duration_ms":7,"exit_code":2,"outcome":"exit"},"timestamp":"2024-03-01T12:00:00Z"}`,
			old:       `{"role":"tool_result","tool_use_id":"call_1","tool_name":"execute","content":[{"type":"text","text":"x"}],"timestamp":"2024-03-01T12:00:00Z","metadata":{"duration_ms":7,"exit_code":2,"outcome":"exit"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := core.ToolResultMessage{
				ToolUseID: "call_1",
				ToolName:  "execute",
				Content:   core.Content{core.TextBlock{Text: "x"}},
				Metadata:  md,
				Timestamp: tc.timestamp,
			}
			newBytes, err := EncodeMessage(msg)
			if err != nil {
				t.Fatal(err)
			}
			if string(newBytes) != tc.new {
				t.Fatalf("new encoding:\ngot:  %s\nwant: %s", newBytes, tc.new)
			}

			old := decodeAsOldBuild(t, newBytes)
			if old.Unknown.Index("metadata") < 0 {
				t.Fatalf("an older build does not keep metadata in Unknown: %+v", old.Unknown)
			}
			oldBytes, err := EncodeMessage(old)
			if err != nil {
				t.Fatal(err)
			}
			if string(oldBytes) != tc.old {
				t.Fatalf("older build re-encoding:\ngot:  %s\nwant: %s", oldBytes, tc.old)
			}

			// Nothing is lost: this build reads the older build's bytes back
			// to the same message, and re-encodes them to the original bytes.
			back, err := DecodeMessage(json.RawMessage(oldBytes))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back.(core.ToolResultMessage).Metadata, md) {
				t.Fatalf("metadata after an older build: %+v, want %+v", back.(core.ToolResultMessage).Metadata, md)
			}
			again, err := EncodeMessage(back)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != tc.new {
				t.Fatalf("re-encoding after an older build:\ngot:  %s\nwant: %s", again, tc.new)
			}
		})
	}
}
