package policy

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
