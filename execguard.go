package agentkit

import (
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
)

// checkExecuteGuard is OQ-8's loud failure: a shell tool in the resolved set
// with no BeforeToolCall interceptor is core.ErrUnguardedExecute. It is
// consulted at the head of every run, before the slot is claimed, so the
// failure is an ordinary returned error and not a stream nobody reads. The
// opt-out is guard.AllowAll; the shipped starting point is guard.Restricted.
func (a *Agent) checkExecuteGuard() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return checkShellGuard(a.cfg.BeforeToolCall, a.cfg.ToolPolicy.Resolve(a.tools))
}

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
