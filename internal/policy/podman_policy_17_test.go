package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TS-17-62: sandbox/podman and sandbox/internal/gitclone never import
// confine, and the sandbox module builds and vets for windows/amd64.
// 17-REQ-10.4.
func TestPodmanNeverConfined_TS17_62(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	sb := filepath.Join(repoRoot(t), "sandbox")
	for _, pkg := range []string{"./podman", "./internal/gitclone"} {
		cmd := exec.Command("go", "list", "-deps", pkg)
		cmd.Dir = sb
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		if strings.Contains(string(out), confinePkg+"\n") {
			t.Errorf("%s imports %s", pkg, confinePkg)
		}
	}
	for _, args := range [][]string{{"build", "./..."}, {"vet", "./podman/", "./internal/gitclone/"}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sb
		cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("GOOS=windows go %v: %v\n%s", args, err, out)
		}
	}
}
