package outline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
)

// Backend names the mechanism that produced a File's declarations.
type Backend string

const (
	BackendGoAST     Backend = "go/ast"
	BackendCtags     Backend = "ctags"
	BackendHeuristic Backend = "heuristic"
	BackendNone      Backend = "none"
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

// File is the outline of one source file.
type File struct {
	Path    string  // slash-separated, relative to Options.Root or absolute
	Lang    string  // language name from the extension table
	Backend Backend // which backend produced the declarations
	Decls   []Decl  // sorted by StartLine, ties broken by Name; never nil
}

// Source identifies a file to outline.
type Source struct {
	Abs string // absolute path
	Src []byte // optional; when nil the file is read from disk
}

// Stats counts events across an OutlineMany call.
type Stats struct {
	MalformedLines int // ctags JSON lines skipped
	Fallbacks      int // batches that fell back to another backend
}

// Options configures outlining.
type Options struct {
	Root         string // when non-empty, File.Path is relative to this
	MaxFileBytes int64  // 0 means 1 MiB
	Runner       func(ctx context.Context, args []string) ([]byte, error)
}

// defaultMaxFileBytes is the default file size limit (1 MiB).
const defaultMaxFileBytes = 1 << 20

// binarySniffSize is how many bytes are checked for a NUL byte.
const binarySniffSize = 8 * 1024

// Outline returns the outline of a single file.
func Outline(ctx context.Context, abs string, src []byte, opts Options) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}

	ext := filepath.Ext(abs)
	lang := langForExt(ext)

	// Unknown extension: return immediately without reading the file.
	if lang == "" {
		return File{
			Path:    filePath(abs, opts.Root),
			Lang:    "",
			Backend: BackendNone,
			Decls:   []Decl{},
		}, nil
	}

	// Read the file if no source was provided.
	if src == nil {
		data, err := os.ReadFile(abs)
		if err != nil {
			return File{}, err
		}
		src = data
	}

	maxBytes := opts.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxFileBytes
	}

	// File too large.
	if int64(len(src)) > maxBytes {
		return File{
			Path:    filePath(abs, opts.Root),
			Lang:    lang,
			Backend: BackendNone,
			Decls:   []Decl{},
		}, nil
	}

	// Binary check: NUL in first 8 KiB.
	sniff := src
	if len(sniff) > binarySniffSize {
		sniff = sniff[:binarySniffSize]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return File{
			Path:    filePath(abs, opts.Root),
			Lang:    lang,
			Backend: BackendNone,
			Decls:   []Decl{},
		}, nil
	}

	// Dispatch to backends.
	// Go files are always parsed in-process; the Runner is never called
	// unless the parser returns no AST at all.
	if lang == LangGo {
		if f, ok := outlineGo(abs, src, opts); ok {
			return f, nil
		}
		// Parser returned no AST at all — fall through to ctags or none.
	}

	// Try ctags if Runner is available.
	if opts.Runner != nil {
		entry := ctagsBatchEntry{srcIdx: 0, abs: abs, src: src}
		batch := []ctagsBatchEntry{entry}
		tagsByIdx, _, err := runCtagsBatch(ctx, opts.Runner, batch)
		if err != nil {
			if ctx.Err() != nil {
				return File{}, ctx.Err()
			}
			// Runner error: fall through to heuristic/none.
		} else {
			decls := tagsByIdx[0]
			if decls == nil {
				decls = []Decl{}
			}
			f := File{
				Path:    filePath(abs, opts.Root),
				Lang:    lang,
				Backend: BackendCtags,
				Decls:   decls,
			}
			return finishFile(f), nil
		}
	}

	// TODO(task 5): heuristic backend.
	f := File{
		Path:    filePath(abs, opts.Root),
		Lang:    lang,
		Backend: BackendNone,
		Decls:   []Decl{},
	}
	return finishFile(f), nil
}

