//go:build linux

package confine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// A test that applies a ruleset does it in a child, the test binary run again
// for that test, so only the child is confined (and it creates no t.TempDir).
const childVar, childDone = "AGENTKIT_CONFINE_CHILD", "CONFINE-CHILD-COMPLETE"

// args are what a parent hands a child.
type args struct {
	Dirs map[string]string
	ABI  int // the kernel's
	Sim  int // the ABI the probe reports instead, when not 0
	Mode sandbox.NetworkMode
}

// kernelABI probes Landlock independently of the package:
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION), number
// 444 on amd64 and arm64.
func kernelABI(t *testing.T, lo, hi int) int {
	t.Helper()
	r, _, errno := syscall.Syscall(444, 0, 0, 1)
	if errno != 0 || int(r) < lo || int(r) > hi {
		t.Skipf("kernel Landlock ABI %d (%v); the test needs %d to %d", r, errno, lo, hi)
	}
	return int(r)
}

// runChild runs the test again in a child with a, and requires exit status
// 0 and the completion marker, so a skipped child fails.
func runChild(t *testing.T, a args) {
	t.Helper()
	b, _ := json.Marshal(a)
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), childVar+"="+string(b))
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), childDone) {
		t.Fatalf("child %+v: %v\n%s", a, err, out)
	}
}

// child reports whether this process is a child, with its args; a child that
// passes prints the completion marker.
func child(t *testing.T) (args, bool) {
	t.Helper()
	var a args
	if os.Getenv(childVar) == "" {
		return a, false
	}
	if err := json.Unmarshal([]byte(os.Getenv(childVar)), &a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			fmt.Println(childDone)
		}
	})
	return a, true
}

// denied fails unless err is EACCES.
func denied(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("%s: %v, want EACCES", what, err)
	}
}

// Setup fails before probing on an unsafe flag, and before applying when the
// ruleset cannot be built.
func TestSetupFailsBeforeApplying(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	var ue *UsageError
	calls := fakeProbe(t, 4, nil)
	if _, err := Setup(Inputs{Workdir: "/"}); !errors.As(err, &ue) || *calls != 0 {
		t.Errorf("Setup(--workdir /) = %v after %d probes, want a usage error and no probe", err, *calls)
	}
	if info, err := Setup(Inputs{Workdir: gone}); err == nil || !strings.Contains(err.Error(), gone) {
		t.Errorf("Setup with a missing workspace = %+v, %v", info, err)
	}
}

// A failed probe, or a kernel offering only ABI 1, is no error: nothing is
// applied and the outcome says why.
func TestSetupAppliesNothingWithoutAUsableABI(t *testing.T) {
	d := dirs(t, "W", "S")
	for _, c := range []struct {
		abi int
		err error
	}{{1, nil}, {0, syscall.ENOSYS}, {0, syscall.EPERM}} {
		calls := fakeProbe(t, c.abi, c.err)
		info, err := Setup(Inputs{Workdir: d["W"], Mode: sandbox.NetworkNone})
		want := sandbox.LandlockInfo{KernelABI: c.abi, Note: Plan(c.abi, c.err, sandbox.NetworkNone).Note}
		if err != nil || !reflect.DeepEqual(info, want) || *calls != 1 {
			t.Errorf("probe %d/%v: Setup = %+v, %v; want %+v", c.abi, c.err, info, err, want)
		}
	}
	testutil.WriteTree(t, d["S"], map[string]string{"x": ""})
}

