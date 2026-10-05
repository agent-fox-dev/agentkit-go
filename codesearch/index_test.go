//go:build !windows

package codesearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-1: New with a workspace returns an index without goroutines, processes,
// walks or disk writes.
func TestNewReturnsIndexWithoutSideEffects_TS03_1(t *testing.T) {
	root := t.TempDir()
	// Create a file so the workspace is non-empty.
	if err := os.WriteFile(filepath.Join(root, "hello.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()

	var runnerCalls int
	fakeRunner := func(_ context.Context, _ []string) ([]byte, error) {
		runnerCalls++
		return nil, nil
	}

	// Settle goroutine count.
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)
	g0 := runtime.NumGoroutine()

	idx, err := newIndex(ws, Options{
		TempDir: tmpDir,
		Runner:  fakeRunner,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if idx == nil {
		t.Fatal("New returned nil index")
	}
	defer idx.Close()

	// Let goroutines settle.
	runtime.Gosched()
	time.Sleep(10 * time.Millisecond)
	g1 := runtime.NumGoroutine()

	// Allow a small margin for runtime fluctuations.
	if g1 > g0+2 {
		t.Errorf("goroutine count increased from %d to %d", g0, g1)
	}

	if runnerCalls != 0 {
		t.Errorf("runner was called %d times, want 0", runnerCalls)
	}

	// TempDir should still be empty.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("TempDir has %d entries, want 0", len(entries))
	}

	// Build counter should be 0.
	if bc := idx.BuildCount(); bc != 0 {
		t.Errorf("build count = %d, want 0", bc)
	}
}

// TS-03-2: New with a nil workspace returns an error and no index.
func TestNewNilWorkspaceReturnsError_TS03_2(t *testing.T) {
	idx, err := newIndex(nil, Options{})
	if idx != nil {
		t.Fatal("expected nil index for nil workspace")
	}
	if err == nil {
		t.Fatal("expected non-nil error for nil workspace")
	}
}

// TS-03-3: Zero or negative limits resolve to defaults and nil Env resolves
// to the reduced environment.
func TestOptionsDefaults_TS03_3(t *testing.T) {
	// Zero values.
	o := normalizeOptions(Options{})
	if o.MaxFiles != 100_000 {
		t.Errorf("MaxFiles = %d, want 100000", o.MaxFiles)
	}
	if o.MaxBytes != 1<<30 {
		t.Errorf("MaxBytes = %d, want %d", o.MaxBytes, int64(1<<30))
	}
	if o.MaxBuildTime != 60*time.Second {
		t.Errorf("MaxBuildTime = %v, want 60s", o.MaxBuildTime)
	}
	if o.TempDir != os.TempDir() {
		t.Errorf("TempDir = %q, want %q", o.TempDir, os.TempDir())
	}
	wantEnv := tools.ReducedEnv(nil)
	if len(o.Env) != len(wantEnv) {
		t.Errorf("Env length = %d, want %d", len(o.Env), len(wantEnv))
	}

	// Negative values.
	o = normalizeOptions(Options{MaxFiles: -1, MaxBytes: -5, MaxBuildTime: -10 * time.Second})
	if o.MaxFiles != 100_000 {
		t.Errorf("negative MaxFiles = %d, want 100000", o.MaxFiles)
	}
	if o.MaxBytes != 1<<30 {
		t.Errorf("negative MaxBytes = %d, want %d", o.MaxBytes, int64(1<<30))
	}
	if o.MaxBuildTime != 60*time.Second {
		t.Errorf("negative MaxBuildTime = %v, want 60s", o.MaxBuildTime)
	}

	// Positive values are kept.
	o = normalizeOptions(Options{MaxFiles: 50, MaxBytes: 1024, MaxBuildTime: 5 * time.Second, TempDir: "/custom"})
	if o.MaxFiles != 50 {
		t.Errorf("positive MaxFiles = %d, want 50", o.MaxFiles)
	}
	if o.MaxBytes != 1024 {
		t.Errorf("positive MaxBytes = %d, want 1024", o.MaxBytes)
	}
	if o.MaxBuildTime != 5*time.Second {
		t.Errorf("positive MaxBuildTime = %v, want 5s", o.MaxBuildTime)
	}
	if o.TempDir != "/custom" {
		t.Errorf("positive TempDir = %q, want /custom", o.TempDir)
	}

	// Explicit Env is kept.
	o = normalizeOptions(Options{Env: []string{"FOO=bar"}})
	if len(o.Env) != 1 || o.Env[0] != "FOO=bar" {
		t.Errorf("explicit Env = %v, want [FOO=bar]", o.Env)
	}
}

// TS-03-33: The indexed file set equals the tools.Walk set with hidden off,
// regular, non-binary and at most 1 MiB.
func TestIndexedFileSetEqualsWalkSet_TS03_33(t *testing.T) {
	root := t.TempDir()

	// Regular Go file.
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n")
	// Regular text file.
	mkFile(t, root, "README.md", "# Hello\n")
	// Nested .gitignore that ignores *.log.
	mkFile(t, root, "sub/.gitignore", "*.log\n")
	mkFile(t, root, "sub/code.go", "package sub\n")
	mkFile(t, root, "sub/debug.log", "log line\n")
	// Hidden directory with a file.
	mkFile(t, root, ".hidden/secret.go", "package hidden\n")
	// Binary file (NUL in first 8 KiB).
	mkFile(t, root, "binary.dat", "hello\x00world\n")
	// Oversized file (1 MiB + 1 byte).
	mkFile(t, root, "huge.txt", strings.Repeat("x", 1<<20+1))
	// Exactly 1 MiB file (should be included).
	mkFile(t, root, "exact1m.txt", strings.Repeat("y", 1<<20))
	// Symlink (should be skipped by Walk since it uses WalkDir).
	os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "link.go"))
	// Nested repository.
	mkFile(t, root, "nested/.git/HEAD", "ref: refs/heads/main\n")
	mkFile(t, root, "nested/lib.go", "package nested\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Collect the expected set from tools.Walk.
	wantSet := map[string]bool{}
	err = tools.Walk(context.Background(), ws, ws.Root, tools.WalkOptions{
		Ignore:        tools.NoGlobalExcludes(),
		IncludeHidden: false,
	}, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil || fi.Size() > 1<<20 {
			return nil
		}
		// Binary check.
		f, err := os.Open(abs)
		if err != nil {
			return nil
		}
		defer f.Close()
		buf := make([]byte, 8192)
		n, _ := f.Read(buf)
		if bytes.IndexByte(buf[:n], 0) >= 0 {
			return nil
		}
		wantSet[rel] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Build the index.
	tmpDir := t.TempDir()
	idx, err := newIndex(ws, Options{
		TempDir:      tmpDir,
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Trigger the build.
	err = idx.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	gotSet := idx.IndexedFiles()

	// Compare.
	for f := range wantSet {
		if !gotSet[f] {
			t.Errorf("expected file %q in index, not found", f)
		}
	}
	for f := range gotSet {
		if !wantSet[f] {
			t.Errorf("unexpected file %q in index", f)
		}
	}

	// Verify specific exclusions.
	for _, excluded := range []string{".hidden/secret.go", "binary.dat", "huge.txt", "link.go", "sub/debug.log"} {
		if gotSet[excluded] {
			t.Errorf("file %q should be excluded from index", excluded)
		}
	}

	// Verify exact 1 MiB file is present.
	if !gotSet["exact1m.txt"] {
		t.Error("exact1m.txt (exactly 1 MiB) should be in the index")
	}

	// Verify document names are slash-separated.
	for f := range gotSet {
		if strings.Contains(f, "\\") {
			t.Errorf("document name %q contains backslash", f)
		}
	}
}

// TS-03-34: Skipped files are counted, zoekt's git indexer is unused and
// CTagsPath is empty so zoekt starts no ctags.
func TestSkippedFilesCountedAndNoCtagsProcess_TS03_34(t *testing.T) {
	root := t.TempDir()

	// Binary file.
	mkFile(t, root, "binary.dat", "hello\x00world\n")
	// Oversized file.
	mkFile(t, root, "huge.txt", strings.Repeat("x", 1<<20+1))
	// Normal file.
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	idx, err := newIndex(ws, Options{
		TempDir:      tmpDir,
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	err = idx.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	stats := idx.BuildStats()
	if stats.BinarySkipped != 1 {
		t.Errorf("BinarySkipped = %d, want 1", stats.BinarySkipped)
	}
	if stats.OversizedSkipped != 1 {
		t.Errorf("OversizedSkipped = %d, want 1", stats.OversizedSkipped)
	}

	// Verify no gitindex package is imported (checked by policy_test.go).
	// Here we just verify the builder was configured with empty CTagsPath
	// by checking that no ctags process was spawned.
	if stats.CtagsProcessSpawned {
		t.Error("zoekt should not have spawned a ctags process")
	}
}

// TS-03-35: Symbols come from OutlineMany in batches of at most 100 with the
// right Root and Runner selection.
func TestOutlineSymbolBatching_TS03_35(t *testing.T) {
	root := t.TempDir()

	// Create 250 files: mix of Go and text.
	for i := 0; i < 150; i++ {
		mkFile(t, root, fmt.Sprintf("pkg/file%03d.go", i),
			fmt.Sprintf("package pkg\nfunc Func%03d() {}\n", i))
	}
	for i := 0; i < 100; i++ {
		mkFile(t, root, fmt.Sprintf("data/file%03d.txt", i),
			fmt.Sprintf("line %d\n", i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Test 1: With a recording fake Runner, verify batching and root.
	var batchSizes []int
	var batchRoots []string
	var runnerUsedInBatch []bool

	fakeRunner := func(_ context.Context, _ []string) ([]byte, error) {
		return nil, fmt.Errorf("fake runner: not implemented")
	}

	tmpDir := t.TempDir()
	idx, err := newIndex(ws, Options{
		TempDir: tmpDir,
		Ignore:  tools.NoGlobalExcludes(),
		Runner:  fakeRunner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Record the outline options used.
	idx.testOutlineHook = func(root string, batchSize int, runner bool) {
		batchRoots = append(batchRoots, root)
		batchSizes = append(batchSizes, batchSize)
		runnerUsedInBatch = append(runnerUsedInBatch, runner)
	}

	err = idx.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Verify we got batches.
	if len(batchSizes) == 0 {
		t.Fatal("no outline batches recorded")
	}

	// Verify batches are at most 100.
	totalFiles := 0
	for i, bs := range batchSizes {
		if bs > 100 {
			t.Errorf("batch %d has %d files, want at most 100", i, bs)
		}
		totalFiles += bs
	}

	// Verify total files equals 250.
	if totalFiles != 250 {
		t.Errorf("total files across batches = %d, want 250", totalFiles)
	}

	// Verify all roots are the workspace root.
	for i, r := range batchRoots {
		if r != ws.Root {
			t.Errorf("batch %d root = %q, want %q", i, r, ws.Root)
		}
	}

	// Verify runner was used (Options.Runner was set).
	for i, used := range runnerUsedInBatch {
		if !used {
			t.Errorf("batch %d: runner should be non-nil when Options.Runner is set", i)
		}
	}

	// Test 2: With DisableCtags, runner should be nil.
	tmpDir2 := t.TempDir()
	idx2, err := newIndex(ws, Options{
		TempDir:      tmpDir2,
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()

	var disabledRunnerUsed bool
	idx2.testOutlineHook = func(_ string, _ int, runner bool) {
		if runner {
			disabledRunnerUsed = true
		}
	}

	err = idx2.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if disabledRunnerUsed {
		t.Error("with DisableCtags, runner should be nil")
	}
}

// TS-03-37: Shards live in <TempDir>/agentkit-codesearch-<hash>/<run id>/
// with mode 0700 and a 64-bit-or-longer SHA hash.
func TestShardDirectoryStructure_TS03_37(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()

	// Build two indexes over the same root.
	idx1, err := newIndex(ws, Options{TempDir: tmpDir, Ignore: tools.NoGlobalExcludes(), DisableCtags: true})
	if err != nil {
		t.Fatal(err)
	}
	defer idx1.Close()

	idx2, err := newIndex(ws, Options{TempDir: tmpDir, Ignore: tools.NoGlobalExcludes(), DisableCtags: true})
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()

	if err := idx1.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := idx2.Build(context.Background()); err != nil {
		t.Fatal(err)
	}

	d1 := idx1.RunDir()
	d2 := idx2.RunDir()

	if d1 == "" || d2 == "" {
		t.Fatal("run directories should not be empty after build")
	}

	// Run directories must differ.
	if d1 == d2 {
		t.Error("two index instances must have different run directories")
	}

	// Parent directories must be the same (same hash).
	p1 := filepath.Dir(d1)
	p2 := filepath.Dir(d2)
	if p1 != p2 {
		t.Errorf("parent dirs differ: %q vs %q", p1, p2)
	}

	// The parent directory name must be agentkit-codesearch-<hash>.
	parentBase := filepath.Base(p1)
	if !strings.HasPrefix(parentBase, "agentkit-codesearch-") {
		t.Errorf("parent dir name %q does not start with agentkit-codesearch-", parentBase)
	}

	// Extract hash part and verify it's at least 16 hex chars.
	hashPart := strings.TrimPrefix(parentBase, "agentkit-codesearch-")
	if len(hashPart) < 16 {
		t.Errorf("hash part %q is shorter than 16 chars", hashPart)
	}

	// Verify hash matches sha256 prefix of the absolute root.
	absRoot := ws.Root
	h := sha256.Sum256([]byte(absRoot))
	wantPrefix := fmt.Sprintf("%x", h[:])[:len(hashPart)]
	if hashPart != wantPrefix {
		t.Errorf("hash part %q does not match sha256 prefix %q of root %q", hashPart, wantPrefix, absRoot)
	}

	// Verify directory mode is 0700 (on platforms that support it).
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(d1)
		if err != nil {
			t.Fatal(err)
		}
		mode := fi.Mode().Perm()
		if mode != 0o700 {
			t.Errorf("run dir mode = %o, want 700", mode)
		}
	}
}

// TS-03-38: A build sweeps 24-hour-old sibling run directories and leaves
// recent ones and its own untouched.
func TestBuildSweepsOldSiblings_TS03_38(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()

	// Compute the hash directory.
	absRoot := ws.Root
	h := sha256.Sum256([]byte(absRoot))
	hashHex := fmt.Sprintf("%x", h[:])
	hashDir := filepath.Join(tmpDir, "agentkit-codesearch-"+hashHex)
	if err := os.MkdirAll(hashDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Create a 25-hour-old sibling run directory.
	oldSibling := filepath.Join(hashDir, "old-run-id")
	if err := os.MkdirAll(oldSibling, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(oldSibling, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create a 1-hour-old sibling run directory.
	recentSibling := filepath.Join(hashDir, "recent-run-id")
	if err := os.MkdirAll(recentSibling, 0o700); err != nil {
		t.Fatal(err)
	}
	recentTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(recentSibling, recentTime, recentTime); err != nil {
		t.Fatal(err)
	}

	// Create a directory under a different hash (25 hours old).
	otherHashDir := filepath.Join(tmpDir, "agentkit-codesearch-0000000000000000")
	otherOld := filepath.Join(otherHashDir, "other-old-run")
	if err := os.MkdirAll(otherOld, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(otherOld, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Build the index.
	idx, err := newIndex(ws, Options{
		TempDir:      tmpDir,
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The 25-hour-old sibling should be removed.
	if _, err := os.Stat(oldSibling); !os.IsNotExist(err) {
		t.Errorf("25-hour-old sibling should be removed, but still exists")
	}

	// The 1-hour-old sibling should remain.
	if _, err := os.Stat(recentSibling); err != nil {
		t.Errorf("1-hour-old sibling should remain: %v", err)
	}

	// The other-hash directory should remain.
	if _, err := os.Stat(otherOld); err != nil {
		t.Errorf("other-hash directory should remain: %v", err)
	}

	// The index's own directory should exist.
	ownDir := idx.RunDir()
	if _, err := os.Stat(ownDir); err != nil {
		t.Errorf("own run directory should exist: %v", err)
	}
}

// mkFile creates a file at root/rel with the given content, creating
// intermediate directories as needed.
func mkFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
