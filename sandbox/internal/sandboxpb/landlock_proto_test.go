package sandboxpb_test

import (
	"os"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-15-21: HelloResponse carries LandlockReport landlock = 7 with the six
// specified fields, fields 1 to 6 unchanged, and the regenerated code passes
// the staleness check offline. 15-REQ-4.1.
func TestLandlockReportProto_TS15_21(t *testing.T) {
	fd := sandboxpb.File_sandbox_proto
	hello := fd.Messages().ByName("HelloResponse")
	type field struct {
		name string
		kind protoreflect.Kind
		list bool
	}
	check := func(msg protoreflect.MessageDescriptor, want map[protoreflect.FieldNumber]field) {
		t.Helper()
		if msg.Fields().Len() != len(want) {
			t.Errorf("%s has %d fields, want %d", msg.Name(), msg.Fields().Len(), len(want))
		}
		for n, w := range want {
			f := msg.Fields().ByNumber(n)
			if f == nil || string(f.Name()) != w.name || f.Kind() != w.kind || f.IsList() != w.list {
				t.Errorf("%s field %d is %v, want %s %v (repeated %v)", msg.Name(), n, f, w.name, w.kind, w.list)
			}
		}
	}
	check(hello, map[protoreflect.FieldNumber]field{
		1: {"protocol_major", protoreflect.Uint32Kind, false}, 2: {"protocol_minor", protoreflect.Uint32Kind, false},
		3: {"version", protoreflect.StringKind, false}, 4: {"workdir", protoreflect.StringKind, false},
		5: {"outline_backend", protoreflect.StringKind, false}, 6: {"git", protoreflect.BoolKind, false},
		7: {"landlock", protoreflect.MessageKind, false},
	})
	if f := hello.Fields().ByNumber(7); f != nil && f.Message() != nil && f.Message().Name() != "LandlockReport" {
		t.Errorf("HelloResponse.landlock is a %s, want LandlockReport", f.Message().Name())
	}
	report := fd.Messages().ByName("LandlockReport")
	if report == nil {
		t.Fatal("no message LandlockReport")
	}
	check(report, map[protoreflect.FieldNumber]field{
		1: {"abi", protoreflect.Uint32Kind, false}, 2: {"kernel_abi", protoreflect.Uint32Kind, false},
		3: {"tcp", protoreflect.BoolKind, false}, 4: {"note", protoreflect.StringKind, false},
		5: {"writable", protoreflect.StringKind, true}, 6: {"read_only", protoreflect.StringKind, true},
	})
	proto, err := os.ReadFile("sandbox.proto")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile("sandbox.proto.sha256")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if err := sandboxpb.CheckProtoHash(proto, recorded); err != nil {
		t.Error(err)
	}
}
