package landlock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-26: nothing inside the sandbox can read the marker or list the
// sandbox directory. 18-REQ-4.4.
func TestMarkerUnreadable_TS18_26(t *testing.T) {
	needABI4(t)
	s, dir := realSandbox(t, newRealProvider(t, Options{}), sandbox.Spec{})
	ts := realTools(t, s)
	code, out := execute(t, ts, "cat "+dir+"/agentkit-sandbox.json")
	if code == 0 || !strings.Contains(out, "Permission denied") || strings.Contains(out, `"pid"`) {
		t.Errorf("cat the marker: %d, %q", code, out)
	}
	if code, out := execute(t, ts, "ls "+dir); code == 0 || !strings.Contains(out, "Permission denied") {
		t.Errorf("ls the sandbox directory: %d, %q", code, out)
	}
}

// TS-18-31: the real starter runs the agent as its own session leader in
// the workspace, independent of Create's context. 18-REQ-5.4.
func TestRealStarter_TS18_31(t *testing.T) {
	needABI4(t)
	p := newRealProvider(t, Options{})
	cctx, cancel := context.WithCancel(context.Background())
	s, err := p.Create(cctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	dir := filepath.Dir(s.Spec().Workdir)
	pid := agentPID(t, dir)
	if pgrp, sid := procStat(t, pid); pgrp != pid || sid != pid {
		t.Errorf("agent %d: process group %d, session %d; want both %d", pid, pgrp, sid, pid)
	}
	if cwd, err := os.Readlink("/proc/" + itoa(pid) + "/cwd"); err != nil || cwd != filepath.Join(dir, "workspace") {
		t.Errorf("agent cwd %q, %v", cwd, err)
	}
	cancel()
	ts := realTools(t, s)
	if code, out := execute(t, ts, "true"); code != 0 || !alive(pid) {
		t.Errorf("after cancelling Create's context: execute %d %q, agent alive %t", code, out, alive(pid))
	}
}

// TS-18-37: an ExtraRW containing a system directory makes the agent exit 2
// and Create report its status and stderr. 18-REQ-5.6.
func TestExtraRWSystemDir_TS18_37(t *testing.T) {
	needABI4(t)
	root := t.TempDir()
	p := newRealProvider(t, Options{Root: root, ExtraRW: []string{"/usr"}})
	s, err := p.Create(context.Background(), sandbox.Spec{})
	const prefix = "landlock: agentkit-sandboxd exited with status 2: "
	if s != nil || err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), "--landlock-rw") {
		t.Fatalf("Create = %v, %v", s, err)
	}
	if got := entries(t, root); len(got) != 0 {
		t.Errorf("Root holds %v", got)
	}
}

// TS-18-38: under NetworkUnrestricted a TCP connect from proxy execute to a
// loopback listener succeeds. 18-REQ-6.4.
func TestUnrestrictedTCP_TS18_38(t *testing.T) {
	needABI4(t)
	ln := listen(t, "")
	s, _ := realSandbox(t, newRealProvider(t, Options{}), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if _, err := s.Sync(context.Background(), sandbox.LocalDir{Path: helperDir(t)}); err != nil {
		t.Fatal(err)
	}
	if code, out := execute(t, realTools(t, s), "./dialtcp "+ln.Addr().String()); code != 0 || ln.accepted.Load() != 1 {
		t.Errorf("dialtcp: %d %q, accepted %d", code, out, ln.accepted.Load())
	}
}

// TS-18-39: Agent().Landlock reports ABI 4 with TCP and the sandbox's roots,
// and tools see exactly Spec.Env plus HOME, TMPDIR and PATH. 18-REQ-6.5,
// 18-REQ-6.6.
func TestReportAndEnv_TS18_39(t *testing.T) {
	k := needABI4(t)
	s, dir := realSandbox(t, newRealProvider(t, Options{}), sandbox.Spec{Env: map[string]string{"FOO": "bar", "MARKER": "landlock-env"}})
	ll := s.Agent().Landlock
	if k == 4 && ll.ABI != 4 || ll.ABI < 4 || !ll.TCP {
		t.Errorf("Landlock %+v on kernel ABI %d", ll, k)
	}
	for _, sub := range []string{"workspace", "home", "tmp"} {
		if !slices.Contains(ll.Writable, evalSymlinks(t, filepath.Join(dir, sub))) {
			t.Errorf("%s is not among Writable %v", sub, ll.Writable)
		}
	}
	_, out := execute(t, realTools(t, s), "env")
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if k, _, ok := strings.Cut(l, "="); ok && !slices.Contains([]string{"PWD", "OLDPWD", "SHLVL", "_"}, k) {
			got = append(got, l)
		}
	}
	slices.Sort(got)
	want := []string{"FOO=bar", "HOME=" + dir + "/home", "MARKER=landlock-env", "PATH=" + defaultPATH, "TMPDIR=" + dir + "/tmp"}
	if !slices.Equal(got, want) {
		t.Errorf("env %q, want %q", got, want)
	}
}

// systemDirs are the read-only roots of the agent's ruleset.
var systemDirs = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt"}

// TS-18-40: go version runs inside the sandbox when the go on PATH lies
// beneath a system directory or an ExtraRO directory. 18-REQ-6.7.
func TestGoReachable_TS18_40(t *testing.T) {
	needABI4(t)
	found, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	goPath := evalSymlinks(t, found)
	o := Options{}
	beneath := false
	for _, d := range systemDirs {
		if within(d, goPath) {
			beneath = true
		}
	}
	if !beneath {
		o.ExtraRO = []string{filepath.Dir(filepath.Dir(goPath))}
	}
	s, _ := realSandbox(t, newRealProvider(t, o), sandbox.Spec{Env: map[string]string{"PATH": filepath.Dir(goPath) + ":/usr/bin:/bin"}})
	if code, out := execute(t, realTools(t, s), "go version"); code != 0 || !strings.HasPrefix(out, "go version") {
		t.Errorf("go version (%s, ExtraRO %v): %d %q", goPath, o.ExtraRO, code, out)
	}
}

// TS-18-41: go version fails with a permission error when the go on PATH
// lies only beneath a directory outside the ruleset. 18-REQ-6.8.
func TestGoUnreachable_TS18_41(t *testing.T) {
	needABI4(t)
	found, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	b, err := os.ReadFile(evalSymlinks(t, found))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "go"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	s, _ := realSandbox(t, newRealProvider(t, Options{}), sandbox.Spec{Env: map[string]string{"PATH": outside + ":/usr/bin:/bin"}})
	if code, out := execute(t, realTools(t, s), "go version"); code == 0 || !strings.Contains(out, "Permission denied") {
		t.Errorf("go version from %s: %d %q", outside, code, out)
	}
}
