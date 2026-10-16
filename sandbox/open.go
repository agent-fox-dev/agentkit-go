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
//
// Open leaves nothing behind when it fails. A nil Provider is an error and
// nothing is called. If Create fails, its error is returned unchanged. If Sync
// or Tools fails, the sandbox is closed once, with a context that carries
// ctx's values but not its cancellation, because an expired or cancelled ctx
// is the commonest reason for the failure and must not stop the cleanup. The
// result is then nil, nil and the step's error, unchanged, so errors.Is
// matches whatever the step's error matches (ErrUnsupported, ErrToolMismatch,
// ErrUnsafeArchive, ...); when Close fails too, the error is the step's joined
// with Close's.
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
			return nil, nil, closeAfter(ctx, sb, err)
		}
	}
	tools, err := sb.Tools(ctx, opts)
	if err != nil {
		return nil, nil, closeAfter(ctx, sb, err)
	}
	return sb, tools, nil
}

// closeAfter closes sb after a step failed with stepErr and returns stepErr,
// joined with Close's error when Close fails too.
func closeAfter(ctx context.Context, sb Sandbox, stepErr error) error {
	if err := sb.Close(context.WithoutCancel(ctx)); err != nil {
		return errors.Join(stepErr, err)
	}
	return stepErr
}
