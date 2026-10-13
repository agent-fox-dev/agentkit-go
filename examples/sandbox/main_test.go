package main

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TS-13-68: the example runs offline against local and provider/faux and
// exits 0, both as a program and in process. 13-REQ-10.8.
func TestExampleRunsOffline_TS13_68(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX ls")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TMPDIR", t.TempDir())
	out, err := exec.Command("go", "run", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Sandbox example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}
	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"synced 3 files", "read_file src/main.go in the sandbox: ok=true",
		"README: Says hello from inside the sandbox.", "TODOs: 2", "src: main.go, util.go",
		"rm: blocked_by_policy", "exported src: main.go, util.go", "Sandbox example completed successfully"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, b.String())
		}
	}
}
