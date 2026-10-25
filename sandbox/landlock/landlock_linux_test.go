package landlock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// needABI4 skips the test unless this kernel offers Landlock ABI 4.
func needABI4(t *testing.T) int {
	t.Helper()
	abi, err := defaultProbe()
	if err != nil || abi < 4 {
		t.Skipf("kernel Landlock ABI %d (%v); these tests need ABI 4", abi, err)
	}
	return abi
}

// realSandbox creates a sandbox with New over the built agent, closed at
// the end of the test, and returns it with its directory.
func realSandbox(t *testing.T, o Options, spec sandbox.Spec) (sandbox.Sandbox, string) {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	o.AgentBinary = agentBin
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, filepath.Dir(s.Spec().Workdir)
}

// execute runs command with the sandbox's execute tool and returns its exit
// code (exitCode's) and output.
func execute(t *testing.T, s sandbox.Sandbox, command string) (int, string) {
	t.Helper()
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"command": command})
	for _, tl := range ts {
		if tl.Name == "execute" {
			r := tl.Execute(context.Background(), b)
			return exitCode(r), r.Text + r.Detail
		}
	}
	t.Fatal("no execute tool")
	return 0, ""
}

// alive reports whether pid is a live, non-zombie process.
func alive(pid int) bool { e, err := readStat(pid); return err == nil && e.State != 'Z' }

// listen starts a loopback listener that sends hello to each connection it
// accepts, counting them.
func listen(t *testing.T, hello string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	n := new(atomic.Int32)
	go func() {
		for c, err := ln.Accept(); err == nil; c, err = ln.Accept() {
			n.Add(1)
			_, _ = c.Write([]byte(hello))
			_ = c.Close()
		}
	}()
	return ln.Addr().String(), n
}

