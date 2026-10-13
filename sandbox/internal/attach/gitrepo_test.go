package attach

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
)

// rpcCounts is the fake agent's count of every RPC.
func rpcCounts(f *fakeagent.Agent) map[string]int {
	m := map[string]int{}
	for _, rpc := range []string{"Hello", "Tools", "Call", "Rsync", "SyncTar", "ExportTar"} {
		m[rpc] = f.Count(rpc)
	}
	return m
}

// TS-17-7: attach's Sync refuses a GitRepo with ErrUnsupported naming the
// provider, makes no RPC and leaves the sandbox live. 17-REQ-1.7.
func TestSyncRefusesGitRepo_TS17_7(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{ID: "x", Provider: "test-provider"})
	before := rpcCounts(f)
	_, err := sb.Sync(context.Background(), sandbox.GitRepo{URL: "https://h/r"})
	if !errors.Is(err, sandbox.ErrUnsupported) || !strings.Contains(err.Error(), "test-provider") {
		t.Fatalf("Sync(GitRepo) = %v, want an error matching ErrUnsupported naming test-provider", err)
	}
	after := rpcCounts(f)
	for rpc, n := range before {
		if after[rpc] != n {
			t.Errorf("%s ran %d times after the refused Sync, %d before", rpc, after[rpc], n)
		}
	}
	if err := Live(sb); err != nil {
		t.Errorf("Live after the refusal = %v, want nil", err)
	}
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err != nil {
		t.Errorf("Tools after the refusal: %v", err)
	}
}

// TS-17-9: Live reports live, unavailable and closed exactly as the gate
// Tools uses does. 17-REQ-1.8.
func TestLive_TS17_9(t *testing.T) {
	ctx := context.Background()
	f := fakeagent.New()
	sb := attachFake(t, f, Config{ID: "live"})
	if err := Live(sb); err != nil {
		t.Fatalf("Live of a live sandbox = %v, want nil", err)
	}

	f.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for !errors.Is(Live(sb), sandbox.ErrUnavailable) {
		if time.Now().After(deadline) {
			t.Fatalf("Live after the agent ended = %v, want ErrUnavailable", Live(sb))
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, toolsErr := sb.Tools(ctx, sandbox.ToolOptions{})
	if got := Live(sb); toolsErr == nil || got.Error() != toolsErr.Error() {
		t.Errorf("Live = %v, Tools' gate error = %v; want the same", got, toolsErr)
	}

	f2 := fakeagent.New()
	sb2 := attachFake(t, f2, Config{ID: "closed"})
	if err := sb2.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := Live(sb2); got != sandbox.ErrClosed {
		t.Errorf("Live after Close = %v, want ErrClosed", got)
	}
	if _, err := sb2.Tools(ctx, sandbox.ToolOptions{}); err != sandbox.ErrClosed {
		t.Errorf("Tools after Close = %v, want ErrClosed as Live reports", err)
	}
}
