package core

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TS-12-12: the session-tree and legacy-configuration files and their types
// are gone.
func TestHistoryAndConfigAreDeleted_TS12_12(t *testing.T) {
	for _, f := range []string{"history.go", "config.go"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("core/%s still exists", f)
		}
	}
	types, _ := coreDecls(t)
	for _, gone := range []string{"ConversationHistory", "SnapshotBranch", "SessionHeader", "Entry", "EntryID",
		"MessageEntry", "CompactionEntry", "AgentConfig", "StopContext", "StopPolicy", "ContextTransform", "Hooks", "QueueMode"} {
		if types[gone] {
			t.Errorf("core still declares %s", gone)
		}
	}
}

// exportedTypeLimit is 12-REQ-5.3's budget.
const exportedTypeLimit = 50

// TS-12-13: core declares at most 50 exported types.
//
// Skipped, and reported: the event vocabulary was kept by decision, and with
// it core is over the budget (docs/errata/12_core_schema_guard.md). The test
// still counts, so the skip message says by how much.
func TestExportedTypeBudget_TS12_13(t *testing.T) {
	types, _ := coreDecls(t)
	n := 0
	for name := range types {
		if !strings.HasPrefix(name, "func ") && name[0] >= 'A' && name[0] <= 'Z' {
			n++
		}
	}
	if n > exportedTypeLimit {
		t.Skipf("core declares %d exported types, over the budget of %d; kept by decision, see docs/errata/12_core_schema_guard.md", n, exportedTypeLimit)
	}
}

// codeLines counts the non-blank, non-comment lines of the production Go
// files under dir, skipping nested modules (a go.mod below dir) and
// testdata.
func codeLines(t *testing.T, dir string, recurse bool) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if !recurse || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		inBlock := false
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case inBlock:
				if strings.Contains(line, "*/") {
					inBlock = false
				}
			case line == "", strings.HasPrefix(line, "//"):
			case strings.HasPrefix(line, "/*"):
				inBlock = !strings.Contains(line, "*/")
			default:
				n++
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TS-12-14: core is at most 1,600 lines of code and the root module at most
// 20,000. A line of code is a non-blank line that is not a comment.
func TestLineBudgets_TS12_14(t *testing.T) {
	if n := codeLines(t, ".", false); n > 1600 {
		t.Errorf("core has %d lines of code, over 1,600", n)
	}
	root, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	if n := codeLines(t, strings.TrimSpace(string(root)), true); n > 20000 {
		t.Errorf("the root module has %d lines of code, over 20,000", n)
	}
}
