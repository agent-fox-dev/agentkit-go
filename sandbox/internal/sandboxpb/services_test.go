package sandboxpb

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type agent struct{ UnimplementedAgentServer }

func (agent) Hello(context.Context, *HelloRequest) (*HelloResponse, error) {
	return &HelloResponse{ProtocolMajor: 1, Version: "v"}, nil
}

// Rsync echoes each data frame back until the client half-closes, then sends done.
func (agent) Rsync(st grpc.BidiStreamingServer[RsyncFrame, RsyncFrame]) error {
	n := int64(0)
	for {
		f, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return st.Send(&RsyncFrame{Frame: &RsyncFrame_Done{Done: &RsyncDone{Files: n}}})
		}
		if err != nil {
			return err
		}
		n++
		if err := st.Send(f); err != nil {
			return err
		}
	}
}

type gateway struct{ UnimplementedGatewayServer }

func (gateway) Capabilities(context.Context, *CapabilitiesRequest) (*CapabilitiesResponse, error) {
	return &CapabilitiesResponse{Provider: "p"}, nil
}

// dial serves the Agent and Gateway services over an in-memory listener and
// returns a client connection to them.
func dial(t *testing.T) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	RegisterAgentServer(srv, agent{})
	RegisterGatewayServer(srv, gateway{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func TestAgentServiceCarriesUnaryAndStreamingCalls(t *testing.T) {
	ctx := context.Background()
	c := NewAgentClient(dial(t))
	h, err := c.Hello(ctx, &HelloRequest{})
	if err != nil || h.GetProtocolMajor() != 1 || h.GetVersion() != "v" {
		t.Fatalf("Hello = %v, %v", h, err)
	}
	st, err := c.Rsync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "b"} {
		if err := st.Send(&RsyncFrame{Frame: &RsyncFrame_Data{Data: []byte(d)}}); err != nil {
			t.Fatal(err)
		}
		if f, err := st.Recv(); err != nil || string(f.GetData()) != d {
			t.Fatalf("echo of %q = %v, %v", d, f, err)
		}
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if f, err := st.Recv(); err != nil || f.GetDone().GetFiles() != 2 {
		t.Errorf("after CloseSend: %v, %v; want a done frame counting 2", f, err)
	}
}

// A method the server does not implement answers Unimplemented, for each RPC shape.
func TestUnimplementedMethodsAnswerUnimplemented(t *testing.T) {
	ctx := context.Background()
	cc := dial(t)
	a, g := NewAgentClient(cc), NewGatewayClient(cc)
	calls := map[string]func() error{
		"Tools": func() error { _, err := a.Tools(ctx, &ToolsRequest{}); return err },
		"Call":  func() error { _, err := a.Call(ctx, &CallRequest{}); return err },
		"SyncTar": func() error {
			st, err := a.SyncTar(ctx)
			if err != nil {
				return err
			}
			_, err = st.CloseAndRecv()
			return err
		},
		"ExportTar": func() error {
			st, err := a.ExportTar(ctx, &ExportRequest{})
			if err != nil {
				return err
			}
			_, err = st.Recv()
			return err
		},
		"Create":  func() error { _, err := g.Create(ctx, &CreateRequest{}); return err },
		"SyncGit": func() error { _, err := g.SyncGit(ctx, &SyncGitRequest{}); return err },
		"List":    func() error { _, err := g.List(ctx, &ListRequest{}); return err },
		"Destroy": func() error { _, err := g.Destroy(ctx, &DestroyRequest{}); return err },
		"Reap":    func() error { _, err := g.Reap(ctx, &ReapRequest{}); return err },
	}
	for name, call := range calls {
		if err := call(); status.Code(err) != codes.Unimplemented {
			t.Errorf("%s = %v, want Unimplemented", name, err)
		}
	}
	if c, err := g.Capabilities(ctx, &CapabilitiesRequest{}); err != nil || c.GetProvider() != "p" {
		t.Errorf("Capabilities = %v, %v", c, err)
	}
}
