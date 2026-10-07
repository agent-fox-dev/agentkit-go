//go:build !windows

package codesearch

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"
	"github.com/sourcegraph/zoekt/query"
)

// These pin issue #90: the codesearch build, its skip accounting, and the
// index's lifecycle under rebuilds and dirty files.

func newTestIndex(t *testing.T, root string, o Options) *Index {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if o.TempDir == "" {
		o.TempDir = t.TempDir()
	}
	o.Ignore = tools.NoGlobalExcludes()
	idx, err := newIndex(ws, o)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func codeSearch(t *testing.T, idx *Index, args map[string]any) core.ToolResult {
	t.Helper()
	in, _ := json.Marshal(args)
	return idx.Tools()[0].Execute(context.Background(), in)
}

// endTagRunner is a fake ctags that, like universal-ctags with
// --fields=+neKS, reports an `end` for every tag: a class spanning lines 1-6
// with a method on lines 2-3 inside it.
func endTagRunner(_ context.Context, args []string) ([]byte, error) {
	var out strings.Builder
	for _, p := range args {
		if !strings.HasSuffix(p, ".py") {
			continue
		}
		fmt.Fprintf(&out, `{"_type":"tag","name":"Loader","path":%q,"line":1,"end":6,"kind":"class"}`+"\n", p)
		fmt.Fprintf(&out, `{"_type":"tag","name":"load","path":%q,"line":2,"end":3,"kind":"member","scope":"Loader","scopeKind":"class"}`+"\n", p)
		fmt.Fprintf(&out, `{"_type":"tag","name":"save","path":%q,"line":5,"end":6,"kind":"member","scope":"Loader","scopeKind":"class"}`+"\n", p)
	}
	return []byte(out.String()), nil
}

// TestNestedDeclarationsDoNotFailTheBuild: a class and the methods inside it,
// and two names declared by one Go spec, gave zoekt overlapping sections and
// failed the whole build with "sections overlap" — permanently, since a
// failed build is retried on every call.
func TestNestedDeclarationsDoNotFailTheBuild(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "loader.py",
		"class Loader:\n    def load(self):\n        return 1\n\n    def save(self):\n        return 2\n")
	mkFile(t, root, "vars.go", "package p\n\nvar alpha, beta = 1, 2\n\nconst (\n\tX, Y = 3, 4\n)\n")

	idx := newTestIndex(t, root, Options{Runner: endTagRunner})
	defer idx.Close()
	if err := idx.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, q := range []string{"sym:load", "sym:Loader", "sym:beta", "sym:Y"} {
		r := codeSearch(t, idx, map[string]any{"query": q})
		if !r.OK || countResultFiles(r) != 1 {
			t.Errorf("%s: ok=%v files=%v (%s %s)", q, r.OK, getResultFilePaths(r), r.Error, r.Detail)
		}
	}
}

// TestSymbolSectionsAreTheIdentifier: a section is the declared name on its
// line — what sym: matches against — and sections never overlap.
func TestSymbolSectionsAreTheIdentifier(t *testing.T) {
	content := []byte("var alpha, beta = 1, 2\nclass Loader:\n    def load(self):\n")
	decls := []outline.Decl{
		{Kind: outline.KindVar, Name: "alpha", StartLine: 1, EndLine: 1},
		{Kind: outline.KindVar, Name: "beta", StartLine: 1, EndLine: 1},
		{Kind: outline.KindClass, Name: "Loader", StartLine: 2, EndLine: 3},
		{Kind: outline.KindMethod, Name: "load", Container: "Loader", StartLine: 3, EndLine: 3},
		{Kind: outline.KindVar, Name: "alpha", StartLine: 1, EndLine: 1}, // a duplicate
	}
	sections, meta := declsToSymbols(content, decls)
	if len(sections) != len(meta) {
		t.Fatalf("%d sections, %d metadata", len(sections), len(meta))
	}
	var got []string
	for i, s := range sections {
		if i > 0 && sections[i-1].End > s.Start {
			t.Fatalf("sections %d and %d overlap: %+v", i-1, i, sections)
		}
		got = append(got, string(content[s.Start:s.End]))
	}
	if want := []string{"alpha", "beta", "Loader", "load"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sections cover %q, want %q", got, want)
	}
}

// lockfile is package-lock-style JSON under 1 MiB with far more distinct
// trigrams than zoekt's 20 000, so zoekt stores it as "not indexed".
func lockfile() string {
	var b strings.Builder
	b.WriteString("{\n")
	for i := 0; i < 1500; i++ {
		sum := sha512.Sum512([]byte(fmt.Sprint(i)))
		fmt.Fprintf(&b, "  \"pkg%d\": {\"integrity\": \"sha512-%s\"},\n", i, base64.StdEncoding.EncodeToString(sum[:]))
	}
	b.WriteString("  \"left-pad\": {}\n}\n")
	return b.String()
}

// TestFilesZoektSkipsAreNotCountedAsIndexed: a file zoekt will not index is
// skipped with its reason, and is not in the indexed set the model is told
// about.
func TestFilesZoektSkipsAreNotCountedAsIndexed(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "package-lock.json", lockfile())
	// A NUL past the 8 KiB sniff: zoekt checks the whole file.
	mkFile(t, root, "late.bin", strings.Repeat("left-pad text\n", 1000)+"\x00\n")
	mkFile(t, root, "index.js", "require('left-pad')\n")

	idx := newTestIndex(t, root, Options{DisableCtags: true})
	defer idx.Close()
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	files := idx.IndexedFiles()
	for _, p := range []string{"package-lock.json", "late.bin"} {
		if files[p] {
			t.Errorf("%s is in the indexed set, but zoekt does not index it", p)
		}
	}
	st := idx.BuildStats()
	if st.FilesIndexed != 1 || st.TooManyTrigramsSkipped != 1 || st.BinarySkipped != 1 {
		t.Fatalf("stats = %+v, want 1 indexed, 1 too_many_trigrams, 1 binary", st)
	}
	r := codeSearch(t, idx, map[string]any{"query": "left-pad"})
	skipped, _ := r.Data["skipped"].(map[string]any)
	if skipped["too_many_trigrams"] != 1 {
		t.Fatalf("skipped = %v, want too_many_trigrams: 1", r.Data["skipped"])
	}
}

