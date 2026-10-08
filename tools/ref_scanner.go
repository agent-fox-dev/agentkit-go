package tools

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/outline"
)

// scanMatch represents an identifier match found in a source file.
type scanMatch struct {
	Path       string
	Line       int
	Column     int
	Confidence string
	Enclosing  outline.Decl
	Source     string
	LineText   string
}

// Site converts a scanMatch to an exported ReferenceSite.
func (m scanMatch) Site() ReferenceSite {
	return ReferenceSite{
		Path:       m.Path,
		Line:       m.Line,
		Column:     m.Column,
		Confidence: m.Confidence,
		Enclosing:  m.Enclosing,
		Source:     m.Source,
	}
}

var currentScannerWorkspace *Workspace

// setScannerWorkspace sets the workspace used by scanner helpers.
func setScannerWorkspace(ws *Workspace) {
	currentScannerWorkspace = ws
}

// candidateIndexWithCtx is an optional Index interface providing context-aware candidate lookup.
type candidateIndexWithCtx interface {
	CandidateFiles(ctx context.Context, name string) ([]string, error)
}

// candidateIndexSimple is an optional Index interface providing simple candidate lookup.
type candidateIndexSimple interface {
	CandidateFiles(name string) []string
}

// candidateSearcherWithCtx is an optional Index interface providing Candidates lookup.
type candidateSearcherWithCtx interface {
	Candidates(ctx context.Context, name string) ([]string, error)
}

// candidateSearcherSimple is an optional Index interface providing Candidates lookup.
type candidateSearcherSimple interface {
	Candidates(name string) []string
}

