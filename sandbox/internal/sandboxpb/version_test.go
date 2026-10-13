package sandboxpb_test

import (
	"context"
	"os"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-14-5: the service has six RPCs, Hello reports the current protocol
// (1.2 since spec 15, 15-REQ-4.2; 14 set 1.1) and the regenerated code
// passes the staleness check offline. 14-REQ-2.2.
func TestProtocolMinor_TS14_5(t *testing.T) {
	if sandbox.ProtocolMajor != 1 || sandbox.ProtocolMinor != 2 {
		t.Errorf("protocol %d.%d, want 1.2", sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	}
	svc := sandboxpb.File_sandbox_proto.Services().ByName("Agent")
	if svc.Methods().Len() != 6 || svc.Methods().ByName("Rsync") == nil {
		t.Errorf("Agent has %d methods, want 13's five plus Rsync", svc.Methods().Len())
	}
	t.Setenv("TMPDIR", t.TempDir())
	sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close(context.Background())
	if a := sb.Agent(); a.ProtocolMajor != 1 || a.ProtocolMinor != 2 {
		t.Errorf("Agent() reports %d.%d, want 1.2", a.ProtocolMajor, a.ProtocolMinor)
	}
	proto, err := os.ReadFile("sandbox.proto")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile("sandbox.proto.sha256")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if err := sandboxpb.CheckProtoHash(proto, recorded); err != nil {
		t.Error(err)
	}
}
