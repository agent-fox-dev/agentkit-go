package landlock

import (
	"context"
	"errors"
	"sync"
)

// newDestroy is a sandbox's destroy step, which attach runs on Close, at the
// TTL and on a Create failure after the agent started: stop the agent, kill
// its session, remove dir. Each action runs even when an earlier one failed,
// the errors are joined, and the step runs once.
func newDestroy(h *agentProc, pt procTable, dir string, remove func(string) error) func(context.Context) error {
	var once sync.Once
	return func(ctx context.Context) error {
		err := errors.New("landlock: the destroy step already ran")
		ran := false
		once.Do(func() {
			ran = true
			err = errors.Join(stopAgent(h, stopWait), sessionKill(ctx, pt, h.pid, sessionWait), remove(dir))
		})
		if !ran {
			return nil
		}
		return err
	}
}
