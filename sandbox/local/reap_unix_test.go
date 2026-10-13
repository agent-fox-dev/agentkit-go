//go:build unix

package local_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-13-61: Reap removes sandbox directories whose marker pid is dead,
// returns their ids, and leaves live ones untouched. 13-REQ-9.7.
func TestReap_TS13_61(t *testing.T) {
	isolate(t)
	a := create(t, sandbox.Spec{Network: unrestricted})
	b := create(t, sandbox.Spec{Network: unrestricted})
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(sandboxDir(a), "agentkit-sandbox.json")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["pid"] = c.ProcessState.Pid()
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(marker, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ids, err := local.Unisolated().Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{a.ID()}) {
		t.Fatalf("Reap = %v, %v; want [%s]", ids, err, a.ID())
	}
	if exists(sandboxDir(a)) || !exists(sandboxDir(b)) {
		t.Errorf("after Reap: A exists %v, B exists %v", exists(sandboxDir(a)), exists(sandboxDir(b)))
	}
}
