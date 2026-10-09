package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestToolPolicyResolution(t *testing.T) {
	builtin := func(n string) Tool {
		return Tool{Name: n, Description: n, Builtin: true, InputSchema: schema.Object(),
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }}
	}
	custom := func(n string) Tool {
		return Tool{Name: n, Description: n, InputSchema: schema.Object(),
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }}
	}
	reg := []Tool{builtin("read"), builtin("write"), builtin("exec")}

	names := func(ts []Tool) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Name)
		}
		return strings.Join(out, ",")
	}

	// The four non-obvious consequences of REQ-TOOL-10, each its own row.
	cases := []struct {
		name   string
		policy ToolPolicy
		want   string
	}{
		{`NoTools "all" disables CUSTOM tools too`,
			ToolPolicy{NoTools: NoToolsAll, CustomTools: []Tool{custom("mine")}}, ""},
		{`NoTools "builtin" leaves custom tools alive`,
			ToolPolicy{NoTools: NoToolsBuiltin, CustomTools: []Tool{custom("mine")}}, "mine"},
		{`a ToolNames allowlist constrains custom tools`,
			ToolPolicy{ToolNames: []string{"read"}, CustomTools: []Tool{custom("mine")}}, "read"},
		{`ExcludeTools applies to custom tools`,
			ToolPolicy{ExcludeTools: []string{"mine"}, CustomTools: []Tool{custom("mine")}}, "read,write,exec"},
		{`Tools non-nil bypasses everything`,
			ToolPolicy{Tools: []Tool{custom("only")}, ToolNames: []string{"read"}, NoTools: NoToolsAll}, "only"},
		{`Tools non-nil but EMPTY means no tools, deliberately`,
			ToolPolicy{Tools: []Tool{}}, ""},
		{`nil ToolNames means the default set`,
			ToolPolicy{}, "read,write,exec"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.policy.Resolve(reg)); got != tc.want {
				t.Fatalf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCustomToolOverridesBuiltinInPlace: overriding must not reorder the tool
// list, because the tool list is part of the cached prompt prefix.
func TestCustomToolOverridesBuiltinInPlace(t *testing.T) {
	reg := []Tool{
		{Name: "a", Builtin: true}, {Name: "read", Builtin: true, Description: "builtin"}, {Name: "z", Builtin: true},
	}
	got := ToolPolicy{
		CustomTools: []Tool{{Name: "read", Description: "custom"}},
	}.Resolve(reg)
	if len(got) != 3 || got[1].Name != "read" || got[1].Description != "custom" {
		t.Fatalf("override did not happen in place: %+v", got)
	}
}

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

// TS-07-6: the transitive closure, deduplicated, in order of first
// appearance (depth first).
func TestReachableToolsClosure_TS07_6(t *testing.T) {
	d := Tool{Name: "D"}
	b := Tool{Name: "B", ReachableTools: []Tool{d}}
	c := Tool{Name: "C", ReachableTools: []Tool{d}}
	a := Tool{Name: "A", ReachableTools: []Tool{b, c}}
	if got, want := names(ReachableTools([]Tool{a})), []string{"A", "B", "D", "C"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReachableTools = %v, want %v", got, want)
	}
	leaf := Tool{Name: "leaf"}
	if got, want := names(ReachableTools([]Tool{leaf, a, c})), []string{"leaf", "A", "B", "D", "C"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReachableTools = %v, want %v", got, want)
	}
	if got := ReachableTools(nil); len(got) != 0 {
		t.Fatalf("ReachableTools(nil) = %v", got)
	}
}

// TS-07-8: the policy filters reachable tools too, recursively, without
// touching the registered tools.
func TestToolPolicyResolveFiltersReachableTools_TS07_8(t *testing.T) {
	c1, c2 := Tool{Name: "c1"}, Tool{Name: "c2"}
	inner := Tool{Name: "inner", ReachableTools: []Tool{c1, c2}}
	wrap := Tool{Name: "wrap", ReachableTools: []Tool{c1, c2, inner}}
	registered := []Tool{wrap}
	resolved := ToolPolicy{ExcludeTools: []string{"c2"}}.Resolve(registered)
	if len(resolved) != 1 || !reflect.DeepEqual(names(resolved[0].ReachableTools), []string{"c1", "inner"}) {
		t.Fatalf("resolved = %v / %v, want wrap reaching [c1 inner]", names(resolved), names(resolved[0].ReachableTools))
	}
	if got := names(resolved[0].ReachableTools[1].ReachableTools); !reflect.DeepEqual(got, []string{"c1"}) {
		t.Fatalf("inner reaches %v, want [c1]", got)
	}
	if got := names(registered[0].ReachableTools); !reflect.DeepEqual(got, []string{"c1", "c2", "inner"}) {
		t.Fatalf("the registered wrapper was modified: %v", got)
	}

	// ToolNames is an allowlist over reachable tools as well.
	allow := ToolPolicy{ToolNames: []string{"wrap", "c2"}}.Resolve(registered)
	if len(allow) != 1 || !reflect.DeepEqual(names(allow[0].ReachableTools), []string{"c2"}) {
		t.Fatalf("allowlisted = %v", allow)
	}
	// NoTools builtin removes built-in children.
	bi := Tool{Name: "bi", Builtin: true}
	mixed := Tool{Name: "mixed", ReachableTools: []Tool{bi, c1}}
	nb := ToolPolicy{NoTools: NoToolsBuiltin}.Resolve([]Tool{mixed})
	if len(nb) != 1 || !reflect.DeepEqual(names(nb[0].ReachableTools), []string{"c1"}) {
		t.Fatalf("NoTools builtin = %v", nb)
	}
}

// TS-07-9: a wrapper whose every reachable tool is filtered out is dropped,
// at any depth.
func TestToolPolicyResolveDropsEmptiedWrapper_TS07_9(t *testing.T) {
	c1 := Tool{Name: "c1"}
	wrap := Tool{Name: "wrap", ReachableTools: []Tool{c1}}
	if resolved := (ToolPolicy{ExcludeTools: []string{"c1"}}).Resolve([]Tool{wrap}); len(resolved) != 0 {
		t.Fatalf("resolved = %v, want the emptied wrapper dropped", names(resolved))
	}
	// An inner wrapper emptied by the filter is dropped from its parent,
	// which then empties and is dropped in turn.
	outer := Tool{Name: "outer", ReachableTools: []Tool{wrap}}
	if resolved := (ToolPolicy{ExcludeTools: []string{"c1"}}).Resolve([]Tool{outer, {Name: "keep"}}); !reflect.DeepEqual(names(resolved), []string{"keep"}) {
		t.Fatalf("resolved = %v, want [keep]", names(resolved))
	}
}

// TS-07-10: a leaf tool, which never reached anything, is kept.
func TestToolPolicyResolveKeepsLeafTools_TS07_10(t *testing.T) {
	leaf := Tool{Name: "leaf"}
	resolved := ToolPolicy{ToolNames: []string{"leaf"}}.Resolve([]Tool{leaf})
	if len(resolved) != 1 || resolved[0].Name != "leaf" || len(resolved[0].ReachableTools) != 0 {
		t.Fatalf("resolved = %+v, want [leaf]", resolved)
	}
	if resolved := (ToolPolicy{}).Resolve([]Tool{leaf}); len(resolved) != 1 {
		t.Fatalf("default policy dropped a leaf: %+v", resolved)
	}
}
