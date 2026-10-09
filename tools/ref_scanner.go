package tools

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// candidateIndex is the optional Index extension that answers which files
// mention a name, sparing find_references a workspace walk.
type candidateIndex interface {
	CandidateFiles(ctx context.Context, name string) ([]string, error)
}

// isAnyComponentHidden reports whether any path component starts with a dot.
func isAnyComponentHidden(rel string) bool {
	return slices.ContainsFunc(strings.Split(rel, "/"), isHidden)
}

// isBinary checks if the first 512 bytes contain a NUL byte.
func isBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 512)], 0) >= 0
}

// isPathIgnored checks if the path or any of its parent directories are ignored.
func isPathIgnored(ig *ignoreEngine, rel string) bool {
	if ig.match(rel, false) {
		return true
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if ig.match(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return false
}

// findCandidateFiles finds workspace-relative file paths that mention name,
// respecting .gitignore and hidden entry exclusion. An idx implementing
// candidateIndex answers instead of a walk; its answer is filtered by the
// same rules. Each file inspected is taken from budget; when it refuses,
// the files found so far are returned.
func findCandidateFiles(ctx context.Context, ws *Workspace, name string, idx Index, budget *refBudget) ([]string, error) {
	if ws == nil {
		return nil, ErrPathNotAllowed
	}

	if cIdx, ok := idx.(candidateIndex); ok {
		if fromIndex, err := cIdx.CandidateFiles(ctx, name); err == nil && fromIndex != nil {
			ig := newIgnoreEngine(ws.Root, IgnoreOptions{})
			var candidates []string
			for _, rel := range fromIndex {
				rel = filepath.ToSlash(filepath.Clean(rel))
				if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || rel == ".." {
					continue
				}
				if isAnyComponentHidden(rel) || isPathIgnored(ig, rel) {
					continue
				}
				if !budget.take() {
					break
				}
				fi, err := os.Stat(filepath.Join(ws.Root, filepath.FromSlash(rel)))
				if err != nil || fi.IsDir() {
					continue
				}
				candidates = append(candidates, rel)
			}
			return candidates, nil
		}
	}

	var candidates []string
	nameBytes := []byte(name)
	err := Walk(ctx, ws, ws.Root, WalkOptions{}, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if !budget.take() {
			return filepath.SkipAll
		}
		content, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(rel)))
		if err != nil || isBinary(content) {
			return nil
		}
		if bytes.Contains(content, nameBytes) {
			candidates = append(candidates, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// isIdentRune reports whether r is an identifier rune for the given language.
func isIdentRune(r rune, lang string) bool {
	if r == '_' {
		return true
	}
	if (lang == outline.LangJavaScript || lang == outline.LangTypeScript) && r == '$' {
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// inSpans reports whether offset off lies in one of the sorted,
// non-overlapping [start, end) spans.
func inSpans(spans [][2]int, off int) bool {
	i := sort.Search(len(spans), func(i int) bool { return spans[i][1] > off })
	return i < len(spans) && spans[i][0] <= off
}

// scanContentForMatches scans content for whole-identifier occurrences of
// target.Name. A hit is lexical when the language's grammar places it
// outside any comment or string; otherwise it is text.
func scanContentForMatches(path string, content []byte, target outline.Decl) []ReferenceSite {
	name := target.Name
	if name == "" {
		return nil
	}

	lang := outline.LangFor(path, content)
	// Without a grammar (Go, which is resolved by type, or any language in
	// a build without cgo) every hit is text.
	spans, classified := outline.CommentAndStringSpans(context.Background(), path, content)

	var sites []ReferenceSite
	lineOff := 0
	for lineIdx, line := range strings.Split(string(content), "\n") {
		off := lineOff
		lineOff += len(line) + 1

		for start := 0; start+len(name) <= len(line); {
			idx := strings.Index(line[start:], name)
			if idx == -1 {
				break
			}
			matchStart := start + idx
			matchEnd := matchStart + len(name)
			start = matchStart + 1

			if matchStart > 0 {
				if prev, _ := utf8.DecodeLastRuneInString(line[:matchStart]); isIdentRune(prev, lang) {
					continue
				}
			}
			if matchEnd < len(line) {
				if next, _ := utf8.DecodeRuneInString(line[matchEnd:]); isIdentRune(next, lang) {
					continue
				}
			}

			confidence := "text"
			if classified && !inSpans(spans, off+matchStart) {
				confidence = "lexical"
			}
			sites = append(sites, ReferenceSite{
				Path:       path,
				Line:       lineIdx + 1,
				Column:     matchStart + 1,
				Confidence: confidence,
				Source:     sanitizeSnippet(line),
			})
		}
	}
	return sites
}
