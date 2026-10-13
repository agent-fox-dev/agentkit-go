package images

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// The local smoke tests: one run of make sandbox-images-test, stamped
// v0.6.0, that builds both images and runs this package's image tests
// against them. Opt-in, because the build pulls base images and takes
// minutes.

const (
	smokeVar     = "AGENTKIT_SANDBOX_SMOKE"
	smokeVersion = "v0.6.0"
)

var smoke struct {
	once   sync.Once
	engine string
	out    string
	err    error
}

// makeImagesTest runs make sandbox-images-test once from the repository
// root and returns the engine it used and make's output. The nested go
// test sees nestedVar, so it does not run these smoke tests again.
func makeImagesTest(t *testing.T) (engine, out string) {
	t.Helper()
	if os.Getenv(nestedVar) != "" {
		t.Skip("nested run")
	}
	if os.Getenv(smokeVar) != "1" {
		t.Skip(smokeVar + "=1 is not set; these smoke tests run make sandbox-images-test")
	}
	engine, ok := pickEngine(os.Getenv, exec.LookPath)
	if !ok {
		t.Skip("no container engine (podman or docker) found")
	}
	root := repoRoot(t)
	smoke.once.Do(func() {
		smoke.engine = engine
		cmd := exec.Command("make", "sandbox-images-test", "CONTAINER="+engine, "AGENTKIT_VERSION="+smokeVersion)
		cmd.Dir = root
		cmd.Env = append(subprocessEnv(), "PATH="+os.Getenv("PATH"))
		var b bytes.Buffer
		cmd.Stdout, cmd.Stderr = &b, &b
		smoke.err = cmd.Run()
		smoke.out = b.String()
	})
	if smoke.err != nil {
		t.Fatalf("make sandbox-images-test AGENTKIT_VERSION=%s: %v\n%s", smokeVersion, smoke.err, smoke.out)
	}
	return smoke.engine, smoke.out
}

// noLeftovers fails t if an agentkit-imgtest-* container or derived image
// remains.
func noLeftovers(t *testing.T, engine string) {
	t.Helper()
	if out, _, _ := runEngine(t, engine, "ps", "-a", "--format", "{{.Names}}"); strings.Contains(out, "agentkit-imgtest-") {
		t.Errorf("leftover containers:\n%s", out)
	}
	if out, _, _ := runEngine(t, engine, "images", "--format", "{{.Repository}}"); strings.Contains(out, "agentkit-imgtest-derived-") {
		t.Errorf("leftover derived images:\n%s", out)
	}
}

// TS-16-51: make builds both images stamped v0.6.0 and the base image tests
// pass over the agent protocol. 16-PATH-1, 16-REQ-1.1, 16-REQ-4.6,
// 16-REQ-6.4.
func TestMakeImagesSmoke_TS16_51(t *testing.T) {
	engine, out := makeImagesTest(t)
	if !strings.Contains(out, "--- PASS: TestBaseImage ") {
		t.Errorf("make output has no PASS for TestBaseImage:\n%s", out)
	}
	for _, ref := range []string{"ghcr.io/agent-fox-dev/agentkit-sandbox:dev", "ghcr.io/agent-fox-dev/agentkit-sandboxd:dev"} {
		if _, se, code := runEngine(t, engine, "image", "inspect", ref); code != 0 {
			t.Errorf("%s is not in the image store: %s", ref, se)
			continue
		}
		so, se, code := runEngine(t, engine, "run", "--rm", "--entrypoint", "/usr/local/libexec/agentkit-sandboxd", ref, "--version")
		if want := versionLine(smokeVersion); code != 0 || so != want {
			t.Errorf("%s --version: exit %d, %q, want %q; stderr %s", ref, code, so, want, se)
		}
	}
	noLeftovers(t, engine)
}

// TS-16-52: an image built from the base image plus the OverlayCopyLine
// line runs the copied agent end to end. 16-PATH-2, 16-REQ-3.3.
func TestDerivedImageSmoke_TS16_52(t *testing.T) {
	engine, out := makeImagesTest(t)
	if !strings.Contains(out, "--- PASS: TestOverlayImageDerived ") {
		t.Errorf("make output has no PASS for TestOverlayImageDerived:\n%s", out)
	}
	noLeftovers(t, engine)
}
