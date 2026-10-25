package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// goFiles is n Go files each declaring F<i>.
func goFiles(n int, ext string) map[string]string {
	files := map[string]string{}
	for i := 0; i < n; i++ {
		if ext == ".py" {
			files[fmt.Sprintf("f%03d.py", i)] = fmt.Sprintf("def func_%03d():\n    pass\n", i)
		} else {
			files[fmt.Sprintf("f%03d.go", i)] = fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i)
		}
	}
	return files
}

func indexed(t *testing.T, r core.ToolResult) (files int, partial bool, reason string) {
	t.Helper()
	mustOK(t, r)
	partial, _ = r.Data["partial"].(bool)
	reason, _ = r.Data["partial_reason"].(string)
	return r.Data["files_indexed"].(int), partial, reason
}

func TestTheSymbolTableIndexesWhatSearchFilesSearches(t *testing.T) {
	ws := newWS(t, map[string]string{
		".gitignore": "ignored/\n", "main.go": "package main\n\nfunc Main() {}\n", "sub/sub.go": "package sub\n",
		"sub/.gitignore": "n.txt\n", "sub/kept.txt": "kept\n", "sub/n.txt": "no\n", "ignored/skip.go": "package ignored\n",
		".hidden/secret.go": "package hidden\n", "data.xyz": "unknown language\n", "binary.go": "package bin\n\x00",
		"vendor/nested/.git/HEAD": "ref\n", "vendor/nested/nested.go": "package nested\n",
	})
	mustSymlink(t, abs(ws, "main.go"), abs(ws, "link.go"))
	mustSymlink(t, abs(ws, "sub"), abs(ws, "linkdir"))
	var want int
	_ = ws.Walk(context.Background(), "", workspace.WalkOptions{}, func(_ string, d fs.DirEntry) error {
		if d.Type().IsRegular() {
			want++
		}
		return nil
	})
	// main, sub, kept, data, binary, nested; never a link, hidden or ignored file.
	if files, partial, _ := indexed(t, call(newFT(ws, Options{}).findSymbolTool(), `{"name":"x"}`)); files != want || want != 6 || partial {
		t.Fatalf("files_indexed = %d, search_files selects %d, want 6", files, want)
	}
}

func TestTheSymbolTableOutlinesInBatches(t *testing.T) {
	ft := newFT(newWS(t, goFiles(250, ".py")), Options{})
	batches := 0
	ft.testOutlineHook = func(context.Context) { batches++ }
	if files, _, _ := indexed(t, call(ft.findSymbolTool(), `{"name":"func"}`)); files != 250 || batches != 3 {
		t.Fatalf("%d files in %d batches, want 250 in 3", files, batches)
	}
}

func TestABoundedBuildIsPartialAndLaterCallsFinishIt(t *testing.T) {
	files := goFiles(40, ".go")
	for i := 0; i < 5; i++ {
		files[fmt.Sprintf("x%d.xyz", i)] = "unknown languages count toward the bound too\n"
	}
	fs := newFT(newWS(t, files), Options{Symbols: SymbolOptions{MaxFiles: 15}}).findSymbolTool()
	r := call(fs, `{"name":"F0"}`)
	n, partial, reason := indexed(t, r)
	if !partial || reason != "files" || n == 0 || n > 15 || r.Data["note"] != SymbolPartialMarker("files") || !strings.HasSuffix(r.Text, "\n"+SymbolPartialMarker("files")) {
		t.Fatalf("call 1: %d files, partial %v %q, note %v", n, partial, reason, r.Data["note"])
	}
	// Already-indexed files do not count again, so every call makes progress.
	for prev, i := n, 0; partial; i++ {
		if n, partial, _ = indexed(t, call(fs, `{"name":"F0"}`)); n <= prev || i > 5 {
			t.Fatalf("call %d indexed %d after %d", i+2, n, prev)
		}
		prev = n
	}
	if n != 45 {
		t.Fatalf("the finished table has %d files, want 45", n)
	}
	// Truncated and partial: the note is the truncation marker, and both show.
	ft := newFT(newWS(t, goFiles(30, ".go")), Options{Symbols: SymbolOptions{MaxFiles: 25}})
	r = call(ft.findSymbolTool(), `{"name":"F","max_results":1}`)
	if r.Data["note"] != SymbolMarker(1) || r.Data["partial"] != true || !strings.HasSuffix(r.Text, SymbolMarker(1)+"\n"+SymbolPartialMarker("files")) {
		t.Fatalf("Data %v Text %q", r.Data, r.Text)
	}
}

