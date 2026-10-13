//go:build unix

package sandbox_test

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-13-54: Export archives a subtree verbatim with hidden and ignored
// files, symlinks as symlinks, other types skipped, and names relative to
// the path. 13-REQ-8.7.
func TestExportVerbatim_TS13_54(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	out := filepath.Join(sb.Agent().Workdir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"result.txt": "result", ".hidden": "h", ".gitignore": "*.log\n", "build.log": "log"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("result.txt", filepath.Join(out, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(out, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := sb.Export(context.Background(), "out", sandbox.TarOut{W: &buf}); err != nil {
		t.Fatal(err)
	}
	hs, bodies := readTar(t, buf.Bytes())
	var names []string
	for _, h := range hs {
		names = append(names, h.Name)
		if h.Name == "link" && (h.Typeflag != tar.TypeSymlink || h.Linkname != "result.txt") {
			t.Errorf("link is %c -> %q", h.Typeflag, h.Linkname)
		}
	}
	slices.Sort(names)
	if want := []string{".gitignore", ".hidden", "build.log", "link", "result.txt"}; !slices.Equal(names, want) {
		t.Errorf("entries %v, want %v", names, want)
	}
	if bodies["result.txt"] != "result" {
		t.Errorf("result.txt holds %q", bodies["result.txt"])
	}
	buf.Reset()
	if err := sb.Export(context.Background(), "out/result.txt", sandbox.TarOut{W: &buf}); err != nil {
		t.Fatal(err)
	}
	if hs, bodies := readTar(t, buf.Bytes()); len(hs) != 1 || hs[0].Name != "result.txt" || hs[0].Typeflag != tar.TypeReg || bodies["result.txt"] != "result" {
		t.Errorf("single-file export: %v", hs)
	}
}
