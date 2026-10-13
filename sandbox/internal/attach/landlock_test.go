package attach

import (
	"context"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-15-26: an agent of protocol 1.1 or 1.0 sends no report, and attach
// fills ABI 0 with the protocol note; the sandboxes are live. 15-REQ-4.6.
func TestOldAgentLandlockNote_TS15_26(t *testing.T) {
	for _, c := range []struct {
		minor uint32
		note  string
	}{{1, "agent protocol 1.1 does not report Landlock"}, {0, "agent protocol 1.0 does not report Landlock"}} {
		f := fakeagent.New()
		f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: c.minor, Version: "v", Workdir: "/ws"})
		destroyed := 0
		sb := attachFake(t, f, Config{Destroy: func(context.Context) error { destroyed++; return nil }})
		if l := sb.Agent().Landlock; l.ABI != 0 || l.Note != c.note {
			t.Errorf("1.%d: Agent().Landlock = %+v, want ABI 0 and %q", c.minor, l, c.note)
		}
		if r := proxies(t, sb)["read_file"].Execute(context.Background(), []byte(`{}`)); r.Error == "sandbox_unavailable" {
			t.Errorf("1.%d: the sandbox is not live", c.minor)
		}
		if destroyed != 0 {
			t.Errorf("1.%d: the destroy step ran %d times", c.minor, destroyed)
		}
	}
}
