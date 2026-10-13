package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-16-3: agentkit-sandboxd built with the -X stamp prints the stamped
// version from --version. 16-REQ-4.4. The command is in the nested sandbox
// module (spec 14's D-2), so it is built from that module's root.
func TestStampedVersion_TS16_3(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the agent; skipped under -short")
	}
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Path}} {{.Dir}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	path, root, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	if path != "github.com/agent-fox-dev/agentkit-go/sandbox" {
		t.Fatalf("the module is %s", path)
	}
	bin := filepath.Join(t.TempDir(), "agentkit-sandboxd")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=v9.8.7",
		"-o", bin, "./cmd/agentkit-sandboxd")
	build.Dir = root
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	cmd := exec.Command(bin, "--version")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("--version: %v (stderr %q)", err, se.String())
	}
	if want := fmt.Sprintf("agentkit-sandboxd v9.8.7 (protocol %d.%d)\n", sandbox.ProtocolMajor, sandbox.ProtocolMinor); so.String() != want || se.Len() != 0 {
		t.Errorf("--version printed %q (stderr %q), want %q", so.String(), se.String(), want)
	}
}
