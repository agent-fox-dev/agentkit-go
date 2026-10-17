//go:build unix

package mcpserve

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TS-23-24: ServeStdio with a ctx that is already done returns ctx.Err()
// without reading stdin. The file offset is shared with a duplicated
// descriptor, so a read through os.Stdin would move the duplicate's offset.
// 23-REQ-4.4.
func TestDoneContextReadsNothing_TS23_24(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte("these bytes must stay unread\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	dup := os.NewFile(uintptr(fd), "stdin-dup")
	defer dup.Close()

	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	s, _ := echoServer(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.ServeStdio(ctx); err != ctx.Err() {
		t.Errorf("ServeStdio returned %v, want ctx.Err()", err)
	}
	if off, err := dup.Seek(0, io.SeekCurrent); err != nil || off != 0 {
		t.Errorf("the shared offset is %d, %v; want 0: stdin was read", off, err)
	}
}
