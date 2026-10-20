package attach

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
)

// wrapper is a provider-style wrapper around another sandbox.
type wrapper struct{ sandbox.Sandbox }

func (w wrapper) Unwrap() sandbox.Sandbox { return w.Sandbox }

// plain is a sandbox attach did not create, with no Unwrap.
type plain struct{}

func (plain) ID() string               { return "plain" }
func (plain) Spec() sandbox.Spec       { return sandbox.Spec{} }
func (plain) Agent() sandbox.AgentInfo { return sandbox.AgentInfo{} }
func (plain) Sync(context.Context, sandbox.Source) (sandbox.SyncResult, error) {
	return sandbox.SyncResult{}, nil
}
func (plain) Tools(context.Context, sandbox.ToolOptions) ([]core.Tool, error) { return nil, nil }
func (plain) Export(context.Context, string, sandbox.Destination) error       { return nil }
func (plain) Close(context.Context) error                                     { return nil }

// cyclic unwraps to itself.
type cyclic struct{ plain }

func (c *cyclic) Unwrap() sandbox.Sandbox { return c }

// TS-19-53: ClientConn and Live follow an Unwrap chain to the sandbox attach
// created. 19-REQ-7.8.
func TestUnwrapChain_TS19_53(t *testing.T) {
	sb := attachFake(t, fakeagent.New(), Config{})
	w2 := wrapper{wrapper{sb}}
	if cc := ClientConn(w2); cc == nil || cc != ClientConn(sb) {
		t.Fatalf("ClientConn(w2) = %v, want ClientConn(sb) = %v", cc, ClientConn(sb))
	}
	if err := Live(w2); err != nil {
		t.Fatalf("Live(w2) on a live sandbox: %v", err)
	}
	if cc := ClientConn(plain{}); cc != nil {
		t.Errorf("ClientConn of a non-attach sandbox = %v, want nil", cc)
	}
	if err := Live(plain{}); err == nil {
		t.Error("Live of a non-attach sandbox is nil, want an error")
	}
	if cc := ClientConn(&cyclic{}); cc != nil {
		t.Errorf("ClientConn of a cyclic chain = %v, want nil", cc)
	}
	if cc := ClientConn(wrapper{}); cc != nil {
		t.Errorf("ClientConn of a wrapper around nil = %v, want nil", cc)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := Live(w2); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("Live(w2) after Close = %v, want ErrClosed", err)
	}
}

// Outgoing follows an Unwrap chain to the sandbox attach created and returns ctx
// with the metadata that sandbox sends with its own RPCs, replacing any outgoing
// metadata ctx had; it is ctx itself when the sandbox sends none or is not one
// attach created.
func TestOutgoingFollowsTheUnwrapChain(t *testing.T) {
	md := metadata.Pairs("agentkit-sandbox-id", "box-7")
	sb := attachFake(t, fakeagent.New(), Config{Metadata: md})
	base := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("stale", "1"))

	for name, s := range map[string]sandbox.Sandbox{"attached": sb, "wrapped": wrapper{wrapper{sb}}} {
		got, ok := metadata.FromOutgoingContext(Outgoing(base, s))
		if !ok || len(got) != 1 || got.Get("agentkit-sandbox-id")[0] != "box-7" {
			t.Errorf("%s: outgoing metadata = %v, want only agentkit-sandbox-id: box-7", name, got)
		}
	}

	bare := attachFake(t, fakeagent.New(), Config{})
	for name, s := range map[string]sandbox.Sandbox{"no metadata": bare, "not attached": plain{}, "cyclic": &cyclic{}, "wraps nil": wrapper{}} {
		if got := Outgoing(base, s); got != base {
			t.Errorf("%s: Outgoing changed the context", name)
		}
	}
}
