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

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The opt-in integration tests: they run the provider against a real
// podman or docker and the locally built base image. make
// sandbox-podman-test builds the images and sets the variables.

const realHint = "; run make sandbox-podman-test to build the images and run these tests"

// prereq decides whether the integration tests can run: not under -short,
// with an engine (AGENTKIT_CONTAINER when set, then podman, then docker),
// and with AGENTKIT_SANDBOX_BASE_IMAGE set. It returns the engine and the
// image, or the reason to skip.
func prereq(short bool, getenv func(string) string, lookPath func(string) (string, error)) (engine, image, skip string) {
	if short {
		return "", "", "the podman integration tests are skipped under -short" + realHint
	}
	if e := getenv("AGENTKIT_CONTAINER"); e != "" {
		if _, err := lookPath(e); err != nil {
			return "", "", fmt.Sprintf("AGENTKIT_CONTAINER=%s is not on PATH%s", e, realHint)
		}
		engine = e
	} else {
		for _, e := range []string{"podman", "docker"} {
			if _, err := lookPath(e); err == nil {
				engine = e
				break
			}
		}
		if engine == "" {
			return "", "", "no container engine (podman or docker) found" + realHint
		}
	}
	if image = getenv("AGENTKIT_SANDBOX_BASE_IMAGE"); image == "" {
		return "", "", "AGENTKIT_SANDBOX_BASE_IMAGE is not set" + realHint
	}
	return engine, image, ""
}

// realEngine skips t unless the integration prerequisites hold.
func realEngine(t *testing.T) (engine, image string) {
	t.Helper()
	engine, image, skip := prereq(testing.Short(), os.Getenv, exec.LookPath)
	if skip != "" {
		t.Skip(skip)
	}
	return engine, image
}

