package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// preSpec17Guard is guard's exported API before spec 17, which changes only
// its doc comments, plus Allow, which spec 22 added on purpose, and Commands
// with its three types, which the shell reading added on purpose
// (docs/errata/17_sandbox_podman.md).
var preSpec17Guard = []string{
	"func (e *SyntaxError) Error() string",
	"func Allow(programs ...string) core.BeforeToolCall",
	"func AllowAll(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision",
	"func Check(argv []string, o Options) Decision",
	"func Commands(line string) ([]Command, error)",
	"func IsShellTool(name string) bool",
	"func Restricted(o Options) core.BeforeToolCall",
	"type Command struct { Text string Assign []string Argv []string Redirects []Redirect }",
	"type Decision struct { Block bool Reason string Terminate bool }",
	"type Options struct { AllowedPrograms []string AllowShellOperators bool AllowEnvPrefixes bool BlockedTools []string TerminateOnBlock bool }",
	"type Redirect struct { FD string Op string Target string }",
	"type SyntaxError struct { Offset int Reason string }",
	"var ShellToolNames",
}

// TS-17-61: the exported surfaces of every package in apiPackages equal the
// golden, and guard's exported declarations equal preSpec17Guard, a list of
// its own that the golden does not replace. 17-REQ-10.3. The golden is the
// change detector over apiPackages, not pinned to spec 13's hash (see
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
