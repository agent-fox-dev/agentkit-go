package session

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/jsonx"
)

// TS-04-47: The session codec writes metadata after usage and before timestamp,
// with tag-named keys in declaration order.
func TestCodecWritesMetadataAfterUsageBeforeTimestamp_TS04_47(t *testing.T) {
	zero := 0
	full := core.ToolResultMessage{
		ToolUseID: "call_1",
		ToolName:  "find_files",
		Content:   core.Content{core.TextBlock{Text: "hello"}},
		IsError:   true,
		AddedToolNames: []string{"grep"},
		Usage: &core.Usage{InputTokens: 1},
		Timestamp: fixedTime,
		Metadata: &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: "bytes",
			TotalBytes:  1024,
			TotalLines:  42,
			SpillPath:   "/tmp/spill.log",
			DurationMS:  99,
			ExitCode:    &zero,
			Outcome:     "ok",
			LineEnding:  "crlf",
		},
	}

	// Encode the full message.
	b, err := EncodeMessage(full)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}

	// Decode as ordered object to check key order.
	v, err := jsonx.DecodeOrdered(b)
	if err != nil {
		t.Fatalf("DecodeOrdered: %v", err)
	}
	if v.Kind != jsonx.KindObject {
		t.Fatal("encoded message is not an object")
	}

	// Check top-level key order.
	var topKeys []string
	for _, m := range v.Object {
		topKeys = append(topKeys, m.Key)
	}
	wantTopKeys := []string{"role", "tool_use_id", "tool_name", "content", "is_error",
		"added_tool_names", "usage", "metadata", "timestamp"}
	if !reflect.DeepEqual(topKeys, wantTopKeys) {
		t.Fatalf("top-level keys = %v, want %v", topKeys, wantTopKeys)
	}

	// Check metadata key order.
	mdVal, ok := v.Object.Get("metadata")
	if !ok || mdVal.Kind != jsonx.KindObject {
		t.Fatal("metadata key missing or not an object")
	}
	var mdKeys []string
	for _, m := range mdVal.Object {
		mdKeys = append(mdKeys, m.Key)
	}
	wantMDKeys := []string{"truncated", "truncated_by", "total_bytes", "total_lines",
		"spill_path", "duration_ms", "exit_code", "outcome", "line_ending"}
	if !reflect.DeepEqual(mdKeys, wantMDKeys) {
		t.Fatalf("metadata keys = %v, want %v", mdKeys, wantMDKeys)
	}

	// Metadata{ExitCode: &0} encodes as "metadata":{"exit_code":0}.
	ecOnly := full
	ecOnly.Metadata = &core.ToolMetadata{ExitCode: &zero}
	b2, err := EncodeMessage(ecOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), `"metadata":{"exit_code":0}`) {
		t.Fatalf("ExitCode-only encoding = %s, want to contain \"metadata\":{\"exit_code\":0}", b2)
	}

	// Metadata{Outcome: "ok"} encodes as "metadata":{"outcome":"ok"}.
	outcomeOnly := full
	outcomeOnly.Metadata = &core.ToolMetadata{Outcome: "ok"}
	b3, err := EncodeMessage(outcomeOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b3), `"metadata":{"outcome":"ok"}`) {
		t.Fatalf("Outcome-only encoding = %s, want to contain \"metadata\":{\"outcome\":\"ok\"}", b3)
	}

	// Metadata nil → no metadata key.
	noMD := full
	noMD.Metadata = nil
	b4, err := EncodeMessage(noMD)
	if err != nil {
		t.Fatal(err)
	}
	v4, err := jsonx.DecodeOrdered(b4)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v4.Object.Get("metadata"); ok {
		t.Fatal("nil Metadata should produce no metadata key")
	}
}

