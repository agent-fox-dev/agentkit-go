package treegen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/treegen"
)

// testdata/walksets.json holds the walk sets tools.Walk listed for seeds 1 to
// 5; the walk treegen's oracles stand on must list the same, and must not read
// a global excludes file that would hide everything.
func TestWalkSetIsTheRecordedOneForEachSeed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "walksets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string][]string
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	// git's own core.excludesFile would take precedence over the XDG path, so
	// it names the same file.
	cfg := t.TempDir()
	ignore := filepath.Join(cfg, "git", "ignore")
	gitconfig := filepath.Join(cfg, "gitconfig")
	if err := os.MkdirAll(filepath.Dir(ignore), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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

// The oracles partition a tree: what Walk lists, the .git trees, the ignored
// entries; and every escaping link is a symlink.
func TestOraclesDescribeAGeneratedPair(t *testing.T) {
	src, recv, outside := t.TempDir(), t.TempDir(), t.TempDir()
	treegen.Pair(t, 3, src, recv, outside)
	for _, root := range []string{src, recv} {
		listed, git, ignored := treegen.WalkSet(t, root), treegen.GitTrees(t, root), treegen.Ignored(t, root)
		entries := treegen.Entries(t, root)
		for rel := range ignored {
			if listed[rel] || git[rel] {
				t.Errorf("%s is ignored and also listed or in a .git tree", rel)
			}
		}
		for rel := range git {
			if !strings.Contains("/"+rel+"/", "/.git/") && !strings.HasSuffix(rel, ".git") {
				t.Errorf("GitTrees holds %s, outside any .git", rel)
			}
		}
		esc := treegen.Escaping(t, root)
		if len(esc) == 0 {
			t.Errorf("%s: the pair has no escaping link", root)
		}
		for rel := range esc {
			if !strings.HasPrefix(entries[rel], "link ") {
				t.Errorf("Escaping holds %s, which Entries describes as %q", rel, entries[rel])
			}
		}
	}
}

func TestSnapshotRecordsAbsenceTimesAndEverythingUnderAPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := treegen.Snapshot(t, root, map[string]bool{"d": true, "gone": true})
	if snap["gone"] != "absent" {
		t.Errorf("gone = %q, want absent", snap["gone"])
	}
	if want := `file -rw------- "x" `; !strings.HasPrefix(snap["d/f"], want) || len(snap["d/f"]) == len(want) {
		t.Errorf("d/f = %q, want %q and a modification time", snap["d/f"], want)
	}
	if !strings.HasPrefix(snap["d"], "dir ") {
		t.Errorf("d = %q, want a directory", snap["d"])
	}
	if got := treegen.Describe(filepath.Join(root, "gone"), false); got != "absent" {
		t.Errorf("Describe of a missing path = %q", got)
	}
}
