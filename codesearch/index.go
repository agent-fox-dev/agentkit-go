//go:build !windows

package codesearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"

	zoekt "github.com/sourcegraph/zoekt"
	zoektindex "github.com/sourcegraph/zoekt/index"
)

// maxFileSize is the per-file size limit for indexing (1 MiB).
const maxFileSize = 1 << 20

// binarySniffSize is how many bytes are checked for a NUL byte.
const binarySniffSize = 8 * 1024

// outlineBatchSize is the maximum number of files per OutlineMany call.
const outlineBatchSize = 100

// sweepAge is how old a sibling run directory must be before it is removed.
const sweepAge = 24 * time.Hour

// Options configures the codesearch index.
type Options struct {
	// Ignore is the ignore configuration, typically the same value the
	// embedder passes to tools.Options.
	Ignore tools.IgnoreOptions

	// Env replaces the subprocess environment for ctags. Nil means
	// tools.ReducedEnv(nil).
	Env []string

	// DisableCtags forces the runner to nil, so every file falls back to
	// the heuristic or go/ast backend.
	DisableCtags bool

	// Runner, when non-nil, replaces the default CtagsRunner. It is the
	// seam tests use to inject a deterministic backend.
	Runner func(ctx context.Context, args []string) ([]byte, error)

	// MaxFiles is the file-count bound for a build. Zero or negative means
	// 100 000.
	MaxFiles int

	// MaxBytes is the byte-count bound for indexed content. Zero or negative
	// means 1 GiB.
	MaxBytes int64

	// MaxBuildTime is the wall-time bound for a build. Zero or negative means
	// 60 s.
	MaxBuildTime time.Duration

	// TempDir is the directory for shard files. Empty means os.TempDir().
	TempDir string
}

// normalizeOptions applies defaults to zero/negative values.
func normalizeOptions(o Options) Options {
	if o.MaxFiles <= 0 {
		o.MaxFiles = 100_000
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1 << 30 // 1 GiB
	}
	if o.MaxBuildTime <= 0 {
		o.MaxBuildTime = 60 * time.Second
	}
	if o.TempDir == "" {
		o.TempDir = defaultTempDir()
	}
	if o.Env == nil {
		o.Env = tools.ReducedEnv(nil)
	}
	return o
}

// New creates a codesearch index for the given workspace. It returns a
// tools.Index without starting a goroutine, running a process, walking the
// workspace or touching the disk. It returns an error if ws is nil.
func New(ws *tools.Workspace, opts Options) (*Index, error) {
	if ws == nil {
		return nil, errors.New("codesearch: workspace must not be nil")
	}

	opts = normalizeOptions(opts)

	return &Index{
		ws:    ws,
		opts:  opts,
		runID: newRunID(),
	}, nil
}

// Index is the codesearch index. It implements tools.Index.
type Index struct {
	ws   *tools.Workspace
	opts Options

	// runID is a unique identifier for this index instance.
	runID string

	// mu guards index state.
	mu sync.RWMutex

	// buildCount tracks how many times the index has been built.
	buildCount atomic.Int32

	// built is true after a successful build.
	built bool

	// runDir is the shard directory for this instance.
	runDir string

	// indexedFiles is the set of slash-separated workspace-relative paths
	// that are in the index.
	indexedFiles map[string]bool

	// stats holds build statistics.
	stats BuildStatsResult

	// outlineFiles holds the outline.File per indexed file.
	outlineFiles map[string]outline.File

	// closed tracks whether Close has been called.
	closed bool

	// runner is the lazily resolved outline runner.
	runnerOnce sync.Once
	runner     func(ctx context.Context, args []string) ([]byte, error)

	// lastTouch is the last time the run directory was touched.
	lastTouch time.Time

	// testOutlineHook, when set, is called for each outline batch with
	// (root, batchSize, runnerNonNil). Tests use it to verify batching.
	testOutlineHook func(root string, batchSize int, runner bool)
}

// BuildStatsResult holds statistics from a build.
type BuildStatsResult struct {
	BinarySkipped       int
	OversizedSkipped    int
	CtagsProcessSpawned bool
	FilesIndexed        int
}

// BuildCount returns the number of times the index has been built.
func (idx *Index) BuildCount() int {
	return int(idx.buildCount.Load())
}

// BuildStats returns statistics from the last build.
func (idx *Index) BuildStats() BuildStatsResult {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.stats
}

// IndexedFiles returns the set of slash-separated workspace-relative paths
// that are in the index.
func (idx *Index) IndexedFiles() map[string]bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make(map[string]bool, len(idx.indexedFiles))
	for k, v := range idx.indexedFiles {
		out[k] = v
	}
	return out
}

// RunDir returns the run directory path.
func (idx *Index) RunDir() string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.runDir
}

// Symbols implements tools.Index.
func (idx *Index) Symbols(_ context.Context, _ tools.SymbolQuery) (tools.SymbolAnswer, bool, error) {
	return tools.SymbolAnswer{}, false, nil
}

