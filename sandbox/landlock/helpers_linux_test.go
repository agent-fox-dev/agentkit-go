package landlock

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// newRealProvider is landlock.New with the built agent and a temporary
// Root, unless o sets them.
func newRealProvider(t *testing.T, o Options) sandbox.Provider {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	if o.AgentBinary == "" {
		o.AgentBinary = agentBin
	}
	p, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// realSandbox creates a sandbox closed at the end of the test, and returns
// it with its directory.
func realSandbox(t *testing.T, p sandbox.Provider, spec sandbox.Spec) (sandbox.Sandbox, string) {
	t.Helper()
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, filepath.Dir(s.Spec().Workdir)
}

// realTools is the sandbox's proxy tools.
func realTools(t *testing.T, s sandbox.Sandbox) []core.Tool {
	t.Helper()
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// execute runs command through the proxy execute tool and returns its exit
// code and output.
func execute(t *testing.T, ts []core.Tool, command string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"command": command})
	for _, tl := range ts {
		if tl.Name == "execute" {
			r := tl.Execute(context.Background(), b)
			code := 0
			if n, ok := r.Data["exit_code"].(json.Number); ok {
				i, _ := n.Int64()
				code = int(i)
			}
			if !r.OK && code == 0 {
				code = -1
			}
			return code, r.Text + r.Detail
		}
	}
	t.Fatal("no execute tool")
	return 0, ""
}

// procStat is pid's process group and session id (fields 5 and 6).
func procStat(t *testing.T, pid int) (pgrp, session int) {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+1:])
	pgrp, _ = strconv.Atoi(f[2])
	session, _ = strconv.Atoi(f[3])
	return pgrp, session
}

// alive reports whether pid names a live, non-zombie process.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	f := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+1:])
	return len(f) > 0 && f[0] != "Z"
}

// eventually polls cond for up to d.
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// listener is a loopback TCP listener counting the connections it accepts,
// sending hello to each.
type listener struct {
	net.Listener
	accepted atomic.Int32
}

func listen(t *testing.T, hello string) *listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &listener{Listener: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.accepted.Add(1)
			_, _ = c.Write([]byte(hello))
			_ = c.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return l
}

// helperDir is a directory holding the dialtcp helper, for a LocalDir Sync.
func helperDir(t *testing.T) string {
	t.Helper()
	return filepath.Dir(dialtcpBin)
}

// agentPID is the pid the sandbox directory's marker names.
func agentPID(t *testing.T, dir string) int {
	t.Helper()
	m, err := readMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m.PID
}

// itoa is strconv.Itoa.
func itoa(i int) string { return strconv.Itoa(i) }

// needABI4 skips t below Landlock ABI 4 on this host and returns the ABI.
func needABI4(t *testing.T) int {
	t.Helper()
	return abiGate(t, defaultProbe)
}
