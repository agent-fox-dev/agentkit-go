package policy

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildConstraint is the //go:build expression of src, or nil.
func buildConstraint(t *testing.T, src []byte) constraint.Expr {
	t.Helper()
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "package ") {
			return nil
		}
		if constraint.IsGoBuild(line) {
			e, err := constraint.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
	}
	return nil
}

// evalFor reports whether e (nil means no constraint) holds for goos.
func evalFor(e constraint.Expr, goos string) bool {
	if e == nil {
		return true
	}
	return e.Eval(func(tag string) bool {
		return tag == goos || tag == "unix" && goos != "windows"
	})
}

// exportedNames are the exported top-level identifiers f declares.
func exportedNames(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out = append(out, s.Name.Name)
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							out = append(out, n.Name)
						}
					}
				}
			}
		}
	}
	return out
}

// TS-14-43: every file importing gokrazy/rsync is excluded on windows and
// built on linux and darwin, its package has windows-only files declaring the
// same exported identifiers, and TS-12-14's limit is still 20,000. The
// cross-target and host-cgo gates run unchanged in crosstarget_test.go.
// 14-REQ-9.3, 14-REQ-9.4.
func TestRsyncBuildTags_TS14_43(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	type pkgNames struct{ other, windows map[string]bool }
	pkgs := map[string]*pkgNames{}
	importing := 0
	for _, rel := range repoGoFiles(t, root) {
		src, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, rel, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		uses := false
		for _, imp := range f.Imports {
			if strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "github.com/gokrazy/rsync") {
				uses = true
			}
		}
		e := buildConstraint(t, src)
		dir := filepath.Dir(rel)
		if uses {
			importing++
			if evalFor(e, "windows") || !evalFor(e, "linux") || !evalFor(e, "darwin") {
				t.Errorf("%s imports gokrazy/rsync but its build constraint %v is not windows-off, linux- and darwin-on", rel, e)
			}
			if pkgs[dir] == nil {
				pkgs[dir] = &pkgNames{map[string]bool{}, map[string]bool{}}
			}
		}
	}
	if importing == 0 {
		t.Fatal("no file imports gokrazy/rsync")
	}
	// Second pass: the exported identifiers of each importing package, split
	// between its windows-only files and its other files.
	for _, rel := range repoGoFiles(t, root) {
		p := pkgs[filepath.Dir(rel)]
		if p == nil {
			continue
		}
		src, _ := os.ReadFile(filepath.Join(root, rel))
		f, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		e := buildConstraint(t, src)
		windowsOnly := evalFor(e, "windows") && !evalFor(e, "linux")
		nonWindows := !evalFor(e, "windows")
		for _, n := range exportedNames(f) {
			switch {
			case windowsOnly:
				p.windows[n] = true
			case nonWindows:
				p.other[n] = true
			}
		}
	}
	for dir, p := range pkgs {
		if len(p.windows) == 0 {
			t.Errorf("%s has no windows-only stub file", dir)
		}
		for n := range p.other {
			if !p.windows[n] {
				t.Errorf("%s: %s is declared for linux and darwin but has no windows stub", dir, n)
			}
		}
	}
	size, err := os.ReadFile(filepath.Join(root, "core", "size_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(size), "n > 20000") {
		t.Error("TS-12-14's 20,000-line limit changed")
	}
}
