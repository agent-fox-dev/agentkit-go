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
	"slices"
	"strings"

	"github.com/agentfox/agentkit-go/outline"
)

var lastWorkspace *Workspace

// workspaceImporter resolves imports within a workspace.
type workspaceImporter struct {
	ws          *Workspace
	modulePath  string
	fset        *token.FileSet
	imported    map[string]*types.Package
	pkgInfos    map[string]*types.Info
	parsedFiles map[string][]*ast.File
	fileSources map[string][]string
	errors      []error
}

// readModulePath reads the module path from go.mod in root, or returns empty string.
func readModulePath(root string) string {
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// newWorkspaceImporter creates a workspace-local Go importer.
func newWorkspaceImporter(ws *Workspace, modulePath string) *workspaceImporter {
	if ws != nil {
		lastWorkspace = ws
		if modulePath == "" {
			modulePath = readModulePath(ws.Root)
		}
	}
	return &workspaceImporter{
		ws:          ws,
		modulePath:  modulePath,
		fset:        token.NewFileSet(),
		imported:    make(map[string]*types.Package),
		pkgInfos:    make(map[string]*types.Info),
		parsedFiles: make(map[string][]*ast.File),
		fileSources: make(map[string][]string),
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
			relDir = ""
			isWorkspace = true
		} else if strings.HasPrefix(path, imp.modulePath+"/") {
			relDir = strings.TrimPrefix(path, imp.modulePath+"/")
			isWorkspace = true
		}
	}

	if !isWorkspace && imp.ws != nil {
		candidate := filepath.Join(imp.ws.Root, path)
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			relDir = path
			isWorkspace = true
		}
	}

	if isWorkspace && imp.ws != nil {
		absDir := filepath.Join(imp.ws.Root, relDir)
		files, err := imp.parseDirFiles(absDir, true)
		if err == nil && len(files) > 0 {
			cfg := makeTypesConfig(imp)
			info := &types.Info{
				Types:      make(map[ast.Expr]types.TypeAndValue),
				Defs:       make(map[*ast.Ident]types.Object),
				Uses:       make(map[*ast.Ident]types.Object),
				Selections: make(map[*ast.SelectorExpr]*types.Selection),
				Scopes:     make(map[ast.Node]*types.Scope),
			}
			pkg, _ := cfg.Check(path, imp.fset, files, info)
			if pkg != nil {
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
		if cached, ok := imp.parsedFiles[path]; ok && len(cached) > 0 {
			files = append(files, cached[0])
			continue
		}
		f, err := parser.ParseFile(imp.fset, path, nil, parser.ParseComments)
		if f != nil {
			files = append(files, f)
			imp.parsedFiles[path] = []*ast.File{f}
		}
		if err != nil {
			imp.errors = append(imp.errors, err)
		}
	}
	return files, nil
}

// checkPackage type-checks a package at pkgPath.
func checkPackage(imp *workspaceImporter, pkgPath string) (*types.Package, error) {
	if imp == nil {
		return nil, fmt.Errorf("nil importer")
	}
	if pkg, ok := imp.imported[pkgPath]; ok {
		return pkg, nil
	}

	relDir := pkgPath
	importPath := pkgPath
	if imp.modulePath != "" {
		if strings.HasPrefix(pkgPath, imp.modulePath+"/") {
			relDir = strings.TrimPrefix(pkgPath, imp.modulePath+"/")
		} else if pkgPath == imp.modulePath {
			relDir = ""
		} else {
			importPath = imp.modulePath + "/" + strings.TrimPrefix(pkgPath, "/")
		}
	}

	absDir := filepath.Join(imp.ws.Root, relDir)
	files, err := imp.parseDirFiles(absDir, false)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files found in %s", pkgPath)
	}

	cfg := makeTypesConfig(imp)
	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
	pkg, _ := cfg.Check(importPath, imp.fset, files, info)
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

// makeTypesConfig creates a types.Config for workspace type-checking.
func makeTypesConfig(imp *workspaceImporter) *types.Config {
	cfg := &types.Config{
		FakeImportC: true,
		Importer:    imp,
	}
	if imp != nil {
		cfg.Error = func(err error) {
			imp.errors = append(imp.errors, err)
		}
	} else {
		cfg.Error = func(err error) {}
	}
	return cfg
}

// typeCheck type-checks parsed AST files with cfg.
func typeCheck(cfg *types.Config, parsedFiles []*ast.File, fsets ...*token.FileSet) (*types.Package, []error) {
	if len(parsedFiles) == 0 {
		return nil, nil
	}
	var errs []error
	origErr := cfg.Error
	cfg.Error = func(err error) {
		errs = append(errs, err)
		if origErr != nil {
			origErr(err)
		}
	}

	pkgName := "main"
	if parsedFiles[0].Name != nil && parsedFiles[0].Name.Name != "" {
		pkgName = parsedFiles[0].Name.Name
	}

	var fset *token.FileSet
	if len(fsets) > 0 && fsets[0] != nil {
		fset = fsets[0]
	} else if imp, ok := cfg.Importer.(*workspaceImporter); ok && imp != nil {
		fset = imp.fset
	} else {
		fset = token.NewFileSet()
	}

	info := &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}

	pkg, _ := cfg.Check(pkgName, fset, parsedFiles, info)
	if pkg == nil {
		pkg = types.NewPackage(pkgName, pkgName)
		pkg.MarkComplete()
	}
	return pkg, errs
}

