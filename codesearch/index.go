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

// touchInterval is the longest a live index leaves its run directory untouched
// while it is queried. It is far below sweepAge, so a sibling's sweep never
// finds a queried index's directory old.
const touchInterval = time.Hour

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

// Index implements tools.Index; stub_windows.go asserts the same there.
var _ tools.Index = (*Index)(nil)

// New creates a codesearch index for the given workspace. It returns a
// tools.Index without starting a goroutine, running a process, walking the
// workspace or touching the disk. It returns an error if ws is nil.
//
// On error the result is a nil interface, never a nil *Index inside one, so
// an embedder that stores it in Options.Index without checking err gets no
// index instead of one whose methods run on a nil receiver. An embedder that
// wants the build counters and statistics asserts the result to *Index.
func New(ws *tools.Workspace, opts Options) (tools.Index, error) {
	idx, err := newIndex(ws, opts)
	if err != nil {
		return nil, err
	}
	return idx, nil
}

// newIndex is New with the concrete type, for the tests in this package that
// use the unexported hooks.
func newIndex(ws *tools.Workspace, opts Options) (*Index, error) {
	if ws == nil {
		return nil, errors.New("codesearch: workspace must not be nil")
	}

	opts = normalizeOptions(opts)

	return &Index{
		ws:    ws,
		opts:  opts,
		runID: newRunID(),
		dirty: newDirtyTracker(),
	}, nil
}

// searchHookResult is the return type for the testSearchHook seam.
type searchHookResult struct {
	files      []searchResultFile
	totalFiles int // total files that matched (may exceed len(files))
}

// searchResultFile is one file in a code_search result.
type searchResultFile struct {
	path       string
	score      float64
	matchCount int
	chunks     []resultChunk
	symbols    []string // declaration names for matches on start lines
}

