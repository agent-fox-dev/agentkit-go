package attach

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
)

// hostile is a tar holding ok.txt and then an escaping symlink.
func hostile(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "ok.txt", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("ok"))
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "esc", Linkname: "../../outside"})
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "after.txt", Mode: 0o644})
	_ = tw.Close()
	return buf.Bytes()
}

// Sync sends a start chunk and then the tar in data chunks of at most 1 MiB,
// and reports Sent as the bytes read from the tar.
func TestSyncStreamsTheTarInChunks(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	blob := testutil.Tar(t, map[string]string{"big.bin": strings.Repeat("z", 3<<20+512<<10)})
	r, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(blob), Path: "sub", Replace: true})
	if err != nil || r.Sent != int64(len(blob)) || r.Deleted != 0 {
		t.Fatalf("Sync = %+v, %v; want Sent %d and nothing deleted", r, err, len(blob))
	}
	chunks := f.Chunks()
	if s := chunks[0].GetStart(); s == nil || s.Path != "sub" || !s.Replace {
		t.Fatalf("first chunk %v, want the start chunk", chunks[0])
	}
	var got []byte
	for _, c := range chunks[1:] {
		if len(c.GetData()) > 1<<20 {
			t.Fatalf("a data chunk of %d bytes", len(c.GetData()))
		}
		got = append(got, c.GetData()...)
	}
	if !bytes.Equal(got, blob) {
		t.Error("the data chunks do not concatenate to the tar")
	}
}

// A tar synced into a real agent is extracted and counted, and a hostile
// entry is ErrUnsafeArchive with the earlier entries kept and the sandbox live.
func TestSyncIntoARealAgent(t *testing.T) {
	sb, ws := real(t)
	r, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(testutil.Tar(t, map[string]string{"a/b.txt": "abc"})), Path: "in"})
	if err != nil || r.Files != 1 || r.Bytes != 3 || read(filepath.Join(ws, "in", "a", "b.txt")) != "abc" {
		t.Fatalf("Sync = %+v, %v", r, err)
	}
	_, err = sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(hostile(t))})
	if !errors.Is(err, sandbox.ErrUnsafeArchive) || !strings.Contains(err.Error(), "esc") {
		t.Errorf("a hostile tar: %v, want ErrUnsafeArchive naming esc", err)
	}
	if !exists(filepath.Join(ws, "ok.txt")) || exists(filepath.Join(ws, "esc")) || exists(filepath.Join(ws, "after.txt")) {
		t.Error("want ok.txt kept and nothing from esc on")
	}
	isLive(t, sb)
}

// Export validates the agent's tar: a hostile entry is ErrUnsafeArchive with
// nothing from it on written, and a missing path an error naming it.
func TestExportValidatesTheAgentsTar(t *testing.T) {
	f := fakeagent.New()
	f.SetExport(hostile(t), nil)
	sb := attachFake(t, f, Config{})
	var out bytes.Buffer
	if err := sb.Export(context.Background(), "", sandbox.TarOut{W: &out}); !errors.Is(err, sandbox.ErrUnsafeArchive) {
		t.Fatalf("Export = %v, want ErrUnsafeArchive", err)
	}
	tr := tar.NewReader(bytes.NewReader(append(out.Bytes(), make([]byte, 1024)...)))
	if h, err := tr.Next(); err != nil || h.Name != "ok.txt" {
		t.Errorf("the output starts with %v, %v; want ok.txt", h, err)
	}
	if h, err := tr.Next(); err != io.EOF {
		t.Errorf("the output holds %v after ok.txt", h)
	}
	f.SetExport(nil, status.Error(codes.NotFound, "no"))
	if err := sb.Export(context.Background(), "gone/x", sandbox.TarOut{W: io.Discard}); err == nil || !strings.Contains(err.Error(), `"gone/x" does not exist`) {
		t.Errorf("a missing path: %v", err)
	}
	isLive(t, sb)
}

// Absolute and escaping paths are refused before any byte moves; empty and
// dot name the root.
func TestTransfersRefuseAPathOutsideTheWorkspace(t *testing.T) {
	f := fakeagent.New()
	f.SetExport(emptyTar, nil)
	sb := attachFake(t, f, Config{})
	ctx := context.Background()
	for _, p := range []string{"/abs", "../x", "a/../../x"} {
		_, err1 := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar), Path: p})
		err2 := sb.Export(ctx, p, sandbox.TarOut{W: io.Discard})
		if err1 == nil || err2 == nil || !strings.Contains(err1.Error(), p) || !strings.Contains(err2.Error(), p) {
			t.Errorf("%q: Sync %v, Export %v; want errors naming it", p, err1, err2)
		}
	}
	if f.Count("SyncTar") != 0 || f.Count("ExportTar") != 0 {
		t.Fatalf("refused paths made %d SyncTar and %d ExportTar RPCs", f.Count("SyncTar"), f.Count("ExportTar"))
	}
	for _, p := range []string{"", ".", "a/../b"} {
		_, err1 := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar), Path: p})
		if err2 := sb.Export(ctx, p, sandbox.TarOut{W: io.Discard}); err1 != nil || err2 != nil {
			t.Errorf("%q: Sync %v, Export %v", p, err1, err2)
		}
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// A read error from the tar or a done context fails Sync with that cause and
// cancels the stream; the sandbox stays live.
func TestSyncStopsAtAReadErrorOrTheContext(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	boom := errors.New("boom")
	part := bytes.Repeat([]byte("x"), 2<<20)
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: io.MultiReader(bytes.NewReader(part), errReader{boom})}); !errors.Is(err, boom) {
		t.Fatalf("Sync with a failing reader = %v, want boom", err)
	}
	testutil.Eventually(t, "the agent to see the stream cancelled", func() bool { return status.Code(f.SyncErr()) == codes.Canceled })
	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	if _, err := sb.Sync(c, sandbox.Tar{R: pr}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Sync with a reader that never returns = %v, want the context's error", err)
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(emptyTar)}); err != nil {
		t.Fatalf("a Sync afterwards = %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// A failing destination, a Tar with no reader and a GitRepo are the caller's
// errors, made without changing the sandbox's state.
func TestTransfersRefuseTheCallersMistakes(t *testing.T) {
	f := fakeagent.New()
	f.SetExport(testutil.Tar(t, map[string]string{"a.txt": "0123456789"}), nil)
	sb := attachFake(t, f, Config{Provider: "test-provider"})
	err := sb.Export(context.Background(), "", sandbox.TarOut{W: failingWriter{}})
	if err == nil || !strings.Contains(err.Error(), "disk full") || errors.Is(err, sandbox.ErrUnsafeArchive) || errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Export to a failing writer = %v, want the writer's error alone", err)
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{}); err == nil {
		t.Error("Sync with no reader succeeded")
	}
	if err := sb.Export(context.Background(), "", nil); err == nil {
		t.Error("Export to no destination succeeded")
	}
	n := f.Count("SyncTar") + f.Count("Rsync")
	_, err = sb.Sync(context.Background(), sandbox.GitRepo{URL: "https://h/r"})
	if !errors.Is(err, sandbox.ErrUnsupported) || !strings.Contains(err.Error(), "test-provider") || f.Count("SyncTar")+f.Count("Rsync") != n {
		t.Errorf("Sync(GitRepo) = %v; want ErrUnsupported naming test-provider and no RPC", err)
	}
	isLive(t, sb)
}
