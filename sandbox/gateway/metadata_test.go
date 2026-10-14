package gateway

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// emptyTar is a tar stream with no entries.
var emptyTar = make([]byte, 1024)

// TS-19-5: the client sends the bearer token on every RPC and the sandbox id
// only on Agent RPCs. 19-REQ-1.5.
func TestMetadata_TS19_5(t *testing.T) {
	f := newFakeGW(t, nil)
	f.agent.SetExport(emptyTar, nil)
	p := f.dial(t)
	ctx := context.Background()
	sb, err := p.Create(ctx, testSpec)
	if err != nil {
		t.Fatal(err)
	}
	_ = tool(t, sb, "read_file").Execute(ctx, []byte(`{"path":"x"}`))
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)}); err != nil {
		t.Fatal(err)
	}
	if err := sb.Export(ctx, ".", sandbox.TarOut{W: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(ctx, "another-id"); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var agentRPCs []string
	for _, r := range f.recorded() {
		if got := r.md.Get("authorization"); !slices.Equal(got, []string{"Bearer " + testToken}) {
			t.Errorf("%s carried authorization %q", r.method, got)
		}
		id := r.md.Get(SandboxIDKey)
		if strings.HasPrefix(r.method, "/agentkit.sandbox.v1.Agent/") {
			agentRPCs = append(agentRPCs, r.method)
			if !slices.Equal(id, []string{sb.ID()}) {
				t.Errorf("%s carried %s %q, want [%s]", r.method, SandboxIDKey, id, sb.ID())
			}
		} else if id != nil {
			t.Errorf("the Gateway RPC %s carried %s %q", r.method, SandboxIDKey, id)
		}
	}
	for _, m := range []string{"Hello", "Tools", "Call", "SyncTar", "ExportTar"} {
		if !slices.Contains(agentRPCs, "/agentkit.sandbox.v1.Agent/"+m) {
			t.Errorf("no %s RPC was recorded", m)
		}
	}
	if d := f.lastDestroy(); d.id != sb.ID() {
		t.Errorf("the DestroyRequest Close sent carried id %q, want %q", d.id, sb.ID())
	}
}
