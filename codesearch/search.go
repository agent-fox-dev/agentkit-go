//go:build !windows

package codesearch

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"

	zoekt "github.com/sourcegraph/zoekt"
)

// maxChunksPerFile is the maximum number of chunks shown per file.
const maxChunksPerFile = 3

// maxDeclNames is the maximum number of declaration names shown per file header.
const maxDeclNames = 5

// resultChunk is one contiguous range of lines in a file result.
type resultChunk struct {
	lines []resultLine
}

// resultLine is one line in a chunk.
type resultLine struct {
	lineNo int
	text   string
	match  bool
}

// convertFileMatches converts zoekt FileMatch results into our internal
// representation: files in score order, at most maxFiles, at most 3 chunks
// per file in zoekt order with lines sorted ascending, context_lines either
// side, overlapping chunks merged, each line cut to SearchLineChars.
func convertFileMatches(fms []zoekt.FileMatch, maxFiles, contextLines int, outlineFiles map[string]outline.File) []searchResultFile {
	if len(fms) > maxFiles {
		fms = fms[:maxFiles]
	}

	var files []searchResultFile
	for _, fm := range fms {
		rf := searchResultFile{
			path:       fm.FileName,
			score:      fm.Score,
			matchCount: countMatches(fm),
		}

		// Convert chunk matches to our representation.
		chunks := convertChunkMatches(fm.ChunkMatches, contextLines)

		// Limit to maxChunksPerFile.
		if len(chunks) > maxChunksPerFile {
			chunks = chunks[:maxChunksPerFile]
		}

		rf.chunks = chunks

		// Collect symbol names for matches on declaration start lines.
		if of, ok := outlineFiles[fm.FileName]; ok {
			rf.symbols = collectDeclNames(fm, of)
		}

		files = append(files, rf)
	}

	return files
}

// countMatches counts the total number of match ranges across all chunks.
func countMatches(fm zoekt.FileMatch) int {
	n := 0
	for _, cm := range fm.ChunkMatches {
		n += len(cm.Ranges)
	}
	return n
}

// convertChunkMatches converts zoekt ChunkMatches into our resultChunks.
// Each chunk's lines are in ascending line order, overlapping chunks are
// merged, and each line is cut to SearchLineChars.
func convertChunkMatches(cms []zoekt.ChunkMatch, contextLines int) []resultChunk {
	var chunks []resultChunk

	for _, cm := range cms {
		chunk := chunkFromZoekt(cm, contextLines)
		chunks = append(chunks, chunk)
	}

	// Merge overlapping chunks.
	chunks = mergeChunks(chunks)

	return chunks
}

// chunkFromZoekt converts a single zoekt ChunkMatch into a resultChunk.
func chunkFromZoekt(cm zoekt.ChunkMatch, contextLines int) resultChunk {
	// Parse the content into lines.
	content := string(cm.Content)
	contentLines := strings.Split(content, "\n")
	// Remove trailing empty line from split.
	if len(contentLines) > 0 && contentLines[len(contentLines)-1] == "" {
		contentLines = contentLines[:len(contentLines)-1]
	}

	startLine := int(cm.ContentStart.LineNumber)

	// Build a set of matched line numbers.
	matchedLines := make(map[int]bool)
	for _, r := range cm.Ranges {
		// A range can span multiple lines.
		for ln := int(r.Start.LineNumber); ln <= int(r.End.LineNumber); ln++ {
			matchedLines[ln] = true
		}
	}

	// Build lines with context.
	var lines []resultLine
	for i, text := range contentLines {
		lineNo := startLine + i
		isMatch := matchedLines[lineNo]

		// Check if this line is within context_lines of a match.
		inContext := false
		if !isMatch && contextLines > 0 {
			for ml := range matchedLines {
				diff := lineNo - ml
				if diff < 0 {
					diff = -diff
				}
				if diff <= contextLines {
					inContext = true
					break
				}
			}
		}

		if isMatch || inContext {
			lines = append(lines, resultLine{
				lineNo: lineNo,
				text:   capLineText(text),
				match:  isMatch,
			})
		}
	}

	// Sort lines by line number.
	sort.Slice(lines, func(i, j int) bool {
		return lines[i].lineNo < lines[j].lineNo
	})

	return resultChunk{lines: lines}
}

