package agent

import (
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Serve serves the Agent service as one HTTP/2 connection on conn. It
// returns nil when the host closes the connection, and an error naming the
// TTL when the TTL expires; either way every in-flight call's context is
// cancelled, which kills the processes the call started, and Serve returns
// after those calls have. Closing conn must interrupt a blocked Read or
// Write on it.
func (a *Agent) Serve(conn net.Conn) error {
	// WaitForHandlers: Stop returns only after every cancelled call has
	// returned, so the processes a call started are gone before Serve
	// returns and a command built on it exits.
	srv := grpc.NewServer(grpc.MaxRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxSendMsgSize(sandbox.MaxMessageBytes),
		grpc.WaitForHandlers(true))
	sandboxpb.RegisterAgentServer(srv, a)
	wc := &watchedConn{Conn: conn, gone: make(chan struct{})}
	lis := &oneConnListener{conns: make(chan net.Conn, 1), done: make(chan struct{}), addr: conn.LocalAddr()}
	lis.conns <- wc
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(lis) }()
	ttl := time.NewTimer(time.Until(a.start.Add(a.ttl)))
	defer ttl.Stop()
	var err error
	select {
	case <-wc.gone:
	case <-ttl.C:
		err = fmt.Errorf("agent: TTL %v expired", a.ttl)
	case err = <-errc:
	}
	// Close the connection first: Stop does not know a connection still in
	// its handshake, and would wait for that to time out.
	_ = wc.Close()
	srv.Stop()
	return err
}

// watchedConn signals gone when a read fails or the connection is closed:
// the host has gone.
type watchedConn struct {
	net.Conn
	once sync.Once
	gone chan struct{}
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.once.Do(func() { close(c.gone) })
	}
	return n, err
}

func (c *watchedConn) Close() error {
	c.once.Do(func() { close(c.gone) })
	return c.Conn.Close()
}

// oneConnListener hands out one connection, then blocks until closed. The
// agent opens no socket.
type oneConnListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	addr  net.Addr
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *oneConnListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return l.addr }
