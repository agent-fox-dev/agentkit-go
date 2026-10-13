package sandbox_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// TS-14-3: a Tar sync reports Sent as the tar bytes read from R and Deleted
// as 0 even when Replace removes entries. 14-REQ-1.4.
func TestTarSent_TS14_3(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "d/", Mode: 0o755})
	for name, n := range map[string]int{"d/a": 10, "b": 2000, "c": 70000} {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(n)})
		_, _ = tw.Write([]byte(strings.Repeat("x", n)))
	}
	_ = tw.Close()
	archive := buf.Bytes()

	c1 := &countingReader{r: bytes.NewReader(archive)}
	r1, err := sb.Sync(context.Background(), sandbox.Tar{R: c1})
	if err != nil {
		t.Fatal(err)
	}
	if want := (sandbox.SyncResult{Files: 3, Bytes: 72010, Deleted: 0, Sent: c1.n}); r1 != want || c1.n != int64(len(archive)) {
		t.Errorf("first Sync = %+v (read %d of %d), want %+v", r1, c1.n, len(archive), want)
	}
	if r := tool(t, sb, "write_file").Execute(context.Background(), []byte(`{"path":"extra.txt","content":"e"}`)); !r.OK {
		t.Fatal(r)
	}
	c2 := &countingReader{r: bytes.NewReader(archive)}
	r2, err := sb.Sync(context.Background(), sandbox.Tar{R: c2, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(sb.Agent().Workdir, "extra.txt")) {
		t.Error("extra.txt survived the Replace")
	}
	if r2.Deleted != 0 || r2.Sent != c2.n || c2.n != int64(len(archive)) {
		t.Errorf("second Sync = %+v (read %d of %d), want Deleted 0 and Sent the archive length", r2, c2.n, len(archive))
	}
}
