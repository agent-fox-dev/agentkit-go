//go:build !windows

package rsyncx

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TS-14-33 (library half): ModulesFor gives each direction one module over
// the workspace root, writable only for SYNC, and a client pushing into the
// read-only server NewServer builds fails without changing the tree it
// serves. 14-REQ-6.6. The Export half is in sandbox/rsync_export_test.go.
func TestExportServerReadOnly_TS14_33(t *testing.T) {
	ws, src := t.TempDir(), t.TempDir()
	for _, d := range []struct {
		dir      Direction
		writable bool
	}{{DirSync, true}, {DirExport, false}} {
		m := ModulesFor(ws, d.dir)
		if len(m) != 1 || m[0].Root != ws || m[0].Writable != d.writable {
			t.Errorf("ModulesFor(%v) = %+v", d.dir, m)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pushed.txt"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	srv, err := NewServer(Filtered(root, ".", NewMatcher(nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The server-side args of a push: the server receives.
	args, err := ServerArgs(ClientArgs(false), false, ".")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sEnd, cEnd := osPipe(t)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, sEnd, args); _ = sEnd.Close() }()
	c, err := newClient(ClientArgs(false), true)
	if err != nil {
		t.Fatal(err)
	}
	_, pushErr := c.Run(ctx, cEnd, []string{src + "/"})
	_ = cEnd.Close()
	if serveErr := <-served; pushErr == nil && serveErr == nil {
		t.Error("a push into the EXPORT server succeeded")
	}
	ents, _ := os.ReadDir(ws)
	if len(ents) != 1 || ents[0].Name() != "a.txt" {
		t.Errorf("the served tree now holds %v", ents)
	}
}

// end is one side of a kernel-buffered duplex pipe; net.Pipe's unbuffered
// writes would deadlock two peers writing at once.
type end struct {
	io.Reader
	io.WriteCloser
	r io.Closer
}

func (e *end) Close() error { _ = e.r.Close(); return e.WriteCloser.Close() }

func osPipe(t *testing.T) (*end, *end) {
	t.Helper()
	ar, bw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	br, aw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	return &end{Reader: ar, WriteCloser: aw, r: ar}, &end{Reader: br, WriteCloser: bw, r: br}
}
