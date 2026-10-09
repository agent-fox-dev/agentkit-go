package core

import (
	"context"
	"reflect"
	"testing"
)

// TS-12-7: ProviderClient has one method, Stream(ctx, Request) returning a
// channel of StreamEvent and an error; StreamEvent and Request carry the
// spec's fields.
func TestProviderClientShape_TS12_7(t *testing.T) {
	iface := reflect.TypeOf((*ProviderClient)(nil)).Elem()
	if iface.NumMethod() != 1 {
		t.Fatalf("ProviderClient has %d methods, want 1", iface.NumMethod())
	}
	m := iface.Method(0)
	want := reflect.TypeOf((func(context.Context, Request) (<-chan StreamEvent, error))(nil))
	if m.Name != "Stream" || m.Type != want {
		t.Fatalf("ProviderClient.%s is %v, want Stream %v", m.Name, m.Type, want)
	}
	if f, ok := reflect.TypeOf(StreamEvent{}).FieldByName("Event"); !ok || f.Type != reflect.TypeOf((*Event)(nil)).Elem() {
		t.Error("StreamEvent.Event is missing or not an Event")
	}
	if f, ok := reflect.TypeOf(StreamEvent{}).FieldByName("Err"); !ok || f.Type != reflect.TypeOf((*error)(nil)).Elem() {
		t.Error("StreamEvent.Err is missing or not an error")
	}
	for _, name := range []string{"System", "Messages", "Tools", "ToolChoice", "MaxTokens", "Temperature", "TopP", "StopSequences", "Effort"} {
		if !hasField(Request{}, name) {
			t.Errorf("Request lacks %s", name)
		}
	}
	for _, gone := range []string{"Options", "Deferred", "EstContextTokens"} {
		if hasField(Request{}, gone) {
			t.Errorf("Request still has %s", gone)
		}
	}
}

// TS-12-8: the registry, the stream options and their helpers are gone.
func TestProviderRegistryIsGone_TS12_8(t *testing.T) {
	types, methods := coreDecls(t)
	for _, gone := range []string{"ProviderRegistry", "ProviderStreamOptions", "ClientFunc", "StreamFunc", "APIProvider", "func Complete", "RequestOptions", "DeferredHandle", "DeferredRequest", "DeferredFunc"} {
		if types[gone] {
			t.Errorf("core still declares %s", gone)
		}
	}
	if len(methods["Dispatch"]) > 0 {
		t.Errorf("core still declares Dispatch on %v", methods["Dispatch"])
	}
}
