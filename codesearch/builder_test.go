//go:build !windows

package codesearch_test

import (
	"context"
	"fmt"
	"testing"

	zoekt "github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"
)

// TS-03-64: TestBuilderAcceptsExternalSymbols verifies that the zoekt builder
// accepts symbol data from outside (Document.Symbols and SymbolsMetaData)
// with CTagsPath empty, and that sym: queries match the supplied symbols.
// Verifies 03-REQ-9.4.
func TestBuilderAcceptsExternalSymbols_TS03_64(t *testing.T) {
	dir := t.TempDir()

	result, err := buildAndSearchWithSymbols(dir)
	if err != nil {
		t.Fatalf("buildAndSearchWithSymbols: %v", err)
	}

	// The sym: query should match the document with the supplied symbol.
	if result.symHitFile == "" {
		t.Fatal("sym:MyFunc query returned no file match")
	}
	if result.symHitFile != "main.go" {
		t.Errorf("sym:MyFunc matched file %q, want main.go", result.symHitFile)
	}

	// The symbol hit should outrank the comment hit.
	if result.symScore <= result.commentScore {
		t.Errorf("symbol hit score (%.2f) should outrank comment hit score (%.2f)",
			result.symScore, result.commentScore)
	}
}

type symbolSearchResult struct {
	symHitFile   string
	symScore     float64
	commentScore float64
}

// buildAndSearchWithSymbols builds a zoekt index with externally-supplied
// symbol data (CTagsPath empty) and searches it with sym: and plain queries.
func buildAndSearchWithSymbols(dir string) (*symbolSearchResult, error) {
	opts := index.Options{
		IndexDir:     dir,
		DisableCTags: true,
		RepositoryDescription: zoekt.Repository{
			Name: "test-repo",
			Branches: []zoekt.RepositoryBranch{
				{Name: "HEAD", Version: "abc123"},
			},
		},
	}

	builder, err := index.NewBuilder(opts)
	if err != nil {
		return nil, fmt.Errorf("NewBuilder: %w", err)
	}

	// Document with a symbol section: "func MyFunc() {}" at bytes 0-17.
	symContent := []byte("func MyFunc() {}\n// some other code\n")
	symDoc := index.Document{
		Name:     "main.go",
		Content:  symContent,
		Language: "Go",
		Branches: []string{"HEAD"},
		Symbols: []index.DocumentSection{
			{Start: 0, End: 17}, // "func MyFunc() {}"
		},
		SymbolsMetaData: []*zoekt.Symbol{
			{Sym: "MyFunc", Kind: "function"},
		},
	}

	// Document with only a comment mentioning MyFunc (no symbol section).
	commentContent := []byte("// MyFunc is referenced here in a comment\nvar x = 1\n")
	commentDoc := index.Document{
		Name:     "comment.go",
		Content:  commentContent,
		Language: "Go",
		Branches: []string{"HEAD"},
	}

	if err := builder.Add(symDoc); err != nil {
		return nil, fmt.Errorf("Add symDoc: %w", err)
	}
	if err := builder.Add(commentDoc); err != nil {
		return nil, fmt.Errorf("Add commentDoc: %w", err)
	}
	if err := builder.Finish(); err != nil {
		return nil, fmt.Errorf("Finish: %w", err)
	}

	// Open the searcher.
	searcher, err := search.NewDirectorySearcher(dir)
	if err != nil {
		return nil, fmt.Errorf("NewDirectorySearcher: %w", err)
	}
	defer searcher.Close()

	// Search with sym:MyFunc.
	symQ, err := query.Parse("sym:MyFunc")
	if err != nil {
		return nil, fmt.Errorf("Parse sym:MyFunc: %w", err)
	}

	ctx := context.Background()
	symResult, err := searcher.Search(ctx, symQ, &zoekt.SearchOptions{
		ChunkMatches: true,
	})
	if err != nil {
		return nil, fmt.Errorf("Search sym:MyFunc: %w", err)
	}

	result := &symbolSearchResult{}

	if len(symResult.Files) > 0 {
		result.symHitFile = symResult.Files[0].FileName
		result.symScore = symResult.Files[0].Score
	}

	// Search with plain "MyFunc" to get the comment hit score.
	plainQ, err := query.Parse("MyFunc")
	if err != nil {
		return nil, fmt.Errorf("Parse MyFunc: %w", err)
	}

	plainResult, err := searcher.Search(ctx, plainQ, &zoekt.SearchOptions{
		ChunkMatches: true,
	})
	if err != nil {
		return nil, fmt.Errorf("Search MyFunc: %w", err)
	}

	// Find the comment.go score in the plain results.
	for _, fm := range plainResult.Files {
		if fm.FileName == "comment.go" {
			result.commentScore = fm.Score
		}
	}

	return result, nil
}