// OutlineMany outlines many files, returning one File per input in order.
func OutlineMany(ctx context.Context, srcs []Source, opts Options) ([]File, Stats, error) {
	if err := ctx.Err(); err != nil {
		return nil, Stats{}, err
	}

	files := make([]File, len(srcs))
	var stats Stats

	maxBytes := opts.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxFileBytes
	}

	// Collect non-Go, in-table, eligible files for ctags batching.
	var ctagsEntries []ctagsBatchEntry

	for i, s := range srcs {
		if err := ctx.Err(); err != nil {
			return nil, Stats{}, err
		}

		ext := filepath.Ext(s.Abs)
		lang := langForExt(ext)

		// Unknown extension: return immediately without reading.
		if lang == "" {
			files[i] = File{
				Path:    filePath(s.Abs, opts.Root),
				Lang:    "",
				Backend: BackendNone,
				Decls:   []Decl{},
			}
			continue
		}

		// Go files are always parsed in-process.
		if lang == LangGo {
			f, err := Outline(ctx, s.Abs, s.Src, opts)
			if err != nil {
				files[i] = File{
					Path:    filePath(s.Abs, opts.Root),
					Lang:    lang,
					Backend: BackendNone,
					Decls:   []Decl{},
				}
				continue
			}
			files[i] = f
			continue
		}

		// Non-Go, in-table file: read source and check eligibility.
		src := s.Src
		if src == nil {
			data, err := loadSourceIfNeeded(s.Abs, nil, maxBytes)
			if err != nil {
				// Unreadable: return as none and continue.
				files[i] = File{
					Path:    filePath(s.Abs, opts.Root),
					Lang:    lang,
					Backend: BackendNone,
					Decls:   []Decl{},
				}
				continue
			}
			src = data
		}

		// Check size limit.
		if int64(len(src)) > maxBytes {
			files[i] = File{
				Path:    filePath(s.Abs, opts.Root),
				Lang:    lang,
				Backend: BackendNone,
				Decls:   []Decl{},
			}
			continue
		}

		// Binary check.
		sniff := src
		if len(sniff) > binarySniffSize {
			sniff = sniff[:binarySniffSize]
		}
		if bytes.IndexByte(sniff, 0) >= 0 {
			files[i] = File{
				Path:    filePath(s.Abs, opts.Root),
				Lang:    lang,
				Backend: BackendNone,
				Decls:   []Decl{},
			}
			continue
		}

		// Eligible for ctags or heuristic.
		ctagsEntries = append(ctagsEntries, ctagsBatchEntry{
			srcIdx: i,
			abs:    s.Abs,
			src:    src,
		})
	}

	// Run ctags batches if Runner is available.
	if opts.Runner != nil && len(ctagsEntries) > 0 {
		batches := makeBatches(ctagsEntries)
		for _, batch := range batches {
			if err := ctx.Err(); err != nil {
				return nil, Stats{}, err
			}

			tagsByIdx, malformed, err := runCtagsBatch(ctx, opts.Runner, batch)
			stats.MalformedLines += malformed

			if err != nil {
				// Check for context cancellation.
				if ctx.Err() != nil {
					return nil, Stats{}, ctx.Err()
				}
				// Runner error: fall back to heuristic/none for this batch.
				stats.Fallbacks++
				for _, e := range batch {
					// TODO(task 5): heuristic fallback.
					files[e.srcIdx] = finishFile(File{
						Path:    filePath(e.abs, opts.Root),
						Lang:    langForExt(filepath.Ext(e.abs)),
						Backend: BackendNone,
						Decls:   []Decl{},
					})
				}
				continue
			}

			// Assign ctags results to files.
			for _, e := range batch {
				decls, ok := tagsByIdx[e.srcIdx]
				if !ok {
					decls = []Decl{}
				}
				files[e.srcIdx] = finishFile(File{
					Path:    filePath(e.abs, opts.Root),
					Lang:    langForExt(filepath.Ext(e.abs)),
					Backend: BackendCtags,
					Decls:   decls,
				})
			}
		}
	} else {
		// No Runner: fall back to heuristic/none for all non-Go files.
		for _, e := range ctagsEntries {
			// TODO(task 5): heuristic backend.
			files[e.srcIdx] = finishFile(File{
				Path:    filePath(e.abs, opts.Root),
				Lang:    langForExt(filepath.Ext(e.abs)),
				Backend: BackendNone,
				Decls:   []Decl{},
			})
		}
	}

	return files, stats, nil
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

// finishFile guarantees non-nil Decls and sorts by StartLine then Name.
func finishFile(f File) File {
	if f.Decls == nil {
		f.Decls = []Decl{}
	}
	sort.SliceStable(f.Decls, func(i, j int) bool {
		if f.Decls[i].StartLine != f.Decls[j].StartLine {
			return f.Decls[i].StartLine < f.Decls[j].StartLine
		}
		return f.Decls[i].Name < f.Decls[j].Name
	})
	return f
}