// parseGoPackageWithComments parses all Go source files in dir with comments preserved.
func parseGoPackageWithComments(dir string) (*token.FileSet, []*ast.File, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if f != nil {
			files = append(files, f)
		}
		if err != nil {
			return fset, files, err
		}
	}
	return fset, files, nil
}

// sanitizeSnippet trims whitespace, replaces control characters, and caps at 200 bytes.
func sanitizeSnippet(line string) string {
	line = strings.TrimSpace(line)
	var b strings.Builder
	for _, r := range line {
		if r < 32 || r == 127 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	if len(res) > 200 {
		res = res[:200]
	}
	return res
}

// findSite locates a ReferenceSite whose Source contains needle.
func findSite(sites []ReferenceSite, needle string) *ReferenceSite {
	for i := range sites {
		if strings.Contains(sites[i].Source, needle) {
			return &sites[i]
		}
	}
	return nil
}

// typeNameFromType extracts simple type name from types.Type.
func typeNameFromType(t types.Type) string {
	if t == nil {
		return ""
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	if basic, ok := t.(*types.Basic); ok {
		return basic.Name()
	}
	return ""
}

// typeNameFromExpr extracts simple type name from an ast.Expr receiver.
func typeNameFromExpr(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return typeNameFromExpr(e.X)
	case *ast.IndexExpr:
		return typeNameFromExpr(e.X)
	default:
		return ""
	}
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
	recvName := typeNameFromType(sig.Recv().Type())
	return recvName == container
}

// resolveGoReferences finds reference sites for target in Go sources across the workspace.
func resolveGoReferences(args ...any) []ReferenceSite {
	var target outline.Decl
	var ws *Workspace
	for _, arg := range args {
		switch v := arg.(type) {
		case outline.Decl:
			target = v
		case *Workspace:
			ws = v
		}
	}
	if ws == nil {
		ws = lastWorkspace
	}
	if ws == nil {
		return []ReferenceSite{}
	}

	imp := newWorkspaceImporter(ws, "")

	// 1. Discover all directories containing .go files
	dirMap := make(map[string]bool)
	_ = Walk(context.Background(), ws, ws.Root, WalkOptions{}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			dirMap[filepath.Dir(filepath.Join(ws.Root, filepath.FromSlash(rel)))] = true
		}
		return nil
	})

	// 2. Parse and type-check packages in all directories
	for dir := range dirMap {
		relDir, err := filepath.Rel(ws.Root, dir)
		if err != nil {
			relDir = dir
		}
		if relDir == "." {
			relDir = ""
		}
		_, _ = checkPackage(imp, relDir)
	}

	// 3. Find target types.Object across all packages
	var targetObj types.Object
	for _, info := range imp.pkgInfos {
		for ident, obj := range info.Defs {
			if obj == nil || obj.Name() != target.Name {
				continue
			}
			if target.Container != "" && !isReceiverMatching(obj, target.Container) {
				continue
			}
			if target.StartLine > 0 {
				pos := imp.fset.Position(ident.Pos())
				if pos.Line != target.StartLine {
					continue
				}
			}
			targetObj = obj
			break
		}
		if targetObj != nil {
			break
		}
	}

	// If not found in Defs, check package scopes
	if targetObj == nil {
		for _, pkg := range imp.imported {
			if pkg == nil || pkg.Scope() == nil {
				continue
			}
			if target.Container == "" {
				if obj := pkg.Scope().Lookup(target.Name); obj != nil {
					targetObj = obj
					break
				}
			} else {
				// Lookup container named type
				if typeObj := pkg.Scope().Lookup(target.Container); typeObj != nil {
					if typeName, ok := typeObj.(*types.TypeName); ok {
						if named, ok := typeName.Type().(*types.Named); ok {
							for i := 0; i < named.NumMethods(); i++ {
								m := named.Method(i)
								if m.Name() == target.Name {
									targetObj = m
									break
								}
							}
							if targetObj == nil {
								if it, ok := named.Underlying().(*types.Interface); ok {
									for i := 0; i < it.NumMethods(); i++ {
										m := it.Method(i)
										if m.Name() == target.Name {
											targetObj = m
											break
										}
									}
								}
							}
						}
					}
				}
			}
			if targetObj != nil {
				break
			}
		}
	}

	var sites []ReferenceSite

	// 4. Traverse all parsed files
	for filePath, fileList := range imp.parsedFiles {
		relPath, err := filepath.Rel(ws.Root, filePath)
		if err != nil {
			relPath = filePath
		}
		relPath = filepath.ToSlash(relPath)

		for _, f := range fileList {
			// Find enclosing declarations and package types.Info for this file
			var fileInfo *types.Info
			for _, info := range imp.pkgInfos {
				// Check if any Def or Use in this file exists in info
				for id := range info.Defs {
					if imp.fset.Position(id.Pos()).Filename == filePath {
						fileInfo = info
						break
					}
				}
				if fileInfo != nil {
					break
				}
				for id := range info.Uses {
					if imp.fset.Position(id.Pos()).Filename == filePath {
						fileInfo = info
						break
					}
				}
				if fileInfo != nil {
					break
				}
			}

			// AST stack traversal
			var stack []ast.Node
			ast.Inspect(f, func(n ast.Node) bool {
				if n == nil {
					if len(stack) > 0 {
						stack = stack[:len(stack)-1]
					}
					return true
				}
				stack = append(stack, n)

				id, isIdent := n.(*ast.Ident)
				if !isIdent || id.Name != target.Name {
					return true
				}

				pos := imp.fset.Position(id.Pos())

				// Check if this identifier is the definition of target itself
				if targetObj != nil && fileInfo != nil {
					if defObj := fileInfo.Defs[id]; defObj != nil {
						if defObj == targetObj || defObj.Pos() == targetObj.Pos() {
							// Skip declaration itself
							return true
						}
					}
				}

				var parent ast.Node
				if len(stack) >= 2 {
					parent = stack[len(stack)-2]
				}

				var enclosing outline.Decl
				for j := len(stack) - 1; j >= 0; j-- {
					if fn, ok := stack[j].(*ast.FuncDecl); ok {
						kind := outline.KindFunc
						container := ""
						if fn.Recv != nil && len(fn.Recv.List) > 0 {
							kind = outline.KindMethod
							container = typeNameFromExpr(fn.Recv.List[0].Type)
						}
						enclosing = outline.Decl{
							Kind:      kind,
							Name:      fn.Name.Name,
							Container: container,
						}
						break
					}
				}
				if enclosing.Kind == "" {
					enclosing = outline.Decl{Kind: "file"}
				}

				confidence := ""

				// Case A: Identifier is in a SelectorExpr (x.id)
				if selExpr, isSel := parent.(*ast.SelectorExpr); isSel && selExpr.Sel == id {
					if fileInfo != nil {
						if sel, ok := fileInfo.Selections[selExpr]; ok && sel != nil {
							selObj := sel.Obj()
							if targetObj != nil {
								if selObj == targetObj || (selObj.Name() == target.Name && isReceiverMatching(selObj, target.Container)) {
									confidence = "resolved"
								} else {
									// Selector resolved to unrelated type (e.g. Beta.Close vs Alpha.Close)
									return true
								}
							} else {
								confidence = "resolved"
							}
						} else if useObj, ok := fileInfo.Uses[id]; ok && useObj != nil {
							// Qualified identifier (e.g. alias.Do())
							if targetObj != nil {
								if useObj == targetObj || useObj.Name() == target.Name {
									confidence = "resolved"
								} else {
									return true
								}
							} else {
								confidence = "resolved"
							}
						} else {
							// Neither in Selections nor Uses: demote to lexical (e.g. stubbed external type)
							confidence = "lexical"
						}
					} else {
						confidence = "lexical"
					}
				} else {
					// Case B: Direct identifier usage
					if fileInfo != nil {
						if useObj, ok := fileInfo.Uses[id]; ok && useObj != nil {
							if targetObj != nil {
								if useObj == targetObj || useObj.Name() == target.Name {
									confidence = "resolved"
								} else {
									return true
								}
							} else {
								confidence = "resolved"
							}
						} else if defObj, ok := fileInfo.Defs[id]; ok && defObj != nil {
							if defObj == targetObj {
								return true
							}
						} else {
							confidence = "lexical"
						}
					} else {
						confidence = "lexical"
					}
				}

				if confidence != "" {
					source := imp.getSourceLine(filePath, pos.Line)
					sites = append(sites, ReferenceSite{
						Path:       relPath,
						Line:       pos.Line,
						Column:     pos.Column,
						Confidence: confidence,
						Enclosing:  enclosing,
						Source:     source,
					})
				}

				return true
			})
		}
	}

	// Sort reference sites deterministically
	slices.SortFunc(sites, func(a, b ReferenceSite) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})

	return sites
}
