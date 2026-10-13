package attach

import (
	"archive/tar"
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

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// tarWithFile is a tar holding one file of size n.
func tarWithFile(t *testing.T, name string, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(n)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bytes.Repeat([]byte("z"), n)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TS-13-7: tar data travels in chunks of at most 1 MiB in both directions.
// 13-REQ-1.6.
func TestTarChunkSizes_TS13_7(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	blob := tarWithFile(t, "big.bin", 3<<20+512<<10)
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(blob)}); err != nil {
		t.Fatal(err)
	}
	chunks := f.Chunks()
	if len(chunks) < 2 || chunks[0].GetStart() == nil {
		t.Fatalf("%d chunks, first %v; want a start chunk then data", len(chunks), chunks[0])
	}
	var got []byte
	for _, c := range chunks[1:] {
		if c.GetStart() != nil || len(c.GetData()) > 1<<20 {
			t.Fatalf("data chunk of %d bytes (start %v)", len(c.GetData()), c.GetStart() != nil)
		}
		got = append(got, c.GetData()...)
	}
	if !bytes.Equal(got, blob) {
		t.Error("the data chunks do not concatenate to the tar")
	}

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "big.bin"), bytes.Repeat([]byte("q"), 3<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	stream, err := rawAgent(t, ws).ExportTar(context.Background(), &sandboxpb.ExportRequest{Path: "big.bin"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for {
		c, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(c.GetData()) > 1<<20 {
			t.Fatalf("export chunk of %d bytes", len(c.GetData()))
		}
		n++
	}
	if n < 3 {
		t.Errorf("%d export chunks for a 3 MiB file, want at least 3", n)
	}
}

// TS-13-47: absolute and escaping Tar and Export paths are refused before
// any byte moves, while empty and dot name the root. 13-REQ-8.1.
func TestTransferPathRefusal_TS13_47(t *testing.T) {
	f := fakeagent.New()
	f.SetExport(make([]byte, 1024), nil)
	sb := attachFake(t, f, Config{})
	ctx := context.Background()
	for _, p := range []string{"/abs", "../x", "a/../../x"} {
		if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar), Path: p}); err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("Sync %q = %v, want an error naming it", p, err)
		}
	}
	for _, p := range []string{"/etc", ".."} {
		if err := sb.Export(ctx, p, sandbox.TarOut{W: io.Discard}); err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("Export %q = %v, want an error naming it", p, err)
		}
	}
	if f.Count("SyncTar") != 0 || f.Count("ExportTar") != 0 {
		t.Fatalf("refused paths made %d SyncTar and %d ExportTar RPCs", f.Count("SyncTar"), f.Count("ExportTar"))
	}
	for _, p := range []string{"", ".", "a/../b"} {
		if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar), Path: p}); err != nil {
			t.Errorf("Sync %q = %v", p, err)
		}
		if err := sb.Export(ctx, p, sandbox.TarOut{W: io.Discard}); err != nil {
			t.Errorf("Export %q = %v", p, err)
		}
	}
	if f.Count("SyncTar") != 3 || f.Count("ExportTar") != 3 {
		t.Errorf("%d SyncTar and %d ExportTar RPCs, want 3 each", f.Count("SyncTar"), f.Count("ExportTar"))
	}
	n := f.Count("Tools")
	if _, err := sb.Tools(ctx, sandbox.ToolOptions{}); err != nil || f.Count("Tools") != n+1 {
		t.Errorf("Tools afterwards: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TS-13-53: a read error from R or a cancelled context fails Sync with that
// cause, cancels the RPC and leaves the sandbox live. 13-REQ-8.6.
func TestSyncReadErrorAndCancel_TS13_53(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	errBoom := errors.New("boom")
	partial := tarWithFile(t, "part.bin", 2<<20)[:2<<20]
	_, err := sb.Sync(context.Background(), sandbox.Tar{R: io.MultiReader(bytes.NewReader(partial), errReader{errBoom})})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Sync with a failing reader = %v, want errBoom", err)
	}
	for deadline := time.Now().Add(2 * time.Second); status.Code(f.SyncErr()) != codes.Canceled; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the agent's stream ended with %v, want Canceled", f.SyncErr())
		}
	}

	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	start := time.Now()
	_, err = sb.Sync(c, sandbox.Tar{R: pr})
	if !errors.Is(err, c.Err()) || time.Since(start) > time.Second {
		t.Fatalf("Sync under a cancelled context = %v after %v, want %v within 1s", err, time.Since(start), c.Err())
	}
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(emptyTar)}); err != nil {
		t.Fatalf("a valid Sync afterwards = %v", err)
	}
}
