package outline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// maxBatchFiles is the maximum number of files per ctags invocation.
const maxBatchFiles = 100

// maxBatchPathBytes is the maximum total byte length of paths per batch.
const maxBatchPathBytes = 32768

// ctagsFixedArgs are the arguments passed to the Runner before the file paths.
var ctagsFixedArgs = []string{
	"--options=NONE",
	"--output-format=json",
	"--fields=+neKS",
	"--sort=no",
	"-f",
	"-",
}

// ctagsTag represents one JSON line from ctags output.
type ctagsTag struct {
	Type      string `json:"_type"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	End       int    `json:"end"`
	Signature string `json:"signature"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
}

// ctagsBatchEntry tracks a file in a ctags batch.
type ctagsBatchEntry struct {
	srcIdx int    // index into the original Sources slice
	abs    string // absolute path
	src    []byte // file content (already loaded)
}

// makeBatches groups non-Go, in-table, eligible files into batches of at most
// maxBatchFiles files and maxBatchPathBytes bytes of path text.
func makeBatches(entries []ctagsBatchEntry) [][]ctagsBatchEntry {
	var batches [][]ctagsBatchEntry
	var cur []ctagsBatchEntry
	curBytes := 0

	for _, e := range entries {
		pathLen := len(e.abs)
		// Start a new batch if adding this file would exceed limits.
		if len(cur) > 0 && (len(cur) >= maxBatchFiles || curBytes+pathLen > maxBatchPathBytes) {
			batches = append(batches, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, e)
		curBytes += pathLen
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// runCtagsBatch calls the Runner for one batch and parses the JSON output.
// It returns the parsed tags grouped by file index, the number of malformed
// lines, and any error. A Runner error is returned as-is.
func runCtagsBatch(
	ctx context.Context,
	runner func(ctx context.Context, args []string) ([]byte, error),
	batch []ctagsBatchEntry,
) (map[int][]Decl, int, error) {
	// Build argument list: fixed args + absolute file paths.
	args := make([]string, 0, len(ctagsFixedArgs)+len(batch))
	args = append(args, ctagsFixedArgs...)

	// Build a map from cleaned absolute path to batch entry index for attribution.
	pathToIdx := make(map[string]int, len(batch))
	for _, e := range batch {
		args = append(args, e.abs)
		cleaned := filepath.Clean(e.abs)
		pathToIdx[cleaned] = e.srcIdx
	}

	stdout, err := runner(ctx, args)
	if err != nil {
		// Check if context was cancelled.
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, err
	}

	return parseCtagsOutput(stdout, pathToIdx, batch)
}

// parseCtagsOutput parses ctags JSON-line output and attributes tags to files.
func parseCtagsOutput(
	stdout []byte,
	pathToIdx map[string]int,
	batch []ctagsBatchEntry,
) (map[int][]Decl, int, error) {
	result := make(map[int][]Decl)
	malformed := 0

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	// Allow long lines (ctags can produce long lines for some tags).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var tag ctagsTag
		if err := json.Unmarshal(line, &tag); err != nil {
			malformed++
			continue
		}

		// Ignore non-tag lines (ptag, program, etc.) without counting as malformed.
		if tag.Type != "tag" {
			continue
		}

		// Validate required fields.
		if tag.Name == "" || tag.Path == "" || tag.Line == 0 {
			malformed++
			continue
		}

		// Attribute by echoed path.
		cleaned := filepath.Clean(tag.Path)
		srcIdx, ok := pathToIdx[cleaned]
		if !ok {
			// Path not in batch — drop.
			continue
		}

		// Map kind.
		kind := mapCtagsKind(tag.Kind)
		if kind == "" {
			// Kind not in the closed set — drop.
			continue
		}

		// Check if this is a method (function-kind tag with type-like scope).
		container := ""
		if (kind == KindFunc) && isTypeLikeScope(tag.ScopeKind) {
			kind = KindMethod
			container = tag.Scope
		}

		// Signature: use the source line at StartLine if available.
		sig := ctagsSignature(tag, batch, srcIdx)

		// Exported: true unless name begins with underscore.
		exported := !strings.HasPrefix(tag.Name, "_")

		endLine := tag.End

		d := Decl{
			Kind:      kind,
			Name:      tag.Name,
			Container: container,
			Signature: sanitiseSignature(sig),
			Exported:  exported,
			StartLine: tag.Line,
			EndLine:   endLine,
		}
		result[srcIdx] = append(result[srcIdx], d)
	}

	return result, malformed, nil
}

// ctagsSignature returns the signature for a ctags tag.
// It prefers the source line at StartLine; falls back to ctags' signature field.
func ctagsSignature(tag ctagsTag, batch []ctagsBatchEntry, srcIdx int) string {
	// Find the batch entry for this srcIdx.
	for _, e := range batch {
		if e.srcIdx == srcIdx {
			line := sourceLineAt(e.src, tag.Line)
			if line != "" {
				return line
			}
			break
		}
	}
	// Fallback to ctags signature field.
	if tag.Signature != "" {
		return tag.Signature
	}
	return tag.Name
}

// sourceLineAt returns the 1-based line from src, or "" if out of range.
func sourceLineAt(src []byte, lineNum int) string {
	if lineNum <= 0 || len(src) == 0 {
		return ""
	}
	cur := 1
	start := 0
	for i, b := range src {
		if cur == lineNum {
			start = i
			// Find end of line.
			end := bytes.IndexByte(src[i:], '\n')
			if end < 0 {
				return strings.TrimRight(string(src[i:]), "\r")
			}
			return strings.TrimRight(string(src[i:i+end]), "\r")
		}
		if b == '\n' {
			cur++
		}
	}
	_ = start
	return ""
}

// mapCtagsKind maps a ctags kind string to the closed Kind set.
// Returns "" for kinds that should be dropped.
func mapCtagsKind(k string) Kind {
	switch strings.ToLower(k) {
	case "function", "func", "subroutine", "procedure":
		return KindFunc
	case "method":
		return KindMethod
	case "type":
		return KindType
	case "class":
		return KindClass
	case "interface":
		return KindInterface
	case "enum":
		return KindEnum
	case "trait":
		return KindTrait
	case "constant", "const":
		return KindConst
	case "variable", "var":
		return KindVar
	case "module", "namespace", "package":
		return KindModule
	case "macro", "define":
		return KindMacro
	default:
		return ""
	}
}

// isTypeLikeScope returns true if the scope kind is type-like.
func isTypeLikeScope(scopeKind string) bool {
	switch strings.ToLower(scopeKind) {
	case "class", "struct", "interface", "enum", "trait", "impl", "type":
		return true
	}
	return false
}

// loadSourceIfNeeded reads the file from disk if src is nil.
func loadSourceIfNeeded(abs string, src []byte, maxBytes int64) ([]byte, error) {
	if src != nil {
		return src, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return data, nil
}
