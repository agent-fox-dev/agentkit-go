package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// syncTar sends start, then blob in data chunks of at most 1 MiB, then the
// extra chunks.
func syncTar(c sandboxpb.AgentClient, start *sandboxpb.TarChunk, blob []byte, extra ...*sandboxpb.TarChunk) (*sandboxpb.SyncResponse, error) {
	s, err := c.SyncTar(context.Background())
	if err != nil {
		return nil, err
	}
	chunks := []*sandboxpb.TarChunk{start}
	for ; len(blob) > 0; blob = blob[min(len(blob), 1<<20):] {
		chunks = append(chunks, &sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: blob[:min(len(blob), 1<<20)]}})
	}
	for _, ch := range append(chunks, extra...) {
		if err := s.Send(ch); err != nil {
			break
		}
	}
	return s.CloseAndRecv()
}

func tarStart(path string, replace bool) *sandboxpb.TarChunk {
	return &sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Start{Start: &sandboxpb.TarStart{Path: path, Replace: replace}}}
}

// A successful sync rebuilds the tool set, so find_symbol sees the synced
// files, while a call already running finishes on the set it started on.
func TestSyncTarRebuildsTheToolSet(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a := newAgent(t, "", testEnv(t), time.Hour)
	a.build = func(ws *workspace.Workspace, o tools.Options) ([]core.Tool, error) {
		all, err := tools.For(ws, o)
		return append(all, stub("hold", func(context.Context) core.ToolResult {
			close(started)
			<-release
			return core.OKResult(nil)
		})), err
	}
	c, _ := serveRaw(t, a)
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	if r := call(t, c, "find_symbol", `{"name":"NewThing"}`); strings.Contains(r.Text+string(r.Data), "thing.go") {
		t.Fatalf("find_symbol matched before the sync: %s", r.Text)
	}
	inflight := make(chan *sandboxpb.CallResponse, 1)
	go func() { inflight <- call(t, c, "hold", `{}`) }()
	<-started
	body := "package pkg\n\nfunc NewThing() {}\n"
	res, err := syncTar(c, tarStart("", false), testutil.Tar(t, map[string]string{"pkg/thing.go": body}))
	if err != nil || res.Files != 1 || res.Bytes != int64(len(body)) {
		t.Fatalf("SyncTar: %v, %v", res, err)
	}
	if r := call(t, c, "find_symbol", `{"name":"NewThing"}`); !r.Ok || !strings.Contains(r.Text+string(r.Data), "pkg/thing.go") {
		t.Fatalf("find_symbol after the sync: %s %s %s", r.Error, r.Text, r.Data)
	}
	close(release)
	if r := <-inflight; !r.Ok {
		t.Fatalf("the in-flight call: %v", r)
	}
}

func TestSyncTarReplaceEmptiesTheTargetFirst(t *testing.T) {
	ws := testutil.Tree(t, map[string]string{"sub/old.txt": "o", "sub/d/deep": "d", "keep.txt": "k"})
	c := configured(t, ws)
	if _, err := syncTar(c, tarStart("sub", true), testutil.Tar(t, map[string]string{"new.txt": "n"})); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"sub/new.txt": true, "sub/old.txt": false, "sub/d": false, "keep.txt": true} {
		if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(p))); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", p, err == nil, want)
		}
	}
}

// A refused entry ends the sync with InvalidArgument naming it, after the
// entries before it; a bad start is refused before anything is written.
func TestSyncTarRefusals(t *testing.T) {
	ws := t.TempDir()
	c := configured(t, ws)
	bad := testutil.Tar(t, map[string]string{"first.txt": "x", "z/../../esc": "y"})
	for _, k := range []struct {
		start *sandboxpb.TarChunk
		blob  []byte
		extra *sandboxpb.TarChunk
		code  codes.Code
		word  string
	}{
		{tarStart("", false), bad, nil, codes.InvalidArgument, "esc"},
		{&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: []byte("x")}}, nil, nil, codes.InvalidArgument, "start chunk"},
		{tarStart("../out", false), nil, nil, codes.InvalidArgument, "leaves the workspace"},
		{tarStart("first.txt/sub", false), nil, nil, codes.FailedPrecondition, "first.txt/sub"},
		{tarStart("", false), []byte("garbage that is not a tar stream at all"), nil, codes.InvalidArgument, "malformed"},
		{tarStart("", false), nil, tarStart("", false), codes.InvalidArgument, "must carry data"},
	} {
		var extra []*sandboxpb.TarChunk
		if k.extra != nil {
			extra = append(extra, k.extra)
		}
		if _, err := syncTar(c, k.start, k.blob, extra...); status.Code(err) != k.code || !strings.Contains(status.Convert(err).Message(), k.word) {
			t.Errorf("SyncTar(%v) = %v, want %v naming %q", k.start, err, k.code, k.word)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "first.txt")); err != nil {
		t.Errorf("the entry before the refused one is gone: %v", err)
	}
}

// exportTar reads a whole ExportTar stream, checking the chunk size.
func exportTar(t *testing.T, c sandboxpb.AgentClient, path string) ([]byte, error) {
	t.Helper()
	st, err := c.ExportTar(context.Background(), &sandboxpb.ExportRequest{Path: path})
	if err != nil {
		return nil, err
	}
	var out []byte
	for {
		ch, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(ch.GetData()) > tarsafe.ChunkBytes {
			t.Errorf("a chunk of %d bytes", len(ch.GetData()))
		}
		out = append(out, ch.GetData()...)
	}
}

// ExportTar streams a subtree in chunks of at most tarsafe.ChunkBytes, and
// refuses a path that is missing or leaves the workspace.
func TestExportTarStreamsTheSubtreeInChunks(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	c := configured(t, testutil.Tree(t, map[string]string{"sub/big": big, "other": "o"}))
	blob, err := exportTar(t, c, "sub")
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	root, _ := os.OpenRoot(dst)
	defer root.Close()
	if _, err := tarsafe.Extract(root, bytes.NewReader(blob)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "big")); string(b) != big {
		t.Errorf("big has %d bytes, want %d", len(b), len(big))
	}
	for path, code := range map[string]codes.Code{"missing": codes.NotFound, "../x": codes.InvalidArgument, "/abs": codes.InvalidArgument} {
		if _, err := exportTar(t, c, path); status.Code(err) != code || !strings.Contains(err.Error(), path) {
			t.Errorf("ExportTar(%q) = %v, want %v naming it", path, err, code)
		}
	}
}
