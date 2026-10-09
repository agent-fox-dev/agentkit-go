package tools

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentfox/agentkit-go/outline"
)

// workspaceImporter resolves imports within a workspace.
type workspaceImporter struct {
	ws          *Workspace
	modulePath  string
	fset        *token.FileSet
	imported    map[string]*types.Package
	pkgInfos    map[string]*types.Info
	parsedFiles map[string]*ast.File // by absolute path
	fileSources map[string][]string
	errors      []error
}

// readModulePath reads the module path from go.mod in root, or returns empty string.
func readModulePath(root string) string {
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// newWorkspaceImporter creates a workspace-local Go importer. An empty
// modulePath is read from the workspace's go.mod.
func newWorkspaceImporter(ws *Workspace, modulePath string) *workspaceImporter {
	if modulePath == "" {
		modulePath = readModulePath(ws.Root)
	}
	return &workspaceImporter{
		ws:          ws,
		modulePath:  modulePath,
		fset:        token.NewFileSet(),
		imported:    make(map[string]*types.Package),
		pkgInfos:    make(map[string]*types.Info),
		parsedFiles: make(map[string]*ast.File),
		fileSources: make(map[string][]string),
	}
}

func newTypesInfo() *types.Info {
	return &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
}

// Import satisfies types.Importer.
func (imp *workspaceImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := imp.imported[path]; ok {
		return pkg, nil
	}

	// Determine if path belongs to the workspace
	isWorkspace := false
	var relDir string
	if imp.modulePath != "" {
		if path == imp.modulePath {
			isWorkspace = true
		} else if rest, ok := strings.CutPrefix(path, imp.modulePath+"/"); ok {
			relDir = rest
			isWorkspace = true
		}
	}
	if !isWorkspace {
		if fi, err := os.Stat(filepath.Join(imp.ws.Root, path)); err == nil && fi.IsDir() {
			relDir = path
			isWorkspace = true
		}
	}

	if isWorkspace {
		files, err := imp.parseDirFiles(filepath.Join(imp.ws.Root, relDir), true)
		if err == nil && len(files) > 0 {
			info := newTypesInfo()
			if pkg, _ := makeTypesConfig(imp).Check(path, imp.fset, files, info); pkg != nil {
				imp.imported[path] = pkg
				imp.pkgInfos[path] = info
				return pkg, nil
			}
		}
	}

	// External import (standard library fmt, os or 3rd-party) -> synthetic empty package
	pkg := types.NewPackage(path, filepath.Base(path))
	pkg.MarkComplete()
	imp.imported[path] = pkg
	return pkg, nil
}

// parseDirFiles parses .go files in dir. If nonTestOnly is true, _test.go files are excluded.
func (imp *workspaceImporter) parseDirFiles(dir string, nonTestOnly bool) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if nonTestOnly && strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if cached, ok := imp.parsedFiles[path]; ok {
			files = append(files, cached)
			continue
		}
		f, err := parser.ParseFile(imp.fset, path, nil, parser.ParseComments)
		if f != nil {
			files = append(files, f)
			imp.parsedFiles[path] = f
		}
		if err != nil {
			imp.errors = append(imp.errors, err)
		}
	}
	return files, nil
}

// checkPackage type-checks a package at pkgPath.
func checkPackage(imp *workspaceImporter, pkgPath string) (*types.Package, error) {
	if pkg, ok := imp.imported[pkgPath]; ok {
		return pkg, nil
	}

	relDir := pkgPath
	importPath := pkgPath
	if imp.modulePath != "" {
		if rest, ok := strings.CutPrefix(pkgPath, imp.modulePath+"/"); ok {
			relDir = rest
		} else if pkgPath == imp.modulePath {
			relDir = ""
		} else {
			importPath = imp.modulePath + "/" + strings.TrimPrefix(pkgPath, "/")
		}
	}

	files, err := imp.parseDirFiles(filepath.Join(imp.ws.Root, relDir), false)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files found in %s", pkgPath)
	}

	info := newTypesInfo()
	pkg, _ := makeTypesConfig(imp).Check(importPath, imp.fset, files, info)
	if pkg == nil {
		pkg = types.NewPackage(importPath, filepath.Base(relDir))
		pkg.MarkComplete()
	}
	imp.imported[importPath] = pkg
	imp.imported[pkgPath] = pkg
	imp.pkgInfos[importPath] = info
	imp.pkgInfos[pkgPath] = info
	return pkg, nil
}

// makeTypesConfig creates a types.Config for workspace type-checking that
// collects errors on imp instead of aborting.
func makeTypesConfig(imp *workspaceImporter) *types.Config {
	return &types.Config{
		FakeImportC: true,
		Importer:    imp,
		Error:       func(err error) { imp.errors = append(imp.errors, err) },
	}
}

// loadGoWorkspace parses and type-checks every directory of ws that holds Go files.
func loadGoWorkspace(ws *Workspace) *workspaceImporter {
	imp := newWorkspaceImporter(ws, "")
	dirs := make(map[string]bool)
	_ = Walk(context.Background(), ws, ws.Root, WalkOptions{}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			dirs[filepath.Dir(filepath.Join(ws.Root, filepath.FromSlash(rel)))] = true
		}
		return nil
	})
	for dir := range dirs {
		relDir, err := filepath.Rel(ws.Root, dir)
		if err != nil {
			relDir = dir
		}
		if relDir == "." {
			relDir = ""
		}
		_, _ = checkPackage(imp, relDir)
	}
	return imp
}

