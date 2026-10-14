package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/landlock"
)

// TS-18-72: the example runs on the landlock provider with --landlock and
// --agent, and its default offline run (main_test.go) is unchanged.
// 18-PATH-5, 18-REQ-10.7.
func TestExampleRunsOnLandlock_TS18_72(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the agent and runs the example; skipped under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	agent := filepath.Join(t.TempDir(), "agentkit-sandboxd")
	build := exec.Command(goBin, "build", "-o", agent, "./cmd/agentkit-sandboxd")
	build.Dir = filepath.Join("..", "..", "sandbox")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building agentkit-sandboxd: %v\n%s", err, out)
	}
	t.Setenv("TMPDIR", t.TempDir())
	gocache, err := exec.Command(goBin, "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	// The provider's default Root is beneath the user cache directory, which
	// must lie outside the system directories (on some hosts HOME is under
	// /opt): point it at a temporary one, keeping go's own build cache.
	run := exec.Command(goBin, "run", ".", "--landlock", "--agent", agent)
	run.Env = append(os.Environ(), "XDG_CACHE_HOME="+t.TempDir(), "GOCACHE="+strings.TrimSpace(string(gocache)))
	out, err := run.CombinedOutput()
	_, newErr := landlock.New(landlock.Options{AgentBinary: agent, Root: t.TempDir()})
	switch {
	case newErr == nil:
		if err != nil || !strings.Contains(string(out), "Sandbox example completed successfully") {
			t.Errorf("go run . --landlock on a Landlock host: %v\n%s", err, out)
		}
	case errors.Is(newErr, landlock.ErrNoLandlock):
		if err == nil || !strings.Contains(string(out), "landlock: this host cannot confine a sandbox with Landlock ABI 4") {
			t.Errorf("go run . --landlock without Landlock: %v\n%s", err, out)
		}
	default:
		t.Fatalf("landlock.New: %v", newErr)
	}
}