func TestTheDefaultFileBoundIsFiftyThousand(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 50 001 files")
	}
	ws := newWS(t, nil)
	for i := 0; i <= defaultMaxFiles; i++ {
		dir := abs(ws, fmt.Sprintf("d%03d", i/1000))
		if i%1000 == 0 {
			_ = os.Mkdir(dir, 0o755)
		}
		if err := os.WriteFile(fmt.Sprintf("%s/f%05d.txt", dir, i), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ft := newFT(ws, Options{Symbols: SymbolOptions{MaxDuration: time.Minute}})
	if n, partial, reason := indexed(t, call(ft.findSymbolTool(), `{"name":"F"}`)); n != defaultMaxFiles || !partial || reason != "files" {
		t.Fatalf("%d files, partial %v %q; want the default bound of 50 000", n, partial, reason)
	}
}

func TestABuildStopsAtItsTimeBound(t *testing.T) {
	ft := newFT(newWS(t, goFiles(20, ".py")), Options{Symbols: SymbolOptions{MaxDuration: 100 * time.Millisecond}})
	stopped := false
	ft.testOutlineHook = func(ctx context.Context) { <-ctx.Done(); stopped = true }
	start := time.Now()
	if _, partial, reason := indexed(t, call(ft.findSymbolTool(), `{"name":"func"}`)); !partial || reason != "time" || !stopped || time.Since(start) > 5*time.Second {
		t.Fatalf("partial %v %q, stopped %v, after %v", partial, reason, stopped, time.Since(start))
	}
	// The default bound is far enough away for one file.
	if _, partial, _ := indexed(t, call(newFT(newWS(t, goFiles(1, ".go")), Options{}).findSymbolTool(), `{"name":"F"}`)); partial {
		t.Fatal("one file is partial under the default bounds")
	}
}

func TestAScopedBuildOfAnIncompleteTableWalksOnlyTheScope(t *testing.T) {
	ws := newWS(t, map[string]string{
		".gitignore": "*.gen\n", "pkg/a/a.go": "package a\n\nfunc A() {}\n", "pkg/a/a.gen": "x\n",
		"pkg/b/b.go": "package b\n\nfunc B() {}\n", "other/o.go": "package other\n\nfunc O() {}\n",
	})
	var mu sync.Mutex
	var visited []string
	orig := walkFn
	walkFn = func(ctx context.Context, w *workspace.Workspace, sub string, o workspace.WalkOptions, fn func(string, fs.DirEntry) error) error {
		if w != ws {
			t.Error("the walk did not go through the tool set's workspace")
		}
		return orig(ctx, w, sub, o, func(rel string, d fs.DirEntry) error {
			mu.Lock()
			visited = append(visited, rel)
			mu.Unlock()
			return fn(rel, d)
		})
	}
	t.Cleanup(func() { walkFn = orig })
	ft := newFT(ws, Options{})
	r := callJSON(t, ft.findSymbolTool(), map[string]any{"name": "A", "path": abs(ws, "pkg/a")})
	if n, _, _ := indexed(t, r); n != 1 || !slices.Equal(symbolNames(r), []string{"A"}) {
		t.Fatalf("%d files, %v", n, symbolNames(r))
	}
	for _, v := range visited {
		if strings.HasPrefix(v, "pkg/b/") || strings.HasPrefix(v, "other/") || strings.HasSuffix(v, ".gen") {
			t.Errorf("visited %s", v)
		}
	}
}

func TestACompleteUnmarkedTableAnswersFromMemory(t *testing.T) {
	ft := newFT(newWS(t, map[string]string{"a.go": "package main\n\nfunc Hello() {}\n", "b.go": "package main\n\nfunc World() {}\n"}), Options{})
	if got := findNames(t, ft, "Hello"); !slices.Equal(got, []string{"Hello"}) {
		t.Fatal(got)
	}
	walks := countWalks(t)
	for _, name := range []string{"Hello", "World"} {
		if got := findNames(t, ft, name); !slices.Equal(got, []string{name}) {
			t.Fatalf("%s: %v", name, got)
		}
	}
	if n := walks.Load(); n != 0 {
		t.Fatalf("%d walks for a complete, unmarked table", n)
	}
}

// builtTable is a complete table over a.go, b.go and c.go.
func builtTable(t *testing.T) (*fileTools, *workspace.Workspace) {
	t.Helper()
	ws := newWS(t, map[string]string{
		"a.go": "package main\n\nfunc AFunc() {}\n", "b.go": "package main\n\nfunc BFunc() {}\n", "c.go": "package main\n\nfunc CFunc() {}\n",
	})
	ft := newFT(ws, Options{})
	mustOK(t, call(ft.findSymbolTool(), `{"name":"Func"}`))
	if ft.table.needsRefresh() {
		t.Fatal("the table is not complete after its first build")
	}
	return ft, ws
}

func indexedAt(ft *fileTools, rel string) time.Time {
	ft.table.lockBlocking()
	defer ft.table.unlock()
	if e := ft.table.entries[rel]; e != nil {
		return e.indexedAt
	}
	return time.Time{}
}

func TestADirtyPathIsReoutlinedOrDroppedWithoutAWalk(t *testing.T) {
	ft, ws := builtTable(t)
	mustOK(t, call(ft.editFile(), `{"path":"a.go","edits":[{"old_string":"AFunc","new_string":"ANew"}]}`))
	_ = os.Remove(abs(ws, "b.go"))
	ft.table.markDirty("b.go")
	_ = os.Remove(abs(ws, "c.go"))
	_ = os.Mkdir(abs(ws, "c.go"), 0o755) // no longer a regular file
	ft.table.markDirty("c.go")
	walks := countWalks(t)
	for name, want := range map[string]int{"ANew": 1, "AFunc": 0, "BFunc": 0, "CFunc": 0} {
		if got := findNames(t, ft, name); len(got) != want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if walks.Load() != 0 {
		t.Errorf("%d walks; only the dirty paths are refreshed", walks.Load())
	}

	// Only the dirty path is re-outlined.
	ft, ws = builtTable(t)
	bAt := indexedAt(ft, "b.go")
	writeAt(t, ws.Root(), "a.go", "package main\n\nfunc AEdited() {}\n")
	ft.table.markDirty("a.go")
	if got := findNames(t, ft, "AEdited"); len(got) != 1 || !indexedAt(ft, "b.go").Equal(bAt) {
		t.Errorf("AEdited %v; b.go must not be re-outlined", got)
	}
}

func TestANewFileOrAGitignoreEscalatesToOneRevalidation(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(ft *fileTools, ws *workspace.Workspace)
		find   string
		want   int
	}{
		{"a new file", func(ft *fileTools, ws *workspace.Workspace) {
			writeAt(t, ws.Root(), "new.go", "package main\n\nfunc NewFunc() {}\n")
			ft.table.markDirty("new.go")
		}, "NewFunc", 1},
		{"a .gitignore", func(ft *fileTools, ws *workspace.Workspace) {
			writeAt(t, ws.Root(), ".gitignore", "b.go\n")
			ft.table.markDirty(".gitignore")
		}, "BFunc", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft, ws := builtTable(t)
			walks := countWalks(t)
			c.change(ft, ws)
			if got := findNames(t, ft, c.find); len(got) != c.want || walks.Load() != 1 || ft.table.needsRefresh() {
				t.Fatalf("%s: %v after %d walks; the escalated pass clears its own mark", c.find, got, walks.Load())
			}
			_ = findNames(t, ft, "AFunc")
			if walks.Load() != 1 {
				t.Fatalf("%d walks over two calls, want 1", walks.Load())
			}
			ft.table.markRevalidateAll() // a shell command ran afterwards
			if !ft.table.needsRefresh() {
				t.Fatal("a mark made after the escalated pass was lost")
			}
		})
	}
}

