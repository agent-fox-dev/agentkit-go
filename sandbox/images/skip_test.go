package images

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const nestedVar = "AGENTKIT_IMGTEST_NESTED"

// subprocessEnv is the test's environment without PATH, the image
// variables and AGENTKIT_CONTAINER, plus extra and the nesting marker.
func subprocessEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "AGENTKIT_CONTAINER" || strings.HasPrefix(k, "AGENTKIT_SANDBOX_") || k == nestedVar {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, nestedVar+"=1"), extra...)
}

// engineStubs is a directory whose podman and docker append their argv to
// marker and exit with code.
func engineStubs(t *testing.T, marker, code string) string {
	t.Helper()
	d := t.TempDir()
	for _, n := range []string{"podman", "docker"} {
		if err := os.WriteFile(filepath.Join(d, n), []byte("#!/bin/sh\necho \"$0 $*\" >> "+marker+"\nexit "+code+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TS-16-26: the image tests skip, naming make build-containers, under
// -short, without an engine, and without their image variable, and go
// test exits 0. 16-REQ-7.1.
func TestImageTestsSkip(t *testing.T) {
	if os.Getenv(nestedVar) != "" {
		t.Skip("nested run")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	marker := filepath.Join(t.TempDir(), "marker")
	stubs := engineStubs(t, marker, "0")
	onlyGo := t.TempDir()
	if err := os.Symlink(goBin, filepath.Join(onlyGo, "go")); err != nil {
		t.Fatal(err)
	}
	images := []string{"AGENTKIT_SANDBOX_BASE_IMAGE=example.com/base:x", "AGENTKIT_SANDBOX_OVERLAY_IMAGE=example.com/overlay:x"}
	for _, c := range []struct {
		name  string
		short bool
		env   []string
	}{
		{"short", true, append([]string{"PATH=" + os.Getenv("PATH")}, images...)},
		{"no engine", false, append([]string{"PATH=" + onlyGo, "CGO_ENABLED=0"}, images...)},
		{"no image variable", false, []string{"PATH=" + stubs + ":" + os.Getenv("PATH")}},
	} {
		args := []string{"test", "-count=1", "-v", "-run", "^Test(Base|Overlay)Image"}
		if c.short {
			args = append(args, "-short")
		}
		cmd := exec.Command(goBin, append(args, ".")...)
		cmd.Env = subprocessEnv(c.env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, out.String())
		}
		o := out.String()
		if regexp.MustCompile(`--- (PASS|FAIL): Test(Base|Overlay)Image`).MatchString(o) {
			t.Errorf("%s: an image test ran:\n%s", c.name, o)
		}
		skips := regexp.MustCompile(`(?m)^--- SKIP: (Test(Base|Overlay)Image\w*)`).FindAllStringSubmatch(o, -1)
		if len(skips) < 6 {
			t.Errorf("%s: %d image tests skipped, want all six:\n%s", c.name, len(skips), o)
		}
		for _, l := range regexp.MustCompile(`(?m)^\s+\w+_test\.go:\d+: (.*)$`).FindAllStringSubmatch(o, -1) {
			if !strings.Contains(l[1], "make build-containers") {
				t.Errorf("%s: a skip message lacks make build-containers: %q", c.name, l[1])
			}
		}
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("a container engine ran: %s", b)
	}
}
