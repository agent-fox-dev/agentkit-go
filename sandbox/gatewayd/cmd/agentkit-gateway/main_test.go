package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// binary is the agentkit-gateway built by TestMain, or "" on Windows.
var binary string

func TestMain(m *testing.M) {
	code := func() int {
		if runtime.GOOS != "windows" {
			dir, err := os.MkdirTemp("", "agentkit-gateway-test-")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			defer os.RemoveAll(dir)
			binary = filepath.Join(dir, "agentkit-gateway")
			if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "building agentkit-gateway: %v\n%s", err, out)
				return 1
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}

// tok40 is a valid 40-character token.
const tok40 = "0123456789abcdefghijklmnopqrstuvwxyzABCD"

// writeFile writes body to name under dir and returns its path.
func writeFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeEngine puts a podman script on PATH that prints its version and logs
// every invocation; it returns the log's path.
func fakeEngine(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake engine is a shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "invocations.log")
	writeFile(t, dir, "podman", "#!/bin/sh\necho \"$@\" >> '"+log+"'\ncase \"$1\" in --version) echo 'podman version 5.0.0';; esac\n", 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// runCmd runs run with args and the real podman backend, never signalled.
func runCmd(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, deps{newBackend: podman.New, signals: make(chan os.Signal)})
	return code, stdout.String(), stderr.String()
}

// TS-19-59: --version prints the build version and both protocol versions
// and exits 0. 19-REQ-8.7.
func TestVersion_TS19_59(t *testing.T) {
	code, out, _ := runCmd("--version")
	want := fmt.Sprintf("agentkit-gateway %s (gateway protocol %d.%d, agent protocol %d.%d)", buildver.Version(),
		gateway.ProtocolMajor, gateway.ProtocolMinor, sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	if code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("--version: exit %d, %q; want 0, %q", code, out, want)
	}
}
