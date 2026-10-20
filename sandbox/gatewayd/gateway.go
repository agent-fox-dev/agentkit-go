package gatewayd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gatewaywire"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

// gatewayService is the Gateway service. Every RPC reaching it has been
// authenticated; the caller's principal is in its context.
type gatewayService struct {
	sandboxpb.UnimplementedGatewayServer
	s *Server
}

// Capabilities answers the gateway's protocol and build versions, the
// backend's capabilities with the network modes limited to the ceiling, and
// the gateway's limits.
func (g *gatewayService) Capabilities(context.Context, *sandboxpb.CapabilitiesRequest) (*sandboxpb.CapabilitiesResponse, error) {
	s := g.s
	resp := gatewaywire.CapsToPB(s.caps)
	resp.Network = nil
	for _, m := range s.offered {
		resp.Network = append(resp.Network, gatewaywire.ModeToPB(m))
	}
	resp.ProtocolMajor, resp.ProtocolMinor, resp.Version = gateway.ProtocolMajor, gateway.ProtocolMinor, buildver.Version()
	resp.MaxTtlNanos, resp.MaxSandboxes = int64(s.maxTTL), int32(s.maxSandboxes)
	return resp, nil
}

// unsupported is an UNSUPPORTED refusal of field.
func unsupported(field, reason string) *sandboxpb.CreateResponse {
	return &sandboxpb.CreateResponse{Refusal: &sandboxpb.Refusal{Kind: sandboxpb.Refusal_UNSUPPORTED, Field: field, Reason: reason,
		Message: fmt.Sprintf("gatewayd: Spec.%s is refused: %s", field, reason)}}
}

// Create checks the request against the gateway's own rules, whatever the
// client checked, then creates the sandbox with the backend and records it
// for the caller.
func (g *gatewayService) Create(ctx context.Context, req *sandboxpb.CreateRequest) (*sandboxpb.CreateResponse, error) {
	s := g.s
	if b := req.GetSpec().GetProvider(); len(b) > 0 {
		if err := wire.Guard(b, wire.Defaults()); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "gatewayd: Spec.Provider: %v", err)
		}
	}
	spec, err := gatewaywire.SpecFromPB(req.GetSpec())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "gatewayd: "+err.Error())
	}
	if !slices.Contains(s.offered, spec.Network.Mode) {
		return unsupported("Network.Mode", fmt.Sprintf("the gateway offers network modes %s, not %s", modeList(s.offered), spec.Network.Mode)), nil
	}
	ttl := spec.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl > s.maxTTL {
		return unsupported("TTL", fmt.Sprintf("TTL %v is above the gateway's maximum of %v", ttl, s.maxTTL)), nil
	}
	if err := s.reg.reserve(); err != nil {
		if errors.Is(err, errFull) {
			return &sandboxpb.CreateResponse{Refusal: &sandboxpb.Refusal{Kind: sandboxpb.Refusal_CAPACITY,
				Message: fmt.Sprintf("gatewayd: the gateway holds its maximum of %d sandboxes", s.maxSandboxes)}}, nil
		}
		return nil, err
	}
	added := false
	defer func() {
		if !added {
			s.reg.unreserve()
		}
	}()
	created := time.Now()
	bsb, err := s.backend.Create(ctx, spec)
	if err != nil {
		if r := backendRefusal(err); r != nil {
			return &sandboxpb.CreateResponse{Refusal: r}, nil
		}
		return nil, status.Error(codes.Unavailable, "gatewayd: backend create: "+err.Error())
	}
	if attach.ClientConn(bsb) == nil {
		err := bsb.Close(context.Background())
		return nil, status.Errorf(codes.Internal, "gatewayd: backend %q returned a sandbox with no agent connection to relay to%s",
			s.caps.Provider, closeNote(err))
	}
	if ctx.Err() != nil {
		// The caller has gone: nobody will learn the id.
		if err := bsb.Close(context.Background()); err != nil {
			s.log.Warn("closing an abandoned sandbox failed", "id", bsb.ID(), "error", err.Error())
		}
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	eff := bsb.Spec()
	if eff.TTL > 0 {
		ttl = eff.TTL
	}
	effPB, err := gatewaywire.SpecToPB(eff)
	if err != nil {
		return nil, errors.Join(status.Error(codes.Internal, "gatewayd: "+err.Error()), bsb.Close(context.Background()))
	}
	e := &entry{id: bsb.ID(), owner: ownerOf(ctx), created: created, expires: created.Add(ttl), spec: eff, sb: bsb}
	if err := s.reg.add(e); err != nil {
		return nil, errors.Join(err, bsb.Close(context.Background()))
	}
	added = true
	s.logCreate(e.id, e.owner, ttl, eff.Network.Mode)
	return &sandboxpb.CreateResponse{Id: e.id, Spec: effPB,
		CreatedUnixNanos: gatewaywire.Nanos(e.created), ExpiresUnixNanos: gatewaywire.Nanos(e.expires)}, nil
}

