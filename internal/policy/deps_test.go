package policy

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	// The OpenRouter wire (provider/openrouter): OpenRouter's own Go SDK for its
	// native Chat Completions API, pinned at a beta 0.x release (docs/DEPS.md).
	"github.com/OpenRouterTeam/go-sdk",
	// Google OAuth for the Vertex AI deployment's credentials.
	"golang.org/x/oauth2",
	// AWS configuration and credentials for the Bedrock deployment.
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/aws-sdk-go-v2/",
	// The sandbox agent protocol's transport (sandbox/internal/sandboxpb).
	"google.golang.org/grpc",
	// The protobuf runtime the generated sandbox protocol code uses.
	"google.golang.org/protobuf",
	// The in-process rsync protocol for sandbox LocalDir transfers (sandbox/internal/rsyncx).
	"github.com/gokrazy/rsync",
	// The sandbox agent's own Landlock confinement (sandbox/internal/confine).
	"github.com/landlock-lsm/go-landlock",
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
// promoted grpc and protobuf, plus the OpenRouter SDK that spec 27 added.
var preSpec13Direct = []string{
	"github.com/OpenRouterTeam/go-sdk",
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

// goModRequire is one require line of a go.mod.
type goModRequire struct {
	version  string
	indirect bool
}

// parseGoMod returns the require lines of the go.mod in dir.
func parseGoMod(t *testing.T, dir string) map[string]goModRequire {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := map[string]goModRequire{}
	for _, line := range strings.Split(string(mod), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) >= 2 && strings.Contains(fields[0], ".") && !strings.HasPrefix(fields[0], "//") {
			reqs[fields[0]] = goModRequire{fields[1], strings.Contains(line, "// indirect")}
		}
	}
	return reqs
}

// directSet is the sorted direct-dependency set `go list -m` reports for the
// module in dir, the module itself included.
func directSet(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{if not .Indirect}}{{.Path}}{{end}}", "all")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m in %s: %v", dir, err)
	}
	var direct []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			direct = append(direct, l)
		}
	}
	slices.Sort(direct)
	return direct
}

// commentedDeps returns the approvedDeps entries that have a non-empty comment
// on the line above them.
func commentedDeps(t *testing.T, root string) map[string]bool {
	t.Helper()
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
	return commented
}

// sandboxModules are the nested modules that hold the sandbox tree; their
// own module paths are allowed in their direct sets.
var sandboxModules = []string{
	"github.com/agent-fox-dev/agentkit-go/sandbox",
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd",
	"github.com/agent-fox-dev/agentkit-go/examples/sandbox",
}

