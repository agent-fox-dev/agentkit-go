package sandboxpb

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// goPackage is a file descriptor's go_package option.
func goPackage(fd protoreflect.FileDescriptor) string {
	return fd.Options().(*descriptorpb.FileOptions).GetGoPackage()
}

// TS-19-1: gateway.proto declares one Gateway service of six unary RPCs in
// agentkit.sandbox.v1, and sandbox.proto still declares exactly one service.
// 19-REQ-1.1.
func TestGatewayProtoShape_TS19_1(t *testing.T) {
	fd := File_gateway_proto
	if fd.Package() != "agentkit.sandbox.v1" {
		t.Errorf("package %q, want agentkit.sandbox.v1", fd.Package())
	}
	if goPackage(fd) != goPackage(File_sandbox_proto) {
		t.Errorf("go_package %q, want sandbox.proto's %q", goPackage(fd), goPackage(File_sandbox_proto))
	}
	if fd.Services().Len() != 1 || fd.Services().Get(0).Name() != "Gateway" {
		t.Fatalf("want exactly one service Gateway, have %d", fd.Services().Len())
	}
	svc := fd.Services().Get(0)
	var names []string
	for i := range svc.Methods().Len() {
		m := svc.Methods().Get(i)
		names = append(names, string(m.Name()))
		if m.IsStreamingClient() || m.IsStreamingServer() {
			t.Errorf("%s streams; every Gateway RPC is unary", m.Name())
		}
	}
	slices.Sort(names)
	if want := []string{"Capabilities", "Create", "Destroy", "List", "Reap", "SyncGit"}; !slices.Equal(names, want) {
		t.Errorf("methods %v, want %v", names, want)
	}
	if n := File_sandbox_proto.Services().Len(); n != 1 || File_sandbox_proto.Services().Get(0).Name() != "Agent" {
		t.Errorf("sandbox.proto declares %d services, want only Agent", n)
	}
}

