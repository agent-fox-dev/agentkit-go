package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

func TestASiteIsAttributedToItsInnermostEnclosingDeclaration(t *testing.T) {
	decls := []outline.Decl{
		{Kind: outline.KindClass, Name: "Engine", StartLine: 10, EndLine: 60},
		{Kind: outline.KindMethod, Name: "Start", Container: "Engine", StartLine: 20, EndLine: 30},
		{Kind: outline.KindFunc, Name: "b", StartLine: 65, EndLine: 70},
		{Kind: outline.KindFunc, Name: "oneLine", StartLine: 80}, // an unknown end is the start line
	}
	for line, want := range map[int]string{25: "Start", 20: "Start", 45: "Engine", 60: "Engine", 65: "b", 70: "b", 80: "oneLine", 5: "", 62: "", 81: ""} {
		got := findEnclosingDecl(decls, line)
		if got.Name != want || (want == "" && got != (outline.Decl{Kind: "file"})) {
			t.Errorf("line %d: %+v, want %q", line, got, want)
		}
	}
	for _, c := range []struct {
		decl outline.Decl
		want string
	}{
		{outline.Decl{Kind: outline.KindMethod, Name: "Run", Signature: "  func (r *Runner) Run() error "}, "func (r *Runner) Run() error"},
		{outline.Decl{Kind: outline.KindClass, Name: "Controller"}, "class Controller"},
		{outline.Decl{Kind: outline.KindModule}, "module"},
		{outline.Decl{Kind: "file"}, "<file>"},
		{outline.Decl{}, "<file>"},
	} {
		if got := renderEnclosingLabel(c.decl); got != c.want {
			t.Errorf("renderEnclosingLabel(%+v) = %q, want %q", c.decl, got, c.want)
		}
	}
	// Wired into References: the Go site's enclosing declaration is the outline's.
	ws := newWS(t, map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n",
		"lib.go": "package m\n\nfunc Target() int { return 1 }\n\nfunc Caller() int {\n\treturn Target()\n}\n"})
	res, err := References(context.Background(), ws, outline.Decl{Kind: outline.KindFunc, Name: "Target", StartLine: 3}, ReferenceOptions{})
	if err != nil || len(res.Sites) != 1 {
		t.Fatalf("%v, %v", siteList(res), err)
	}
	if enc := res.Sites[0].Enclosing; enc.Name != "Caller" || enc.StartLine != 5 || enc.EndLine != 7 || enc.Signature != "func Caller() int" {
		t.Fatalf("Enclosing = %+v", enc)
	}
	// A file that cannot be outlined attributes everything to <file>.
	sites := []ReferenceSite{{Path: "missing.go", Line: 3}}
	attributeSites(sites, func(rel string) []outline.Decl { return outlineDecls(context.Background(), ws, rel) })
	if sites[0].Enclosing != (outline.Decl{Kind: "file"}) {
		t.Fatalf("%+v", sites[0].Enclosing)
	}
}

func TestASnippetIsTrimmedCappedAndFreeOfControlCharacters(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                 "",
		"\t\t\t\n\r":                       "",
		"   leading and trailing   ":       "leading and trailing",
		"\x01\tcalc(1)\t# tab\x7f":         "calc(1) # tab",
		strings.Repeat("a", 200):           strings.Repeat("a", 200),
		strings.Repeat("b", 201):           strings.Repeat("b", 200),
		strings.Repeat("x", 199) + "日本":    strings.Repeat("x", 199), // the cut backs off a split rune
		strings.Repeat("x", 197) + " 🚀 y":  strings.Repeat("x", 197), // and then the trailing space
		strings.Repeat(" ", 50) + "middle": "middle",
	} {
		got := sanitizeSnippet(raw)
		if got != want || len(got) > 200 || !utf8.ValidString(got) {
			t.Errorf("sanitizeSnippet(%.30q) = %.40q, want %.40q", raw, got, want)
		}
	}
	// The scanner's sites carry the sanitized line.
	if s := scanContentForMatches("x.py", []byte("\x01\tcalc(1)\t# tab\x7f\n"), outline.Decl{Name: "calc"}); len(s) != 1 || s[0].Source != "calc(1) # tab" {
		t.Fatalf("%+v", s)
	}
}
