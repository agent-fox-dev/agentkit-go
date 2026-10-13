package sandbox_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TS-16-54: an embedder binary resolves the published image references from
// its stamped version, and gets ErrNoPublishedImage for (devel) and
// pseudo-versions. 16-PATH-4, 16-REQ-5.5, 16-REQ-5.6.
func TestEmbedderImageRefs_TS16_54(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the embedder program; skipped under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	const pseudo = "v1.2.4-0.20240102150405-abcdefabcdef"
	dir := t.TempDir()
	build := func(name, stamp string) string {
		t.Helper()
		bin := filepath.Join(dir, name)
		args := []string{"build", "-buildvcs=false", "-o", bin}
		if stamp != "" {
			args = append(args, "-ldflags", "-X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp="+stamp)
		}
		cmd := exec.Command(goBin, append(args, "./testdata/imageref")...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		return bin
	}
	run := func(bin string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(bin)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", bin, err)
		}
		return so.String(), se.String(), code
	}

	so, se, code := run(build("stamped", "v1.2.3"))
	if want := "ghcr.io/agent-fox-dev/agentkit-sandbox:1.2.3\nghcr.io/agent-fox-dev/agentkit-sandboxd:1.2.3\n"; code != 0 || so != want {
		t.Errorf("stamped v1.2.3: exit %d, stdout %q, want exit 0 and %q; stderr %q", code, so, want, se)
	}

	for _, c := range []struct{ name, stamp, version string }{
		{"unstamped", "", "(devel)"},
		{"pseudo", pseudo, pseudo},
	} {
		so, se, code := run(build(c.name, c.stamp))
		if code != 1 || so != "" {
			t.Errorf("%s: exit %d, stdout %q, want exit 1 and no stdout", c.name, code, so)
		}
		for _, want := range []string{"sandbox: no published image for this agentkit-go version", c.version, "Spec.Image", "make sandbox-images"} {
			if !strings.Contains(se, want) {
				t.Errorf("%s: stderr %q does not contain %q", c.name, se, want)
			}
		}
	}
}
