//go:build !windows

package codesearch

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func TestNewStartsNothingAndWritesNothing(t *testing.T) {
	ws := openWorkspace(t, testutil.Tree(t, map[string]string{"a.go": "package p\n"}))
	tmp := t.TempDir()

	g0 := runtime.NumGoroutine()
	built, err := New(ws, Options{TempDir: tmp})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if g := runtime.NumGoroutine(); g > g0 {
		t.Errorf("goroutines %d -> %d after New", g0, g)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("New wrote %d entries into TempDir", len(entries))
	}
	idx := built.(*Index)
	if idx.BuildCount() != 0 || idx.RunDir() != "" {
		t.Errorf("BuildCount = %d, RunDir = %q after New, want 0 and none", idx.BuildCount(), idx.RunDir())
	}
}

// A failed New is a nil interface, not a nil *Index inside one, so an
// embedder that stores it without checking err gets no code_search.
func TestNewRefusesANilWorkspaceWithANilInterface(t *testing.T) {
	idx, err := New(nil, Options{})
	if err == nil || err.Error() != "codesearch: workspace must not be nil" {
		t.Fatalf("New(nil) err = %v", err)
	}
	if idx != nil {
		t.Fatalf("New(nil) = %T(%v), want a nil interface", idx, idx)
	}
	all, err := tools.For(openWorkspace(t, t.TempDir()), tools.Options{Index: idx})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name == "code_search" {
			t.Error("tools.For added code_search for an index that failed to construct")
		}
	}
}

