package landlock

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
)

// versionLine is a valid --version answer.
const versionLine = "agentkit-sandboxd v0.0.0-test (protocol 1.2)\n"

// placeholderAgent is an executable file standing for agentkit-sandboxd
// where the version run is faked.
func placeholderAgent(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agentkit-sandboxd")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// testSeams are Linux seams with probe abi and a version runner that answers
// versionLine and counts its runs.
func testSeams(abi int, versionRuns *int) seams {
	return seams{goos: "linux", probe: func() (int, error) { return abi, nil },
		runVersion: func(string) (string, error) {
			if versionRuns != nil {
				*versionRuns++
			}
			return versionLine, nil
		},
		executable: func() (string, error) { return "/nonexistent/test.binary", nil },
		lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		resolver:   func(string) string { return "" },
		remove:     removeTree,
	}
}

// countingProbe returns a probe that reports abi and err, and counts calls.
func countingProbe(abi int, err error, calls *int) func() (int, error) {
	return func() (int, error) { *calls++; return abi, err }
}

// TS-18-3: New on a non-Linux GOOS refuses with ErrNoLandlock naming the
// GOOS and sandbox/podman, without probing or creating anything.
// 18-REQ-1.2.
func TestNewRefusesNonLinux_TS18_3(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "root")
	calls := 0
	p, err := newProvider(Options{Root: missing, AgentBinary: "/bin/true"}, seams{goos: "darwin", probe: countingProbe(4, nil, &calls)})
	if p != nil || !errors.Is(err, ErrNoLandlock) || !strings.Contains(err.Error(), "darwin") || !strings.Contains(err.Error(), "sandbox/podman") {
		t.Fatalf("newProvider(darwin) = %v, %v; want nil and ErrNoLandlock naming darwin and sandbox/podman", p, err)
	}
	if calls != 0 {
		t.Errorf("the probe ran %d times", calls)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Root was created: %v", err)
	}
	if goos := os.Getenv("GOOS"); goos == "" && !onLinux() {
		if _, err := New(Options{Root: missing}); !errors.Is(err, ErrNoLandlock) {
			t.Errorf("New on this host = %v, want ErrNoLandlock", err)
		}
	}
}

// TS-18-5: a probed kernel ABI from 0 to 3 makes New refuse with
// ErrNoLandlock naming the ABI found, ABI 4, Linux 6.7 and sandbox/podman.
// 18-REQ-1.4.
func TestNewRefusesOldABI_TS18_5(t *testing.T) {
	for abi := 0; abi <= 3; abi++ {
		calls := 0
		p, err := newProvider(Options{Root: t.TempDir()}, seams{goos: "linux", probe: countingProbe(abi, nil, &calls)})
		if p != nil || !errors.Is(err, ErrNoLandlock) {
			t.Fatalf("ABI %d: %v, %v; want ErrNoLandlock", abi, p, err)
		}
		for _, want := range []string{"ABI " + strconv.Itoa(abi), "ABI 4", "Linux 6.7", "sandbox/podman"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ABI %d: %q does not contain %q", abi, err, want)
			}
		}
		if calls != 1 {
			t.Errorf("ABI %d: the probe ran %d times, want 1", abi, calls)
		}
	}
}

// TS-18-6: a failing probe makes New refuse with ErrNoLandlock naming the
// errno and its hint. 18-REQ-1.5.
func TestNewProbeErrors_TS18_6(t *testing.T) {
	hints := map[syscall.Errno]string{syscall.ENOSYS: "no Landlock", syscall.EOPNOTSUPP: "lsm=", syscall.EPERM: "seccomp"}
	for _, e := range []syscall.Errno{syscall.ENOSYS, syscall.EOPNOTSUPP, syscall.EPERM, syscall.EINVAL} {
		calls := 0
		p, err := newProvider(Options{Root: t.TempDir()}, seams{goos: "linux", probe: countingProbe(0, e, &calls)})
		if p != nil || !errors.Is(err, ErrNoLandlock) || !strings.Contains(err.Error(), e.Error()) {
			t.Fatalf("%v: %v, %v; want ErrNoLandlock naming the errno", e, p, err)
		}
		if h, ok := hints[e]; ok && !strings.Contains(err.Error(), h) {
			t.Errorf("%v: %q lacks the hint %q", e, err, h)
		}
		if e == syscall.EINVAL {
			for _, h := range hints {
				if strings.Contains(err.Error(), h) {
					t.Errorf("EINVAL: %q carries the hint %q", err, h)
				}
			}
		}
	}
}

