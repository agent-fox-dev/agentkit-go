package tools

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// ------------------------------------------------------------------ read_file

func TestReadFilePagesThroughAFile(t *testing.T) {
	read := tool(t, map[string]string{
		"two.txt": "a\nb\n", "empty.txt": "", "main.go": "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
	}, "read_file")
	for _, c := range []struct {
		args, content, truncatedBy string
		lines                      int64
	}{
		// The model reads the file itself, with the markers as lines of it.
		{`{"path":"main.go"}`, "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}", "", 5},
		{`{"path":"main.go","limit":2}`, "package main\n\n" + ReadOffsetMarker(1, 2, 5), "lines", 5},
		{`{"path":"main.go","offset":4,"limit":1}`, "\tprintln(\"hi\")\n" + ReadOffsetMarker(4, 4, 5), "lines", 5},
		{`{"path":"main.go","offset":0,"limit":99999}`, "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}", "", 5},
		// "a\nb\n" is two lines, as an editor counts them.
		{`{"path":"two.txt","offset":2}`, "b", "", 2},
		{`{"path":"two.txt","limit":2}`, "a\nb", "", 2}, // a limit on the last line is no truncation
		{`{"path":"empty.txt"}`, "", "", 0},
	} {
		r := mustOK(t, call(read, c.args))
		if r.Text != c.content || r.Data["content"] != c.content || r.Data["encoding"] != "utf-8" ||
			r.Metadata.TruncatedBy != c.truncatedBy || r.Metadata.TotalLines != c.lines {
			t.Errorf("%s: Text %q %+v, want %q truncated by %q, %d lines", c.args, r.Text, r.Metadata, c.content, c.truncatedBy, c.lines)
		}
	}
	if r := call(read, `{"path":"two.txt","offset":3}`); r.Error != "offset_past_end" || r.Text != "" || !strings.HasPrefix(r.LLMText(), `{"detail"`) {
		t.Errorf("offset 3 of a 2-line file: %+v; an error keeps the envelope", r)
	}
	if r := call(read, `{"path":"two.txt"}`); r.Metadata.TotalBytes != 4 {
		t.Errorf("total bytes = %d, want 4", r.Metadata.TotalBytes)
	}
}

func TestReadFileCutsOnWholeLinesAndMarksALongLine(t *testing.T) {
	var big strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&big, "%03d%s\n", i, strings.Repeat("x", 1020)) // 1 KB lines
	}
	huge := strings.Repeat("é", 60*1024) // 120 KB on one line
	read := tool(t, map[string]string{"big.txt": big.String(), "long file.txt": "first\n" + huge + "\nlast\n"}, "read_file")

	r := mustOK(t, call(read, `{"path":"big.txt"}`))
	lines := strings.Split(r.Text, "\n")
	shown := lines[:len(lines)-1]
	for i, l := range shown {
		if len(l) != 1023 || l[:3] != fmt.Sprintf("%03d", i) {
			t.Fatalf("line %d was cut: %d bytes", i+1, len(l))
		}
	}
	if want := ReadOffsetMarker(1, len(shown), 100); lines[len(lines)-1] != want || r.Metadata.TruncatedBy != "bytes" || len(r.Text) > DefaultByteLimit+len(want)+1 {
		t.Fatalf("the byte cut ends %q (%s), want one marker %q with the real offset", lines[len(lines)-1], r.Metadata.TruncatedBy, want)
	}

	r = mustOK(t, call(read, `{"path":"long file.txt"}`))
	want := "first\n" + LongLineMarker(2, int64(len(huge)), DefaultByteLimit, "long file.txt") + "\nlast"
	if r.Text != want || r.Metadata.TruncatedBy != "bytes" || r.Metadata.TotalLines != 3 {
		t.Fatalf("Text %.80q… %+v, want the long line replaced by its marker and the read continued", r.Text, r.Metadata)
	}
}

