package podman

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// repoRoot is the repository root above the test.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "sandbox", "podman")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no repository root above the test")
		}
		dir = parent
	}
}

// TS-17-63: make sandbox-podman-test depends on sandbox-images, runs the
// podman tests with the image variables, and is not part of make check.
// 17-REQ-10.5.
func TestMakeSandboxPodmanTest_TS17_63(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub engines are shell scripts")
	}
	mk, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not on PATH")
	}
	root := repoRoot(t)
	marker := filepath.Join(t.TempDir(), "marker")
	stubs := t.TempDir()
	for _, n := range []string{"podman", "docker"} {
		if err := os.WriteFile(filepath.Join(stubs, n), []byte("#!/bin/sh\necho \"$0 $*\" >> "+marker+"\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeN := func(target string) string {
		t.Helper()
		cmd := exec.Command(mk, "-n", target)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + stubs + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("make -n %s: %v\n%s", target, err, out.String())
		}
		return out.String()
	}

	mf, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^sandbox-podman-test:[^\n#]*\bsandbox-images\b`).Match(mf) {
		t.Error("sandbox-podman-test does not list sandbox-images as a prerequisite")
	}
	out := makeN("sandbox-podman-test")
	lines := strings.Split(out, "\n")
	lastBuild, testAt := -1, -1
	var test string
	for i, l := range lines {
		if strings.Contains(l, "build -f sandbox/images/Containerfile") {
			lastBuild = i
		}
		if strings.Contains(l, "go test -count=1 -v ./podman/") {
			test, testAt = l, i
		}
	}
	if testAt < 0 || lastBuild < 0 || testAt < lastBuild {
		t.Fatalf("make -n sandbox-podman-test does not build the images and then run the podman tests:\n%s", out)
	}
	for _, want := range []string{"cd sandbox && ", "AGENTKIT_SANDBOX_BASE_IMAGE=quay.io/agent-fox-dev/agentkit-sandbox:latest ",
		"AGENTKIT_SANDBOX_OVERLAY_IMAGE=quay.io/agent-fox-dev/agentkit-sandboxd:latest ", "AGENTKIT_SANDBOX_IMAGE_VERSION=", "AGENTKIT_CONTAINER=podman "} {
		if !strings.Contains(test, want) {
			t.Errorf("the test line %q lacks %q", test, want)
		}
	}
	check := makeN("check")
	if strings.Contains(check, "sandbox-podman-test") || strings.Contains(check, "./podman/") || strings.Contains(check, "AGENTKIT_SANDBOX_BASE_IMAGE") {
		t.Errorf("make check runs the podman integration tests:\n%s", check)
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("a container engine ran under make -n: %s", b)
	}
}
