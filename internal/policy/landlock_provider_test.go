package policy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TS-18-62: sandbox/landlock depends on neither go-landlock nor confine, for
// the host GOOS and for linux, and the TS-15-39 import policy holds.
// 18-REQ-10.1.
func TestLandlockProviderImports_TS18_62(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	for _, goos := range []string{"", "linux"} {
		cmd := exec.Command("go", "list", "-deps", "./landlock")
		cmd.Dir = filepath.Join(root, "sandbox")
		cmd.Env = os.Environ()
		if goos != "" {
			cmd.Env = append(cmd.Env, "GOOS="+goos)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("GOOS=%s go list -deps ./landlock: %v", goos, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			if strings.HasPrefix(dep, "github.com/landlock-lsm/go-landlock") || dep == confinePkg {
				t.Errorf("GOOS=%s: sandbox/landlock depends on %s", goos, dep)
			}
		}
	}
	violations, _, _ := checkConfinement(root, repoGoFiles(t, root))
	if len(violations) != 0 {
		t.Errorf("the confinement policy is violated:\n%s", strings.Join(violations, "\n"))
	}
}

// TS-18-63: the sandbox module's direct requirements are the ones recorded
// before spec 18. 18-REQ-10.2.
func TestLandlockProviderAddsNoDependency_TS18_63(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = filepath.Join(root, "sandbox")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod struct {
		Require []struct {
			Path, Version string
			Indirect      bool
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	var direct []string
	for _, r := range mod.Require {
		if !r.Indirect {
			direct = append(direct, r.Path+"@"+r.Version)
		}
	}
	slices.Sort(direct)
	want, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "sandbox-direct-requires.18.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(direct, "\n"); got != strings.TrimSpace(string(want)) {
		t.Errorf("the sandbox module's direct requirements are\n%s\nwant\n%s", got, want)
	}
}
