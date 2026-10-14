package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TS-19-65: make sandbox-gateway builds a trimmed, stamped binary, and the
// Makefile wires the gatewayd module and the opt-in target. 19-REQ-9.6.
func TestSandboxGatewayMake_TS19_65(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the gateway with make; skipped under -short")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	root := repoRoot(t)
	makeIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("make", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	bin := filepath.Join(root, "build", "bin", "agentkit-gateway")
	_ = os.Remove(bin)
	makeIn("sandbox-gateway", "AGENTKIT_VERSION=v0.19.0-test")
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || !strings.Contains(string(out), "v0.19.0-test") {
		t.Errorf("agentkit-gateway --version = %q, %v; want the stamped v0.19.0-test", out, err)
	}
	info, err := exec.Command("go", "version", "-m", bin).Output()
	if err != nil || !strings.Contains(string(info), "-trimpath=true") {
		t.Errorf("the binary's build info lacks -trimpath=true: %v\n%s", err, info)
	}

	mf, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var modules, docLoop bool
	for _, line := range strings.Split(string(mf), "\n") {
		if strings.HasPrefix(line, "MODULES :=") && strings.Contains(line, "sandbox/gatewayd") {
			modules = true
		}
		if strings.Contains(line, "@for m in") && strings.Contains(line, "sandbox/gatewayd") {
			docLoop = true
		}
	}
	if !modules || !docLoop {
		t.Errorf("MODULES lists sandbox/gatewayd: %v; the doc loop does: %v", modules, docLoop)
	}
	dry := makeIn("-n", "sandbox-gateway-test", "CONTAINER=podman")
	images, tests := strings.Index(dry, "-f containers/agentkit-sandbox/Containerfile"), strings.Index(dry, "cd sandbox/gatewayd")
	if images < 0 || tests < 0 || images > tests {
		t.Errorf("make -n sandbox-gateway-test does not run the image build before the gateway tests:\n%s", dry)
	}
	if strings.Contains(makeIn("-n", "check"), "sandbox-gateway-test") {
		t.Error("make check runs sandbox-gateway-test")
	}
}
