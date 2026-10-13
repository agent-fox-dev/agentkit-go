package sandboxpb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// TS-14-4: the generated descriptor declares a bidirectional Rsync RPC over
// RsyncFrame with the start, plan, args, data and done oneof members and
// their fields. 14-REQ-2.1.
func TestRsyncShape_TS14_4(t *testing.T) {
	fd := File_sandbox_proto
	m := fd.Services().ByName("Agent").Methods().ByName("Rsync")
	if m == nil {
		t.Fatal("service Agent has no Rsync method")
	}
	if !m.IsStreamingClient() || !m.IsStreamingServer() || m.Input().Name() != "RsyncFrame" || m.Output().Name() != "RsyncFrame" {
		t.Errorf("Rsync is %v/%v %s->%s, want bidirectional RsyncFrame", m.IsStreamingClient(), m.IsStreamingServer(), m.Input().Name(), m.Output().Name())
	}
	frame := fd.Messages().ByName("RsyncFrame")
	if frame.Oneofs().Len() != 1 {
		t.Fatalf("RsyncFrame has %d oneofs, want 1", frame.Oneofs().Len())
	}
	members := map[string]protoreflect.FieldDescriptor{}
	oo := frame.Oneofs().Get(0)
	for i := range oo.Fields().Len() {
		f := oo.Fields().Get(i)
		members[string(f.Name())] = f
	}
	if len(members) != 5 {
		t.Errorf("oneof members %v, want exactly start, plan, args, data, done", members)
	}
	for _, n := range []string{"start", "plan", "args", "data", "done"} {
		if members[n] == nil {
			t.Fatalf("oneof lacks %s", n)
		}
	}
	if members["data"].Kind() != protoreflect.BytesKind {
		t.Errorf("data is %v, want bytes", members["data"].Kind())
	}

	field := func(msg protoreflect.MessageDescriptor, name string, kind protoreflect.Kind, list bool) {
		t.Helper()
		f := msg.Fields().ByName(protoreflect.Name(name))
		if f == nil || f.Kind() != kind || f.IsList() != list {
			t.Errorf("%s.%s is %v, want kind %v list %v", msg.Name(), name, f, kind, list)
		}
	}
	start := members["start"].Message()
	dir := start.Fields().ByName("direction")
	if dir == nil || dir.Kind() != protoreflect.EnumKind {
		t.Fatalf("start.direction is %v, want an enum", dir)
	}
	vals := dir.Enum().Values()
	if vals.Len() != 3 || vals.ByNumber(0) == nil || vals.ByName("SYNC") == nil || vals.ByName("EXPORT") == nil {
		t.Errorf("direction values: %d, want an unspecified zero value, SYNC and EXPORT", vals.Len())
	}
	field(start, "path", protoreflect.StringKind, false)
	for _, n := range []string{"include_ignored", "exclude_git", "delete"} {
		field(start, n, protoreflect.BoolKind, false)
	}
	plan := members["plan"].Message()
	field(plan, "rules", protoreflect.StringKind, true)
	field(plan, "file", protoreflect.BoolKind, false)
	field(members["args"].Message(), "args", protoreflect.StringKind, true)
	done := members["done"].Message()
	for _, n := range []string{"files", "bytes", "deleted"} {
		field(done, n, protoreflect.Int64Kind, false)
	}
}
