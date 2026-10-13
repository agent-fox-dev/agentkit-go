package attach

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// emptyTar is a tar stream with no entries.
var emptyTar = make([]byte, 1024)

// attachFake serves f and attaches a sandbox to it with cfg; the TTL
// defaults to an hour.
func attachFake(t *testing.T, f *fakeagent.Agent, cfg Config) sandbox.Sandbox {
	t.Helper()
	if cfg.Spec.TTL == 0 {
		cfg.Spec.TTL = time.Hour
	}
	sb, err := Dial(context.Background(), f.Serve(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()); f.Stop() })
	return sb
}

// proxies returns sb's proxy tools by name.
func proxies(t *testing.T, sb sandbox.Sandbox) map[string]core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]core.Tool{}
	for _, p := range tl {
		out[p.Name] = p
	}
	return out
}

// TS-13-28: attach dials the given connection exactly once, never redials,
// and forwards per-call outgoing metadata. 13-REQ-5.1.
func TestOneShotDialAndMetadata_TS13_28(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	d := oneShot(a)
	c1, err1 := d(context.Background(), "")
	c2, err2 := d(context.Background(), "")
	if c1 != a || err1 != nil || c2 != nil || err2 == nil {
		t.Fatalf("dialer gave (%v, %v) then (%v, %v); want the conn once, then an error", c1, err1, c2, err2)
	}

	f := fakeagent.New()
	sb := attachFake(t, f, Config{Metadata: metadata.Pairs("x-agentkit-test", "1")})
	p := proxies(t, sb)["read_file"]
	if r := p.Execute(context.Background(), []byte(`{}`)); !r.OK {
		t.Fatalf("call: %+v", r)
	}
	for _, rpc := range []string{"Hello", "Call"} {
		if got := f.Metadata(rpc).Get("x-agentkit-test"); !slices.Equal(got, []string{"1"}) {
			t.Errorf("%s saw x-agentkit-test = %v", rpc, got)
		}
	}
	f.Stop()
	if r := p.Execute(context.Background(), []byte(`{}`)); r.OK {
		t.Error("a call after the agent stopped succeeded")
	}
	if n := f.Conns(); n != 1 {
		t.Errorf("the fake accepted %d connections, want 1", n)
	}
}

// TS-13-29: a different protocol major is refused with ErrProtocol naming
// both versions, and the destroy step runs. 13-REQ-5.2.
func TestProtocolMajorRefused_TS13_29(t *testing.T) {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 2, ProtocolMinor: 0})
	defer f.Stop()
	calls := 0
	sb, err := Dial(context.Background(), f.Serve(), Config{Spec: sandbox.Spec{TTL: time.Hour},
		Destroy: func(context.Context) error { calls++; return nil }})
	host := fmt.Sprintf("%d.%d", sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	if sb != nil || !errors.Is(err, sandbox.ErrProtocol) || !strings.Contains(err.Error(), "2.0") || !strings.Contains(err.Error(), host) {
		t.Fatalf("Dial = %v, %v; want nil and ErrProtocol naming 2.0 and %s", sb, err, host)
	}
	if calls != 1 {
		t.Errorf("destroy ran %d times, want 1", calls)
	}
	select {
	case <-f.Closed():
	case <-time.After(2 * time.Second):
		t.Error("the client connection was not closed")
	}
}

// TS-13-30: any minor of the same major is accepted, Spec returns the
// effective spec with the reported workdir, and Agent returns the Hello
// facts. 13-REQ-5.3.
func TestHandshakeAccepted_TS13_30(t *testing.T) {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: 7, Version: "v9.0.0", Workdir: "/real/ws", OutlineBackend: "none", Git: true})
	caps := sandbox.Capabilities{Provider: "fake", Network: []sandbox.NetworkMode{sandbox.NetworkUnrestricted}}
	spec, err := sandbox.CheckSpec(caps, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	sb := attachFake(t, f, Config{Spec: spec})
	if sb.Spec().TTL != time.Hour || sb.Spec().Workdir != "/real/ws" {
		t.Errorf("Spec() = %+v", sb.Spec())
	}
	want := sandbox.AgentInfo{Version: "v9.0.0", ProtocolMajor: 1, ProtocolMinor: 7, Workdir: "/real/ws", OutlineBackend: "none", Git: true}
	if sb.Agent() != want {
		t.Errorf("Agent() = %+v, want %+v", sb.Agent(), want)
	}
}

