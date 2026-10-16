package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// preSpec17Guard is guard's exported API before spec 17, which changes only
// its doc comments, plus Allow, which spec 22 added on purpose
// (docs/errata/17_sandbox_podman.md).
var preSpec17Guard = []string{
	"func Allow(programs ...string) core.BeforeToolCall",
	"func AllowAll(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision",
	"func Check(argv []string, o Options) Decision",
	"func IsShellTool(name string) bool",
	"func Restricted(o Options) core.BeforeToolCall",
	"type Decision struct { Block bool Reason string Terminate bool }",
	"type Options struct { AllowedPrograms []string AllowShellOperators bool AllowEnvPrefixes bool BlockedTools []string TerminateOnBlock bool }",
	"var ShellToolNames",
}

// TS-17-61: the exported surfaces of agentkit, core, tools and codemode
// still equal the golden, and guard's exported declarations are unchanged.
// 17-REQ-10.3. The golden is no longer pinned to spec 13's hash (see
// TS-14-44).
func TestExportedAPIUnchanged_TS17_61(t *testing.T) {
	root := repoRoot(t)
	want, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	for _, pkg := range apiPackages {
		got.WriteString("== " + pkg + "\n")
		for _, line := range exportedAPI(t, filepath.Join(root, pkg)) {
			got.WriteString(line + "\n")
		}
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("the exported API differs from the golden:\n%s", got.String())
	}
	if g := exportedAPI(t, filepath.Join(root, "guard")); !slices.Equal(g, preSpec17Guard) {
		t.Errorf("guard's exported API changed:\n%q\nwant\n%q", g, preSpec17Guard)
	}
}
