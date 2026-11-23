package outline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Backend names the mechanism that produced a File's declarations.
type Backend string

const (
	BackendGoAST      Backend = "go/ast"
	BackendTreeSitter Backend = "tree-sitter"
	BackendNone       Backend = "none"
)

// Kind is the closed set of declaration kinds.
type Kind string

const (
	KindFunc      Kind = "func"
	KindMethod    Kind = "method"
	KindType      Kind = "type"
	KindClass     Kind = "class"
	KindInterface Kind = "interface"
	KindEnum      Kind = "enum"
	KindTrait     Kind = "trait"
	KindConst     Kind = "const"
	KindVar       Kind = "var"
	KindModule    Kind = "module"
	KindMacro     Kind = "macro"
)

// Decl is one declaration in a source file.
type Decl struct {
	Kind      Kind
	Name      string
	Container string
	Signature string
	Exported  bool
	StartLine int // 1-based, inclusive
	EndLine   int // 1-based, inclusive; 0 when unknown
}

// NotParsedReason says why a file was not parsed: a closed set.
type NotParsedReason string

const (
	// NotParsedUnsupported: the extension is not in the table, or this build
	// has no backend for the language (any language but Go without cgo).
	NotParsedUnsupported NotParsedReason = "unsupported"
	// NotParsedTooLarge: the file is over Options.MaxFileBytes.
	NotParsedTooLarge NotParsedReason = "too_large"
	// NotParsedBinary: a NUL byte in the file's first 8 KiB.
	NotParsedBinary NotParsedReason = "binary"
	// NotParsedFailed: the backend returned no syntax tree, or the file
	// could not be read.
	NotParsedFailed NotParsedReason = "failed"
)

// SyntaxErrorKind is the closed set of syntax error kinds.
type SyntaxErrorKind string

const (
	// SyntaxErrorUnexpected is text the grammar could not place.
	SyntaxErrorUnexpected SyntaxErrorKind = "unexpected"
	// SyntaxErrorMissing is a token the grammar needed and did not find,
	// reported where it was expected.
	SyntaxErrorMissing SyntaxErrorKind = "missing"
)

// SyntaxError is one syntax error a parse found: a position and a kind,
// with no message.
type SyntaxError struct {
	Line   int // 1-based
	Column int // 1-based, in bytes
	Kind   SyntaxErrorKind
}

// File is the outline of one source file, with the parse verdict of the
// parse that produced its declarations.
type File struct {
	Path    string  // slash-separated, relative to Options.Root or absolute
	Lang    string  // language name from the extension table
	Backend Backend // which backend produced the declarations
	Decls   []Decl  // sorted by StartLine, ties broken by Name; never nil

	// Parsed is true when a backend parsed the file's bytes, whether or not
	// it found syntax errors; Backend is then not BackendNone.
	Parsed bool
	// NotParsed is empty when Parsed is true, and otherwise says why the
	// file was not parsed. Backend is then BackendNone and Decls empty.
	NotParsed NotParsedReason
	// SyntaxErrors are the errors the parse found, in position order, at
	// most Options.MaxSyntaxErrors of them; empty and never nil when there
	// are none or the file was not parsed.
	SyntaxErrors []SyntaxError
	// SyntaxErrorsTruncated is true when the parse found more errors than
	// SyntaxErrors holds.
	SyntaxErrorsTruncated bool
}

// Source identifies a file to outline.
type Source struct {
	Abs string // absolute path
	Src []byte // optional; when nil the file is read from disk
}

// Options configures outlining.
type Options struct {
	Root         string // when non-empty, File.Path is relative to this
	MaxFileBytes int64  // 0 means 1 MiB
	// MaxSyntaxErrors caps File.SyntaxErrors: 0 means 20, and a negative
	// value is treated as 0.
	MaxSyntaxErrors int
}

// defaultMaxFileBytes is the default file size limit (1 MiB).
const defaultMaxFileBytes = 1 << 20

// binarySniffSize is how many bytes are checked for a NUL byte.
const binarySniffSize = 8 * 1024

