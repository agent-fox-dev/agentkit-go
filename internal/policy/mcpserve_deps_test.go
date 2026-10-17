package policy

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// mcpservePath is the MCP serving module's path.
const mcpservePath = "github.com/agent-fox-dev/agentkit-go/mcpserve"

const (
	rootModulePath = "github.com/agent-fox-dev/agentkit-go"
	mcpSDKPath     = "github.com/modelcontextprotocol/go-sdk"
)

// TS-23-1: the mcpserve module is separate, replaces the root at ../, and
// directly requires only the root module and the MCP SDK, at the version the
// root requires. 23-REQ-1.1.
func TestMcpserveModule_TS23_1(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	dir := filepath.Join(root, "mcpserve")
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var m struct {
		Module  struct{ Path string }
		Go      string
		Replace []struct{ Old, New struct{ Path string } }
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.Module.Path != mcpservePath || m.Go != "1.27" {
		t.Errorf("module %s, go %s; want %s, 1.27", m.Module.Path, m.Go, mcpservePath)
	}
	if len(m.Replace) != 1 || m.Replace[0].Old.Path != rootModulePath || m.Replace[0].New.Path != "../" {
		t.Errorf("replacements %+v, want exactly the root module at ../", m.Replace)
	}
	direct := directSet(t, dir)
	var others []string
	for _, d := range direct {
		if d != mcpservePath {
			others = append(others, d)
		}
	}
	if want := []string{rootModulePath, mcpSDKPath}; !slices.Equal(others, want) && !slices.Equal(others, []string{mcpSDKPath, rootModulePath}) {
		t.Errorf("mcpserve requires %v directly, want exactly %v", others, want)
	}
	if got, want := parseGoMod(t, dir)[mcpSDKPath].version, parseGoMod(t, root)[mcpSDKPath].version; got == "" || got != want {
		t.Errorf("mcpserve requires the SDK at %q, the root at %q", got, want)
	}
	if err := validateAllowlist(direct, append(slices.Clone(approvedDeps), mcpservePath)); err != nil {
		t.Error(err)
	}
}

// TS-23-2: the mcpserve module's graph adds no module absent from the root
// module's graph. 23-REQ-1.2.
func TestMcpserveGraph_TS23_2(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	rootMods := modulePaths(t, root)
	for _, p := range modulePaths(t, filepath.Join(root, "mcpserve")) {
		if p != mcpservePath && !slices.Contains(rootMods, p) {
			t.Errorf("mcpserve's graph holds %s, which the root module's graph lacks", p)
		}
	}
}
