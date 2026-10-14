package gateway

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"google.golang.org/grpc"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gatewaywire"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Provider is a sandbox.Provider whose sandboxes live on a gateway. It holds
// one connection to the gateway, shared by every sandbox it creates.
type Provider struct {
	url  string
	cc   *grpc.ClientConn
	gw   sandboxpb.GatewayClient
	caps sandbox.Capabilities
	info GatewayInfo

	mu     sync.Mutex
	closed bool

	closeOnce sync.Once
	closeErr  error
}

// Capabilities is the answer cached at Dial: named "gateway/<backend>", the
// network modes within the gateway's ceiling, the backend's isolation,
// resources, image, workdir and Landlock, and the backend's notes followed
// by a sentence naming the gateway's host and limits.
func (p *Provider) Capabilities() sandbox.Capabilities { return p.caps }

// Gateway is what the gateway reported at Dial.
func (p *Provider) Gateway() GatewayInfo { return p.info }

// isClosed reports whether Close has been called.
func (p *Provider) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Reap runs the backend's Reap on the gateway and returns the removed ids,
// sorted, with an error carrying the gateway's error text when a removal
// failed.
func (p *Provider) Reap(ctx context.Context) ([]string, error) {
	if p.isClosed() {
		return nil, ErrClosed
	}
	resp, err := p.gw.Reap(ctx, &sandboxpb.ReapRequest{})
	if err != nil {
		return nil, p.rpcErr("reap", err)
	}
	ids := slices.Clone(resp.GetIds())
	slices.Sort(ids)
	if e := resp.GetError(); e != "" {
		return ids, fmt.Errorf("gateway %s: reap: %s", p.url, e)
	}
	return ids, nil
}

// List returns the caller's sandboxes on the gateway, sorted by creation
// time and then id.
func (p *Provider) List(ctx context.Context) ([]Info, error) {
	if p.isClosed() {
		return nil, ErrClosed
	}
	resp, err := p.gw.List(ctx, &sandboxpb.ListRequest{})
	if err != nil {
		return nil, p.rpcErr("list", err)
	}
	var out []Info
	for _, e := range resp.GetSandboxes() {
		out = append(out, Info{ID: e.GetId(), Labels: e.GetLabels(),
			Created: gatewaywire.FromNanos(e.GetCreatedUnixNanos()), Expires: gatewaywire.FromNanos(e.GetExpiresUnixNanos())})
	}
	slices.SortFunc(out, func(a, b Info) int {
		return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// Destroy destroys the caller's sandbox id on the gateway. An unknown id
// returns nil, so Destroy is idempotent.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	if p.isClosed() {
		return ErrClosed
	}
	return p.destroyRPC(ctx, id)
}

// destroyRPC sends Gateway.Destroy for id.
func (p *Provider) destroyRPC(ctx context.Context, id string) error {
	if _, err := p.gw.Destroy(ctx, &sandboxpb.DestroyRequest{Id: id}); err != nil {
		return p.rpcErr("destroy", err)
	}
	return nil
}

// Close closes the connection to the gateway. Later calls return nil, and
// Create, Reap, List and Destroy return ErrClosed without an RPC.
func (p *Provider) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.closeErr = p.cc.Close()
	})
	return p.closeErr
}
