package policy

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gatewaydPath is the gateway server module's path.
const gatewaydPath = "github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"

// gatewaydAllowed is everything the gatewayd module may require directly:
// the root and sandbox modules, gRPC, and protobuf when a hand-written file
// imports it.
var gatewaydAllowed = []string{
	"github.com/agent-fox-dev/agentkit-go",
	"github.com/agent-fox-dev/agentkit-go/sandbox",
	"google.golang.org/grpc",
	"google.golang.org/protobuf",
}

// importsProtobuf reports whether a Go file of the module in dir, generated
// code aside, imports a google.golang.org/protobuf package.
func importsProtobuf(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".pb.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "google.golang.org/protobuf") {
				found = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TS-19-60: the gatewayd module is separate, replaces both parent modules,
// and directly requires only the allowed set. 19-REQ-9.1.
func TestGatewaydModule_TS19_60(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	dir := filepath.Join(repoRoot(t), "sandbox", "gatewayd")
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
	if m.Module.Path != gatewaydPath || m.Go != "1.27" {
		t.Errorf("module %s, go %s; want %s, 1.27", m.Module.Path, m.Go, gatewaydPath)
	}
	replaced := map[string]string{}
	for _, r := range m.Replace {
		replaced[r.Old.Path] = r.New.Path
	}
	if replaced["github.com/agent-fox-dev/agentkit-go"] != "../../" || replaced["github.com/agent-fox-dev/agentkit-go/sandbox"] != "../" {
		t.Errorf("replacements %v, want the root at ../../ and the sandbox module at ../", replaced)
	}
	direct := directSet(t, dir)
	for _, d := range direct {
		if d == gatewaydPath {
			continue
		}
		if !slices.Contains(gatewaydAllowed, d) {
			t.Errorf("gatewayd requires %s directly", d)
		}
		if d == "google.golang.org/protobuf" && !importsProtobuf(t, dir) {
			t.Error("gatewayd requires protobuf directly, but no hand-written file imports it")
		}
	}
	if err := validateAllowlist(direct, append(slices.Clone(approvedDeps), sandboxModules...)); err != nil {
		t.Error(err)
	}
	if !slices.Contains(sandboxModules, gatewaydPath) {
		t.Errorf("sandboxModules does not list %s", gatewaydPath)
	}
}

// modulePaths is the module paths `go list -m all` reports in dir.
func modulePaths(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Path}}", "all")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m all in %s: %v", dir, err)
	}
	return strings.Fields(string(out))
}

// TS-19-61: the gatewayd module's graph adds no module absent from the
// sandbox module's graph. 19-REQ-9.2.
func TestGatewaydGraph_TS19_61(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	sb := modulePaths(t, filepath.Join(root, "sandbox"))
	for _, p := range modulePaths(t, filepath.Join(root, "sandbox", "gatewayd")) {
		if p != gatewaydPath && !slices.Contains(sb, p) {
			t.Errorf("gatewayd's graph holds %s, which the sandbox module's graph lacks", p)
		}
	}
}
