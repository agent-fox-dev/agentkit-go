package landlock

import (
	"bufio"
	"context"
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
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-67: an embedder creates a confined sandbox, runs commands that
// write the workspace but cannot reach home, a sibling directory or TCP, and
// closes it. 18-PATH-1, 18-REQ-6.1, 18-REQ-6.2, 18-REQ-6.3.
func TestSmokeConfined_TS18_67(t *testing.T) {
	needABI4(t)
	ctx := context.Background()
	ln := listen(t, "")
	sibling := t.TempDir()
	if err := os.WriteFile(filepath.Join(sibling, "secret.txt"), []byte("sibling-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{AgentBinary: agentBin, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.Spec().Workdir)
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: helperDir(t)}); err != nil {
		t.Fatal(err)
	}
	ts := realTools(t, s)
	if code, out := execute(t, ts, "touch ws.txt"); code != 0 {
		t.Errorf("touch ws.txt: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace", "ws.txt")); err != nil {
		t.Errorf("ws.txt is not on the host: %v", err)
	}
	if code, out := execute(t, ts, "touch $HOME/h.txt $TMPDIR/t.txt"); code != 0 {
		t.Errorf("touch in HOME and TMPDIR: %d %q", code, out)
	}
	homeFile := filepath.Join(realHome, "agentkit-ll-"+s.ID()+".txt")
	if code, out := execute(t, ts, "touch "+homeFile); code == 0 || !strings.Contains(out, "Permission denied") {
		t.Errorf("touch in the real home: %d %q", code, out)
	}
	if _, err := os.Stat(homeFile); err == nil {
		_ = os.Remove(homeFile)
		t.Error("a file appeared in the real home")
	}
	if code, out := execute(t, ts, "cat "+sibling+"/secret.txt"); code == 0 || !strings.Contains(out, "Permission denied") || strings.Contains(out, "sibling-secret") {
		t.Errorf("cat the sibling secret: %d %q", code, out)
	}
	if code, out := execute(t, ts, "./dialtcp "+ln.Addr().String()); code == 0 || ln.accepted.Load() != 0 {
		t.Errorf("dialtcp under NetworkNone: %d %q, accepted %d", code, out, ln.accepted.Load())
	}
	if err := s.Close(ctx); err != nil {
		t.Errorf("Close = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sandbox directory remains: %v", err)
	}
}

// TS-18-68: a GitRepo Sync runs its init command in a confined init agent
// that may use TCP while the sandbox's own agent still cannot. 18-PATH-2,
// 18-REQ-8.4, 18-REQ-8.5.
func TestSmokeInitAgent_TS18_68(t *testing.T) {
	needABI4(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	ctx := context.Background()
	ln := listen(t, "hello-from-host")
	helpers := t.TempDir()
	port := ln.Addr().(*net.TCPAddr).Port
	helper := filepath.Join(helpers, "init.sh")
	body := "#!/bin/bash\nexec 3<>/dev/tcp/127.0.0.1/" + strconv.Itoa(port) + " && cat <&3 > got.txt && env > env.txt\n"
	if err := os.WriteFile(helper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	pv, err := New(Options{AgentBinary: agentBin, Root: t.TempDir(), ExtraRO: []string{helpers}})
	if err != nil {
		t.Fatal(err)
	}
	p := pv.(*provider)
	var initPID int
	start := p.seams.start
	p.seams.start = func(req startReq) (*agentProc, net.Conn, error) {
		h, c, err := start(req)
		if err == nil && slices.Contains(req.args, "unrestricted") {
			initPID = h.pid
		}
		return h, c, err
	}
	p.seams.initCommand = func(sandbox.GitRepo, string) []string { return []string{helper} }
	s, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	ws := s.Spec().Workdir
	res, err := s.Sync(ctx, sandbox.GitRepo{URL: "https://example.invalid/r"})
	if err != nil || res != (sandbox.SyncResult{}) || ln.accepted.Load() != 1 {
		t.Fatalf("Sync = %+v, %v; accepted %d", res, err, ln.accepted.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "got.txt")); string(b) != "hello-from-host" {
		t.Errorf("got.txt = %q", b)
	}
	envb, _ := os.ReadFile(filepath.Join(ws, "env.txt"))
	var keys []string
	for _, l := range strings.Split(strings.TrimSpace(string(envb)), "\n") {
		if k, _, ok := strings.Cut(l, "="); ok && !slices.Contains([]string{"PWD", "OLDPWD", "SHLVL", "_"}, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	if want := []string{"GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "HOME", "PATH", "TMPDIR"}; !slices.Equal(keys, want) {
		t.Errorf("the init environment is %v, want %v", keys, want)
	}
	if initPID == 0 || len(sessionPIDs(t, initPID)) != 0 {
		t.Errorf("the init agent %d's session still has %v", initPID, sessionPIDs(t, initPID))
	}
	if err := os.WriteFile(filepath.Join(ws, "dialtcp"), mustRead(t, dialtcpBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, out := execute(t, realTools(t, s), "./dialtcp "+ln.Addr().String()); code == 0 || ln.accepted.Load() != 1 {
		t.Errorf("dialtcp from the sandbox: %d %q, accepted %d", code, out, ln.accepted.Load())
	}
}

// TS-18-69: with AGENTKIT_SANDBOX_GIT_URL set, a real public clone under
// NetworkNone fills the workspace and the sandbox still cannot connect over
// TCP. 18-PATH-2, 18-REQ-10.6.
func TestSmokePublicClone_TS18_69(t *testing.T) {
	u := os.Getenv("AGENTKIT_SANDBOX_GIT_URL")
	if u == "" {
		t.Skip("AGENTKIT_SANDBOX_GIT_URL is not set; make sandbox-landlock-test sets it")
	}
	needABI4(t)
	ctx := context.Background()
	ln := listen(t, "")
	p := newRealProvider(t, Options{})
	s, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.Spec().Workdir)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := s.Sync(cctx, sandbox.GitRepo{URL: u, Depth: 1}); err != nil {
		t.Fatalf("GitRepo Sync of %s: %v", u, err)
	}
	if fi, err := os.Stat(filepath.Join(s.Spec().Workdir, ".git")); err != nil || !fi.IsDir() {
		t.Errorf("no .git in the workspace: %v", err)
	}
	ts := realTools(t, s)
	if code, out := execute(t, ts, "git -C . rev-parse HEAD"); code != 0 || !regexp.MustCompile(`^[0-9a-f]{40}`).MatchString(out) {
		t.Errorf("git rev-parse HEAD: %d %q", code, out)
	}
	if err := os.WriteFile(filepath.Join(s.Spec().Workdir, "dialtcp"), mustRead(t, dialtcpBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, out := execute(t, ts, "./dialtcp "+ln.Addr().String()); code == 0 || ln.accepted.Load() != 0 {
		t.Errorf("dialtcp after the clone: %d %q", code, out)
	}
	if err := s.Close(ctx); err != nil {
		t.Errorf("Close = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sandbox directory remains: %v", err)
	}
}

func init() { children["reap"] = reapChild }

// reapChild creates a sandbox on the shared Root, starts a background sleep,
// reports "<id> <agent pid> <sleep pid>" and waits to be killed.
func reapChild() error {
	ctx := context.Background()
	p, err := New(Options{Root: os.Getenv("AGENTKIT_LANDLOCK_ROOT"), AgentBinary: agentBin})
	if err != nil {
		return err
	}
	s, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		return err
	}
	ts, err := s.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		return err
	}
	var out string
	for _, tl := range ts {
		if tl.Name == "execute" {
			out = tl.Execute(ctx, []byte(`{"command":"sleep 300 >/dev/null 2>&1 & echo $!"}`)).Text
		}
	}
	m, err := readMarker(filepath.Dir(s.Spec().Workdir))
	if err != nil {
		return err
	}
	fmt.Printf("%s %d %s\n", s.ID(), m.PID, strings.TrimSpace(out))
	select {}
}

// TS-18-70: a sandbox left by a killed embedder is reaped with its
// outliving background process, while a live sandbox is left alone.
// 18-PATH-3, 18-REQ-7.8, 18-REQ-9.6, 18-REQ-9.3.
func TestSmokeReap_TS18_70(t *testing.T) {
	needABI4(t)
	ctx := context.Background()
	root := t.TempDir()
	live, liveDir := realSandbox(t, newRealProvider(t, Options{Root: root}), sandbox.Spec{})
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childVar+"=reap", childAgentVar+"="+agentBin, "AGENTKIT_LANDLOCK_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("the child reported %q: %v", line, err)
	}
	var id string
	var agentPID, sleepPID int
	if _, err := fmt.Sscanf(line, "%s %d %d", &id, &agentPID, &sleepPID); err != nil {
		t.Fatalf("the child reported %q: %v", line, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(sleepPID, syscall.SIGKILL) })
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	if !eventually(10*time.Second, func() bool { return !alive(agentPID) }) {
		t.Fatalf("the agent %d outlived its embedder", agentPID)
	}
	dir := filepath.Join(root, id)
	if m, err := readMarker(dir); err != nil || m.Role != "agent" {
		t.Fatalf("the left sandbox's marker: %+v, %v", m, err)
	}
	ids, err := newRealProvider(t, Options{Root: root}).Reap(ctx)
	if err != nil || !slices.Contains(ids, id) || slices.Contains(ids, live.ID()) {
		t.Fatalf("Reap = %v, %v; want %s and not %s", ids, err, id, live.ID())
	}
	if alive(sleepPID) {
		t.Errorf("the sleep %d outlived Reap", sleepPID)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the reaped directory remains: %v", err)
	}
	if _, err := os.Stat(liveDir); err != nil {
		t.Errorf("the live sandbox's directory is gone: %v", err)
	}
	if code, out := execute(t, realTools(t, live), "true"); code != 0 {
		t.Errorf("the live sandbox: %d %q", code, out)
	}
}

// TS-18-71: a sandbox never closed is destroyed at its 5-second TTL with its
// background process, and later calls fail as unavailable. 18-PATH-4,
// 18-REQ-7.7.
func TestSmokeTTL_TS18_71(t *testing.T) {
	needABI4(t)
	ctx := context.Background()
	p := newRealProvider(t, Options{})
	s, err := p.Create(ctx, sandbox.Spec{TTL: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.Spec().Workdir)
	ts := realTools(t, s)
	_, out := execute(t, ts, "sleep 300 >/dev/null 2>&1 & echo $!")
	sleepPID, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("sleep: %q", out)
	}
	t.Cleanup(func() { _ = syscall.Kill(sleepPID, syscall.SIGKILL) })
	agent := agentPID(t, dir)
	if !eventually(10*time.Second, func() bool {
		_, err := os.Stat(dir)
		return !alive(agent) && !alive(sleepPID) && errors.Is(err, os.ErrNotExist)
	}) {
		t.Fatalf("10s after creation: agent alive %t, sleep alive %t", alive(agent), alive(sleepPID))
	}
	if _, err := s.Tools(ctx, sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("Tools after the TTL = %v, want ErrUnavailable", err)
	}
}

// sessionPIDs are the live processes whose session id is sid.
func sessionPIDs(t *testing.T, sid int) []int {
	t.Helper()
	procs, err := defaultProcTable().list()
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, e := range procs {
		if e.Session == sid && e.State != 'Z' {
			out = append(out, e.PID)
		}
	}
	return out
}

// mustRead reads p, failing t.
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
