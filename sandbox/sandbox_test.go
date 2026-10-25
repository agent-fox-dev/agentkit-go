package sandbox_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/internal/toolconformance"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The tests here run a sandbox end to end through the local provider: the
// real agent, attach, the gRPC transport, rsync and the tar rules.

func TestLocalDirSyncAppliesTheIgnoreRulesAndAResyncMovesOnlyTheChange(t *testing.T) {
	noLocalDir(t)
	testutil.Hermetic(t)
	ctx := context.Background()
	files := map[string]string{".gitignore": "build/\n", "build/out.o": "o", ".git/HEAD": "ref: refs/heads/main\n", "old.txt": "old", "notes.txt": "first"}
	for i := range 15 {
		files[fmt.Sprintf("pkg/f%02d.go", i)] = fmt.Sprintf("package pkg // %d\n", i)
	}
	src := testutil.Tree(t, files)
	if err := os.Symlink("/etc", filepath.Join(src, "esc")); err != nil {
		t.Fatal(err)
	}
	sb := newLocal(t)
	ws := sb.Agent().Workdir

	r, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Delete: true})
	if err != nil || r.Files != 19 || r.Sent <= 0 { // build/out.o is ignored
		t.Fatalf("the first Sync = %+v, %v; want 19 files and a positive Sent", r, err)
	}
	if readFile(t, filepath.Join(ws, ".git/HEAD")) != files[".git/HEAD"] || lexists(filepath.Join(ws, "build")) || lexists(filepath.Join(ws, "esc")) {
		t.Error("the workspace lacks .git/HEAD, or holds the ignored build/ or the escaping symlink")
	}

	const notes = "second, and longer"
	testutil.WriteTree(t, src, map[string]string{"notes.txt": notes})
	if err := os.Remove(filepath.Join(src, "old.txt")); err != nil {
		t.Fatal(err)
	}
	r, err = sb.Sync(ctx, sandbox.LocalDir{Path: src, Delete: true})
	if err != nil || r.Files != 1 || r.Bytes != int64(len(notes)) || r.Deleted != 1 {
		t.Errorf("the re-sync = %+v, %v; want 1 file of %d bytes and 1 deletion", r, err, len(notes))
	}
	if got := call(t, sb, "read_file", `{"path":"notes.txt"}`); !strings.Contains(got.LLMText(), notes) || lexists(filepath.Join(ws, "old.txt")) {
		t.Errorf("after the re-sync read_file gives %q, old.txt exists %v", got.LLMText(), lexists(filepath.Join(ws, "old.txt")))
	}
}

func TestExportWithDeleteKeepsTheDestinationsGitAndIgnoredFiles(t *testing.T) {
	noLocalDir(t)
	testutil.Hermetic(t)
	ctx := context.Background()
	sb := newLocal(t)
	dst := testutil.Tree(t, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".gitignore": ".env\n", ".env": "TOKEN=1\n", "stale.txt": "s"})
	// A workspace .git, which an export never carries.
	testutil.WriteTree(t, sb.Agent().Workdir, map[string]string{".git/HEAD": "evil", ".git/hooks/post-checkout": "#!/bin/sh\n"})
	call(t, sb, "write_file", `{"path":"out/result.txt","content":"result"}`)

	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: dst, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dst, "out/result.txt")) != "result" || lexists(filepath.Join(dst, "stale.txt")) {
		t.Error("dst lacks out/result.txt or still holds stale.txt")
	}
	if readFile(t, filepath.Join(dst, ".git/HEAD")) != "ref: refs/heads/main\n" || lexists(filepath.Join(dst, ".git/hooks")) || readFile(t, filepath.Join(dst, ".env")) != "TOKEN=1\n" {
		t.Error("the export changed dst/.git or dst/.env")
	}

	var buf bytes.Buffer
	if err := sb.Export(ctx, "out", sandbox.TarOut{W: &buf}); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	h, err := tr.Next()
	if err != nil || h.Name != "result.txt" {
		t.Fatalf("the exported tar starts with %v, %v; want result.txt", h, err)
	}
	if body, _ := io.ReadAll(tr); string(body) != "result" {
		t.Errorf("result.txt in the tar holds %q", body)
	}
}

func TestARefusedTarEntryStopsTheSyncAndTheSandboxStaysLive(t *testing.T) {
	ctx := context.Background()
	sb := newLocal(t)
	blob := testutil.Tar(t, map[string]string{"a.txt": "kept", "z/../../evil": "x"})
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(blob)}); !errors.Is(err, sandbox.ErrUnsafeArchive) {
		t.Fatalf("Sync = %v, want ErrUnsafeArchive", err)
	}
	if got := call(t, sb, "read_file", `{"path":"a.txt"}`); !strings.Contains(got.LLMText(), "kept") {
		t.Errorf("the entry before the refused one: %q", got.LLMText())
	}
	r, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(testutil.Tar(t, map[string]string{"b.txt": "later"})), Path: "sub"})
	if err != nil || r.Files != 1 || r.Bytes != 5 {
		t.Errorf("a later Sync = %+v, %v", r, err)
	}
}

func TestProxyToolsPassTheSharedToolConformanceCases(t *testing.T) {
	build := func(t *testing.T, tree toolconformance.Tree) []core.Tool {
		sb := newLocal(t)
		if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(tree.Tar(t))}); err != nil {
			t.Fatal(err)
		}
		tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	for _, name := range toolconformance.CaseNames() {
		t.Run(name, func(t *testing.T) { toolconformance.RunGroup(t, name, build) })
	}
}
