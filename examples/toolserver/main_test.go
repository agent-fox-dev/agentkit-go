package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

func TestTheProgramDrivesTheCommandInBothModes(t *testing.T) {
	env := testutil.Environ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "OPENROUTER_API_KEY")
	tmp := t.TempDir()
	env["TMPDIR"] = tmp
	res := testutil.GoRun(t, "", ".", env)
	if res.Exit != 0 {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	wants := []string{"agentkit-tools serves 13 tools: read_file", "code_mode", "toolserver example completed successfully"}
	if runtime.GOOS == "windows" {
		wants = []string{"code_search is not served on this platform", "toolserver example completed successfully"}
	} else {
		wants = append(wants,
			"session: code_search for needle found a.go",
			"session: after edit_file, code_search for fresh_marker found b.go",
			"call: code_search for needle found a.go",
			"call: code_mode read 3 files")
	}
	for _, w := range wants {
		if !strings.Contains(res.Stdout, w) {
			t.Errorf("the output lacks %q:\n%s", w, res.Stdout)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "agentkit-toolserver-example-*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}
