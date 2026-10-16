package tools

import (
	"context"
	"errors"

	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// References finds usages and callers of target across ws, bounded by the
// default SymbolOptions. It uses no reference cache and no index. A nil ws
// returns an error, an opts.Path outside ws returns an error wrapping
// workspace.ErrPathNotAllowed, and a cancelled context returns the context's
// error; each with an empty result.
func References(ctx context.Context, ws *workspace.Workspace, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error) {
	if ws == nil {
		return ReferenceResult{}, errors.New("tools: References needs a workspace; open one with workspace.Open")
	}
	return executeReferenceSearch(ctx, bridge(ws), target, "", "", opts, SymbolOptions{}, nil)
}
