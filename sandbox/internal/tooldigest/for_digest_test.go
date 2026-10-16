package tooldigest_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-34: every tool's sandbox definition digest is unchanged. The digests
// in testdata/digests.json were computed from tools.All before the workspace
// package existed; an agent built before that change still advertises them, so
// a host built after it must still accept it.
func TestForKeepsTheDigests_TS20_34(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "digests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}

	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if len(ts) != len(recorded) {
		t.Fatalf("For returned %d tools, %d digests were recorded", len(ts), len(recorded))
	}
	for i, tl := range ts {
		if tl.Name != recorded[i].Name {
			t.Errorf("tool %d is %s, recorded %s", i, tl.Name, recorded[i].Name)
			continue
		}
		if got := hex.EncodeToString(tooldigest.Digest(tl)); got != recorded[i].Digest {
			t.Errorf("%s: digest %s, recorded %s", tl.Name, got, recorded[i].Digest)
		}
	}
}
