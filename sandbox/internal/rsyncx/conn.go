package rsyncx

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// hostStream is the host's end of an Rsync stream, as the generated client
// stream provides it.
type hostStream interface {
	Send(*sandboxpb.RsyncFrame) error
	Recv() (*sandboxpb.RsyncFrame, error)
	CloseSend() error
	Context() context.Context
}

// agentStream is the agent's end of an Rsync stream.
type agentStream interface {
	Send(*sandboxpb.RsyncFrame) error
	Recv() (*sandboxpb.RsyncFrame, error)
	Context() context.Context
}

// FrameError is a frame that arrived where only data may.
type FrameError struct {
	Kind string
}

func (e *FrameError) Error() string { return "unexpected " + e.Kind + " frame" }

// FrameKind names the kind of f: start, plan, args, data, done or empty.
func FrameKind(f *sandboxpb.RsyncFrame) string {
	switch f.GetFrame().(type) {
	case *sandboxpb.RsyncFrame_Start:
		return "start"
	case *sandboxpb.RsyncFrame_Plan:
		return "plan"
	case *sandboxpb.RsyncFrame_Args:
		return "args"
	case *sandboxpb.RsyncFrame_Data:
		return "data"
	case *sandboxpb.RsyncFrame_Done:
		return "done"
	}
	return "empty"
}

// Conn is one end of an Rsync stream as the io.ReadWriteCloser the rsync
// library reads and writes: writes leave as data frames of at most
// MaxDataChunk bytes, one whole Write at a time.
type Conn struct {
	send      func(*sandboxpb.RsyncFrame) error
	recv      func() (*sandboxpb.RsyncFrame, error)
	closeSend func() error // nil on the agent's end
	host      bool

	wmu    sync.Mutex
	sent   atomic.Int64
	closed atomic.Bool
	once   sync.Once

	rmu     sync.Mutex
	rbuf    []byte
	eof     bool
	done    *sandboxpb.RsyncDone
	failure error // the stream error or out-of-order frame a read met
}

// HostConn wraps the host's end: Close half-closes the send side, and a done
// frame ends reads with io.EOF.
func HostConn(st hostStream) *Conn {
	return &Conn{send: st.Send, recv: st.Recv, closeSend: st.CloseSend, host: true}
}

// AgentConn wraps the agent's end: Close only stops further writes, and a
// done frame is out of order.
func AgentConn(st agentStream) *Conn {
	return &Conn{send: st.Send, recv: st.Recv}
}

// Write sends p as data frames, in order, without interleaving with another
// Write.
func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed.Load() {
		return 0, errors.New("rsync stream closed")
	}
	n := 0
	for len(p) > 0 {
		chunk := append([]byte(nil), p[:min(len(p), MaxDataChunk)]...)
		if err := c.send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: chunk}}); err != nil {
			return n, err
		}
		n += len(chunk)
		c.sent.Add(int64(len(chunk)))
		p = p[len(chunk):]
	}
	return n, nil
}

// Read returns the data of received data frames. On the host a done frame
// ends the stream; any other frame kind is a *FrameError.
func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for len(c.rbuf) == 0 {
		if c.eof {
			return 0, io.EOF
		}
		f, err := c.recv()
		if err == io.EOF {
			c.eof = true
			return 0, io.EOF
		}
		if err != nil {
			c.failure = err
			return 0, err
		}
		switch fr := f.GetFrame().(type) {
		case *sandboxpb.RsyncFrame_Data:
			c.rbuf = fr.Data
		case *sandboxpb.RsyncFrame_Done:
			if !c.host {
				c.failure = &FrameError{Kind: "done"}
				return 0, c.failure
			}
			c.done, c.eof = fr.Done, true
		default:
			c.failure = &FrameError{Kind: FrameKind(f)}
			return 0, c.failure
		}
	}
	n := copy(p, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

// Close stops further writes; on the host it also half-closes the send side,
// once.
func (c *Conn) Close() error {
	c.closed.Store(true)
	var err error
	c.once.Do(func() {
		if c.closeSend != nil {
			err = c.closeSend()
		}
	})
	return err
}

// Failure is the stream error, or the *FrameError, a read met; nil when the
// stream itself was sound.
func (c *Conn) Failure() error {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	return c.failure
}

// Ended reports whether a read met the end of the stream, status OK,
// without a done frame: a malformed response, whatever the library made of
// it.
func (c *Conn) Ended() bool {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	return c.eof && c.done == nil
}

// Sent is the data bytes written to the stream.
func (c *Conn) Sent() int64 { return c.sent.Load() }

// Done is the done frame, once a host end has read it.
func (c *Conn) Done() (*sandboxpb.RsyncDone, bool) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	return c.done, c.done != nil
}