// TestCloseWaitsForARebuild: a rebuild started by a query is waited for, the
// directory it built is removed, and the per-workspace directory with it.
func TestCloseWaitsForARebuild(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		mkFile(t, root, fmt.Sprintf("f%02d.go", i), fmt.Sprintf("package p\n// word %d\n", i))
	}
	idx := newTestIndex(t, root, Options{DisableCtags: true})
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	idx.testOutlineHook = func(string, int, bool) {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	mkFile(t, root, "f00.go", "package p\n// changed 0\n")
	mkFile(t, root, "f01.go", "package p\n// changed 1\n")
	idx.Invalidate("f00.go")
	idx.Invalidate("f01.go") // 10% dirty: the next query rebuilds

	queryDone := make(chan core.ToolResult, 1)
	go func() { queryDone <- codeSearch(t, idx, map[string]any{"query": "word"}) }()
	<-entered

	closed := make(chan struct{})
	go func() { _ = idx.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a rebuild was in progress")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-closed
	<-queryDone

	if _, err := os.Stat(idx.hashDirPath()); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(idx.hashDirPath())
		t.Fatalf("the workspace's shard directory survived Close: %v %v", err, entries)
	}
}

// TestConcurrentDirtyQueriesBuildOneOverlay: one dirty set, one overlay.
func TestConcurrentDirtyQueriesBuildOneOverlay(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		mkFile(t, root, fmt.Sprintf("f%02d.go", i), fmt.Sprintf("package p\n// word %d\n", i))
	}
	idx := newTestIndex(t, root, Options{DisableCtags: true})
	defer idx.Close()
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	mkFile(t, root, "f00.go", "package p\n// word fresh\n")
	idx.Invalidate("f00.go")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := codeSearch(t, idx, map[string]any{"query": "fresh"}); !r.OK || countResultFiles(r) != 1 {
				t.Errorf("query: ok=%v files=%v %s", r.OK, getResultFilePaths(r), r.Detail)
			}
		}()
	}
	wg.Wait()
	if n := idx.OverlayBuildCount(); n != 1 {
		t.Fatalf("%d overlay builds for one dirty set, want 1", n)
	}
}

// TestADirtyTopResultKeepsThePageFull: dropping a dirty file from the
// indexed hits must not shrink the page or lose the cap marker.
func TestADirtyTopResultKeepsThePageFull(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		mkFile(t, root, fmt.Sprintf("f%02d.go", i), "package p\n// needle\n")
	}
	idx := newTestIndex(t, root, Options{DisableCtags: true})
	defer idx.Close()
	r := codeSearch(t, idx, map[string]any{"query": "needle", "max_files": 10})
	paths := getResultFilePaths(r)
	if len(paths) != 10 || r.Data["truncated"] != true {
		t.Fatalf("before: %d files, truncated=%v", len(paths), r.Data["truncated"])
	}
	mkFile(t, root, paths[0], "package p\n// no longer\n")
	idx.Invalidate(paths[0])

	r = codeSearch(t, idx, map[string]any{"query": "needle", "max_files": 10})
	if got := getResultFilePaths(r); len(got) != 10 || r.Data["truncated"] != true {
		t.Fatalf("after editing a top result: %d files, truncated=%v, want 10 and true (29 still match)",
			len(got), r.Data["truncated"])
	}
	if !strings.Contains(r.Text, "max_files") {
		t.Fatalf("the cap marker is missing:\n%s", r.Text)
	}
}

// TestAWalkDeadlineKeepsWhatItWalked: the wall-time bound firing during the
// walk keeps the files already walked (spec 03 §4) instead of indexing none.
func TestAWalkDeadlineKeepsWhatItWalked(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 4000; i++ {
		mkFile(t, root, fmt.Sprintf("d%02d/f%04d.txt", i%40, i), fmt.Sprintf("word %d\n", i))
	}
	idx := newTestIndex(t, root, Options{DisableCtags: true, MaxBuildTime: 15 * time.Millisecond})
	defer idx.Close()
	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx.mu.RLock()
	partial, reason := idx.partial, idx.partialReason
	idx.mu.RUnlock()
	if !partial || reason != "time" {
		t.Skipf("the walk finished inside the bound on this machine (partial=%v %q)", partial, reason)
	}
	if n := idx.BuildStats().FilesIndexed; n == 0 {
		t.Fatal("a walk cut by the deadline indexed nothing; the files it walked were discarded")
	}
}

// TestThePathConstraintIsCaseSensitive: the path was validated exactly, so
// `src` must not also match `Src/`.
func TestThePathConstraintIsCaseSensitive(t *testing.T) {
	for _, c := range []struct {
		rel   string
		isDir bool
	}{{"src", true}, {"src/a.txt", false}} {
		q, err := pathConstraint(c.rel, c.isDir)
		if err != nil {
			t.Fatal(err)
		}
		re, ok := q.(*query.Regexp)
		if !ok || !re.CaseSensitive || !re.FileName || re.Content {
			t.Fatalf("pathConstraint(%q) = %#v, want a case-sensitive file-name regexp", c.rel, q)
		}
	}
}