// Index is the codesearch index. It implements tools.Index.
type Index struct {
	ws   *tools.Workspace
	opts Options

	// runID is a unique identifier for this index instance.
	runID string

	// mu guards index state. It is held only for short critical sections,
	// never across a build, so a call waiting behind a build can abandon the
	// wait when its context ends. Queries share the read side so they run
	// concurrently with each other.
	mu sync.RWMutex

	// inFlight tracks the number of in-flight queries. Close waits for
	// this to reach zero before deleting the run directory.
	inFlight sync.WaitGroup

	// buildCount tracks how many times the index has been built.
	buildCount atomic.Int32

	// overlayBuildCount tracks how many times the overlay shard has been built.
	overlayBuildCount atomic.Int32

	// revalCount tracks how many revalidation walks have been performed.
	revalCount atomic.Int32

	// built is true after a successful or partial build.
	built bool

	// partial is true when the build was stopped by a bound.
	partial bool

	// partialReason is the bound that stopped the build ("files", "bytes"
	// or "time").
	partialReason string

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

	// overlay holds the current overlay shard state for dirty files.
	overlay *overlayState

	// building is true while a build started by Build or the first query is
	// in progress.
	building bool

	// buildDone is closed when the current build finishes, whatever its
	// outcome. Waiters select on this and their own context.
	buildDone chan struct{}

	// rebuilding is true when a full rebuild is in progress.
	rebuilding bool

	// rebuildDone is closed when the current rebuild finishes.
	// Waiters select on this and their own context.
	rebuildDone chan struct{}

	// dirty tracks files modified since the index was built.
	dirty *dirtyTracker

	// fileInfos holds the (size, mtime, indexedAt) of each indexed file.
	fileInfos map[string]indexedFileInfo

	// runner is the lazily resolved outline runner.
	runnerOnce sync.Once
	runner     func(ctx context.Context, args []string) ([]byte, error)

	// lastTouch is the last time the run directory's modification time was
	// set: when a build or rebuild wrote it, or when touchRunDir refreshed
	// it. Guarded by mu.
	lastTouch time.Time

	// testOutlineHook, when set, is called for each outline batch with
	// (root, batchSize, runnerNonNil). Tests use it to verify batching.
	testOutlineHook func(root string, batchSize int, runner bool)

	// testSearchHook, when set, replaces the real zoekt search. Tests use
	// it to inject errors or blocking behaviour.
	testSearchHook func(ctx context.Context, query string) (*searchHookResult, error)

	// testRevalHook, when set, is called at the start of a revalidation
	// walk. Tests use it to block the walk and inject concurrent writes.
	testRevalHook func()

	// queryTimeout is the maximum wall time for a single search query.
	// Zero means the default of 10 s.
	queryTimeout time.Duration
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

// OverlayBuildCount returns the number of times the overlay shard has been built.
func (idx *Index) OverlayBuildCount() int {
	return int(idx.overlayBuildCount.Load())
}

// RevalCount returns the number of revalidation walks performed.
func (idx *Index) RevalCount() int {
	return int(idx.revalCount.Load())
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

// Symbols is implemented in symbols.go.

// Tools is implemented in tool.go.

// Invalidate implements tools.Index. It never blocks on a build or query in
// progress for longer than it takes to set a flag, never returns an error and
// never panics on a closed index.
func (idx *Index) Invalidate(rel string) {
	// Never panic on a closed or nil index.
	if idx == nil || idx.dirty == nil {
		return
	}
	if rel == "" {
		idx.dirty.markRevalidateAll()
	} else {
		idx.dirty.markDirty(rel)
	}
}

// Close releases resources. It waits for a build and in-flight queries to
// finish, then releases the zoekt searcher and deletes the run directory. It
// is idempotent: every call after the first returns nil immediately.
func (idx *Index) Close() error {
	idx.mu.Lock()
	if idx.closed {
		idx.mu.Unlock()
		return nil
	}
	idx.closed = true
	building, buildDone := idx.building, idx.buildDone
	idx.mu.Unlock()

	// Wait for a build in progress, so nothing writes into the run directory
	// after it is removed. No new build starts once closed is set.
	if building {
		<-buildDone
	}

	// Wait for in-flight queries to finish.
	idx.inFlight.Wait()

	idx.mu.Lock()
	runDir := idx.runDir
	idx.runDir = ""
	ov := idx.overlay
	idx.overlay = nil
	idx.mu.Unlock()

	if runDir != "" {
		os.RemoveAll(runDir)
	}
	cleanupOverlay(ov)
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

// touchRunDir sets the modification time of this index's own run directory to
// now, at most once per touchInterval, so that a sibling index's sweep sees a
// directory in use as young (03-REQ-5.6). It never touches another run's
// directory. A failure is ignored and retried by the next query.
func (idx *Index) touchRunDir() {
	idx.mu.RLock()
	runDir, last := idx.runDir, idx.lastTouch
	idx.mu.RUnlock()
	if runDir == "" || time.Since(last) < touchInterval {
		return
	}

	now := time.Now()
	if err := os.Chtimes(runDir, now, now); err != nil {
		return
	}
	idx.mu.Lock()
	// The directory may have been replaced by a rebuild since it was read;
	// that one is fresh and set its own lastTouch.
	if idx.runDir == runDir {
		idx.lastTouch = now
	}
	idx.mu.Unlock()
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

// fileEntry is a file discovered during a walk.
type fileEntry struct {
	rel string // slash-separated workspace-relative path
	abs string // absolute path
}

// buildOutput holds the results of a doBuild call.
type buildOutput struct {
	indexedFiles  map[string]bool
	outlineFiles  map[string]outline.File
	stats         BuildStatsResult
	fileInfos     map[string]indexedFileInfo
	partial       bool
	partialReason string
}

// Build triggers the index build. It walks the workspace, collects files,
// outlines them, and builds a zoekt index. Three bounds apply: MaxFiles,
// MaxBytes and MaxBuildTime. When one stops the build, the index keeps what
// it has and marks itself partial. A partial index is not retried by later
// calls. A cancelled call context discards the partial build and leaves the
// next call free to build again.
//
// A call arriving while another build is running waits for it, abandonably:
// when ctx ends first it returns an "aborted" error without waiting for the
// build to finish. The lock is not held during the build.
func (idx *Index) Build(ctx context.Context) error {
	return idx.build(ctx, false)
}

// build runs a build, or waits for the one in progress. With onlyIfUnbuilt it
// returns nil as soon as the index is built, whether by this call or by the
// one it waited for; a build that failed or was cancelled leaves the waiter
// free to build for itself.
func (idx *Index) build(ctx context.Context, onlyIfUnbuilt bool) error {
	// Claim the build slot, waiting abandonably while another call holds it.
	for {
		idx.mu.Lock()
		if idx.closed {
			idx.mu.Unlock()
			return errors.New("index_closed")
		}
		if onlyIfUnbuilt && idx.built {
			idx.mu.Unlock()
			return nil
		}
		if !idx.building {
			break
		}
		done := idx.buildDone
		idx.mu.Unlock()
		select {
		case <-done:
			// The build finished; loop to re-check the state.
		case <-ctx.Done():
			return fmt.Errorf("aborted: %w", ctx.Err())
		}
	}
	idx.building = true
	idx.buildDone = make(chan struct{})
	done := idx.buildDone
	idx.mu.Unlock()

	defer func() {
		idx.mu.Lock()
		idx.building = false
		close(done)
		idx.mu.Unlock()
	}()

	// Snapshot the dirty generation so we can clear only marks older than
	// this build when it finishes.
	_, _, buildGenStart := idx.dirty.snapshot()

	// Create a derived context with the wall-time bound.
	buildCtx, buildCancel := context.WithTimeout(ctx, idx.opts.MaxBuildTime)
	defer buildCancel()

	// Create the run directory. idx.runID is stable here: only a rebuild
	// changes it, and a rebuild needs a built index.
	runDir := idx.runDirPath()
	hashDir := idx.hashDirPath()

	if err := os.MkdirAll(hashDir, 0o700); err != nil {
		return fmt.Errorf("index_failed: %w", err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return fmt.Errorf("index_failed: %w", err)
	}

	// Sweep old sibling run directories.
	idx.sweepOldRuns()

	result, err := idx.doBuild(buildCtx, ctx, runDir)
	if err != nil {
		os.RemoveAll(runDir)
		return err
	}

	// Publish the new state under the lock.
	idx.mu.Lock()
	idx.runDir = runDir
	idx.lastTouch = time.Now()
	idx.indexedFiles = result.indexedFiles
	idx.outlineFiles = result.outlineFiles
	idx.stats = result.stats
	idx.fileInfos = result.fileInfos
	idx.built = true
	idx.partial = result.partial
	idx.partialReason = result.partialReason
	idx.buildCount.Add(1)
	idx.mu.Unlock()

	// Clear dirty marks that predate this build, keeping any marks made
	// during the build (generation-based clearing).
	idx.dirty.clearOlderThan(buildGenStart)

	return nil
}

// doBuild performs the actual index build work. It does NOT hold the main
// lock, so it can be used by both Build (which holds the lock) and rebuild
// (which does not). buildCtx is the derived context with the wall-time bound;
// callerCtx is the original caller's context for cancellation detection.
func (idx *Index) doBuild(buildCtx, callerCtx context.Context, runDir string) (*buildOutput, error) {
	var files []fileEntry
	var stats BuildStatsResult
	var partialReason string

	err := tools.Walk(buildCtx, idx.ws, idx.ws.Root, tools.WalkOptions{
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

		// File-count bound.
		if len(files) >= idx.opts.MaxFiles {
			partialReason = "files"
			return errBoundReached
		}
		return nil
	})
	if err != nil && err != errBoundReached {
		if callerCtx.Err() != nil {
			return nil, fmt.Errorf("aborted: %w", callerCtx.Err())
		}
		if buildCtx.Err() != nil && callerCtx.Err() == nil {
			partialReason = "time"
		} else {
			return nil, fmt.Errorf("index_failed: walk error: %w", err)
		}
	}

	// Outline files in batches of at most outlineBatchSize.
	runner := idx.outlineRunner()
	outlineOpts := outline.Options{
		Root:   idx.ws.Root,
		Runner: runner,
	}

	outlineResults := make(map[string]outline.File, len(files))
	var totalBytes int64

	for i := 0; i < len(files); i += outlineBatchSize {
		if buildCtx.Err() != nil && callerCtx.Err() == nil && partialReason == "" {
			partialReason = "time"
			files = files[:i]
			break
		}
		if callerCtx.Err() != nil {
			return nil, fmt.Errorf("aborted: %w", callerCtx.Err())
		}

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

		outFiles, _, err := outline.OutlineMany(buildCtx, srcs, outlineOpts)
		if err != nil {
			if callerCtx.Err() != nil {
				return nil, fmt.Errorf("aborted: %w", callerCtx.Err())
			}
			if buildCtx.Err() != nil && callerCtx.Err() == nil && partialReason == "" {
				partialReason = "time"
				files = files[:i]
				break
			}
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
		return nil, fmt.Errorf("index_failed: builder: %w", err)
	}

	indexedFiles := make(map[string]bool, len(files))
	hashes := make(map[string]uint64, len(files))

	for _, fe := range files {
		if callerCtx.Err() != nil {
			_ = builder.Finish()
			return nil, fmt.Errorf("aborted: %w", callerCtx.Err())
		}
		if buildCtx.Err() != nil && callerCtx.Err() == nil && partialReason == "" {
			partialReason = "time"
			break
		}

		content, err := os.ReadFile(fe.abs)
		if err != nil {
			continue
		}

		if totalBytes+int64(len(content)) > idx.opts.MaxBytes && partialReason == "" {
			partialReason = "bytes"
			break
		}

		doc := zoektindex.Document{
			Name:     fe.rel,
			Content:  content,
			Branches: []string{"HEAD"},
		}

		ext := filepath.Ext(fe.abs)
		lang := langForExt(ext)
		if lang != "" {
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
		indexedFiles[fe.rel] = true
		hashes[fe.rel] = contentHash(content)
		totalBytes += int64(len(content))
	}

	if err := builder.Finish(); err != nil {
		return nil, fmt.Errorf("index_failed: finish: %w", err)
	}

	stats.FilesIndexed = len(indexedFiles)

	// Record file info for revalidation.
	now := time.Now()
	fileInfos := make(map[string]indexedFileInfo, len(indexedFiles))
	for _, fe := range files {
		if !indexedFiles[fe.rel] {
			continue
		}
		abs := filepath.Join(idx.ws.Root, filepath.FromSlash(fe.rel))
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		fileInfos[fe.rel] = indexedFileInfo{
			size:      fi.Size(),
			mtime:     fi.ModTime(),
			indexedAt: now,
			hash:      hashes[fe.rel],
		}
	}

	return &buildOutput{
		indexedFiles:  indexedFiles,
		outlineFiles:  outlineResults,
		stats:         stats,
		fileInfos:     fileInfos,
		partial:       partialReason != "",
		partialReason: partialReason,
	}, nil
}

// errBoundReached is a sentinel error used to stop the walk when a bound is hit.
var errBoundReached = errors.New("bound reached")

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
