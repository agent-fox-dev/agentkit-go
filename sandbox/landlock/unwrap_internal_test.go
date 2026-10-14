package landlock

import (
	"context"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
)

// TS-19-53: the landlock wrapper unwraps to its attached sandbox, so
// attach.ClientConn and attach.Live reach it. 19-REQ-7.8.
func TestUnwrap_TS19_53(t *testing.T) {
	f := fakeagent.New()
	inner, err := attach.Dial(context.Background(), f.Serve(), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inner.Close(context.Background()); f.Stop() })
	w := &landlockSandbox{inner: inner}
	if got := w.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %v, want the attached sandbox", got)
	}
	if cc := attach.ClientConn(w); cc == nil || cc != attach.ClientConn(inner) {
		t.Fatalf("attach.ClientConn(wrapper) = %v, want the attached sandbox's", cc)
	}
	if err := attach.Live(w); err != nil {
		t.Fatalf("attach.Live(wrapper) = %v", err)
	}
}
