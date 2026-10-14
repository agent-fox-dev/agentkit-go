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

// makeEnv is a make binary, found before PATH is replaced, and stub
// directories: a holds podman and docker, b only docker, c neither; each
// holds git when git is found. The stubs append their argv to marker.
type makeEnv struct {
	make, a, b, c, marker, git string
}

func newMakeEnv(t *testing.T) *makeEnv {
	t.Helper()
	mk, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not on PATH")
	}
	e := &makeEnv{make: mk, marker: filepath.Join(t.TempDir(), "marker")}
	e.git, _ = exec.LookPath("git")
	stub := "#!/bin/sh\necho \"$0 $*\" >> " + e.marker + "\nexit 0\n"
	mkdir := func(engines ...string) string {
		d := t.TempDir()
		for _, n := range engines {
			if err := os.WriteFile(filepath.Join(d, n), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if e.git != "" {
			if err := os.Symlink(e.git, filepath.Join(d, "git")); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	e.a, e.b, e.c = mkdir("podman", "docker"), mkdir("docker"), mkdir()
	return e
}

// run runs make in dir with PATH set to path and returns its combined
// output and exit code.
func (e *makeEnv) run(t *testing.T, dir, path string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.make, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir()}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

// buildLines are the lines of out that run the image build.
func buildLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "build -f sandbox/images/Containerfile") {
			lines = append(lines, l)
		}
	}
	return lines
}

func (e *makeEnv) markerAbsent(t *testing.T) {
	t.Helper()
	if b, err := os.ReadFile(e.marker); err == nil {
		t.Errorf("a container engine ran: %s", b)
	}
}

