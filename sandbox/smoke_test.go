package sandbox_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-13-69 (smoke, 13-PATH-1): an embedder creates a local sandbox, syncs a
// project, gets eleven proxy tools, reads a synced file and closes the
// sandbox. Every component is real: local, attach, the agent, the gRPC
// client and server, the stdio adapter over os.Pipe, the tar rules and
// tools.For.
func TestSmokeCreateSyncReadClose_TS13_69(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	sb, err := local.Unisolated().Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.TempDir(), "agentkit-sandbox-local", sb.ID())
	for _, p := range []string{"workspace", "home", "tmp", "agentkit-sandbox.json"} {
		if !exists(filepath.Join(dir, p)) {
			t.Errorf("%s is missing from the sandbox directory", p)
		}
	}
	res, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, file("hello.txt", "hi there\n"), file("src/main.go", "package main\n")))})
	if err != nil || res.Files != 2 {
		t.Fatalf("Sync = %+v, %v", res, err)
	}
	proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	host, _ := tools.For(ws, tools.Options{})
	if len(proxies) != 11 || len(host) != 11 {
		t.Fatalf("%d proxies, %d tools.For tools; want 11", len(proxies), len(host))
	}
	for i := range host {
		if proxies[i].Name != host[i].Name {
			t.Errorf("proxy %d is %s, tools.For has %s", i, proxies[i].Name, host[i].Name)
		}
	}
	r := tool(t, sb, "read_file").Execute(ctx, []byte(`{"path":"hello.txt"}`))
	if !r.OK || !strings.Contains(r.Text, "hi there") || r.Data == nil || !strings.Contains(fmt.Sprint(r.Data["content"]), "hi there") {
		t.Fatalf("read_file: %+v", r)
	}
	if r.Metadata == nil {
		t.Error("read_file's metadata did not come back decoded")
	}
	if err := sb.Close(ctx); err != nil || exists(dir) {
		t.Fatalf("Close = %v; directory exists %v", err, exists(dir))
	}
}
