package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
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

// TS-20-60: apiPackages includes workspace and the regenerated golden records
// the new surfaces of tools and workspace. 20-REQ-10.1. (TS-13-67 compares the
// current API to the golden.)
func TestAPIGoldenRecordsWorkspace_TS20_60(t *testing.T) {
	if !contains(apiPackages, "workspace") {
		t.Errorf("apiPackages is %v: it lacks workspace", apiPackages)
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]string{}
	cur := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if name, ok := strings.CutPrefix(line, "== "); ok {
			cur = name
			continue
		}
		sections[cur] += line + "\n"
	}
	for _, c := range []struct {
		pkg, line string
		want      bool
	}{
		{"workspace", "func Open(root string, opts Options) (*Workspace, error)", true},
		{"workspace", "type Workspace struct", true},
		{"tools", "func For(ws *workspace.Workspace, opts Options) ([]core.Tool, error)", true},
		{"tools", "func All(", false},
		{"tools", "func NewWorkspace(", false},
	} {
		if got := strings.Contains(sections[c.pkg], c.line); got != c.want {
			t.Errorf("the golden's %s section contains %q = %v, want %v", c.pkg, c.line, got, c.want)
		}
	}
}

// TS-20-61: the goldenSHA13 comparisons are gone from TS-14-44, TS-15-42 and
// TS-17-61, while the golden comparison and TS-17-61's guard check remain.
// 20-REQ-10.2. (Those tests, run with everything else, are the comparison.)
func TestGoldenHashPinsAreRetired_TS20_61(t *testing.T) {
	for _, name := range []string{"exported_api_14_test.go", "exported_api_15_test.go", "exported_api_17_test.go"} {
		src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "policy", name))
		if err != nil {
			t.Fatal(err)
		}
		pf, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(pf, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "goldenSHA13" {
				t.Errorf("%s still refers to goldenSHA13", name)
			}
			return true
		})
		if !strings.Contains(string(src), "exported-api.golden") {
			t.Errorf("%s no longer compares the current API to the golden", name)
		}
	}
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "policy", "exported_api_17_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "preSpec17Guard") {
		t.Error("TS-17-61 lost its guard API check")
	}
}

// TS-20-62: policy testdata matches the last commit and ProtocolMinor is 2:
// TS-19-64's check, which passes once the regenerated golden is committed.
// 20-REQ-10.3.
func TestPolicyTestdataIsCommitted_TS20_62(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	cmd := exec.Command("git", "status", "--porcelain", "--", "internal/policy/testdata")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("no repository to compare internal/policy/testdata with: %v", err)
	}
	if len(out) > 0 {
		t.Errorf("internal/policy/testdata has changes against HEAD:\n%s", out)
	}
}

// TS-20-63: the root module stays within TS-12-14's 20,000-line budget, which
// core's TestLineBudgets_TS12_14 measures. 20-REQ-10.4.
func TestRootModuleStaysWithinItsLineBudget_TS20_63(t *testing.T) {
	cmd := exec.Command("go", "test", "./core", "-run", "TestLineBudgets_TS12_14", "-count=1")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("TS-12-14: %v\n%s", err, out)
	}
}
