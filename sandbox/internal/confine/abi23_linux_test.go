//go:build linux

package confine

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-15-31 (simulated half): with the probe reporting 2 or 3, the
// filesystem ruleset applies at that ABI under none, TCP stays open, and
// the note names the missing TCP support. 15-REQ-5.6.
func TestSimulatedOldKernel_TS15_31(t *testing.T) {
	if a, ok := child(t); ok {
		d := a.Dirs
		fakeProbe(t, a.Sim, nil)
		info, err := Setup(Inputs{Workdir: d["W"], Env: []string{"HOME=" + d["H"], "TMPDIR=" + d["T"]}, Mode: sandbox.NetworkNone})
		want := fmt.Sprintf("kernel Landlock ABI %d cannot restrict TCP (needs ABI 4, Linux 6.7)", a.Sim)
		if err != nil || info.ABI != a.Sim || info.TCP || info.Note != want {
			t.Fatalf("Setup = %+v, %v; want ABI %d and %q", info, err, a.Sim, want)
		}
		if err := os.WriteFile(filepath.Join(d["W"], "a"), nil, 0o644); err != nil {
			t.Errorf("creating under W: %v", err)
		}
		denied(t, "creating S/x", os.WriteFile(filepath.Join(d["S"], "x"), nil, 0o644))
		_, err = os.ReadFile(filepath.Join(d["S"], "secret"))
		denied(t, "reading S/secret", err)
		var dl net.Dialer
		dl.SetMultipathTCP(false)
		if c, err := dl.Dial("tcp", a.Addr); err != nil {
			t.Errorf("loopback dial: %v", err)
		} else {
			_ = c.Close()
		}
		return
	}
	k := needABI(t, 2)
	l, err := net.Listen("tcp", "127.0.0.1:0")
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
	for _, sim := range []int{2, 3} {
		if k < sim {
			continue
		}
		d := dirs(t, "W", "H", "T", "S")
		write(t, filepath.Join(d["S"], "secret"), "s")
		runChild(t, t.Name(), childArgs{Dirs: d, Sim: sim, Addr: l.Addr().String()})
	}
}
