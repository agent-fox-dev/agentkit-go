package attach

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Dial uses the connection once and never redials, and every RPC carries
// the configured metadata.
func TestDialUsesTheConnectionOnceAndSendsTheMetadata(t *testing.T) {
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
	p := proxies(t, attachFake(t, f, Config{Metadata: metadata.Pairs("x-agentkit-test", "1")}))["read_file"]
	if r := p.Execute(context.Background(), []byte(`{}`)); !r.OK {
		t.Fatalf("call: %+v", r)
	}
	for _, rpc := range []string{"Hello", "Tools", "Call"} {
		if got := f.Metadata(rpc).Get("x-agentkit-test"); !slices.Equal(got, []string{"1"}) {
			t.Errorf("%s saw x-agentkit-test = %v", rpc, got)
		}
	}
	f.Stop()
	if r := p.Execute(context.Background(), []byte(`{}`)); r.OK || f.Conns() != 1 {
		t.Errorf("after the agent stopped: %+v and %d connections; want a failure and no redial", r, f.Conns())
	}
}

// A refused attach closes the client and runs the destroy step once.
func refusedDial(t *testing.T, f *fakeagent.Agent, spec sandbox.Spec) error {
	t.Helper()
	destroyed := 0
	sb, err := Dial(context.Background(), f.Serve(), Config{Provider: "fake", Spec: spec,
		Destroy: func(context.Context) error { destroyed++; return nil }})
	t.Cleanup(f.Stop)
	if sb != nil || err == nil || destroyed != 1 {
		t.Errorf("Dial = %v, %v with %d destroys; want nil, an error and one destroy", sb, err, destroyed)
	}
	select {
	case <-f.Closed():
	case <-time.After(5 * time.Second):
		t.Error("the client connection was not closed")
	}
	return err
}

func TestDialRefusesAnotherProtocolMajor(t *testing.T) {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 2})
	err := refusedDial(t, f, sandbox.Spec{TTL: time.Hour})
	host := fmt.Sprintf("%d.%d", sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	if !errors.Is(err, sandbox.ErrProtocol) || !strings.Contains(err.Error(), "2.0") || !strings.Contains(err.Error(), host) {
		t.Errorf("Dial = %v; want ErrProtocol naming 2.0 and %s", err, host)
	}
}

// Any minor of the major is accepted; Spec carries the agent's workdir and
// Agent the Hello facts, with a protocol note for an agent that reports no
// Landlock ruleset.
func TestDialReportsTheAgent(t *testing.T) {
	for _, c := range []struct {
		minor uint32
		r     *sandboxpb.LandlockReport
		want  sandbox.LandlockInfo
	}{
		{7, nil, sandbox.LandlockInfo{Note: "agent protocol 1.7 does not report Landlock"}},
		{1, &sandboxpb.LandlockReport{Abi: 3}, sandbox.LandlockInfo{Note: "agent protocol 1.1 does not report Landlock"}},
		{0, nil, sandbox.LandlockInfo{Note: "agent protocol 1.0 does not report Landlock"}},
		{2, &sandboxpb.LandlockReport{Abi: 3, KernelAbi: 5, Tcp: true, Note: "n", Writable: []string{"/w"}, ReadOnly: []string{"/usr"}},
			sandbox.LandlockInfo{ABI: 3, KernelABI: 5, TCP: true, Note: "n", Writable: []string{"/w"}, ReadOnly: []string{"/usr"}}},
	} {
		f := fakeagent.New()
		f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: c.minor, Version: "v9", Workdir: "/real/ws",
			OutlineBackend: "none", Git: true, Landlock: c.r})
		sb := attachFake(t, f, Config{Spec: sandbox.Spec{TTL: 2 * time.Hour}})
		want := sandbox.AgentInfo{Version: "v9", ProtocolMajor: 1, ProtocolMinor: c.minor, Workdir: "/real/ws", OutlineBackend: "none", Git: true, Landlock: c.want}
		if !reflect.DeepEqual(sb.Agent(), want) || sb.Spec().Workdir != "/real/ws" || sb.Spec().TTL != 2*time.Hour {
			t.Errorf("1.%d: Agent() = %+v, Spec() = %+v; want %+v", c.minor, sb.Agent(), sb.Spec(), want)
		}
	}
}

