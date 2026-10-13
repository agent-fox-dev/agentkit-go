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

// TestMain makes the test binary a fake engine when AGENTKIT_FAKE_ENGINE is
// set (the helper-process pattern): volume and rm commands exit 0; `run -i`
// writes 64 KiB to stderr, from HEAD to TAIL, without reading stdin, and
// exits 1; any other run (the diagnostic) exits 1.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEngineVar) == "" {
		os.Exit(m.Run())
	}
	args := os.Args[1:]
	switch {
	case len(args) > 0 && (args[0] == "volume" || args[0] == "rm"):
		os.Exit(0)
	case len(args) > 0 && args[0] == "run" && slices.Contains(args, "-i"):
		body := "HEAD" + strings.Repeat("x", 64<<10-8) + "TAIL"
		fmt.Fprint(os.Stderr, body)
		os.Exit(1)
	}
	os.Exit(1)
}

// TS-17-31: the real runner drains a noisy engine's stderr without
// blocking and keeps only its last 8 KiB; short commands use EngineEnv
// with a 2-minute timeout; the engine process gets its own process group.
// 17-REQ-5.2.
func TestRealRunnerStderrTail_TS17_31(t *testing.T) {
	env := []string{fakeEngineVar + "=stderr64k", "PATH=" + os.Getenv("PATH")}
	p, err := New(Options{Binary: os.Args[0], Engine: EnginePodman, EngineEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = p.Create(ctx, sandbox.Spec{Image: "img:1"})
	if ctx.Err() != nil {
		t.Fatalf("Create did not return before the deadline: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "TAIL") || strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("Create = %v, want an error carrying the stderr tail only", err)
	}
	if _, tail, ok := strings.Cut(err.Error(), "engine stderr: "); !ok || len(tail) > 8192 {
		t.Errorf("the stderr part is %d bytes (found %t), want at most 8192", len(tail), ok)
	}

	opts := shortOptions(env)
	if !slices.Equal(opts.Env, env) || opts.Timeout != 2*time.Minute || opts.MaxBytes <= 0 {
		t.Errorf("shortOptions = %+v", opts)
	}
	if def := shortOptions(nil); !slices.Equal(def.Env, os.Environ()) {
		t.Error("shortOptions(nil).Env is not os.Environ()")
	}
	if attr := sysProcAttr(); attr == nil || !attr.Setpgid {
		t.Errorf("the engine process's SysProcAttr is %+v, want Setpgid", attr)
	}
}
