package agentkit

import (
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
)

// checkShellGuard refuses a resolved tool set that reaches a shell tool when
// there is no guard to authorize it. New calls it, so the refusal comes at
// construction, before any request or stream exists.
func checkShellGuard(g core.BeforeToolCall, resolved []core.Tool) error {
	if g != nil {
		return nil
	}
	for _, t := range resolved {
		if guard.IsShellTool(t.Name) {
			return fmt.Errorf("%w (tool %q)", core.ErrUnguardedExecute, t.Name)
		}
	}
	// A shell tool behind a wrapper is as unguarded as one the model calls
	// directly (07-REQ-3): the wrapper calls it through the same pipeline,
	// and with no interceptor nothing stands in its way.
	for _, t := range resolved {
		for _, r := range core.ReachableTools(t.ReachableTools) {
			if guard.IsShellTool(r.Name) {
				return fmt.Errorf("%w (wrapper %q reached shell tool %q)", core.ErrUnguardedExecute, t.Name, r.Name)
			}
		}
	}
	return nil
}