// TS-18-2: New runs its checks in order, stops at the first failure and runs
// no process but the version check. 18-REQ-1.1.
func TestNewOrder_TS18_2(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "root")
	runs := 0
	if _, err := newProvider(Options{Root: missing, ExtraRO: []string{"rel"}}, testSeams(3, &runs)); !errors.Is(err, ErrNoLandlock) {
		t.Errorf("(a) = %v, want the ABI error", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("(a) created Root")
	}
	aFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(aFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newProvider(Options{Root: aFile, ExtraRO: []string{"rel"}}, testSeams(4, &runs)); err == nil || !strings.Contains(err.Error(), aFile) || strings.Contains(err.Error(), "ExtraRO") {
		t.Errorf("(b) = %v, want an error naming Root, not ExtraRO", err)
	}
	if _, err := newProvider(Options{Root: t.TempDir(), ExtraRO: []string{"rel"}}, testSeams(4, &runs)); err == nil || !strings.Contains(err.Error(), "Options.ExtraRO[0]") {
		t.Errorf("(c) = %v, want an error naming Options.ExtraRO[0]", err)
	}
	if runs != 0 {
		t.Errorf("the version ran %d times before the agent step", runs)
	}
	starts := 0
	s := testSeams(4, &runs)
	s.start = func(startReq) (*agentProc, net.Conn, error) { starts++; return nil, nil, errors.New("not started") }
	if p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: placeholderAgent(t)}, s); err != nil || p == nil || runs != 1 || starts != 0 {
		t.Errorf("(d) = %v, %v after %d version runs and %d starts; want a provider after 1 and 0", p, err, runs, starts)
	}
}

// TS-18-4: on Linux New probes exactly once and continues for any ABI of 4
// or more; the default probe is the syscall-444 version query. 18-REQ-1.3.
func TestNewProbesOnce_TS18_4(t *testing.T) {
	for _, abi := range []int{4, 5, 6} {
		calls := 0
		s := testSeams(abi, nil)
		s.probe = countingProbe(abi, nil, &calls)
		if p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: placeholderAgent(t)}, s); err != nil || p == nil || calls != 1 {
			t.Errorf("ABI %d: %v, %v after %d probes; want a provider after 1", abi, p, err, calls)
		}
	}
	if onLinux() {
		r, e := rawLandlockABI()
		got, gerr := defaultProbe()
		if e != 0 && !errors.Is(gerr, e) || e == 0 && got != r {
			t.Errorf("defaultProbe = %d, %v; the raw query gives %d, %v", got, gerr, r, e)
		}
	}
}

// TS-18-8: New resolves Root to an absolute, symlink-free directory, using
// the user cache default when empty and creating it with mode 0700.
// 18-REQ-2.1.
func TestNewRoot_TS18_8(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes and the cache directory are Unix-specific")
	}
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	ucd, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	newRoot := func(root string) string {
		t.Helper()
		p, err := newProvider(Options{Root: root, AgentBinary: placeholderAgent(t)}, testSeams(4, nil))
		if err != nil {
			t.Fatalf("Root %q: %v", root, err)
		}
		return p.root
	}
	got := newRoot("")
	if want := evalSymlinks(t, filepath.Join(ucd, "agentkit-sandbox-landlock")); got != want || perm(t, got) != 0o700 {
		t.Errorf("default Root = %s (mode %o), want %s (0700)", got, perm(t, got), want)
	}
	tmp := t.TempDir()
	t.Chdir(tmp)
	if err := os.Mkdir("rel", 0o700); err != nil {
		t.Fatal(err)
	}
	if got := newRoot("rel"); got != evalSymlinks(t, filepath.Join(tmp, "rel")) {
		t.Errorf("relative Root = %s", got)
	}
	dir, link := t.TempDir(), filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if got := newRoot(link); got != evalSymlinks(t, dir) {
		t.Errorf("symlinked Root = %s, want %s", got, evalSymlinks(t, dir))
	}
	missing := filepath.Join(t.TempDir(), "a", "b")
	newRoot(missing)
	if fi, err := os.Stat(missing); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("a missing Root was not created 0700: %v, %v", fi, err)
	}
}

