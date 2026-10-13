package policy

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// approvedDeps is the root module's direct-dependency allowlist (PRD 10 §4,
// 09-REQ-7.2). An entry ending in "/" approves every module under that path.
// docs/DEPS.md keeps the longer rulings.
var approvedDeps = []string{
	// The module itself: `go list -m all` names the main module first.
	"github.com/agent-fox-dev/agentkit-go",
	// Glob matching for find_files and the ignore engine.
	"github.com/bmatcuk/doublestar/v4",
	// The MCP client and pool, on the official SDK.
	"github.com/modelcontextprotocol/go-sdk",
	// TOML parsing for MCP configuration (internal/toml).
	"github.com/pelletier/go-toml/v2",
	// Tree-sitter outlines: the runtime and its grammar modules.
	"github.com/tree-sitter/",
	"github.com/tree-sitter-grammars/",
	// Code mode's sandboxed Starlark runtime.
	"go.starlark.net",
	// The Anthropic wire: transport, auth, retries and stream framing.
	"github.com/anthropics/anthropic-sdk-go",
	// Google OAuth for the Vertex AI deployment's credentials.
	"golang.org/x/oauth2",
	// AWS configuration and credentials for the Bedrock deployment.
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/aws-sdk-go-v2/",
	// The sandbox agent protocol's transport (sandbox/internal/sandboxpb).
	"google.golang.org/grpc",
	// The protobuf runtime the generated sandbox protocol code uses.
	"google.golang.org/protobuf",
}

// validateAllowlist returns an error naming every dependency in deps that no
// approved entry covers.
func validateAllowlist(deps, approved []string) error {
	var bad []string
	for _, dep := range deps {
		if dep == "" {
			continue
		}
		ok := false
		for _, a := range approved {
			if dep == a || (strings.HasSuffix(a, "/") && strings.HasPrefix(dep, a)) {
				ok = true
				break
			}
		}
		if !ok {
			bad = append(bad, dep)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("unapproved direct dependencies: %s", strings.Join(bad, ", "))
	}
	return nil
}

// TS-09-15: the dependencies the cut removed are no longer direct
// requirements of the root go.mod.
func TestRemovedDependenciesPruned_TS09_15(t *testing.T) {
	f, err := os.Open(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	disallowed := map[string]bool{
		"golang.org/x/net": true, "golang.org/x/image": true,
		"golang.org/x/time": true, "code.dny.dev/ssrf": true,
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "// indirect") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) > 0 && disallowed[fields[0]] {
			t.Errorf("go.mod still requires %s directly", fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

// TS-09-16: every direct dependency `go list -m` reports for the root module
// is on the allowlist.
func TestDirectDependenciesAreApproved_TS09_16(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH; the allowlist needs the toolchain")
	}
	cmd := exec.Command("go", "list", "-m", "-f", "{{if not .Indirect}}{{.Path}}{{end}}", "all")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	if err := validateAllowlist(strings.Split(strings.TrimSpace(string(out)), "\n"), approvedDeps); err != nil {
		t.Fatal(err)
	}
}

// TS-09-17: an unapproved dependency is rejected by name.
func TestAnUnapprovedDependencyIsNamed_TS09_17(t *testing.T) {
	err := validateAllowlist([]string{"github.com/bmatcuk/doublestar/v4", "github.com/unapproved/evil-dep"}, approvedDeps)
	if err == nil {
		t.Fatal("an unapproved dependency passed the allowlist")
	}
	if !strings.Contains(err.Error(), "github.com/unapproved/evil-dep") {
		t.Fatalf("error %q does not name the unapproved dependency", err)
	}
	if strings.Contains(err.Error(), "doublestar") {
		t.Fatalf("error %q names an approved dependency", err)
	}
	// A prefix entry approves modules under it, not a lookalike beside it.
	if err := validateAllowlist([]string{"github.com/tree-sitter-evil/x"}, approvedDeps); err == nil {
		t.Fatal("a lookalike of an approved prefix passed")
	}
}

// TS-10-1: go.mod requires the official Anthropic SDK directly, at a tagged
// release rather than a pseudo-version.
func TestAnthropicSDKIsPinned_TS10_1(t *testing.T) {
	f, err := os.Open(filepath.Join(repoRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var version string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "require "))
		if len(fields) >= 2 && fields[0] == "github.com/anthropics/anthropic-sdk-go" {
			if strings.Contains(sc.Text(), "// indirect") {
				t.Fatal("the Anthropic SDK is only an indirect requirement")
			}
			version = fields[1]
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if version == "" {
		t.Fatal("go.mod does not require github.com/anthropics/anthropic-sdk-go")
	}
	if !strings.HasPrefix(version, "v") || strings.Contains(version, "-0.") || strings.HasPrefix(version, "v0.0.0-") {
		t.Fatalf("anthropic-sdk-go is pinned to %s, want a tagged release", version)
	}
}

// preSpec13Direct is the root module's direct-dependency set before spec 13
// promoted grpc and protobuf.
var preSpec13Direct = []string{
	"github.com/agent-fox-dev/agentkit-go",
	"github.com/anthropics/anthropic-sdk-go",
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/aws-sdk-go-v2/config",
	"github.com/aws/aws-sdk-go-v2/credentials",
	"github.com/bmatcuk/doublestar/v4",
	"github.com/modelcontextprotocol/go-sdk",
	"github.com/pelletier/go-toml/v2",
	"github.com/tree-sitter-grammars/tree-sitter-kotlin",
	"github.com/tree-sitter-grammars/tree-sitter-lua",
	"github.com/tree-sitter/go-tree-sitter",
	"github.com/tree-sitter/tree-sitter-bash",
	"github.com/tree-sitter/tree-sitter-c",
	"github.com/tree-sitter/tree-sitter-c-sharp",
	"github.com/tree-sitter/tree-sitter-cpp",
	"github.com/tree-sitter/tree-sitter-java",
	"github.com/tree-sitter/tree-sitter-javascript",
	"github.com/tree-sitter/tree-sitter-php",
	"github.com/tree-sitter/tree-sitter-python",
	"github.com/tree-sitter/tree-sitter-ruby",
	"github.com/tree-sitter/tree-sitter-rust",
	"github.com/tree-sitter/tree-sitter-scala",
	"github.com/tree-sitter/tree-sitter-typescript",
	"go.starlark.net",
	"golang.org/x/oauth2",
}

// TS-13-65: grpc and protobuf are direct requirements at the go.sum
// versions, approved with reasons, with x/net indirect and the module graph
// unchanged. 13-REQ-10.5.
func TestSandboxDepsPromoted_TS13_65(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	type req struct {
		version  string
		indirect bool
	}
	reqs := map[string]req{}
	for _, line := range strings.Split(string(mod), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) >= 2 && strings.Contains(fields[0], ".") {
			reqs[fields[0]] = req{fields[1], strings.Contains(line, "// indirect")}
		}
	}
	for m, v := range map[string]string{"google.golang.org/grpc": "v1.64.1", "google.golang.org/protobuf": "v1.36.11"} {
		if r := reqs[m]; r.version != v || r.indirect {
			t.Errorf("go.mod requires %s %+v, want %s direct", m, r, v)
		}
	}
	if r, ok := reqs["golang.org/x/net"]; !ok || !r.indirect {
		t.Errorf("golang.org/x/net is %+v, want an indirect requirement", r)
	}

	// Each new allowlist entry carries a reason on the line above it.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, "internal", "policy", "deps_test.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	commented := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		line := fset.Position(lit.Pos()).Line
		for _, cg := range f.Comments {
			if fset.Position(cg.End()).Line == line-1 && strings.TrimSpace(cg.Text()) != "" {
				commented[strings.Trim(lit.Value, `"`)] = true
			}
		}
		return true
	})
	for _, m := range []string{"google.golang.org/grpc", "google.golang.org/protobuf"} {
		if !slices.Contains(approvedDeps, m) || !commented[m] {
			t.Errorf("approvedDeps lacks %s with a reason comment above it", m)
		}
	}

	cmd := exec.Command("go", "list", "-m", "-f", "{{if not .Indirect}}{{.Path}}{{end}}", "all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	var direct []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			direct = append(direct, l)
		}
	}
	if err := validateAllowlist(direct, approvedDeps); err != nil {
		t.Error(err)
	}
	want := append(slices.Clone(preSpec13Direct), "google.golang.org/grpc", "google.golang.org/protobuf")
	slices.Sort(want)
	slices.Sort(direct)
	if !slices.Equal(direct, want) {
		t.Errorf("direct requirements are\n%s\nwant the pre-spec set plus grpc and protobuf:\n%s",
			strings.Join(direct, "\n"), strings.Join(want, "\n"))
	}

	cmd = exec.Command("go", "list", "-m", "all")
	cmd.Dir = root
	all, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m all: %v", err)
	}
	snap, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "go-list-m-all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(all) != string(snap) {
		t.Errorf("go list -m all differs from the pre-spec snapshot:\n%s", all)
	}
}

// TS-10-2: the allowlist approves the Anthropic SDK.
func TestAnthropicSDKIsApproved_TS10_2(t *testing.T) {
	if err := validateAllowlist([]string{"github.com/anthropics/anthropic-sdk-go"}, approvedDeps); err != nil {
		t.Fatal(err)
	}
}
