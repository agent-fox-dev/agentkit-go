package landlock

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
)

// The provider's sandbox forwards to the attached sandbox and unwraps to
// it, so attach.ClientConn and attach.Live reach it.
func TestSandboxForwardsToTheAttachedSandbox(t *testing.T) {
	f := newInProc()
	s, err := newTestProvider(t, Options{}, f.with).Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	ls := s.(*landlockSandbox)
	inner := ls.Unwrap()
	if inner != ls.inner || attach.ClientConn(s) == nil || attach.ClientConn(s) != attach.ClientConn(inner) || attach.Live(s) != nil {
		t.Errorf("Unwrap() = %v, ClientConn %v, Live %v", inner, attach.ClientConn(s), attach.Live(s))
	}
	if s.ID() != inner.ID() || !reflect.DeepEqual(s.Spec(), inner.Spec()) || !reflect.DeepEqual(s.Agent(), inner.Agent()) {
		t.Error("ID, Spec or Agent differs from the attached sandbox's")
	}
	if err := s.Export(context.Background(), ".", sandbox.LocalDir{Path: t.TempDir()}); err != nil {
		t.Errorf("Export: %v", err)
	}
	if err := s.Close(context.Background()); err != nil || !errors.Is(attach.Live(inner), sandbox.ErrClosed) {
		t.Errorf("Close = %v, then Live %v", err, attach.Live(inner))
	}
	<-ls.agentDone()
}