// TS-18-9: a Root that is not a directory or cannot be created makes New
// fail with a plain error naming Root. 18-REQ-2.2.
func TestNewBadRoot_TS18_9(t *testing.T) {
	aFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(aFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{aFile, filepath.Join(aFile, "sub")} {
		runs := 0
		p, err := newProvider(Options{Root: r, AgentBinary: placeholderAgent(t)}, testSeams(4, &runs))
		if p != nil || err == nil || !strings.Contains(err.Error(), "Root") || !strings.Contains(err.Error(), r) || errors.Is(err, ErrNoLandlock) || runs != 0 {
			t.Errorf("Root %s: %v, %v after %d version runs", r, p, err, runs)
		}
	}
}

// TS-18-10: a relative or / ExtraRO or ExtraRW entry makes New fail naming
// the field and its index, without ErrNoLandlock. 18-REQ-2.3.
func TestNewExtraRelative_TS18_10(t *testing.T) {
	abs := t.TempDir()
	for _, c := range []struct {
		o    Options
		want string
	}{
		{Options{ExtraRO: []string{abs, "rel/dir"}}, "Options.ExtraRO[1]"},
		{Options{ExtraRO: []string{"./x"}}, "Options.ExtraRO[0]"},
		{Options{ExtraRW: []string{"/"}}, "Options.ExtraRW[0]"},
	} {
		runs := 0
		c.o.Root, c.o.AgentBinary = t.TempDir(), placeholderAgent(t)
		p, err := newProvider(c.o, testSeams(4, &runs))
		if p != nil || err == nil || !strings.Contains(err.Error(), c.want) || errors.Is(err, ErrNoLandlock) || runs != 0 {
			t.Errorf("%+v: %v, %v after %d version runs; want an error naming %s", c.o, p, err, runs, c.want)
		}
	}
}

// TS-18-11: an extra entry that equals, contains or lies beneath Root,
// after resolution or cleaning, is refused naming it and Root. 18-REQ-2.4.
func TestNewExtraOverlapsRoot_TS18_11(t *testing.T) {
	r := evalSymlinks(t, t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(r, link); err != nil {
		t.Fatal(err)
	}
	for _, o := range []Options{
		{ExtraRO: []string{r}}, {ExtraRW: []string{filepath.Dir(r)}}, {ExtraRO: []string{filepath.Join(r, "missing", "sub")}},
		{ExtraRO: []string{link}}, {ExtraRO: []string{r + "/../" + filepath.Base(r) + "/x"}},
	} {
		o.Root, o.AgentBinary = r, placeholderAgent(t)
		p, err := newProvider(o, testSeams(4, nil))
		if p != nil || err == nil || !strings.Contains(err.Error(), r) || errors.Is(err, ErrNoLandlock) ||
			!strings.Contains(err.Error(), "Options.ExtraRO[0]") && !strings.Contains(err.Error(), "Options.ExtraRW[0]") {
			t.Errorf("%+v: %v, %v; want a refusal naming the entry and Root", o, p, err)
		}
	}
	if _, err := newProvider(Options{Root: r, ExtraRO: []string{r + "-other"}, AgentBinary: placeholderAgent(t)}, testSeams(4, nil)); err != nil {
		t.Errorf("the sibling %s-other was refused: %v", r, err)
	}
}

// TS-18-13: with no usable agent binary New fails, without ErrNoLandlock,
// listing every path tried and the install command. 18-REQ-2.6.
func TestNewNoAgent_TS18_13(t *testing.T) {
	exeDir := t.TempDir()
	s := testSeams(4, nil)
	s.executable = func() (string, error) { return filepath.Join(exeDir, "test.binary"), nil }
	_, err := newProvider(Options{Root: t.TempDir()}, s)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(evalSymlinks(t, exeDir), "agentkit-sandboxd")) ||
		!strings.Contains(err.Error(), "PATH") || !strings.Contains(err.Error(), installLine(buildver.Version())) || errors.Is(err, ErrNoLandlock) {
		t.Errorf("no agent: %v", err)
	}
	aDir := t.TempDir()
	if _, err := newProvider(Options{Root: t.TempDir(), AgentBinary: aDir}, s); err == nil || !strings.Contains(err.Error(), aDir) || errors.Is(err, ErrNoLandlock) {
		t.Errorf("AgentBinary a directory: %v", err)
	}
	if got := installLine("(devel)"); got != "cd sandbox && go install ./cmd/agentkit-sandboxd" {
		t.Errorf("installLine((devel)) = %q", got)
	}
	if got := installLine("v1.4.0"); got != "go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@v1.4.0" {
		t.Errorf("installLine(v1.4.0) = %q", got)
	}
}

// evalSymlinks is filepath.EvalSymlinks, failing t on error.
func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// perm is p's permission bits.
func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}
