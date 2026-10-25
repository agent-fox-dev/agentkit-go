package provider_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func wireTool(name string, s *schema.Schema) core.ToolWire {
	return core.ToolWire{Name: name, InputSchema: s}
}

func TestToolPrefixSyncReportsWhatChangedSinceTheLastTurn(t *testing.T) {
	a, b := schema.Object(schema.Prop("x", schema.String())), schema.Object()
	rebuilt := schema.Object(schema.Prop("x", schema.String()))
	var p provider.ToolPrefix

	steps := []struct {
		name        string
		tools       []core.ToolWire
		added       []string
		invalidated bool
		reason      string
		marshalled  int
	}{
		{"the first turn adds every tool", []core.ToolWire{wireTool("a", a), wireTool("b", b)}, []string{"a", "b"}, false, "", 2},
		{"an unchanged list marshals nothing", []core.ToolWire{wireTool("a", a), wireTool("b", b)}, nil, false, "", 0},
		{"a rebuilt identical schema is marshalled but keeps the prefix",
			[]core.ToolWire{wireTool("a", rebuilt), wireTool("b", b)}, nil, false, "", 1},
		{"an added tool does not invalidate",
			[]core.ToolWire{wireTool("a", rebuilt), wireTool("b", b), wireTool("c", b)}, []string{"c"}, false, "", 1},
		{"a changed schema invalidates",
			[]core.ToolWire{wireTool("a", schema.Object()), wireTool("b", b), wireTool("c", b)}, nil, true, "schema changed: a", 1},
		{"a removed tool invalidates", []core.ToolWire{wireTool("a", a), wireTool("b", b)}, nil, true, "", 1},
		{"removal is reported by name", []core.ToolWire{wireTool("a", a)}, nil, true, "tool removed: b", 0},
	}
	for _, s := range steps {
		raws, rep, err := p.Sync(s.tools)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if !slices.Equal(rep.Added, s.added) || rep.Invalidated != s.invalidated || rep.Marshalled != s.marshalled ||
			(s.reason != "" && rep.Reason != s.reason) {
			t.Errorf("%s: report = %+v", s.name, rep)
		}
		for i, tw := range s.tools {
			want, _ := json.Marshal(tw.InputSchema)
			if string(raws[i]) != string(want) {
				t.Errorf("%s: schema %d = %s, want %s", s.name, i, raws[i], want)
			}
		}
	}
}

func TestANilToolPrefixMarshalsEveryTimeInTheGivenDialect(t *testing.T) {
	var p *provider.ToolPrefix
	tools := []core.ToolWire{wireTool("a", schema.Object()), wireTool("b", schema.Object())}
	dialect := func(*schema.Schema) (json.RawMessage, error) { return json.RawMessage(`"gemini"`), nil }
	for range 2 {
		raws, rep, err := p.SyncWith(tools, dialect)
		if err != nil || rep.Marshalled != 2 || rep.Invalidated || string(raws[1]) != `"gemini"` {
			t.Fatalf("SyncWith = %s, %+v, %v", raws, rep, err)
		}
	}
}

func TestToolPrefixSyncReturnsTheMarshallersError(t *testing.T) {
	boom := errors.New("boom")
	fail := func(*schema.Schema) (json.RawMessage, error) { return nil, boom }
	tools := []core.ToolWire{wireTool("a", schema.Object())}
	for _, p := range []*provider.ToolPrefix{nil, {}} {
		if _, _, err := p.SyncWith(tools, fail); !errors.Is(err, boom) {
			t.Errorf("prefix %v: err = %v, want the marshaller's", p, err)
		}
	}
}

func TestSplitDeferredToolsDefersOnlyToolsAddedBeforeTheirFirstUse(t *testing.T) {
	added := func(names ...string) core.ToolResultMessage {
		return core.ToolResultMessage{ToolUseID: "x", AddedToolNames: names}
	}
	used := func(name string) core.AssistantMessage {
		return core.AssistantMessage{Content: core.Content{core.ToolUseBlock{ID: "x", Name: name, Input: json.RawMessage(`{}`)}}}
	}
	tools := []core.ToolWire{wireTool("base", nil), wireTool("late", nil), wireTool("known", nil)}
	history := core.Messages{
		used("known"),
		added("late", "known"), // known was used before the marker, so it stays immediate
		used("late"),           // use after the marker does not un-defer
	}
	s := provider.SplitDeferredTools(tools, history)
	if s.Promoted || len(s.Immediate) != 2 || s.Immediate[0].Name != "base" || s.Immediate[1].Name != "known" {
		t.Errorf("immediate = %+v, promoted = %v", s.Immediate, s.Promoted)
	}
	if !s.IsDeferred("late") || s.IsDeferred("base") {
		t.Errorf("deferred = %+v, want late only", s.Deferred)
	}

	// With every tool deferred there is no prefix to anchor: all are promoted.
	all := provider.SplitDeferredTools(tools[1:2], core.Messages{added("late")})
	if !all.Promoted || len(all.Immediate) != 1 || len(all.Deferred) != 0 {
		t.Errorf("all deferred = %+v, want them promoted", all)
	}
}
