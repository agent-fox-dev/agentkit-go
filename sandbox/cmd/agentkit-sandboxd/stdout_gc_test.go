package main

import (
	"os"
	"runtime"
	"testing"
)

// The agent's protocol stream survives garbage collection: the original
// stdout *os.File, unreachable after redirectStdout, used to close fd 1
// when finalized, so a busy agent's connection ended with EOF. GOGC=1 makes
// the collector run constantly.
func TestStdoutSurvivesGC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX echo")
	}
	p := start(t, []string{"PATH=" + os.Getenv("PATH"), "GOGC=1", "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()},
		"--network", "unrestricted", "--workdir", t.TempDir())
	for i := range 30 {
		if r := tool(t, p.sb, "execute", `{"command":"echo hi"}`); !r.OK {
			t.Fatalf("call %d: %+v (agent stderr %q)", i, r, p.stderr.String())
		}
	}
}
