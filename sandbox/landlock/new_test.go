package landlock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

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