func TestNormalizeOptionsFillsZeroAndNegativeLimits(t *testing.T) {
	def := Options{MaxFiles: 100_000, MaxBytes: 1 << 30, MaxBuildTime: time.Minute, TempDir: os.TempDir()}
	for _, c := range []struct {
		in, want Options
	}{
		{Options{}, def},
		{Options{MaxFiles: -1, MaxBytes: -5, MaxBuildTime: -time.Second}, def},
		{Options{MaxFiles: 50, MaxBytes: 1024, MaxBuildTime: 5 * time.Second, TempDir: "/custom"},
			Options{MaxFiles: 50, MaxBytes: 1024, MaxBuildTime: 5 * time.Second, TempDir: "/custom"}},
	} {
		if got := normalizeOptions(c.in); got != c.want {
			t.Errorf("normalizeOptions(%+v) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// lockfile is package-lock-style JSON under 1 MiB with far more distinct
// trigrams than zoekt indexes in one file.
func lockfile() string {
	var b strings.Builder
	b.WriteString("{\n")
	for i := range 1500 {
		sum := sha512.Sum512([]byte(fmt.Sprint(i)))
		fmt.Fprintf(&b, "  \"pkg%d\": {\"integrity\": \"sha512-%s\"},\n", i, base64.StdEncoding.EncodeToString(sum[:]))
	}
	b.WriteString("  \"left-pad\": {}\n}\n")
	return b.String()
}

// The index holds the regular files the workspace walk yields, less what
// zoekt could not search, and counts each skipped file under its reason.
func TestBuildIndexesTheSearchableFilesOfTheWalk(t *testing.T) {
	root := testutil.Tree(t, map[string]string{
		"main.go":           "package main\nfunc main() {}\n",
		"README.md":         "# left-pad\n",
		"sub/.gitignore":    "*.log\n",
		"sub/code.go":       "package sub\n",
		"sub/debug.log":     "log line\n",
		".hidden/secret.go": "package hidden\n",
		"nested/.git/HEAD":  "ref: refs/heads/main\n",
		"nested/lib.go":     "package nested\n",
		"exact1m.txt":       strings.Repeat("y", maxFileSize),
		"huge.txt":          strings.Repeat("x", maxFileSize+1),
		"binary.dat":        "hello\x00world\n",
		"late.bin":          strings.Repeat("left-pad text\n", 1000) + "\x00\n",
		"package-lock.json": lockfile(),
		"ab.txt":            "ab",
	})
	if err := os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	idx := indexFor(t, openWorkspace(t, root), Options{})
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := slices.Sorted(maps.Keys(idx.IndexedFiles()))
	if want := []string{"README.md", "exact1m.txt", "main.go", "nested/lib.go", "sub/code.go"}; !slices.Equal(got, want) {
		t.Errorf("indexed %v, want %v", got, want)
	}
	want := BuildStatsResult{BinarySkipped: 2, OversizedSkipped: 1, TooManyTrigramsSkipped: 1, TooSmallSkipped: 1, FilesIndexed: 5}
	if st := idx.BuildStats(); st != want {
		t.Errorf("BuildStats = %+v, want %+v", st, want)
	}
	r := find(t, idx, "left-pad")
	if got := paths(r); !slices.Equal(got, []string{"README.md"}) {
		t.Errorf("left-pad found in %v, want only README.md", got)
	}
	skipped := r.Data["skipped"].(map[string]any)
	if skipped["binary"] != 2 || skipped["oversized"] != 1 || skipped["too_many_trigrams"] != 1 || skipped["too_small"] != 1 {
		t.Errorf("Data.skipped = %v", skipped)
	}
}

func TestBuildIndexesExactlyTheFilesTheWalkYields(t *testing.T) {
	trees := 20
	if testing.Short() {
		trees = 5
	}
	for seed := range uint64(trees) {
		root := t.TempDir()
		genTree(t, rand.New(rand.NewPCG(seed, 54)), root)
		ws := openWorkspace(t, root)
		want := map[string]bool{}
		if err := ws.Walk(context.Background(), "", workspace.WalkOptions{}, func(rel string, d fs.DirEntry) error {
			if d.Type().IsRegular() {
				want[rel] = true
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		idx := indexFor(t, ws, Options{})
		if err := idx.Build(context.Background()); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if got := idx.IndexedFiles(); !maps.Equal(got, want) {
			t.Fatalf("seed %d: indexed %v, walked %v", seed, got, want)
		}
	}
}

// genTree writes a random tree of small files: nested .gitignore files with
// negations, nested repositories and hidden entries.
func genTree(t *testing.T, r *rand.Rand, root string) {
	names := []string{"a.go", "b.go", "c.txt", "build", "tmp", ".hid", "keep.log", "x.log", "top", "ab.go", "sub"}
	pats := []string{"*.log", "build/", "!keep.log", "/top", "tmp", "a*", "!ab.go", "b.go", "**/x.log", "*.txt", ".hid"}
	files := map[string]string{"main.go": "package main\n\nfunc main() {}\n"}
	var mk func(dir string, depth int)
	mk = func(dir string, depth int) {
		if r.IntN(4) == 0 {
			files[dir+".git/HEAD"] = "ref: refs/heads/main\n"
		}
		if r.IntN(2) == 0 {
			files[dir+".gitignore"] = pats[r.IntN(len(pats))] + "\n" + pats[r.IntN(len(pats))] + "\n"
		}
		for _, n := range names {
			if r.IntN(3) != 0 {
				continue
			}
			if depth < 3 && r.IntN(2) == 0 {
				mk(dir+n+"/", depth+1)
				continue
			}
			files[dir+n] = "package p\n\nfunc F() {}\n"
		}
	}
	mk("", 0)
	testutil.WriteTree(t, root, files)
}

func TestBuildOutlinesInBatchesOfAtMostAHundredFiles(t *testing.T) {
	idx := newFixture(t, numbered(205, "pkg/f%03d.go", "package pkg\nfunc F%03d() {}\n"), Options{})
	var sizes []int
	idx.testOutlineHook = func(root string, n int) {
		if root != idx.ws.Root() {
			t.Errorf("outline root = %q, want %q", root, idx.ws.Root())
		}
		sizes = append(sizes, n)
	}
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sizes, []int{100, 100, 5}) {
		t.Errorf("outline batches = %v, want [100 100 5]", sizes)
	}
}

// Hitting a bound gives a partial index, which says so, and which the next
// call does not build again.
func TestBuildBoundsGiveAPartialIndexThatIsNotRetried(t *testing.T) {
	for _, c := range []struct {
		name    string
		files   int
		o       Options
		reason  string
		indexed int // -1: not checked
	}{
		{"exactly MaxFiles files is complete", 5, Options{MaxFiles: 5}, "", 5},
		{"the file past MaxFiles", 6, Options{MaxFiles: 5}, "files", 5},
		{"bytes", 20, Options{MaxBytes: 100}, "bytes", -1},
		{"time", 20, Options{MaxBuildTime: time.Nanosecond}, "time", -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, numbered(c.files, "f%02d.go", "package p\n// needle %d\n"), c.o)
			for range 2 {
				r := find(t, idx, "needle")
				if r.Data["partial"] != (c.reason != "") || r.Data["partial_reason"] != c.reason {
					t.Fatalf("partial = %v (%v), want reason %q", r.Data["partial"], r.Data["partial_reason"], c.reason)
				}
				if note, _ := r.Data["note"].(string); c.reason != "" && !strings.Contains(note, "search_files") {
					t.Errorf("note = %q, want it to point at search_files", note)
				}
				if c.indexed >= 0 && r.Data["files_indexed"] != c.indexed {
					t.Errorf("files_indexed = %v, want %d", r.Data["files_indexed"], c.indexed)
				}
			}
			if n := idx.BuildCount(); n != 1 {
				t.Errorf("BuildCount = %d after two calls, want 1", n)
			}
		})
	}
}

func TestAPartialIndexNotesItBesideTheCapMarker(t *testing.T) {
	idx := newFixture(t, numbered(12, "f%02d.go", "package p\n// needle\n"), Options{MaxFiles: 8})
	r := find(t, idx, "needle", "max_files", 2)
	note, _ := r.Data["note"].(string)
	if r.Data["truncated"] != true || !strings.Contains(note, "search_files") || !strings.Contains(note, "max_files") {
		t.Errorf("truncated = %v, note = %q, want both the cap marker and the partial note", r.Data["truncated"], note)
	}
}

// expiringCtx reports a deadline exceeded after n calls to Err: a deadline
// that passes partway through the walk.
type expiringCtx struct {
	context.Context
	left atomic.Int32
}

func (c *expiringCtx) Err() error {
	if c.left.Add(-1) < 0 {
		return context.DeadlineExceeded
	}
	return nil
}

// A deadline hit in the walk keeps the files walked so far, without symbols,
// for a grace window of a quarter of MaxBuildTime, and no longer.
func TestADeadlineInTheWalkKeepsTheWalkedFilesForAGraceWindow(t *testing.T) {
	for _, c := range []struct {
		maxBuild time.Duration
		keep     bool
	}{{time.Minute, true}, {time.Nanosecond, false}} {
		idx := newFixture(t, numbered(20, "f%02d.go", "package p\n// needle\n"), Options{MaxBuildTime: c.maxBuild})
		ctx := &expiringCtx{Context: context.Background()}
		ctx.left.Store(5)
		out, err := idx.doBuild(ctx, context.Background(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if out.partialReason != "time" || (len(out.indexedFiles) > 0) != c.keep {
			t.Errorf("MaxBuildTime %v: reason %q, %d files indexed, want time and kept=%v",
				c.maxBuild, out.partialReason, len(out.indexedFiles), c.keep)
		}
	}
}

// A call cancelled during the build is aborted and leaves no index; the next
// call builds a complete one.
func TestACancelledBuildIsDiscarded(t *testing.T) {
	idx := newFixture(t, numbered(10, "f%02d.go", "package p\n// needle\n"), Options{})
	started, release := blockOutline(t, idx)
	ctx, cancel := context.WithCancel(context.Background())
	first := async(ctx, idx, "needle")
	recv(t, started, "the build")
	cancel()
	release()
	if r := recv(t, first, "the cancelled call"); r.Error != "aborted" {
		t.Fatalf("cancelled call = %v %q, want aborted", r.OK, r.Error)
	}
	if n := idx.BuildCount(); n != 0 {
		t.Fatalf("BuildCount = %d after a cancelled build, want 0", n)
	}
	if r := find(t, idx, "needle"); r.Data["partial"] != false || idx.BuildCount() != 1 {
		t.Errorf("next call: partial = %v, BuildCount = %d, want a complete build", r.Data["partial"], idx.BuildCount())
	}
}

func TestACallBehindTheFirstBuildGivesUpWithItsContext(t *testing.T) {
	idx := newFixture(t, numbered(10, "f%02d.go", "package p\n// needle\n"), Options{})
	started, release := blockOutline(t, idx)
	first := async(context.Background(), idx, "needle")
	recv(t, started, "the build")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if r := recv(t, async(ctx, idx, "needle"), "the waiting call"); r.Error != "aborted" {
		t.Errorf("waiting call = %v %q, want aborted", r.OK, r.Error)
	}
	if !stays(first) {
		t.Fatal("the first call returned while its build was blocked")
	}
	release()
	if r := recv(t, first, "the first call"); !r.OK {
		t.Errorf("first call: %s: %s", r.Error, r.Detail)
	}
}

// Calls arriving during the first build wait for it and use it; the
// accessors do not wait at all.
func TestCallsBehindTheFirstBuildShareIt(t *testing.T) {
	idx := newFixture(t, numbered(10, "f%02d.go", "package p\n// needle\n"), Options{})
	started, release := blockOutline(t, idx)
	first := async(context.Background(), idx, "needle")
	recv(t, started, "the build")
	second := async(context.Background(), idx, "needle")

	accessed := make(chan struct{})
	go func() {
		idx.BuildStats()
		idx.IndexedFiles()
		idx.RunDir()
		close(accessed)
	}()
	recv(t, accessed, "the accessors during the build")

	release()
	for _, ch := range []<-chan core.ToolResult{first, second} {
		if r := recv(t, ch, "a call"); !r.OK {
			t.Errorf("%s: %s", r.Error, r.Detail)
		}
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1 shared build", n)
	}
}

func TestACallBehindACancelledBuildBuildsForItself(t *testing.T) {
	idx := newFixture(t, numbered(10, "f%02d.go", "package p\n// needle\n"), Options{})
	started, release := blockOutline(t, idx)
	ctx, cancel := context.WithCancel(context.Background())
	builder := async(ctx, idx, "needle")
	recv(t, started, "the build")
	waiter := async(context.Background(), idx, "needle")

	cancel()
	release()
	if r := recv(t, builder, "the builder"); r.Error != "aborted" {
		t.Errorf("cancelled builder = %v %q, want aborted", r.OK, r.Error)
	}
	if r := recv(t, waiter, "the waiter"); !r.OK {
		t.Errorf("waiter: %s: %s", r.Error, r.Detail)
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want the waiter's own build", n)
	}
}

func TestCloseWaitsForTheFirstBuildAndRemovesItsRunDirectory(t *testing.T) {
	idx := newFixture(t, numbered(10, "f%02d.go", "package p\n// needle\n"), Options{})
	started, release := blockOutline(t, idx)
	first := async(context.Background(), idx, "needle")
	recv(t, started, "the build")
	runDir := idx.runDirPath()

	closed := make(chan error, 1)
	go func() { closed <- idx.Close() }()
	if !stays(closed) {
		t.Fatal("Close returned while a build was running")
	}
	release()
	if err := recv(t, closed, "Close"); err != nil {
		t.Fatal(err)
	}
	recv(t, first, "the first call")
	if !gone(runDir) {
		t.Errorf("run directory %s survived Close", runDir)
	}
}

func TestCloseWaitsForInFlightQueriesThenRemovesTheShards(t *testing.T) {
	idx := newFixture(t, map[string]string{"a.go": "package p\n// needle\n"}, Options{})
	find(t, idx, "needle")
	runDir, hashDir := idx.RunDir(), idx.hashDirPath()

	entered, gate := make(chan struct{}), make(chan struct{})
	idx.testSearchHook = func(context.Context, string) (*searchHookResult, error) {
		close(entered)
		<-gate
		return &searchHookResult{}, nil
	}
	query := async(context.Background(), idx, "needle")
	recv(t, entered, "the query")
	closed := make(chan error, 1)
	go func() { closed <- idx.Close() }()
	if !stays(closed) {
		t.Fatal("Close returned before the in-flight query finished")
	}
	close(gate)
	recv(t, query, "the query")
	if err := recv(t, closed, "Close"); err != nil {
		t.Fatal(err)
	}
	if !gone(runDir) || !gone(hashDir) {
		t.Errorf("shards survived Close: run dir gone %v, workspace dir gone %v", gone(runDir), gone(hashDir))
	}
	for range 2 {
		if err := idx.Close(); err != nil {
			t.Errorf("repeated Close: %v", err)
		}
	}
}

// A rebuild started by a query is waited for, and the directory it built is
// removed with the per-workspace directory.
func TestCloseWaitsForARebuild(t *testing.T) {
	idx := newFixture(t, numbered(20, "f%02d.go", "package p\n// word %d\n"), Options{})
	find(t, idx, "word")
	started, release := blockOutline(t, idx)
	for _, rel := range []string{"f00.go", "f01.go"} { // 10% dirty: the next query rebuilds
		write(t, idx, rel, "package p\n// changed\n")
		idx.Invalidate(rel)
	}
	query := async(context.Background(), idx, "word")
	recv(t, started, "the rebuild")

	closed := make(chan error, 1)
	go func() { closed <- idx.Close() }()
	if !stays(closed) {
		t.Fatal("Close returned while a rebuild was running")
	}
	release()
	if err := recv(t, closed, "Close"); err != nil {
		t.Fatal(err)
	}
	if r := recv(t, query, "the query"); r.Error != "index_closed" {
		t.Errorf("query during Close = %v %q, want index_closed", r.OK, r.Error)
	}
	if !gone(idx.hashDirPath()) {
		entries, _ := os.ReadDir(idx.hashDirPath())
		t.Errorf("the workspace's shard directory survived Close: %v", entries)
	}
}

func TestAClosedIndexRefusesWithoutBuilding(t *testing.T) {
	idx := newFixture(t, map[string]string{"main.go": "package main\nfunc main() {}\n"}, Options{})
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	if r := call(context.Background(), idx, map[string]any{"query": "main"}); r.Error != "index_closed" {
		t.Errorf("code_search after Close = %v %q, want index_closed", r.OK, r.Error)
	}
	if err := idx.Build(context.Background()); err == nil {
		t.Error("Build after Close succeeded")
	}
	if _, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "main"}); ok || err != nil {
		t.Errorf("Symbols after Close = ok:%v err:%v, want false and nil", ok, err)
	}
	if n := idx.BuildCount(); n != 0 {
		t.Errorf("BuildCount = %d, want 0", n)
	}
}

func TestConcurrentQueriesSymbolsAndInvalidations(t *testing.T) {
	idx := newFixture(t, numbered(10, "f%02d.go", "package p\n// needle\nfunc F%d() {}\n"), Options{})
	find(t, idx, "needle")
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if r := call(context.Background(), idx, map[string]any{"query": "needle"}); !r.OK {
				t.Errorf("code_search: %s: %s", r.Error, r.Detail)
			}
		}()
		go func() {
			defer wg.Done()
			if _, _, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "F0"}); err != nil {
				t.Errorf("Symbols: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			idx.Invalidate(fmt.Sprintf("f%02d.go", i%10))
		}()
	}
	wg.Wait()
}

// Each index has its own run directory, mode 0700, under a directory named
// for the SHA-256 of the workspace root.
func TestShardsLiveInARunDirectoryPerIndex(t *testing.T) {
	ws := openWorkspace(t, testutil.Tree(t, map[string]string{"main.go": "package main\n"}))
	tmp := t.TempDir()
	var dirs []string
	for range 2 {
		idx := indexFor(t, ws, Options{TempDir: tmp})
		if err := idx.Build(context.Background()); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, idx.RunDir())
	}
	sum := sha256.Sum256([]byte(ws.Root()))
	want := filepath.Join(tmp, fmt.Sprintf("agentkit-codesearch-%x", sum[:]))
	if dirs[0] == dirs[1] || filepath.Dir(dirs[0]) != want || filepath.Dir(dirs[1]) != want {
		t.Fatalf("run directories %v, want two distinct ones under %s", dirs, want)
	}
	if fi, err := os.Stat(dirs[0]); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("run directory %v: mode want 0700", err)
	}
}