func TestACancelledDirtyRefreshKeepsTheEntriesAndTheirMarks(t *testing.T) {
	ft, ws := builtTable(t)
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		writeAt(t, ws.Root(), f, "package main\n\nfunc "+strings.ToUpper(f[:1])+"New() {}\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ft.table.lockBlocking()
	ft.table.refreshDirtyPaths(ctx, ft, map[string]bool{"a.go": true, "b.go": true, "c.go": true})
	kept := len(ft.table.entries)
	ft.table.unlock()
	if dirty, _ := marks(ft.table); kept != 3 || len(dirty) != 3 {
		t.Fatalf("%d entries kept, %d marked again; want 3 and 3", kept, len(dirty))
	}
	for _, name := range []string{"ANew", "BNew", "CNew"} {
		if len(findNames(t, ft, name)) != 1 {
			t.Errorf("%s was not found by the next call", name)
		}
	}
}

func TestARevalidationReoutlinesWhatChangedAndDropsWhatIsGone(t *testing.T) {
	ws := newWS(t, map[string]string{
		"unchanged.go": "package main\n\nfunc Unchanged() {}\n", "changed.go": "package main\n\nfunc ChangedOld() {}\n",
		"deleted.go": "package main\n\nfunc Deleted() {}\n", "racy.go": "package main\n\nfunc RacyOld() {}\n",
	})
	ft := newFT(ws, Options{})
	mustOK(t, call(ft.findSymbolTool(), `{"name":"x"}`))
	// Every entry but racy.go was indexed long after its mtime.
	ft.table.lockBlocking()
	for rel, e := range ft.table.entries {
		if rel != "racy.go" {
			e.indexedAt = time.Now().Add(-10 * time.Second)
		}
	}
	ft.table.unlock()
	unchangedAt := indexedAt(ft, "unchanged.go")
	writeAt(t, ws.Root(), "changed.go", "package main\n\nfunc ChangedNew() {}\nfunc Extra() {}\n")
	_ = os.Remove(abs(ws, "deleted.go"))
	writeAt(t, ws.Root(), "new.go", "package main\n\nfunc NewFunc() {}\n")
	// The same size, in the same timestamp granule: only the racy window sees it.
	fi, _ := os.Stat(abs(ws, "racy.go"))
	writeAt(t, ws.Root(), "racy.go", "package main\n\nfunc RacyNew() {}\n")
	_ = os.Chtimes(abs(ws, "racy.go"), fi.ModTime(), fi.ModTime())
	ft.table.markRevalidateAll()
	for name, want := range map[string]int{"ChangedNew": 1, "ChangedOld": 0, "Deleted": 0, "NewFunc": 1, "RacyNew": 1, "RacyOld": 0, "Unchanged": 1} {
		if got := findNames(t, ft, name); len(got) != want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if !indexedAt(ft, "unchanged.go").Equal(unchangedAt) {
		t.Error("an unchanged file was re-outlined")
	}
}

// TestTheRacyWindowIsMeasuredAgainstTheIndexTime sets a file's mtime and its
// record's index time, then revalidates.
func TestTheRacyWindowIsMeasuredAgainstTheIndexTime(t *testing.T) {
	setRecord := func(ft *fileTools, path string, mtimeBeforeIndex, indexedAgo time.Duration) time.Time {
		indexed := time.Now().Add(-indexedAgo)
		mtime := indexed.Add(-mtimeBeforeIndex)
		_ = os.Chtimes(path, mtime, mtime)
		fi, _ := os.Stat(path)
		ft.table.lockBlocking()
		defer ft.table.unlock()
		e := ft.table.entries["racy.go"]
		e.indexedAt, e.mtime, e.size = indexed, fi.ModTime(), fi.Size()
		return indexed
	}
	t.Run("a same-size edit is seen by a revalidation more than 2s later", func(t *testing.T) {
		ws := newWS(t, map[string]string{"racy.go": "package main\n\nfunc RacyOld() {}\n"})
		ft := newFT(ws, Options{})
		mustOK(t, call(ft.findSymbolTool(), `{"name":"x"}`))
		recorded := setRecord(ft, abs(ws, "racy.go"), 500*time.Millisecond, 5*time.Second)
		fi, _ := os.Stat(abs(ws, "racy.go"))
		writeAt(t, ws.Root(), "racy.go", "package main\n\nfunc RacyNew() {}\n")
		_ = os.Chtimes(abs(ws, "racy.go"), fi.ModTime(), fi.ModTime())
		ft.table.markRevalidateAll()
		if len(findNames(t, ft, "RacyNew")) != 1 || len(findNames(t, ft, "RacyOld")) != 0 || !indexedAt(ft, "racy.go").After(recorded) {
			t.Fatal("the racy file was not re-outlined")
		}
		// Re-outlined now, long after its mtime, it stops being racy.
		first := indexedAt(ft, "racy.go")
		testutil.Eventually(t, "the clock to move", func() bool { return time.Now().After(first) })
		ft.table.markRevalidateAll()
		_ = findNames(t, ft, "RacyNew")
		if !indexedAt(ft, "racy.go").Equal(first) {
			t.Fatal("the file was re-outlined again")
		}
	})
	t.Run("an old mtime is not racy", func(t *testing.T) {
		ws := newWS(t, map[string]string{"racy.go": "package main\n\nfunc Racy() {}\n"})
		ft := newFT(ws, Options{})
		mustOK(t, call(ft.findSymbolTool(), `{"name":"x"}`))
		before := setRecord(ft, abs(ws, "racy.go"), time.Hour, time.Second)
		ft.table.markRevalidateAll()
		_ = findNames(t, ft, "Racy")
		if !indexedAt(ft, "racy.go").Equal(before) {
			t.Fatal("a file with an old mtime was re-outlined")
		}
	})
}

func TestTheWholeTableMarkIsClearedOnlyByACompleteFullPass(t *testing.T) {
	files := map[string]string{"pkg/a.go": "package pkg\n\nfunc A() {}\n", "other/c.go": "package other\n\nfunc C() {}\n"}
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("extra/f%03d.go", i)] = "package extra\n"
	}
	ws := newWS(t, files)
	ft := newFT(ws, Options{})
	fs := ft.findSymbolTool()
	mustOK(t, call(fs, `{"name":"A"}`))
	stillMarked := func(what string) {
		t.Helper()
		if _, all := marks(ft.table); !all {
			t.Fatalf("the whole-table mark was cleared by %s", what)
		}
	}
	ft.table.markRevalidateAll()
	mustOK(t, callJSON(t, fs, map[string]any{"name": "A", "path": abs(ws, "pkg")}))
	stillMarked("a scoped pass")
	ft.symOpts.MaxFiles = 3
	if _, partial, _ := indexed(t, call(fs, `{"name":"A"}`)); !partial {
		t.Fatal("the bounded pass is not partial")
	}
	stillMarked("a bounded pass")
	ft.symOpts.MaxFiles = 0
	mustOK(t, call(fs, `{"name":"A"}`))
	if _, all := marks(ft.table); all {
		t.Fatal("a complete full pass left the mark set")
	}
	walks := countWalks(t)
	mustOK(t, call(fs, `{"name":"A"}`))
	if walks.Load() != 0 {
		t.Fatal("a cleared table walked again")
	}
}

