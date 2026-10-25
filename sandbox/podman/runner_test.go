//go:build unix

package podman

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

const fakeEngineVar = "AGENTKIT_FAKE_ENGINE"

// TestMain makes the test binary a fake engine when AGENTKIT_FAKE_ENGINE is set.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEngineVar) == "" {
		os.Exit(m.Run())
	}
	args := os.Args[1:]
	switch {
	case len(args) > 0 && (args[0] == "volume" || args[0] == "rm"):
		os.Exit(0)
	case len(args) > 0 && args[0] == "run" && slices.Contains(args, "-i"):
		fmt.Fprint(os.Stderr, "HEAD"+strings.Repeat("x", 64<<10-8)+"TAIL")
	}
	os.Exit(1)
}

// A noisy engine's stderr is drained without blocking, and only 8 KiB kept.
func TestRealRunnerKeepsTheEngineStderrTail(t *testing.T) {
	env := []string{fakeEngineVar + "=1", "PATH=" + os.Getenv("PATH")}
	p, err := New(Options{Binary: os.Args[0], Engine: EnginePodman, EngineEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = p.Create(ctx, sandbox.Spec{Image: "img:1"})
	if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), "TAIL") || strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("Create = %v, want an error carrying the stderr tail only, before the deadline", err)
	}
	if _, tail, ok := strings.Cut(err.Error(), "engine stderr: "); !ok || len(tail) > 8192 {
		t.Errorf("the stderr part is %d bytes (found %t), want at most 8192", len(tail), ok)
	}
	if opts := shortOptions(nil); !slices.Equal(opts.Env, os.Environ()) || opts.Timeout != 2*time.Minute || opts.MaxBytes <= 0 {
		t.Errorf("shortOptions(nil) = %+v, want os.Environ(), 2 minutes and a bounded tail", opts)
	}
}

// The engine process runs in its own process group, which KillGroup ends.
func TestKillGroupEndsTheEngineProcessGroup(t *testing.T) {
	p, err := startProc([]string{"/bin/sh", "-c", "sleep 60 & wait"}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.KillGroup(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the process outlived its group kill")
	}
	if p.ExitCode() != -1 {
		t.Errorf("ExitCode = %d, want -1 for a signal", p.ExitCode())
	}
	if err := p.KillGroup(); err != nil {
		t.Errorf("killing a group already gone = %v, want nil", err)
	}
}
