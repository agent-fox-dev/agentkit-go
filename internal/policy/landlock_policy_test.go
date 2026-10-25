package policy

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	landlockModule = "github.com/landlock-lsm/go-landlock"
	confineDir     = "sandbox/internal/confine"
	confinePkg     = modulePath + "/" + confineDir
	sandboxdDir    = "sandbox/cmd/agentkit-sandboxd"
)

// knownGOOS are the file-name suffixes that constrain a Go file to one OS.
var knownGOOS = []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd", "android", "ios", "plan9", "solaris", "js", "wasip1", "aix", "dragonfly", "illumos"}

// buildsFor reports whether the file rel, parsed as f, builds for goos: its
// //go:build line and its _GOOS file-name suffix.
func buildsFor(f *ast.File, rel, goos string) bool {
	base := strings.TrimSuffix(filepath.Base(rel), ".go")
	for _, os := range knownGOOS {
		if strings.HasSuffix(base, "_"+os) || strings.Contains(base, "_"+os+"_") {
			if os != goos {
				return false
			}
		}
	}
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				if e, err := constraint.Parse(c.Text); err == nil {
					return e.Eval(func(tag string) bool { return tag == goos || (tag == "unix" && goos != "windows") })
				}
			}
		}
	}
	return true
}

// checkConfinement checks the Go files files (relative to root) against the
// rules that keep the host unconfined: only Linux-only files under
// sandbox/internal/confine import go-landlock, only agentkit-sandboxd
// imports confine, and nothing calls a method named BestEffort. It returns
// one violation per breach, naming the file (and the line for BestEffort),
// and how many go-landlock and confine importers it saw.
func checkConfinement(root string, files []string) (violations []string, llImporters, confineImporters int) {
	fset := token.NewFileSet()
	for _, rel := range files {
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.ParseComments)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == landlockModule || strings.HasPrefix(p, landlockModule+"/") {
				llImporters++
				if dir != confineDir {
					violations = append(violations, fmt.Sprintf("%s imports %s outside %s", rel, p, confineDir))
				} else if !buildsFor(f, rel, "linux") || buildsFor(f, rel, "darwin") || buildsFor(f, rel, "windows") {
					violations = append(violations, fmt.Sprintf("%s imports %s but is not constrained to Linux", rel, p))
				}
			}
			if p == confinePkg {
				confineImporters++
				if dir != sandboxdDir {
					violations = append(violations, fmt.Sprintf("%s imports %s; only %s may", rel, p, sandboxdDir))
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "BestEffort" {
					violations = append(violations, fmt.Sprintf("%s:%d calls BestEffort", rel, fset.Position(c.Pos()).Line))
				}
			}
			return true
		})
	}
	return violations, llImporters, confineImporters
}

// TS-15-39: go-landlock is imported only by Linux-only confine files,
// confine only by agentkit-sandboxd, and no file calls BestEffort.
// 15-REQ-7.1, 15-REQ-7.2, 15-REQ-7.3. The walk covers every module: the
// sandbox code is a nested module (docs/adr/05-keep-the-sandbox-in-its-own-module-and-sync-through-a-filtered-view.md).
func TestHostNeverConfined_TS15_39(t *testing.T) {
	root := repoRoot(t)
	v, ll, conf := checkConfinement(root, repoGoFiles(t, root))
	for _, x := range v {
		t.Error(x)
	}
	if ll == 0 || conf == 0 {
		t.Errorf("%d go-landlock importers and %d confine importers: the check is vacuous", ll, conf)
	}

	for _, c := range []struct {
		name, rel, src, want string
	}{
		{"outside", "sandbox/internal/agent/x.go", "package agent\nimport _ \"github.com/landlock-lsm/go-landlock/landlock\"\n", "sandbox/internal/agent/x.go"},
		{"unconstrained", confineDir + "/y.go", "package confine\nimport _ \"github.com/landlock-lsm/go-landlock/landlock\"\n", confineDir + "/y.go"},
		{"agent imports confine", "sandbox/internal/agent/z.go", "package agent\nimport _ \"" + confinePkg + "\"\n", "sandbox/internal/agent/z.go"},
		{"best effort", "sandbox/w.go", "package sandbox\nfunc f(c interface{ BestEffort() int }) {\n\t_ = c.BestEffort()\n}\n", "sandbox/w.go:3"},
	} {
		dir := t.TempDir()
		p := filepath.Join(dir, filepath.FromSlash(c.rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		v, _, _ := checkConfinement(dir, []string{c.rel})
		if len(v) != 1 || !strings.Contains(v[0], c.want) {
			t.Errorf("%s: violations %q, want one naming %s", c.name, v, c.want)
		}
	}
}
