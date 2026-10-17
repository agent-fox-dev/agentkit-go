package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

// The tests below replace os.Stdin and os.Stdout, which are process state, so
// none of them runs in parallel.

// stdio points os.Stdin and os.Stdout at pipes for the test and returns the
// client's ends: toServer writes the server's stdin, fromServer reads its
// stdout. The originals come back with the test.
func stdio(t *testing.T) (toServer, fromServer *os.File) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
	})
	return inW, outR
}

// serving starts s.ServeStdio and returns the channel its result arrives on.
func serving(ctx context.Context, s *Server) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.ServeStdio(ctx) }()
	return done
}

// echoServer is a Server over a runner with one tool, echo.
func echoServer(t *testing.T, opts Options, extra ...core.Tool) (*Server, *recorder) {
	t.Helper()
	echo := core.Tool{Name: "echo", Description: "answers with its text argument", InputSchema: schema.Object(schema.Prop("text", schema.String())),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var a struct{ Text string }
			_ = json.Unmarshal(in, &a)
			return core.OKResult(map[string]any{"echo": a.Text})
		}}
	rec := &recorder{toolRunner: runnerOf(t, append([]core.Tool{echo}, extra...)...)}
	s, err := newServer(rec, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

// within waits for done, failing the test if it takes longer than d.
func within(t *testing.T, d time.Duration, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
		return nil
	}
}