// Under the ruleset the writable roots accept create, cross-directory
// rename, truncate and execute; everything else is read-only or denied with
// EACCES, on every thread and in every later process, with no_new_privs set.
// The ABI applied is exactly the one chosen (V2 does not handle truncate),
// and TCP bind and connect are denied at ABI 4 under NetworkNone only.
func TestSetupConfinesTheProcess(t *testing.T) {
	if a, ok := child(t); ok {
		d, abi := a.Dirs, min(a.ABI, 4)
		if a.Sim != 0 {
			fakeProbe(t, a.Sim, nil)
			abi = a.Sim
		}
		start, result := make(chan struct{}), make(chan error)
		go func() {
			runtime.LockOSThread()
			<-start
			result <- os.WriteFile(filepath.Join(d["S"], "thread"), nil, 0o644)
		}()
		info, err := Setup(Inputs{Workdir: d["W"], Env: []string{"HOME=" + d["H"], "TMPDIR=" + d["T"]},
			RW: []string{d["R"]}, RO: []string{d["O"]}, Mode: a.Mode})
		tcp := abi >= 4 && a.Mode == sandbox.NetworkNone
		note := Plan(abi, nil, a.Mode).Note
		if err != nil || info.ABI != abi || info.TCP != tcp || info.Note != note || info.Writable[0] != resolved(d["W"])[0] {
			t.Fatalf("Setup = %+v, %v; want ABI %d, TCP %v and %q", info, err, abi, tcp, note)
		}
		for _, k := range []string{"W", "H", "T", "R"} {
			if err := os.WriteFile(filepath.Join(d[k], "new"), nil, 0o644); err != nil {
				t.Errorf("creating under %s: %v", k, err)
			}
		}
		if err := os.Rename(filepath.Join(d["W"], "a", "f"), filepath.Join(d["W"], "b", "f")); err != nil {
			t.Errorf("rename across directories: %v", err)
		}
		if err := syscall.Truncate(filepath.Join(d["W"], "b", "f"), 0); err != nil {
			t.Errorf("truncate in W: %v", err)
		}
		if err := syscall.Truncate(filepath.Join(d["O"], "ro"), 0); abi == 2 && err != nil {
			t.Errorf("truncate in O under V2: %v (V2 does not handle truncate)", err)
		} else if abi > 2 {
			denied(t, "truncate in O", err)
		}
		script := filepath.Join(d["W"], "s.sh")
		_ = os.WriteFile(script, []byte("#!/bin/sh\necho x > /dev/null\n"), 0o755)
		if err := exec.Command(script).Run(); err != nil {
			t.Errorf("executing a script in W: %v", err)
		}
		for _, p := range []string{filepath.Join(d["O"], "ro"), "/etc/passwd", "/proc/self/status"} {
			if _, err := os.ReadFile(p); err != nil {
				t.Errorf("reading %s: %v", p, err)
			}
		}
		denied(t, "creating S/new", os.WriteFile(filepath.Join(d["S"], "new"), nil, 0o644))
		denied(t, "creating O/new", os.WriteFile(filepath.Join(d["O"], "new"), nil, 0o644))
		denied(t, "writing /proc/self/comm", os.WriteFile("/proc/self/comm", []byte("x"), 0o644))
		_, err = os.ReadFile(filepath.Join(d["S"], "secret"))
		denied(t, "reading S/secret", err)
		close(start)
		denied(t, "a thread started before Setup creating S/thread", <-result)
		if r, _, _ := syscall.Syscall(syscall.SYS_PRCTL, 39 /* PR_GET_NO_NEW_PRIVS */, 0, 0); r != 1 {
			t.Errorf("PR_GET_NO_NEW_PRIVS = %d", r)
		}
		if exec.Command("/bin/sh", "-c", "touch "+filepath.Join(d["S"], "y")).Run() == nil {
			t.Error("a later process created S/y")
		}
		var lc net.ListenConfig
		lc.SetMultipathTCP(false)
		l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if tcp {
			denied(t, "bind", err)
			_, err = net.Dial("tcp", "127.0.0.1:1")
			denied(t, "connect", err)
		} else if err != nil {
			t.Errorf("bind under %v: %v", a.Mode, err)
		} else if _, err := net.Dial("tcp", l.Addr().String()); err != nil {
			t.Errorf("connect under %v: %v", a.Mode, err)
		}
		return
	}
	k := kernelABI(t, 2, 99)
	runs := []args{{Mode: sandbox.NetworkNone}, {Mode: sandbox.NetworkAllowlist}, {Mode: sandbox.NetworkUnrestricted}}
	for sim := 2; sim <= min(k, 3); sim++ {
		runs = append(runs, args{Sim: sim, Mode: sandbox.NetworkNone})
	}
	for _, a := range runs {
		d := dirs(t, "W/b", "H", "T", "R")
		testutil.WriteTree(t, d["base"], map[string]string{"S/secret": "s", "O/ro": "r", "W/a/f": "f"})
		d["W"], d["O"], d["S"] = filepath.Join(d["base"], "W"), filepath.Join(d["base"], "O"), filepath.Join(d["base"], "S")
		a.Dirs, a.ABI = d, k
		runChild(t, a)
	}
}

// On a kernel offering exactly ABI 2 or 3, Setup with the probe reporting 4
// fails instead of applying a weaker ruleset.
func TestSetupFailsRatherThanWeakenTheRuleset(t *testing.T) {
	if a, ok := child(t); ok {
		fakeProbe(t, 4, nil)
		if info, err := Setup(Inputs{Workdir: a.Dirs["W"], Mode: sandbox.NetworkNone}); err == nil {
			t.Errorf("Setup with V4 on an older kernel = %+v, nil", info)
		}
		return
	}
	kernelABI(t, 2, 3)
	runChild(t, args{Dirs: dirs(t, "W")})
}
