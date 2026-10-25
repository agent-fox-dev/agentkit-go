package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/landlock"
)

// On local, with provider/faux: the agent reads and searches inside the
// sandbox, the guard refuses rm, src comes out into the host directory, and
// the deferred Close removes the sandbox's directory.
func TestTheOfflineRunWorksInTheSandboxExportsAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX ls")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	tmp, dir := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var b strings.Builder
	if err := run(context.Background(), &b, dir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read_file src/main.go in the sandbox: ok=true",
		"README: Says hello from inside the sandbox.", "TODOs: 2", "src: main.go, util.go",
		"rm: blocked_by_policy", "exported src: main.go, util.go", "Sandbox example completed successfully"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	for _, name := range []string{"main.go", "util.go"} {
		if _, err := os.Stat(filepath.Join(dir, "out", name)); err != nil {
			t.Errorf("the exported %s is not in the output directory: %v", name, err)
		}
	}
	if ents, err := os.ReadDir(filepath.Join(tmp, "agentkit-sandbox-local")); len(ents) > 0 || (err != nil && !errors.Is(err, os.ErrNotExist)) {
		t.Errorf("%d sandbox directories remain after the deferred Close (%v)", len(ents), err)
	}
}

func TestTheProgramRunsOffline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX ls")
	}
	env := testutil.Environ("ANTHROPIC_API_KEY")
	env["TMPDIR"] = t.TempDir()
	if res := testutil.GoRun(t, "", ".", env); res.Exit != 0 || !strings.Contains(res.Stdout, "Sandbox example completed successfully") {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}

// --landlock runs the same program with the agent as a confined child process
// on a host with Landlock ABI 4, and fails saying so on one without.
func TestTheProgramRunsOnLandlockOrSaysTheHostCannot(t *testing.T) {
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
	gocache, err := exec.Command(goBin, "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	// The provider's default Root is beneath the user cache directory, which
	// must lie outside the system directories: point it at a temporary one,
	// keeping go's own build cache.
	run := exec.Command(goBin, "run", ".", "--landlock", "--agent", agent)
	run.Env = append(os.Environ(), "TMPDIR="+t.TempDir(), "XDG_CACHE_HOME="+t.TempDir(), "GOCACHE="+strings.TrimSpace(string(gocache)))
	out, runErr := run.CombinedOutput()
	switch _, err := landlock.New(landlock.Options{AgentBinary: agent, Root: t.TempDir()}); {
	case err == nil:
		if runErr != nil || !strings.Contains(string(out), "Sandbox example completed successfully") {
			t.Errorf("go run . --landlock on a Landlock host: %v\n%s", runErr, out)
		}
	case errors.Is(err, landlock.ErrNoLandlock):
		if runErr == nil || !strings.Contains(string(out), "landlock: this host cannot confine a sandbox with Landlock ABI 4") {
			t.Errorf("go run . --landlock without Landlock: %v\n%s", runErr, out)
		}
	default:
		t.Fatalf("landlock.New: %v", err)
	}
}

// With a real engine and AGENTKIT_SANDBOX_BASE_IMAGE, --podman runs the same
// program in a container with no network.
func TestTheProgramRunsOnPodman(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the example against a container engine; skipped under -short")
	}
	image := os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE")
	if image == "" {
		t.Skip("AGENTKIT_SANDBOX_BASE_IMAGE is not set; run make build-containers and set it to the base image")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not on PATH; the example's --podman uses podman.Options{}, whose engine is podman")
	}
	cmd := exec.Command("go", "run", ".", "--podman", "--image", image)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), " on podman: ") || !strings.Contains(string(out), "Sandbox example completed successfully") {
		t.Fatalf("go run . --podman: %v\n%s", err, out)
	}
}
