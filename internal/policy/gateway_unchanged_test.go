package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"testing"
)

// TS-19-64: the root and sandbox modules' requirements and graphs, the
// exported-API golden and the Agent protocol minor are unchanged; the
// graph, golden and version tests (TS-09-15, TS-09-16, TS-13-65, TS-14-44,
// TS-15-42) check the rest with their testdata untouched. 19-REQ-9.5.
func TestGatewayLeavesTheBuild_TS19_64(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "sandbox", "version.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	minor := ""
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok {
			for i, name := range vs.Names {
				if name.Name == "ProtocolMinor" && i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
						minor = lit.Value
					}
				}
			}
		}
		return true
	})
	if minor != "2" {
		t.Errorf("sandbox.ProtocolMinor is %q, want 2", minor)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	head := exec.Command("git", "rev-parse", "--verify", "HEAD")
	head.Dir = root
	if err := head.Run(); err != nil {
		t.Skipf("no commit to compare internal/policy/testdata with: %v", err)
	}
	cmd := exec.Command("git", "status", "--porcelain", "--", "internal/policy/testdata")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if len(out) > 0 {
		t.Errorf("internal/policy/testdata has changes:\n%s", out)
	}
}
