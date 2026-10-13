package podman

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-40: a never-closed sandbox is destroyed by attach's host-side TTL
// timer. 17-REQ-6.6.
func TestTTLDestroys_TS17_40(t *testing.T) {
	f := newFake(t)
	s, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{Image: "img:1", TTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	deadline := time.Now().Add(10 * time.Second)
	for !f.ran("volume", "rm", "agentkit-"+id) {
		if time.Now().After(deadline) {
			t.Fatalf("no destroy step 10s after a 1s TTL: %v", f.recorded())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !f.ran("rm", "-f", "agentkit-"+id) || !f.ran("rm", "-f", "agentkit-"+id+"-init") {
		t.Error("the TTL's destroy step did not remove the containers")
	}
	ctx := context.Background()
	gone := func(err error) bool {
		return errors.Is(err, sandbox.ErrUnavailable) || errors.Is(err, sandbox.ErrClosed)
	}
	if _, err := s.Tools(ctx, sandbox.ToolOptions{}); !gone(err) {
		t.Errorf("Tools after the TTL = %v", err)
	}
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); !gone(err) {
		t.Errorf("Sync after the TTL = %v", err)
	}
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: t.TempDir()}); !gone(err) {
		t.Errorf("Export after the TTL = %v", err)
	}
}

// TS-17-41: an agent that ends early makes the sandbox unavailable and
// keeps the volume until Close. 17-REQ-6.8.
func TestAgentEndsEarly_TS17_41(t *testing.T) {
	f := newFake(t)
	s, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{Image: "img:1"})
	if err != nil {
		t.Fatal(err)
	}
	f.lastProc().exit(0)
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := s.Tools(ctx, sandbox.ToolOptions{})
		if errors.Is(err, sandbox.ErrUnavailable) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Tools after the agent ended = %v, want ErrUnavailable", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Sync = %v, want ErrUnavailable", err)
	}
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: t.TempDir()}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Export = %v, want ErrUnavailable", err)
	}
	if f.ran("volume", "rm") {
		t.Fatal("the volume was removed before Close")
	}
	_ = s.Close(ctx)
	if !f.ran("volume", "rm", "agentkit-"+s.ID()) {
		t.Error("Close did not remove the volume")
	}
}
