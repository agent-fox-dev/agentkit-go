package sandbox

import (
	"context"
	"errors"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Open creates a sandbox from spec with p, fills it from src and returns it
// with its proxy tools: p.Create, then Sync(src) when src is not nil, then
// Tools(opts). It is the sequence every embedder writes, in the order that
// lets a GitRepo source work: a GitRepo must be the first Sync and come before
// the first Tools call.
//
// The sandbox is returned exactly as Create returned it, so accessors that
// follow its Unwrap chain keep working, and it is live: the caller closes it.
// ctx bounds the calls Open makes, not the sandbox's lifetime. The SyncResult
// is discarded; an embedder who needs it calls Create, Sync and Tools itself.
func Open(ctx context.Context, p Provider, spec Spec, src Source, opts ToolOptions) (Sandbox, []core.Tool, error) {
	if p == nil {
		return nil, nil, errors.New("sandbox: Open needs a Provider")
	}
	sb, err := p.Create(ctx, spec)
	if err != nil {
		return nil, nil, err
	}
	if src != nil {
		if _, err := sb.Sync(ctx, src); err != nil {
			return nil, nil, err
		}
	}
	tools, err := sb.Tools(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return sb, tools, nil
}