// TS-04-49: Encoding and decoding a tool result round-trips its Metadata exactly
// and re-encodes to the same bytes.
func TestMetadataRoundTripProperty_TS04_49(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < 1000; i++ {
		m := randomToolResultMessage(rng)

		b, err := EncodeMessage(m)
		if err != nil {
			t.Fatalf("iter %d: EncodeMessage: %v", i, err)
		}

		decoded, err := DecodeMessage(b)
		if err != nil {
			t.Fatalf("iter %d: DecodeMessage: %v", i, err)
		}
		dm := decoded.(core.ToolResultMessage)

		// nil stays nil.
		if (m.Metadata == nil) != (dm.Metadata == nil) {
			t.Fatalf("iter %d: Metadata nil mismatch: orig=%v decoded=%v", i, m.Metadata, dm.Metadata)
		}
		if m.Metadata != nil {
			if !reflect.DeepEqual(*dm.Metadata, *m.Metadata) {
				t.Fatalf("iter %d: Metadata mismatch:\norig:    %+v\ndecoded: %+v", i, *m.Metadata, *dm.Metadata)
			}
		}

		// Re-encode must produce the same bytes.
		b2, err := EncodeMessage(dm)
		if err != nil {
			t.Fatalf("iter %d: re-EncodeMessage: %v", i, err)
		}
		if string(b) != string(b2) {
			t.Fatalf("iter %d: re-encode mismatch:\nfirst:  %s\nsecond: %s", i, b, b2)
		}
	}
}

func randomToolResultMessage(rng *rand.Rand) core.ToolResultMessage {
	m := core.ToolResultMessage{
		ToolUseID: "call_1",
		ToolName:  "probe",
		Content:   core.Content{core.TextBlock{Text: "x"}},
		Timestamp: fixedTime,
	}

	// ~30% nil metadata.
	if rng.Intn(10) < 3 {
		return m
	}

	md := &core.ToolMetadata{}
	if rng.Intn(2) == 1 {
		md.Truncated = true
	}
	if rng.Intn(2) == 1 {
		md.TruncatedBy = []string{"bytes", "lines"}[rng.Intn(2)]
	}
	if rng.Intn(2) == 1 {
		md.TotalBytes = int64(rng.Intn(100000))
	}
	if rng.Intn(2) == 1 {
		md.TotalLines = int64(rng.Intn(10000))
	}
	if rng.Intn(2) == 1 {
		md.SpillPath = "/tmp/spill-" + randomString(rng, 8)
	}
	if rng.Intn(2) == 1 {
		md.DurationMS = int64(rng.Intn(5000))
	}
	// ExitCode: nil, &0, or &k.
	switch rng.Intn(3) {
	case 1:
		v := 0
		md.ExitCode = &v
	case 2:
		v := rng.Intn(256)
		md.ExitCode = &v
	}
	if rng.Intn(2) == 1 {
		md.Outcome = []string{"ok", "exit", "signal", "timeout", "abort"}[rng.Intn(5)]
	}
	if rng.Intn(2) == 1 {
		md.LineEnding = []string{"lf", "crlf"}[rng.Intn(2)]
	}

	// If all fields are zero, it's empty → treated as nil. Make it non-empty.
	if md.IsEmpty() {
		md.Outcome = "ok"
	}

	m.Metadata = md
	return m
}

func randomString(rng *rand.Rand, n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rng.Intn(len(letters))]
	}
	return string(b)
}

