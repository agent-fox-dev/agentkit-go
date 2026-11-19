package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestTheListingHasTheFourKeysAndDefaultSchemas(t *testing.T) {
	full := core.Tool{Name: "full", Description: "with schemas",
		InputSchema:  schema.Object(schema.Prop("path", schema.String())),
		OutputSchema: schema.Object(schema.Prop("ok", schema.Bool()))}
	bare := core.Tool{Name: "bare", Description: "without"}
	defs, err := definitions([]core.Tool{full, bare})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d entries, want 2", len(got))
	}
	for _, d := range got {
		var keys []string
		for k := range d {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"description", "input_schema", "name", "output_schema"}) {
			t.Errorf("keys = %v", keys)
		}
	}
	in, _ := json.Marshal(full.InputSchema)
	out, _ := json.Marshal(full.OutputSchema)
	if string(got[0]["input_schema"]) != string(in) || string(got[0]["output_schema"]) != string(out) {
		t.Errorf("full: schemas = %s, %s", got[0]["input_schema"], got[0]["output_schema"])
	}
	if string(got[1]["input_schema"]) != `{"type":"object"}` || string(got[1]["output_schema"]) != "null" {
		t.Errorf("bare: schemas = %s, %s", got[1]["input_schema"], got[1]["output_schema"])
	}

	res := run(t, "tools", "--dir", sampleTree(t))
	if res.exit != 0 {
		t.Fatalf("exit %d, stderr %s", res.exit, res.stderr)
	}
	parseListing(t, res.stdout)
}
