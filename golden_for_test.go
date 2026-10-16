package agentkit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-33 (the golden clause): the canonical request, carrying the real
// find_files OutputSchema from tools.For, is projected through core.ToolWires
// and the Anthropic body still equals testdata/golden/request_anthropic.json.
func TestGoldenRequestIsStillProducedWithForTools_TS20_33(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out *schema.Schema
	for _, tl := range all {
		if tl.Name == "find_files" {
			out = tl.OutputSchema
		}
	}
	if out == nil {
		t.Fatal("find_files declares no OutputSchema")
	}
	req := canonicalRequest(t)
	req.Tools = core.ToolWires(canonicalTools(out))
	cases := goldenCasesFor(t, req)
	if len(cases) != 1 {
		t.Fatalf("%d providers, want 1", len(cases))
	}
	for _, tc := range cases {
		want, err := os.ReadFile(filepath.Join("testdata", "golden", "request_"+tc.name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, []byte(tc.body)) {
			t.Errorf("%s request body differs from its golden with tools.For's find_files OutputSchema", tc.name)
		}
	}
}