// Outline returns the outline of a single file: Go files from go/ast, the
// other languages in the extension table from their tree-sitter grammar
// (when built with cgo), anything else as none. An unreadable file is an
// error; a cancelled context returns ctx.Err().
func Outline(ctx context.Context, abs string, src []byte, opts Options) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	f := notParsed(filePath(abs, opts.Root), langForExt(filepath.Ext(abs)), NotParsedUnsupported)
	// Unknown extension: return immediately without reading the file.
	if f.Lang == "" {
		return f, nil
	}
	maxBytes := opts.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxFileBytes
	}
	// Read the file if no source was provided — after checking its size, so
	// a 500 MB tracked bundle is skipped on the stat rather than read whole
	// and then rejected.
	if src == nil {
		data, err := loadSource(abs, maxBytes)
		if errors.Is(err, errTooLarge) {
			f.NotParsed = NotParsedTooLarge
			return f, nil
		}
		if err != nil {
			return File{}, err
		}
		src = data
	}
	// Too large, then binary (a NUL in the first 8 KiB): the size decides
	// first, as it does for a file on disk, which is never read to sniff.
	if int64(len(src)) > maxBytes {
		f.NotParsed = NotParsedTooLarge
		return f, nil
	}
	if bytes.IndexByte(src[:min(len(src), binarySniffSize)], 0) >= 0 {
		f.NotParsed = NotParsedBinary
		return f, nil
	}

	if f.Lang == LangGo {
		if g, ok := outlineGo(abs, src, opts); ok {
			return g, nil
		}
		f.NotParsed = NotParsedFailed // the parser returned no AST at all
		return f, nil
	}
	f.Lang = LangFor(abs, src)
	decls, ok, err := outlineTreeSitter(ctx, f.Lang, filepath.Ext(abs), src)
	if err != nil {
		if ctx.Err() != nil {
			return File{}, ctx.Err()
		}
		f.NotParsed = NotParsedFailed // the grammar or its query did not load
		return f, nil
	}
	if !ok {
		return f, nil // no grammar for the language in this build: unsupported
	}
	f.Backend, f.Decls, f.Parsed, f.NotParsed = BackendTreeSitter, decls, true, ""
	return finishFile(f), nil
}

// notParsed is the File of a file no backend parsed, for the reason why.
func notParsed(path, lang string, why NotParsedReason) File {
	return File{Path: path, Lang: lang, Backend: BackendNone, Decls: []Decl{}, NotParsed: why, SyntaxErrors: []SyntaxError{}}
}

// OutlineMany outlines many files, returning one File per input in order. A
// file that cannot be read is returned as none; a cancelled context returns
// ctx.Err().
func OutlineMany(ctx context.Context, srcs []Source, opts Options) ([]File, error) {
	files := make([]File, len(srcs))
	for i, s := range srcs {
		f, err := Outline(ctx, s.Abs, s.Src, opts)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			f = notParsed(filePath(s.Abs, opts.Root), langForExt(filepath.Ext(s.Abs)), NotParsedFailed)
		}
		files[i] = f
	}
	return files, nil
}

// errTooLarge is loadSource's report that the file is over the size limit
// and was not read.
var errTooLarge = errors.New("outline: file over the size limit")

// loadSource reads the file. A file larger than maxBytes is not read at all:
// its size is checked on a stat first, and errTooLarge returned.
func loadSource(abs string, maxBytes int64) ([]byte, error) {
	if fi, err := os.Stat(abs); err == nil && fi.Size() > maxBytes {
		return nil, errTooLarge
	}
	return os.ReadFile(abs)
}

// filePath computes the File.Path value. When root is non-empty, the path is
// relative to root and slash-separated. When root is empty, the path is the
// slash-converted absolute path.
func filePath(abs, root string) string {
	if root != "" {
		rel, err := filepath.Rel(root, abs)
		if err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}

// finishFile guarantees non-nil Decls and SyntaxErrors and sorts the
// declarations by StartLine then Name.
func finishFile(f File) File {
	if f.Decls == nil {
		f.Decls = []Decl{}
	}
	if f.SyntaxErrors == nil {
		f.SyntaxErrors = []SyntaxError{}
	}
	sort.SliceStable(f.Decls, func(i, j int) bool {
		if f.Decls[i].StartLine != f.Decls[j].StartLine {
			return f.Decls[i].StartLine < f.Decls[j].StartLine
		}
		return f.Decls[i].Name < f.Decls[j].Name
	})
	return f
}
