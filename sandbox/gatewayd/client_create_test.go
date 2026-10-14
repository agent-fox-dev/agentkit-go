package gatewayd_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
)

// unwrapper is a sandbox with an Unwrap method.
type unwrapper interface{ Unwrap() sandbox.Sandbox }

// TS-19-22: over a local backend, RequireLandlock, Image and NetworkNone
// are refused on the client naming gateway/local. 19-REQ-4.1.
func TestClientRefusesOverLocal_TS19_22(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	for _, c := range []struct {
		spec  sandbox.Spec
		field string
	}{
		{sandbox.Spec{Network: unrestrictedSpec.Network, RequireLandlock: true}, "RequireLandlock"},
		{sandbox.Spec{Network: unrestrictedSpec.Network, Image: "example/img"}, "Image"},
		{sandbox.Spec{}, "Network.Mode"},
	} {
		_, err := p.Create(context.Background(), c.spec)
		var se *sandbox.SpecError
		if !errors.As(err, &se) || !errors.Is(err, sandbox.ErrUnsupported) || se.Provider != "gateway/local" || se.Field != c.field {
			t.Errorf("%s: %v, want a SpecError for gateway/local", c.field, err)
		}
	}
	if n := be.Creates(); n != 0 {
		t.Errorf("the backend's Create ran %d times, want 0", n)
	}
}

// TS-19-25: the sandbox Create returns carries the gateway id, the
// effective spec with the agent's Workdir and the relayed Hello, and is
// attach's sandbox. 19-REQ-4.4.
func TestCreatedSandbox_TS19_25(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb := create(t, p, sandbox.Spec{Network: unrestrictedSpec.Network, Labels: map[string]string{"k": "v"}})
	bsb := be.Last()
	if sb.ID() != bsb.ID() {
		t.Errorf("ID %q, want the backend's %q", sb.ID(), bsb.ID())
	}
	if sb.Spec().Workdir != sb.Agent().Workdir || sb.Spec().Workdir != bsb.Spec().Workdir {
		t.Errorf("Workdir %q, agent %q, backend %q: want all equal", sb.Spec().Workdir, sb.Agent().Workdir, bsb.Spec().Workdir)
	}
	if !reflect.DeepEqual(sb.Spec(), bsb.Spec()) {
		t.Errorf("Spec %+v, want the effective %+v", sb.Spec(), bsb.Spec())
	}
	if !reflect.DeepEqual(sb.Agent(), bsb.Agent()) {
		t.Errorf("Agent %+v, want the relayed Hello %+v", sb.Agent(), bsb.Agent())
	}
	u, ok := sb.(unwrapper)
	if !ok {
		t.Fatalf("%T has no Unwrap", sb)
	}
	if got := reflect.TypeOf(u.Unwrap()).String(); got != "*attach.sb" {
		t.Errorf("the sandbox unwraps to %s, want attach's sandbox", got)
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, map[string]string{"a.txt": "a"}))}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := sb.Export(ctx, ".", sandbox.TarOut{W: &buf}); err != nil || buf.Len() == 0 {
		t.Errorf("TarOut Export: %v (%d bytes)", err, buf.Len())
	}
}