// RequireLandlock is refused, naming the report, unless the agent's ruleset
// satisfies the network mode.
func TestDialChecksRequireLandlockAgainstTheReport(t *testing.T) {
	req := func(m sandbox.NetworkMode) sandbox.Spec {
		return sandbox.Spec{RequireLandlock: true, Network: sandbox.Network{Mode: m}, TTL: time.Hour}
	}
	for _, c := range []struct {
		minor uint32
		r     *sandboxpb.LandlockReport
		mode  sandbox.NetworkMode
		want  []string
	}{
		{2, &sandboxpb.LandlockReport{Abi: 0, KernelAbi: 1, Note: "not applied"}, sandbox.NetworkNone, []string{"ABI 0", "kernel ABI 1", "TCP false", "not applied"}},
		{2, &sandboxpb.LandlockReport{Abi: 4, KernelAbi: 6, Note: "x"}, sandbox.NetworkNone, []string{"ABI 4", "kernel ABI 6", "TCP false", "note: x"}},
		{1, nil, sandbox.NetworkUnrestricted, []string{"1.1"}},
	} {
		f := fakeagent.New()
		f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: c.minor, Landlock: c.r})
		err := refusedDial(t, f, req(c.mode))
		var se *sandbox.SpecError
		if !errors.Is(err, sandbox.ErrUnsupported) || !errors.As(err, &se) || se.Provider != "fake" || se.Field != "RequireLandlock" {
			t.Fatalf("Dial = %v; want a RequireLandlock SpecError from fake", err)
		}
		for _, w := range c.want {
			if !strings.Contains(se.Reason, w) {
				t.Errorf("reason %q lacks %q", se.Reason, w)
			}
		}
	}
	for _, c := range []struct {
		r    *sandboxpb.LandlockReport
		mode sandbox.NetworkMode
	}{
		{&sandboxpb.LandlockReport{Abi: 3}, sandbox.NetworkUnrestricted},
		{&sandboxpb.LandlockReport{Abi: 4, KernelAbi: 6, Tcp: true}, sandbox.NetworkNone},
		{&sandboxpb.LandlockReport{Abi: 2}, sandbox.NetworkAllowlist},
	} {
		f := fakeagent.New()
		f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: 2, Landlock: c.r})
		if _, err := attachFake(t, f, Config{Spec: req(c.mode)}).Tools(context.Background(), sandbox.ToolOptions{}); err != nil {
			t.Errorf("%v with %v: %v", c.mode, c.r, err)
		}
	}
}

// Close tears down once, returning the destroy step's error first, and stops
// the TTL timer.
func TestCloseTearsDownOnce(t *testing.T) {
	f := fakeagent.New()
	defer f.Stop()
	boom, calls := errors.New("boom"), 0
	box, err := Dial(context.Background(), f.Serve(), Config{Spec: sandbox.Spec{TTL: time.Hour},
		Destroy: func(context.Context) error { calls++; return boom }})
	if err != nil {
		t.Fatal(err)
	}
	if err := box.Close(context.Background()); !errors.Is(err, boom) {
		t.Errorf("first Close = %v, want boom", err)
	}
	if err := box.Close(context.Background()); err != nil || calls != 1 {
		t.Errorf("second Close = %v after %d destroys, want nil after 1", err, calls)
	}
	if box.(*sb).timer.Load().Stop() {
		t.Error("the TTL timer was still running after Close")
	}
}

