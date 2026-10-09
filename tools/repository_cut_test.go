package tools_test

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// cutReadFile is read_file from tools.All over a fresh workspace at root.
func cutReadFile(t *testing.T, root string) core.Tool {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name == "read_file" {
			return tl
		}
	}
	t.Fatal("read_file is not in tools.All")
	return core.Tool{}
}

// assertImageRefused reads a file holding data and checks the 09-REQ-3.2..3.5
// contract: OK false, Error unsupported_file, and a Detail that says image
// reading is unsupported and names the format.
func assertImageRefused(t *testing.T, name string, data []byte, format string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	res := cutReadFile(t, root).Execute(context.Background(), json.RawMessage(`{"path":"`+name+`"}`))
	if res.OK || res.Error != "unsupported_file" {
		t.Fatalf("%s: OK %v Error %q, want unsupported_file", name, res.OK, res.Error)
	}
	if !strings.Contains(res.Detail, "reading images is not supported") || !strings.Contains(res.Detail, format) {
		t.Fatalf("%s: Detail %q, want it to say reading images is not supported and name %s", name, res.Detail, format)
	}
}

// TS-09-5: tools.go imports no image library, and read_file declares a
// text-only description and output schema.
func TestReadFileIsTextOnly_TS09_5(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "tools.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if strings.HasSuffix(p, "/imagex") || strings.HasPrefix(p, "golang.org/x/image") {
			t.Errorf("tools.go imports %s", p)
		}
	}
	tl := cutReadFile(t, t.TempDir())
	if strings.Contains(strings.ToLower(tl.Description), "image") {
		t.Errorf("read_file description mentions images: %q", tl.Description)
	}
	b, err := json.Marshal(tl.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mime_type", "width", "height"} {
		if strings.Contains(string(b), key) {
			t.Errorf("read_file output schema declares %s: %s", key, b)
		}
	}
}

// TS-09-6: PNG and JPEG are refused by their magic bytes.
func TestReadFileRefusesPNGAndJPEG_TS09_6(t *testing.T) {
	assertImageRefused(t, "sample.png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00}, "PNG")
	assertImageRefused(t, "sample.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}, "JPEG")
}

// TS-09-7: GIF and WebP are refused by their magic bytes.
func TestReadFileRefusesGIFAndWebP_TS09_7(t *testing.T) {
	assertImageRefused(t, "sample.gif", []byte("GIF89a\x01\x00\x01\x00"), "GIF")
	assertImageRefused(t, "sample.webp", append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("VP8 ")...), "WebP")
}

// TS-09-8: tools.All registers none of the pruned tools, and the conformance
// suite no longer names them.
func TestPrunedToolsAbsent_TS09_8(t *testing.T) {
	all, err := tools.All(tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(all))
	for i, tl := range all {
		names[i] = tl.Name
	}
	src, err := os.ReadFile("conformance_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, pruned := range []string{"fetch_url", "powershell", "subagent"} {
		if slices.Contains(names, pruned) {
			t.Errorf("tools.All registers %s", pruned)
		}
		if strings.Contains(string(src), pruned) {
			t.Errorf("conformance_test.go still mentions %s", pruned)
		}
	}
}

// TS-09-9: every tool tools.All returns is one of the approved workspace and
// navigation tools.
func TestAllToolsApproved_TS09_9(t *testing.T) {
	approved := map[string]bool{
		"read_file": true, "write_file": true, "edit_file": true,
		"list_files": true, "find_files": true, "search_files": true,
		"file_outline": true, "find_symbol": true, "find_references": true,
		"execute": true, "run_command": true,
	}
	all, err := tools.All(tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if !approved[tl.Name] {
			t.Errorf("tools.All returns unapproved tool %q", tl.Name)
		}
	}
}