// TS-04-50: Absent or all-unknown metadata decodes as nil, unknown nested keys
// are dropped, and metadata never lands in Unknown.
func TestAbsentOrUnknownMetadataDecodesAsNil_TS04_50(t *testing.T) {
	base := func(md string) json.RawMessage {
		s := `{"role":"tool_result","tool_use_id":"c1","tool_name":"probe","content":[{"type":"text","text":"x"}]`
		if md != "" {
			s += "," + md
		}
		s += "}"
		return json.RawMessage(s)
	}

	cases := []struct {
		name    string
		raw     json.RawMessage
		wantMD  *core.ToolMetadata
	}{
		{"no_key", base(""), nil},
		{"empty_object", base(`"metadata":{}`), nil},
		{"all_unknown", base(`"metadata":{"future":1}`), nil},
		{"mixed", base(`"metadata":{"outcome":"exit","future":1}`), &core.ToolMetadata{Outcome: "exit"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := DecodeMessage(tc.raw)
			if err != nil {
				t.Fatalf("DecodeMessage: %v", err)
			}
			tr := m.(core.ToolResultMessage)

			if tc.wantMD == nil {
				if tr.Metadata != nil {
					t.Fatalf("Metadata = %+v, want nil", tr.Metadata)
				}
			} else {
				if tr.Metadata == nil {
					t.Fatal("Metadata is nil, want non-nil")
				}
				if !reflect.DeepEqual(*tr.Metadata, *tc.wantMD) {
					t.Fatalf("Metadata = %+v, want %+v", *tr.Metadata, *tc.wantMD)
				}
			}

			// metadata must never land in Unknown.
			if tr.Unknown.Index("metadata") >= 0 {
				t.Fatal("metadata key found in Unknown")
			}
		})
	}

	// The mixed case re-encodes without the unknown "future" key.
	t.Run("mixed_reencode", func(t *testing.T) {
		m, err := DecodeMessage(cases[3].raw)
		if err != nil {
			t.Fatal(err)
		}
		b, err := EncodeMessage(m)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, `"metadata":{"outcome":"exit"}`) {
			t.Fatalf("re-encoded = %s, want metadata with only outcome", s)
		}
		if strings.Contains(s, "future") {
			t.Fatalf("re-encoded still contains 'future': %s", s)
		}
	})
}

// TS-04-52: The event JSON union carries the same metadata object as the session
// log for ToolResultEvent, TurnEndEvent and AgentDoneEvent.
func TestEventJSONCarriesMetadata_TS04_52(t *testing.T) {
	two := 2
	m := core.ToolResultMessage{
		ToolUseID: "call_1",
		ToolName:  "probe",
		Content:   core.Content{core.TextBlock{Text: "x"}},
		Metadata: &core.ToolMetadata{
			ExitCode:   &two,
			Outcome:    "exit",
			DurationMS: 8,
		},
		Timestamp: fixedTime,
	}

	// Encode the message via session codec.
	msgBytes, err := EncodeMessage(m)
	if err != nil {
		t.Fatal(err)
	}

	// Extract the metadata substring from the message encoding.
	wantMD := extractMetadataJSON(t, msgBytes)

	events := []struct {
		name  string
		event core.Event
	}{
		{"ToolResultEvent", core.ToolResultEvent{Message: m}},
		{"TurnEndEvent", core.TurnEndEvent{ToolResults: []core.ToolResultMessage{m}}},
		{"AgentDoneEvent", core.AgentDoneEvent{Result: core.RunResult{Messages: core.Messages{m}}}},
	}

	for _, tc := range events {
		t.Run(tc.name, func(t *testing.T) {
			b, err := EventJSON(tc.event)
			if err != nil {
				t.Fatalf("EventJSON: %v", err)
			}

			// The event JSON must contain the message encoding.
			s := string(b)
			if !strings.Contains(s, string(msgBytes)) {
				t.Fatalf("event JSON does not contain the message encoding.\nevent: %s\nmsg:   %s", s, msgBytes)
			}

			// The metadata in the event must match the session codec's.
			gotMD := extractMetadataJSON(t, b)
			if gotMD != wantMD {
				t.Fatalf("metadata mismatch:\nevent: %s\nwant:  %s", gotMD, wantMD)
			}
		})
	}
}

// extractMetadataJSON finds the "metadata":{...} substring in a JSON blob.
func extractMetadataJSON(t *testing.T, b json.RawMessage) string {
	t.Helper()
	s := string(b)
	idx := strings.Index(s, `"metadata":{`)
	if idx < 0 {
		t.Fatalf("no metadata key found in: %s", s)
	}
	// Find the matching closing brace.
	start := idx + len(`"metadata":`)
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[idx : i+1]
			}
		}
	}
	t.Fatalf("unmatched braces in metadata: %s", s[idx:])
	return ""
}

// Ensure fixedTime is available (it's defined in store_test.go, same package).
var _ = fixedTime
