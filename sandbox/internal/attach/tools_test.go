package attach

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-13-38: a digest, name or order difference from the agent is refused
// with ErrToolMismatch naming each differing tool and both versions.
// 13-REQ-6.3.
func TestToolMismatch_TS13_38(t *testing.T) {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, Version: "v9.9.9", Workdir: "/ws"})
	sb := attachFake(t, f, Config{})
	index := func(resp *sandboxpb.ToolsResponse, name string) int {
		for i, ti := range resp.Tools {
			if ti.Name == name {
				return i
			}
		}
		t.Fatalf("no %s", name)
		return -1
	}
	cases := []struct {
		names  []string
		mutate func(*sandboxpb.ToolsResponse)
	}{
		{[]string{"read_file"}, func(r *sandboxpb.ToolsResponse) { r.Tools[index(r, "read_file")].Digest[0] ^= 1 }},
		{[]string{"edit_file", "write_file"}, func(r *sandboxpb.ToolsResponse) {
			i, j := index(r, "edit_file"), index(r, "write_file")
			r.Tools[i], r.Tools[j] = r.Tools[j], r.Tools[i]
		}},
		{[]string{"find_references"}, func(r *sandboxpb.ToolsResponse) {
			i := index(r, "find_references")
			r.Tools = append(r.Tools[:i], r.Tools[i+1:]...)
		}},
	}
	for _, tc := range cases {
		resp := proto.Clone(fakeagent.HostTools()).(*sandboxpb.ToolsResponse)
		tc.mutate(resp)
		f.SetTools(resp, nil)
		tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
		if tl != nil || !errors.Is(err, sandbox.ErrToolMismatch) {
			t.Fatalf("%v: Tools = %d tools, %v; want ErrToolMismatch", tc.names, len(tl), err)
		}
		for _, want := range append(tc.names, "v9.9.9", buildver.Version()) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: message %q does not name %s", tc.names, err, want)
			}
		}
		n := f.Count("SyncTar")
		if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(emptyTar)}); err != nil || f.Count("SyncTar") != n+1 {
			t.Errorf("%v: Sync after the refusal: %v, %d RPCs", tc.names, err, f.Count("SyncTar")-n)
		}
	}
}