// ageDir sets dir's modification time to age ago.
func ageDir(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
}

// A build removes the sibling run directories of its workspace that are over
// a day old, and nothing else.
func TestBuildSweepsSiblingRunDirectoriesOlderThanADay(t *testing.T) {
	ws := openWorkspace(t, testutil.Tree(t, map[string]string{"main.go": "package main\n"}))
	tmp := t.TempDir()
	sum := sha256.Sum256([]byte(ws.Root()))
	hashDir := filepath.Join(tmp, fmt.Sprintf("agentkit-codesearch-%x", sum[:]))
	old, recent := filepath.Join(hashDir, "old-run"), filepath.Join(hashDir, "recent-run")
	other := filepath.Join(tmp, "agentkit-codesearch-0000000000000000", "other-run")
	for _, d := range []string{old, recent, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	testutil.WriteTree(t, hashDir, map[string]string{"stray-file": "x"})
	ageDir(t, old, 25*time.Hour)
	ageDir(t, other, 25*time.Hour)
	ageDir(t, recent, time.Hour)

	idx := indexFor(t, ws, Options{TempDir: tmp})
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !gone(old) {
		t.Error("the 25-hour-old sibling survived the sweep")
	}
	for _, d := range []string{recent, other, idx.RunDir()} {
		if gone(d) {
			t.Errorf("%s was swept", d)
		}
	}
}

// ageRunDir makes idx look as it does when its run directory was last touched
// age ago.
func ageRunDir(t *testing.T, idx *Index, age time.Duration) {
	t.Helper()
	ageDir(t, idx.RunDir(), age)
	idx.mu.Lock()
	idx.lastTouch = time.Now().Add(-age)
	idx.mu.Unlock()
}

func runDirAge(t *testing.T, idx *Index) time.Duration {
	t.Helper()
	fi, err := os.Stat(idx.RunDir())
	if err != nil {
		t.Fatal(err)
	}
	return time.Since(fi.ModTime())
}

// A queried index touches its own run directory at most once an hour, so a
// sibling's sweep never finds it old.
func TestQueriesKeepTheRunDirectoryYoung(t *testing.T) {
	files := map[string]string{"a.go": "package pkg\n// needle\nfunc A() {}\n"}
	for _, c := range []struct {
		name  string
		query func(t *testing.T, idx *Index)
	}{
		{"code_search", func(t *testing.T, idx *Index) { find(t, idx, "needle") }},
		{"Symbols", func(t *testing.T, idx *Index) {
			if _, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "A"}); !ok || err != nil {
				t.Fatalf("Symbols = %v %v", ok, err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, files, Options{})
			find(t, idx, "needle")
			ageRunDir(t, idx, 25*time.Hour)
			c.query(t, idx)
			if age := runDirAge(t, idx); age > time.Minute {
				t.Errorf("run directory is %v old after a query", age)
			}
		})
	}

	t.Run("at most once an hour", func(t *testing.T) {
		idx := newFixture(t, files, Options{})
		find(t, idx, "needle")
		ageDir(t, idx.RunDir(), 25*time.Hour)
		idx.mu.Lock()
		idx.lastTouch = time.Now().Add(-30 * time.Minute)
		idx.mu.Unlock()
		for range 3 {
			find(t, idx, "needle")
		}
		if age := runDirAge(t, idx); age < 24*time.Hour {
			t.Errorf("run directory touched again within the hour (%v old)", age)
		}
	})
}