func TestAMarkMadeDuringABuildIsNotLost(t *testing.T) {
	ws := newWS(t, map[string]string{"a.go": "package main\n\nfunc InitialA() {}\n"})
	ft := newFT(ws, Options{Symbols: SymbolOptions{MaxDuration: time.Minute}})
	started, release := holdBuild(ft)
	done := make(chan core.ToolResult, 1)
	go func() { done <- call(ft.findSymbolTool(), `{"name":"InitialA"}`) }()
	<-started
	// Marking only sets a flag: it does not wait for the build.
	mustOK(t, call(ft.writeFile(), `{"path":"c.go","content":"package main\n\nfunc WrittenDuringBuild() {}\n"}`))
	mustOK(t, call(ft.editFile(), `{"path":"a.go","edits":[{"old_string":"InitialA","new_string":"InitialA2"}]}`))
	ft.markTableRevalidateAll()
	release()
	mustOK(t, <-done)
	if dirty, all := marks(ft.table); !all || !dirty["c.go"] {
		t.Fatalf("the marks made during the build were lost: %v %v", dirty, all)
	}
	if got := findNames(t, ft, "WrittenDuringBuild"); len(got) != 1 {
		t.Fatalf("the next call did not apply the mark: %v", got)
	}
}

func TestACallWaitingOnABuildIsAbandonedWhenCancelled(t *testing.T) {
	ft := newFT(newWS(t, goFiles(5, ".py")), Options{Symbols: SymbolOptions{MaxDuration: time.Minute}})
	fs := ft.findSymbolTool()
	started, release := holdBuild(ft)
	defer release()
	first := make(chan core.ToolResult, 1)
	go func() { first <- call(fs, `{"name":"func"}`) }()
	<-started
	base, cancel := context.WithCancel(context.Background())
	ctx := &watchedCtx{Context: base, watched: make(chan struct{})}
	second := make(chan core.ToolResult, 1)
	go func() { second <- fs.Execute(ctx, []byte(`{"name":"func"}`)) }()
	<-ctx.watched // the second call is waiting on the table's lock
	cancel()
	if r := <-second; r.Error != "aborted" || r.Detail != "Operation aborted" {
		t.Fatalf("the waiting call: %+v", r)
	}
	select {
	case r := <-first:
		t.Fatalf("the build finished while it was held: %+v", r)
	default:
	}
	release()
	mustOK(t, <-first)
}

