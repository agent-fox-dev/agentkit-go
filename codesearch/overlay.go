//go:build !windows

package codesearch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/agentfox/agentkit-go/outline"

	zoekt "github.com/sourcegraph/zoekt"
	zoektindex "github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"
)

// overlayState holds the state of the overlay shard for dirty files.
type overlayState struct {
	// dirtySet is the set of dirty paths the overlay was built for.
	// If the current dirty set matches this, the overlay is reused.
	dirtySet map[string]bool

	// dir is the directory containing the overlay shard files. It is a
	// subdirectory of runDir, the run directory the overlay was built
	// against, so the sweep of a dead process's run directory takes it too
	// (spec 03 §4).
	dir    string
	runDir string

	// outlineFiles holds the outline.File per overlaid file.
	outlineFiles map[string]outline.File
}

// buildOverlayShard builds a small zoekt index containing only the dirty files
// that still exist on disk. It returns the overlay state or nil if no dirty
// files are searchable.
func (idx *Index) buildOverlayShard(ctx context.Context, dirtySet map[string]bool) (*overlayState, error) {
	// Collect dirty files that still exist and are not gone.
	type fileEntry struct {
		rel string
		abs string
	}
	var files []fileEntry

	for rel := range dirtySet {
		if idx.dirty.isGone(rel) {
			continue
		}
		abs := filepath.Join(idx.ws.Root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil {
			continue // file doesn't exist
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if fi.Size() > maxFileSize {
			continue
		}
		// Binary check.
		f, err := os.Open(abs)
		if err != nil {
			continue
		}
		buf := make([]byte, binarySniffSize)
		n, _ := f.Read(buf)
		f.Close()
		if bytes.IndexByte(buf[:n], 0) >= 0 {
			continue
		}
		files = append(files, fileEntry{rel: rel, abs: abs})
	}

	if len(files) == 0 {
		return &overlayState{
			dirtySet:     copyStringBoolMap(dirtySet),
			outlineFiles: make(map[string]outline.File),
		}, nil
	}

	// Outline the dirty files.
	runner := idx.outlineRunner()
	outlineOpts := outline.Options{
		Root:   idx.ws.Root,
		Runner: runner,
	}

	srcs := make([]outline.Source, len(files))
	for i, fe := range files {
		srcs[i] = outline.Source{Abs: fe.abs}
	}

	outFiles, _, err := outline.OutlineMany(ctx, srcs, outlineOpts)
	if err != nil {
		// Non-fatal: continue without symbols.
		outFiles = make([]outline.File, len(files))
		for i, fe := range files {
			outFiles[i] = outline.File{
				Path:    fe.rel,
				Backend: outline.BackendNone,
			}
		}
	}

	outlineResults := make(map[string]outline.File, len(files))
	for i, fe := range files {
		if i < len(outFiles) {
			outlineResults[fe.rel] = outFiles[i]
		}
	}

	// Create the overlay shard's directory inside the run directory. The run
	// directory is touched while this process lives and swept 24 hours after
	// it dies; a directory beside it in TempDir was swept by nothing and
	// leaked whenever a process ended without Close. zoekt loads only the
	// *.zoekt files directly in a directory, so the index's own searcher does
	// not see the overlay's shards.
	idx.mu.RLock()
	runDir := idx.runDir
	release := idx.leaseDir(runDir)
	idx.mu.RUnlock()
	defer release()
	if runDir == "" {
		return nil, fmt.Errorf("create overlay dir: no run directory")
	}
	overlayDir, err := os.MkdirTemp(runDir, "overlay-*")
	if err != nil {
		return nil, fmt.Errorf("create overlay dir: %w", err)
	}

	// Build the overlay index.
	builderOpts := zoektindex.Options{
		IndexDir:     overlayDir,
		DisableCTags: true,
		RepositoryDescription: zoekt.Repository{
			Name: "workspace-overlay",
			Branches: []zoekt.RepositoryBranch{
				{Name: "HEAD", Version: "overlay"},
			},
		},
	}

	builder, err := zoektindex.NewBuilder(builderOpts)
	if err != nil {
		os.RemoveAll(overlayDir)
		return nil, fmt.Errorf("overlay builder: %w", err)
	}

	for _, fe := range files {
		content, err := os.ReadFile(fe.abs)
		if err != nil {
			continue
		}

		doc := zoektindex.Document{
			Name:     fe.rel,
			Content:  content,
			Branches: []string{"HEAD"},
		}

		if lang := langFor(fe.abs, content); lang != "" {
			doc.Language = lang
		}

		if of, ok := outlineResults[fe.rel]; ok && len(of.Decls) > 0 {
			sections, metadata := declsToSymbols(content, of.Decls)
			doc.Symbols = sections
			doc.SymbolsMetaData = metadata
		}

		if err := builder.Add(doc); err != nil {
			continue
		}
	}

	if err := builder.Finish(); err != nil {
		os.RemoveAll(overlayDir)
		return nil, fmt.Errorf("overlay finish: %w", err)
	}

	return &overlayState{
		dirtySet:     copyStringBoolMap(dirtySet),
		dir:          overlayDir,
		runDir:       runDir,
		outlineFiles: outlineResults,
	}, nil
}

// searchOverlay searches the overlay shard with the given query and returns
// the matching files.
func searchOverlay(ctx context.Context, overlayDir string, q query.Q, maxFiles, contextLines int, outlineFiles map[string]outline.File) ([]searchResultFile, error) {
	if overlayDir == "" {
		return nil, nil
	}

	searcher, err := search.NewDirectorySearcher(overlayDir)
	if err != nil {
		return nil, fmt.Errorf("open overlay searcher: %w", err)
	}
	defer searcher.Close()

	opts := &zoekt.SearchOptions{
		MaxDocDisplayCount: maxFiles + 1,
		NumContextLines:    contextLines,
		ChunkMatches:       true,
	}

	result, err := searcher.Search(ctx, q, opts)
	if err != nil {
		return nil, err
	}

	files := convertFileMatches(result.Files, maxFiles, contextLines, outlineFiles)
	return files, nil
}

// mergeByScore merges indexed and overlay results by score, keeping at most
// maxFiles. Overlay files replace any indexed file with the same path.
// All overlay hits are guaranteed to be included (indexed hits are dropped
// to make room if needed), so a fresh edit cannot fall off the end of the
// max_files cap.
func mergeByScore(indexed, overlay []searchResultFile, maxFiles int) []searchResultFile {
	// Build a set of overlay paths for deduplication.
	overlayPaths := make(map[string]bool, len(overlay))
	for _, f := range overlay {
		overlayPaths[f.path] = true
	}

	// Remove indexed files that are in the overlay (they're dirty).
	var cleanIndexed []searchResultFile
	for _, f := range indexed {
		if !overlayPaths[f.path] {
			cleanIndexed = append(cleanIndexed, f)
		}
	}

	// Reserve slots for overlay hits.
	indexedSlots := maxFiles - len(overlay)
	if indexedSlots < 0 {
		indexedSlots = 0
	}

	// Sort indexed by score descending and keep at most indexedSlots.
	sort.Slice(cleanIndexed, func(i, j int) bool {
		return cleanIndexed[i].score > cleanIndexed[j].score
	})
	if len(cleanIndexed) > indexedSlots {
		cleanIndexed = cleanIndexed[:indexedSlots]
	}

	// Merge all: indexed + overlay, sorted by score descending.
	merged := append(cleanIndexed, overlay...)
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	// Cap at maxFiles (should already be <= maxFiles).
	if len(merged) > maxFiles {
		merged = merged[:maxFiles]
	}

	return merged
}

// cleanupOverlay removes the overlay shard directory.
func cleanupOverlay(ov *overlayState) {
	if ov != nil && ov.dir != "" {
		os.RemoveAll(ov.dir)
	}
}

// copyStringBoolMap returns a copy of a map[string]bool.
func copyStringBoolMap(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// dirtySetEqual returns true if two dirty sets are identical.
func dirtySetEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
