package gatewayd_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-19-45: Shutdown ends in-flight relayed calls, closes every backend
// sandbox, makes Serve return nil and is idempotent. 19-REQ-6.8.
func TestShutdown_TS19_45(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb1 := create(t, p, unrestrictedSpec)
	create(t, p, unrestrictedSpec)
	exec := toolOf(t, sb1, "execute")
	res := make(chan string, 1)
	go func() { res <- exec.Execute(ctx, []byte(`{"command":"sleep 30"}`)).Error }()
	time.Sleep(300 * time.Millisecond)
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := env.Server.Shutdown(sctx); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	select {
	case code := <-res:
		if code != "sandbox_unavailable" {
			t.Errorf("the in-flight execute ended with %q, want sandbox_unavailable", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight execute did not end within 5 s")
	}
	if err := <-env.Served; err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	for _, s := range be.Sandboxes() {
		if !gone(s.Dir()) {
			t.Errorf("backend sandbox %s is still there", s.ID())
		}
	}
	if _, err := gateway.Dial(ctx, gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: env.Listener.Dial}); err == nil {
		t.Error("a Dial after Shutdown succeeded")
	}
	if err := env.Server.Shutdown(sctx); err != nil {
		t.Errorf("a second Shutdown = %v", err)
	}

	be2 := gwtest.NewCounting()
	be2.SetCloseErr(errors.New("close failed"))
	env2 := gwtest.Start(t, gatewayd.Config{Backend: be2, NetworkModes: unrestricted})
	rawCreate(t, env2.RawGateway(t), gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	if err := env2.Server.Shutdown(sctx); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Errorf("Shutdown over a failing Close = %v, want an error carrying it", err)
	}
}