// closeNote is ", and closing it failed: <err>" for a non-nil err.
func closeNote(err error) string {
	if err == nil {
		return ""
	}
	return ", and closing it failed: " + err.Error()
}

// backendRefusal is the refusal a backend Create error becomes, or nil for
// an error that is not a refusal.
func backendRefusal(err error) *sandboxpb.Refusal {
	r := &sandboxpb.Refusal{Message: err.Error()}
	var se *sandbox.SpecError
	switch {
	case errors.As(err, &se):
		r.Kind, r.Field, r.Reason = sandboxpb.Refusal_UNSUPPORTED, se.Field, se.Reason
	case errors.Is(err, sandbox.ErrUnsupported):
		r.Kind = sandboxpb.Refusal_UNSUPPORTED
	case errors.Is(err, sandbox.ErrNoAgent):
		r.Kind = sandboxpb.Refusal_NO_AGENT
	case errors.Is(err, sandbox.ErrNoPublishedImage):
		r.Kind = sandboxpb.Refusal_NO_PUBLISHED_IMAGE
	case errors.Is(err, sandbox.ErrProtocol):
		r.Kind = sandboxpb.Refusal_PROTOCOL
	default:
		return nil
	}
	return r
}

// List answers the caller's sandboxes.
func (g *gatewayService) List(ctx context.Context, _ *sandboxpb.ListRequest) (*sandboxpb.ListResponse, error) {
	resp := &sandboxpb.ListResponse{}
	for _, e := range g.s.reg.list(ownerOf(ctx)) {
		resp.Sandboxes = append(resp.Sandboxes, &sandboxpb.SandboxEntry{Id: e.id, Labels: e.spec.Labels,
			CreatedUnixNanos: gatewaywire.Nanos(e.created), ExpiresUnixNanos: gatewaywire.Nanos(e.expires)})
	}
	return resp, nil
}

// Destroy removes the caller's sandbox and closes it. An unknown id, or
// another owner's, is answered OK; a failed Close removes the entry anyway
// and is answered Unavailable.
func (g *gatewayService) Destroy(ctx context.Context, req *sandboxpb.DestroyRequest) (*sandboxpb.DestroyResponse, error) {
	if e := g.s.reg.lookup(ownerOf(ctx), req.GetId()); e != nil {
		if err := g.s.reg.remove(e, reasonClient); err != nil {
			return nil, status.Errorf(codes.Unavailable, "gatewayd: destroy %q: %v", e.id, err)
		}
	}
	return &sandboxpb.DestroyResponse{}, nil
}

// Reap runs the backend's Reap and answers its ids and its error text.
func (g *gatewayService) Reap(ctx context.Context, _ *sandboxpb.ReapRequest) (*sandboxpb.ReapResponse, error) {
	ids, err := g.s.reapBackend(ctx)
	resp := &sandboxpb.ReapResponse{Ids: ids}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp, nil
}
