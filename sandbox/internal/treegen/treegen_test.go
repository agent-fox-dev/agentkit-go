package treegen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/treegen"
)

// TS-20-57 (the treegen half): treegen walks through the workspace package and
// its walk output is what tools.Walk's was, for the same generated trees.
// testdata/walksets.json holds the sets recorded from tools.Walk before the
// workspace package existed, for seeds 1 to 5.
func TestWalkSetIsWhatToolsWalkVisited_TS20_57(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "walksets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string][]string
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}

	// A global excludes file in the environment that would hide everything:
	// the walk must not read it. git's own core.excludesFile would take
	// precedence over the XDG path, so it names the same file, and the
	// machine's own configuration is out of the picture.
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(cfg, "git", "ignore")
	if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitconfig := filepath.Join(cfg, "gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[core]\n\texcludesFile = "+ignore+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	for seed := uint64(1); seed <= 5; seed++ {
		want := recorded[strconv.FormatUint(seed, 10)]
		var got []string
		for rel := range treegen.WalkSet(t, treegen.Gen(t, seed)) {
			got = append(got, rel)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("seed %d: the walk set is %v, want the recorded %v", seed, got, want)
		}
	}
}