// A second index on the workspace builds after the first has lived for more
// than a day; the first, queried since, keeps its shards.
func TestASiblingBuildDoesNotSweepALiveIndex(t *testing.T) {
	ws := openWorkspace(t, testutil.Tree(t, map[string]string{"a.go": "package p\n// needle\n"}))
	tmp := t.TempDir()
	older := indexFor(t, ws, Options{TempDir: tmp})
	find(t, older, "needle")
	ageRunDir(t, older, 25*time.Hour)
	find(t, older, "needle")

	newer := indexFor(t, ws, Options{TempDir: tmp})
	if err := newer.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gone(older.RunDir()) {
		t.Fatal("the live index's run directory was swept")
	}
	find(t, older, "needle")
}

// A shard directory swapped out while a query reads it is removed only when
// the last reader is done; one nobody reads is removed at once.
func TestARetiredDirectoryOutlivesItsReaders(t *testing.T) {
	idx := newFixture(t, map[string]string{"a.go": "package p\n"}, Options{})
	read, unread := filepath.Join(t.TempDir(), "read"), filepath.Join(t.TempDir(), "unread")
	for _, d := range []string{read, unread} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	release := idx.leaseDir(read)
	idx.retireDir(read)
	if gone(read) {
		t.Fatal("removed while a reader held it")
	}
	release()
	release() // a second release changes nothing
	if !gone(read) {
		t.Error("survived its last reader")
	}
	idx.retireDir(unread)
	if !gone(unread) {
		t.Error("an unread retired directory was kept")
	}
}

