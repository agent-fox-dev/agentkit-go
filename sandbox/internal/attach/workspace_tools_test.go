package attach

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
)

// builtinNames is the order tools.For returns the built-ins in.
var builtinNames = []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
	"file_outline", "find_symbol", "find_references", "execute", "run_command"}

// TS-20-57 (the attach and fakeagent half): hostTools and fakeagent build the
// eleven built-ins with tools.For over a throwaway workspace, and their
// definitions are the same, so a host and an agent of one build agree.
func TestHostToolsAndFakeagentBuildWithFor_TS20_57(t *testing.T) {
	host, err := hostTools(sandbox.ToolOptions{})
	if err != nil {
		t.Fatalf("hostTools: %v", err)
	}
	var names []string
	for _, tl := range host {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, builtinNames) {
		t.Fatalf("hostTools returned %v, want %v", names, builtinNames)
	}
	fake := fakeagent.HostTools()
	if len(fake.Tools) != len(host) {
		t.Fatalf("fakeagent advertises %d tools, hostTools builds %d", len(fake.Tools), len(host))
	}
	for i, tl := range host {
		if fake.Tools[i].Name != tl.Name || string(fake.Tools[i].Digest) != string(tooldigest.Digest(tl)) {
			t.Errorf("tool %d: fakeagent has %s, hostTools has %s: the digests must agree", i, fake.Tools[i].Name, tl.Name)
		}
	}
}

// TS-20-58: a host from this change attaches to an agent whose tool
// definitions were recorded before it, with no ErrToolMismatch. The digests in
// the tooldigest package's testdata were computed from tools.All before the
// workspace package existed.
func TestHostAcceptsAnAgentWithPreChangeDigests_TS20_58(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "tooldigest", "testdata", "digests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	resp := &sandboxpb.ToolsResponse{}
	for _, r := range recorded {
		d, err := hex.DecodeString(r.Digest)
		if err != nil {
			t.Fatal(err)
		}
		resp.Tools = append(resp.Tools, &sandboxpb.ToolInfo{Name: r.Name, Digest: d})
	}

	f := fakeagent.New()
	f.SetTools(resp, nil)
	sb := attachFake(t, f, Config{})
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if errors.Is(err, sandbox.ErrToolMismatch) {
		t.Fatalf("a host refused an agent with the digests recorded before this change: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(tl) != len(recorded) {
		t.Fatalf("the host exposes %d tools, the agent advertised %d", len(tl), len(recorded))
	}
	for i, p := range tl {
		if p.Name != recorded[i].Name {
			t.Errorf("proxy %d is %s, the agent advertised %s", i, p.Name, recorded[i].Name)
		}
	}
}
