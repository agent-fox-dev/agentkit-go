package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// TS-12-38 (smoke, 12-PATH-1): an authored schema keeps its property order
// through Parse, and arguments that satisfy it reach the handler as the
// model's own bytes.
func TestSmokeParsedSchemaValidatesArguments_TS12_38(t *testing.T) {
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"query":{"type":"string"},"filter":{"type":"string"},"limit":{"type":"integer","maximum":10}},"required":["query"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.PropertyOrder, []string{"query", "filter", "limit"}) {
		t.Fatalf("PropertyOrder = %v", s.PropertyOrder)
	}
	tool := core.Tool{Name: "search", InputSchema: s}
	in := json.RawMessage(`{"query":"test", "filter":"active","limit":5}`)
	prep, err := core.PrepareArguments(tool, core.ToolUseBlock{ID: "t1", Name: "search", Input: in})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prep.Raw, in) || prep.Args["query"] != "test" {
		t.Fatalf("prepared = %s, %v", prep.Raw, prep.Args)
	}
	// The parsed constraints hold too.
	if _, err := core.PrepareArguments(tool, core.ToolUseBlock{ID: "t2", Name: "search", Input: json.RawMessage(`{"query":"x","limit":50,"extra":1}`)}); err == nil ||
		!strings.Contains(err.Error(), "at most 10") || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("PrepareArguments = %v, want the bound and the closed object enforced", err)
	}
}

// TS-12-39 (smoke, 12-PATH-2): a run_command call goes through Restricted,
// which asks Check about its argv.
func TestSmokeRestrictedDelegatesToCheck_TS12_39(t *testing.T) {
	opts := guard.Options{AllowedPrograms: []string{"ls", "git"}}
	hook := guard.Restricted(opts)
	call := func(argv ...any) core.BeforeToolCallDecision {
		return hook(context.Background(), core.BeforeToolCallContext{ToolName: "run_command", Arguments: map[string]any{"argv": argv}})
	}
	if d := call("ls", "-la"); d.Block {
		t.Fatalf("ls was blocked: %+v", d)
	}
	if guard.Check([]string{"ls", "-la"}, opts).Block {
		t.Fatal("Check and Restricted disagree")
	}
	want := guard.Check([]string{"rm", "-rf", "/"}, opts)
	if d := call("rm", "-rf", "/"); !d.Block || d.Reason != "guard.Restricted: "+want.Reason {
		t.Fatalf("rm = %+v, want Check's reason %q behind the adapter's prefix", d, want.Reason)
	}
}

// TS-12-40 (smoke, 12-PATH-3): the assembled prompt goes out in a Request
// and a ProviderClient streams the turn back on a channel.
func TestSmokePromptAndProviderStream_TS12_40(t *testing.T) {
	tools := []core.Tool{{Name: "read_file", PromptGuidelines: []string{"Read before editing."}}, {Name: "execute"}}
	sys := prompt.Build("", tools)
	if !strings.HasPrefix(sys, prompt.BaseInstructions) || !strings.Contains(sys, "Read before editing.") {
		t.Fatalf("prompt:\n%s", sys)
	}
	var client core.ProviderClient = faux.New(faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")))
	ch, err := client.Stream(context.Background(), core.Request{
		System: []core.ContentBlock{core.TextBlock{Text: sys}},
		Tools:  core.ToolWires(tools),
	})
	if err != nil {
		t.Fatal(err)
	}
	var final *core.AssistantMessage
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if end, ok := ev.Event.(core.MessageEndEvent); ok {
			m := end.Message
			final = &m
		}
	}
	if final == nil || final.Content.Text() != "done" || final.StopReason != core.StopReasonStop {
		t.Fatalf("final message = %+v", final)
	}
}
