package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TS-17-60: sandbox/podman and sandbox/internal/gitclone add no Go module
// dependency: the sandbox module's direct set is TS-14-42's, its graph is
// the snapshot, and every non-standard dependency of the new packages is in
// the sandbox module, the root module or an already-required module.
// 17-REQ-10.2. The snapshot is the whole graph, so the direct set is
// compared with the set TS-14-42 pins (docs/errata/17_sandbox_podman.md).
func TestPodmanAddsNoDependency_TS17_60(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	root := repoRoot(t)
	sb := filepath.Join(root, "sandbox")
	want := []string{"github.com/agent-fox-dev/agentkit-go", "github.com/agent-fox-dev/agentkit-go/sandbox",
		"github.com/gokrazy/rsync", "github.com/landlock-lsm/go-landlock", "google.golang.org/grpc", "google.golang.org/protobuf"}
	if got := directSet(t, sb); !slices.Equal(got, want) {
		t.Errorf("the sandbox module's direct set is %v, want %v", got, want)
	}
	graph := exec.Command("go", "list", "-m", "all")
	graph.Dir = sb
	all, err := graph.Output()
	if err != nil {
		t.Fatalf("go list -m all: %v", err)
	}
	snap, err := os.ReadFile(filepath.Join(root, "internal", "policy", "testdata", "sandbox-go-list-m-all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(all) != string(snap) {
		t.Errorf("the sandbox module's graph differs from its snapshot:\n%s", all)
	}
	deps := exec.Command("go", "list", "-deps", "-test", "-f", "{{if not .Standard}}{{with .Module}}{{.Path}}{{end}}{{end}}",
		"./podman/...", "./internal/gitclone/...")
	deps.Dir = sb
	out, err := deps.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	required := parseGoMod(t, sb)
	for _, m := range strings.Fields(string(out)) {
		if _, ok := required[m]; ok || m == "github.com/agent-fox-dev/agentkit-go" || m == "github.com/agent-fox-dev/agentkit-go/sandbox" {
			continue
		}
		t.Errorf("the new packages depend on %s, which sandbox/go.mod does not require", m)
	}
}