// newRealProvider is podman.New over the chosen engine.
func newRealProvider(t *testing.T, engine string, o Options) sandbox.Provider {
	t.Helper()
	if o.Binary == "" {
		o.Binary = engine
	}
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newRealSandbox creates a sandbox closed at the end of the test.
func newRealSandbox(t *testing.T, p sandbox.Provider, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s, err := p.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// engineLabelled is what the engine still lists with the sandbox's label:
// containers, then volumes.
func engineLabelled(t *testing.T, engine, id string) string {
	t.Helper()
	var out strings.Builder
	for _, args := range [][]string{
		{"ps", "-a", "--filter", "label=agentkit.sandbox=" + id, "-q"},
		{"volume", "ls", "--filter", "label=agentkit.sandbox=" + id, "-q"},
	} {
		b, err := exec.Command(engine, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", engine, args, err, b)
		}
		out.Write(b)
	}
	return strings.TrimSpace(out.String())
}

// realTools is the sandbox's proxy tools.
func realTools(t *testing.T, s sandbox.Sandbox) []core.Tool {
	t.Helper()
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// TS-17-53: the integration prerequisites skip under -short, without an
// engine or without the base image variable. 17-REQ-9.1.
func TestPrereq_TS17_53(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	look := func(found ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			if slices.Contains(found, n) {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		}
	}
	for _, c := range []struct {
		short          bool
		getenv         func(string) string
		lookPath       func(string) (string, error)
		engine, reason string
	}{
		{true, env("AGENTKIT_SANDBOX_BASE_IMAGE", "img"), look("podman"), "", "-short"},
		{false, env("AGENTKIT_SANDBOX_BASE_IMAGE", "img"), look(), "", "no container engine"},
		{false, env("AGENTKIT_CONTAINER", "missing-engine", "AGENTKIT_SANDBOX_BASE_IMAGE", "img"), look("podman", "docker"), "", "missing-engine"},
		{false, env(), look("docker"), "", "AGENTKIT_SANDBOX_BASE_IMAGE"},
		{false, env("AGENTKIT_SANDBOX_BASE_IMAGE", "img"), look("podman", "docker"), "podman", ""},
		{false, env("AGENTKIT_CONTAINER", "docker", "AGENTKIT_SANDBOX_BASE_IMAGE", "img"), look("podman", "docker"), "docker", ""},
		{false, env("AGENTKIT_SANDBOX_BASE_IMAGE", "img"), look("docker"), "docker", ""},
	} {
		engine, image, skip := prereq(c.short, c.getenv, c.lookPath)
		if c.reason != "" {
			if !strings.Contains(skip, c.reason) {
				t.Errorf("prereq: skip %q, want one naming %q", skip, c.reason)
			}
			continue
		}
		if skip != "" || engine != c.engine || image != "img" {
			t.Errorf("prereq = %q, %q, skip %q; want %q, img", engine, image, skip, c.engine)
		}
	}
}

// TS-17-54: a second LocalDir Sync after one host edit transfers one file,
// and Export brings the model's edits back. 17-REQ-9.3.
func TestRealResync_TS17_54(t *testing.T) {
	engine, image := realEngine(t)
	s := newRealSandbox(t, newRealProvider(t, engine, Options{}), sandbox.Spec{Image: image})
	ctx := context.Background()
	src := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(src, n), []byte(n+" v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // a different modification time, whatever the clock's resolution
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a.txt v2, edited on the host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := s.Sync(ctx, sandbox.LocalDir{Path: src})
	if err != nil || r.Files != 1 {
		t.Fatalf("second Sync = %+v, %v; want Files 1", r, err)
	}
	res := callTool(t, realTools(t, s), "edit_file", map[string]any{"path": "b.txt",
		"edits": []map[string]string{{"old_string": "b.txt v1", "new_string": "b.txt edited in the sandbox"}}})
	if !res.OK {
		t.Fatalf("edit_file: %+v", res)
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
	id := s.ID()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if left := engineLabelled(t, engine, id); left != "" {
		t.Errorf("after Close the engine still lists %s", left)
	}
}

// shellVars are the variables bash itself exports to the commands it runs;
// they are the tool's shell's, not the container's.
var shellVars = []string{"OLDPWD", "PWD", "SHLVL", "_"}

// TS-17-55: the container sees exactly Spec.Env plus the unset defaults,
// and no host path or engine-only variable leaks. 17-REQ-9.5.
func TestRealEnvironment_TS17_55(t *testing.T) {
	engine, image := realEngine(t)
	p := newRealProvider(t, engine, Options{EngineEnv: append(os.Environ(), "AGENTKIT_LEAK_MARKER=leaked")})
	s := newRealSandbox(t, p, sandbox.Spec{Image: image, Env: map[string]string{"FOO": "bar", "TERM": "xterm"}})
	ts := realTools(t, s)
	var results []string
	run := func(name string, input map[string]any) core.ToolResult {
		r := callTool(t, ts, name, input)
		results = append(results, r.Text+fmt.Sprint(r.Data)+r.Error+r.Detail)
		return r
	}
	envOut := run("execute", map[string]any{"command": "env"}).Text
	vars := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(envOut), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !slices.Contains(shellVars, k) {
			vars[k] = v
		}
	}
	// Both engines add HOSTNAME, the container's own hostname: docker
	// always, podman from version 6 even with --unsetenv-all
	// (docs/errata/17_sandbox_podman.md).
	want := []string{"FOO", "HOME", "LANG", "PATH", "TERM", "TMPDIR"}
	if h, ok := vars["HOSTNAME"]; ok {
		if hostHost, _ := os.Hostname(); h == hostHost {
			t.Errorf("HOSTNAME is the host's hostname %q", h)
		}
		want = append(want, "HOSTNAME")
	} else if p.Capabilities().Provider == "docker" {
		t.Error("docker added no HOSTNAME")
	}
	slices.Sort(want)
	var names []string
	for k := range vars {
		names = append(names, k)
	}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("the container's variables are %v, want %v:\n%s", names, want, envOut)
	}
	if vars["TERM"] != "xterm" || vars["HOME"] != "/tmp" || vars["TMPDIR"] != "/tmp" || vars["FOO"] != "bar" {
		t.Errorf("TERM %q, HOME %q, TMPDIR %q, FOO %q", vars["TERM"], vars["HOME"], vars["TMPDIR"], vars["FOO"])
	}
	if strings.Contains(envOut, "AGENTKIT_LEAK_MARKER") {
		t.Error("a variable of the engine's environment reached the container")
	}
	run("execute", map[string]any{"command": "pwd; ls -la"})
	run("write_file", map[string]any{"path": "probe.txt", "content": "probe"})
	run("read_file", map[string]any{"path": "probe.txt"})
	run("list_files", map[string]any{"path": "."})
	wd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	for _, r := range results {
		if wd != "" && strings.Contains(r, wd) || home != "" && home != "/" && strings.Contains(r, home) {
			t.Errorf("a tool result names a host path: %s", r)
		}
	}
}

// TS-17-56: a 1 GiB allocation in a 256 MiB sandbox is killed, and the
// sandbox stays usable. 17-REQ-9.6.
func TestRealMemoryBound_TS17_56(t *testing.T) {
	engine, image := realEngine(t)
	s := newRealSandbox(t, newRealProvider(t, engine, Options{}), sandbox.Spec{Image: image, Resources: sandbox.Resources{MemoryBytes: 256 << 20}})
	ts := realTools(t, s)
	r := callTool(t, ts, "execute", map[string]any{"command": `bash -c 'x=$(head -c 1073741824 /dev/zero | tr "\0" a); echo ${#x}'`})
	if r.OK || strings.Contains(r.Text, "1073741824") {
		t.Errorf("the 1 GiB allocation was not killed: %+v", r)
	}
	r2 := callTool(t, ts, "execute", map[string]any{"command": "echo ok"})
	if !r2.OK || !strings.Contains(r2.Text, "ok") {
		t.Errorf("echo ok after the allocation: %+v", r2)
	}
	if _, err := s.Tools(context.Background(), sandbox.ToolOptions{}); err != nil {
		t.Errorf("the sandbox did not stay live: %v", err)
	}
}

// TS-17-57: on podman with Landlock ABI 2 or more, writing to /var/tmp is a
// permission error, and RequireLandlock succeeds on kernel ABI 4.
// 17-REQ-9.7.
func TestRealLandlock_TS17_57(t *testing.T) {
	engine, image := realEngine(t)
	p := newRealProvider(t, engine, Options{})
	if p.Capabilities().Provider != "podman" {
		t.Skip("podman only: docker's --read-only adds no /var/tmp tmpfs")
	}
	s := newRealSandbox(t, p, sandbox.Spec{Image: image})
	l := s.Agent().Landlock
	if l.ABI < 2 {
		t.Skipf("the agent applied Landlock ABI %d (%s); this needs 2 or more", l.ABI, l.Note)
	}
	r := callTool(t, realTools(t, s), "execute", map[string]any{"command": "echo x > /var/tmp/f"})
	if !strings.Contains(r.Text, "Permission denied") || strings.Contains(r.Text, "Read-only file system") {
		t.Errorf("writing /var/tmp: %+v, want Permission denied", r)
	}
	if l.KernelABI < 4 {
		t.Skipf("the kernel offers Landlock ABI %d; the RequireLandlock part needs 4", l.KernelABI)
	}
	s2 := newRealSandbox(t, p, sandbox.Spec{Image: image, RequireLandlock: true})
	if !s2.Agent().Landlock.Satisfies(sandbox.NetworkNone) {
		t.Errorf("RequireLandlock sandbox's Landlock %+v does not satisfy NetworkNone", s2.Agent().Landlock)
	}
}

// TS-17-58: with AGENTKIT_SANDBOX_RUNTIME=krun the sandbox runs in a
// libkrun micro-VM. 17-REQ-9.8.
func TestRealKrun_TS17_58(t *testing.T) {
	engine, image := realEngine(t)
	if os.Getenv("AGENTKIT_SANDBOX_RUNTIME") != "krun" {
		t.Skip("AGENTKIT_SANDBOX_RUNTIME is not krun")
	}
	p := newRealProvider(t, engine, Options{Runtime: "krun"})
	if p.Capabilities().Isolation != sandbox.IsolationMicroVM {
		t.Fatalf("Isolation = %v, want IsolationMicroVM", p.Capabilities().Isolation)
	}
	s := newRealSandbox(t, p, sandbox.Spec{Image: image})
	if r := callTool(t, realTools(t, s), "execute", map[string]any{"command": "echo vm"}); !strings.Contains(r.Text, "vm") {
		t.Errorf("echo vm: %+v", r)
	}
	id := s.ID()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left := engineLabelled(t, engine, id); left != "" {
		t.Errorf("after Close the engine still lists %s", left)
	}
}