// mergeChunks merges overlapping or adjacent chunks and keeps zoekt's order.
// The chunks arrive best first, and each merged chunk keeps the place of its
// best-ranked part: a chunk that touches an earlier one is folded into it,
// and a chunk that bridges several is folded into the earliest. Cutting the
// result to maxChunksPerFile therefore keeps zoekt's best chunks, best first.
func mergeChunks(chunks []resultChunk) []resultChunk {
	var merged []resultChunk

	for _, c := range chunks {
		if len(c.lines) == 0 {
			continue
		}

		// The slots are pairwise disjoint, so the ones c touches are the only
		// ones its union with them can touch.
		var touching []int
		for i, m := range merged {
			if chunksTouch(m, c) {
				touching = append(touching, i)
			}
		}
		if len(touching) == 0 {
			merged = append(merged, c)
			continue
		}

		u := c
		for _, i := range touching {
			u = unionChunks(merged[i], u)
		}
		merged[touching[0]] = u
		for k := len(touching) - 1; k >= 1; k-- {
			merged = slices.Delete(merged, touching[k], touching[k]+1)
		}
	}

	return merged
}

// chunksTouch reports whether two non-empty chunks overlap or are adjacent.
func chunksTouch(a, b resultChunk) bool {
	aFirst, aLast := a.lines[0].lineNo, a.lines[len(a.lines)-1].lineNo
	bFirst, bLast := b.lines[0].lineNo, b.lines[len(b.lines)-1].lineNo
	return bFirst <= aLast+1 && aFirst <= bLast+1
}

// unionChunks returns the lines of a and b in ascending order, preferring a
// match over context where both have the same line.
func unionChunks(a, b resultChunk) resultChunk {
	lineMap := make(map[int]resultLine, len(a.lines)+len(b.lines))
	for _, l := range a.lines {
		lineMap[l.lineNo] = l
	}
	for _, l := range b.lines {
		if existing, ok := lineMap[l.lineNo]; !ok || (l.match && !existing.match) {
			lineMap[l.lineNo] = l
		}
	}
	lines := make([]resultLine, 0, len(lineMap))
	for _, l := range lineMap {
		lines = append(lines, l)
	}
	sort.Slice(lines, func(i, j int) bool {
		return lines[i].lineNo < lines[j].lineNo
	})
	return resultChunk{lines: lines}
}

