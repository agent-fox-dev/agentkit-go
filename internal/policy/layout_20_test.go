package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// importsOf is the import paths of the non-test Go files in dir, relative to
// the module root.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		pf, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range pf.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TS-20-51: tools imports workspace, MatchGlob and ExpandBraces stay in tools,
// and the root agentkit package imports neither in non-test code. 20-REQ-8.5.
func TestWorkspaceImportGraph_TS20_51(t *testing.T) {
	if !contains(importsOf(t, "tools"), modulePath+"/workspace") {
		t.Error("tools does not import workspace")
	}
	root := importsOf(t, ".")
	for _, banned := range []string{modulePath + "/tools", modulePath + "/workspace"} {
		if contains(root, banned) {
			t.Errorf("the root agentkit package imports %s in non-test code", banned)
		}
	}

	files, err := filepath.Glob(filepath.Join(repoRoot(t), "tools", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		pf, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pf.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				declared[fd.Name.Name] = true
			}
		}
	}
	for _, name := range []string{"MatchGlob", "ExpandBraces"} {
		if !declared[name] {
			t.Errorf("tools no longer declares %s", name)
		}
	}
}

// TS-20-59: the .proto files still hash to their recorded .sha256 and
// sandbox.ProtocolMinor is still 2: the workspace move changes no wire.
// (That every module and example compiles is what make vet and make test
// measure.) 20-REQ-9.8.
func TestProtoFilesAndProtocolMinorAreUnchanged_TS20_59(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "sandbox", "internal", "sandboxpb")
	for _, name := range []string{"sandbox.proto", "gateway.proto"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(dir, name+".sha256"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != strings.TrimSpace(string(want)) {
			t.Errorf("%s hashes to %s, its .sha256 records %s", name, got, strings.TrimSpace(string(want)))
		}
	}

	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "sandbox", "version.go"), nil, 0)
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
}
