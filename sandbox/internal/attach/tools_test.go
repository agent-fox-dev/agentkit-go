package attach

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
)

// The proxies are the host's tools.For definitions, in order, and a real
// agent of this build advertises the same digests.
func TestToolsAreProxiesOfTheHostDefinitions(t *testing.T) {
	sb, _ := real(t)
	got, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	host, err := hostTools(sandbox.ToolOptions{})
	if err != nil || len(got) != len(host) {
		t.Fatalf("%d proxies, %d host tools, %v", len(got), len(host), err)
	}
	for i, h := range host {
		p := got[i]
		if !bytes.Equal(tooldigest.Digest(p), tooldigest.Digest(h)) || p.Label != h.Label ||
			reflect.ValueOf(p.PrepareArguments).Pointer() != reflect.ValueOf(h.PrepareArguments).Pointer() {
			t.Errorf("proxy %d (%s) is not the host's definition of %s", i, p.Name, h.Name)
		}
	}
	for i, ti := range fakeagent.HostTools().Tools {
		if ti.Name != host[i].Name || !bytes.Equal(ti.Digest, tooldigest.Digest(host[i])) {
			t.Errorf("fakeagent's tool %d (%s) is not the host's", i, ti.Name)
		}
	}
}

// The options are fixed by the first success: equal options return the same
// proxies without an RPC, other options ErrToolsConfigured.
func TestToolsIsConfiguredOnce(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	ctx := context.Background()
	a, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if err != nil || len(b) != len(a) || f.Count("Tools") != 1 {
		t.Errorf("a repeat: %d tools, %v, %d RPCs", len(b), err, f.Count("Tools"))
	}
	if _, err := sb.Tools(ctx, sandbox.ToolOptions{DisableSpill: true}); !errors.Is(err, sandbox.ErrToolsConfigured) {
		t.Errorf("other options: %v, want ErrToolsConfigured", err)
	}
}

// A digest, name or order difference is ErrToolMismatch naming each tool and
// both versions; the sandbox stays live.
func TestToolsRefusesAnAgentWithOtherDefinitions(t *testing.T) {
	f := fakeOf(0)
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, Version: "v9.9.9", Workdir: "/ws"})
	sb := attachFake(t, f, Config{})
	at := func(r *sandboxpb.ToolsResponse, name string) int {
		for i, ti := range r.Tools {
			if ti.Name == name {
				return i
			}
		}
		t.Fatalf("no %s", name)
		return -1
	}
	for _, c := range []struct {
		names  []string
		mutate func(*sandboxpb.ToolsResponse)
	}{
		{[]string{"read_file"}, func(r *sandboxpb.ToolsResponse) { r.Tools[at(r, "read_file")].Digest[0] ^= 1 }},
		{[]string{"edit_file", "write_file"}, func(r *sandboxpb.ToolsResponse) {
			i, j := at(r, "edit_file"), at(r, "write_file")
			r.Tools[i], r.Tools[j] = r.Tools[j], r.Tools[i]
		}},
		{[]string{"find_references is missing"}, func(r *sandboxpb.ToolsResponse) {
			i := at(r, "find_references")
			r.Tools = append(r.Tools[:i], r.Tools[i+1:]...)
		}},
		{[]string{"extra is not a host tool"}, func(r *sandboxpb.ToolsResponse) { r.Tools = append(r.Tools, &sandboxpb.ToolInfo{Name: "extra"}) }},
	} {
		resp := proto.Clone(fakeagent.HostTools()).(*sandboxpb.ToolsResponse)
		c.mutate(resp)
		f.SetTools(resp, nil)
		tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
		if tl != nil || !errors.Is(err, sandbox.ErrToolMismatch) {
			t.Fatalf("%v: Tools = %d tools, %v; want ErrToolMismatch", c.names, len(tl), err)
		}
		for _, want := range append(c.names, "v9.9.9", buildver.Version()) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: message %q does not name %s", c.names, err, want)
			}
		}
	}
	if err := Live(sb); err != nil {
		t.Errorf("Live after the refusals = %v", err)
	}
}

// The digests an agent released earlier advertises (the tooldigest
// package's testdata) are accepted.
func TestToolsAcceptsTheRecordedDigests(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "tooldigest", "testdata", "digests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded []struct{ Name, Digest string }
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	resp := &sandboxpb.ToolsResponse{}
	for _, r := range recorded {
		d, _ := hex.DecodeString(r.Digest)
		resp.Tools = append(resp.Tools, &sandboxpb.ToolInfo{Name: r.Name, Digest: d})
	}
	f := fakeagent.New()
	f.SetTools(resp, nil)
	if tl, err := attachFake(t, f, Config{}).Tools(context.Background(), sandbox.ToolOptions{}); err != nil || len(tl) != len(recorded) {
		t.Fatalf("Tools = %d tools, %v; want the %d recorded", len(tl), err, len(recorded))
	}
}
