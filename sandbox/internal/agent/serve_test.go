//go:build unix

package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Serve ends at the TTL with an error naming it, or with nil when the host
// closes the connection; either way the in-flight call is cancelled and its
// processes are gone.
func TestServeEndsCancellingInFlightCalls(t *testing.T) {
	for _, k := range []struct {
		ttl        time.Duration
		hostCloses bool
		want       string
	}{{time.Second, false, "TTL 1s expired"}, {time.Hour, true, ""}} {
		ws := t.TempDir()
		cc, errc := dial(t, newAgent(t, ws, testEnv(t), k.ttl))
		c := sandboxpb.NewAgentClient(cc)
		if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
			t.Fatal(err)
		}
		calls := make(chan error, 1)
		go func() { _, err := c.Call(context.Background(), backgroundChild); calls <- err }()
		waitLine(t, filepath.Join(ws, "child.pid"))
		if k.hostCloses {
			_ = cc.Close()
		}
		select {
		case err := <-errc:
			if (k.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), k.want) {
				t.Errorf("Serve = %v, want %q", err, k.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return")
		}
		if err := <-calls; err == nil {
			t.Error("the in-flight call ended without an error")
		}
		assertChildGone(t, ws)
	}
}