// definesName reports whether any type-checked package declares an object named name.
func (imp *workspaceImporter) definesName(name string) bool {
	for _, info := range imp.pkgInfos {
		for _, obj := range info.Defs {
			if obj != nil && obj.Name() == name {
				return true
			}
		}
	}
	return false
}

// typeNameFromType extracts simple type name from types.Type.
func typeNameFromType(t types.Type) string {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	switch t := t.(type) {
	case *types.Named:
		return t.Obj().Name()
	case *types.Basic:
		return t.Name()
	}
	return ""
}

// getSourceLine returns the sanitized line from filePath at 1-based lineNum.
func (imp *workspaceImporter) getSourceLine(filePath string, lineNum int) string {
	lines, ok := imp.fileSources[filePath]
	if !ok {
		content, err := os.ReadFile(filePath)
		if err != nil {
			return ""
		}
		lines = strings.Split(string(content), "\n")
		imp.fileSources[filePath] = lines
	}
	if lineNum >= 1 && lineNum <= len(lines) {
		return sanitizeSnippet(lines[lineNum-1])
	}
	return ""
}

// isReceiverMatching checks whether a function or method matches target receiver container.
func isReceiverMatching(obj types.Object, container string) bool {
	if container == "" {
		return true
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return typeNameFromType(sig.Recv().Type()) == container
}

// findTargetObject locates target's types.Object: first among the
// declarations of every checked package, then in package scopes.
func (imp *workspaceImporter) findTargetObject(target outline.Decl) types.Object {
	for _, info := range imp.pkgInfos {
		for ident, obj := range info.Defs {
			if obj == nil || obj.Name() != target.Name || !isReceiverMatching(obj, target.Container) {
				continue
			}
			if target.StartLine > 0 && imp.fset.Position(ident.Pos()).Line != target.StartLine {
				continue
			}
			return obj
		}
	}
	for _, pkg := range imp.imported {
		if obj := lookupInScope(pkg.Scope(), target); obj != nil {
			return obj
		}
	}
	return nil
}

// lookupInScope finds target in scope: by name, or as a method of the
// named type target.Container (its declared methods, then its interface's).
func lookupInScope(scope *types.Scope, target outline.Decl) types.Object {
	if target.Container == "" {
		return scope.Lookup(target.Name)
	}
	tn, ok := scope.Lookup(target.Container).(*types.TypeName)
	if !ok {
		return nil
	}
	named, ok := tn.Type().(*types.Named)
	if !ok {
		return nil
	}
	for m := range named.Methods() {
		if m.Name() == target.Name {
			return m
		}
	}
	if it, ok := named.Underlying().(*types.Interface); ok {
		for m := range it.Methods() {
			if m.Name() == target.Name {
				return m
			}
		}
	}
	return nil
}

// classifyGoIdent returns the confidence of a use of the target at id, or
// "" when id is not a reference to it. info is the types.Info of id's file
// (nil when the file was not type-checked) and parent is id's parent node.
func classifyGoIdent(info *types.Info, id *ast.Ident, parent ast.Node, targetObj types.Object, container string) string {
	if info == nil {
		return "lexical"
	}
	if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Sel == id {
		if s := info.Selections[sel]; s != nil {
			// A selector resolved to an unrelated type (Beta.Close vs Alpha.Close) is dropped.
			if targetObj == nil || s.Obj() == targetObj || isReceiverMatching(s.Obj(), container) {
				return "resolved"
			}
			return ""
		}
	} else if info.Defs[id] != nil {
		return "" // a declaration, not a use
	}
	if info.Uses[id] != nil {
		return "resolved"
	}
	// Unresolved, e.g. a selector on a stubbed external type: demoted, not dropped.
	return "lexical"
}

// resolveGoReferences finds reference sites for target in the Go sources imp has checked.
func resolveGoReferences(imp *workspaceImporter, target outline.Decl) []ReferenceSite {
	targetObj := imp.findTargetObject(target)

	// Index each file's types.Info once: the file scope is recorded in
	// the Info of every package check that included the file.
	fileInfos := make(map[*ast.File]*types.Info)
	for _, info := range imp.pkgInfos {
		for node := range info.Scopes {
			if f, ok := node.(*ast.File); ok && fileInfos[f] == nil {
				fileInfos[f] = info
			}
		}
	}

	var sites []ReferenceSite
	for filePath, f := range imp.parsedFiles {
		relPath, err := filepath.Rel(imp.ws.Root, filePath)
		if err != nil {
			relPath = filePath
		}
		relPath = filepath.ToSlash(relPath)
		info := fileInfos[f]

		ast.PreorderStack(f, nil, func(n ast.Node, stack []ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || id.Name != target.Name {
				return true
			}
			var parent ast.Node
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			confidence := classifyGoIdent(info, id, parent, targetObj, target.Container)
			if confidence == "" {
				return true
			}
			pos := imp.fset.Position(id.Pos())
			sites = append(sites, ReferenceSite{
				Path:       relPath,
				Line:       pos.Line,
				Column:     pos.Column,
				Confidence: confidence,
				Source:     imp.getSourceLine(filePath, pos.Line),
			})
			return true
		})
	}
	return sites
}
