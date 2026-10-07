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

	// localTypes holds, per file, the scope names that tags declared inside
	// the type-like locals this file dropped would carry (see below).
	localTypes := make(map[int][]string)

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

		lang := langForExt(filepath.Ext(tag.Path))

		// Map kind.
		kind := mapCtagsKind(tag.Kind, lang)
		if kind == "" && isPythonMethodTag(tag, lang) {
			// Python reports a method as kind "member" in its class; it is a
			// function-kind tag in a type-like scope, which becomes a method
			// below.
			kind = KindFunc
		}
		if kind == "" {
			// Kind not in the closed set — drop.
			continue
		}

		// A tag declared inside a type this file dropped as a local is a local
		// too: its container is never reported.
		if underLocalType(tag.Scope, localTypes[srcIdx]) {
			continue
		}

		// Drop tags that are nested inside a function (locals).
		// A function-kind tag with a non-empty, non-type-like scope is a local.
		// A type-like tag nested in a function is also dropped.
		container := ""
		if tag.ScopeKind != "" {
			if (kind == KindFunc || kind == KindMethod) && isTypeLikeScope(tag.ScopeKind, lang) {
				// A function or method scoped in a type-like kind is a method
				// of that type. Most parsers say "method"; C++, PHP and
				// Python say "function" or "member".
				kind = KindMethod
				container = tag.Scope
			} else if isFunctionLikeScope(tag.ScopeKind, lang) {
				// Any tag scoped in a function-like kind is a local — drop.
				// A type among them takes what is declared in it along.
				if isTypeKind(kind) {
					localTypes[srcIdx] = append(localTypes[srcIdx], localScopeNames(tag)...)
				}
				continue
			}
		}

		// A "method" in no type is a function: Kotlin and GDScript report a
		// top-level function as kind "method" with no scope.
		if kind == KindMethod && container == "" {
			kind = KindFunc
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

// mapCtagsKind maps a ctags kind string to the closed Kind set. lang
// decides the words whose meaning differs between universal ctags' parsers.
// Returns "" for kinds that should be dropped.
func mapCtagsKind(k, lang string) Kind {
	switch strings.ToLower(k) {
	case "function", "func", "subroutine", "procedure", "generator",
		"subprogram", "subprogspec":
		return KindFunc
	case "method", "singletonmethod", "rpc":
		return KindMethod
	case "type", "struct", "typedef", "union", "alias", "typealias",
		"message", "record", "table", "view":
		return KindType
	case "class":
		return KindClass
	case "interface":
		switch lang {
		case LangRust:
			// The Rust parser reports a trait as "interface".
			return KindTrait
		case LangObjectiveC:
			// An Objective-C @interface declares a class.
			return KindClass
		}
		return KindInterface
	case "protocol", "service":
		return KindInterface
	case "object":
		// A Kotlin object is a singleton class. Other parsers (JSON among
		// them) use the word for things that are not declarations.
		if lang == LangKotlin {
			return KindClass
		}
		return ""
	case "enum":
		return KindEnum
	case "trait":
		return KindTrait
	case "constant", "const":
		return KindConst
	case "variable", "var":
		return KindVar
	case "resource", "data", "output":
		// Terraform's named blocks are its declarations.
		if lang == LangTerraform {
			return KindVar
		}
		return ""
	case "module", "namespace", "package", "packspec":
		return KindModule
	case "macro", "define":
		return KindMacro
	default:
		return ""
	}
}

// isTypeLikeScope returns true if the scope kind is type-like: a function or
// method scoped in it is a method of it.
func isTypeLikeScope(scopeKind, lang string) bool {
	switch strings.ToLower(scopeKind) {
	case "class", "struct", "interface", "enum", "trait", "impl", "type",
		// Rust's and Objective-C's parsers name an impl block, or an
		// @implementation, "implementation".
		"implementation",
		// A Protobuf or Thrift rpc belongs to its service.
		"service",
		// Kotlin methods of an object, Objective-C methods of a protocol.
		"object", "protocol":
		return true
	case "module":
		// A Ruby module holds methods (`def self.x`, mixins); in Elixir,
		// Erlang or Fortran a module holds functions.
		return lang == LangRuby
	}
	return false
}

// isFunctionLikeScope returns true if the scope kind is function-like,
// meaning tags nested in it are locals and should be dropped. Python names the
// scope of what is nested in a method "member", because that is the kind
// ctags gives a Python method.
func isFunctionLikeScope(scopeKind, lang string) bool {
	switch strings.ToLower(scopeKind) {
	case "function", "func", "method", "subroutine", "procedure":
		return true
	case "member":
		return lang == LangPython
	}
	return false
}

// isPythonMethodTag reports whether the tag is a Python method: universal
// ctags gives it kind "member" and the scope kind of its class. A C or C++
// data member has the same kind and a type-like scope too, and is a field, which
// 01-REQ-1.2 drops, so the language decides, not the scope kind.
func isPythonMethodTag(tag ctagsTag, lang string) bool {
	return lang == LangPython &&
		strings.EqualFold(tag.Kind, "member") &&
		isTypeLikeScope(tag.ScopeKind, lang)
}

// isTypeKind reports whether k declares a type.
func isTypeKind(k Kind) bool {
	switch k {
	case KindType, KindClass, KindInterface, KindEnum, KindTrait:
		return true
	}
	return false
}

// scopeSeparators are the separators ctags puts between the names of a tag's
// scope: "." in most languages, "::" in C++ and Rust, "\\" in PHP.
var scopeSeparators = []string{".", "::", "\\"}

// localScopeNames returns the forms the scope of a tag declared inside the
// dropped local type tag can take, one per separator.
func localScopeNames(tag ctagsTag) []string {
	names := make([]string, 0, len(scopeSeparators))
	for _, sep := range scopeSeparators {
		names = append(names, tag.Scope+sep+tag.Name)
	}
	return names
}

// underLocalType reports whether scope is, or is nested in, one of the scopes
// a dropped local type gives its contents.
func underLocalType(scope string, locals []string) bool {
	if scope == "" {
		return false
	}
	for _, l := range locals {
		if scope == l {
			return true
		}
		for _, sep := range scopeSeparators {
			if strings.HasPrefix(scope, l+sep) {
				return true
			}
		}
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