// TS-19-2: SandboxSpec mirrors sandbox.Spec field for field, times are Unix
// nanoseconds, and Refusal carries the six kinds. 19-REQ-1.2.
func TestGatewayMessages_TS19_2(t *testing.T) {
	fd := File_gateway_proto
	msg := func(name string) protoreflect.MessageDescriptor {
		t.Helper()
		m := fd.Messages().ByName(protoreflect.Name(name))
		if m == nil {
			t.Fatalf("no message %s", name)
		}
		return m
	}
	field := func(m protoreflect.MessageDescriptor, name string) protoreflect.FieldDescriptor {
		t.Helper()
		f := m.Fields().ByName(protoreflect.Name(name))
		if f == nil {
			t.Fatalf("%s has no field %s", m.Name(), name)
		}
		return f
	}

	spec := msg("SandboxSpec")
	// The Spec field each Go field of sandbox.Spec travels in.
	wire := map[string]string{"Image": "image", "Workdir": "workdir", "Env": "env", "Resources": "resources",
		"Network": "network", "TTL": "ttl_nanos", "Labels": "labels", "Provider": "provider", "RequireLandlock": "require_landlock"}
	st := reflect.TypeFor[sandbox.Spec]()
	for i := range st.NumField() {
		if f := st.Field(i); f.IsExported() {
			name, ok := wire[f.Name]
			if !ok {
				t.Errorf("sandbox.Spec.%s has no SandboxSpec field", f.Name)
				continue
			}
			field(spec, name)
		}
	}
	if spec.Fields().Len() != len(wire) {
		t.Errorf("SandboxSpec has %d fields, sandbox.Spec %d", spec.Fields().Len(), len(wire))
	}
	for _, name := range []string{"env", "labels"} {
		f := field(spec, name)
		if !f.IsMap() || f.MapKey().Kind() != protoreflect.StringKind || f.MapValue().Kind() != protoreflect.StringKind {
			t.Errorf("SandboxSpec.%s is not map<string,string>", name)
		}
	}
	for name, kind := range map[string]protoreflect.Kind{"image": protoreflect.StringKind, "workdir": protoreflect.StringKind,
		"ttl_nanos": protoreflect.Int64Kind, "provider": protoreflect.BytesKind, "require_landlock": protoreflect.BoolKind} {
		if k := field(spec, name).Kind(); k != kind {
			t.Errorf("SandboxSpec.%s is %v, want %v", name, k, kind)
		}
	}
	res := field(spec, "resources").Message()
	for name, kind := range map[string]protoreflect.Kind{"cpus": protoreflect.DoubleKind, "memory_bytes": protoreflect.Int64Kind,
		"pids": protoreflect.Int64Kind, "disk_bytes": protoreflect.Int64Kind} {
		if k := field(res, name).Kind(); k != kind {
			t.Errorf("Resources.%s is %v, want %v", name, k, kind)
		}
	}
	if res.Fields().Len() != 4 {
		t.Errorf("Resources has %d fields, want 4", res.Fields().Len())
	}
	nw := field(spec, "network").Message()
	if f := field(nw, "mode"); f.Kind() != protoreflect.EnumKind || f.Enum().Name() != "NetworkMode" {
		t.Errorf("Network.mode is %v, want the NetworkMode enum", f.Kind())
	}
	allow := field(nw, "allow")
	if allow.Cardinality() != protoreflect.Repeated || allow.Kind() != protoreflect.MessageKind {
		t.Fatalf("Network.allow is not a repeated message")
	}
	if field(allow.Message(), "host").Kind() != protoreflect.StringKind || field(allow.Message(), "port").Kind() != protoreflect.Int32Kind {
		t.Errorf("Network.allow is not a list of host and port")
	}

	modes := fd.Enums().ByName("NetworkMode")
	if modes == nil {
		t.Fatal("no enum NetworkMode")
	}
	for name, want := range map[string]sandbox.NetworkMode{"NONE": sandbox.NetworkNone, "ALLOWLIST": sandbox.NetworkAllowlist, "UNRESTRICTED": sandbox.NetworkUnrestricted} {
		v := modes.Values().ByName(protoreflect.Name(name))
		if v == nil || int(v.Number()) != int(want) {
			t.Errorf("NetworkMode %s is %v, want %d", name, v, int(want))
		}
	}

	refusal := msg("Refusal")
	for _, name := range []string{"kind", "field", "reason", "message"} {
		field(refusal, name)
	}
	if refusal.Fields().Len() != 4 {
		t.Errorf("Refusal has %d fields, want 4", refusal.Fields().Len())
	}
	var kinds []string
	kv := field(refusal, "kind").Enum().Values()
	for i := range kv.Len() {
		kinds = append(kinds, string(kv.Get(i).Name()))
	}
	if want := []string{"UNSUPPORTED", "NO_AGENT", "NO_PUBLISHED_IMAGE", "PROTOCOL", "CAPACITY", "REFUSED"}; !slices.Equal(kinds, want) {
		t.Errorf("Refusal kinds %v, want %v", kinds, want)
	}

	entry := field(msg("ListResponse"), "sandboxes").Message()
	for _, m := range []protoreflect.MessageDescriptor{msg("CreateResponse"), entry} {
		for _, name := range []string{"created_unix_nanos", "expires_unix_nanos"} {
			if k := field(m, name).Kind(); k != protoreflect.Int64Kind {
				t.Errorf("%s.%s is %v, want int64", m.Name(), name, k)
			}
		}
	}
	if k := field(msg("CapabilitiesResponse"), "max_ttl_nanos").Kind(); k != protoreflect.Int64Kind {
		t.Errorf("CapabilitiesResponse.max_ttl_nanos is %v, want int64", k)
	}
	for i := range fd.Imports().Len() {
		if p := fd.Imports().Get(i).Path(); strings.HasPrefix(p, "google/protobuf/") {
			t.Errorf("gateway.proto imports the well-known type %s", p)
		}
	}
}

// TS-19-7: an edited gateway.proto fails the offline staleness check with
// "run make proto". 19-REQ-1.7.
func TestGatewayProtoNotStale_TS19_7(t *testing.T) {
	t.Setenv("PATH", "")
	proto, err := os.ReadFile("gateway.proto")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile("gateway.proto.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkProtoHashFor("gateway.proto", proto, recorded); err != nil {
		t.Fatal(err)
	}
	edited := append(slices.Clone(proto), []byte("\n// edit\n")...)
	err = checkProtoHashFor("gateway.proto", edited, recorded)
	if err == nil || !strings.Contains(err.Error(), "run make proto") {
		t.Fatalf("an edited gateway.proto gave %v, want an error saying to run make proto", err)
	}
}
