package core

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agentfox/agentkit-go/schema"
)

func noopHandler(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

// TS-07-1: a tool declares the tools it reaches and whether it terminates the
// run.
func TestReachableToolsAndTerminatingDeclared_TS07_1(t *testing.T) {
	child := Tool{Name: "child", Handler: noopHandler}
	parent := Tool{Name: "parent", ReachableTools: []Tool{child}, Terminating: true}
	if len(parent.ReachableTools) != 1 || parent.ReachableTools[0].Name != "child" {
		t.Fatalf("ReachableTools = %+v, want [child]", parent.ReachableTools)
	}
	if !parent.Terminating {
		t.Fatal("Terminating is not set")
	}
}

// TS-07-2: neither field reaches a provider: ToolWire has no such fields and
// its JSON carries none.
func TestReachableToolsAndTerminatingNotOnWire_TS07_2(t *testing.T) {
	in := schema.Object(schema.Prop("q", schema.String()))
	tl := Tool{Name: "wrapper", Description: "wrapper tool", InputSchema: in,
		ReachableTools: []Tool{{Name: "child", Handler: noopHandler}}, Terminating: true}
	for _, w := range append([]ToolWire{tl.Wire()}, ToolWires([]Tool{tl})...) {
		if w.Name != "wrapper" || w.Description != "wrapper tool" || w.InputSchema != in {
			t.Fatalf("wire = %+v", w)
		}
		typ := reflect.TypeOf(w)
		for _, f := range []string{"ReachableTools", "Terminating"} {
			if _, ok := typ.FieldByName(f); ok {
				t.Fatalf("ToolWire has a %s field", f)
			}
		}
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"ReachableTools", "reachable_tools", "Terminating", "terminating"} {
			if _, ok := m[k]; ok {
				t.Fatalf("wire JSON carries %q: %s", k, b)
			}
		}
	}
}