// Tools implements tools.Index.
func (idx *Index) Tools() []core.Tool {
	return nil
}

// Invalidate implements tools.Index. It never blocks on a build or query in
// progress for longer than it takes to set a flag, never returns an error and
// never panics on a closed index.
func (idx *Index) Invalidate(_ string) {}

// Close releases resources. It is idempotent.
func (idx *Index) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.closed {
		return nil
	}
	idx.closed = true
	if idx.runDir != "" {
		os.RemoveAll(idx.runDir)
	}
	return nil
}

// outlineRunner returns the lazily resolved runner. It is nil when ctags is
// disabled and no custom Runner was provided.
func (idx *Index) outlineRunner() func(ctx context.Context, args []string) ([]byte, error) {
	idx.runnerOnce.Do(func() {
		switch {
		case idx.opts.DisableCtags:
			idx.runner = nil
		case idx.opts.Runner != nil:
			idx.runner = idx.opts.Runner
		default:
			idx.runner = tools.CtagsRunner(idx.opts.Env)
		}
	})
	return idx.runner
}

// hashDir returns the hash directory name for the workspace root.
func (idx *Index) hashDir() string {
	h := sha256.Sum256([]byte(idx.ws.Root))
	return fmt.Sprintf("agentkit-codesearch-%x", h[:])
}

// hashDirPath returns the full path to the hash directory.
func (idx *Index) hashDirPath() string {
	return filepath.Join(idx.opts.TempDir, idx.hashDir())
}

// runDirPath returns the full path to this instance's run directory.
func (idx *Index) runDirPath() string {
	return filepath.Join(idx.hashDirPath(), idx.runID)
}

