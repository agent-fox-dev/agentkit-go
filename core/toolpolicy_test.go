package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestToolPolicyResolveAppliesItsFieldsUniformlyToBuiltinAndCustomTools(t *testing.T) {
	reg := []Tool{{Name: "read", Builtin: true}, {Name: "write", Builtin: true}, {Name: "exec", Builtin: true}}
	mine := []Tool{{Name: "mine"}}
	for _, tc := range []struct {
		name   string
		policy ToolPolicy
		want   string
	}{
		{"the zero policy is the registered set", ToolPolicy{}, "read,write,exec"},
		{"custom tools follow the registered ones", ToolPolicy{CustomTools: mine}, "read,write,exec,mine"},
		{`NoTools "all" disables custom tools too`, ToolPolicy{NoTools: NoToolsAll, CustomTools: mine, ToolNames: []string{"mine"}}, ""},
		{`NoTools "builtin" leaves custom tools`, ToolPolicy{NoTools: NoToolsBuiltin, CustomTools: mine}, "mine"},
		{"ToolNames constrains custom tools", ToolPolicy{ToolNames: []string{"read"}, CustomTools: mine}, "read"},
		{"an empty ToolNames allows nothing", ToolPolicy{ToolNames: []string{}}, ""},
		{"ExcludeTools applies to custom tools", ToolPolicy{ExcludeTools: []string{"mine", "write"}, CustomTools: mine}, "read,exec"},
		{"Tools bypasses everything", ToolPolicy{Tools: []Tool{{Name: "only"}}, ToolNames: []string{"read"}, NoTools: NoToolsAll}, "only"},
		{"an empty Tools means no tools", ToolPolicy{Tools: []Tool{}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(names(tc.policy.Resolve(reg)), ","); got != tc.want {
				t.Fatalf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

// The tool list is part of the cached prompt prefix, so an override must not reorder it.
func TestToolPolicyResolveOverridesABuiltinInPlace(t *testing.T) {
	reg := []Tool{{Name: "a", Builtin: true}, {Name: "read", Builtin: true, Description: "builtin"}, {Name: "z", Builtin: true}}
	got := ToolPolicy{CustomTools: []Tool{{Name: "read", Description: "custom"}}}.Resolve(reg)
	if len(got) != 3 || got[1].Name != "read" || got[1].Description != "custom" {
		t.Fatalf("override did not happen in place: %+v", got)
	}
}

func TestToolPolicyResolveFiltersWhatEachToolReaches(t *testing.T) {
	c1, c2 := Tool{Name: "c1"}, Tool{Name: "c2"}
	inner := Tool{Name: "inner", ReachableTools: []Tool{c1, c2}}
	wrap := Tool{Name: "wrap", ReachableTools: []Tool{c1, c2, inner}}
	registered := []Tool{wrap}

	resolved := ToolPolicy{ExcludeTools: []string{"c2"}}.Resolve(registered)
	if len(resolved) != 1 || !reflect.DeepEqual(names(resolved[0].ReachableTools), []string{"c1", "inner"}) ||
		!reflect.DeepEqual(names(resolved[0].ReachableTools[1].ReachableTools), []string{"c1"}) {
		t.Fatalf("resolved = %+v, want wrap reaching c1 and inner, inner reaching c1", resolved)
	}
	if got := names(registered[0].ReachableTools); !reflect.DeepEqual(got, []string{"c1", "c2", "inner"}) {
		t.Fatalf("the registered wrapper was modified: %v", got)
	}
	if allow := (ToolPolicy{ToolNames: []string{"wrap", "c2"}}).Resolve(registered); len(allow) != 1 ||
		!reflect.DeepEqual(names(allow[0].ReachableTools), []string{"c2"}) {
		t.Fatalf("allowlisted = %+v", allow)
	}
	mixed := Tool{Name: "mixed", ReachableTools: []Tool{{Name: "bi", Builtin: true}, c1}}
	if nb := (ToolPolicy{NoTools: NoToolsBuiltin}).Resolve([]Tool{mixed}); len(nb) != 1 ||
		!reflect.DeepEqual(names(nb[0].ReachableTools), []string{"c1"}) {
		t.Fatalf("NoTools builtin = %+v", nb)
	}
}

func TestToolPolicyResolveDropsAWrapperLeftReachingNothingButKeepsALeaf(t *testing.T) {
	wrap := Tool{Name: "wrap", ReachableTools: []Tool{{Name: "c1"}}}
	outer := Tool{Name: "outer", ReachableTools: []Tool{wrap}}
	got := ToolPolicy{ExcludeTools: []string{"c1"}}.Resolve([]Tool{wrap, outer, {Name: "leaf"}})
	if !reflect.DeepEqual(names(got), []string{"leaf"}) || len(got[0].ReachableTools) != 0 {
		t.Fatalf("resolved = %+v, want only the leaf", got)
	}
}
