package sandboxpb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TS-13-1: the generated descriptor declares agentkit.sandbox.v1 with one
// Agent service of six RPCs (spec 14 added Rsync), JSON payloads as bytes,
// and self-contained Call messages. 13-REQ-1.1, 13-REQ-1.7.
func TestProtocolShape_TS13_1(t *testing.T) {
	fd := File_sandbox_proto
	if fd == nil {
		t.Fatal("File_sandbox_proto is nil: no generated descriptor")
	}
	if fd.Package() != "agentkit.sandbox.v1" {
		t.Errorf("package %q", fd.Package())
	}
	gp := fd.Options().(*descriptorpb.FileOptions).GetGoPackage()
	if path, _, _ := strings.Cut(gp, ";"); !strings.HasSuffix(path, "sandbox/internal/sandboxpb") {
		t.Errorf("go_package %q does not end in sandbox/internal/sandboxpb", gp)
	}
	if fd.Services().Len() != 1 || fd.Services().Get(0).Name() != "Agent" {
		t.Fatalf("want exactly one service Agent, have %d", fd.Services().Len())
	}
	svc := fd.Services().Get(0)
	var names []string
	for i := range svc.Methods().Len() {
		names = append(names, string(svc.Methods().Get(i).Name()))
	}
	slices.Sort(names)
	if want := []string{"Call", "ExportTar", "Hello", "Rsync", "SyncTar", "Tools"}; !slices.Equal(names, want) {
		t.Fatalf("methods %v, want %v", names, want)
	}
	shape := func(name string) string {
		m := svc.Methods().ByName(protoreflect.Name(name))
		return fmt.Sprintf("%v/%v %s->%s", m.IsStreamingClient(), m.IsStreamingServer(), m.Input().Name(), m.Output().Name())
	}
	for name, want := range map[string]string{
		"Hello":     "false/false HelloRequest->HelloResponse",
		"Tools":     "false/false ToolsRequest->ToolsResponse",
		"Call":      "false/false CallRequest->CallResponse",
		"SyncTar":   "true/false TarChunk->SyncResponse",
		"ExportTar": "false/true ExportRequest->TarChunk",
		"Rsync":     "true/true RsyncFrame->RsyncFrame",
	} {
		if got := shape(name); got != want {
			t.Errorf("%s is %s, want %s", name, got, want)
		}
	}

	kinds := func(msg string) map[string]protoreflect.Kind {
		m := fd.Messages().ByName(protoreflect.Name(msg))
		if m == nil {
			t.Fatalf("no message %s", msg)
		}
		out := map[string]protoreflect.Kind{}
		for i := range m.Fields().Len() {
			f := m.Fields().Get(i)
			out[string(f.Name())] = f.Kind()
		}
		return out
	}
	req, resp := kinds("CallRequest"), kinds("CallResponse")
	wantReq := map[string]protoreflect.Kind{"name": protoreflect.StringKind, "input": protoreflect.BytesKind}
	wantResp := map[string]protoreflect.Kind{
		"ok": protoreflect.BoolKind, "data": protoreflect.BytesKind, "error": protoreflect.StringKind,
		"detail": protoreflect.StringKind, "metadata": protoreflect.BytesKind, "text": protoreflect.StringKind,
	}
	for _, c := range []struct {
		got, want map[string]protoreflect.Kind
	}{{req, wantReq}, {resp, wantResp}} {
		if len(c.got) != len(c.want) {
			t.Errorf("fields %v, want %v", c.got, c.want)
		}
		for name, k := range c.want {
			if c.got[name] != k {
				t.Errorf("field %s is %v, want %v", name, c.got[name], k)
			}
		}
		for name, k := range c.got {
			if k == protoreflect.MessageKind || k == protoreflect.GroupKind {
				t.Errorf("field %s is message-typed: Call messages must be self-contained", name)
			}
		}
	}
}

// checkProtoHash reports whether recorded is the hex SHA-256 of proto, the
// offline staleness check behind TS-13-5.
func checkProtoHash(proto, recorded []byte) error {
	sum := sha256.Sum256(proto)
	if got := strings.TrimSpace(string(recorded)); got != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("sandbox.proto hashes to %x but sandbox.proto.sha256 records %q: the generated code is stale; run make proto", sum, got)
	}
	return nil
}

// TS-13-5: the offline staleness check fails with a "run make proto" message
// when the .proto hash differs from the recorded one. 13-REQ-1.5.
func TestProtoNotStale_TS13_5(t *testing.T) {
	t.Setenv("PATH", "")
	proto, err := os.ReadFile("sandbox.proto")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile("sandbox.proto.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkProtoHash(proto, recorded); err != nil {
		t.Fatal(err)
	}
	edited := append(slices.Clone(proto), []byte("\n// edit\n")...)
	err = checkProtoHash(edited, recorded)
	if err == nil || !strings.Contains(err.Error(), "make proto") {
		t.Fatalf("an edited .proto gave %v, want an error saying to run make proto", err)
	}
}
