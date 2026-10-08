//go:build cgo

package outline

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unsafe"

	tskotlin "github.com/tree-sitter-grammars/tree-sitter-kotlin/bindings/go"
	tslua "github.com/tree-sitter-grammars/tree-sitter-lua/bindings/go"
	ts "github.com/tree-sitter/go-tree-sitter"
	tsbash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	tscsharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tsphp "github.com/tree-sitter/tree-sitter-php/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsscala "github.com/tree-sitter/tree-sitter-scala/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// grammar is one tree-sitter language: its parser and the tags query that
// picks its declarations out of the syntax tree.
//
// A query pattern captures the declaration node as @definition.<Kind>, with
// <Kind> one of the closed Kind set, and its name as @name. It may capture
// @container, the type an out-of-line definition belongs to (C++
// `void Foo::bar()`). A node captured as @scope is not reported, but gives
// what is declared inside it a container (Rust's `impl Foo`).
type grammar struct {
	load     func() unsafe.Pointer
	query    string
	exported func(d tsDecl) bool
	// moduleIsType makes a module a container of methods (Ruby) instead of a
	// namespace the declarations in it see through.
	moduleIsType bool

	once sync.Once
	lang *ts.Language
	q    *ts.Query
	err  error
}

// grammars maps a language, or ".tsx", to its grammar.
var grammars = map[string]*grammar{
	LangPython:     {load: tspython.Language, query: pythonQuery, exported: notUnderscore},
	LangJavaScript: {load: tsjs.Language, query: jsQuery, exported: jsExported},
	LangTypeScript: {load: tsts.LanguageTypescript, query: tsQuery, exported: jsExported},
	".tsx":         {load: tsts.LanguageTSX, query: tsQuery, exported: jsExported},
	LangJava:       {load: tsjava.Language, query: javaQuery, exported: headerHas(`public`)},
	LangCSharp:     {load: tscsharp.Language, query: csharpQuery, exported: headerHas(`public`)},
	LangKotlin:     {load: tskotlin.Language, query: kotlinQuery, exported: headerLacks(`private|internal|protected`)},
	LangScala:      {load: tsscala.Language, query: scalaQuery, exported: headerLacks(`private|protected`)},
	LangRust:       {load: tsrust.Language, query: rustQuery, exported: headerHas(`pub`)},
	LangC:          {load: tsc.Language, query: cQuery, exported: always},
	LangCPP:        {load: tscpp.Language, query: cppQuery, exported: always},
	LangPHP:        {load: tsphp.LanguagePHP, query: phpQuery, exported: headerLacks(`private|protected`)},
	LangRuby:       {load: tsruby.Language, query: rubyQuery, exported: notUnderscore, moduleIsType: true},
	LangLua:        {load: tslua.Language, query: luaQuery, exported: headerLacks(`local`)},
	LangShell:      {load: tsbash.Language, query: bashQuery, exported: notUnderscore},
}

// grammarFor returns the grammar for a file, or nil.
func grammarFor(lang, ext string) *grammar {
	if strings.EqualFold(ext, ".tsx") {
		lang = ".tsx"
	}
	return grammars[lang]
}

// compile loads the language and compiles its query, once.
func (g *grammar) compile() error {
	g.once.Do(func() {
		g.lang = ts.NewLanguage(g.load())
		q, qerr := ts.NewQuery(g.lang, g.query)
		if qerr != nil {
			g.err = qerr
			return
		}
		g.q = q
	})
	return g.err
}

// parse returns the syntax tree of src, or nil when ctx ended first.
func (g *grammar) parse(ctx context.Context, src []byte) (*ts.Tree, error) {
	if err := g.compile(); err != nil {
		return nil, err
	}
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(g.lang); err != nil {
		return nil, err
	}
	t := p.ParseCtx(ctx, src, nil)
	if t == nil {
		return nil, ctx.Err()
	}
	return t, nil
}

// tsDecl is a declaration a query found, before its container is known.
type tsDecl struct {
	node      ts.Node
	kind      Kind
	name      string
	container string
	header    string // source from the declaration's start to its name
	scope     bool   // a container only, not reported
}