func TestACancellationDuringABuildIsAnAbort(t *testing.T) {
	ft := newFT(newWS(t, goFiles(5, ".py")), Options{Symbols: SymbolOptions{MaxDuration: time.Minute}})
	started, release := holdBuild(ft)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan core.ToolResult, 1)
	go func() { done <- ft.findSymbolTool().Execute(ctx, []byte(`{"name":"func"}`)) }()
	<-started
	cancel()
	if r := <-done; r.Error != "aborted" || r.Data != nil {
		t.Fatalf("%+v", r)
	}
	if !ft.table.needsRefresh() {
		t.Fatal("a cancelled build left the table complete")
	}
}

func TestConcurrentCallsAndWritesAgree(t *testing.T) {
	ft := newFT(newWS(t, goFiles(10, ".go")), Options{})
	fs, write, edit := ft.findSymbolTool(), ft.writeFile(), ft.editFile()
	mustOK(t, call(fs, `{"name":"F"}`))
	var wg sync.WaitGroup
	errs := make(chan string, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r core.ToolResult
			switch i % 3 {
			case 0:
				r = call(fs, `{"name":"F"}`)
			case 1:
				r = callJSON(t, write, map[string]any{"path": fmt.Sprintf("w%d.go", i), "content": fmt.Sprintf("package main\n\nfunc W%d() {}\n", i)})
			case 2:
				r = callJSON(t, edit, map[string]any{"path": fmt.Sprintf("f%03d.go", i%10), "edits": []Edit{{OldString: "{}", NewString: "{ }"}}})
			}
			if !r.OK && r.Error != "edit_not_found" {
				errs <- r.Error + ": " + r.Detail
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if got := symbolNames(mustOK(t, call(fs, `{"name":"W","max_results":50}`))); len(got) != 10 {
		t.Fatalf("after the writes: %v", got)
	}
}

// TestTheToolSetKeepsFindSymbolFresh runs the tools as a model would: edits,
// new files, a .gitignore and a shell command are all seen by the next call.
func TestTheToolSetKeepsFindSymbolFresh(t *testing.T) {
	needPOSIX(t)
	ws := newWS(t, map[string]string{"main.go": "package main\n\nfunc OldFunc() {}\n", "victim.go": "package main\n\nfunc Victim() {}\n"})
	ts := toolSet(t, ws, Options{})
	find := func(name string) []string {
		t.Helper()
		return symbolNames(mustOK(t, callJSON(t, ts["find_symbol"], map[string]any{"name": name, "exact": true})))
	}
	if len(find("Victim")) != 1 {
		t.Fatal("Victim is not found at first")
	}
	mustOK(t, call(ts["edit_file"], `{"path":"main.go","edits":[{"old_string":"func OldFunc() {}","new_string":"func OldFunc() {}\n\nfunc NewThing() {}"}]}`))
	mustOK(t, call(ts["write_file"], `{"path":"fresh.go","content":"package main\n\nfunc Fresh() {}\n"}`))
	mustOK(t, call(ts["write_file"], `{"path":".gitignore","content":"fresh.go\n"}`))
	mustOK(t, call(ts["execute"], `{"command":"rm victim.go"}`))
	for name, want := range map[string]int{"NewThing": 1, "OldFunc": 1, "Fresh": 0, "Victim": 0} {
		if got := find(name); len(got) != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// watchedCtx signals the first time something waits on it.
type watchedCtx struct {
	context.Context
	watched chan struct{}
	once    sync.Once
}

func (w *watchedCtx) Done() <-chan struct{} {
	w.once.Do(func() { close(w.watched) })
	return w.Context.Done()
}
