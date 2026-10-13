package attach

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

// rawAgent serves a real agent over a net.Pipe and returns a raw client
// with a 64 MiB send limit.
func rawAgent(t *testing.T, ws string) sandboxpb.AgentClient {
	t.Helper()
	a, err := agent.New(agent.Config{Workdir: ws, Env: []string{"TMPDIR=" + t.TempDir()}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	hostEnd, agentEnd := net.Pipe()
	go func() { _ = a.Serve(agentEnd) }()
	cc, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return hostEnd, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64<<20), grpc.MaxCallRecvMsgSize(64<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return sandboxpb.NewAgentClient(cc)
}

// TS-13-6: the protocol constants are 1.1 (minor 1 since spec 14) and 16 MiB, and both ends raise
// their send and receive limits to MaxMessageBytes. 13-REQ-1.6.
func TestMessageLimits_TS13_6(t *testing.T) {
	if sandbox.ProtocolMajor != 1 || sandbox.ProtocolMinor != 1 || sandbox.MaxMessageBytes != 16<<20 ||
		int64(sandbox.MaxMessageBytes) != wire.Defaults().MaxMessageBytes {
		t.Fatalf("constants %d.%d, %d", sandbox.ProtocolMajor, sandbox.ProtocolMinor, sandbox.MaxMessageBytes)
	}
	c := rawAgent(t, t.TempDir())
	ctx := context.Background()
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{DisableSpill: true}); err != nil {
		t.Fatal(err)
	}
	in := `{"path":"big.txt","content":"` + strings.Repeat("x", 15<<20) + `"}`
	if _, err := c.Call(ctx, &sandboxpb.CallRequest{Name: "write_file", Input: []byte(in)}); err != nil {
		t.Errorf("a 15 MiB call: %v, want an answer (agent receive limit above gRPC's default)", err)
	}
	if _, err := c.Call(ctx, &sandboxpb.CallRequest{Name: "write_file", Input: bytes.Repeat([]byte("x"), 17<<20)}); status.Code(err) != codes.ResourceExhausted {
		t.Errorf("a 17 MiB call: %v, want ResourceExhausted", err)
	}

	f := fakeagent.New()
	p := proxies(t, attachFake(t, f, Config{}))["read_file"]
	f.SetCall(&sandboxpb.CallResponse{Ok: true, Data: []byte(`{"k":"` + strings.Repeat("y", 10<<20) + `"}`)}, nil, false)
	if r := p.Execute(ctx, []byte(`{}`)); !r.OK {
		t.Errorf("a 10 MiB response: %s %s, want OK (host receive limit above gRPC's default)", r.Error, r.Detail)
	}
}
