package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-22-17 (integration): Open on local with a LocalDir returns a live
// sandbox whose proxies match Sandbox.Tools. 22-REQ-4.4.
func TestOpenOnLocalWithLocalDir_TS22_17(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LocalDir is refused on Windows")
	}
	isolate(t)
	ctx := context.Background()
	const body = "package main\n\n// the synced text\nfunc main() {}\n"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, tools, err := sandbox.Open(ctx, local.Unisolated(), sandbox.Spec{Network: unrestricted}, sandbox.LocalDir{Path: dir}, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sb == nil {
		t.Fatal("Open returned no sandbox")
	}
	closed := false
	defer func() {
		if !closed {
			_ = sb.Close(ctx)
		}
	}()

	later, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := proxyNames(tools), proxyNames(later); !slices.Equal(got, want) {
		t.Errorf("Open's proxies are %v, a later Tools call returns %v", got, want)
	}

	var read core.Tool
	for _, p := range tools {
		if p.Name == "read_file" {
			read = p
		}
	}
	if read.Execute == nil {
		t.Fatalf("no read_file proxy among %v", proxyNames(tools))
	}
	r := read.Execute(ctx, []byte(`{"path":"src/main.go"}`))
	if !r.OK || !strings.Contains(r.LLMText(), "the synced text") {
		t.Errorf("read_file = ok %v, %q; want the synced file's content", r.OK, r.LLMText())
	}

	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: dir}); err != nil {
		t.Errorf("a second Sync: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out")
	if err := sb.Export(ctx, "src", sandbox.LocalDir{Path: out}); err != nil {
		t.Errorf("Export: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(out, "main.go")); err != nil || string(got) != body {
		t.Errorf("the exported main.go = %q, %v", got, err)
	}
	closed = true
	if err := sb.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func proxyNames(tools []core.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}
