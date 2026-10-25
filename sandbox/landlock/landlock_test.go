package landlock

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// It refuses with ErrNoLandlock before creating anything; the probe runs once.
func TestNewRefusesAHostWithoutLandlockABI4(t *testing.T) {
	for _, c := range []struct {
		goos  string
		abi   int
		err   error
		want  []string
		probe int // the probe runs this many times
	}{
		{"darwin", 4, nil, []string{"GOOS darwin has no Landlock", "sandbox/podman"}, 0},
		{"linux", 0, nil, []string{"kernel Landlock ABI 0", "ABI 4 (Linux 6.7)", "sandbox/podman"}, 1},
		{"linux", 3, nil, []string{"kernel Landlock ABI 3", "ABI 4 (Linux 6.7)"}, 1},
		{"linux", 0, syscall.ENOSYS, []string{"probing Landlock: function not implemented (the kernel has no Landlock)"}, 1},
		{"linux", 0, syscall.EOPNOTSUPP, []string{"(Landlock is not enabled in lsm=)"}, 1},
		{"linux", 0, syscall.EPERM, []string{"(the call is blocked, typically by a container's seccomp filter)"}, 1},
		{"linux", 0, syscall.EINVAL, []string{"probing Landlock: invalid argument"}, 1},
	} {
		calls, root := 0, filepath.Join(t.TempDir(), "root")
		s := seams{goos: c.goos, probe: func() (int, error) { calls++; return c.abi, c.err }}
		p, err := newProvider(Options{Root: root}, s)
		if p != nil || !errors.Is(err, ErrNoLandlock) || calls != c.probe {
			t.Errorf("%s ABI %d %v: %v, %v after %d probes", c.goos, c.abi, c.err, p, err, calls)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s ABI %d %v: %q lacks %q", c.goos, c.abi, c.err, err, w)
			}
		}
		if c.err == syscall.EINVAL && strings.Contains(err.Error(), "(") {
			t.Errorf("EINVAL carries a hint: %q", err)
		}
		if _, serr := os.Stat(root); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("%s ABI %d: Root was created", c.goos, c.abi)
		}
	}
	if runtime.GOOS != "linux" {
		if _, err := New(Options{Root: t.TempDir()}); !errors.Is(err, ErrNoLandlock) {
			t.Errorf("New on %s = %v, want ErrNoLandlock", runtime.GOOS, err)
		}
	}
}

// Platform, ABI, Root, ExtraRO, ExtraRW, then the agent; any ABI from 4 is accepted.
func TestNewChecksInOrderAndStartsOnlyTheVersionRun(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		o    Options
		want string
	}{
		{Options{Root: file, ExtraRO: []string{"rel"}}, "Options.Root"},
		{Options{Root: t.TempDir(), ExtraRO: []string{"rel"}, ExtraRW: []string{"rel"}}, "Options.ExtraRO[0]"},
		{Options{Root: t.TempDir(), ExtraRW: []string{"rel"}, AgentBinary: filepath.Join(file, "x")}, "Options.ExtraRW[0]"},
	} {
		runs := 0
		if _, err := newProvider(c.o, testSeams(4, &runs)); err == nil || !strings.HasPrefix(err.Error(), "landlock: "+c.want) || runs != 0 {
			t.Errorf("%+v: %v after %d version runs; want an error naming %s first", c.o, err, runs, c.want)
		}
	}
	for _, abi := range []int{4, 6} {
		runs, starts := 0, 0
		s := testSeams(abi, &runs)
		s.start = func(startReq) (*agentProc, net.Conn, error) { starts++; return nil, nil, errors.New("not started") }
		agent := script(t, t.TempDir(), "agentkit-sandboxd", "exit 0")
		if p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: agent}, s); err != nil || p.bin != agent || runs != 1 || starts != 0 {
			t.Errorf("ABI %d: %v after %d version runs and %d starts", abi, err, runs, starts)
		}
	}
}
