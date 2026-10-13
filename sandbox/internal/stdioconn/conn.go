// Package stdioconn makes a net.Conn of a reader and a writer, such as a
// process's stdin and stdout or two os.Pipe pairs. agentkit-sandboxd serves
// the agent over it, and the local provider runs the agent in-process over
// it, so both use the production transport.
package stdioconn

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// New returns a net.Conn that reads from r and writes to w. Close closes
// both and is idempotent. Deadlines are forwarded to r and w when they
// support them (an *os.File does) and are otherwise ignored.
func New(r io.ReadCloser, w io.WriteCloser) net.Conn {
	return &conn{r: r, w: w}
}

type conn struct {
	r    io.ReadCloser
	w    io.WriteCloser
	once sync.Once
	err  error
}

func (c *conn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *conn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c *conn) Close() error {
	c.once.Do(func() { c.err = errors.Join(c.r.Close(), c.w.Close()) })
	return c.err
}

func (c *conn) LocalAddr() net.Addr  { return addr{} }
func (c *conn) RemoteAddr() net.Addr { return addr{} }

type readDeadliner interface{ SetReadDeadline(time.Time) error }
type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

func (c *conn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

func (c *conn) SetReadDeadline(t time.Time) error {
	if d, ok := c.r.(readDeadliner); ok {
		return d.SetReadDeadline(t)
	}
	return nil
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	if d, ok := c.w.(writeDeadliner); ok {
		return d.SetWriteDeadline(t)
	}
	return nil
}

// addr is the fixed address of a stdio connection.
type addr struct{}

func (addr) Network() string { return "stdio" }
func (addr) String() string  { return "stdio" }
