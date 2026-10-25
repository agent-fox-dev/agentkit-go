package local_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// Reap removes the sandboxes whose creating process has gone and leaves live
// ones and anything without a marker; where there is no liveness probe
// (Windows) it removes nothing.
func TestReapRemovesOnlyTheSandboxesOfGoneProcesses(t *testing.T) {
	root := isolate(t)
	if ids, err := local.Unisolated().Reap(context.Background()); ids != nil || err != nil {
		t.Fatalf("Reap with no root = %v, %v", ids, err)
	}
	dead, live := create(t, sandbox.Spec{Network: unrestricted}), create(t, sandbox.Spec{Network: unrestricted})
	for _, p := range []string{"stray-file", "no-marker/x"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o700); err != nil || os.WriteFile(filepath.Join(root, p), nil, 0o600) != nil {
			t.Fatal(err)
		}
	}
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dirOf(dead), "agentkit-sandbox.json")
	raw, err := os.ReadFile(marker)
	var m map[string]any
	if err != nil || json.Unmarshal(raw, &m) != nil {
		t.Fatalf("marker %s: %v", raw, err)
	}
	m["pid"] = gone.ProcessState.Pid()
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(marker, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	ids, err := local.Unisolated().Reap(context.Background())
	want := []string{dead.ID()}
	if runtime.GOOS == "windows" {
		want = nil
	}
	if err != nil || !slices.Equal(ids, want) {
		t.Fatalf("Reap = %v, %v; want %v", ids, err, want)
	}
	if exists(dirOf(dead)) == (want != nil) || !exists(dirOf(live)) || !exists(filepath.Join(root, "no-marker")) || !exists(filepath.Join(root, "stray-file")) {
		t.Errorf("after Reap: dead %v, live %v, no-marker %v, stray-file %v", exists(dirOf(dead)), exists(dirOf(live)),
			exists(filepath.Join(root, "no-marker")), exists(filepath.Join(root, "stray-file")))
	}
}
