package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gatewaywire"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// defaultImage is the image an empty Spec.Image becomes when the backend
// honours Image: the client's own, so the agent matches the client's tools.
// Tests replace it.
var defaultImage = sandbox.DefaultImage

// Create creates a sandbox on the gateway. The spec is checked first on the
// client, with no RPC: CheckSpec against the cached capabilities, the
// gateway's MaxTTL, and that Spec.Provider encodes as JSON; an empty Image
// becomes the client's sandbox.DefaultImage when the backend honours Image.
// The gateway checks everything again. A refusal is returned as the
// matching sandbox or gateway error; a sandbox the gateway created but the
// client could not attach to is destroyed.
func (p *Provider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	if p.isClosed() {
		return nil, ErrClosed
	}
	eff, err := sandbox.CheckSpec(p.caps, spec)
	if err != nil {
		return nil, err
	}
	if max := p.info.MaxTTL; eff.TTL > max {
		return nil, &sandbox.SpecError{Provider: p.caps.Provider, Field: "TTL",
			Reason: fmt.Sprintf("TTL %v is above the gateway's maximum of %v", eff.TTL, max)}
	}
	pb, err := gatewaywire.SpecToPB(spec)
	if err != nil {
		return nil, &sandbox.SpecError{Provider: p.caps.Provider, Field: "Provider", Reason: err.Error()}
	}
	if pb.Image == "" && p.caps.Image {
		if pb.Image, err = defaultImage(); err != nil {
			return nil, err
		}
	}
	resp, err := p.gw.Create(ctx, &sandboxpb.CreateRequest{Spec: pb})
	if err != nil {
		return nil, p.rpcErr("create", err)
	}
	if r := resp.GetRefusal(); r != nil {
		return nil, refusalErr(p.caps.Provider, r)
	}
	id := resp.GetId()
	destroy := func(ctx context.Context) error {
		p.forget(id)
		return p.destroyRPC(ctx, id)
	}
	got, err := gatewaywire.SpecFromPB(resp.GetSpec())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("gateway %s: create: the effective spec: %w", p.url, err), destroyNow(destroy))
	}
	inner, err := attach.New(ctx, noWatch{p.cc}, nopCloser{}, attach.Config{ID: id, Spec: got,
		Metadata: metadata.Pairs(SandboxIDKey, id), Provider: p.caps.Provider, Destroy: destroy})
	if err != nil {
		return nil, err
	}
	sb := &gwSandbox{inner: inner, p: p, id: id}
	if ctx.Err() != nil {
		return nil, errors.Join(fmt.Errorf("gateway: create: %w", ctx.Err()), sb.Close(context.Background()))
	}
	if !p.remember(sb) {
		return nil, errors.Join(ErrClosed, sb.Close(context.Background()))
	}
	return sb, nil
}

// destroyNow runs destroy with a fresh 30-second context.
func destroyNow(destroy func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return destroy(ctx)
}

// noWatch hides the shared *grpc.ClientConn from attach, whose connection
// watcher would otherwise mark every sandbox unavailable at a network blip:
// liveness is decided per RPC.
type noWatch struct{ grpc.ClientConnInterface }

// nopCloser leaves the shared connection open when a sandbox closes.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }
