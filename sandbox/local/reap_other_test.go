//go:build !unix

package local_test

import (
	"context"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-13-61 on a platform without a liveness probe: Reap removes nothing.
// 13-REQ-9.7.
func TestReapWithoutProbe_TS13_61(t *testing.T) {
	isolate(t)
	a := create(t, sandbox.Spec{Network: unrestricted})
	ids, err := local.Unisolated().Reap(context.Background())
	if err != nil || len(ids) != 0 || !exists(sandboxDir(a)) {
		t.Fatalf("Reap = %v, %v; want nothing removed", ids, err)
	}
}