// A symbol section is the declared identifier on its start line, as a whole
// word, and sections never overlap.
func TestDeclsToSymbolsMarksEachDeclaredIdentifierOnce(t *testing.T) {
	content := []byte("var alpha, beta = 1, 2\nclass Loader:\n    def load(self):\n    reload = load\n    pass\n")
	decls := []outline.Decl{
		{Kind: outline.KindVar, Name: "alpha", StartLine: 1},
		{Kind: outline.KindVar, Name: "beta", StartLine: 1},
		{Kind: outline.KindClass, Name: "Loader", StartLine: 2, EndLine: 5},
		{Kind: outline.KindMethod, Name: "load", Container: "Loader", StartLine: 3},
		{Kind: outline.KindVar, Name: "load", StartLine: 4},    // not the load in reload
		{Kind: outline.KindVar, Name: "missing", StartLine: 5}, // not on its line: the whole line
		{Kind: outline.KindVar, Name: "alpha", StartLine: 1},   // a duplicate
		{Kind: outline.KindVar, Name: "empty", StartLine: 6},   // the empty last line
		{Kind: outline.KindVar, Name: "out", StartLine: 99},
		{Kind: outline.KindVar, Name: "zero", StartLine: 0},
	}
	sections, meta := declsToSymbols(content, decls)
	var got, syms []string
	for i, s := range sections {
		if i > 0 && sections[i-1].End > s.Start {
			t.Fatalf("sections %d and %d overlap", i-1, i)
		}
		got = append(got, string(content[s.Start:s.End]))
		syms = append(syms, meta[i].Sym)
	}
	if want := []string{"alpha", "beta", "Loader", "load", "load", "    pass"}; !slices.Equal(got, want) {
		t.Errorf("sections cover %q, want %q", got, want)
	}
	if want := []string{"alpha", "beta", "Loader", "Loader.load", "load", "missing"}; !slices.Equal(syms, want) {
		t.Errorf("symbols %q, want %q", syms, want)
	}
	if want := uint32(strings.LastIndex(string(content), "load")); len(sections) > 4 && sections[4].Start != want {
		t.Errorf("the line-4 section starts at %d, want %d (the word, not the load in reload)", sections[4].Start, want)
	}
}

