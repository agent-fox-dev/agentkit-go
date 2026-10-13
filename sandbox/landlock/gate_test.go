package landlock

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// onLinux reports whether the tests run on Linux.
func onLinux() bool { return runtime.GOOS == "linux" }

// abiGate skips tb unless probe reports Landlock ABI 4 or later, naming what
// it found, and returns the ABI.
func abiGate(tb testing.TB, probe func() (int, error)) int {
	tb.Helper()
	abi, err := probe()
	if err != nil {
		tb.Skipf("Landlock unavailable: %v", err)
		return 0
	}
	if abi < 4 {
		tb.Skipf("kernel Landlock ABI %d, these tests need ABI 4", abi)
		return 0
	}
	return abi
}

// recTB records a skip instead of ending the test.
type recTB struct {
	testing.TB
	skipped bool
	msg     string
}

func (r *recTB) Helper() {}
func (r *recTB) Skipf(format string, args ...any) {
	r.skipped, r.msg = true, fmt.Sprintf(format, args...)
}

// TS-18-65: the Linux test gate skips below ABI 4 naming the ABI it found.
// 18-REQ-10.4.
func TestABIGate_TS18_65(t *testing.T) {
	tb := &recTB{TB: t}
	abiGate(tb, func() (int, error) { return 3, nil })
	if !tb.skipped || !strings.Contains(tb.msg, "3") || !strings.Contains(tb.msg, "4") {
		t.Errorf("ABI 3: skipped %t, %q", tb.skipped, tb.msg)
	}
	tb = &recTB{TB: t}
	abiGate(tb, func() (int, error) { return 0, syscall.ENOSYS })
	if !tb.skipped || !strings.Contains(tb.msg, syscall.ENOSYS.Error()) {
		t.Errorf("ENOSYS: skipped %t, %q", tb.skipped, tb.msg)
	}
	tb = &recTB{TB: t}
	if k := abiGate(tb, func() (int, error) { return 4, nil }); tb.skipped || k != 4 {
		t.Errorf("ABI 4: skipped %t, returned %d", tb.skipped, k)
	}
}
