package sandboxpb

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// Each message family the agent and the gateway exchange survives the wire
// unchanged: oneof members, nested reports, maps and JSON payload bytes.
func TestMessagesSurviveTheWire(t *testing.T) {
	for _, m := range []proto.Message{
		&RsyncFrame{Frame: &RsyncFrame_Start{Start: &RsyncStart{Direction: RsyncStart_EXPORT, Path: "sub", IncludeIgnored: true, ExcludeGit: true, Delete: true}}},
		&RsyncFrame{Frame: &RsyncFrame_Plan{Plan: &RsyncPlan{Rules: []string{"build", ".git"}, File: true}}},
		&RsyncFrame{Frame: &RsyncFrame_Args{Args: &RsyncArgs{Args: []string{"--server"}, Rules: []string{"x"}, Present: []string{"a"}}}},
		&RsyncFrame{Frame: &RsyncFrame_Data{Data: []byte{0, 1, 2}}},
		&RsyncFrame{Frame: &RsyncFrame_Done{Done: &RsyncDone{Files: 2, Bytes: 9, Deleted: 1, Present: []string{"a"}}}},
		&TarChunk{Body: &TarChunk_Data{Data: []byte("tar")}},
		&HelloResponse{ProtocolMajor: 1, ProtocolMinor: 2, Version: "v1", Workdir: "/w", OutlineBackend: "tree-sitter", Git: true,
			Landlock: &LandlockReport{Abi: 4, KernelAbi: 5, Tcp: true, Note: "n", Writable: []string{"/w"}, ReadOnly: []string{"/usr"}}},
		&CallRequest{Name: "read", Input: []byte(`{"path":"a"}`)},
		&CallResponse{Ok: true, Data: []byte(`{}`), Text: "t", Metadata: []byte(`{"k":1}`)},
		&SandboxSpec{Image: "img", Workdir: "/w", Env: map[string]string{"A": "1"}, TtlNanos: 5, Labels: map[string]string{"l": "v"},
			Resources: &Resources{Cpus: 1.5, MemoryBytes: 1 << 30, Pids: 9, DiskBytes: 1 << 31},
			Network:   &Network{Mode: NetworkMode_ALLOWLIST, Allow: []*Endpoint{{Host: "h", Port: 443}}},
			Provider:  []byte(`{"k":"v"}`), RequireLandlock: true},
		&Refusal{Kind: Refusal_CAPACITY, Field: "Resources", Reason: "r", Message: "m"},
	} {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", m, err)
		}
		got := m.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(b, got); err != nil {
			t.Fatalf("Unmarshal(%T): %v", m, err)
		}
		if !proto.Equal(got, m) {
			t.Errorf("%T round trip\n got %v\nwant %v", m, got, m)
		}
	}
}