func TestReadFileStreamsInsteadOfLoadingTheFile(t *testing.T) {
	const size = 16 << 20
	ws := newWS(t, nil)
	f, err := os.Create(abs(ws, "large.log"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := []byte(strings.Repeat("0123456789abcdef0123456789abcde\n", 32)) // 1 KB
	for n := 0; n < size; n += len(chunk) {
		_, _ = f.Write(chunk)
	}
	f.Close()
	read := toolSet(t, ws, Options{})["read_file"]
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r := mustOK(t, call(read, `{"path":"large.log","limit":5}`))
	runtime.ReadMemStats(&after)
	if r.Metadata.TotalLines != size/32 || after.TotalAlloc-before.TotalAlloc > size/4 {
		t.Fatalf("reading 5 lines of 16 MB allocated %d bytes (%d lines counted)", after.TotalAlloc-before.TotalAlloc, r.Metadata.TotalLines)
	}
}

func TestReadFileRefusesImagesByTheirMagicBytes(t *testing.T) {
	read := tool(t, map[string]string{
		"a.png":          "\x89PNG\r\n\x1a\n\x00",
		"screenshot.txt": "\x89PNG\r\n\x1a\n\x00", // the extension does not matter
		"b.jpg":          "\xff\xd8\xff\xe0\x00\x10JFIF",
		"c.gif":          "GIF89a\x01\x00\x01\x00",
		"d.webp":         "RIFF\x00\x00\x00\x00WEBPVP8 ",
	}, "read_file")
	for path, format := range map[string]string{"a.png": "PNG", "screenshot.txt": "PNG", "b.jpg": "JPEG", "c.gif": "GIF", "d.webp": "WebP"} {
		r := callJSON(t, read, map[string]string{"path": path})
		if r.Error != "unsupported_file" || r.Detail != path+" is a "+format+" image: reading images is not supported" || len(r.Blocks) != 0 {
			t.Errorf("%s: %+v", path, r)
		}
	}
}

func TestTheFileToolsRefuseWhatIsNotARegularFile(t *testing.T) {
	ws := newWS(t, map[string]string{"sub/x": ""})
	ts := toolSet(t, ws, Options{})
	for _, name := range []string{"read_file", "edit_file"} {
		r := call(ts[name], `{"path":"sub","edits":[{"old_string":"a","new_string":"b"}]}`)
		if r.Error != "not_a_file" || r.Detail != "sub is a directory; use list_files" {
			t.Errorf("%s on a directory: %+v", name, r)
		}
	}
	if runtime.GOOS != "windows" {
		if out, err := exec.Command("mkfifo", abs(ws, "pipe")).CombinedOutput(); err != nil {
			t.Skipf("mkfifo: %v %s", err, out)
		}
		for _, name := range []string{"read_file", "edit_file"} {
			done := make(chan core.ToolResult, 1)
			go func() { done <- call(ts[name], `{"path":"pipe","edits":[{"old_string":"a","new_string":"b"}]}`) }()
			select {
			case r := <-done:
				if r.Error != "not_a_file" || r.Detail != "pipe is a named pipe" {
					t.Errorf("%s on a FIFO: %+v", name, r)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s blocked opening a FIFO: it must be refused before it is opened", name)
			}
		}
	}
	for mode, want := range map[fs.FileMode]string{fs.ModeSocket: "x is a socket", fs.ModeDevice: "x is a device", fs.ModeIrregular: "x is not a regular file"} {
		if got := notAFile("x", mode); got != want {
			t.Errorf("notAFile(%v) = %q, want %q", mode, got, want)
		}
	}
}

func TestReadFileSaysWhenAFileIsNotUTF8(t *testing.T) {
	read := tool(t, map[string]string{"l1.txt": "caf\xe9\nplain\n", "ok.txt": "café\n"}, "read_file")
	r := mustOK(t, call(read, `{"path":"l1.txt"}`))
	if r.Data["encoding"] != "unknown (not valid UTF-8)" || r.Text != "caf\xe9\nplain\n"+InvalidUTF8Marker("l1.txt") {
		t.Errorf("Latin-1: %v %q", r.Data["encoding"], r.Text)
	}
	if r := mustOK(t, call(read, `{"path":"ok.txt"}`)); r.Data["encoding"] != "utf-8" || r.Text != "café" {
		t.Errorf("UTF-8: %v %q", r.Data["encoding"], r.Text)
	}
}

// ------------------------------------------------------------- write and edit

func TestWriteAndEditGiveTheModelASentence(t *testing.T) {
	ts := toolSet(t, newWS(t, nil), Options{})
	for _, c := range []struct{ tool, args, text string }{
		{"write_file", `{"path":"a/b.txt","content":"one two\n"}`, "Wrote 8 bytes to a/b.txt"},
		{"edit_file", `{"path":"a/b.txt","edits":[{"old_string":"two","new_string":"2"}]}`, "Applied 1 edit to a/b.txt"},
		{"edit_file", `{"path":"a/b.txt","edits":[{"old_string":"one","new_string":"1"},{"old_string":"2","new_string":"II"}]}`, "Applied 2 edits to a/b.txt"},
		{"read_file", `{"path":"a/b.txt"}`, "1 II"},
	} {
		if r := mustOK(t, call(ts[c.tool], c.args)); r.LLMText() != c.text {
			t.Errorf("%s %s: %q, want %q", c.tool, c.args, r.LLMText(), c.text)
		}
	}
	if r := mustOK(t, call(ts["write_file"], `{"path":"a/b.txt","content":"x"}`)); r.Data["written"] != true || r.Data["bytes"] != 1 {
		t.Errorf("write_file Data = %v", r.Data)
	}
}

func TestEditFile(t *testing.T) {
	ws := newWS(t, map[string]string{"win.txt": "alpha\r\nbravo\r\ncharlie\r\n", "dup.txt": "x\nx\n"})
	edit := toolSet(t, ws, Options{})["edit_file"]
	r := mustOK(t, call(edit, `{"path":"win.txt","edits":[{"old_string":"alpha\r\nbravo","new_string":"ALPHA\r\nBRAVO"}]}`))
	if got, _ := os.ReadFile(abs(ws, "win.txt")); string(got) != "ALPHA\r\nBRAVO\r\ncharlie\r\n" || r.Metadata.LineEnding != "crlf" || r.Data["edits_applied"] != 1 {
		t.Errorf("a CRLF edit: %q %+v", got, r.Metadata)
	}
	for args, code := range map[string]string{
		`{"path":"dup.txt","edits":[{"old_string":"x","new_string":"y"}]}`:       "edit_not_unique",
		`{"path":"dup.txt","edits":[]}`:                                          "edit_empty",
		`{"path":"dup.txt","edits":[{"old_string":"x\nx","new_string":"x\nx"}]}`: "edit_noop",
		`{"path":"dup.txt","edits":"not an edit list"}`:                          "invalid_arguments",
	} {
		if r := call(edit, args); r.Error != code {
			t.Errorf("%s: %q, want %q", args, r.Error, code)
		}
	}

	// The size is what is checked, so a sparse file is enough.
	f, err := os.Create(abs(ws, "big.log"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(EditFileMaxBytes + 1)
	f.Close()
	if r := call(edit, `{"path":"big.log","edits":[{"old_string":"a","new_string":"b"}]}`); r.Error != "file_too_large" || !strings.Contains(r.Detail, "use execute with sed") {
		t.Errorf("an oversized file: %+v", r)
	}
}

func TestWritesAreAtomicAndKeepModeAndLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits and symlinks differ on Windows")
	}
	ws := newWS(t, map[string]string{"run.sh": "echo old\n", "real.txt": "x"})
	ts := toolSet(t, ws, Options{})
	script := abs(ws, "run.sh")
	_ = os.Chmod(script, 0o755)
	before, _ := os.Stat(script)
	mustOK(t, call(ts["edit_file"], `{"path":"run.sh","edits":[{"old_string":"old","new_string":"new"}]}`))
	after, _ := os.Stat(script)
	if os.SameFile(before, after) || after.Mode().Perm() != 0o755 {
		t.Errorf("edit_file rewrote in place (%v) or lost the mode (%v); a crash mid-write would truncate the file", os.SameFile(before, after), after.Mode())
	}

	target := abs(ws, "real.txt")
	_ = os.Chmod(target, 0o600)
	mustSymlink(t, target, abs(ws, "link.txt"))
	mustOK(t, call(ts["write_file"], `{"path":"link.txt","content":"through the link"}`))
	fi, _ := os.Lstat(abs(ws, "link.txt"))
	got, _ := os.ReadFile(target)
	tfi, _ := os.Stat(target)
	if fi.Mode()&os.ModeSymlink == 0 || string(got) != "through the link" || tfi.Mode().Perm() != 0o600 {
		t.Errorf("write_file through a link: link %v, target %q mode %v", fi.Mode(), got, tfi.Mode())
	}
	entries, _ := os.ReadDir(ws.Root())
	for _, e := range entries {
		if strings.Contains(e.Name(), ".agentkit-") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
	// A directory in the way is a write failure, not a replaced directory.
	if r := call(ts["write_file"], `{"path":"sub/x","content":"a"}`); !r.OK {
		t.Fatal(r.Detail)
	}
	if r := call(ts["write_file"], `{"path":"sub","content":"a"}`); r.Error != "write_failed" {
		t.Errorf("writing over a directory: %+v", r)
	}
}

func TestTheFileToolsStayInsideTheWorkspace(t *testing.T) {
	outsideDir := t.TempDir()
	writeAt(t, outsideDir, "o.txt", "original")
	outside := filepath.Join(outsideDir, "o.txt")
	ws := newWS(t, nil)
	mustSymlink(t, outsideDir, abs(ws, "out"))
	mustSymlink(t, filepath.Join(outsideDir, "dangling"), abs(ws, "escape"))
	ts := toolSet(t, ws, Options{})
	for _, path := range []string{outside, "out/o.txt", "out/new.txt", "escape", "../o.txt"} {
		for name, args := range map[string]any{
			"read_file":  map[string]any{"path": path},
			"write_file": map[string]any{"path": path, "content": "pwned"},
			"edit_file":  map[string]any{"path": path, "edits": []Edit{{OldString: "original", NewString: "x"}}},
		} {
			r := callJSON(t, ts[name], args)
			if r.Error != "path_not_allowed" || !strings.HasPrefix(r.Detail, "workspace: path_not_allowed") ||
				!strings.Contains(r.Detail, ws.Root()) || !strings.Contains(r.Detail, "inside the workspace") {
				t.Errorf("%s %s: %+v; want path_not_allowed naming the root and the next step", name, path, r)
			}
		}
	}
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Errorf("the outside file now reads %q", got)
	}
	for _, p := range []string{"new.txt", "dangling"} {
		if _, err := os.Stat(filepath.Join(outsideDir, p)); err == nil {
			t.Errorf("%s was created outside the workspace", p)
		}
	}
	// A dangling link whose target is inside is written through.
	mustSymlink(t, "inner/newfile", abs(ws, "stay"))
	_ = os.Mkdir(abs(ws, "inner"), 0o755)
	mustOK(t, call(ts["write_file"], `{"path":"stay","content":"ok"}`))
	if b, _ := os.ReadFile(abs(ws, "inner/newfile")); string(b) != "ok" {
		t.Errorf("the write through an inside link landed elsewhere: %q", b)
	}
}

// TestWriteFileRechecksContainmentBeforeMakingDirectories swaps a directory
// for a link to outside while write_file waits on the path lock, after it
// resolved the path and before it writes: the re-check must run before
// MkdirAll, or the missing directories are built outside.
func TestWriteFileRechecksContainmentBeforeMakingDirectories(t *testing.T) {
	outside := t.TempDir()
	ws := newWS(t, map[string]string{"sub/keep": ""})
	ft := newFT(ws, Options{})
	key, err := lockKey(abs(ws, "sub/deeper/file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	release := ft.locks.acquire(key)
	done := make(chan core.ToolResult, 1)
	go func() { done <- call(ft.writeFile(), `{"path":"sub/deeper/file.txt","content":"pwned"}`) }()
	testutil.Eventually(t, "write_file to wait on the path lock", func() bool { return lockWaiters(ft.locks, key) == 2 })
	if err := os.RemoveAll(abs(ws, "sub")); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, abs(ws, "sub"))
	release()
	if r := <-done; r.Error != "path_not_allowed" {
		t.Fatalf("want path_not_allowed, got %+v", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "deeper")); err == nil {
		t.Fatal("MkdirAll built a directory outside the workspace")
	}
}

// lockWaiters is the number of holders and waiters of key.
func lockWaiters(p *pathLocks, key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.m[key]; e != nil {
		return e.ref
	}
	return 0
}

// TestEditsToOneFileSerializeAcrossItsSpellings holds the real file's lock
// and checks that edits through a symlink and a relative spelling queue on
// that same lock, while an edit to another file goes ahead.
func TestEditsToOneFileSerializeAcrossItsSpellings(t *testing.T) {
	ws := newWS(t, map[string]string{"real.txt": "alpha\nbravo\n", "other.txt": "charlie\n"})
	mustSymlink(t, "real.txt", abs(ws, "link.txt"))
	ft := newFT(ws, Options{})
	edit := ft.editFile()
	key, err := lockKey(abs(ws, "real.txt"))
	if err != nil {
		t.Fatal(err)
	}
	release := ft.locks.acquire(key)
	start := func(path, old, new string) <-chan core.ToolResult {
		ch := make(chan core.ToolResult, 1)
		go func() {
			ch <- callJSON(t, edit, map[string]any{"path": path, "edits": []Edit{{OldString: old, NewString: new}}})
		}()
		return ch
	}
	viaLink, viaRel := start("link.txt", "alpha", "ALPHA"), start("./sub/../real.txt", "bravo", "BRAVO")
	if r := <-start("other.txt", "charlie", "CHARLIE"); !r.OK {
		t.Fatalf("an edit to another file: %+v", r)
	}
	testutil.Eventually(t, "both spellings to wait on the real file's lock", func() bool { return lockWaiters(ft.locks, key) == 3 })
	release()
	for _, ch := range []<-chan core.ToolResult{viaLink, viaRel} {
		mustOK(t, <-ch)
	}
	if got, _ := os.ReadFile(abs(ws, "real.txt")); string(got) != "ALPHA\nBRAVO\n" {
		t.Fatalf("a lost update: %q", got)
	}
	if n := len(ft.locks.m); n != 0 {
		t.Fatalf("the lock table kept %d entries; they are released at refcount zero", n)
	}
}

func TestLockKeyResolvesSpellingsAndPropagatesOtherErrors(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Mkdir(filepath.Join(dir, "real"), 0o755)
	mustSymlink(t, "real", filepath.Join(dir, "linked"))
	for in, want := range map[string]string{
		filepath.Join(dir, "not-yet.txt"):         filepath.Join(dir, "not-yet.txt"),
		filepath.Join(dir, "linked", "new.txt"):   filepath.Join(dir, "real", "new.txt"), // the parent is resolved
		filepath.Join(dir, "missing", "deep.txt"): filepath.Join(dir, "missing", "deep.txt"),
	} {
		if got, err := lockKey(in); err != nil || got != want {
			t.Errorf("lockKey(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	mustSymlink(t, "loop", filepath.Join(dir, "loop"))
	if _, err := lockKey(filepath.Join(dir, "loop")); err == nil {
		t.Error("a link loop must be an error, not a key two spellings may not share")
	}
	if _, err := lockKey(filepath.Join(dir, "loop", "x")); err == nil {
		t.Error("a link loop in the parent must be an error")
	}
	ft := newFT(newWS(t, nil), Options{})
	mustSymlink(t, "loop", abs(ft.ws, "loop"))
	for _, name := range []string{"writeFile", "editFile"} {
		tl := map[string]core.Tool{"writeFile": ft.writeFile(), "editFile": ft.editFile()}[name]
		if r := call(tl, `{"path":"loop","content":"x","edits":[{"old_string":"a","new_string":"b"}]}`); r.OK {
			t.Errorf("%s through a link loop: %+v", name, r)
		}
	}
}

func TestRepairEditArgs(t *testing.T) {
	want := []any{map[string]any{"old_string": "a", "new_string": "b"}}
	for name, c := range map[string]struct {
		in   map[string]any
		want any
	}{
		"edits as a JSON string":           {map[string]any{"edits": `[{"old_string":"a","new_string":"b"}]`}, want},
		"a bare object":                    {map[string]any{"edits": map[string]any{"old_string": "a", "new_string": "b"}}, want},
		"legacy top-level keys":            {map[string]any{"old_string": "a", "new_string": "b"}, want},
		"quote-gt for quote-colon-quote":   {map[string]any{"edits": `[{"old_string">\tTokens int\n", "new_string":"b"}]`}, []any{map[string]any{"old_string": "\tTokens int\n", "new_string": "b"}}},
		"gt for colon":                     {map[string]any{"edits": `[{"old_string">"a", "new_string">"b"}]`}, want},
		"trailing commas":                  {map[string]any{"edits": `[{"old_string":"a","new_string":"b",},]`}, want},
		"a raw control character":          {map[string]any{"edits": "[{\"old_string\">\tfoo\nbar\", \"new_string\":\"b\"}]"}, []any{map[string]any{"old_string": "\tfoo\nbar", "new_string": "b"}}},
		"a string value is never repaired": {map[string]any{"edits": `[{"old_string":"a\">b,]","new_string":"b"},]`}, []any{map[string]any{"old_string": `a">b,]`, "new_string": "b"}}},
		"an unrepairable string is left":   {map[string]any{"edits": "not json at all"}, "not json at all"},
		"a repair that is not edits":       {map[string]any{"edits": `[{"x":1},]`}, `[{"x":1},]`},
		"an empty repaired list":           {map[string]any{"edits": `[,]`}, `[,]`},
	} {
		got := repairEditArgs(c.in)
		if testutil.MustJSON(t, got["edits"]) != testutil.MustJSON(t, c.want) {
			t.Errorf("%s: edits = %#v, want %#v", name, got["edits"], c.want)
		}
		if _, still := got["old_string"]; still && name == "legacy top-level keys" {
			t.Error("the legacy keys must be removed once folded into edits")
		}
	}
	// Wired into the argument pipeline, so the schema check sees the repair.
	c, err := core.NewToolUse("c", "edit_file", json.RawMessage(`{"path":"p.go","edits":"[{\"old_string\">\"a\", \"new_string\":\"b\"},]"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := core.PrepareArguments(tool(t, nil, "edit_file"), c); err != nil || testutil.MustJSON(t, p.Args["edits"]) != testutil.MustJSON(t, want) {
		t.Fatalf("PrepareArguments = %v, %v", p.Args, err)
	}
}

// ---------------------------------------------------------- list and find

func TestListFiles(t *testing.T) {
	files := map[string]string{"src/a.go": "", "README": "", ".gitignore": "README\n", "empty/.keep": ""}
	for i := 0; i < ListEntryCap+1; i++ {
		files[fmt.Sprintf("many/f%04d", i)] = ""
	}
	ws := newWS(t, files)
	mustSymlink(t, "src", abs(ws, "srclink"))
	_ = os.Remove(abs(ws, "empty/.keep"))
	list := toolSet(t, ws, Options{})["list_files"]
	// It does not apply .gitignore, and a link to a directory lists as one.
	if r := mustOK(t, call(list, `{}`)); r.Text != ".gitignore\nREADME\nempty/\nmany/\nsrc/\nsrclink/" {
		t.Errorf("list_files Text = %q", r.Text)
	}
	if r := mustOK(t, call(list, `{"path":"empty"}`)); r.Text != "No entries." || r.Data["truncated"] != false {
		t.Errorf("an empty directory: %q", r.Text)
	}
	r := mustOK(t, call(list, `{"path":"many"}`))
	if entries := r.Data["entries"].([]string); len(entries) != ListEntryDefault || r.Data["note"] != ListMarker(ListEntryDefault) ||
		!strings.HasSuffix(r.Text, "\n"+ListMarker(ListEntryDefault)) || r.Metadata.TruncatedBy != "lines" {
		t.Errorf("the default limit: %d entries, note %v", len(entries), r.Data["note"])
	}
	r = mustOK(t, call(list, `{"path":"many","limit":5000}`))
	if entries := r.Data["entries"].([]string); len(entries) != ListEntryCap || r.Data["note"] != ListMarker(ListEntryCap) || strings.HasPrefix(entries[len(entries)-1], "[") {
		t.Errorf("a limit over the cap: %d entries, note %v; the marker is not an entry", len(entries), r.Data["note"])
	}
	if r := call(list, `{"path":"missing"}`); r.Error != "list_failed" {
		t.Errorf("a missing directory: %+v", r)
	}
}

func TestFindFiles(t *testing.T) {
	files := map[string]string{
		"src/a.go": "", "src/pkg/b.go": "", "docs/c.md": "", "UPPER.GO": "",
		".gitignore": "*.log\nignored/\n", "x.log": "", "ignored/d.go": "",
		"gen/.gitignore": "*.go\n", "gen/generated.go": "",
		".github/ci.yml": "", "vendor/dep/.git/HEAD": "", "sub/mod/.git": "gitdir: x\n", "sub/mod/m.go": "",
	}
	for i := 0; i < FindResultDefault+1; i++ {
		files[fmt.Sprintf("many/f%04d.txt", i)] = ""
	}
	find := tool(t, files, "find_files")
	run := func(args string) string {
		t.Helper()
		return strings.Join(mustOK(t, call(find, args)).Data["files"].([]string), " ")
	}
	for args, want := range map[string]string{
		// Ignored files and .git at any depth are skipped; other dotfiles are not.
		`{"pattern":"**/*.go"}`:                          "UPPER.GO src/a.go src/pkg/b.go sub/mod/m.go",
		`{"pattern":"**/*.go","path":"src"}`:             "a.go pkg/b.go",
		`{"pattern":"**/*.yml"}`:                         ".github/ci.yml",
		`{"pattern":"*","file_type":"dir","path":"src"}`: "pkg/",
		`{"pattern":"src/*","file_type":"directory"}`:    "src/pkg/",
		`{"pattern":"src/*","file_type":"any"}`:          "src/a.go src/pkg/",
		`{"pattern":"**/*.zzz"}`:                         "",
	} {
		if got := run(args); got != want {
			t.Errorf("%s = %q, want %q", args, got, want)
		}
	}
	if r := mustOK(t, call(find, `{"pattern":"*.zzz"}`)); r.Text != `No files match "*.zzz".` {
		t.Errorf("no match: %q", r.Text)
	}
	r := mustOK(t, call(find, `{"pattern":"many/*.txt"}`))
	if n := len(r.Data["files"].([]string)); n != FindResultDefault || r.Data["marker"] != FindMarker(FindResultDefault) ||
		!strings.HasSuffix(r.Text, "\n"+FindMarker(FindResultDefault)) || r.Metadata.TruncatedBy != "lines" {
		t.Errorf("the default limit: %d files, marker %v", n, r.Data["marker"])
	}
	if n := len(mustOK(t, call(find, `{"pattern":"many/*.txt","limit":5000}`)).Data["files"].([]string)); n != FindResultDefault+1 {
		t.Errorf("a limit over the cap is clamped to the cap, which fits all %d: got %d", FindResultDefault+1, n)
	}
	if r := mustOK(t, call(find, `{"pattern":"**","file_type":"dir","limit":1}`)); r.Data["truncated"] != true {
		t.Errorf("a directory over the limit truncates too: %v", r.Data)
	}
	if r := call(find, `{"pattern":"*","file_type":"symlink"}`); r.Error != "invalid_arguments" {
		t.Errorf("an unknown file_type: %+v", r)
	}
}

// TestTheWalkingToolsReadTheGlobalExcludesOncePerWorkspace injects a global
// excludes file through the workspace and counts the git config lookups.
func TestTheWalkingToolsReadTheGlobalExcludesOncePerWorkspace(t *testing.T) {
	root := testutil.Tree(t, map[string]string{"a.go": "package a\n\nfunc Needle() {}\n", "b.secret": "Needle\n"})
	excludes := filepath.Join(t.TempDir(), "global-ignore")
	writeAt(t, filepath.Dir(excludes), "global-ignore", "*.secret\n")
	var lookups atomic.Int32
	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.IgnoreOptions{
		Getenv:    func(string) string { return "" },
		Home:      func() (string, error) { return "", os.ErrNotExist },
		GitConfig: func() string { lookups.Add(1); return excludes },
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := toolSet(t, ws, Options{})
	for i := 0; i < 2; i++ {
		for name, args := range map[string]string{
			"find_files": `{"pattern":"*"}`, "search_files": `{"pattern":"Needle"}`,
			"find_symbol": `{"name":"Needle"}`, "find_references": `{"name":"Needle"}`,
		} {
			if r := mustOK(t, call(ts[name], args)); strings.Contains(r.Text, "b.secret") {
				t.Errorf("%s did not apply the global excludes: %s", name, r.Text)
			}
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("git config was consulted %d times, want once per workspace", n)
	}
}

// ------------------------------------------------------------- shell tools

func TestTheShellToolsRunInTheWorkspaceRoot(t *testing.T) {
	needPOSIX(t)
	ws := newWS(t, map[string]string{"script.sh": "#!/bin/sh\necho from-workspace\n"})
	_ = os.Chmod(abs(ws, "script.sh"), 0o755)
	if cwd, _ := os.Getwd(); cwd == ws.Root() {
		t.Fatal("the test needs the working directory to differ from the workspace")
	}
	ts := toolSet(t, ws, Options{})
	for name, args := range map[string]string{
		"execute": `{"command":"pwd"}`, "run_command": `{"argv":["pwd"]}`,
	} {
		if r := mustOK(t, call(ts[name], args)); r.Text != ws.Root()+"\n" {
			t.Errorf("%s printed %q, want the workspace root", name, r.Text)
		}
	}
	// A relative program is the workspace's, not the process's.
	if r := mustOK(t, call(ts["run_command"], `{"argv":["./script.sh"]}`)); r.Text != "from-workspace\n" {
		t.Errorf("./script.sh: %q", r.Text)
	}
}

func TestTheShellToolsValidateTheirArguments(t *testing.T) {
	ts := toolSet(t, newWS(t, nil), Options{})
	for _, c := range []struct{ tool, args string }{
		{"execute", `{"command":"  "}`},
		{"execute", `{"command":"true","timeout_s":0}`},
		{"execute", `{"command":"true","timeout_s":-5}`},
		{"run_command", `{"argv":[]}`},
		{"run_command", `{"argv":["  "]}`},
		{"run_command", `{}`},
		{"run_command", `{"argv":["true"],"timeout_s":0}`},
	} {
		if r := call(ts[c.tool], c.args); r.Error != "invalid_arguments" {
			t.Errorf("%s %s: %+v, want invalid_arguments", c.tool, c.args, r)
		}
	}
	r := call(ts["run_command"], `{"argv":["definitely-not-a-program-xyz"]}`)
	if r.Error != "exec_failed" || !strings.Contains(r.Detail, "definitely-not-a-program-xyz") {
		t.Errorf("a missing program: %+v", r)
	}
}

func TestTheShellToolsEnvelope(t *testing.T) {
	needPOSIX(t)
	ts := toolSet(t, newWS(t, nil), Options{})
	for _, c := range []struct{ tool, args, text, code string }{
		{"execute", `{"command":"echo out; echo err >&2; exit 3"}`, "out\nerr\n[exit 3]", "command_exit"},
		{"execute", `{"command":"printf partial; sleep 30","timeout_s":1}`, "partial\n[timeout after 1s]", "command_timeout"},
		// Nothing is re-parsed: each argument reaches the program verbatim.
		{"run_command", `{"argv":["echo","hello; $(id)","&& rm -rf /"]}`, "hello; $(id) && rm -rf /\n", ""},
		{"run_command", `{"argv":["sh","-c","kill -KILL $$"]}`, "[killed by signal 9]", "command_signal"},
		// The command's stdin is the null device.
		{"run_command", `{"argv":["sh","-c","cat; echo after"],"timeout_s":5}`, "after\n", ""},
	} {
		r := call(ts[c.tool], c.args)
		if r.Text != c.text || r.Error != c.code || r.OK != (c.code == "") || r.Metadata.ExitCode == nil ||
			r.Metadata.Outcome != r.Data["outcome"] || *r.Metadata.ExitCode != r.Data["exit_code"] {
			t.Errorf("%s %s: Text %q error %q %+v, want %q %q", c.tool, c.args, r.Text, r.Error, r.Metadata, c.text, c.code)
		}
	}
}

func TestExecResultToToolAddsAStatusLineOnlyWhenInformative(t *testing.T) {
	for _, c := range []struct {
		res     ExecResult
		timeout time.Duration
		want    string
	}{
		{ExecResult{Output: "hello\n", Outcome: OutcomeOK}, 0, "hello\n"},
		{ExecResult{Output: "boom", Outcome: OutcomeExit, ExitCode: 2}, 0, "boom\n[exit 2]"},
		{ExecResult{Outcome: OutcomeExit, ExitCode: 1}, 0, "[exit 1]"},
		{ExecResult{Output: "p\n", Outcome: OutcomeTimeout}, 5 * time.Second, "p\n[timeout after 5s]"},
		{ExecResult{Outcome: OutcomeTimeout}, 0, "[timeout]"},
		{ExecResult{Output: "x\n", Outcome: OutcomeAbort}, 0, "x\n[aborted]"},
		{ExecResult{Outcome: OutcomeSignal, ExitCode: 143}, 0, "[killed by signal 15]"},
		{ExecResult{Outcome: OutcomeSignal, ExitCode: -1}, 0, "[killed by signal]"},
	} {
		got := execResultToTool(c.res, c.timeout)
		if got.Text != c.want || got.OK != (c.res.Outcome == OutcomeOK) {
			t.Errorf("%+v: Text %q OK %v, want %q", c.res, got.Text, got.OK, c.want)
		}
	}
	// IOErr is not rendered: the result is the same without it.
	base := ExecResult{Output: "t", Outcome: OutcomeTimeout, ExitCode: 137, Truncated: true, TotalBytes: 9, SpillPath: "/s", Duration: time.Second}
	withErr := base
	withErr.IOErr = os.ErrClosed
	a, b := execResultToTool(base, time.Second), execResultToTool(withErr, time.Second)
	if testutil.MustJSON(t, a) != testutil.MustJSON(t, b) || a.Metadata.TruncatedBy != "bytes" || a.Metadata.SpillPath != "/s" || a.Metadata.DurationMS != 1000 {
		t.Errorf("with IOErr %s\nwithout %s", testutil.MustJSON(t, b), testutil.MustJSON(t, a))
	}
}

func TestTheShellToolsTruncateFromTheTailAndSpill(t *testing.T) {
	needPOSIX(t)
	cmd := `{"command":"head -c 70000 /dev/zero | tr '\\0' x; echo END"}`
	spill := filepath.Join(t.TempDir(), "spill")
	ws := newWS(t, nil)
	r := call(toolSet(t, ws, Options{SpillDir: spill})["execute"], cmd)
	if !r.OK || !strings.HasPrefix(r.Text, "[18804 bytes elided of 70004 total. Full output: "+spill+"/agentkit-exec-") ||
		!strings.HasSuffix(r.Text, "xEND\n") || r.Metadata.TotalBytes != 70004 || r.Metadata.TruncatedBy != "bytes" {
		t.Fatalf("%.120q… %+v", r.Text, r.Metadata)
	}
	if fi, err := os.Stat(r.Metadata.SpillPath); err != nil || fi.Size() != 70004 {
		t.Fatalf("the spill file must hold the complete output: %v %v", fi, err)
	}
	// Spilling is on by default, into a per-workspace directory under the
	// temp directory named after a hash of the root.
	r = call(toolSet(t, ws, Options{})["execute"], cmd)
	h := fnv.New32a()
	_, _ = h.Write([]byte(ws.Root()))
	want := filepath.Join(os.TempDir(), fmt.Sprintf("agentkit-spill-%08x", h.Sum32()))
	t.Cleanup(func() { os.RemoveAll(want) })
	if filepath.Dir(r.Metadata.SpillPath) != want || defaultSpillDir(newWS(t, nil).Root()) == want {
		t.Fatalf("default spill %q, want it under %s, a directory per workspace", r.Metadata.SpillPath, want)
	}
	if r := call(toolSet(t, ws, Options{DisableSpill: true})["execute"], cmd); r.Metadata.SpillPath != "" {
		t.Fatalf("DisableSpill left %q", r.Metadata.SpillPath)
	}
}

func TestTheShellToolsEnvironment(t *testing.T) {
	needPOSIX(t)
	t.Setenv("GITHUB_TOKEN", "ghp_should_not_leak")
	ws := newWS(t, nil)
	// A nil Env is reduced for a tool built by hand too, not only through For.
	for _, tl := range []core.Tool{executeTool(ws, Options{}), runCommandTool(ws, Options{})} {
		if r := mustOK(t, call(tl, `{"command":"env","argv":["env"]}`)); strings.Contains(r.Text, "ghp_") || !strings.Contains(r.Text, "PATH=") {
			t.Errorf("%s leaked the token:\n%s", tl.Name, r.Text)
		}
	}
	ts := toolSet(t, ws, Options{Env: []string{"PATH=" + os.Getenv("PATH"), "MARK=1"}})
	for _, name := range []string{"execute", "run_command"} {
		if r := mustOK(t, call(ts[name], `{"command":"env","argv":["env"]}`)); !strings.Contains(r.Text, "MARK=1") || strings.Contains(r.Text, "ghp_") {
			t.Errorf("%s did not run with Options.Env:\n%s", name, r.Text)
		}
	}
}