// outlineTreeSitter returns the declarations tree-sitter finds in src.
// ok is false when the language has no grammar.
func outlineTreeSitter(ctx context.Context, lang, ext string, src []byte) (decls []Decl, ok bool, err error) {
	g := grammarFor(lang, ext)
	if g == nil {
		return nil, false, nil
	}
	tree, err := g.parse(ctx, src)
	if err != nil {
		return nil, true, err
	}
	defer tree.Close()

	qc := ts.NewQueryCursor()
	defer qc.Close()
	names := g.q.CaptureNames()
	byID := map[uintptr]int{}
	var found []tsDecl
	matches := qc.Matches(g.q, tree.RootNode(), src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var d tsDecl
		var nameNode *ts.Node
		for _, c := range m.Captures {
			switch capName := names[c.Index]; {
			case capName == "name":
				nameNode = &c.Node
				d.name = c.Node.Utf8Text(src)
			case capName == "container":
				d.container = c.Node.Utf8Text(src)
			case capName == "scope":
				d.node, d.scope = c.Node, true
			case strings.HasPrefix(capName, "definition."):
				d.node, d.kind = c.Node, Kind(strings.TrimPrefix(capName, "definition."))
			}
		}
		if d.name == "" || nameNode == nil {
			continue
		}
		if i := strings.LastIndex(d.name, "::"); i > 0 && d.container != "" {
			// C++ `void ns::Widget::draw()`: the container is ns::Widget.
			d.container, d.name = d.container+"::"+d.name[:i], d.name[i+2:]
		}
		if _, dup := byID[d.node.Id()]; dup {
			continue // a later pattern matched the same declaration
		}
		d.header = string(src[d.node.StartByte():max(nameNode.StartByte(), d.node.StartByte())])
		byID[d.node.Id()] = len(found)
		found = append(found, d)
	}

	lines := lineStarts(src)
	for _, d := range found {
		if d.scope || !g.resolveContainer(&d, found, byID) {
			continue
		}
		// The declaration starts on the line of its name, as the signature
		// does: a Java annotation or a C# attribute in front of it is not
		// its first line, as a Go doc comment is not.
		start := int(d.node.StartPosition().Row) + strings.Count(d.header, "\n")
		end := int(d.node.EndPosition().Row)
		if end > start && d.node.EndPosition().Column == 0 {
			end-- // a node ending at a newline ends on the line before
		}
		decls = append(decls, Decl{
			Kind:      d.kind,
			Name:      d.name,
			Container: d.container,
			Signature: sanitiseSignature(lineAt(src, lines, start)),
			Exported:  g.exported(d),
			StartLine: start + 1,
			EndLine:   end + 1,
		})
	}
	return decls, true, nil
}

// resolveContainer sets d's container from the nearest declaration
// enclosing it and reports whether d is kept: a declaration inside a
// function is a local and is dropped; a function inside a type is a method
// of it; a namespace is seen through.
func (g *grammar) resolveContainer(d *tsDecl, found []tsDecl, byID map[uintptr]int) bool {
	for p := d.node.Parent(); p != nil; p = p.Parent() {
		i, ok := byID[p.Id()]
		if !ok {
			continue
		}
		outer := found[i]
		switch {
		case outer.kind == KindFunc || outer.kind == KindMethod:
			return false
		case outer.kind == KindModule && !g.moduleIsType, outer.node.Kind() == "type_definition":
			// A namespace is seen through, and so is a C typedef: it names
			// the struct declared in it rather than containing it.
			continue
		}
		if d.container == "" {
			d.container = outer.name
		}
		break
	}
	if d.kind == KindFunc && d.container != "" {
		d.kind = KindMethod
	}
	if d.kind == KindMethod && d.container == "" {
		d.kind = KindFunc
	}
	return true
}

// lineStarts returns the byte offset each line of src starts at.
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineAt returns 0-based line n of src without its line ending.
func lineAt(src []byte, starts []int, n int) string {
	if n < 0 || n >= len(starts) {
		return ""
	}
	end := len(src)
	if n+1 < len(starts) {
		end = starts[n+1]
	}
	return strings.TrimRight(string(src[starts[n]:end]), "\r\n")
}

// CommentAndStringSpans returns the byte ranges [start, end) of the comments
// and string literals in src, in order, as the language's tree-sitter grammar
// parses them. ok is false when the language has no grammar or ctx ended.
func CommentAndStringSpans(ctx context.Context, path string, src []byte) (spans [][2]int, ok bool) {
	g := grammarFor(LangFor(path, src), filepath.Ext(path))
	if g == nil {
		return nil, false
	}
	tree, err := g.parse(ctx, src)
	if err != nil {
		return nil, false
	}
	defer tree.Close()
	c := tree.Walk()
	defer c.Close()
	for {
		n := c.Node()
		if n.IsNamed() && isLiteralKind(n.Kind()) {
			spans = append(spans, [2]int{int(n.StartByte()), int(n.EndByte())})
		} else if c.GotoFirstChild() {
			continue
		}
		for !c.GotoNextSibling() {
			if !c.GotoParent() {
				return spans, true
			}
		}
	}
}

// isLiteralKind reports whether a named node kind is a comment or a string.
func isLiteralKind(kind string) bool {
	return strings.Contains(kind, "comment") || strings.Contains(kind, "string") ||
		strings.Contains(kind, "heredoc") || slices.Contains([]string{"char_literal", "character_literal", "text_block", "nowdoc"}, kind)
}

// Exported rules.

func notUnderscore(d tsDecl) bool { return !strings.HasPrefix(d.name, "_") }
func always(tsDecl) bool          { return true }

// headerHas is exported when one of the words is among the modifiers in
// front of the name.
func headerHas(words string) func(tsDecl) bool {
	re := regexp.MustCompile(`\b(?:` + words + `)\b`)
	return func(d tsDecl) bool { return re.MatchString(d.header) }
}

// headerLacks is exported unless one of the words is among the modifiers.
func headerLacks(words string) func(tsDecl) bool {
	has := headerHas(words)
	return func(d tsDecl) bool { return !has(d) }
}

var isPrivate = headerHas(`private`)

// jsExported is exported when the declaration is inside an export statement
// and is not private (`#name`, or TypeScript's `private`).
func jsExported(d tsDecl) bool {
	if strings.HasPrefix(d.name, "#") || isPrivate(d) {
		return false
	}
	for p := d.node.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "export_statement" {
			return true
		}
	}
	return false
}
