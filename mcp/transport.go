package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/agentfox/agentkit-go/wire"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK owns the protocol; this file owns the three trust boundaries where
// bytes AgentKit did not write enter the process (REQ-SEC-11). The SDK's own
// decoder accepts duplicate keys (last wins) and its bounds are not ours, so
// every inbound message is held to wire's rules BEFORE the SDK decodes it:
// stdio frames here, inbound HTTP bodies in Server.HTTPHandler, and HTTP
// response bodies (JSON or SSE) in strictRoundTripper.

// strictConn is an sdk.Connection speaking newline-delimited JSON, whose
// inbound frames are bounded by wire.FrameReader and checked by wire.Guard.
//
// A rejected frame is a Read error, and the SDK closes a connection whose Read
// fails: REQ-SEC-11.4's "poisoned by the first malformed message" holds,
// because there is no safe place in a stream with untrustworthy framing to
// resume from.
type strictConn struct {
	frames *wire.FrameReader
	limits wire.Limits
	w      io.Writer
	close  func() error

	writeMu sync.Mutex
	once    sync.Once
	err     error
}

func newStrictConn(r io.Reader, w io.Writer, limits wire.Limits, closeFn func() error) *strictConn {
	return &strictConn{frames: wire.NewNDJSON(r, limits), limits: limits, w: w, close: closeFn}
}

func (c *strictConn) Read(context.Context) (jsonrpc.Message, error) {
	frame, err := c.frames.Next()
	if err != nil {
		return nil, err
	}
	if err := wire.Guard(frame, c.limits); err != nil {
		return nil, err
	}
	return jsonrpc.DecodeMessage(frame)
}

// Write honours ctx even when the peer has stopped draining its pipe: the
// write runs on its own goroutine, so REQ-MCP-CLIENT-07's timeout_s is a
// timeout on returning and not only on the response. An abandoned write keeps
// the lock until it completes or Close unblocks it, so frames never interleave.
func (c *strictConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		_, err := c.w.Write(append(data, '\n'))
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *strictConn) Close() error {
	c.once.Do(func() { c.err = c.close() })
	return c.err
}

func (c *strictConn) SessionID() string { return "" }

// connTransport hands the SDK one already-built connection.
type connTransport struct{ conn sdk.Connection }

func (t connTransport) Connect(context.Context) (sdk.Connection, error) { return t.conn, nil }

// NewPipeTransport speaks strict NDJSON over a reader/writer pair: the server's
// stdio mode, and an in-process client/server pair with no subprocess and no
// port. Close closes both ends that are io.Closers, the reader first, because
// that is what unblocks a read loop waiting on it.
func NewPipeTransport(r io.Reader, w io.Writer, limits wire.Limits) sdk.Transport {
	return connTransport{newStrictConn(r, w, limits, func() error {
		var err error
		for _, x := range []any{r, w} {
			if c, ok := x.(io.Closer); ok {
				err = errors.Join(err, c.Close())
			}
		}
		return err
	})}
}

// maxStderrLine bounds one stderr line delivered to the Warnf hook.
const maxStderrLine = 1 << 20

// commandTransport runs an MCP server as a subprocess.
//
// It replaces the SDK's CommandTransport for three reasons: the child gets
// exactly Env and never the parent's environment (REQ-MCP-CLIENT-10); it runs
// in its own process group and Close kills the GROUP, so helpers it spawned do
// not outlive it holding the pipe; and its frames go through strictConn.
type commandTransport struct {
	command string
	args    []string
	dir     string
	env     []string
	stderr  func(line string)
	limits  wire.Limits
}

func (t *commandTransport) Connect(context.Context) (sdk.Connection, error) {
	cmd := exec.Command(t.command, t.args...)
	cmd.Dir = t.dir
	// An explicit empty slice, never nil: exec treats a nil Env as "inherit
	// the parent's", which is the one behaviour REQ-MCP-CLIENT-10 forbids.
	cmd.Env = append([]string{}, t.env...)
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// stdout and stderr are OUR pipes, not cmd.StdoutPipe's: Wait closes the
	// pipes exec created, and the reaper calls Wait the moment the process
	// exits — so a server that wrote its last response and exited could have
	// that frame discarded before the reader got to it.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_, _ = stdoutR.Close(), stdoutW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdoutW, stderrW
	err = cmd.Start()
	// The child holds the write ends now. Ours must go, or the reader never
	// sees EOF — it would be waiting on a writer that is this very process.
	_, _ = stdoutW.Close(), stderrW.Close()
	if err != nil {
		_, _ = stdoutR.Close(), stderrR.Close()
		return nil, fmt.Errorf("mcp: starting %q: %w", t.command, err)
	}
	go pumpStderr(stderrR, t.stderr)
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	return newStrictConn(stdoutR, stdin, t.limits, func() error {
		_ = stdin.Close()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			// A server that will not exit on a closed stdin is killed, group
			// and all; the bounded wait is what stops Close from hanging.
			killGroup(cmd)
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
			}
		}
		// Only now, after the process is reaped: closing the read end earlier
		// is the frame-losing race the private pipe exists to avoid.
		return stdoutR.Close()
	}), nil
}

