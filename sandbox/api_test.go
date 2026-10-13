package sandbox_test

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// TS-13-8: the sandbox package exports the specified host API with the
// specified field names, constants, sealed interfaces and errors.
// 13-REQ-2.1.
func TestHostAPI_TS13_8(t *testing.T) {
	if sandbox.AgentPath != "/usr/local/libexec/agentkit-sandboxd" {
		t.Errorf("AgentPath = %q", sandbox.AgentPath)
	}
	_ = sandbox.Spec{
		Image: "", Workdir: "", Env: map[string]string{},
		Resources: sandbox.Resources{CPUs: 0, MemoryBytes: 0, PIDs: 0, DiskBytes: 0},
		Network:   sandbox.Network{Mode: sandbox.NetworkNone, Allow: []sandbox.Endpoint{{Host: "h", Port: 1}}},
		TTL:       time.Duration(0), Labels: map[string]string(nil), Provider: map[string]any{},
	}
	_ = sandbox.Capabilities{
		Provider: "p", Isolation: sandbox.IsolationNone, Network: []sandbox.NetworkMode{sandbox.NetworkNone},
		Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
		Image:     true, Workdir: true, Notes: "",
	}
	_ = sandbox.AgentInfo{Version: "", ProtocolMajor: uint32(1), ProtocolMinor: uint32(0), Workdir: "", OutlineBackend: "", Git: false}
	_ = sandbox.ToolOptions{Symbols: tools.SymbolOptions{}, SpillDir: "", DisableSpill: false}
	_ = sandbox.SyncResult{Files: int(0), Bytes: int64(0)}
	var _ sandbox.Source = sandbox.Tar{R: io.Reader(nil), Path: "", Replace: false}
	var _ sandbox.Destination = sandbox.TarOut{W: io.Writer(nil)}
	var _ sandbox.Provider
	var _ sandbox.Sandbox

	if sandbox.NetworkMode(0) != sandbox.NetworkNone {
		t.Error("NetworkNone is not the zero NetworkMode")
	}
	if sandbox.NetworkNone == sandbox.NetworkAllowlist || sandbox.NetworkAllowlist == sandbox.NetworkUnrestricted || sandbox.NetworkNone == sandbox.NetworkUnrestricted {
		t.Error("network modes are not distinct")
	}
	iso := map[sandbox.Isolation]bool{sandbox.IsolationNone: true, sandbox.IsolationConfinement: true, sandbox.IsolationContainer: true, sandbox.IsolationMicroVM: true}
	if len(iso) != 4 {
		t.Error("isolation levels are not distinct")
	}

	for _, it := range []reflect.Type{reflect.TypeFor[sandbox.Source](), reflect.TypeFor[sandbox.Destination]()} {
		sealed := false
		for i := range it.NumMethod() {
			if !it.Method(i).IsExported() {
				sealed = true
			}
		}
		if !sealed {
			t.Errorf("%s has no unexported method: it is not sealed", it)
		}
	}

	errs := []error{sandbox.ErrUnsupported, sandbox.ErrProtocol, sandbox.ErrUnavailable, sandbox.ErrClosed,
		sandbox.ErrToolMismatch, sandbox.ErrToolsConfigured, sandbox.ErrUnsafeArchive}
	for i, a := range errs {
		if a == nil {
			t.Fatalf("error %d is nil", i)
		}
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
	if sandbox.ProtocolMajor != 1 || sandbox.ProtocolMinor != 1 || sandbox.MaxMessageBytes != 16<<20 {
		t.Errorf("protocol %d.%d, max %d", sandbox.ProtocolMajor, sandbox.ProtocolMinor, sandbox.MaxMessageBytes)
	}
}
