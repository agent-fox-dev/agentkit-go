package tools

import (
	"context"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// SymbolQuery carries find_symbol's validated arguments for the Index.Symbols
// method. Path is a slash-separated workspace-relative directory (empty for
// the whole workspace) and Kind is already lower-cased.
type SymbolQuery struct {
	Name  string
	Kind  string
	Path  string
	Exact bool
}

// SymbolAnswer is the response from Index.Symbols.
//
// FilesIndexed and Backends describe the queried scope, as they do for the
// table-based find_symbol: FilesIndexed is the number of indexed files under
// SymbolQuery.Path (the whole workspace when it is empty), and Backends maps
// each outline backend name to its number of those files. A nil Backends
// means the Index does not report them, and find_symbol counts the backends
// of the matches instead.
type SymbolAnswer struct {
	Matches      []SymbolMatch
	FilesIndexed int
	Backends     map[string]int
}

// Index is the standard-library-only seam through which a codesearch index
// plugs into the root tools package. An embedder sets Options.Index to an
// implementation; nil means no index.
type Index interface {
	// Symbols answers a find_symbol query from the index. It returns
	// ok=true only when the index can answer authoritatively; ok=false
	// means the caller should fall back to its own table.
	Symbols(ctx context.Context, q SymbolQuery) (SymbolAnswer, bool, error)

	// Tools returns the tools the index provides (e.g. code_search).
	Tools() []core.Tool

	// Invalidate records rel as dirty. An empty rel marks the whole index
	// for revalidation. It must not block on a build or query in progress
	// for longer than it takes to set a flag; it never returns an error
	// and never panics on a closed index.
	Invalidate(rel string)

	// Close releases resources. It is idempotent.
	Close() error
}

// CapMarker is the exported wrapper of capMarker. It produces the shared
// truncation-marker wording so that codesearch and the built-in tools use
// identical text.
func CapMarker(noun, param string, limit, max int, alternative string) string {
	return capMarker(noun, param, limit, max, alternative)
}

// ClampLimit is the exported wrapper of clampLimit. It applies a tool's
// default and cap to a model-supplied limit.
func ClampLimit(n, def, cap int) int {
	return clampLimit(n, def, cap)
}
