package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// Offline, on provider/faux: the scripts call the bound workspace tools and
// the MCP inventory tool through code_mode, a failed read is a value the
// script handles, the second script's main() is its return value, and the
// example's workspace is removed when it is done.
func TestTheScriptsRunTheBoundToolsAndTheExampleCleansUp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"code_mode binds 4 tools",
		"Go files: main.go, util.go",
		"CHANGELOG.md: read_failed",
		"README.md -> # Example project", "docs/NOTES.md -> Notes: the scripts read these concurrently.",
		`Return value: {"apples": 12, "pears": 0}`,
		"called list_files", "called find_files", "called read_file", "called inventory__stock",
		"model: The workspace has two Go files, no CHANGELOG.md, and 12 apples and 0 pears in stock.",
		"Code mode example completed successfully",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "codemode-example-*")); len(left) > 0 {
		t.Errorf("the example's workspace was not removed: %v", left)
	}
}

func TestTheProgramRunsOffline(t *testing.T) {
	env := testutil.Environ()
	env["TMPDIR"] = t.TempDir()
	if res := testutil.GoRun(t, "", ".", env); res.Exit != 0 || !strings.Contains(res.Stdout, "Code mode example completed successfully") {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}