// Tools write only W, HOME and TMPDIR, run only the system directories and ExtraRO,
// have no TCP; the agent leads its session, and Close kills what it left and removes all.
func TestSandboxConfinesItsTools(t *testing.T) {
	needABI4(t)
	addr, accepted := listen(t, "")
	sibling := testutil.Tree(t, map[string]string{"secret.txt": "sibling-secret"})
	realHome, _ := os.UserHomeDir()
	ro := testutil.Tree(t, map[string]string{"bin/hi": "#!/bin/sh\necho hi\n"})
	outside := script(t, sibling, "hi", "echo hi")
	if err := os.Chmod(filepath.Join(ro, "bin/hi"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := ro + "/bin:/usr/bin:/bin"
	ctx, cancel := context.WithCancel(context.Background())
	p, err := New(Options{Root: t.TempDir(), AgentBinary: agentBin, ExtraRO: []string{ro}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Create(ctx, sandbox.Spec{Env: map[string]string{"FOO": "bar", "PATH": path}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	dir := filepath.Dir(s.Spec().Workdir)
	pid := func() int { m, _ := readMarker(dir); return m.PID }()
	if e, err := readStat(pid); err != nil || e.Session != pid || func() int { g, _ := syscall.Getpgid(pid); return g }() != pid {
		t.Errorf("agent %d: %+v, %v; want it to lead its session and group", pid, e, err)
	}
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err != nil || cwd != s.Spec().Workdir {
		t.Errorf("the agent runs in %q, %v", cwd, err)
	}
	if l := s.Agent().Landlock; l.ABI != 4 || !l.TCP || !slices.Contains(l.Writable, dir+"/workspace") || !slices.Contains(l.Writable, dir+"/home") || !slices.Contains(l.Writable, dir+"/tmp") {
		t.Errorf("Agent().Landlock = %+v", l)
	}
	if _, err := s.Sync(context.Background(), sandbox.LocalDir{Path: filepath.Dir(dialtcpBin)}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		command string
		ok      bool
	}{
		{"touch ws.txt $HOME/h.txt $TMPDIR/t.txt", true},
		{"hi", true},
		{outside, false},
		{"touch " + filepath.Join(realHome, "agentkit-ll-"+s.ID()), false},
		{"cat " + sibling + "/secret.txt", false},
		{"cat " + dir + "/" + markerName, false},
		{"ls " + dir, false},
		{"./dialtcp " + addr, false},
		{"mkdir -p ro/a && touch ro/a/f && chmod -R a-w ro", true},
	} {
		if code, out := execute(t, s, c.command); (code == 0) != c.ok || !c.ok && !strings.Contains(strings.ToLower(out), "permission denied") {
			t.Errorf("%s: exit %d: %q", c.command, code, out)
		}
	}
	_, out := execute(t, s, "env")
	var env []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if k, _, _ := strings.Cut(l, "="); !slices.Contains([]string{"PWD", "OLDPWD", "SHLVL", "_"}, k) {
			env = append(env, l)
		}
	}
	if slices.Sort(env); !slices.Equal(env, []string{"FOO=bar", "HOME=" + dir + "/home", "PATH=" + path, "TMPDIR=" + dir + "/tmp"}) {
		t.Errorf("the tools' environment is %q", env)
	}
	if _, err := os.Stat(s.Spec().Workdir + "/ws.txt"); err != nil || accepted.Load() != 0 {
		t.Errorf("ws.txt on the host: %v; %d TCP connections", err, accepted.Load())
	}
	_, out = execute(t, s, "sleep 300 >/dev/null 2>&1 & echo $!")
	sleeper, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("sleep: %q", out)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("Close = %v", err)
	}
	testutil.Eventually(t, "the agent and its sleep to die", func() bool { return !alive(pid) && !alive(sleeper) })
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sandbox directory remains: %v", err)
	}
	u, _ := realSandbox(t, Options{}, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if _, err := u.Sync(context.Background(), sandbox.LocalDir{Path: filepath.Dir(dialtcpBin)}); err != nil {
		t.Fatal(err)
	}
	if code, out := execute(t, u, "./dialtcp "+addr); code != 0 || accepted.Load() != 1 {
		t.Errorf("dialtcp under NetworkUnrestricted: %d %q, %d accepted", code, out, accepted.Load())
	}
}

// The sandbox's own agent still cannot connect.
func TestGitRepoInitAgentMayConnect(t *testing.T) {
	needABI4(t)
	addr, accepted := listen(t, "hello-from-host")
	_, port, _ := net.SplitHostPort(addr)
	helpers := t.TempDir()
	helper := filepath.Join(helpers, "init.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/bash\nexec 3<>/dev/tcp/127.0.0.1/"+port+" && cat <&3 > got.txt && env > env.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pv, err := New(Options{AgentBinary: agentBin, Root: t.TempDir(), ExtraRO: []string{helpers}})
	if err != nil {
		t.Fatal(err)
	}
	p := pv.(*provider)
	p.seams.initCommand = func(sandbox.GitRepo, string) []string { return []string{helper} }
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ws := s.Spec().Workdir
	if _, err := s.Sync(context.Background(), sandbox.GitRepo{URL: "https://example.invalid/r"}); err != nil || accepted.Load() != 1 {
		t.Fatalf("Sync = %v; %d accepted", err, accepted.Load())
	}
	b, _ := os.ReadFile(filepath.Join(ws, "got.txt"))
	envb, _ := os.ReadFile(filepath.Join(ws, "env.txt"))
	var keys []string
	for _, l := range strings.Split(strings.TrimSpace(string(envb)), "\n") {
		if k, _, _ := strings.Cut(l, "="); !slices.Contains([]string{"PWD", "OLDPWD", "SHLVL", "_"}, k) {
			keys = append(keys, k)
		}
	}
	if slices.Sort(keys); string(b) != "hello-from-host" || !slices.Equal(keys, []string{"GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "HOME", "PATH", "TMPDIR"}) {
		t.Errorf("the init agent got %q with the environment %v", b, keys)
	}
	if b, err := os.ReadFile(dialtcpBin); err != nil || os.WriteFile(filepath.Join(ws, "dialtcp"), b, 0o755) != nil {
		t.Fatal(err)
	}
	if code, out := execute(t, s, "./dialtcp "+addr); code == 0 || accepted.Load() != 1 {
		t.Errorf("dialtcp from the sandbox: %d %q", code, out)
	}
}

// With AGENTKIT_SANDBOX_GIT_URL set, a real public clone fills the
// workspace of a NetworkNone sandbox.
func TestGitRepoClonesAPublicRepository(t *testing.T) {
	u := os.Getenv("AGENTKIT_SANDBOX_GIT_URL")
	if u == "" {
		t.Skip("AGENTKIT_SANDBOX_GIT_URL is not set; make sandbox-landlock-test sets it")
	}
	needABI4(t)
	s, _ := realSandbox(t, Options{}, sandbox.Spec{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := s.Sync(ctx, sandbox.GitRepo{URL: u, Depth: 1}); err != nil {
		t.Fatalf("GitRepo Sync of %s: %v", u, err)
	}
	if code, out := execute(t, s, "git rev-parse HEAD"); code != 0 || !regexp.MustCompile(`^[0-9a-f]{40}`).MatchString(out) {
		t.Errorf("git rev-parse HEAD: %d %q", code, out)
	}
}

func init() {
	children["reap"] = reapChild
	children["unconfined"] = unconfinedChild
}

// reapChild creates a sandbox on the shared Root with a background sleep,
// prints "<id> <agent pid> <sleep pid>" and waits to be killed.
func reapChild() error {
	p, err := New(Options{Root: os.Getenv("AGENTKIT_LANDLOCK_ROOT"), AgentBinary: agentBin})
	if err != nil {
		return err
	}
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		return err
	}
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ts, func(tl core.Tool) bool { return tl.Name == "execute" })
	out := ts[i].Execute(context.Background(), []byte(`{"command":"sleep 300 >/dev/null 2>&1 & echo $!"}`)).Text
	m, err := readMarker(filepath.Dir(s.Spec().Workdir))
	if err != nil {
		return err
	}
	fmt.Printf("%s %d %s\n", s.ID(), m.PID, strings.TrimSpace(out))
	select {}
}

// A sandbox left by a killed embedder is reaped with its background
// process, while a live sandbox is left alone.
func TestReapRemovesASandboxLeftByAKilledEmbedder(t *testing.T) {
	needABI4(t)
	root := t.TempDir()
	live, liveDir := realSandbox(t, Options{Root: root}, sandbox.Spec{})
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childVar+"=reap", childAgentVar+"="+agentBin, "AGENTKIT_LANDLOCK_ROOT="+root)
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var id string
	var agent, sleeper int
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if _, serr := fmt.Sscanf(line, "%s %d %d", &id, &agent, &sleeper); err != nil || serr != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("the child reported %q: %v, %v", line, err, serr)
	}
	t.Cleanup(func() { _ = syscall.Kill(sleeper, syscall.SIGKILL) })
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	testutil.Eventually(t, "the agent to exit with its embedder", func() bool { return !alive(agent) })
	p, err := New(Options{Root: root, AgentBinary: agentBin})
	if err != nil {
		t.Fatal(err)
	}
	if ids, err := p.Reap(context.Background()); err != nil || !slices.Equal(ids, []string{id}) || alive(sleeper) || !slices.Equal(entries(t, root), []string{filepath.Base(liveDir)}) {
		t.Fatalf("Reap = %v, %v; sleep alive %t; Root %v", ids, err, alive(sleeper), entries(t, root))
	}
	if code, out := execute(t, live, "true"); code != 0 {
		t.Errorf("the live sandbox after Reap: %d %q", code, out)
	}
}

// A sandbox never closed is destroyed at its TTL with its background
// process, and later calls fail as unavailable.
func TestSandboxIsDestroyedAtItsTTL(t *testing.T) {
	needABI4(t)
	s, dir := realSandbox(t, Options{}, sandbox.Spec{TTL: 2 * time.Second})
	_, out := execute(t, s, "sleep 300 >/dev/null 2>&1 & echo $!")
	sleeper, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("sleep: %q", out)
	}
	t.Cleanup(func() { _ = syscall.Kill(sleeper, syscall.SIGKILL) })
	m, _ := readMarker(dir)
	testutil.Eventually(t, "the TTL to destroy the sandbox", func() bool {
		_, err := os.Stat(dir)
		return !alive(m.PID) && !alive(sleeper) && errors.Is(err, os.ErrNotExist)
	})
	if _, err := s.Tools(context.Background(), sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Tools after the TTL = %v, want ErrUnavailable", err)
	}
}

// unconfinedChild uses a sandbox end to end, then writes outside any
// workspace and connects to the parent's listener.
func unconfinedChild() error {
	ctx := context.Background()
	root, _ := os.MkdirTemp("", "landlock-child-")
	defer os.RemoveAll(root)
	p, err := New(Options{Root: root, AgentBinary: agentBin})
	if err != nil {
		return err
	}
	s, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		return err
	}
	if _, err := s.Tools(ctx, sandbox.ToolOptions{}); err != nil {
		return err
	}
	if err := s.Close(ctx); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENTKIT_LANDLOCK_OUT"), "after-close"), nil, 0o644); err != nil {
		return err
	}
	c, err := net.DialTimeout("tcp", os.Getenv("AGENTKIT_LANDLOCK_ADDR"), 5*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// The embedder's own process stays unconfined for files and TCP after
// using the provider.
func TestEmbedderStaysUnconfined(t *testing.T) {
	needABI4(t)
	addr, accepted := listen(t, "")
	out := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childVar+"=unconfined", childAgentVar+"="+agentBin, "AGENTKIT_LANDLOCK_ADDR="+addr, "AGENTKIT_LANDLOCK_OUT="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the child: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(out, "after-close")); err != nil || accepted.Load() != 1 {
		t.Errorf("after the child closed its sandbox: %v; %d connections", err, accepted.Load())
	}
}
