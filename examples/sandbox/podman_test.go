package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TS-17-64: with a real engine and AGENTKIT_SANDBOX_BASE_IMAGE, the example
// runs the same program on the podman provider under NetworkNone. The
// offline run over local is TS-13-68 and TS-14-45, unchanged. 17-REQ-10.6.
func TestExampleRunsOnPodman_TS17_64(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the example against a container engine; skipped under -short")
	}
	image := os.Getenv("AGENTKIT_SANDBOX_BASE_IMAGE")
	if image == "" {
		t.Skip("AGENTKIT_SANDBOX_BASE_IMAGE is not set; run make sandbox-images and set it to the base image")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not on PATH; the example's --podman uses podman.Options{}, whose engine is podman")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	cmd := exec.Command(goBin, "run", ".", "--podman", "--image", image)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), " on podman: ") || !strings.Contains(string(out), "Sandbox example completed successfully") {
		t.Fatalf("go run . --podman: %v\n%s", err, out)
	}
}