// TS-13-31: deliberate refusals leave the sandbox live, Unimplemented names
// the agent version, and any other status makes it unavailable.
// 13-REQ-5.4.
func TestFailureClassification_TS13_31(t *testing.T) {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: 3, Version: "v1"})
	sb := attachFake(t, f, Config{})
	ctx, o := context.Background(), sandbox.ToolOptions{}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.Unimplemented} {
		f.SetTools(nil, status.Error(code, "x"))
		n := f.Count("Tools")
		_, err := sb.Tools(ctx, o)
		if err == nil || errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%v: %v, want a refusal that leaves the sandbox live", code, err)
		}
		if code == codes.Unimplemented && !strings.Contains(err.Error(), "1.3") {
			t.Errorf("Unimplemented error %q does not name the agent's protocol 1.3", err)
		}
		_, _ = sb.Tools(ctx, o)
		if f.Count("Tools") != n+2 {
			t.Errorf("%v: the second Tools call did not reach the agent", code)
		}
	}
	f.SetTools(nil, status.Error(codes.Internal, "x"))
	if _, err := sb.Tools(ctx, o); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("Internal: %v, want ErrUnavailable", err)
	}
	n := f.Count("Tools")
	if _, err := sb.Tools(ctx, o); !errors.Is(err, sandbox.ErrUnavailable) || f.Count("Tools") != n {
		t.Errorf("after a transport failure Tools returned %v and made %d RPCs", err, f.Count("Tools")-n)
	}
}

// TS-13-34: Close runs the destroy step once, returns its error first,
// cancels the TTL timer, and later Closes return nil. 13-REQ-5.7.
func TestCloseIdempotent_TS13_34(t *testing.T) {
	f := fakeagent.New()
	defer f.Stop()
	errBoom := errors.New("boom")
	calls := 0
	sb, err := Dial(context.Background(), f.Serve(), Config{Spec: sandbox.Spec{TTL: 300 * time.Millisecond},
		Destroy: func(context.Context) error { calls++; return errBoom }})
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("first Close = %v, want errBoom", err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	time.Sleep(600 * time.Millisecond)
	if calls != 1 {
		t.Errorf("destroy ran %d times, want 1", calls)
	}
	select {
	case <-f.Closed():
	case <-time.After(2 * time.Second):
		t.Error("the client connection was not closed")
	}
}

// TS-13-35: Tools, Sync and Export on an unavailable or closed sandbox
// return ErrUnavailable or ErrClosed without an RPC. 13-REQ-5.8.
func TestGatedWhenUnavailableOrClosed_TS13_35(t *testing.T) {
	ctx := context.Background()
	fu, fc := fakeagent.New(), fakeagent.New()
	unavail, closed := attachFake(t, fu, Config{}), attachFake(t, fc, Config{})
	fu.SetCall(nil, status.Error(codes.Internal, "broken"), false)
	if r := proxies(t, unavail)["read_file"].Execute(ctx, []byte(`{}`)); r.Error != "sandbox_unavailable" {
		t.Fatalf("the Internal call returned %+v", r)
	}
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	counts := func(f *fakeagent.Agent) [3]int {
		return [3]int{f.Count("Tools"), f.Count("SyncTar"), f.Count("ExportTar")}
	}
	beforeU, beforeC := counts(fu), counts(fc)
	for _, c := range []struct {
		sb   sandbox.Sandbox
		want error
	}{{unavail, sandbox.ErrUnavailable}, {closed, sandbox.ErrClosed}} {
		_, err1 := c.sb.Tools(ctx, sandbox.ToolOptions{})
		_, err2 := c.sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)})
		err3 := c.sb.Export(ctx, "", sandbox.TarOut{W: &bytes.Buffer{}})
		for i, err := range []error{err1, err2, err3} {
			if !errors.Is(err, c.want) {
				t.Errorf("operation %d returned %v, want %v", i, err, c.want)
			}
		}
	}
	if counts(fu) != beforeU || counts(fc) != beforeC {
		t.Errorf("gated operations made RPCs: %v -> %v, %v -> %v", beforeU, counts(fu), beforeC, counts(fc))
	}
}
