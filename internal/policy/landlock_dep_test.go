package policy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TS-15-40: go-landlock is promoted to a direct requirement of the sandbox
// module at its existing version, approved with a reason; psx stays
// indirect and the module graph is unchanged. 15-REQ-8.1, 15-REQ-8.2. The
// requirement is the nested sandbox module's (14's D-2), and the version is
// the pseudo-version 14 selected, not the v0.10.1 the spec expected
// (docs/errata/15_sandbox_landlock_agent.md).
func TestLandlockPromoted_TS15_40(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	sb := filepath.Join(root, "sandbox")
	snapPath := filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.txt")
	snap, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	pre, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.14.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snap, pre) {
		t.Error("the sandbox module's graph snapshot differs from the one 14 committed")
	}
	want := ""
	for _, line := range strings.Split(string(snap), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "github.com/landlock-lsm/go-landlock" {
			want = f[1]
		}
	}
	reqs := parseGoMod(t, sb)
	if r := reqs["github.com/landlock-lsm/go-landlock"]; want == "" || r.version != want || r.indirect {
		t.Errorf("sandbox/go.mod requires go-landlock %+v, want %s direct", r, want)
	}
	if r := reqs["kernel.org/pub/linux/libs/security/libcap/psx"]; !r.indirect {
		t.Errorf("psx is %+v, want indirect", r)
	}
	if _, ok := parseGoMod(t, root)["github.com/landlock-lsm/go-landlock"]; ok {
		t.Error("the root go.mod requires go-landlock; only the sandbox module may")
	}
	direct14 := []string{"github.com/agent-fox-dev/agentkit-go", "github.com/agent-fox-dev/agentkit-go/sandbox",
		"github.com/gokrazy/rsync", "google.golang.org/grpc", "google.golang.org/protobuf"}
	wantDirect := slices.Sorted(slices.Values(append(direct14, "github.com/landlock-lsm/go-landlock")))
	got := directSet(t, sb)
	if !slices.Equal(got, wantDirect) {
		t.Errorf("the sandbox module's direct set is %v, want 14's plus go-landlock: %v", got, wantDirect)
	}
	if err := validateAllowlist(got, append(slices.Clone(approvedDeps), sandboxModules...)); err != nil {
		t.Error(err)
	}
	if !slices.Contains(approvedDeps, "github.com/landlock-lsm/go-landlock") || !commentedDeps(t, root)["github.com/landlock-lsm/go-landlock"] {
		t.Error("approvedDeps lacks github.com/landlock-lsm/go-landlock with a reason comment above it")
	}
	cmd := exec.Command("go", "list", "-m", "all")
	cmd.Dir = sb
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m all: %v", err)
	}
	if !bytes.Equal(out, snap) {
		t.Errorf("go list -m all differs from the snapshot:\n%s", out)
	}
}
