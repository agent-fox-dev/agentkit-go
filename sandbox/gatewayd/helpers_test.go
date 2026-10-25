package gatewayd_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

var (
	// unrestricted is the ceiling of the tests over local, which offers no other mode.
	unrestricted     = []sandbox.NetworkMode{sandbox.NetworkUnrestricted}
	unrestrictedSpec = sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}
	bothModes        = sandbox.Capabilities{Provider: "stub", Network: []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}}
	repo             = sandbox.GitRepo{URL: "https://example.test/r.git", Ref: "main", Depth: 1}
	emptyTar         = make([]byte, 1024)
)

const (
	refusalText = "gatewayd: a valid bearer token is required"
	tok40       = "0123456789abcdefghijklmnopqrstuvwxyzABCD"
)

// start serves cfg with a captured log, and the unrestricted ceiling when cfg sets none.
func start(t *testing.T, cfg gatewayd.Config) (*gwtest.Env, *gwtest.Logs) {
	t.Helper()
	logs, logger := gwtest.NewLogs()
	if cfg.NetworkModes == nil {
		cfg.NetworkModes = unrestricted
	}
	cfg.Logger = logger
	return gwtest.Start(t, cfg), logs
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// authed is a background context carrying token and the metadata pairs kv.
func authed(token string, kv ...string) context.Context {
	return gwtest.Ctx(context.Background(), token, kv...)
}

func specPB(mode sandboxpb.NetworkMode, ttl time.Duration) *sandboxpb.SandboxSpec {
	return &sandboxpb.SandboxSpec{Network: &sandboxpb.Network{Mode: mode}, TtlNanos: int64(ttl)}
}

// createReq is an unrestricted Create request with ttl.
func createReq(ttl time.Duration) *sandboxpb.CreateRequest {
	return &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, ttl)}
}

// rawCreate creates an unrestricted sandbox with a raw client and token.
func rawCreate(t *testing.T, gw sandboxpb.GatewayClient, token string) string {
	t.Helper()
	r, err := gw.Create(authed(token), createReq(0))
	if err != nil || r.GetRefusal() != nil {
		t.Fatalf("Create: %v, %v", r.GetRefusal(), err)
	}
	return r.GetId()
}

// listIDs is the ids a raw List with token answers.
func listIDs(t *testing.T, gw sandboxpb.GatewayClient, token string) []string {
	t.Helper()
	l, err := gw.List(authed(token), &sandboxpb.ListRequest{})
	must(t, err)
	var ids []string
	for _, e := range l.GetSandboxes() {
		ids = append(ids, e.GetId())
	}
	return ids
}

// create creates a sandbox through p, closed at the end of the test.
func create(t *testing.T, p *gateway.Provider, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	sb, err := p.Create(context.Background(), spec)
	must(t, err)
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// tool is sb's proxy tool name.
func tool(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	i := slices.IndexFunc(tl, func(x core.Tool) bool { return x.Name == name })
	if err != nil || i < 0 {
		t.Fatalf("no proxy tool %s: %v", name, err)
	}
	return tl[i]
}

func run(t *testing.T, sb sandbox.Sandbox, name, input string) core.ToolResult {
	t.Helper()
	return tool(t, sb, name).Execute(context.Background(), []byte(input))
}

func gone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// destroys is the reason of every 'sandbox destroyed' line logged for id.
func destroys(logs *gwtest.Logs, id string) []string {
	var out []string
	for _, m := range logs.Messages("sandbox destroyed") {
		if m["id"] == id {
			out = append(out, m["reason"].(string))
		}
	}
	return out
}

// wantRefused fails t unless err is the refusal every unauthenticated RPC gets.
func wantRefused(t *testing.T, what string, err error) {
	t.Helper()
	if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != refusalText {
		t.Errorf("%s: %v, want Unauthenticated %q", what, err, refusalText)
	}
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// authn is an Authenticator mapping tokens to principals and counting its calls.
type authn struct {
	mu     sync.Mutex
	tokens map[string]string
	calls  int
}

// newAuthn maps each token to the principal that follows it.
func newAuthn(pairs ...string) *authn {
	a := &authn{tokens: map[string]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		a.tokens[pairs[i]] = pairs[i+1]
	}
	return a
}

func (a *authn) Authenticate(_ context.Context, tok string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	p, ok := a.tokens[tok]
	return p, ok
}

func (a *authn) revoke(tok string) { a.mu.Lock(); delete(a.tokens, tok); a.mu.Unlock() }
func (a *authn) called() int       { a.mu.Lock(); defer a.mu.Unlock(); return a.calls }

// ctxRec is a backend over Counting that records the context of each sandbox's Close.
type ctxRec struct {
	*gwtest.Counting
	mu   sync.Mutex
	ctxs map[string][]context.Context
}

type ctxSandbox struct {
	sandbox.Sandbox
	b *ctxRec
}

func (b *ctxRec) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := b.Counting.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &ctxSandbox{Sandbox: sb, b: b}, nil
}

func (b *ctxRec) closes(id string) []context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.ctxs[id])
}

func (s *ctxSandbox) Unwrap() sandbox.Sandbox { return s.Sandbox }

func (s *ctxSandbox) Close(ctx context.Context) error {
	s.b.mu.Lock()
	s.b.ctxs[s.ID()] = append(s.b.ctxs[s.ID()], ctx)
	s.b.mu.Unlock()
	return s.Sandbox.Close(ctx)
}