// capLineText cuts a line to SearchLineChars runes, appending "…" if truncated.
func capLineText(s string) string {
	if len(s) <= tools.SearchLineChars {
		return s
	}
	n := 0
	for i := range s {
		if n == tools.SearchLineChars {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// collectDeclNames collects declaration names for matches that fall on a
// declaration's start line. Returns at most maxDeclNames names.
// For members, uses Container.Name format.
func collectDeclNames(fm zoekt.FileMatch, of outline.File) []string {
	// Build a set of matched line numbers.
	matchedLines := make(map[int]bool)
	for _, cm := range fm.ChunkMatches {
		for _, r := range cm.Ranges {
			for ln := int(r.Start.LineNumber); ln <= int(r.End.LineNumber); ln++ {
				matchedLines[ln] = true
			}
		}
	}

	var names []string
	seen := make(map[string]bool)
	for _, d := range of.Decls {
		if matchedLines[d.StartLine] {
			name := d.Name
			if d.Container != "" {
				name = d.Container + "." + d.Name
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
				if len(names) >= maxDeclNames {
					break
				}
			}
		}
	}

	return names
}

// renderResult renders the full text output for a code_search result.
func renderResult(files []searchResultFile, info resultInfo) string {
	var b strings.Builder

	// First line: matched and indexed file counts, index size, symbol sources,
	// partial/dirty.
	b.WriteString(renderFirstLine(info, len(files)))
	b.WriteByte('\n')

	if len(files) == 0 {
		b.WriteString("No matches found.")
		return b.String()
	}

	for fi, f := range files {
		if fi > 0 {
			b.WriteByte('\n')
		}
		// File header.
		b.WriteString(renderFileHeader(f))
		b.WriteByte('\n')

		// Chunks.
		for _, chunk := range f.chunks {
			for _, line := range chunk.lines {
				sep := '-'
				if line.match {
					sep = ':'
				}
				fmt.Fprintf(&b, "  %d%c %s\n", line.lineNo, sep, line.text)
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// resultInfo holds metadata for the first line and markers.
type resultInfo struct {
	filesIndexed   int
	indexSizeBytes int64
	symbolSources  map[string]int // backend -> file count
	partial        bool
	partialReason  string
	dirtyFiles     int
}

// renderFirstLine renders the first line of the result text. matched is the
// number of files the text shows.
func renderFirstLine(info resultInfo, matched int) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("%d files matched", matched))

	parts = append(parts, fmt.Sprintf("%d files indexed", info.filesIndexed))

	if info.indexSizeBytes > 0 {
		parts = append(parts, fmt.Sprintf("index %s", humanSize(info.indexSizeBytes)))
	}

	// Symbol sources.
	if len(info.symbolSources) > 0 {
		var srcParts []string
		// Sort for deterministic output.
		var backends []string
		for b := range info.symbolSources {
			backends = append(backends, b)
		}
		sort.Strings(backends)
		for _, b := range backends {
			srcParts = append(srcParts, fmt.Sprintf("%s: %d", b, info.symbolSources[b]))
		}
		parts = append(parts, "symbols: "+strings.Join(srcParts, ", "))
	}

	if info.partial {
		reason := info.partialReason
		if reason == "" {
			reason = "unknown"
		}
		parts = append(parts, fmt.Sprintf("partial (%s limit)", reason))
	}

	if info.dirtyFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d dirty files overlaid", info.dirtyFiles))
	}

	return "[" + strings.Join(parts, "; ") + "]"
}

// humanSize formats bytes as a human-readable string.
func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// renderFileHeader renders the header line for a file.
func renderFileHeader(f searchResultFile) string {
	path := sanitizePath(f.path)

	var parts []string
	parts = append(parts, path)
	parts = append(parts, fmt.Sprintf("(%d matches)", f.matchCount))

	// Note when matches were not shown.
	shownMatches := 0
	for _, chunk := range f.chunks {
		for _, line := range chunk.lines {
			if line.match {
				shownMatches++
			}
		}
	}
	if shownMatches < f.matchCount {
		parts = append(parts, fmt.Sprintf("[%d matches not shown]", f.matchCount-shownMatches))
	}

	// Declaration names.
	if len(f.symbols) > 0 {
		parts = append(parts, "{"+strings.Join(f.symbols, ", ")+"}")
	}

	return strings.Join(parts, " ")
}

// sanitizePath replaces control characters in a path with '?'.
func sanitizePath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); {
		r, size := utf8.DecodeRuneInString(path[i:])
		if r == utf8.RuneError && size <= 1 {
			// Invalid UTF-8 byte.
			b.WriteByte('?')
			i++
			continue
		}
		if unicode.IsControl(r) && r != '\t' {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// applyByteCap drops whole files from the end (keeping at least one), then
// drops chunks from the last file if it alone is too large, to keep the
// rendered text within the byte budget. Returns the truncated files, the
// bytes marker text, and whether truncation happened.
func applyByteCap(files []searchResultFile, info resultInfo, budget int) ([]searchResultFile, string, bool) {
	text := renderResult(files, info)
	if len(text) <= budget {
		return files, "", false
	}

	// Drop whole files from the end, keeping at least one.
	for len(files) > 1 {
		files = files[:len(files)-1]
		text = renderResult(files, info)
		if len(text) <= budget-200 { // leave room for the marker
			break
		}
	}

	// If the single remaining file is still too large, drop chunks from the end.
	if len(files) == 1 && len(files[0].chunks) > 1 {
		for len(files[0].chunks) > 1 {
			files[0].chunks = files[0].chunks[:len(files[0].chunks)-1]
			text = renderResult(files, info)
			if len(text) <= budget-200 {
				break
			}
		}
	}

	// If still too large with one chunk, truncate lines from the end.
	if len(files) == 1 && len(files[0].chunks) == 1 {
		chunk := &files[0].chunks[0]
		for len(chunk.lines) > 1 {
			chunk.lines = chunk.lines[:len(chunk.lines)-1]
			text = renderResult(files, info)
			if len(text) <= budget-200 {
				break
			}
		}
	}

	marker := bytesMarker()
	return files, marker, true
}

// bytesMarker returns the truncation marker for byte-limit truncation.
func bytesMarker() string {
	return fmt.Sprintf("[%s limit reached. Narrow with path, add file: to the query, or reduce context_lines]",
		humanSize(int64(tools.DefaultByteLimit)))
}

// cutLines returns the longest prefix of text that is at most max bytes and
// ends at a line boundary, without the trailing newline. It never splits a
// line, so it never splits a rune either; when not even the first line fits
// it returns "".
func cutLines(text string, max int) string {
	if len(text) <= max {
		return text
	}
	if max < 0 {
		return ""
	}
	// text[max] may itself be the newline that ends a line of exactly max bytes.
	cut := strings.LastIndexByte(text[:max+1], '\n')
	if cut < 0 {
		return ""
	}
	return text[:cut]
}
