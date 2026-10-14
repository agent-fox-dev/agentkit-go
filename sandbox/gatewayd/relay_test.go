package gatewayd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// emptyTar is a tar stream with no entries.
var emptyTar = make([]byte, 1024)

// eventually fails t unless cond holds within 5 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// gone reports whether path no longer exists.
func gone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// rawCreate creates a sandbox with a raw Gateway client and token, failing
// t on a refusal.
func rawCreate(t *testing.T, gw sandboxpb.GatewayClient, token string, mode sandboxpb.NetworkMode) string {
	t.Helper()
	r, err := gw.Create(gwtest.Ctx(context.Background(), token), &sandboxpb.CreateRequest{Spec: specPB(mode, 0)})
	if err != nil || r.GetRefusal() != nil {
		t.Fatalf("Create: %v, %v", r.GetRefusal(), err)
	}
	return r.GetId()
}

// readFile runs the read_file proxy on sb.
func readFile(t *testing.T, sb sandbox.Sandbox, path string) map[string]any {
	t.Helper()
	r := toolOf(t, sb, "read_file").Execute(context.Background(), []byte(`{"path":"`+path+`"}`))
	return map[string]any{"ok": r.OK, "error": r.Error, "data": r.Data}
}

// TS-19-46: the relay forwards messages unchanged with unknown fields,
// copies streams frame by frame with half-close, and propagates
// cancellation. 19-REQ-7.1.
func TestRelayForwards_TS19_46(t *testing.T) {
	be := gwtest.NewAgents(nil)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	cc := env.Raw(t)
	id := rawCreate(t, sandboxpb.NewGatewayClient(cc), gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	rec := be.ByID(id).Rec
	ag := sandboxpb.NewAgentClient(cc)
	ctx := gwtest.Ctx(context.Background(), gwtest.Token, gateway.SandboxIDKey, id)

	// field 99, varint 1, in both directions.
	unk, agentUnk := []byte{0x98, 0x06, 0x01}, []byte{0x98, 0x06, 0x02}
	resp := &sandboxpb.CallResponse{Ok: true}
	resp.ProtoReflect().SetUnknown(agentUnk)
	rec.SetCallResponse(resp)
	req := &sandboxpb.CallRequest{Name: "read_file"}
	req.ProtoReflect().SetUnknown(unk)
	got, err := ag.Call(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	calls := rec.Calls()
	if len(calls) == 0 || !bytes.Equal(calls[len(calls)-1].ProtoReflect().GetUnknown(), unk) {
		t.Errorf("the agent's CallRequest lost its unknown field")
	}
	if !bytes.Equal(got.ProtoReflect().GetUnknown(), agentUnk) {
		t.Errorf("the client's CallResponse lost the agent's unknown field")
	}

	st, err := ag.SyncTar(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := st.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: []byte{byte(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CloseAndRecv(); err != nil {
		t.Fatal(err)
	}
	if ch := rec.Chunks(); len(ch) != 3 || ch[0].GetData()[0] != 0 || ch[2].GetData()[0] != 2 {
		t.Errorf("the agent received chunks %v, want three in order", ch)
	}

	blob := bytes.Repeat([]byte("e"), 3<<20)
	rec.SetExport(blob, nil)
	es, err := ag.ExportTar(ctx, &sandboxpb.ExportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var n, frames int
	for {
		c, err := es.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n, frames = n+len(c.GetData()), frames+1
	}
	if n != len(blob) || frames != 3 {
		t.Errorf("ExportTar relayed %d bytes in %d frames, want %d in 3", n, frames, len(blob))
	}

	rs, err := ag.Rsync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sent := []*sandboxpb.RsyncFrame{
		{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT}}},
		{Frame: &sandboxpb.RsyncFrame_Data{Data: []byte("one")}},
		{Frame: &sandboxpb.RsyncFrame_Data{Data: []byte("two")}},
	}
	for _, f := range sent {
		if err := rs.Send(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := rs.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var echoed []string
	for {
		f, err := rs.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		echoed = append(echoed, string(f.GetData()))
	}
	if !slices.Equal(echoed, []string{"", "one", "two"}) {
		t.Errorf("the Rsync frames came back as %q", echoed)
	}

	rec.SetBlock(true)
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := ag.Call(cctx, req); done <- err }()
	<-rec.Entered()
	cancel()
	<-done
	eventually(t, "the agent's call context to end", func() bool { return errors.Is(rec.CallCtxErr(), context.Canceled) })
}

// TS-19-47: a missing, malformed, unknown, foreign or dead sandbox id is
// answered Unavailable naming the id, without reaching an agent.
// 19-REQ-7.2.
func TestRelayRouting_TS19_47(t *testing.T) {
	be := gwtest.NewAgents(nil)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	id := rawCreate(t, gw, gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	rec := be.ByID(id).Rec
	n := rec.Count("Hello")
	for _, c := range []struct{ tok, id string }{
		{gwtest.Token, ""}, {gwtest.Token, "../../etc"}, {gwtest.Token, "no-such-id"}, {gwtest.TokenB, id},
	} {
		ctx := gwtest.Ctx(context.Background(), c.tok)
		if c.id != "" {
			ctx = gwtest.Ctx(context.Background(), c.tok, gateway.SandboxIDKey, c.id)
		}
		_, err := ag.Hello(ctx, &sandboxpb.HelloRequest{})
		if status.Code(err) != codes.Unavailable {
			t.Errorf("id %q: %v, want Unavailable", c.id, err)
		}
		if c.id != "" && !strings.Contains(status.Convert(err).Message(), c.id) {
			t.Errorf("id %q: %q does not name it", c.id, status.Convert(err).Message())
		}
		if c.id == "" && !strings.Contains(status.Convert(err).Message(), gateway.SandboxIDKey) {
			t.Errorf("no id: %q does not say the id is missing", status.Convert(err).Message())
		}
	}
	if got := rec.Count("Hello"); got != n {
		t.Errorf("the agent received %d Hellos from refused RPCs", got-n)
	}

	lb := gwtest.NewCounting()
	lenv := gwtest.Start(t, gatewayd.Config{Backend: lb, NetworkModes: unrestricted})
	lcc := lenv.Raw(t)
	lid := rawCreate(t, sandboxpb.NewGatewayClient(lcc), gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	bsb := lb.ByID(lid)
	if err := localtest.Cut(bsb.Sandbox); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the cut sandbox to read as dead", func() bool {
		_, err := sandboxpb.NewAgentClient(lcc).Hello(gwtest.Ctx(context.Background(), gwtest.Token, gateway.SandboxIDKey, lid), &sandboxpb.HelloRequest{})
		return status.Code(err) == codes.Unavailable && strings.Contains(err.Error(), lid)
	})
	l, err := sandboxpb.NewGatewayClient(lcc).List(gwtest.Ctx(context.Background(), gwtest.Token), &sandboxpb.ListRequest{})
	if err != nil || len(l.GetSandboxes()) != 0 {
		t.Errorf("List after the cut = %v, %v; want no entries", l.GetSandboxes(), err)
	}
	eventually(t, "the cut sandbox's directory to go", func() bool { return gone(bsb.Dir()) })
}

// TS-19-48: an agent behind the gateway never sees the authorization header
// or the sandbox id key. 19-REQ-7.3.
func TestMetadataHygiene_TS19_48(t *testing.T) {
	be := gwtest.NewAgents(func(r *gwtest.Recorder) { r.SetExport(emptyTar, nil) })
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb, err := p.Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	_ = readFile(t, sb, "x")
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)}); err != nil {
		t.Fatal(err)
	}
	if err := sb.Export(ctx, ".", sandbox.TarOut{W: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	rs, err := sandboxpb.NewAgentClient(env.Raw(t)).Rsync(gwtest.Ctx(ctx, gwtest.Token, gateway.SandboxIDKey, sb.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{}}}); err != nil {
		t.Fatal(err)
	}
	_ = rs.CloseSend()
	for {
		if _, err := rs.Recv(); err != nil {
			break
		}
	}
	rec := be.ByID(sb.ID()).Rec
	for _, rpc := range []string{"Hello", "Tools", "Call", "SyncTar", "ExportTar", "Rsync"} {
		if rec.Count(rpc) == 0 {
			t.Errorf("the agent served no %s", rpc)
			continue
		}
		md := rec.Metadata(rpc)
		if md.Get("authorization") != nil || md.Get(gateway.SandboxIDKey) != nil {
			t.Errorf("%s reached the agent with metadata %v", rpc, md)
		}
	}
}

// TS-19-49: the agent's four refusal codes pass through, any other upstream
// error becomes Unavailable, and an unknown Agent method is Unimplemented.
// 19-REQ-7.4.
func TestRelayErrors_TS19_49(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb, err := p.Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, map[string]string{"x": "hello"}))}); err != nil {
		t.Fatal(err)
	}
	err = sb.Export(ctx, "missing/path", sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "dst")})
	if err == nil || errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Export of a missing path: %v, want the agent's refusal", err)
	}
	if r := readFile(t, sb, "x"); r["ok"] != true {
		t.Errorf("read_file after the refusal: %v; the sandbox should stay live", r)
	}

	be := gwtest.NewAgents(nil)
	aenv := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	cc := aenv.Raw(t)
	id := rawCreate(t, sandboxpb.NewGatewayClient(cc), gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	rec := be.ByID(id).Rec
	actx := gwtest.Ctx(ctx, gwtest.Token, gateway.SandboxIDKey, id)
	ag := sandboxpb.NewAgentClient(cc)
	for _, c := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.Unimplemented} {
		rec.SetCall(nil, status.Error(c, "m"), true)
		_, err := ag.Call(actx, &sandboxpb.CallRequest{Name: "read_file"})
		if status.Code(err) != c || status.Convert(err).Message() != "m" {
			t.Errorf("%v arrived as %v", c, err)
		}
	}
	rec.SetCall(nil, status.Error(codes.Internal, "agent boom"), true)
	if _, err := ag.Call(actx, &sandboxpb.CallRequest{Name: "read_file"}); status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "agent boom") {
		t.Errorf("Internal agent boom arrived as %v, want Unavailable carrying it", err)
	}
	err = cc.Invoke(actx, "/agentkit.sandbox.v1.Agent/Future", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("an unknown Agent method: %v, want Unimplemented", err)
	}
}
