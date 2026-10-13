package landlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TS-18-66: make sandbox-landlock-test runs the verbose landlock tests with
// the default clone URL and is not part of make check. 18-REQ-10.5.
func TestMakeSandboxLandlockTest_TS18_66(t *testing.T) {
	mk, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not on PATH")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	makeN := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(mk, append([]string{"-n"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "AGENTKIT_SANDBOX_GIT_URL=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	cmd := exec.Command(mk, "-n", "sandbox-landlock-test")
	cmd.Dir = root
	cmd.Env = filterEnv(os.Environ(), "AGENTKIT_SANDBOX_GIT_URL")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n sandbox-landlock-test: %v\n%s", err, b)
	}
	out := string(b)
	for _, want := range []string{"cd sandbox", "go test -count=1 -v ./landlock/", "AGENTKIT_SANDBOX_GIT_URL=https://github.com/agent-fox-dev/agentkit-go"} {
		if !strings.Contains(out, want) {
			t.Errorf("make -n sandbox-landlock-test lacks %q:\n%s", want, out)
		}
	}
	if out := makeN("sandbox-landlock-test", "AGENTKIT_SANDBOX_GIT_URL=https://example.invalid/r"); !strings.Contains(out, "AGENTKIT_SANDBOX_GIT_URL=https://example.invalid/r") {
		t.Errorf("the override is not used:\n%s", out)
	}
	mf, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?m)^check:(.*)$`).FindSubmatch(mf); m == nil || strings.Contains(string(m[1]), "sandbox-landlock-test") {
		t.Errorf("check's prerequisites: %q", m)
	}
	if out := makeN("check"); strings.Contains(out, "go test -count=1 -v ./landlock/") {
		t.Errorf("make check runs the verbose landlock tests:\n%s", out)
	}
}

// filterEnv is env without name.
func filterEnv(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return out
}
