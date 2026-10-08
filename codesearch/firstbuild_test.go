//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// blockedFirstBuild returns an index whose first build is parked inside the
// outline step, with the code_search call that started it still running. The
// returned release function lets the build finish; it is idempotent and also
// runs at cleanup, before the index is closed.
func blockedFirstBuild(t *testing.T) (idx *Index, tool core.Tool, first <-chan core.ToolResult, release func()) {
	t.Helper()

	root := t.TempDir()
	for i := 0; i < 10; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// searchword\nfunc F%d() {}\n", i))
	}
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err = newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 1)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	idx.testOutlineHook = func(_ string, _ int) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
	}
	t.Cleanup(func() {
		release()
		idx.Close()
	})

	tool = idx.Tools()[0]
	done := make(chan core.ToolResult, 1)
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		done <- tool.Execute(context.Background(), in)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first build did not start")
	}
	return idx, tool, done, release
}

// 03-REQ-8.2: a call waiting behind the first build abandons the wait when its
// context ends and returns aborted without waiting for the build to finish.
func TestCancelledWaiterBehindFirstBuildAborts(t *testing.T) {
	_, tool, first, release := blockedFirstBuild(t)

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan core.ToolResult, 1)
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		second <- tool.Execute(ctx, in)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case r := <-second:
		if r.OK || r.Error != "aborted" {
			t.Errorf("cancelled waiter = ok:%v error:%q, want aborted", r.OK, r.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled call stayed blocked behind the first build")
	}

	// The build the waiter left behind is untouched and still completes.
	select {
	case <-first:
		t.Fatal("first call returned while its build was still blocked")
	default:
	}
	release()
	select {
	case r := <-first:
		if !r.OK {
			t.Errorf("first call after release: %s: %s", r.Error, r.Detail)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first call did not return after the build was released")
	}
}

// 03-REQ-7.3 and Design Decision 7: Symbols starts no build and never waits
// for one; during the first build it answers ok=false at once so find_symbol
// uses its own table within its time bound.
func TestSymbolsDoesNotWaitBehindFirstBuild(t *testing.T) {
	idx, _, _, _ := blockedFirstBuild(t)

	type answer struct {
		ok  bool
		err error
	}
	got := make(chan answer, 1)
	go func() {
		_, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "F"})
		got <- answer{ok, err}
	}()

	select {
	case a := <-got:
		if a.ok || a.err != nil {
			t.Errorf("Symbols during the first build = ok:%v err:%v, want ok=false, nil", a.ok, a.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Symbols stayed blocked behind the first build")
	}
	if n := idx.BuildCount(); n != 0 {
		t.Errorf("BuildCount = %d while the first build is running, want 0", n)
	}
}

// The read-only accessors are not held up by the first build either.
func TestAccessorsDoNotWaitBehindFirstBuild(t *testing.T) {
	idx, _, _, _ := blockedFirstBuild(t)

	done := make(chan struct{})
	go func() {
		idx.BuildStats()
		idx.IndexedFiles()
		idx.RunDir()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("an accessor stayed blocked behind the first build")
	}
}

// A caller that does not give up waits for the build in progress and uses it:
// two concurrent first calls produce one build.
func TestWaiterSharesTheFirstBuild(t *testing.T) {
	idx, tool, first, release := blockedFirstBuild(t)

	second := make(chan core.ToolResult, 1)
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		second <- tool.Execute(context.Background(), in)
	}()
	time.Sleep(100 * time.Millisecond)
	release()

	for name, ch := range map[string]<-chan core.ToolResult{"first": first, "second": second} {
		select {
		case r := <-ch:
			if !r.OK {
				t.Errorf("%s call: %s: %s", name, r.Error, r.Detail)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s call did not return", name)
		}
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1 shared build", n)
	}
}

// When the build a caller waited for is cancelled, the waiter is free to build
// again instead of inheriting the failure.
func TestWaiterBuildsAgainAfterTheBuilderIsCancelled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// searchword\nfunc F%d() {}\n", i))
	}
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	started := make(chan struct{}, 1)
	gate := make(chan struct{})
	idx.testOutlineHook = func(_ string, _ int) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate
	}
	tool := idx.Tools()[0]

	builderCtx, cancelBuilder := context.WithCancel(context.Background())
	builder := make(chan core.ToolResult, 1)
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		builder <- tool.Execute(builderCtx, in)
	}()
	<-started

	waiter := make(chan core.ToolResult, 1)
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		waiter <- tool.Execute(context.Background(), in)
	}()
	time.Sleep(100 * time.Millisecond)

	cancelBuilder()
	close(gate)

	if r := <-builder; r.OK || r.Error != "aborted" {
		t.Errorf("cancelled builder = ok:%v error:%q, want aborted", r.OK, r.Error)
	}
	select {
	case r := <-waiter:
		if !r.OK {
			t.Errorf("waiter after a cancelled build: %s: %s", r.Error, r.Detail)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiter did not build after the builder was cancelled")
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1 (the waiter's own build)", n)
	}
}

// Close waits for a build in progress, so nothing writes into the run
// directory after Close returns, and the directory does not outlive it.
func TestCloseWaitsForTheBuildAndRemovesItsRunDir(t *testing.T) {
	idx, _, first, release := blockedFirstBuild(t)
	runDir := idx.runDirPath()

	closed := make(chan error, 1)
	go func() { closed <- idx.Close() }()

	select {
	case <-closed:
		t.Fatal("Close returned while a build was still running")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after the build finished")
	}
	<-first

	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("run directory %s still exists after Close (stat err: %v)", runDir, err)
	}
}
