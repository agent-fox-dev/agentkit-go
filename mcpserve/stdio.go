package mcpserve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

// ServeStdio serves one MCP session on os.Stdin and os.Stdout, for a harness
// that starts the program as a child process. Frames are newline-delimited
// JSON, and every inbound frame is held to wire.Defaults() before the SDK
// decodes it, so a frame that breaks those limits ends the session and never
// reaches the runner. It is meant to be called once per process; that is not
// enforced.
//
// It returns nil when the client closes its end, after every reply already owed
// has been written. It returns ctx.Err() when ctx ends: the session is closed
// first, and a call still running sees a cancelled context. Any other end of the
// session returns an error starting "mcpserve: ", and is logged as a warning by
// its rule alone, since a rejected frame's text is workspace content. A ctx that
// is already done returns ctx.Err() without reading stdin.
//
// A served session closes os.Stdin when it ends and leaves os.Stdout open.
// Nothing but protocol frames is written to stdout; the program's own output
// belongs on stderr.
//
// It returns as soon as the session is closed even when stdin is a blocking
// pipe, as it is for a child process, whose pending read closing os.Stdin does
// not interrupt. That read is left to a goroutine of its own, which ends when
// the read does: with the client's next byte, the end of its input, or the
// process.
func (s *Server) ServeStdio(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only a Writer is handed over, so the transport, which closes the ends that
	// are Closers, closes stdin and leaves stdout.
	return s.serveStream(ctx, detach(os.Stdin), struct{ io.Writer }{os.Stdout})
}

// detached is a file read on a goroutine of its own, so that Close returns a
// Read still pending on it. Go interrupts a read pending on a file only when
// the file is pollable, and a stdin inherited as a blocking pipe is not, while
// the SDK's ServerSession.Close waits for its reader to return.
type detached struct {
	*io.PipeReader
	f *os.File
}

func detach(f *os.File) *detached {
	pr, pw := io.Pipe()
	go func() {
		// A nil error closes the pipe with io.EOF, the clean end of input.
		_, err := io.Copy(pw, f)
		_ = pw.CloseWithError(err)
	}()
	return &detached{PipeReader: pr, f: f}
}

// Close ends a pending Read with io.ErrClosedPipe and closes the file, which
// interrupts the copy's read only when the file is pollable; otherwise the copy
// stops at its next write.
func (d *detached) Close() error {
	_ = d.PipeReader.Close()
	return d.f.Close()
}

func (s *Server) serveStream(ctx context.Context, r io.Reader, w io.Writer) error {
	sess, err := s.open(ctx, agentmcp.NewPipeTransport(r, w, wire.Defaults()))
	if err != nil {
		return s.stdioFailed(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- sess.wait() }()
	select {
	case err := <-ended:
		_ = sess.close()
		if err == nil {
			return nil
		}
		return s.stdioFailed(err)
	case <-ctx.Done():
		_ = sess.close()
		<-ended //nolint:errcheck // waits for the session to end; ctx.Err() is the result
		return ctx.Err()
	}
}

// stdioFailed logs the end of a session and returns it as an mcpserve error.
// The log names the wire rule a frame broke, or says only that the session
// failed: the text of a wire error names the keys of the offending frame.
func (s *Server) stdioFailed(err error) error {
	reason := "session error"
	var we *wire.Error
	if errors.As(err, &we) {
		reason = "frame rejected: " + string(we.Rule)
	}
	s.log.Warn("stdio session ended with an error", "reason", reason)
	return fmt.Errorf("mcpserve: stdio session ended: %w", err)
}
