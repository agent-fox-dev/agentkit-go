package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The engine-gated tests run the provider against a real podman or docker
// and the locally built base image, for what only a real container proves.

const realHint = "; run make sandbox-podman-test to build the images and run these tests"

// realProvider is New over a real engine, or skips t as the gate says.
func realProvider(t *testing.T, o Options) (sandbox.Provider, string, string) {
	t.Helper()
	candidates := []string{"podman", "docker"}
	if e := os.Getenv("AGENTKIT_CONTAINER"); e != "" {
		candidates = []string{e}
	}
	i := slices.IndexFunc(candidates, func(e string) bool { _, err := exec.LookPath(e); return err == nil })
	image := os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE")
	switch {
	case testing.Short():
		t.Skip("the podman integration tests are skipped under -short" + realHint)
	case i < 0:
		t.Skipf("no container engine %v found on PATH%s", candidates, realHint)
	case image == "":
		t.Skip("AGENTKIT_SANDBOX_BASE_IMAGE is not set" + realHint)
	}
	o.Binary = candidates[i]
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return p, candidates[i], image
}

// realSandbox is a sandbox closed when the test ends.
func realSandbox(t *testing.T, p sandbox.Provider, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func realExec(t *testing.T, s sandbox.Sandbox, command string) (ok bool, text string) {
	t.Helper()
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := callTool(t, ts, "execute", map[string]any{"command": command})
	return r.OK, r.Text
}

// labelled is what the engine lists with filter: containers, then volumes.
func labelled(t *testing.T, engine, filter string) []string {
	t.Helper()
	var all []string
	for _, args := range [][]string{{"ps", "-a", "--filter", filter, "--format", "{{.Names}}"}, {"volume", "ls", "--filter", filter, "--format", "{{.Name}}"}} {
		b, err := exec.Command(engine, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", engine, args, err, b)
		}
		all = append(all, strings.Fields(string(b))...)
	}
	return all
}

// A re-sync moves one file, .gitignore'd files stay out, Close leaves nothing.
func TestRealSandboxRoundTrip(t *testing.T) {
	p, engine, image := realProvider(t, Options{})
	src := testutil.Tree(t, map[string]string{"a.txt": "a v1\n", "b.txt": "b v1\n", ".gitignore": "build/\n", "build/out.bin": "binary"})
	s := realSandbox(t, p, sandbox.Spec{Image: image})
	ctx := context.Background()
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatal(err)
	}
	a, later := filepath.Join(src, "a.txt"), time.Now().Add(time.Hour)
	if err := errors.Join(os.WriteFile(a, []byte("a edited on the host\n"), 0o644), os.Chtimes(a, later, later)); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil || r.Files != 1 {
		t.Fatalf("second Sync = %+v, %v; want Files 1", r, err)
	}
	if ok, text := realExec(t, s, "echo 'b edited in the sandbox' > b.txt && ls"); !ok || strings.Contains(text, "build") {
		t.Fatalf("execute: %t, %q", ok, text)
	}
	out := t.TempDir()
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: out}); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]string{"a.txt": "edited on the host", "b.txt": "edited in the sandbox"} {
		if b, err := os.ReadFile(filepath.Join(out, n)); err != nil || !strings.Contains(string(b), want) {
			t.Errorf("exported %s = %q, %v; want %q", n, b, err, want)
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, engine, "label=agentkit.sandbox="+s.ID()); len(left) != 0 {
		t.Errorf("after Close the engine still lists %v", left)
	}
}

// Only Spec.Env and the defaults reach the container, and memory is bounded.
func TestRealSandboxIsConfinedToItsSpec(t *testing.T) {
	p, _, image := realProvider(t, Options{EngineEnv: append(os.Environ(), "AGENTKIT_LEAK_MARKER=leaked")})
	s := realSandbox(t, p, sandbox.Spec{Image: image, Env: map[string]string{"FOO": "bar", "TERM": "xterm"}, Resources: sandbox.Resources{MemoryBytes: 256 << 20}})
	_, envOut := realExec(t, s, "env; pwd")
	vars := map[string]string{}
	for _, l := range strings.Split(envOut, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !slices.Contains([]string{"OLDPWD", "PWD", "SHLVL", "_", "HOSTNAME"}, k) {
			vars[k] = v // the shell's own variables and the engine's HOSTNAME aside
		}
	}
	want := map[string]string{"FOO": "bar", "HOME": "/tmp", "LANG": "C.UTF-8", "PATH": defaultPATH, "TERM": "xterm", "TMPDIR": "/tmp"}
	if wd, _ := os.Getwd(); fmt.Sprint(vars) != fmt.Sprint(want) || strings.Contains(envOut, wd) {
		t.Errorf("the container's environment is %v, want %v and no host path:\n%s", vars, want, envOut)
	}
	if ok, text := realExec(t, s, `bash -c 'x=$(head -c 1073741824 /dev/zero | tr "\0" a); echo ${#x}'`); ok || strings.Contains(text, "1073741824") {
		t.Errorf("the 1 GiB allocation was not killed: %q", text)
	}
	if ok, text := realExec(t, s, "echo ok"); !ok || !strings.Contains(text, "ok") {
		t.Errorf("echo ok after the allocation: %q", text)
	}
}

func TestRealGitRepoClonesWithoutGivingTheSandboxANetwork(t *testing.T) {
	p, engine, image := realProvider(t, Options{})
	url := os.Getenv("AGENTKIT_SANDBOX_GIT_URL")
	if url == "" {
		url = "https://github.com/agent-fox-dev/agentkit-go"
	}
	s := realSandbox(t, p, sandbox.Spec{Image: image})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := s.Sync(ctx, sandbox.GitRepo{URL: url, Depth: 1}); err != nil {
		t.Fatalf("GitRepo Sync of %s = %v", url, err)
	}
	if ok, text := realExec(t, s, "grep '^module ' go.mod"); !ok || text == "" {
		t.Errorf("go.mod of the clone: %t, %q", ok, text)
	}
	if ok, text := realExec(t, s, "bash -c 'exec 3<>/dev/tcp/1.1.1.1/443'"); ok {
		t.Errorf("a TCP connect from the sandbox succeeded: %q", text)
	}
	if left := labelled(t, engine, "name=agentkit-"+s.ID()+"-init"); len(left) != 0 {
		t.Errorf("the init container remains: %v", left)
	}
}