// newRunID generates a unique run ID using timestamp and random data.
func newRunID() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d-%p-%d", time.Now().UnixNano(), h, os.Getpid())
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// sweepOldRuns removes sibling run directories older than sweepAge.
func (idx *Index) sweepOldRuns() {
	hashDir := idx.hashDirPath()
	entries, err := os.ReadDir(hashDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Never remove our own directory.
		if name == idx.runID {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > sweepAge {
			os.RemoveAll(filepath.Join(hashDir, name))
		}
	}
}

// Build triggers the index build. It walks the workspace, collects files,
// outlines them, and builds a zoekt index.
func (idx *Index) Build(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.closed {
		return errors.New("index_closed")
	}

	// Create the run directory.
	runDir := idx.runDirPath()
	hashDir := idx.hashDirPath()

	if err := os.MkdirAll(hashDir, 0o700); err != nil {
		return fmt.Errorf("index_failed: %w", err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return fmt.Errorf("index_failed: %w", err)
	}
	idx.runDir = runDir

	// Sweep old sibling run directories.
	idx.sweepOldRuns()

	// Walk the workspace and collect eligible files.
	type fileEntry struct {
		rel string // slash-separated workspace-relative path
		abs string // absolute path
	}
	var files []fileEntry
	var stats BuildStatsResult

	err := tools.Walk(ctx, idx.ws, idx.ws.Root, tools.WalkOptions{
		Ignore:        idx.opts.Ignore,
		IncludeHidden: false,
	}, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		abs := filepath.Join(idx.ws.Root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil {
			return nil // skip unreadable
		}

		// Size check.
		if fi.Size() > maxFileSize {
			stats.OversizedSkipped++
			return nil
		}

		// Binary check: NUL in first 8 KiB.
		f, err := os.Open(abs)
		if err != nil {
			return nil
		}
		buf := make([]byte, binarySniffSize)
		n, _ := f.Read(buf)
		f.Close()
		if bytes.IndexByte(buf[:n], 0) >= 0 {
			stats.BinarySkipped++
			return nil
		}

		files = append(files, fileEntry{rel: rel, abs: abs})
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			// Cancelled: discard partial build.
			os.RemoveAll(runDir)
			idx.runDir = ""
			return fmt.Errorf("aborted: %w", ctx.Err())
		}
		return fmt.Errorf("index_failed: walk error: %w", err)
	}

	// Outline files in batches of at most outlineBatchSize.
	runner := idx.outlineRunner()
	outlineOpts := outline.Options{
		Root:   idx.ws.Root,
		Runner: runner,
	}

	outlineResults := make(map[string]outline.File, len(files))

	for i := 0; i < len(files); i += outlineBatchSize {
		end := i + outlineBatchSize
		if end > len(files) {
			end = len(files)
		}
		batch := files[i:end]

		if idx.testOutlineHook != nil {
			idx.testOutlineHook(idx.ws.Root, len(batch), runner != nil)
		}

		srcs := make([]outline.Source, len(batch))
		for j, fe := range batch {
			srcs[j] = outline.Source{Abs: fe.abs}
		}

		outFiles, _, err := outline.OutlineMany(ctx, srcs, outlineOpts)
		if err != nil {
			if ctx.Err() != nil {
				os.RemoveAll(runDir)
				idx.runDir = ""
				return fmt.Errorf("aborted: %w", ctx.Err())
			}
			// Non-fatal: continue without symbols for this batch.
			for _, fe := range batch {
				outlineResults[fe.rel] = outline.File{
					Path:    fe.rel,
					Backend: outline.BackendNone,
					Decls:   []outline.Decl{},
				}
			}
			continue
		}

		for j, fe := range batch {
			if j < len(outFiles) {
				outlineResults[fe.rel] = outFiles[j]
			}
		}
	}

	// Build the zoekt index.
	builderOpts := zoektindex.Options{
		IndexDir:     runDir,
		DisableCTags: true,
		RepositoryDescription: zoekt.Repository{
			Name: "workspace",
			Branches: []zoekt.RepositoryBranch{
				{Name: "HEAD", Version: "local"},
			},
		},
	}

	builder, err := zoektindex.NewBuilder(builderOpts)
	if err != nil {
		return fmt.Errorf("index_failed: builder: %w", err)
	}

	indexedFiles := make(map[string]bool, len(files))

	for _, fe := range files {
		content, err := os.ReadFile(fe.abs)
		if err != nil {
			continue // skip unreadable
		}

		doc := zoektindex.Document{
			Name:     fe.rel,
			Content:  content,
			Branches: []string{"HEAD"},
		}

		// Detect language from extension.
		ext := filepath.Ext(fe.abs)
		lang := langForExt(ext)
		if lang != "" {
			doc.Language = lang
		}

		// Add symbol sections from outline.
		if of, ok := outlineResults[fe.rel]; ok && len(of.Decls) > 0 {
			sections, metadata := declsToSymbols(content, of.Decls)
			doc.Symbols = sections
			doc.SymbolsMetaData = metadata
		}

		if err := builder.Add(doc); err != nil {
			continue // skip files that fail to add
		}
		indexedFiles[fe.rel] = true
	}

	if err := builder.Finish(); err != nil {
		return fmt.Errorf("index_failed: finish: %w", err)
	}

	stats.FilesIndexed = len(indexedFiles)
	idx.indexedFiles = indexedFiles
	idx.outlineFiles = outlineResults
	idx.stats = stats
	idx.built = true
	idx.buildCount.Add(1)

	return nil
}

// declsToSymbols converts outline declarations to zoekt symbol sections.
// It computes byte offsets from line numbers in the content.
func declsToSymbols(content []byte, decls []outline.Decl) ([]zoektindex.DocumentSection, []*zoekt.Symbol) {
	// Build a line-to-byte-offset table.
	lineOffsets := computeLineOffsets(content)

	var sections []zoektindex.DocumentSection
	var metadata []*zoekt.Symbol

	for _, d := range decls {
		if d.StartLine <= 0 || d.StartLine > len(lineOffsets) {
			continue
		}

		start := lineOffsets[d.StartLine-1]
		var end uint32
		if d.EndLine > 0 && d.EndLine <= len(lineOffsets) {
			// End of the end line.
			if d.EndLine < len(lineOffsets) {
				end = lineOffsets[d.EndLine] - 1 // before the newline
			} else {
				end = uint32(len(content))
			}
		} else {
			// Just the start line.
			if d.StartLine < len(lineOffsets) {
				end = lineOffsets[d.StartLine] - 1
			} else {
				end = uint32(len(content))
			}
		}

		if end <= start {
			end = start + 1
			if end > uint32(len(content)) {
				end = uint32(len(content))
			}
		}

		sections = append(sections, zoektindex.DocumentSection{
			Start: start,
			End:   end,
		})

		kind := string(d.Kind)
		sym := d.Name
		if d.Container != "" {
			sym = d.Container + "." + d.Name
		}

		metadata = append(metadata, &zoekt.Symbol{
			Sym:  sym,
			Kind: kind,
		})
	}

	return sections, metadata
}

// computeLineOffsets returns the byte offset of the start of each line.
// lineOffsets[0] is the offset of line 1.
func computeLineOffsets(content []byte) []uint32 {
	offsets := []uint32{0}
	for i, b := range content {
		if b == '\n' {
			offsets = append(offsets, uint32(i+1))
		}
	}
	return offsets
}

// langForExt maps file extensions to language names for zoekt.
func langForExt(ext string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".js":
		return "JavaScript"
	case ".ts":
		return "TypeScript"
	case ".java":
		return "Java"
	case ".c", ".h":
		return "C"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "C++"
	case ".rs":
		return "Rust"
	case ".rb":
		return "Ruby"
	case ".sh", ".bash":
		return "Shell"
	case ".md":
		return "Markdown"
	case ".json":
		return "JSON"
	case ".yaml", ".yml":
		return "YAML"
	case ".xml":
		return "XML"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".sql":
		return "SQL"
	case ".r":
		return "R"
	case ".swift":
		return "Swift"
	case ".kt":
		return "Kotlin"
	case ".scala":
		return "Scala"
	case ".lua":
		return "Lua"
	case ".pl", ".pm":
		return "Perl"
	case ".php":
		return "PHP"
	case ".cs":
		return "C#"
	case ".txt":
		return ""
	default:
		return ""
	}
}
