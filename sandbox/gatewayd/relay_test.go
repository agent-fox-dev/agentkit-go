package gatewayd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// agentEnv serves Recorder agents with one sandbox: a raw Agent client, its id, a context naming it, its agent.
func agentEnv(t *testing.T) (*gwtest.Env, sandboxpb.AgentClient, string, context.Context, *gwtest.Recorder) {
	t.Helper()
	be := gwtest.NewAgents(nil)
	env, _ := start(t, gatewayd.Config{Backend: be})
	cc := env.Raw(t)
	id := rawCreate(t, sandboxpb.NewGatewayClient(cc), gwtest.Token)
	return env, sandboxpb.NewAgentClient(cc), id, authed(gwtest.Token, gateway.SandboxIDKey, id), be.ByID(id).Rec
}

var startFrame = &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{}}}

// Unknown fields survive, streams are copied frame by frame with the half-close, and a cancel reaches the agent.
func TestRelayForwardsMessagesAndStreamsUnchanged(t *testing.T) {
	_, ag, _, ctx, rec := agentEnv(t)
	unk, agentUnk := []byte{0x98, 0x06, 0x01}, []byte{0x98, 0x06, 0x02} // field 99, varint
	resp := &sandboxpb.CallResponse{Ok: true}
	resp.ProtoReflect().SetUnknown(agentUnk)
	rec.SetCallResponse(resp)
	req := &sandboxpb.CallRequest{Name: "read_file"}
	req.ProtoReflect().SetUnknown(unk)
	got, err := ag.Call(ctx, req)
	if calls := rec.Calls(); err != nil || !bytes.Equal(calls[len(calls)-1].ProtoReflect().GetUnknown(), unk) || !bytes.Equal(got.ProtoReflect().GetUnknown(), agentUnk) {
		t.Errorf("Call: %v; an unknown field was lost on the way", err)
	}

	st, _ := ag.SyncTar(ctx)
	for i := range 3 {
		_ = st.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: []byte{byte(i)}}})
	}
	if _, err := st.CloseAndRecv(); err != nil || len(rec.Chunks()) != 3 || rec.Chunks()[2].GetData()[0] != 2 {
		t.Errorf("SyncTar: %v; the agent received %v, want three chunks in order", err, rec.Chunks())
	}

	rec.SetExport(bytes.Repeat([]byte("e"), 3<<20), nil)
	es, _ := ag.ExportTar(ctx, &sandboxpb.ExportRequest{})
	var n, frames int
	for c, err := es.Recv(); err != io.EOF; c, err = es.Recv() {
		must(t, err)
		n, frames = n+len(c.GetData()), frames+1
	}
	if n != 3<<20 || frames != 3 {
		t.Errorf("ExportTar relayed %d bytes in %d frames, want 3 MiB in 3", n, frames)
	}

	rs, _ := ag.Rsync(ctx)
	for _, f := range []*sandboxpb.RsyncFrame{startFrame, {Frame: &sandboxpb.RsyncFrame_Data{Data: []byte("one")}}, {Frame: &sandboxpb.RsyncFrame_Data{Data: []byte("two")}}} {
		_ = rs.Send(f)
	}
	_ = rs.CloseSend()
	var echoed []string
	for f, err := rs.Recv(); err != io.EOF; f, err = rs.Recv() {
		must(t, err)
		echoed = append(echoed, string(f.GetData()))
	}
	if !slices.Equal(echoed, []string{"", "one", "two"}) {
		t.Errorf("the Rsync frames came back as %q", echoed)
	}

	rec.SetBlock(true)
	cctx, cancel := context.WithCancel(ctx)
	go func() { _, _ = ag.Call(cctx, req) }()
	<-rec.Entered()
	cancel()
	testutil.Eventually(t, "the agent's call to be cancelled", func() bool { return errors.Is(rec.CallCtxErr(), context.Canceled) })
}

// A missing, repeated, malformed, unknown or foreign id is answered Unavailable naming it, and reaches no agent.
func TestRelayAnswersUnavailableForAnRPCThatNamesNoSandboxOfTheCaller(t *testing.T) {
	env, ag, id, ctx, rec := agentEnv(t)
	key := gateway.SandboxIDKey
	for _, c := range []struct {
		ctx  context.Context
		want string
	}{
		{authed(gwtest.Token), "carries no " + key},
		{authed(gwtest.Token, key, id, key, id), "2 values of " + key},
		{authed(gwtest.Token, key, strings.Repeat("x", 257)), "malformed sandbox id"},
		{authed(gwtest.Token, key, "a b"), `malformed sandbox id "a b"`},
		{authed(gwtest.Token, key, "no-such-id"), `no sandbox "no-such-id"`},
		{authed(gwtest.TokenB, key, id), `no sandbox "` + id + `"`},
	} {
		if _, err := ag.Hello(c.ctx, &sandboxpb.HelloRequest{}); status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v, want Unavailable with %q", err, c.want)
		}
	}
	if _, err := ag.Hello(ctx, &sandboxpb.HelloRequest{}); err != nil || rec.Count("Hello") != 2 { // the backend's handshake and this one
		t.Errorf("Hello for the sandbox: %v; the agent served %d Hellos, want 2", err, rec.Count("Hello"))
	}
	errs := everyRPC(t, env, authed(gwtest.Token)) // naming no sandbox
	for _, rpc := range []string{"Tools", "Call", "SyncTar", "ExportTar", "Rsync"} {
		if status.Code(errs[rpc]) != codes.Unavailable || !strings.Contains(errs[rpc].Error(), "carries no "+key) {
			t.Errorf("%s naming no sandbox: %v, want Unavailable", rpc, errs[rpc])
		}
	}
}

