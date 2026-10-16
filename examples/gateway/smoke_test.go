package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TS-22-38 (smoke, 22-PATH-4): an embedder opens a sandbox through a gateway
// and lists it empty after closing. gateway.Dial, an in-process gatewayd
// server over the local backend, sandbox.Open, core.Named, guard.Allow, the
// faux provider and the agent are all real: rm is refused with
// blocked_by_policy, src is exported with main.go and util.go, the sandbox is
// closed, and the gateway lists nothing for the token.
func TestSmokeGatewayOpenListsEmptyAfterClose_TS22_38(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LocalDir is refused on Windows")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := t.TempDir()

	var b strings.Builder
	if err := run(context.Background(), &b, io.Discard, dir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"on gateway/local", "listed ", "rm: blocked_by_policy", "exported src: main.go, util.go",
		"the gateway holds 0 sandboxes", "Gateway example completed successfully",
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
		t.Errorf("%d sandbox directories remain under %s after the gateway closed the sandbox", len(ents), root)
	}
}
