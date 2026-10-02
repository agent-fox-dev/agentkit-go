package outline

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
)

// parseGo is a seam for testing: it can be replaced to simulate a parser
// that returns a nil AST.
var parseGo = parser.ParseFile

// outlineGo parses a Go file with go/ast and returns its declarations.
// It returns (File, true) when it handled the file (even partially),
// or (File{}, false) when the parser returned no AST at all and the
// caller should fall through to the next backend.
func outlineGo(abs string, src []byte, opts Options) (File, bool) {
	fset := token.NewFileSet()
	f, err := parseGo(fset, abs, src, parser.SkipObjectResolution)
	if f == nil {
		// No AST at all — fall through.
		_ = err
		return File{}, false
	}
	// Even if err != nil (syntax errors), we use the partial AST.

	var decls []Decl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			decls = append(decls, goFuncDecl(fset, d, src)...)
		case *ast.GenDecl:
			decls = append(decls, goGenDecl(fset, d, src)...)
		}
	}

	result := File{
		Path:    filePath(abs, opts.Root),
		Lang:    LangGo,
		Backend: BackendGoAST,
		Decls:   decls,
	}
	return finishFile(result), true
}

// goFuncDecl converts a FuncDecl to a Decl slice (always 0 or 1 element).
func goFuncDecl(fset *token.FileSet, d *ast.FuncDecl, src []byte) []Decl {
	name := d.Name.Name
	if name == "_" {
		return nil
	}

	kind := KindFunc
	container := ""
	if d.Recv != nil && len(d.Recv.List) > 0 {
		kind = KindMethod
		container = extractReceiverBase(d.Recv.List[0].Type)
	}

	startLine := fset.Position(d.Pos()).Line
	endLine := fset.Position(d.End()).Line
	exported := ast.IsExported(name)
	sig := goFuncSignature(fset, d, src)

	return []Decl{{
		Kind:      kind,
		Name:      name,
		Container: container,
		Signature: sanitiseSignature(sig),
		Exported:  exported,
		StartLine: startLine,
		EndLine:   endLine,
	}}
}

// goGenDecl converts a GenDecl (type, const, var) to Decl slices.
func goGenDecl(fset *token.FileSet, d *ast.GenDecl, src []byte) []Decl {
	var decls []Decl
	grouped := d.Lparen.IsValid()

	for _, spec := range d.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			name := spec.Name.Name
			if name == "_" {
				continue
			}
			kind := KindType
			if _, ok := spec.Type.(*ast.InterfaceType); ok {
				kind = KindInterface
			}

			var startLine, endLine int
			if grouped {
				startLine = fset.Position(spec.Pos()).Line
				endLine = fset.Position(spec.End()).Line
			} else {
				startLine = fset.Position(d.Pos()).Line
				endLine = fset.Position(d.End()).Line
			}

			sig := goTypeSignature(fset, spec, src)

			decls = append(decls, Decl{
				Kind:      kind,
				Name:      name,
				Container: "",
				Signature: sanitiseSignature(sig),
				Exported:  ast.IsExported(name),
				StartLine: startLine,
				EndLine:   endLine,
			})

		case *ast.ValueSpec:
			kind := KindConst
			if d.Tok == token.VAR {
				kind = KindVar
			}
			for _, ident := range spec.Names {
				if ident.Name == "_" {
					continue
				}

				var startLine, endLine int
				if grouped {
					startLine = fset.Position(spec.Pos()).Line
					endLine = fset.Position(spec.End()).Line
				} else {
					startLine = fset.Position(d.Pos()).Line
					endLine = fset.Position(d.End()).Line
				}

				sig := goValueSignature(fset, d.Tok, ident.Name, spec)

				decls = append(decls, Decl{
					Kind:      kind,
					Name:      ident.Name,
					Container: "",
					Signature: sanitiseSignature(sig),
					Exported:  ast.IsExported(ident.Name),
					StartLine: startLine,
					EndLine:   endLine,
				})
			}
		}
	}
	return decls
}

// extractReceiverBase extracts the base type name from a receiver expression,
// unwrapping *, (), IndexExpr and IndexListExpr.
func extractReceiverBase(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// goFuncSignature builds the "func ..." signature up to the body, on one line.
func goFuncSignature(fset *token.FileSet, d *ast.FuncDecl, src []byte) string {
	// Create a copy without the body and print it.
	cp := *d
	cp.Body = nil
	cp.Doc = nil // exclude doc comment from signature

	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.RawFormat}
	if err := cfg.Fprint(&buf, fset, &cp); err != nil {
		// Fallback: extract from source.
		return goFuncSigFromSource(fset, d, src)
	}
	return buf.String()
}

// goFuncSigFromSource extracts the function signature from source bytes
// as a fallback when go/printer fails.
func goFuncSigFromSource(fset *token.FileSet, d *ast.FuncDecl, src []byte) string {
	start := fset.Position(d.Pos()).Offset
	end := len(src)
	if d.Body != nil {
		end = fset.Position(d.Body.Pos()).Offset
	}
	if start >= 0 && start < len(src) && end <= len(src) {
		return strings.TrimSpace(string(src[start:end]))
	}
	return fmt.Sprintf("func %s", d.Name.Name)
}

// goTypeSignature builds the type signature.
func goTypeSignature(fset *token.FileSet, spec *ast.TypeSpec, src []byte) string {
	name := spec.Name.Name

	switch spec.Type.(type) {
	case *ast.StructType:
		return "type " + name + " struct"
	case *ast.InterfaceType:
		return "type " + name + " interface"
	}

	// For aliases and other types, print the underlying type expression.
	if spec.Assign.IsValid() {
		// Type alias: type Name = Expr
		var buf bytes.Buffer
		cfg := printer.Config{Mode: printer.RawFormat}
		if err := cfg.Fprint(&buf, fset, spec.Type); err == nil {
			return "type " + name + " = " + buf.String()
		}
	}

	// Other types: type Name <underlying>
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.RawFormat}
	if err := cfg.Fprint(&buf, fset, spec.Type); err == nil {
		return "type " + name + " " + buf.String()
	}

	return "type " + name
}

// goValueSignature builds "const Name" or "var Name [Type]".
func goValueSignature(fset *token.FileSet, tok token.Token, name string, spec *ast.ValueSpec) string {
	prefix := "const"
	if tok == token.VAR {
		prefix = "var"
	}

	if spec.Type != nil {
		var buf bytes.Buffer
		cfg := printer.Config{Mode: printer.RawFormat}
		if err := cfg.Fprint(&buf, fset, spec.Type); err == nil {
			return prefix + " " + name + " " + buf.String()
		}
	}

	return prefix + " " + name
}