// Nested declarations and several names from one spec once gave zoekt
// overlapping sections and failed the whole build.
func TestNestedDeclarationsAreSearchableBySymbol(t *testing.T) {
	if !treeSitter() {
		t.Skip("the Python class needs the tree-sitter backend (cgo)")
	}
	idx := newFixture(t, map[string]string{
		"loader.py": "class Loader:\n    def load(self):\n        return 1\n\n    def save(self):\n        return 2\n",
		"vars.go":   "package p\n\nvar alpha, beta = 1, 2\n\nconst (\n\tX, Y = 3, 4\n)\n",
	}, Options{})
	for _, q := range []string{"sym:load", "sym:Loader", "sym:beta", "sym:Y"} {
		if r := find(t, idx, q); len(paths(r)) != 1 {
			t.Errorf("%s found %v, want one file", q, paths(r))
		}
	}
}

// A .h header holding C++ is C++, so lang:c++ finds it; extensions outline
// does not outline still get the language zoekt filters on.
func TestLangForSniffsTheContentThenTheExtension(t *testing.T) {
	for _, c := range []struct{ path, content, want string }{
		{"/r/w.h", "namespace ui {\nclass Widget {};\n}\n", "C++"},
		{"/r/c.h", "struct point { int x; };\n", "C"},
		{"/r/README.md", "# x", "Markdown"},
		{"/r/conf.YML", "a: 1", "YAML"},
		{"/r/notes.txt", "x", ""},
	} {
		if got := langFor(c.path, []byte(c.content)); got != c.want {
			t.Errorf("langFor(%s) = %q, want %q", c.path, got, c.want)
		}
	}
}
