package gateway

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
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
	// open is every sandbox created through the provider and not yet
	// destroyed, by id.
	open map[string]*gwSandbox

	closeOnce sync.Once
	closeErr  error
}

var _ sandbox.Provider = (*Provider)(nil)

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

// remember records sb as open, or reports false when the provider has been
// closed meanwhile.
func (p *Provider) remember(sb *gwSandbox) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	if p.open == nil {
		p.open = map[string]*gwSandbox{}
	}
	p.open[sb.id] = sb
	return true
}

// forget drops id from the open sandboxes.
func (p *Provider) forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.open, id)
}

// Close closes every sandbox created through the provider that is still
// open, which destroys each on the gateway, and then the connection. It
// returns the joined errors. Later calls return nil, and Create, Reap, List
// and Destroy return ErrClosed without an RPC.
func (p *Provider) Close() error {
	first := false
	p.closeOnce.Do(func() {
		first = true
		p.mu.Lock()
		p.closed = true
		open := slices.Collect(maps.Values(p.open))
		p.mu.Unlock()
		var errs []error
		for _, sb := range open {
			errs = append(errs, sb.Close(context.Background()))
		}
		p.closeErr = errors.Join(append(errs, p.cc.Close())...)
	})
	if !first {
		return nil
	}
	return p.closeErr
}
