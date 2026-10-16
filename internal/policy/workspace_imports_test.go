package policy

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// doublestarImport is the one third-party package workspace may import.
const doublestarImport = "github.com/bmatcuk/doublestar/v4"

// checkWorkspaceImports is the import rule of package workspace (20-REQ-8.3):
// its non-test files import the standard library and doublestar, and nothing
// else, first-party packages included, so that every package of the module
// can depend on it. It returns an error naming the offending file and import
// path, or nil. Test files are exempt.
func checkWorkspaceImports(dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return err
	}
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		pf, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range pf.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			// A standard-library path has no dot in its first element.
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") && p != doublestarImport {
				bad = append(bad, fmt.Sprintf("%s imports %s", filepath.Base(f), p))
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("package workspace may import only the standard library and %s: %s",
			doublestarImport, strings.Join(bad, "; "))
	}
	return nil
}

// TS-20-50: the import rule names the offending file and import path for a
// non-test file of workspace, exempts test files, and passes on the real
// package. 20-REQ-8.4.
func TestWorkspaceImportRule_TS20_50(t *testing.T) {
	write := func(dir, name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	offending := t.TempDir()
	write(offending, "ok.go", "package workspace\n\nimport (\n\t\"os\"\n\t\""+doublestarImport+"\"\n)\n")
	write(offending, "bad.go", "package workspace\n\nimport _ \""+modulePath+"/tools\"\n")
	err := checkWorkspaceImports(offending)
	if err == nil {
		t.Fatal("a non-test file importing tools was accepted")
	}
	for _, want := range []string{"bad.go", modulePath + "/tools"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "ok.go") {
		t.Errorf("the error %q names a file that is within the rule", err)
	}

	testOnly := t.TempDir()
	write(testOnly, "ok.go", "package workspace\n\nimport \"os\"\n")
	write(testOnly, "x_test.go", "package workspace\n\nimport _ \""+modulePath+"/tools\"\n")
	if err := checkWorkspaceImports(testOnly); err != nil {
		t.Errorf("an offending import in a _test.go file was refused: %v", err)
	}

	if err := checkWorkspaceImports(filepath.Join(repoRoot(t), "workspace")); err != nil {
		t.Errorf("the real workspace package breaks its import rule: %v", err)
	}
}
