//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/tools"

	zoekt "github.com/sourcegraph/zoekt"
)

// zoektChunk builds a ChunkMatch whose content is the lines first..last, all
// of them matching.
func zoektChunk(first, last int) zoekt.ChunkMatch {
	var content strings.Builder
	for ln := first; ln <= last; ln++ {
		fmt.Fprintf(&content, "line %d\n", ln)
	}
	return zoekt.ChunkMatch{
		Content:      []byte(content.String()),
		ContentStart: zoekt.Location{LineNumber: uint32(first), Column: 1},
		Ranges: []zoekt.Range{{
			Start: zoekt.Location{LineNumber: uint32(first)},
			End:   zoekt.Location{LineNumber: uint32(last)},
		}},
	}
}

// shownChunks converts chunks given in zoekt's order and returns each shown
// chunk as [first line, last line].
func shownChunks(cms ...zoekt.ChunkMatch) [][2]int {
	files := convertFileMatches(
		[]zoekt.FileMatch{{FileName: "f.go", ChunkMatches: cms}},
		10, 0, map[string]outline.File{})
	var out [][2]int
	for _, c := range files[0].chunks {
		out = append(out, [2]int{c.lines[0].lineNo, c.lines[len(c.lines)-1].lineNo})
	}
	return out
}

func equalChunks(a, b [][2]int) bool { return slices.Equal(a, b) }

// 03-REQ-4.1: a file shows at most 3 chunks in zoekt's order, best first. The
// three shown are zoekt's three best, not the three earliest in the file.
func TestBestThreeChunksSurviveInZoektOrder(t *testing.T) {
	// Zoekt ranked the match at line 100 first and the one at line 10 second.
	got := shownChunks(
		zoektChunk(100, 100), zoektChunk(10, 10), zoektChunk(30, 30),
		zoektChunk(50, 50), zoektChunk(70, 70))
	want := [][2]int{{100, 100}, {10, 10}, {30, 30}}
	if !equalChunks(got, want) {
		t.Errorf("shown chunks = %v, want zoekt's best three in zoekt's order %v", got, want)
	}
}

// Overlapping chunks are merged, and a lower-ranked chunk that overlaps a
// better one folds into the better one's slot instead of taking its own.
func TestOverlappingChunkFoldsIntoTheHigherRankedSlot(t *testing.T) {
	// Rank order: 50, 10, 51 (touches 50), 90. The fourth chunk would be cut
	// if 51 took a slot of its own.
	got := shownChunks(
		zoektChunk(50, 50), zoektChunk(10, 10), zoektChunk(51, 51), zoektChunk(90, 90))
	want := [][2]int{{50, 51}, {10, 10}, {90, 90}}
	if !equalChunks(got, want) {
		t.Errorf("shown chunks = %v, want %v", got, want)
	}
}

// A chunk that bridges two slots merges both into the better-ranked one.
func TestBridgingChunkMergesIntoTheBestRankedSlot(t *testing.T) {
	// Rank order: 10-11, 14-15, 12-13 (touches both), 90.
	got := shownChunks(
		zoektChunk(10, 11), zoektChunk(14, 15), zoektChunk(12, 13), zoektChunk(90, 90))
	want := [][2]int{{10, 15}, {90, 90}}
	if !equalChunks(got, want) {
		t.Errorf("shown chunks = %v, want %v", got, want)
	}
}

// The lines of a merged chunk are in ascending order, whatever order its
// parts arrived in.
func TestMergedChunkLinesAreAscending(t *testing.T) {
	files := convertFileMatches(
		[]zoekt.FileMatch{{FileName: "f.go", ChunkMatches: []zoekt.ChunkMatch{
			zoektChunk(20, 21), zoektChunk(18, 19), zoektChunk(22, 22),
		}}},
		10, 0, map[string]outline.File{})
	if len(files[0].chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 merged chunk", len(files[0].chunks))
	}
	var got []int
	for _, l := range files[0].chunks[0].lines {
		got = append(got, l.lineNo)
	}
	if want := []int{18, 19, 20, 21, 22}; !slices.Equal(got, want) {
		t.Errorf("merged lines = %v, want %v", got, want)
	}
}

// End to end: a declaration at the bottom of a file outranks the comment
// mentions above it, so its chunk is shown, and first.
func TestCodeSearchShowsTheBestChunksNotTheEarliest(t *testing.T) {
	root := t.TempDir()
	var src strings.Builder
	src.WriteString("package pkg\n")
	for i := 0; i < 5; i++ {
		src.WriteString(strings.Repeat("// filler\n", 10))
		src.WriteString("// the needle is mentioned here\n")
	}
	src.WriteString(strings.Repeat("// filler\n", 10))
	src.WriteString("func needle() {}\n")
	mkFile(t, root, "many.go", src.String())

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	in, _ := json.Marshal(map[string]any{"query": "needle", "context_lines": 1})
	r := idx.Tools()[0].Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("code_search: %s: %s", r.Error, r.Detail)
	}

	declLine := 1 + 5*11 + 10 + 1 // package line, five filler+comment blocks, filler, then the func
	files := r.Data["files"].([]any)
	chunks := files[0].(map[string]any)["chunks"].([]any)
	if len(chunks) == 0 || len(chunks) > maxChunksPerFile {
		t.Fatalf("got %d chunks, want 1 to %d", len(chunks), maxChunksPerFile)
	}
	var first []int
	for _, l := range chunks[0].(map[string]any)["lines"].([]any) {
		first = append(first, l.(map[string]any)["line"].(int))
	}
	if !slices.Contains(first, declLine) {
		t.Errorf("the first chunk shows lines %v, want the declaration at line %d first", first, declLine)
	}
}