// Not the authorization header, not the sandbox id, nothing the caller added.
func TestTheAgentSeesNoneOfTheCallersMetadata(t *testing.T) {
	env, _, _, ctx, rec := agentEnv(t)
	everyRPC(t, env, metadata.AppendToOutgoingContext(ctx, "x-private", "hunter2"))
	for _, rpc := range []string{"Hello", "Tools", "Call", "SyncTar", "ExportTar", "Rsync"} {
		md := rec.Metadata(rpc)
		if rec.Count(rpc) == 0 || md.Get("authorization") != nil || md.Get(gateway.SandboxIDKey) != nil || md.Get("x-private") != nil {
			t.Errorf("%s reached the agent %d times, last with metadata %v", rpc, rec.Count(rpc), md)
		}
	}
}

// Its four refusal codes pass through; any other error is Unavailable with its message; an unknown method Unimplemented.
func TestRelayMapsTheAgentsErrors(t *testing.T) {
	env, ag, _, ctx, rec := agentEnv(t)
	for _, c := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.Unimplemented, codes.Internal} {
		want := c
		if c == codes.Internal {
			want = codes.Unavailable
		}
		rec.SetCall(nil, status.Error(c, "m"), true)
		rec.SetTools(nil, status.Error(c, "m"))
		rec.SetExport(nil, status.Error(c, "m"))
		_, cerr := ag.Call(ctx, &sandboxpb.CallRequest{Name: "read_file"})
		_, terr := ag.Tools(ctx, &sandboxpb.ToolsRequest{})
		es, _ := ag.ExportTar(ctx, &sandboxpb.ExportRequest{})
		_, eerr := es.Recv()
		for name, err := range map[string]error{"Call": cerr, "Tools": terr, "ExportTar": eerr} {
			if status.Code(err) != want || status.Convert(err).Message() != "m" {
				t.Errorf("%s: %v arrived as %v, want %v", name, c, err, want)
			}
		}
	}
	rec.SetRsync(nil) // the agent answers Rsync Unimplemented
	rs, _ := ag.Rsync(ctx)
	_ = rs.Send(startFrame)
	if _, err := rs.Recv(); status.Code(err) != codes.Unimplemented {
		t.Errorf("an Rsync the agent does not serve: %v, want Unimplemented", err)
	}
	rs, _ = ag.Rsync(ctx)
	_ = rs.CloseSend()
	if _, err := rs.Recv(); status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "before its start frame") {
		t.Errorf("an Rsync with no start frame: %v, want Unavailable", err)
	}
	if err := env.Raw(t).Invoke(ctx, "/agentkit.sandbox.v1.Agent/Future", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("an unknown Agent method: %v, want Unimplemented", err)
	}
}

// End to end: the relay sends on the sandbox id of a backend that is itself a gateway client, so transfers, tools and Close work.
func TestRelayReachesASandboxBehindAnotherGateway(t *testing.T) {
	be := gwtest.NewCounting()
	inner, _ := start(t, gatewayd.Config{Backend: be})
	outer, _ := start(t, gatewayd.Config{Backend: inner.Dial(t, gwtest.Token)})
	ctx := context.Background()
	sb := create(t, outer.Dial(t, gwtest.Token), unrestrictedSpec)
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: testutil.Tree(t, map[string]string{"d/x": "hello"})}); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(testutil.Tar(t, map[string]string{"y": "tar"}))}); err != nil {
		t.Fatal(err)
	}
	if r := run(t, sb, "read_file", `{"path":"d/x"}`); !r.OK || !strings.Contains(r.Text, "hello") {
		t.Errorf("read_file through two gateways: %+v", r)
	}
	out := filepath.Join(t.TempDir(), "out")
	var tarOut bytes.Buffer
	if sb.Export(ctx, ".", sandbox.LocalDir{Path: out}) != nil || sb.Export(ctx, "y", sandbox.TarOut{W: &tarOut}) != nil || gone(filepath.Join(out, "d", "x")) ||
		!strings.Contains(tarOut.String(), "tar") {
		t.Errorf("the exports through two gateways lost a file")
	}
	if err := sb.Close(ctx); err != nil || be.Closes() != 1 {
		t.Errorf("Close: %v, %d backend closes; want the inner backend's sandbox closed", err, be.Closes())
	}
}
