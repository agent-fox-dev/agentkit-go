package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// testEnv is an explicit agent environment: the host's PATH and a private
// HOME and TMPDIR.
func testEnv(t *testing.T) []string {
	t.Helper()
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
}

// newAgent builds an agent over dir (a fresh temp dir when empty).
func newAgent(t *testing.T, dir string, env []string, ttl time.Duration) *Agent {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	a, err := New(Config{Workdir: dir, Env: env, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// dial serves a over a net.Pipe and returns a client connection to it and
// the channel Serve's result arrives on.
func dial(t *testing.T, a *Agent) (*grpc.ClientConn, <-chan error) {
	t.Helper()
	hostEnd, agentEnd := net.Pipe()
	errc := make(chan error, 1)
	go func() { errc <- a.Serve(agentEnd) }()
	cc, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return hostEnd, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20), grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc, errc
}

// serveRaw is dial's connection as an Agent client.
func serveRaw(t *testing.T, a *Agent) (sandboxpb.AgentClient, <-chan error) {
	t.Helper()
	cc, errc := dial(t, a)
	return sandboxpb.NewAgentClient(cc), errc
}

// configured serves an agent over ws (a fresh one when empty) with its tool
// set built.
func configured(t *testing.T, ws string) sandboxpb.AgentClient {
	t.Helper()
	c, _ := serveRaw(t, newAgent(t, ws, testEnv(t), time.Hour))
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// call runs one tool call and fails the test on an RPC error.
func call(t *testing.T, c sandboxpb.AgentClient, name, input string) *sandboxpb.CallResponse {
	t.Helper()
	r, err := c.Call(context.Background(), &sandboxpb.CallRequest{Name: name, Input: []byte(input)})
	if err != nil {
		t.Fatalf("Call %s: %v", name, err)
	}
	return r
}

// stub is a tool named name that runs run.
func stub(name string, run func(context.Context) core.ToolResult) core.Tool {
	return core.Tool{Name: name, Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult { return run(ctx) }}
}

// waitLine waits for path to hold a full line and returns it.
func waitLine(t *testing.T, path string) string {
	t.Helper()
	var line string
	testutil.Eventually(t, path+" to hold a line", func() bool {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 || b[len(b)-1] != '\n' {
			return false
		}
		line = string(b[:len(b)-1])
		return true
	})
	return line
}
