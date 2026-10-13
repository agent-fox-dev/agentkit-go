//go:build linux

package confine

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
)

// TS-15-11: a probe failure is not fatal: the outcome is ABI 0 with the
// unavailable note, nothing is applied, and an agent given that outcome
// serves and reports it. 15-REQ-2.5.
func TestProbeFailureContinues_TS15_11(t *testing.T) {
	probeErrs := []error{syscall.ENOSYS, syscall.EOPNOTSUPP, syscall.EPERM}
	if a, ok := child(t); ok {
		for _, perr := range probeErrs {
			fakeProbe(t, 0, perr)
			info, err := Setup(Inputs{Workdir: a.Dirs["W"], Mode: sandbox.NetworkNone})
			want := sandbox.LandlockInfo{Note: "Landlock unavailable: " + perr.Error()}
			if err != nil || !reflect.DeepEqual(info, want) {
				t.Errorf("%v: Setup = %+v, %v; want %+v", perr, info, err, want)
			}
			b, _ := json.Marshal(info)
			t.Logf("OUTCOME %s", b)
		}
		if err := os.WriteFile(filepath.Join(a.Dirs["S"], "x"), nil, 0o644); err != nil {
			t.Errorf("creating outside every root: %v", err)
		}
		c, err := net.Dial("tcp", a.Addr)
		if err != nil {
			t.Errorf("loopback dial: %v", err)
		} else {
			_ = c.Close()
		}
		return
	}
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
	out := runChild(t, t.Name(), childArgs{Dirs: dirs(t, "W", "S"), Addr: l.Addr().String()})
	var outcomes []sandbox.LandlockInfo
	for _, line := range strings.Split(out, "\n") {
		if _, js, ok := strings.Cut(line, "OUTCOME "); ok {
			var info sandbox.LandlockInfo
			if err := json.Unmarshal([]byte(js), &info); err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, info)
		}
	}
	if len(outcomes) != len(probeErrs) {
		t.Fatalf("the child printed %d outcomes", len(outcomes))
	}
	for _, info := range outcomes {
		a, err := agent.New(agent.Config{Workdir: t.TempDir(), TTL: time.Hour, Landlock: info})
		if err != nil {
			t.Fatal(err)
		}
		hostEnd, agentEnd := net.Pipe()
		go func() { _ = a.Serve(agentEnd) }()
		sb, err := attach.Dial(context.Background(), hostEnd, attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
		if err != nil {
			t.Fatal(err)
		}
		if got := sb.Agent().Landlock; !reflect.DeepEqual(got, info) {
			t.Errorf("Agent().Landlock = %+v, want %+v", got, info)
		}
		_ = sb.Close(context.Background())
	}
}

// TS-15-12 (probe half): on a kernel offering exactly ABI 2 or 3, Setup with
// the probe reporting 4 fails (V4 is unsupported) instead of applying a
// weaker ruleset. 15-REQ-2.6. The binary half is in agentkit-sandboxd.
func TestStrictVersionFails_TS15_12(t *testing.T) {
	if a, ok := child(t); ok {
		fakeProbe(t, 4, nil)
		info, err := Setup(Inputs{Workdir: a.Dirs["W"], Mode: sandbox.NetworkNone})
		if err == nil {
			t.Errorf("Setup with V4 on an older kernel = %+v, nil", info)
		}
		return
	}
	k, err := kernelABI()
	if err != nil || k < 2 || k > 3 {
		t.Skipf("kernel Landlock ABI %d (%v); this half needs a kernel offering exactly ABI 2 or 3", k, err)
	}
	runChild(t, t.Name(), childArgs{Dirs: dirs(t, "W")})
}
