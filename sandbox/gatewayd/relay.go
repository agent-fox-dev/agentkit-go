package gatewayd

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// relay is the Agent service the gateway serves: it relays each RPC, typed
// and unchanged, to the agent of the caller's sandbox the RPC names in
// agentkit-sandbox-id. Every condition on the gateway's side is answered
// Unavailable, so a client never mistakes a gone sandbox for the agent's
// own refusal. An Agent RPC this build does not know is answered
// Unimplemented by gRPC itself.
type relay struct {
	sandboxpb.UnimplementedAgentServer
	s *Server
}

// maxIDLen bounds a sandbox id in metadata.
const maxIDLen = 256

// route is the caller's live sandbox named by ctx's agentkit-sandbox-id. A
// missing, repeated or malformed id, an unknown id and another owner's id
// are all answered Unavailable; a sandbox whose backend is no longer live
// is removed and answered Unavailable too.
func (r *relay) route(ctx context.Context) (*entry, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(gateway.SandboxIDKey)
	switch {
	case len(vals) == 0:
		return nil, status.Errorf(codes.Unavailable, "gatewayd: the RPC carries no %s: it names no sandbox", gateway.SandboxIDKey)
	case len(vals) > 1:
		return nil, status.Errorf(codes.Unavailable, "gatewayd: the RPC carries %d values of %s: %q", len(vals), gateway.SandboxIDKey, vals)
	case !validID(vals[0]):
		return nil, status.Errorf(codes.Unavailable, "gatewayd: malformed sandbox id %q", vals[0])
	}
	return r.s.reg.access(ownerOf(ctx), vals[0])
}

// validID reports whether id is a plausible sandbox id: non-empty, at most
// maxIDLen bytes of printable ASCII.
func validID(id string) bool {
	if id == "" || len(id) > maxIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return false
		}
	}
	return true
}

// upstream is a client of e's agent and the context to call it under: the
// incoming RPC's, so cancellation and deadlines propagate, with none of its
// metadata but what the backend's sandbox sends with its own RPCs. That is
// nothing for a container or local sandbox, whose agent never sees the
// authorization header or the sandbox id, and the sandbox id there for a
// sandbox created through a gateway client, whose "agent" is the next gateway.
func upstream(ctx context.Context, e *entry) (sandboxpb.AgentClient, context.Context) {
	return sandboxpb.NewAgentClient(attach.ClientConn(e.sb)), attach.Outgoing(metadata.NewOutgoingContext(ctx, metadata.MD{}), e.sb)
}

// mapUpstream is the status a relayed RPC's upstream error is answered
// with: the agent's four refusal codes pass through with their messages,
// and anything else is Unavailable carrying the upstream message.
func mapUpstream(err error) error {
	if err == nil {
		return nil
	}
	st := status.Convert(err)
	switch st.Code() {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.Unimplemented:
		return status.Error(st.Code(), st.Message())
	}
	return status.Error(codes.Unavailable, st.Message())
}

func (r *relay) Hello(ctx context.Context, in *sandboxpb.HelloRequest) (*sandboxpb.HelloResponse, error) {
	e, err := r.route(ctx)
	if err != nil {
		return nil, err
	}
	up, uctx := upstream(ctx, e)
	resp, err := up.Hello(uctx, in)
	return resp, mapUpstream(err)
}

// Tools takes the GitRepo slot whatever its outcome.
func (r *relay) Tools(ctx context.Context, in *sandboxpb.ToolsRequest) (*sandboxpb.ToolsResponse, error) {
	e, err := r.route(ctx)
	if err != nil {
		return nil, err
	}
	release := e.slot.hold()
	defer release()
	e.slot.take()
	up, uctx := upstream(ctx, e)
	resp, err := up.Tools(uctx, in)
	return resp, mapUpstream(err)
}

func (r *relay) Call(ctx context.Context, in *sandboxpb.CallRequest) (*sandboxpb.CallResponse, error) {
	e, err := r.route(ctx)
	if err != nil {
		return nil, err
	}
	up, uctx := upstream(ctx, e)
	resp, err := up.Call(uctx, in)
	return resp, mapUpstream(err)
}

// SyncTar copies the client's chunks to the agent and the agent's answer
// back; one that succeeds takes the GitRepo slot.
func (r *relay) SyncTar(stream sandboxpb.Agent_SyncTarServer) error {
	e, err := r.route(stream.Context())
	if err != nil {
		return err
	}
	release := e.slot.hold()
	defer release()
	up, uctx := upstream(stream.Context(), e)
	us, err := up.SyncTar(uctx)
	if err != nil {
		return mapUpstream(err)
	}
	for {
		c, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := us.Send(c); err != nil {
			// The agent ended the stream; CloseAndRecv has its status.
			break
		}
	}
	resp, err := us.CloseAndRecv()
	if err != nil {
		return mapUpstream(err)
	}
	e.slot.take()
	return stream.SendAndClose(resp)
}

func (r *relay) ExportTar(in *sandboxpb.ExportRequest, stream sandboxpb.Agent_ExportTarServer) error {
	e, err := r.route(stream.Context())
	if err != nil {
		return err
	}
	up, uctx := upstream(stream.Context(), e)
	us, err := up.ExportTar(uctx, in)
	if err != nil {
		return mapUpstream(err)
	}
	for {
		c, err := us.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return mapUpstream(err)
		}
		if err := stream.Send(c); err != nil {
			return err
		}
	}
}

// Rsync copies frames both ways, propagating the client's half-close. A
// transfer whose start frame says SYNC and that ends OK takes the GitRepo
// slot.
func (r *relay) Rsync(stream sandboxpb.Agent_RsyncServer) error {
	e, err := r.route(stream.Context())
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.Unavailable, "gatewayd: the Rsync stream ended before its start frame")
		}
		return err
	}
	sync := first.GetStart().GetDirection() == sandboxpb.RsyncStart_SYNC
	if sync {
		release := e.slot.hold()
		defer release()
	}
	up, uctx := upstream(stream.Context(), e)
	us, err := up.Rsync(uctx)
	if err != nil {
		return mapUpstream(err)
	}
	if err := us.Send(first); err != nil {
		_, err = us.Recv()
		return mapUpstream(rsyncEnd(err))
	}
	go func() {
		for {
			f, err := stream.Recv()
			if err != nil {
				_ = us.CloseSend()
				return
			}
			if err := us.Send(f); err != nil {
				return
			}
		}
	}()
	for {
		f, err := us.Recv()
		if err == io.EOF {
			if sync {
				e.slot.take()
			}
			return nil
		}
		if err != nil {
			return mapUpstream(err)
		}
		if err := stream.Send(f); err != nil {
			return err
		}
	}
}

// rsyncEnd is err, or an Unavailable status when the agent ended the stream
// with no error before it was used.
func rsyncEnd(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return status.Error(codes.Unavailable, "gatewayd: the agent ended the Rsync stream at once")
	}
	return err
}
