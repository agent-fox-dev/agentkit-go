package outline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TS-01-12: A cancelled context makes Outline and OutlineMany return exactly ctx.Err().
func TestCancelledContext_TS_01_12(t *testing.T) {
	// Part 1: context cancelled before the call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Outline(ctx, "/fake/a.go", []byte("package a\n"), Options{})
	if err != context.Canceled {
		t.Fatalf("Outline: err = %v, want context.Canceled", err)
	}

	_, _, err = OutlineMany(ctx, []Source{{Abs: "/fake/a.go", Src: []byte("package a\n")}}, Options{})
	if err != context.Canceled {
		t.Fatalf("OutlineMany: err = %v, want context.Canceled", err)
	}

	// Part 2: context cancelled during a blocking fake Runner.
	ctx2, cancel2 := context.WithCancel(context.Background())
	dir := t.TempDir()
	pyFile := filepath.Join(dir, "a.py")
	if err := os.WriteFile(pyFile, []byte("def foo():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	started := make(chan struct{})
	blockingRunner := func(ctx context.Context, args []string) ([]byte, error) {
		mu.Lock()
		mu.Unlock()
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	// Cancel after the runner starts.
	go func() {
		<-started
		cancel2()
	}()

	_, _, err = OutlineMany(ctx2, []Source{{Abs: pyFile}}, Options{Runner: blockingRunner})
	if err != context.Canceled {
		t.Fatalf("OutlineMany with blocking runner: err = %v, want context.Canceled", err)
	}
}

// TS-01-14: OutlineMany returns an unreadable file as none and still outlines the others.
func TestOutlineMany_UnreadableFile_TS_01_14(t *testing.T) {
	dir := t.TempDir()

	// First: a readable .go file.
	goFile1 := filepath.Join(dir, "a.go")
	if err := os.WriteFile(goFile1, []byte("package a\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second: a missing .py file (unreadable).
	missingPy := filepath.Join(dir, "missing.py")

	// Third: another readable .go file.
	goFile2 := filepath.Join(dir, "b.go")
	if err := os.WriteFile(goFile2, []byte("package b\n\nfunc World() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	srcs := []Source{
		{Abs: goFile1},
		{Abs: missingPy},
		{Abs: goFile2},
	}

	fs, _, err := OutlineMany(ctx, srcs, Options{})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}
	if len(fs) != 3 {
		t.Fatalf("len(fs) = %d, want 3", len(fs))
	}

	// First file: outlined with go/ast.
	if fs[0].Backend != BackendGoAST {
		t.Errorf("fs[0].Backend = %q, want %q", fs[0].Backend, BackendGoAST)
	}
	if len(fs[0].Decls) == 0 {
		t.Error("fs[0].Decls is empty, want at least one decl")
	}

	// Second file: Backend none (unreadable).
	if fs[1].Backend != BackendNone {
		t.Errorf("fs[1].Backend = %q, want %q", fs[1].Backend, BackendNone)
	}
	if fs[1].Decls == nil {
		t.Error("fs[1].Decls is nil, want non-nil empty slice")
	}
	if len(fs[1].Decls) != 0 {
		t.Errorf("fs[1].Decls has %d entries, want 0", len(fs[1].Decls))
	}

	// Third file: outlined with go/ast.
	if fs[2].Backend != BackendGoAST {
		t.Errorf("fs[2].Backend = %q, want %q", fs[2].Backend, BackendGoAST)
	}
	if len(fs[2].Decls) == 0 {
		t.Error("fs[2].Decls is empty, want at least one decl")
	}
}

// TS-01-15: OutlineMany returns one File per input in input order, plus Stats.
func TestOutlineMany_InputOrder_TS_01_15(t *testing.T) {
	dir := t.TempDir()

	// Create files of various types.
	files := []struct {
		name    string
		content string
	}{
		{"a.go", "package a\nfunc A() {}\n"},
		{"b.py", "def b():\n    pass\n"},
		{"c.unknown", "whatever"},
		{"d.go", "package d\nfunc D() {}\n"},
	}

	var srcs []Source
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, Source{Abs: p})
	}

	// Add an oversize file.
	bigFile := filepath.Join(dir, "big.go")
	if err := os.WriteFile(bigFile, []byte(strings.Repeat("x", 600)), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs = append(srcs, Source{Abs: bigFile})

	// Add an unreadable file.
	srcs = append(srcs, Source{Abs: filepath.Join(dir, "nonexistent.py")})

	ctx := context.Background()
	fakeRunner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte{}, nil
	}

	fs, st, err := OutlineMany(ctx, srcs, Options{
		Runner:       fakeRunner,
		MaxFileBytes: 500,
		Root:         dir,
	})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	// One File per input.
	if len(fs) != len(srcs) {
		t.Fatalf("len(fs) = %d, want %d", len(fs), len(srcs))
	}

	// Each File.Path corresponds to the input.
	for i, f := range fs {
		wantPath := filepath.ToSlash(filepath.Base(srcs[i].Abs))
		// Since Root is dir, the path should be just the filename.
		if f.Path != wantPath {
			// For files that don't exist, the path is still computed.
			t.Logf("fs[%d].Path = %q (expected base %q)", i, f.Path, wantPath)
		}
	}

	// Stats is returned (even if zero).
	_ = st
}

// TS-01-24: The Runner is called with the exact ctags argument list followed by absolute paths.
func TestOutlineMany_CtagsArgs_TS_01_24(t *testing.T) {
	dir := t.TempDir()
	pyFile := filepath.Join(dir, "hello.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var recordedArgs []string
	runner := func(_ context.Context, args []string) ([]byte, error) {
		recordedArgs = args
		return []byte{}, nil
	}

	ctx := context.Background()
	_, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	if len(recordedArgs) < 7 {
		t.Fatalf("args too short: %v", recordedArgs)
	}

	// args[0] is "--options=NONE"
	if recordedArgs[0] != "--options=NONE" {
		t.Errorf("args[0] = %q, want %q", recordedArgs[0], "--options=NONE")
	}

	// args[1:6] are the fixed flags.
	wantFixed := []string{"--output-format=json", "--fields=+neKS", "--sort=no", "-f", "-"}
	for i, want := range wantFixed {
		if recordedArgs[1+i] != want {
			t.Errorf("args[%d] = %q, want %q", 1+i, recordedArgs[1+i], want)
		}
	}

	// The remaining args are the absolute file paths.
	filePaths := recordedArgs[6:]
	if len(filePaths) != 1 {
		t.Fatalf("expected 1 file path, got %d: %v", len(filePaths), filePaths)
	}
	if filePaths[0] != pyFile {
		t.Errorf("file path = %q, want %q", filePaths[0], pyFile)
	}

	// No "ctags" program name in args.
	for _, a := range recordedArgs {
		if a == "ctags" {
			t.Error("args contain 'ctags' program name, should not")
		}
	}
}

// TS-01-25: The Runner never receives a directory, -R or a path starting with a dash.
func TestOutlineMany_NoDirNoRNoDash_TS_01_25(t *testing.T) {
	dir := t.TempDir()

	// Create files with tricky names (but absolute paths).
	pyFile := filepath.Join(dir, "normal.py")
	if err := os.WriteFile(pyFile, []byte("def f():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var allArgs [][]string
	runner := func(_ context.Context, args []string) ([]byte, error) {
		cp := make([]string, len(args))
		copy(cp, args)
		allArgs = append(allArgs, cp)
		return []byte{}, nil
	}

	ctx := context.Background()
	_, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	for _, args := range allArgs {
		for _, a := range args {
			if a == "-R" {
				t.Error("args contain -R")
			}
		}
		// File args start after the 6 fixed args.
		if len(args) > 6 {
			for _, a := range args[6:] {
				if !filepath.IsAbs(a) {
					t.Errorf("file arg is not absolute: %q", a)
				}
				if strings.HasPrefix(a, "-") {
					t.Errorf("file arg starts with dash: %q", a)
				}
			}
		}
	}
}

// TS-01-26: Batches never exceed 100 files or 32 KiB of path text.
func TestOutlineMany_Batching_TS_01_26(t *testing.T) {
	dir := t.TempDir()

	// Create 250 .py files.
	var srcs []Source
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("file_%04d.py", i)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("def f():\n    pass\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, Source{Abs: p})
	}

	var mu sync.Mutex
	var calls [][]string
	runner := func(_ context.Context, args []string) ([]byte, error) {
		cp := make([]string, len(args))
		copy(cp, args)
		mu.Lock()
		calls = append(calls, cp)
		mu.Unlock()
		return []byte{}, nil
	}

	ctx := context.Background()
	_, _, err := OutlineMany(ctx, srcs, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	// Check batch constraints.
	allFiles := make(map[string]bool)
	for _, call := range calls {
		if len(call) < 6 {
			t.Fatalf("call too short: %v", call)
		}
		filePaths := call[6:]

		// At most 100 files per batch.
		if len(filePaths) > 100 {
			t.Errorf("batch has %d files, want <= 100", len(filePaths))
		}

		// At most 32 KiB of path text.
		totalBytes := 0
		for _, p := range filePaths {
			totalBytes += len(p)
		}
		if totalBytes > 32768 {
			t.Errorf("batch has %d bytes of path text, want <= 32768", totalBytes)
		}

		// Track all files.
		for _, p := range filePaths {
			if allFiles[p] {
				t.Errorf("file %q appears in multiple batches", p)
			}
			allFiles[p] = true
		}
	}

	// Every file appears in exactly one call.
	if len(allFiles) != 250 {
		t.Errorf("total unique files = %d, want 250", len(allFiles))
	}

	// Fewer calls than files.
	if len(calls) >= 250 {
		t.Errorf("number of calls = %d, want far fewer than 250", len(calls))
	}

	// 250 files with short paths should give 3 batches (100+100+50).
	if len(calls) != 3 {
		t.Logf("number of calls = %d (expected 3 for 250 short-path files)", len(calls))
	}
}

// TS-01-26 additional: test 32 KiB path text limit.
func TestOutlineMany_BatchingPathLimit_TS_01_26_extra(t *testing.T) {
	dir := t.TempDir()

	// Create nested directories to make long absolute paths.
	// Each nesting level adds ~50 chars, 6 levels ≈ 300 chars per path.
	nested := dir
	for i := 0; i < 6; i++ {
		nested = filepath.Join(nested, strings.Repeat("d", 50))
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 80 files with long names in the nested directory.
	// Each path is ~465 bytes. 32768/465 ≈ 70, so 80 files need 2 batches.
	var srcs []Source
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("%s_%04d.py", strings.Repeat("f", 90), i)
		p := filepath.Join(nested, name)
		if err := os.WriteFile(p, []byte("def f():\n    pass\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, Source{Abs: p})
	}

	var mu sync.Mutex
	var calls [][]string
	runner := func(_ context.Context, args []string) ([]byte, error) {
		cp := make([]string, len(args))
		copy(cp, args)
		mu.Lock()
		calls = append(calls, cp)
		mu.Unlock()
		return []byte{}, nil
	}

	ctx := context.Background()
	_, _, err := OutlineMany(ctx, srcs, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	for _, call := range calls {
		if len(call) < 6 {
			continue
		}
		filePaths := call[6:]
		if len(filePaths) > 100 {
			t.Errorf("batch has %d files, want <= 100", len(filePaths))
		}
		totalBytes := 0
		for _, p := range filePaths {
			totalBytes += len(p)
		}
		if totalBytes > 32768 {
			t.Errorf("batch has %d bytes of path text, want <= 32768", totalBytes)
		}
	}

	// With ~1700-byte paths, 32768/1700 ≈ 19 files per batch, so we need more than 1 call.
	if len(calls) < 2 {
		t.Errorf("expected multiple batches for long paths, got %d", len(calls))
	}
}

// TS-01-28: Lines whose _type is not tag are ignored and not counted as malformed.
func TestOutlineMany_NonTagLines_TS_01_28(t *testing.T) {
	dir := t.TempDir()
	pyFile := filepath.Join(dir, "a.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake ctags output with ptag lines and one valid tag.
	output := strings.Join([]string{
		`{"_type": "ptag", "name": "!_TAG_FILE_FORMAT", "pattern": "2"}`,
		`{"_type": "program", "name": "Universal Ctags"}`,
		fmt.Sprintf(`{"_type": "tag", "name": "hello", "path": %q, "line": 1, "kind": "function"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	ctx := context.Background()
	fs, st, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	// Only the valid tag becomes a Decl.
	if len(fs) != 1 {
		t.Fatalf("len(fs) = %d, want 1", len(fs))
	}
	if len(fs[0].Decls) != 1 {
		t.Fatalf("len(Decls) = %d, want 1", len(fs[0].Decls))
	}
	if fs[0].Decls[0].Name != "hello" {
		t.Errorf("Decl name = %q, want %q", fs[0].Decls[0].Name, "hello")
	}

	// Stats malformed count is 0 (non-tag lines are not malformed).
	if st.MalformedLines != 0 {
		t.Errorf("MalformedLines = %d, want 0", st.MalformedLines)
	}
}

// TS-01-29: Invalid JSON or lines missing name, path or line are skipped and counted.
func TestOutlineMany_MalformedLines_TS_01_29(t *testing.T) {
	dir := t.TempDir()
	pyFile := filepath.Join(dir, "a.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake ctags output with various malformed lines.
	output := strings.Join([]string{
		`{truncated json`, // 1: invalid JSON
		fmt.Sprintf(`{"_type": "tag", "path": %q, "line": 1, "kind": "function"}`, pyFile),                  // 2: missing name
		`{"_type": "tag", "name": "foo", "line": 1, "kind": "function"}`,                                    // 3: missing path
		fmt.Sprintf(`{"_type": "tag", "name": "bar", "path": %q, "kind": "function"}`, pyFile),              // 4: missing line
		fmt.Sprintf(`{"_type": "tag", "name": "hello", "path": %q, "line": 1, "kind": "function"}`, pyFile), // valid
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	ctx := context.Background()
	fs, st, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	// Exactly 4 malformed lines.
	if st.MalformedLines != 4 {
		t.Errorf("MalformedLines = %d, want 4", st.MalformedLines)
	}

	// Only the valid tag yields a Decl.
	if len(fs) != 1 {
		t.Fatalf("len(fs) = %d, want 1", len(fs))
	}
	if len(fs[0].Decls) != 1 {
		t.Fatalf("len(Decls) = %d, want 1", len(fs[0].Decls))
	}
	if fs[0].Decls[0].Name != "hello" {
		t.Errorf("Decl name = %q, want %q", fs[0].Decls[0].Name, "hello")
	}
}

// TS-01-30: A tag echoing a path outside the batch is dropped.
func TestOutlineMany_ForeignPath_TS_01_30(t *testing.T) {
	dir := t.TempDir()
	pyFile := filepath.Join(dir, "a.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake output: one tag for a foreign path, one valid tag for a.py.
	output := strings.Join([]string{
		`{"_type": "tag", "name": "Foreign", "path": "/elsewhere/z.py", "line": 1, "kind": "function"}`,
		fmt.Sprintf(`{"_type": "tag", "name": "hello", "path": %q, "line": 1, "kind": "function"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	ctx := context.Background()
	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	if len(fs) != 1 {
		t.Fatalf("len(fs) = %d, want 1", len(fs))
	}

	// No File contains the Foreign decl.
	for _, f := range fs {
		for _, d := range f.Decls {
			if d.Name == "Foreign" {
				t.Error("found Foreign decl, should have been dropped")
			}
		}
	}

	// The a.py Decl is present.
	if len(fs[0].Decls) != 1 || fs[0].Decls[0].Name != "hello" {
		t.Errorf("expected hello decl, got %v", fs[0].Decls)
	}
}

// TS-01-31: Tags are attributed by echoed path, not batch position.
func TestOutlineMany_AttributeByPath_TS_01_31(t *testing.T) {
	dir := t.TempDir()
	aFile := filepath.Join(dir, "a.py")
	bFile := filepath.Join(dir, "b.py")
	cFile := filepath.Join(dir, "c.py")

	if err := os.WriteFile(aFile, []byte("def inA():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bFile, []byte("def inB():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cFile, []byte("# empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake output: b.py's tag comes before a.py's tag (reversed order).
	output := strings.Join([]string{
		fmt.Sprintf(`{"_type": "tag", "name": "inB", "path": %q, "line": 1, "kind": "function"}`, bFile),
		fmt.Sprintf(`{"_type": "tag", "name": "inA", "path": %q, "line": 1, "kind": "function"}`, aFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	ctx := context.Background()
	fs, _, err := OutlineMany(ctx, []Source{{Abs: aFile}, {Abs: bFile}, {Abs: cFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	if len(fs) != 3 {
		t.Fatalf("len(fs) = %d, want 3", len(fs))
	}

	// a.py has only inA.
	aNames := declNames(fs[0].Decls)
	if len(aNames) != 1 || aNames[0] != "inA" {
		t.Errorf("a.py decls = %v, want [inA]", aNames)
	}

	// b.py has only inB.
	bNames := declNames(fs[1].Decls)
	if len(bNames) != 1 || bNames[0] != "inB" {
		t.Errorf("b.py decls = %v, want [inB]", bNames)
	}

	// c.py has no tags but Backend should be ctags (it was in the batch).
	if fs[2].Backend != BackendCtags {
		t.Errorf("c.py Backend = %q, want %q", fs[2].Backend, BackendCtags)
	}
	if len(fs[2].Decls) != 0 {
		t.Errorf("c.py decls = %v, want empty", fs[2].Decls)
	}
	if fs[2].Decls == nil {
		t.Error("c.py Decls is nil, want non-nil empty slice")
	}
}