// TS-16-18: the make variables take their documented defaults and every one
// can be overridden on the command line. 16-REQ-6.1.
func TestMakeVariables_TS16_18(t *testing.T) {
	e := newMakeEnv(t)
	root := repoRoot(t)
	rev := ""
	if e.git != "" {
		cmd := exec.Command(e.git, "rev-parse", "HEAD")
		cmd.Dir = root
		out, _ := cmd.Output()
		rev = strings.TrimSpace(string(out))
	}
	for _, c := range []struct{ path, engine string }{{e.a, "podman"}, {e.b, "docker"}} {
		out, code := e.run(t, root, c.path, "-n", "sandbox-images")
		lines := buildLines(out)
		if code != 0 || len(lines) != 2 {
			t.Fatalf("PATH with %s: exit %d:\n%s", c.engine, code, out)
		}
		for _, l := range lines {
			if !strings.HasPrefix(l, c.engine+" build") || strings.Contains(l, "--platform") ||
				!strings.Contains(l, "--build-arg AGENTKIT_VERSION= ") {
				t.Errorf("default build line %q", l)
			}
			if e.git != "" && !strings.Contains(l, "--build-arg AGENTKIT_REVISION="+rev+" ") {
				t.Errorf("build line %q lacks AGENTKIT_REVISION=%s", l, rev)
			}
		}
		if !strings.Contains(lines[0], "-t quay.io/agent-fox-dev/agentkit-sandbox:dev") || !strings.Contains(lines[1], "-t quay.io/agent-fox-dev/agentkit-sandboxd:dev") {
			t.Errorf("default tags: %q", lines)
		}
	}
	out, _ := e.run(t, root, e.a, "-n", "sandbox-images", "CONTAINER=docker", "SANDBOX_BASE_IMAGE=example.com/b",
		"SANDBOX_OVERLAY_IMAGE=example.com/o", "SANDBOX_IMAGE_TAG=t1", "SANDBOX_PLATFORM=linux/arm64",
		"AGENTKIT_VERSION=v1.0.0", "AGENTKIT_REVISION=abc")
	lines := buildLines(out)
	if len(lines) != 2 {
		t.Fatalf("overrides:\n%s", out)
	}
	for _, l := range lines {
		for _, want := range []string{"docker build", "--platform linux/arm64", "--build-arg AGENTKIT_VERSION=v1.0.0", "--build-arg AGENTKIT_REVISION=abc"} {
			if !strings.Contains(l, want) {
				t.Errorf("override line %q lacks %q", l, want)
			}
		}
	}
	if !strings.Contains(lines[0], "-t example.com/b:t1") || !strings.Contains(lines[1], "-t example.com/o:t1") {
		t.Errorf("override tags: %q", lines)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(readRepo(t, "Makefile")), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = e.run(t, dir, e.a, "-n", "sandbox-images")
	for _, l := range buildLines(out) {
		if !strings.Contains(l, "--build-arg AGENTKIT_REVISION= ") {
			t.Errorf("outside a checkout, the line %q has a revision", l)
		}
	}
}

// TS-16-19: make sandbox-images builds the base target, then the overlay
// target, from the root with both build arguments and the right tag each.
// 16-REQ-6.2.
func TestMakeSandboxImages_TS16_19(t *testing.T) {
	e := newMakeEnv(t)
	root := repoRoot(t)
	for _, platform := range []string{"", "linux/amd64"} {
		args := []string{"-n", "sandbox-images"}
		if platform != "" {
			args = append(args, "SANDBOX_PLATFORM="+platform)
		}
		out, _ := e.run(t, root, e.a, args...)
		lines := buildLines(out)
		if len(lines) != 2 || !strings.Contains(lines[0], "--target base") || !strings.Contains(lines[1], "--target overlay") {
			t.Fatalf("platform %q: build lines %q", platform, lines)
		}
		if strings.Contains(out, "cd ") {
			t.Errorf("the recipe changes directory:\n%s", out)
		}
		for i, l := range lines {
			f := strings.Fields(l)
			if f[0] != "podman" || f[1] != "build" || f[len(f)-1] != "." ||
				!strings.Contains(l, "--build-arg AGENTKIT_VERSION=") || !strings.Contains(l, "--build-arg AGENTKIT_REVISION=") {
				t.Errorf("line %q", l)
			}
			tags := regexp.MustCompile(`-t (\S+)`).FindAllStringSubmatch(l, -1)
			want := []string{"quay.io/agent-fox-dev/agentkit-sandbox:dev", "quay.io/agent-fox-dev/agentkit-sandboxd:dev"}[i]
			if len(tags) != 1 || tags[0][1] != want {
				t.Errorf("line %q tags %v, want only %s", l, tags, want)
			}
			if (platform == "") == strings.Contains(l, "--platform") || (platform != "" && !strings.Contains(l, "--platform "+platform)) {
				t.Errorf("platform %q: line %q", platform, l)
			}
		}
	}
}

// TS-16-20: make sandbox-images fails before any build when the container
// engine is missing, naming podman, docker and CONTAINER. 16-REQ-6.3.
func TestMakeNoEngine_TS16_20(t *testing.T) {
	e := newMakeEnv(t)
	root := repoRoot(t)
	for _, c := range []struct {
		path string
		args []string
	}{{e.c, []string{"sandbox-images"}}, {e.a, []string{"sandbox-images", "CONTAINER=agentkit-no-such-engine"}}} {
		out, code := e.run(t, root, c.path, c.args...)
		if code == 0 || !strings.Contains(out, "podman") || !strings.Contains(out, "docker") || !strings.Contains(out, "CONTAINER") {
			t.Errorf("%v: exit %d:\n%s", c.args, code, out)
		}
		if len(buildLines(out)) != 0 {
			t.Errorf("%v ran a build:\n%s", c.args, out)
		}
	}
	e.markerAbsent(t)
}

// TS-16-21: make sandbox-images-test builds then runs the image tests with
// the right environment, both targets are in help, and check stays
// engine-free. 16-REQ-6.4, 16-REQ-6.5. The image tests are in the nested
// sandbox module, so the recipe runs them there
// (docs/errata/16_sandbox_images.md).
func TestMakeImagesTest_TS16_21(t *testing.T) {
	e := newMakeEnv(t)
	root := repoRoot(t)
	for _, version := range []string{"", "v0.6.0"} {
		out, _ := e.run(t, root, e.a, "-n", "sandbox-images-test", "AGENTKIT_VERSION="+version)
		var test string
		lastBuild, testAt := -1, -1
		for i, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "build -f sandbox/images/Containerfile") {
				lastBuild = i
			}
			if strings.Contains(l, "go test -count=1 -v ./images/") {
				test, testAt = l, i
			}
		}
		if testAt < 0 || testAt < lastBuild {
			t.Fatalf("version %q: no go test line after the builds:\n%s", version, out)
		}
		for _, want := range []string{"cd sandbox && ", "AGENTKIT_SANDBOX_BASE_IMAGE=quay.io/agent-fox-dev/agentkit-sandbox:dev ",
			"AGENTKIT_SANDBOX_OVERLAY_IMAGE=quay.io/agent-fox-dev/agentkit-sandboxd:dev ",
			"AGENTKIT_SANDBOX_IMAGE_VERSION=" + version + " ", "AGENTKIT_CONTAINER=podman "} {
			if !strings.Contains(test, want) {
				t.Errorf("the test line %q lacks %q", test, want)
			}
		}
		if strings.HasPrefix(strings.TrimSpace(test), "-") || strings.Contains(test, "|| true") || strings.Contains(test, "; true") {
			t.Errorf("the test line %q ignores go test's status", test)
		}
	}
	help, _ := e.run(t, root, e.a+":/usr/bin:/bin", "help")
	for _, target := range []string{"sandbox-images ", "sandbox-images-test "} {
		found := false
		for _, l := range strings.Split(help, "\n") {
			if f := strings.TrimSpace(l); strings.HasPrefix(f, target) && len(strings.Fields(f)) > 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("make help lists no %s:\n%s", target, help)
		}
	}
	m := regexp.MustCompile(`(?m)^check:([^#\n]*)`).FindStringSubmatch(readRepo(t, "Makefile"))
	if m == nil || strings.Join(strings.Fields(m[1]), " ") != "fmt vet lint test" {
		t.Errorf("check's prerequisites are %q", m)
	}
	out, _ := e.run(t, root, e.a+":/usr/bin:/bin", "-n", "check")
	if strings.Contains(out, "podman") || strings.Contains(out, "docker") || strings.Contains(out, "build -f sandbox/images") {
		t.Errorf("make -n check runs a container engine:\n%s", out)
	}
	e.markerAbsent(t)
}