// pumpStderr delivers the child's stderr line by line. A line too long to
// buffer does not stop the READING: the child still holds the write end, and
// once the pipe fills, a server that logs is a server that hangs. The rest is
// drained and discarded, and the caller is told once why.
func pumpStderr(r *os.File, deliver func(string)) {
	defer r.Close()
	if deliver == nil {
		deliver = func(string) {}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxStderrLine)
	for sc.Scan() {
		deliver(sc.Text())
	}
	if err := sc.Err(); err != nil {
		deliver(fmt.Sprintf("[agentkit] stderr line exceeded %d bytes (%v); the rest of "+
			"this server's stderr is discarded", maxStderrLine, err))
		_, _ = io.Copy(io.Discard, r)
	}
}

// strictRoundTripper is the client side of the HTTP boundary: it adds the
// configured headers to every request and holds every response body — a JSON
// answer or each SSE event's data — to wire's rules before the SDK reads it.
type strictRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	limits  wire.Limits
}

func (t *strictRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	max := t.limits.WithDefaults().MaxMessageBytes
	switch ct := resp.Header.Get("Content-Type"); {
	case strings.HasPrefix(ct, "application/json"):
		body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
		_ = resp.Body.Close()
		if err == nil && int64(len(body)) > max {
			err = fmt.Errorf("response body exceeds %d bytes", max)
		}
		if err == nil {
			err = wire.Guard(body, t.limits)
		}
		if err != nil {
			return nil, fmt.Errorf("mcp: response rejected: %w", err)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
	case strings.HasPrefix(ct, "text/event-stream"):
		resp.Body = &sseGuard{src: bufio.NewReader(resp.Body), body: resp.Body,
			limits: t.limits, max: max + 64<<10}
	}
	return resp, nil
}

// sseGuard passes an event stream through unchanged, but releases each event
// only once its joined data lines pass wire.Guard. An event longer than the
// message bound is an error before it is buffered whole.
type sseGuard struct {
	src    *bufio.Reader
	body   io.Closer
	limits wire.Limits
	max    int64

	event, data, out []byte
	err              error
}

func (g *sseGuard) Read(p []byte) (int, error) {
	for len(g.out) == 0 && g.err == nil {
		g.err = g.next()
	}
	if len(g.out) == 0 {
		return 0, g.err
	}
	n := copy(p, g.out)
	g.out = g.out[n:]
	return n, nil
}

func (g *sseGuard) Close() error { return g.body.Close() }

// next consumes one line. A blank line, or the end of the stream, completes
// the event.
func (g *sseGuard) next() error {
	var line []byte
	for {
		chunk, err := g.src.ReadSlice('\n')
		line = append(line, chunk...)
		if int64(len(g.event)+len(line)) > g.max {
			return fmt.Errorf("mcp: server-sent event exceeds %d bytes", g.max)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			g.event = append(g.event, line...)
			g.addData(line)
			if rerr := g.release(); rerr != nil {
				return rerr
			}
			return err
		}
		break
	}
	g.event = append(g.event, line...)
	if len(bytes.TrimRight(line, "\r\n")) == 0 {
		return g.release()
	}
	g.addData(line)
	return nil
}

func (g *sseGuard) addData(line []byte) {
	v, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("data:"))
	if !ok {
		return
	}
	if g.data != nil {
		g.data = append(g.data, '\n')
	}
	g.data = append(g.data, bytes.TrimPrefix(v, []byte(" "))...)
}

func (g *sseGuard) release() error {
	if len(g.data) > 0 {
		if err := wire.Guard(g.data, g.limits); err != nil {
			return fmt.Errorf("mcp: server-sent event rejected: %w", err)
		}
	}
	g.out, g.event, g.data = g.event, nil, nil
	return nil
}

// isHeaderSafe reports whether s is safe to place in an HTTP header: no
// control bytes. A control byte in a header is a request-splitting attempt,
// and it can arrive through an interpolated secret.
func isHeaderSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return false
		}
	}
	return s != ""
}

// interpolate expands ${VAR} and $VAR against a lookup (REQ-MCP-CLIENT-07).
//
// The lookup reports PRESENCE as well as value, and only an ABSENT variable
// is returned as missing. A variable deliberately set to the empty string is
// a value the operator chose, not a reference that failed to resolve — and
// NFR-SEC-03 makes the latter a configuration error, so the two must not be
// confused. The literal `${VAR}` is never handed to the child either way.
func interpolate(s string, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte('$')
			break
		}
		if s[i+1] == '$' {
			b.WriteByte('$') // $$ is a literal dollar
			i += 2
			continue
		}
		name, next := "", 0
		if s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			name, next = s[i+2:i+2+end], i+3+end
		} else {
			j := i + 1
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			if j == i+1 {
				b.WriteByte('$')
				i++
				continue
			}
			name, next = s[i+1:j], j
		}
		v, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
		}
		b.WriteString(v)
		i = next
	}
	return b.String(), missing
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
