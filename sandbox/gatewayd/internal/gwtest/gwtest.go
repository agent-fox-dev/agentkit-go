// Package gwtest runs a gateway in the test process: an in-memory listener,
// a server started with gatewayd.Config, gateway clients and raw gRPC
// clients dialled to it, a captured log, and test backends. Only tests
// import it.
package gwtest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Token and TokenB are valid gateway tokens.
const (
	Token  = "gwtest-token-A-0123456789abcdefghijklmnopqrstuv"
	TokenB = "gwtest-token-B-0123456789abcdefghijklmnopqrstuv"
)

// URL is the URL clients dial; the Listener's Dial ignores the address.
const URL = "http://127.0.0.1"

// Listener is an in-memory net.Listener: Dial hands the server one end of a
// net.Pipe and returns the other.
type Listener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// NewListener returns an open Listener.
func NewListener() *Listener {
	return &Listener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *Listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *Listener) Addr() net.Addr { return memAddr{} }

// Dial connects to the listener; it fails once the listener is closed.
func (l *Listener) Dial(ctx context.Context, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "gwtest" }

// Env is a gateway serving on an in-memory listener.
type Env struct {
	Server   *gatewayd.Server
	Listener *Listener
	// Served receives Serve's result.
	Served chan error
}

// Start serves a gateway with cfg; Tokens defaults to [Token, TokenB]. The
// server is shut down at the end of the test.
func Start(t *testing.T, cfg gatewayd.Config) *Env {
	t.Helper()
	if cfg.Tokens == nil {
		cfg.Tokens = []string{Token, TokenB}
	}
	srv, err := gatewayd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{Server: srv, Listener: NewListener(), Served: make(chan error, 1)}
	go func() { e.Served <- srv.Serve(e.Listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return e
}

// Dial is a gateway client of the server with token, closed at the end of
// the test.
func (e *Env) Dial(t *testing.T, token string) *gateway.Provider {
	t.Helper()
	p, err := gateway.Dial(context.Background(), URL, gateway.Options{Token: token, Dialer: e.Listener.Dial})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// Raw is a gRPC connection to the server with no credentials; Ctx adds them
// per call.
func (e *Env) Raw(t *testing.T) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///gwtest",
		grpc.WithContextDialer(e.Listener.Dial),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxCallSendMsgSize(sandbox.MaxMessageBytes)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

// RawGateway is a Gateway client over Raw.
func (e *Env) RawGateway(t *testing.T) sandboxpb.GatewayClient {
	return sandboxpb.NewGatewayClient(e.Raw(t))
}

// Ctx is ctx with `authorization: Bearer <token>` (none when token is
// empty) and the metadata pairs kv.
func Ctx(ctx context.Context, token string, kv ...string) context.Context {
	if token != "" {
		kv = append([]string{"authorization", "Bearer " + token}, kv...)
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// Logs is a captured JSON log.
type Logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// NewLogs returns a capture and a logger writing to it.
func NewLogs() (*Logs, *slog.Logger) {
	l := &Logs{}
	return l, slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *Logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// String is everything logged so far.
func (l *Logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Lines is every line logged so far, decoded.
func (l *Logs) Lines() []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// Messages is the lines whose msg is msg.
func (l *Logs) Messages(msg string) []map[string]any {
	var out []map[string]any
	for _, m := range l.Lines() {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}