const (
	initFrame  = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`
	readyFrame = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
)

// TS-23-20 (integration): ServeStdio serves the tools to a client over
// in-process pipes. 23-REQ-4.1.
func TestServeStdioOverPipes_TS23_20(t *testing.T) {
	toServer, fromServer := stdio(t)
	s, _ := echoServer(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serving(ctx, s)

	conn, err := agentmcp.Connect(ctx, agentmcp.ServerConfig{Name: "client", Timeout: 3 * time.Second},
		agentmcp.NewPipeTransport(fromServer, toServer, wire.Defaults()), agentmcp.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := conn.ListTools(ctx)
	if err != nil || len(listed) != 1 || listed[0].Name != "echo" {
		t.Fatalf("ListTools = %v, %v; want the runner's one tool", listed, err)
	}
	out, err := conn.CallRaw(ctx, "echo", json.RawMessage(`{"text":"hello over stdio"}`))
	if err != nil || out.IsError || len(out.Content) == 0 {
		t.Fatalf("CallRaw = %+v, %v", out, err)
	}
	if text := out.Content[0].(*agentmcp.TextContent).Text; !strings.Contains(text, "hello over stdio") {
		t.Errorf("the call returned %q, want the runner's text", text)
	}
	_ = conn.Close()
	if err := within(t, 5*time.Second, done, "ServeStdio after the client left"); err != nil {
		t.Errorf("ServeStdio returned %v after a clean leave, want nil", err)
	}
}

// TS-23-21 (integration): a child process running ServeStdio is usable through
// mcp.Pool with ServerConfig.Command, and exits 0 once the pool closes.
// 23-REQ-4.1.
func TestServeStdioInAChildProcess_TS23_21(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child is this test binary re-executed with a marker variable")
	}
	exitFile := filepath.Join(t.TempDir(), "exit")
	pool := agentmcp.NewPool(agentmcp.ConnectionOptions{Warnf: func(format string, args ...any) { t.Logf("child: "+format, args...) }})
	ctx := context.Background()
	cfg := agentmcp.ServerConfig{Name: "child", Command: os.Args[0], Env: map[string]string{
		childMarker: "1", childExitFile: exitFile, "PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}}
	if _, err := pool.Connect(ctx, cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	imported, err := pool.Tools(ctx, nil)
	if err != nil || len(imported) != 1 || imported[0].Name != "child__echo" {
		t.Fatalf("Tools = %v, %v; want child__echo", imported, err)
	}
	res := imported[0].Execute(ctx, json.RawMessage(`{"text":"from the child"}`))
	if !res.OK || !strings.Contains(res.Text, "from the child") {
		t.Errorf("the call = %+v, want the runner's text", res)
	}
	if err := pool.Close(); err != nil {
		t.Logf("pool.Close: %v", err)
	}
	var outcome []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if outcome, err = os.ReadFile(exitFile); err == nil {
			break
		}
	}
	if string(outcome) != "exit 0" {
		t.Errorf("the child ended %q, want %q", outcome, "exit 0")
	}
}

// frames reads the lines fromServer writes, until it is closed, and sends each
// on the returned channel.
func frames(fromServer *os.File) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(fromServer)
		sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out
}

// TS-23-22 (integration): when the client closes its end, every reply already
// owed is written, and ServeStdio returns nil. 23-REQ-4.2.
func TestClientEOFWritesOwedReplies_TS23_22(t *testing.T) {
	toServer, fromServer := stdio(t)
	slow := core.Tool{Name: "slow", Description: "answers after a moment", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			time.Sleep(150 * time.Millisecond)
			return core.OKResult(map[string]any{"late": true})
		}}
	s, _ := echoServer(t, Options{}, slow)
	lines := frames(fromServer)
	done := serving(context.Background(), s)

	for _, f := range []string{initFrame, readyFrame, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow"}}`} {
		if _, err := io.WriteString(toServer, f+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	_ = toServer.Close()

	if err := within(t, 5*time.Second, done, "ServeStdio after EOF"); err != nil {
		t.Fatalf("ServeStdio returned %v, want nil", err)
	}
	var replied bool
	timeout := time.After(2 * time.Second)
	for !replied {
		select {
		case line := <-lines:
			var m struct {
				ID     json.RawMessage `json:"id"`
				Result struct {
					Content []struct{ Text string } `json:"content"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(line), &m) == nil && string(m.ID) == "7" {
				replied = len(m.Result.Content) > 0 && strings.Contains(m.Result.Content[0].Text, "late")
				if !replied {
					t.Fatalf("the reply for call 7 is %s", line)
				}
			}
		case <-timeout:
			t.Fatal("the reply owed to call 7 was never written")
		}
	}
}

// TS-23-23 (integration): cancelling ctx while a session is open closes it and
// returns ctx.Err(). 23-REQ-4.3.
func TestCancelClosesTheSession_TS23_23(t *testing.T) {
	toServer, fromServer := stdio(t)
	s, _ := echoServer(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serving(ctx, s)
	conn, err := agentmcp.Connect(ctx, agentmcp.ServerConfig{Name: "client", Timeout: 3 * time.Second},
		agentmcp.NewPipeTransport(fromServer, toServer, wire.Defaults()), agentmcp.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	if err := within(t, 5*time.Second, done, "ServeStdio after ctx ended"); !errors.Is(err, context.Canceled) {
		t.Errorf("ServeStdio returned %v, want context.Canceled", err)
	}
}

// TS-23-25 (integration): a frame that breaks the wire limits ends the session
// with an mcpserve error and never reaches the runner. 23-REQ-4.5.
func TestOverLimitFrameEndsTheSession_TS23_25(t *testing.T) {
	deep := strings.Repeat("[", 70) + strings.Repeat("]", 70)
	for name, c := range map[string]struct {
		frame string
		rule  wire.Rule
	}{
		"duplicate key": {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"a","text":"b"}}}`, wire.RuleDuplicateKey},
		"too deep":      {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":` + deep + `}}}`, wire.RuleDepth},
		"too long":      {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"` + strings.Repeat("a", 17<<20) + `"}}}`, wire.RuleMessageBytes},
	} {
		toServer, _ := stdio(t)
		logs := newCapture()
		s, rec := echoServer(t, Options{Logger: slog.New(logs)})
		done := serving(context.Background(), s)
		go func() {
			for _, f := range []string{initFrame, readyFrame, c.frame} {
				if _, err := io.WriteString(toServer, f+"\n"); err != nil {
					return
				}
			}
		}()
		err := within(t, 10*time.Second, done, "ServeStdio on a "+name+" frame")
		if err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") {
			t.Errorf("%s: ServeStdio returned %v, want an error starting %q", name, err, "mcpserve: ")
		}
		var we *wire.Error
		if !errors.As(err, &we) || we.Rule != c.rule {
			t.Errorf("%s: the session ended with %v, want the wire rule %q", name, err, c.rule)
		}
		if n := len(rec.seen()); n != 0 {
			t.Errorf("%s: the runner saw %d calls, want none", name, n)
		}
		var reasons []string
		for _, r := range logs.records() {
			if r.Level == slog.LevelWarn {
				reasons = append(reasons, attrsOf(r)["reason"].String())
			}
		}
		if want := "frame rejected: " + string(c.rule); len(reasons) != 1 || reasons[0] != want {
			t.Errorf("%s: Warn reasons %q, want exactly %q", name, reasons, want)
		}
	}
}

// TS-23-26 (integration): ServeStdio closes stdin, leaves stdout open, and
// writes only JSON-RPC frames. 23-REQ-4.6.
func TestStandardStreamsAfterServing_TS23_26(t *testing.T) {
	toServer, fromServer := stdio(t)
	s, _ := echoServer(t, Options{})
	lines := frames(fromServer)
	done := serving(context.Background(), s)
	for _, f := range []string{initFrame, readyFrame,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`} {
		if _, err := io.WriteString(toServer, f+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	_ = toServer.Close()
	if err := within(t, 5*time.Second, done, "ServeStdio"); err != nil {
		t.Fatalf("ServeStdio returned %v", err)
	}

	if _, err := os.Stdin.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("a read on stdin gives %v, want os.ErrClosed: ServeStdio closes stdin", err)
	}
	if _, err := os.Stdout.Write([]byte("x")); err != nil {
		t.Errorf("a write to stdout gives %v: ServeStdio leaves stdout open", err)
	}
	var got []string
	for deadline := time.After(2 * time.Second); len(got) < 3; {
		select {
		case line := <-lines:
			got = append(got, line)
		case <-deadline:
			t.Fatalf("only %d frames were written, want the three replies: %q", len(got), got)
		}
	}
	for _, line := range got {
		var m struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil || m.JSONRPC != "2.0" {
			if line != "x" {
				t.Errorf("stdout carries %q, which is not a JSON-RPC message", line)
			}
		}
	}
}

// TS-23-47 (integration): a session that ends with an error logs exactly one
// Warn record; a clean end and a cancelled ctx log none. 23-REQ-8.4.
func TestSessionErrorIsLoggedOnce_TS23_47(t *testing.T) {
	logs := newCapture()
	s, _ := echoServer(t, Options{Logger: slog.New(logs)})
	warns := func() []slog.Record {
		var out []slog.Record
		for _, r := range logs.records() {
			if r.Level >= slog.LevelWarn {
				out = append(out, r)
			}
		}
		return out
	}

	toServer, _ := stdio(t)
	done := serving(context.Background(), s)
	go func() {
		for _, f := range []string{initFrame, readyFrame, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"a":1,"a":2}}}`} {
			if _, err := io.WriteString(toServer, f+"\n"); err != nil {
				return
			}
		}
	}()
	if err := within(t, 5*time.Second, done, "ServeStdio on a bad frame"); err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") {
		t.Fatalf("ServeStdio returned %v, want an mcpserve error", err)
	}
	if w := warns(); len(w) != 1 {
		t.Fatalf("%d Warn records after a failed session, want 1", len(w))
	} else if r := attrsOf(w[0]); r["reason"].String() == "" {
		t.Errorf("the Warn record has no reason: %v", r)
	}

	toServer, _ = stdio(t)
	done = serving(context.Background(), s)
	for _, f := range []string{initFrame, readyFrame} {
		_, _ = io.WriteString(toServer, f+"\n")
	}
	_ = toServer.Close()
	if err := within(t, 5*time.Second, done, "ServeStdio on a clean end"); err != nil {
		t.Fatalf("a clean end returned %v", err)
	}

	toServer, _ = stdio(t)
	ctx, cancel := context.WithCancel(context.Background())
	done = serving(ctx, s)
	for _, f := range []string{initFrame, readyFrame} {
		_, _ = io.WriteString(toServer, f+"\n")
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := within(t, 5*time.Second, done, "ServeStdio after ctx ended"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled session returned %v", err)
	}
	if w := warns(); len(w) != 1 {
		var msgs []string
		for _, r := range w {
			msgs = append(msgs, r.Message)
		}
		t.Errorf("%d Warn records after a clean end and a cancel, want still 1: %q", len(w), msgs)
	}
}