// TS-13-65: grpc and protobuf are direct requirements at the go.sum
// versions, approved with reasons, with x/net indirect and the root module's
// graph unchanged. 13-REQ-10.5. Since spec 14 the sandbox tree is its own
// module (docs/errata/14_sandbox_rsync_sync.md): grpc and protobuf are direct
// requirements of sandbox/go.mod, and the root module's direct set is back to
// the pre-spec-13 set.
func TestSandboxDepsPromoted_TS13_65(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	sb := filepath.Join(root, "sandbox")
	reqs := parseGoMod(t, sb)
	for m, v := range map[string]string{"google.golang.org/grpc": "v1.64.1", "google.golang.org/protobuf": "v1.36.11"} {
		if r := reqs[m]; r.version != v || r.indirect {
			t.Errorf("sandbox/go.mod requires %s %+v, want %s direct", m, r, v)
		}
	}
	for _, dir := range []string{root, sb} {
		if r, ok := parseGoMod(t, dir)["golang.org/x/net"]; !ok || !r.indirect {
			t.Errorf("%s: golang.org/x/net is %+v, want an indirect requirement", dir, r)
		}
	}
	commented := commentedDeps(t, root)
	for _, m := range []string{"google.golang.org/grpc", "google.golang.org/protobuf"} {
		if !slices.Contains(approvedDeps, m) || !commented[m] {
			t.Errorf("approvedDeps lacks %s with a reason comment above it", m)
		}
	}

	direct := directSet(t, root)
	if err := validateAllowlist(direct, approvedDeps); err != nil {
		t.Error(err)
	}
	want := slices.Sorted(slices.Values(preSpec13Direct))
	if !slices.Equal(direct, want) {
		t.Errorf("root direct requirements are\n%s\nwant the pre-spec set:\n%s",
			strings.Join(direct, "\n"), strings.Join(want, "\n"))
	}
	sbDirect := directSet(t, sb)
	if err := validateAllowlist(sbDirect, append(slices.Clone(approvedDeps), sandboxModules...)); err != nil {
		t.Error(err)
	}
	for _, m := range []string{"google.golang.org/grpc", "google.golang.org/protobuf"} {
		if !slices.Contains(sbDirect, m) {
			t.Errorf("the sandbox module's direct set lacks %s: %v", m, sbDirect)
		}
	}

	cmd := exec.Command("go", "list", "-m", "all")
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

// d1Upgrades are the versions D-1 accepted: minimum version selection raises
// these when gokrazy/rsync v0.3.8 enters the sandbox module's graph
// (docs/errata/14_sandbox_rsync_sync.md).
var d1Upgrades = map[string]string{
	"github.com/BurntSushi/toml": "v1.6.0",
	"golang.org/x/crypto":        "v0.46.0",
	"golang.org/x/mod":           "v0.30.0",
	"golang.org/x/net":           "v0.47.0",
	"golang.org/x/text":          "v0.32.0",
	"golang.org/x/tools":         "v0.39.0",
}

// TS-14-42: gokrazy/rsync v0.3.8 is the only new direct requirement,
// approved with a reason; the sandbox module's graph gains only additions
// and D-1's upgrades; x/net stays out of the direct set. Spec 15 made
// go-landlock direct (TS-15-40).
// 14-REQ-9.1, 14-REQ-9.2. The requirement is the nested sandbox module's
// (D-2), so the root module's go.mod and graph do not change at all.
func TestRsyncDependency_TS14_42(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	sb := filepath.Join(root, "sandbox")
	reqs := parseGoMod(t, sb)
	if r := reqs["github.com/gokrazy/rsync"]; r.version != "v0.3.8" || r.indirect {
		t.Errorf("sandbox/go.mod requires gokrazy/rsync %+v, want v0.3.8 direct", r)
	}
	if _, ok := parseGoMod(t, root)["github.com/gokrazy/rsync"]; ok {
		t.Error("the root go.mod requires gokrazy/rsync; only the sandbox module may")
	}
	for _, m := range []string{"golang.org/x/net"} {
		if r, ok := reqs[m]; ok && !r.indirect {
			t.Errorf("sandbox/go.mod requires %s directly", m)
		}
	}
	want := []string{"github.com/agent-fox-dev/agentkit-go", "github.com/agent-fox-dev/agentkit-go/sandbox",
		"github.com/gokrazy/rsync", "github.com/landlock-lsm/go-landlock", "google.golang.org/grpc", "google.golang.org/protobuf"}
	if got := directSet(t, sb); !slices.Equal(got, want) {
		t.Errorf("the sandbox module's direct set is %v, want %v", got, want)
	}
	if !slices.Contains(approvedDeps, "github.com/gokrazy/rsync") || !commentedDeps(t, root)["github.com/gokrazy/rsync"] {
		t.Error("approvedDeps lacks github.com/gokrazy/rsync with a reason comment above it")
	}

	cmd := exec.Command("go", "list", "-m", "all")
	cmd.Dir = sb
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m all: %v", err)
	}
	snap, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(snap) {
		t.Errorf("the sandbox module's go list -m all differs from its snapshot:\n%s", out)
	}
	base, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.13.txt"))
	if err != nil {
		t.Fatal(err)
	}
	now := strings.Split(string(out), "\n")
	for _, line := range strings.Split(strings.TrimSpace(string(base)), "\n") {
		f := strings.Fields(line)
		if v, up := d1Upgrades[f[0]]; up {
			line = f[0] + " " + v
		}
		if !slices.Contains(now, line) {
			t.Errorf("%q is missing from the graph: an existing version moved beyond D-1", line)
		}
	}
}

