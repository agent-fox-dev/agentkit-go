package tooldigest_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func base() core.Tool {
	return core.Tool{
		Name: "read", Description: "reads a file",
		InputSchema:      schema.Object(schema.Prop("path", schema.String("the path"))),
		OutputSchema:     schema.Array(schema.Int()),
		PromptGuidelines: []string{"one", "two"},
	}
}

func builtTools(t *testing.T) []core.Tool {
	t.Helper()
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestDigestIsASHA256ThatACopyShares(t *testing.T) {
	d := tooldigest.Digest(base())
	if len(d) != 32 {
		t.Fatalf("digest has %d bytes, want a SHA-256", len(d))
	}
	if !bytes.Equal(d, tooldigest.Digest(base())) {
		t.Error("two equal tools have different digests")
	}
}

func TestDigestChangesWithEachDefinitionField(t *testing.T) {
	want := tooldigest.Digest(base())
	for name, change := range map[string]func(*core.Tool){
		"Name":             func(t *core.Tool) { t.Name += "~" },
		"Description":      func(t *core.Tool) { t.Description += "~" },
		"InputSchema":      func(t *core.Tool) { t.InputSchema = schema.Object(schema.Prop("other", t.InputSchema)) },
		"OutputSchema":     func(t *core.Tool) { t.OutputSchema = nil },
		"PromptGuidelines": func(t *core.Tool) { t.PromptGuidelines = append(t.PromptGuidelines, "") },
		"guideline split":  func(t *core.Tool) { t.PromptGuidelines = []string{"onetwo"} },
		"Builtin":          func(t *core.Tool) { t.Builtin = true },
		"ExecutionMode":    func(t *core.Tool) { t.ExecutionMode = 1 },
		"field boundary":   func(t *core.Tool) { t.Name, t.Description = "rea", "dreads a file" },
	} {
		tl := base()
		change(&tl)
		if bytes.Equal(want, tooldigest.Digest(tl)) {
			t.Errorf("changing %s left the digest unchanged", name)
		}
	}
}

func TestDigestIgnoresFieldsOutsideTheDefinition(t *testing.T) {
	tl := base()
	tl.Label = "label~"
	tl.Execute = func(context.Context, json.RawMessage) core.ToolResult { return core.ToolResult{} }
	tl.Handler = func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
	tl.PrepareArguments = func(m map[string]any) map[string]any { return m }
	if !bytes.Equal(tooldigest.Digest(base()), tooldigest.Digest(tl)) {
		t.Error("a label or a function changed the digest")
	}
}

func TestDigestAgreesBetweenTwoBuildsOfTheTools(t *testing.T) {
	h, g := builtTools(t), builtTools(t)
	for i := range h {
		if !bytes.Equal(tooldigest.Digest(h[i]), tooldigest.Digest(g[i])) {
			t.Errorf("%s: digest differs between two tools.For builds", h[i].Name)
		}
	}
}

// testdata/digests.json holds the digests agents already released advertise;
// a host must keep computing the same ones to accept them.
func TestDigestOfTheBuiltInToolsIsUnchanged(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "digests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	ts := builtTools(t)
	if len(ts) != len(recorded) {
		t.Fatalf("For returned %d tools, %d digests were recorded", len(ts), len(recorded))
	}
	for i, tl := range ts {
		if tl.Name != recorded[i].Name {
			t.Errorf("tool %d is %s, recorded %s", i, tl.Name, recorded[i].Name)
			continue
		}
		if got := hex.EncodeToString(tooldigest.Digest(tl)); got != recorded[i].Digest {
			t.Errorf("%s: digest %s, recorded %s", tl.Name, got, recorded[i].Digest)
		}
	}
}
