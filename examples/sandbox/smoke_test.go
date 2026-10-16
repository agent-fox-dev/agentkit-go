package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TS-22-36 (smoke, 22-PATH-2): an embedder opens a local sandbox, runs a
// guarded agent in it and exports the result. sandbox.Open, the local
// provider, the sandbox agent, core.Named, guard.Allow, codemode, the faux
// provider and the agent are all real: the agent reads and searches inside the
// sandbox, the guard refuses rm, src is exported to a host directory, and the
// deferred Close removes the sandbox's directory.
func TestSmokeSandboxOpenGuardedAgentExport_TS22_36(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX ls")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := t.TempDir()

	var b strings.Builder
	if err := run(context.Background(), &b, dir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"README: Says hello from inside the sandbox.", "TODOs: 2", "src: main.go, util.go",
		"rm: blocked_by_policy", "exported src: main.go, util.go",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	for _, name := range []string{"main.go", "util.go"} {
		if _, err := os.Stat(filepath.Join(dir, "out", name)); err != nil {
			t.Errorf("the exported %s is not in the output directory: %v", name, err)
		}
	}
	root := filepath.Join(tmp, "agentkit-sandbox-local")
	ents, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(ents) > 0 {
		t.Errorf("%d sandbox directories remain under %s after the deferred Close", len(ents), root)
	}
}