// openRouterSDK is the OpenRouter Go SDK, the one dependency of
// provider/openrouter (spec 27).
const openRouterSDK = "github.com/OpenRouterTeam/go-sdk"

// TS-27-49: go.mod requires the OpenRouter SDK at exactly v0.9.66 as a direct
// requirement, and the allowlist approves it with a reason on the line above.
// 27-REQ-9.1, 27-REQ-9.3.
func TestOpenRouterSDKIsPinned_TS27_49(t *testing.T) {
	root := repoRoot(t)
	r, ok := parseGoMod(t, root)[openRouterSDK]
	if !ok {
		t.Fatalf("go.mod does not require %s", openRouterSDK)
	}
	if r.version != "v0.9.66" {
		t.Errorf("go.mod pins %s at %s, want v0.9.66", openRouterSDK, r.version)
	}
	if r.indirect {
		t.Errorf("%s is only an indirect requirement", openRouterSDK)
	}
	if !slices.Contains(approvedDeps, openRouterSDK) || !commentedDeps(t, root)[openRouterSDK] {
		t.Errorf("approvedDeps lacks %s with a reason comment above it", openRouterSDK)
	}
}

// moduleDirs returns every directory under the repository that holds a go.mod,
// the root first, and skips test fixtures.
func moduleDirs(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata" || d.Name() == "build") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "go.mod" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

// TS-27-50: of every package in every module, only provider/openrouter imports
// the OpenRouter SDK or one of its subpackages, tests included. 27-REQ-9.2.
func TestOnlyTheOpenRouterProviderImportsTheSDK_TS27_50(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	const tmpl = "{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}"
	var importers []string
	for _, dir := range moduleDirs(t) {
		cmd := exec.Command("go", "list", "-e", "-f", tmpl, "./...")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list in %s: %v", dir, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.Fields(line)
			for _, imp := range f[min(1, len(f)):] {
				if imp == openRouterSDK || strings.HasPrefix(imp, openRouterSDK+"/") {
					importers = append(importers, f[0])
					break
				}
			}
		}
	}
	if len(importers) != 1 || !strings.HasSuffix(importers[0], "/provider/openrouter") {
		t.Errorf("packages importing %s: %v, want only provider/openrouter", openRouterSDK, importers)
	}
}

// TS-27-55: the non-test sources of provider/openrouter write no model id,
// window, output cap or price: the string and number literals hold none of the
// catalog's openrouter row. 27-REQ-9.8.
func TestOpenRouterProviderHoldsNoModelMetadata_TS27_55(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "provider", "openrouter", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	banned := map[float64]bool{1000000: true, 131072: true, 32768: true, 0.15: true, 0.47: true, 0.016: true}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok {
				return true
			}
			switch lit.Kind {
			case token.STRING:
				if strings.Contains(lit.Value, "qwen/") || strings.Contains(lit.Value, "qwen3") {
					t.Errorf("%s: string literal %s names a catalog model", filepath.Base(f), lit.Value)
				}
			case token.INT, token.FLOAT:
				if v, err := strconv.ParseFloat(strings.ReplaceAll(lit.Value, "_", ""), 64); err == nil && banned[v] {
					t.Errorf("%s: number literal %s is a catalog window, cap or price", filepath.Base(f), lit.Value)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Error("provider/openrouter has no non-test Go files to scan")
	}
}

// TS-10-2: the allowlist approves the Anthropic SDK.
func TestAnthropicSDKIsApproved_TS10_2(t *testing.T) {
	if err := validateAllowlist([]string{"github.com/anthropics/anthropic-sdk-go"}, approvedDeps); err != nil {
		t.Fatal(err)
	}
}
