package stdioconn

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// pair returns two connections joined by two os.Pipe pairs.
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

func TestConnCarriesBytesBothWays(t *testing.T) {
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
}

func TestConnCloseIsIdempotentAndGivesThePeerEOF(t *testing.T) {
	a, b := pair(t)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if _, err := b.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("peer read after Close = %v, want io.EOF", err)
	}
}

func TestConnAddressesAreStdio(t *testing.T) {
	a, _ := pair(t)
	for _, ad := range []net.Addr{a.LocalAddr(), a.RemoteAddr()} {
		if ad.Network() != "stdio" || ad.String() != "stdio" {
			t.Errorf("address = %s/%s, want stdio/stdio", ad.Network(), ad.String())
		}
	}
}

func TestConnForwardsDeadlinesToFiles(t *testing.T) {
	a, _ := pair(t)
	if err := a.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read past the deadline = %v, want os.ErrDeadlineExceeded", err)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestConnIgnoresDeadlinesOnStreamsThatHaveNone(t *testing.T) {
	c := New(io.NopCloser(strings.NewReader("x")), nopWriteCloser{io.Discard})
	if err := c.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetDeadline = %v, want nil", err)
	}
	if n, err := c.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Errorf("read = %d, %v; the deadline should be ignored", n, err)
	}
}
