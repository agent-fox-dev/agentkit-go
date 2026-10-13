//go:build linux

package confine

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// dirs creates the named directories under one parent and returns them.
func dirs(t *testing.T, names ...string) map[string]string {
	t.Helper()
	base := t.TempDir()
	out := map[string]string{}
	for _, n := range names {
		out[n] = filepath.Join(base, n)
		if err := os.MkdirAll(out[n], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func inputs(d map[string]string, mode sandbox.NetworkMode) Inputs {
	return Inputs{Workdir: d["W"], Env: []string{"HOME=" + d["H"], "TMPDIR=" + d["T"]},
		RW: []string{d["R"]}, RO: []string{d["O"]}, Mode: mode}
}

// denied fails unless err is EACCES.
func denied(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("%s: %v, want EACCES", what, err)
	}
}

// TS-15-2: under the applied ruleset, the writable roots accept create,
// cross-directory rename, truncate and execute, and everything outside is
// denied with EACCES. 15-REQ-1.1, 15-REQ-1.2.
func TestRulesetEffect_TS15_2(t *testing.T) {
	if a, ok := child(t); ok {
		d := a.Dirs
		info, err := Setup(inputs(d, sandbox.NetworkUnrestricted))
		if err != nil || info.ABI < 2 {
			t.Fatalf("Setup = %+v, %v", info, err)
		}
		for _, k := range []string{"W", "H", "T", "R"} {
			if err := os.WriteFile(filepath.Join(d[k], "new"), []byte("n"), 0o644); err != nil {
				t.Errorf("creating under %s: %v", k, err)
			}
		}
		if err := os.Rename(filepath.Join(d["W"], "a", "f"), filepath.Join(d["W"], "b", "f")); err != nil {
			t.Errorf("rename across directories: %v", err)
		}
		if info.ABI >= 3 {
			if err := syscall.Truncate(filepath.Join(d["W"], "b", "f"), 0); err != nil {
				t.Errorf("truncate: %v", err)
			}
		}
		script := filepath.Join(d["W"], "s.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command(script).Run(); err != nil {
			t.Errorf("executing a script in W: %v", err)
		}
		denied(t, "creating S/new", os.WriteFile(filepath.Join(d["S"], "new"), nil, 0o644))
		denied(t, "creating O/new", os.WriteFile(filepath.Join(d["O"], "new"), nil, 0o644))
		denied(t, "writing /proc/self/comm", os.WriteFile("/proc/self/comm", []byte("x"), 0o644))
		_, err = os.ReadFile(filepath.Join(d["S"], "secret"))
		denied(t, "reading S/secret", err)
		for _, p := range []string{filepath.Join(d["O"], "ro.txt"), "/etc/passwd", "/proc/self/status"} {
			if _, err := os.ReadFile(p); err != nil {
				t.Errorf("reading %s: %v", p, err)
			}
		}
		for _, c := range []string{"true", "echo x > /dev/null"} {
			if err := exec.Command("/bin/sh", "-c", c).Run(); err != nil {
				t.Errorf("/bin/sh -c %q: %v", c, err)
			}
		}
		return
	}
	needABI(t, 2)
	d := dirs(t, "W", "H", "T", "R", "O", "S", "W/a", "W/b")
	write(t, filepath.Join(d["S"], "secret"), "s")
	write(t, filepath.Join(d["O"], "ro.txt"), "r")
	write(t, filepath.Join(d["W/a"], "f"), "f")
	runChild(t, t.Name(), childArgs{Dirs: d})
}

// TS-15-3: at applied ABI 4 the ruleset denies TCP bind and connect under
// none, loopback included, and leaves TCP alone under allowlist and
// unrestricted. 15-REQ-1.3, 15-REQ-1.4.
func TestRulesetTCP_TS15_3(t *testing.T) {
	if a, ok := child(t); ok {
		mode := sandbox.NetworkMode(a.Mode)
		info, err := Setup(inputs(a.Dirs, mode))
		if err != nil {
			t.Fatal(err)
		}
		none := mode == sandbox.NetworkNone
		if info.TCP != none {
			t.Errorf("TCP %v under %v", info.TCP, mode)
		}
		var d net.Dialer
		d.SetMultipathTCP(false)
		c, err := d.Dial("tcp", a.Addr)
		if none {
			denied(t, "dial", err)
		} else if err != nil {
			t.Errorf("dial: %v", err)
		} else {
			_ = c.Close()
		}
		var lc net.ListenConfig
		lc.SetMultipathTCP(false)
		l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if none {
			denied(t, "bind", err)
		} else if err != nil {
			t.Errorf("bind: %v", err)
		} else {
			_ = l.Close()
		}
		return
	}
	needABI(t, 4)
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	d := dirs(t, "W", "H", "T", "R", "O")
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted} {
		runChild(t, t.Name(), childArgs{Dirs: d, Addr: l.Addr().String(), Mode: int(mode)})
	}
	if n := accepted.Load(); n != 2 {
		t.Errorf("the listener accepted %d connections, want 2 (allowlist and unrestricted)", n)
	}
}

// TS-15-4 (applying half): with missing system, device and flag paths the
// apply returns nil, and the workspace stays writable. 15-REQ-1.5.
func TestMissingPathsApply_TS15_4(t *testing.T) {
	if a, ok := child(t); ok {
		replaceLists(t, []string{"/nonexistent-agentkit-sys"}, []string{"/dev/nonexistent-agentkit"})
		info, err := Setup(Inputs{Workdir: a.Dirs["W"], RO: []string{a.Dirs["M1"]}, RW: []string{a.Dirs["M2"]}})
		if err != nil || info.ABI < 2 {
			t.Fatalf("Setup = %+v, %v", info, err)
		}
		if err := os.WriteFile(filepath.Join(a.Dirs["W"], "new"), nil, 0o644); err != nil {
			t.Errorf("creating under W: %v", err)
		}
		return
	}
	needABI(t, 2)
	d := dirs(t, "W")
	d["M1"], d["M2"] = filepath.Join(d["W"], "..", "m1"), filepath.Join(d["W"], "..", "m2")
	runChild(t, t.Name(), childArgs{Dirs: d})
}

// TS-15-9 (probe half): Setup probes exactly once per call; a simulated
// ABI 1 or probe error applies nothing, and a simulated 2 or 3 applies that
// version. 15-REQ-2.3.
func TestProbeOnce_TS15_9(t *testing.T) {
	if a, ok := child(t); ok {
		if a.Sim == 0 {
			for _, c := range []struct {
				abi int
				err error
			}{{1, nil}, {0, syscall.ENOSYS}} {
				calls := fakeProbe(t, c.abi, c.err)
				info, err := Setup(Inputs{Workdir: a.Dirs["W"]})
				if err != nil || info.ABI != 0 || *calls != 1 {
					t.Errorf("probe %d/%v: %+v, %v, %d probes", c.abi, c.err, info, err, *calls)
				}
			}
			if err := os.WriteFile(filepath.Join(a.Dirs["S"], "x"), nil, 0o644); err != nil {
				t.Errorf("creating outside every root after ABI 0: %v", err)
			}
			return
		}
		calls := fakeProbe(t, a.Sim, nil)
		info, err := Setup(Inputs{Workdir: a.Dirs["W"]})
		if err != nil || info.ABI != a.Sim || *calls != 1 {
			t.Errorf("simulated %d: %+v, %v, %d probes", a.Sim, info, err, *calls)
		}
		return
	}
	k := needABI(t, 0)
	d := dirs(t, "W", "S")
	runChild(t, t.Name(), childArgs{Dirs: d})
	for _, sim := range []int{2, 3} {
		if k >= sim {
			runChild(t, t.Name(), childArgs{Dirs: d, Sim: sim})
		}
	}
}

// TS-15-10: applying sets no_new_privs, reaches every thread and every later
// process, and uses exactly the chosen version rather than the kernel's
// best. 15-REQ-2.4.
func TestApplyStrict_TS15_10(t *testing.T) {
	if a, ok := child(t); ok {
		d := a.Dirs
		// Explicit HOME and TMPDIR: the default /tmp would hold S.
		in := Inputs{Workdir: d["W"], Env: []string{"HOME=" + d["H"], "TMPDIR=" + d["T"]}, RO: []string{d["O"]},
			Mode: sandbox.NetworkMode(a.Mode)}
		switch a.Sim {
		case 0:
			start, result := make(chan struct{}), make(chan error)
			go func() {
				runtime.LockOSThread()
				<-start
				result <- os.WriteFile(filepath.Join(d["S"], "x"), nil, 0o644)
			}()
			info, err := Setup(in)
			if err != nil || info.ABI < 2 {
				t.Fatalf("Setup = %+v, %v", info, err)
			}
			if n := noNewPrivs(); n != 1 {
				t.Errorf("PR_GET_NO_NEW_PRIVS = %d", n)
			}
			close(start)
			denied(t, "the pinned thread creating S/x", <-result)
			if err := exec.Command("/bin/sh", "-c", "touch "+filepath.Join(d["S"], "y")).Run(); err == nil {
				t.Error("a grandchild created S/y")
			}
			if _, err := os.Lstat(filepath.Join(d["S"], "y")); err == nil {
				t.Error("S/y exists")
			}
		case 2, 3:
			fakeProbe(t, a.Sim, nil)
			info, err := Setup(in)
			if err != nil || info.ABI != a.Sim {
				t.Fatalf("Setup = %+v, %v", info, err)
			}
			terr := syscall.Truncate(filepath.Join(d["O"], "f"), 0)
			if a.Sim == 2 {
				if terr != nil {
					t.Errorf("truncate under V2: %v (V2 does not handle truncate)", terr)
				}
				denied(t, "creating O/new", os.WriteFile(filepath.Join(d["O"], "new"), nil, 0o644))
			} else {
				denied(t, "truncate under V3", terr)
			}
			if a.Addr != "" {
				if info.TCP {
					t.Error("the report says TCP under a simulated ABI 3")
				}
				var dl net.Dialer
				dl.SetMultipathTCP(false)
				c, err := dl.Dial("tcp", a.Addr)
				if err != nil {
					t.Errorf("dial under V3 paths only: %v", err)
				} else {
					_ = c.Close()
				}
			}
		}
		return
	}
	k := needABI(t, 2)
	d := dirs(t, "W", "H", "T", "O", "S")
	write(t, filepath.Join(d["O"], "f"), "content")
	runChild(t, t.Name(), childArgs{Dirs: d})
	if k >= 3 {
		runChild(t, t.Name(), childArgs{Dirs: d, Sim: 2})
		write(t, filepath.Join(d["O"], "f"), "content")
		runChild(t, t.Name(), childArgs{Dirs: d, Sim: 3})
	}
	if k >= 4 {
		var lc net.ListenConfig
		lc.SetMultipathTCP(false)
		l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		runChild(t, t.Name(), childArgs{Dirs: d, Sim: 3, Addr: l.Addr().String(), Mode: int(sandbox.NetworkNone)})
	}
}
