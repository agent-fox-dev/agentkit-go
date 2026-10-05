//go:build !windows

package codesearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"
)

// maxSymbolMatches is the maximum number of matches Symbols will return
// with ok=true. Above this threshold it returns ok=false so find_symbol
// falls back to its own table.
const maxSymbolMatches = 1000

// Symbols implements tools.Index. It answers from the per-file outline.File
// data collected by the build, using the matching rules of
// 02_symbol_navigation_tools Requirement 2 (smart-case prefix, exact,
// qualified names, kind, path). It returns ok=true only when the index has
// been built by an earlier code_search, is not partial and not closed, and
// the matches number at most 1000. It never starts a build.
func (idx *Index) Symbols(ctx context.Context, q tools.SymbolQuery) (tools.SymbolAnswer, bool, error) {
	// Check context first.
	if ctx.Err() != nil {
		return tools.SymbolAnswer{}, false, ctx.Err()
	}

	idx.mu.RLock()
	closed := idx.closed
	built := idx.built
	partial := idx.partial
	idx.mu.RUnlock()

	if closed {
		return tools.SymbolAnswer{}, false, nil
	}
	if !built {
		return tools.SymbolAnswer{}, false, nil
	}

	// A query of a live index keeps its run directory young for a sibling
	// index's sweep, whether or not this one is answered from the outlines.
	idx.touchRunDir()

	if partial {
		return tools.SymbolAnswer{}, false, nil
	}

	// Run any pending revalidation and re-outline dirty files before answering.
	if err := idx.symbolsHandleDirty(ctx); err != nil {
		return tools.SymbolAnswer{}, false, err
	}

	// Check context again after dirty handling.
	if ctx.Err() != nil {
		return tools.SymbolAnswer{}, false, ctx.Err()
	}

	// Collect matches from the outline data.
	idx.mu.RLock()
	outlineFiles := idx.outlineFiles
	filesIndexed := len(idx.indexedFiles)
	idx.mu.RUnlock()

	// Get the current dirty set to apply overlays.
	dirtySet := idx.dirty.dirtyPathSet()

	// Build the scope prefix from the query path.
	scopePrefix := q.Path
	if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
		scopePrefix += "/"
	}

	qualified := strings.Contains(q.Name, ".")
	caseSensitive := symbolHasUppercase(q.Name)

	var matches []tools.SymbolMatch

	// Search indexed (non-dirty) outline files.
	for rel, of := range outlineFiles {
		if dirtySet[rel] {
			continue // skip dirty files; they'll be re-outlined below
		}
		if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
			continue
		}
		for _, d := range of.Decls {
			if matchesSymbolQuery(q, d, qualified, caseSensitive) {
				matches = append(matches, declToSymbolMatch(rel, of, d))
			}
		}
	}

	// Re-outline dirty files in scope and search them.
	if len(dirtySet) > 0 {
		runner := idx.outlineRunner()
		for rel := range dirtySet {
			if idx.dirty.isGone(rel) {
				continue
			}
			if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
				continue
			}

			abs := filepath.Join(idx.ws.Root, filepath.FromSlash(rel))
			fi, err := os.Stat(abs)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}

			of, err := outline.Outline(ctx, abs, nil, outline.Options{
				Root:   idx.ws.Root,
				Runner: runner,
			})
			if err != nil {
				if ctx.Err() != nil {
					return tools.SymbolAnswer{}, false, ctx.Err()
				}
				continue
			}

			for _, d := range of.Decls {
				if matchesSymbolQuery(q, d, qualified, caseSensitive) {
					matches = append(matches, declToSymbolMatch(rel, of, d))
				}
			}
		}
	}

	// If more than maxSymbolMatches, return ok=false.
	if len(matches) > maxSymbolMatches {
		return tools.SymbolAnswer{}, false, nil
	}

	return tools.SymbolAnswer{
		Matches:      matches,
		FilesIndexed: filesIndexed,
	}, true, nil
}

// symbolsHandleDirty runs revalidation if needed before answering a Symbols
// query. It mirrors handleDirty but respects the caller's context.
func (idx *Index) symbolsHandleDirty(ctx context.Context) error {
	if !idx.dirty.hasDirty() {
		return nil
	}

	idx.mu.RLock()
	indexedFiles := idx.indexedFiles
	fileInfos := idx.fileInfos
	idx.mu.RUnlock()

	if idx.dirty.needsRevalidation(indexedFiles) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if idx.testRevalHook != nil {
			idx.testRevalHook()
		}
		idx.dirty.revalidate(idx.ws, idx.opts.Ignore, indexedFiles, fileInfos)
		idx.revalCount.Add(1)
	}

	return nil
}

// matchesSymbolQuery checks whether a declaration matches the SymbolQuery
// using the same rules as matchSymbols in tools/symbol_match.go:
// smart-case prefix, exact, qualified Container.Name, kind, path.
func matchesSymbolQuery(q tools.SymbolQuery, d outline.Decl, qualified, caseSensitive bool) bool {
	// Kind filter.
	if q.Kind != "" && string(d.Kind) != q.Kind {
		return false
	}

	// Name matching.
	if qualified {
		return matchQualifiedDecl(q.Name, d, q.Exact, caseSensitive)
	}
	return matchDeclName(q.Name, d.Name, q.Exact, caseSensitive)
}

// matchQualifiedDecl matches a dotted name against Container.Name.
func matchQualifiedDecl(name string, d outline.Decl, exact, caseSensitive bool) bool {
	if d.Container == "" {
		return false
	}
	qualifiedName := d.Container + "." + d.Name
	return matchDeclName(name, qualifiedName, exact, caseSensitive)
}

// matchDeclName performs the actual string comparison: exact or prefix, with
// case sensitivity determined by smart-case rules.
func matchDeclName(query, target string, exact, caseSensitive bool) bool {
	if exact {
		return query == target
	}
	if caseSensitive {
		return strings.HasPrefix(target, query)
	}
	return len(target) >= len(query) &&
		strings.EqualFold(target[:len(query)], query)
}

// symbolHasUppercase returns true if s contains any uppercase rune.
func symbolHasUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// declToSymbolMatch converts an outline.Decl to a tools.SymbolMatch.
func declToSymbolMatch(rel string, of outline.File, d outline.Decl) tools.SymbolMatch {
	return tools.SymbolMatch{
		Path:      rel,
		Backend:   string(of.Backend),
		Kind:      string(d.Kind),
		Name:      d.Name,
		Container: d.Container,
		Signature: d.Signature,
		Exported:  d.Exported,
		StartLine: d.StartLine,
		EndLine:   d.EndLine,
	}
}