// isAnyComponentHidden reports whether any path component starts with a dot.
func isAnyComponentHidden(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

// isBinary checks if the first 512 bytes contain a NUL byte.
func isBinary(data []byte) bool {
	n := len(data)
	if n > 512 {
		n = 512
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// isPathIgnored checks if the path or any of its parent directories are ignored.
func isPathIgnored(ig *ignoreEngine, rel string) bool {
	if ig.match(rel, false) {
		return true
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		if ig.match(parent, true) {
			return true
		}
	}
	return false
}

// findCandidateFiles finds workspace-relative file paths that mention name,
// respecting .gitignore, hidden entry exclusion, and index candidate acceleration.
func findCandidateFiles(ws *Workspace, name string, idx Index) ([]string, error) {
	return findCandidateFilesCtx(context.Background(), ws, name, idx)
}

// findCandidateFilesCtx is findCandidateFiles with context.
func findCandidateFilesCtx(ctx context.Context, ws *Workspace, name string, idx Index) ([]string, error) {
	if ws == nil {
		return nil, ErrPathNotAllowed
	}

	var candidates []string
	ig := newIgnoreEngine(ws.Root, IgnoreOptions{})

	// Check if idx provides candidate files
	var fromIndex []string
	if idx != nil {
		if cIdx, ok := idx.(candidateIndexWithCtx); ok {
			cands, err := cIdx.CandidateFiles(ctx, name)
			if err == nil {
				fromIndex = cands
			}
		} else if cIdx, ok := idx.(candidateIndexSimple); ok {
			fromIndex = cIdx.CandidateFiles(name)
		} else if cIdx, ok := idx.(candidateSearcherWithCtx); ok {
			cands, err := cIdx.Candidates(ctx, name)
			if err == nil {
				fromIndex = cands
			}
		} else if cIdx, ok := idx.(candidateSearcherSimple); ok {
			fromIndex = cIdx.Candidates(name)
		}
	}

	if fromIndex != nil {
		// Filter index results by ignore rules, hidden exclusions, and existence
		for _, rel := range fromIndex {
			rel = filepath.ToSlash(filepath.Clean(rel))
			if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || rel == ".." {
				continue
			}
			if isAnyComponentHidden(rel) {
				continue
			}
			if isPathIgnored(ig, rel) {
				continue
			}
			abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
			fi, err := os.Stat(abs)
			if err != nil || fi.IsDir() {
				continue
			}
			candidates = append(candidates, rel)
		}
		return candidates, nil
	}

	// Native walk
	nameBytes := []byte(name)
	err := Walk(ctx, ws, ws.Root, WalkOptions{Ignore: IgnoreOptions{}, IncludeHidden: false}, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
		content, err := os.ReadFile(abs)
		if err != nil {
			return nil
		}
		if isBinary(content) {
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

// sanitizeSourceLine strips control chars, replaces tabs/spaces, trims, and caps at 200 bytes.
func sanitizeSourceLine(line string) string {
	line = strings.TrimSpace(line)
	var b strings.Builder
	for _, r := range line {
		if r < 32 && r != '\t' {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// scanContentForMatches scans content bytes for identifier occurrences of target.
func scanContentForMatches(path string, content []byte, name string, target outline.Decl) []scanMatch {
	searchName := name
	if target.Name != "" {
		searchName = target.Name
	} else {
		_, member := splitContainerMember(name)
		if member != "" {
			searchName = member
		}
	}

	if searchName == "" {
		return nil
	}

	lang := outline.LangFor(path, content)
	targetDeclared := target.Name != "" || target.Kind != ""
	// A hit is lexical only when the language's grammar says it is outside
	// any comment or string; without a grammar (Go, which is resolved by
	// type, or any language in a build without cgo) every hit is text.
	spans, classified := outline.CommentAndStringSpans(context.Background(), path, content)

	var matches []scanMatch
	nameLen := len(searchName)

	lineOff := 0
	for lineIdx, line := range strings.Split(string(content), "\n") {
		off := lineOff
		lineOff += len(line) + 1
		if len(line) < nameLen {
			continue
		}

		start := 0
		for start+nameLen <= len(line) {
			idx := strings.Index(line[start:], searchName)
			if idx == -1 {
				break
			}

			matchStart := start + idx
			matchEnd := matchStart + nameLen

			// Check identifier boundaries
			validBoundary := true
			if matchStart > 0 {
				prevRune, _ := utf8.DecodeLastRuneInString(line[:matchStart])
				if isIdentRune(prevRune, lang) {
					validBoundary = false
				}
			}
			if validBoundary && matchEnd < len(line) {
				nextRune, _ := utf8.DecodeRuneInString(line[matchEnd:])
				if isIdentRune(nextRune, lang) {
					validBoundary = false
				}
			}

			if validBoundary {
				confidence := "text"
				if targetDeclared && classified && !inSpans(spans, off+matchStart) {
					confidence = "lexical"
				}

				matches = append(matches, scanMatch{
					Path:       path,
					Line:       lineIdx + 1,
					Column:     matchStart + 1,
					Confidence: confidence,
					Enclosing:  outline.Decl{Kind: "file"},
					Source:     sanitizeSourceLine(line),
					LineText:   line,
				})
			}

			start = matchStart + 1
		}
	}

	return matches
}

// scanFileForName scans a single file for whole-identifier occurrences of name.
func scanFileForName(path string, name string) []scanMatch {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return scanContentForMatches(path, content, name, outline.Decl{Name: name})
}

// scanNonGoSite scans a single non-Go code snippet or file for target.
func scanNonGoSite(path string, content string, target outline.Decl) ReferenceSite {
	matches := scanContentForMatches(path, []byte(content), target.Name, target)
	if len(matches) > 0 {
		return matches[0].Site()
	}
	return ReferenceSite{}
}

// scanWorkspaceCandidates scans candidate files across the workspace for target.
func scanWorkspaceCandidates(ctx context.Context, ws *Workspace, name string, target outline.Decl, idx Index) []ReferenceSite {
	searchName := name
	if target.Name != "" {
		searchName = target.Name
	} else {
		_, member := splitContainerMember(name)
		if member != "" {
			searchName = member
		}
	}

	candidates, err := findCandidateFilesCtx(ctx, ws, searchName, idx)
	if err != nil {
		return nil
	}

	var sites []ReferenceSite
	for _, rel := range candidates {
		abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
		content, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		matches := scanContentForMatches(rel, content, searchName, target)
		for _, m := range matches {
			sites = append(sites, m.Site())
		}
	}

	return sites
}

// scanAllSites scans the workspace for all reference occurrences of name with target.
func scanAllSites(args ...any) []ReferenceSite {
	var ws *Workspace
	var name string
	var target outline.Decl

	for _, arg := range args {
		switch v := arg.(type) {
		case *Workspace:
			ws = v
		case string:
			name = v
		case outline.Decl:
			target = v
		}
	}
	if ws == nil {
		ws = currentScannerWorkspace
	}
	if ws == nil {
		ws = lastWorkspace
	}
	if ws == nil {
		return nil
	}
	return scanWorkspaceCandidates(context.Background(), ws, name, target, nil)
}

// isCommentOrString checks if a reference site represents a match inside comments or strings.
func isCommentOrString(s ReferenceSite) bool {
	src := s.Source
	return strings.Contains(src, "#") || strings.Contains(src, "//") || strings.Contains(src, "/*") || strings.Contains(src, `"`) || strings.Contains(src, `'`)
}

// isMarkdown checks if a reference site is in a markdown file.
func isMarkdown(s ReferenceSite) bool {
	return strings.HasSuffix(s.Path, ".md")
}
