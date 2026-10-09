package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// renderEnclosingLabel renders the signature or '<Kind> <Name>' for a declaration,
// or '<file>' when Kind equals 'file' or is empty.
func renderEnclosingLabel(decl outline.Decl) string {
	if decl.Kind == "file" || decl.Kind == "" {
		return "<file>"
	}
	sig := strings.TrimSpace(decl.Signature)
	if sig != "" {
		return sig
	}
	if decl.Name != "" {
		return fmt.Sprintf("%s %s", decl.Kind, decl.Name)
	}
	return string(decl.Kind)
}

// sanitizeSnippet trims whitespace, caps at 200 bytes, and replaces ASCII control characters (< 0x20) with spaces.
func sanitizeSnippet(rawLine string) string {
	var b strings.Builder
	for _, r := range rawLine {
		if r < 32 || r == 127 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	if len(res) > 200 {
		res = res[:200]
		// Backtrack if truncated inside a multi-byte UTF-8 sequence.
		for len(res) > 0 && !utf8.ValidString(res) {
			res = res[:len(res)-1]
		}
		res = strings.TrimRight(res, " ")
	}
	return res
}

// findEnclosingDecl returns the innermost declaration of decls whose line
// range spans line (05-REQ-5.1, 05-REQ-5.2), or a zero Decl with Kind
// "file" when none does (05-REQ-5.3).
func findEnclosingDecl(decls []outline.Decl, line int) outline.Decl {
	best, bestSpan := outline.Decl{Kind: "file"}, -1
	for _, d := range decls {
		end := max(d.EndLine, d.StartLine) // EndLine 0 means unknown
		if d.StartLine > line || end < line {
			continue
		}
		// On equal spans the later declaration is the nested one.
		if span := end - d.StartLine; bestSpan < 0 || span <= bestSpan {
			best, bestSpan = d, span
		}
	}
	return best
}

// attributeSites sets each site's Enclosing from its file's outline, as
// returned by outlineOf for a workspace-relative path. A file without an
// outline attributes every site to <file>.
func attributeSites(sites []ReferenceSite, outlineOf func(rel string) []outline.Decl) {
	decls := make(map[string][]outline.Decl)
	for i := range sites {
		d, ok := decls[sites[i].Path]
		if !ok {
			d = outlineOf(sites[i].Path)
			decls[sites[i].Path] = d
		}
		sites[i].Enclosing = findEnclosingDecl(d, sites[i].Line)
	}
}

// outlineDecls outlines the workspace file rel, returning nil when it
// cannot be outlined.
func outlineDecls(ctx context.Context, ws *Workspace, rel string) []outline.Decl {
	f, err := outline.Outline(ctx, filepath.Join(ws.Root, filepath.FromSlash(rel)), nil, outline.Options{Root: ws.Root})
	if err != nil {
		return nil
	}
	return f.Decls
}
