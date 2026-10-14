package gatewayd_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-19-34: every RPC of both services without a valid bearer token is
// answered Unauthenticated before the backend is touched. 19-REQ-5.4,
// 19-REQ-2.6.
func TestUnauthenticated_TS19_34(t *testing.T) {
	be := gwtest.NewCounting()
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}, Logger: logger, NetworkModes: unrestricted})
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	const wrong = "wrong-token-of-sufficient-length-000000"
	rpcs := map[string]func(ctx context.Context) error{
		"Capabilities": func(ctx context.Context) error {
			_, err := gw.Capabilities(ctx, &sandboxpb.CapabilitiesRequest{})
			return err
		},
		"Create": func(ctx context.Context) error { _, err := gw.Create(ctx, &sandboxpb.CreateRequest{}); return err },
		"SyncGit": func(ctx context.Context) error {
			_, err := gw.SyncGit(ctx, &sandboxpb.SyncGitRequest{Id: "x"})
			return err
		},
		"List": func(ctx context.Context) error { _, err := gw.List(ctx, &sandboxpb.ListRequest{}); return err },
		"Destroy": func(ctx context.Context) error {
			_, err := gw.Destroy(ctx, &sandboxpb.DestroyRequest{Id: "x"})
			return err
		},
		"Reap":  func(ctx context.Context) error { _, err := gw.Reap(ctx, &sandboxpb.ReapRequest{}); return err },
		"Hello": func(ctx context.Context) error { _, err := ag.Hello(ctx, &sandboxpb.HelloRequest{}); return err },
		"Tools": func(ctx context.Context) error { _, err := ag.Tools(ctx, &sandboxpb.ToolsRequest{}); return err },
		"Call": func(ctx context.Context) error {
			_, err := ag.Call(ctx, &sandboxpb.CallRequest{Name: "read_file"})
			return err
		},
		"SyncTar":   func(ctx context.Context) error { return firstRecv(ag.SyncTar(ctx)) },
		"ExportTar": func(ctx context.Context) error { return firstRecvExport(ag.ExportTar(ctx, &sandboxpb.ExportRequest{})) },
		"Rsync":     func(ctx context.Context) error { return firstRecvRsync(ag.Rsync(ctx)) },
	}
	for _, auth := range []string{"", "Bearer " + wrong, "Basic " + gwtest.Token, "Bearer " + gwtest.Token[:31]} {
		for name, rpc := range rpcs {
			ctx := context.Background()
			if auth != "" {
				ctx = gwtest.Ctx(ctx, "", "authorization", auth)
			}
			if err := rpc(ctx); status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s with authorization %q: %v, want Unauthenticated", name, auth, err)
			}
		}
	}
	if be.Creates() != 0 || be.Closes() != 0 || be.Syncs() != 0 {
		t.Errorf("the backend was called: %d creates, %d closes, %d syncs", be.Creates(), be.Closes(), be.Syncs())
	}
	refused := logs.Messages("authentication refused")
	if len(refused) == 0 || refused[0]["peer"] == "" || refused[0]["peer"] == nil {
		t.Errorf("no refused-authentication line with a peer address: %v", refused)
	}
	if out := logs.String(); strings.Contains(out, gwtest.Token) || strings.Contains(out, wrong) {
		t.Error("the log contains a token")
	}
	_, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: wrong, Dialer: env.Listener.Dial})
	if !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial with a wrong token: %v, want ErrUnauthenticated", err)
	}
}

// firstRecv sends nothing, half-closes and reads the SyncTar answer.
func firstRecv(s sandboxpb.Agent_SyncTarClient, err error) error {
	if err != nil {
		return err
	}
	_, err = s.CloseAndRecv()
	return err
}

func firstRecvExport(s grpc.ServerStreamingClient[sandboxpb.TarChunk], err error) error {
	if err != nil {
		return err
	}
	_, err = s.Recv()
	return err
}

func firstRecvRsync(s grpc.BidiStreamingClient[sandboxpb.RsyncFrame, sandboxpb.RsyncFrame], err error) error {
	if err != nil {
		return err
	}
	if _, err = s.Recv(); err == io.EOF {
		return nil
	}
	return err
}
