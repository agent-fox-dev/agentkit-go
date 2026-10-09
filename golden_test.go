package agentkit

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/prompt"
	"github.com/agentfox/agentkit-go/schema"
)

// NFR-TEST-08: byte-for-byte goldens for the artifacts assembled from many
// parts, where no single unit is wrong but the composed whole drifts.
//
// -update rewrites them. That flag is the danger the requirement names: "a
// golden regenerated from the output it exists to check is circular". The
// discipline is that a diff is REVIEWED, not blessed — the point of the
// assembled-prompt golden is precisely that a change to any tool's description
// shows up in a code review as a prompt diff.
var update = flag.Bool("update", false, "rewrite golden files")

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n\nRun `go test -run %s -update` and REVIEW the diff before "+
			"committing it.", err, t.Name())
	}
	if string(want) != got {
		t.Fatalf("%s drifted.\n\n--- want ---\n%s\n--- got ---\n%s\n\n"+
			"If the change is intended, run `go test -run %s -update` and review "+
			"the diff as part of the change.", name, want, got, t.Name())
	}
}

// ---- (b) the per-provider request body

// TestGoldenProviderRequestBodies pins the wire body the provider builds from
// one canonical request (NFR-TEST-06/08b).
//
// PROVENANCE (NFR-TEST-08.1) — and the honest limit of this file
//
//	goldens:   testdata/golden/request_anthropic.json
//	reference: AgentKit itself, captured through RequestOptions.OnPayload with
//	           no network and no API key. THIS IS NOT A VENDOR CAPTURE. These
//	           pin the request body against REGRESSION — they catch AgentKit
//	           changing what it sends — and say nothing about whether what it
//	           sends is what the vendor currently accepts.
//	version:   the working tree
//	command:   go test -run TestGoldenProviderRequestBodies -update .
func TestGoldenProviderRequestBodies(t *testing.T) {
	for _, tc := range goldenRequestCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			checkGolden(t, "request_"+tc.name+".json", tc.body)
		})
	}
}

// TestTheAssembledPromptReachesTheProvider. Everything above tests the
// assembler; this tests that the loop uses it. Until it did, PromptGuidelines
// was a field nothing read — a tool could declare guidance the model never saw.
func TestTheAssembledPromptReachesTheProvider(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil)
	if err := a.RegisterTool(core.Tool{
		Name: "widget", Description: "does a thing", InputSchema: schema.Object(),
		PromptGuidelines: []string{"Use widget for widget-shaped problems."},
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.OKResult(nil)
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if len(s.seen) == 0 {
		t.Fatal("the provider was never called")
	}
	sys := systemTextOf(t, s)
	if !strings.Contains(sys, "Use widget for widget-shaped problems.") {
		t.Fatalf("a registered tool's guideline never reached the provider.\nsystem = %q", sys)
	}
	if !strings.Contains(sys, prompt.BaseInstructions) {
		t.Fatalf("the built-in base instructions never reached the provider.\nsystem = %q", sys)
	}
}

// TestACustomPromptReachesTheProviderWithoutBuiltins is the same wiring for
// the other branch.
func TestACustomPromptReachesTheProviderWithoutBuiltins(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.SystemPrompt = "Only answer in haiku."
	})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	sys := systemTextOf(t, s)
	if sys != "Only answer in haiku." {
		t.Fatalf("a custom prompt must reach the provider alone; got %q", sys)
	}
}

func systemTextOf(t *testing.T, s *scripted) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.systems) == 0 {
		t.Fatal("the provider recorded no system prompt")
	}
	var b strings.Builder
	for _, blk := range s.systems[0] {
		if tb, ok := blk.(core.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}
