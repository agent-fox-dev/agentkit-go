package stdioconn

import (
	"io"
	"net"
	"os"
	"testing"
)

// pair returns two adapters joined by two os.Pipe pairs.
func pair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	r1, w1, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a, b := New(r1, w2), New(r2, w1)
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// The adapter carries bytes both ways over two os.Pipe pairs, a Close gives
// io.EOF on the peer, Close is idempotent, and both addresses are non-nil.
func TestAdapterOverPipes(t *testing.T) {
	a, b := pair(t)
	buf := make([]byte, 2)
	if _, err := a.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(b, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("b read %q, %v", buf, err)
	}
	if _, err := b.Write([]byte("yo")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(a, buf); err != nil || string(buf) != "yo" {
		t.Fatalf("a read %q, %v", buf, err)
	}
	if a.LocalAddr() == nil || a.RemoteAddr() == nil {
		t.Error("nil address")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if _, err := b.Read(buf); err != io.EOF {
		t.Errorf("peer read after Close = %v, want io.EOF", err)
	}
}