// At the TTL the sandbox becomes unavailable naming it and is torn down;
// when the agent connection ends it becomes unavailable too.
func TestSandboxBecomesUnavailableAtTheTTLOrWhenTheAgentGoes(t *testing.T) {
	f := fakeagent.New()
	destroyed := make(chan struct{})
	ttl := attachFake(t, f, Config{Spec: sandbox.Spec{TTL: 50 * time.Millisecond},
		Destroy: func(context.Context) error { close(destroyed); return nil }})
	<-destroyed
	if err := Live(ttl); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "TTL 50ms expired") {
		t.Errorf("Live after the TTL = %v", err)
	}
	g := fakeagent.New()
	gone := attachFake(t, g, Config{})
	g.Stop()
	testutil.Eventually(t, "the sandbox to notice the agent went", func() bool { return errors.Is(Live(gone), sandbox.ErrUnavailable) })
}

// Both ends raise the message limits to MaxMessageBytes: a 15 MiB call and a
// 10 MiB answer go through, a 17 MiB call is refused.
func TestMessagesUpToTheLimitGoThrough(t *testing.T) {
	sb, _ := real(t)
	ctx := context.Background()
	in := `{"path":"big.txt","content":"` + strings.Repeat("x", 15<<20) + `"}`
	if r := proxies(t, sb)["write_file"].Execute(ctx, []byte(in)); !r.OK {
		t.Errorf("a 15 MiB call: %s %s", r.Error, r.Detail)
	}
	c := sandboxpb.NewAgentClient(ClientConn(sb))
	if _, err := c.Call(ctx, &sandboxpb.CallRequest{Name: "write_file", Input: bytes.Repeat([]byte("x"), 17<<20)}); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("a 17 MiB call: %v, want ResourceExhausted", err)
	}
	f := fakeagent.New()
	p := proxies(t, attachFake(t, f, Config{}))["read_file"]
	f.SetCall(&sandboxpb.CallResponse{Ok: true, Data: []byte(`{"k":"` + strings.Repeat("y", 10<<20) + `"}`)}, nil, false)
	if r := p.Execute(ctx, []byte(`{}`)); !r.OK {
		t.Errorf("a 10 MiB response: %s %s", r.Error, r.Detail)
	}
}

type wrapper struct{ sandbox.Sandbox }

func (w wrapper) Unwrap() sandbox.Sandbox { return w.Sandbox }

// plain is a sandbox attach did not create, with no Unwrap.
type plain struct{ sandbox.Sandbox }

type cyclic struct{ plain }

func (c *cyclic) Unwrap() sandbox.Sandbox { return c }

// ClientConn, Outgoing, Live and Fail follow an Unwrap chain to the sandbox
// attach created, and leave anything else alone.
func TestHelpersFollowTheUnwrapChain(t *testing.T) {
	md := metadata.Pairs("agentkit-sandbox-id", "box-7")
	sb := attachFake(t, fakeagent.New(), Config{Metadata: md})
	base := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("stale", "1"))
	w2 := wrapper{wrapper{sb}}
	if cc := ClientConn(w2); cc == nil || cc != ClientConn(sb) || Live(w2) != nil {
		t.Fatalf("ClientConn(w2) = %v, Live(w2) = %v", cc, Live(w2))
	}
	if got, _ := metadata.FromOutgoingContext(Outgoing(base, w2)); len(got) != 1 || got.Get("agentkit-sandbox-id")[0] != "box-7" {
		t.Errorf("Outgoing metadata = %v, want only the sandbox's", got)
	}
	bare := attachFake(t, fakeagent.New(), Config{})
	for name, s := range map[string]sandbox.Sandbox{"no metadata": bare, "not attached": plain{}, "cyclic": &cyclic{}, "wraps nil": wrapper{}} {
		if Outgoing(base, s) != base || (s != bare && (ClientConn(s) != nil || Live(s) == nil)) {
			t.Errorf("%s: Outgoing, ClientConn or Live did not leave it alone", name)
		}
		if s != bare {
			Fail(s, "ignored")
		}
	}
	Fail(w2, "provider says so")
	if err := Live(sb); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "provider says so") {
		t.Errorf("Live after Fail = %v", err)
	}
	if r := proxies(t, bare)["read_file"].Execute(context.Background(), nil); r.Error == "sandbox_unavailable" {
		t.Error("Fail of another sandbox reached bare")
	}
}
