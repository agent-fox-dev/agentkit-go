package sandbox_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

type openCtxKey struct{}

// openProvider is a Provider that records the calls Open makes on it and on
// the one sandbox it hands out, in order, with what each call was given.
type openProvider struct {
	calls []string
	sb    *openSandbox

	createErr  error
	createCtx  context.Context
	createSpec sandbox.Spec
}

// openSandbox is the Sandbox openProvider hands out.
type openSandbox struct {
	p     *openProvider
	tools []core.Tool

	syncCtx   context.Context
	syncSrc   sandbox.Source
	toolsCtx  context.Context
	toolsOpts sandbox.ToolOptions
}

func newOpenProvider() *openProvider {
	p := &openProvider{}
	p.sb = &openSandbox{p: p, tools: []core.Tool{{Name: "read_file"}, {Name: "search_files"}}}
	return p
}

func (p *openProvider) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{Provider: "double"}
}

func (p *openProvider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	p.calls = append(p.calls, "Create")
	p.createCtx, p.createSpec = ctx, spec
	if p.createErr != nil {
		return nil, p.createErr
	}
	return p.sb, nil
}

func (p *openProvider) Reap(context.Context) ([]string, error) { return nil, nil }

func (s *openSandbox) ID() string               { return "double-1" }
func (s *openSandbox) Spec() sandbox.Spec       { return sandbox.Spec{} }
func (s *openSandbox) Agent() sandbox.AgentInfo { return sandbox.AgentInfo{} }

func (s *openSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	s.p.calls = append(s.p.calls, "Sync")
	s.syncCtx, s.syncSrc = ctx, src
	return sandbox.SyncResult{Files: 3, Bytes: 30}, nil
}

func (s *openSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	s.p.calls = append(s.p.calls, "Tools")
	s.toolsCtx, s.toolsOpts = ctx, opts
	return s.tools, nil
}

func (s *openSandbox) Export(context.Context, string, sandbox.Destination) error { return nil }

func (s *openSandbox) Close(context.Context) error {
	s.p.calls = append(s.p.calls, "Close")
	return nil
}

// TS-22-15: Open calls Create, Sync and Tools once each in order, returns the
// unwrapped sandbox and never closes it. 22-REQ-4.1, 22-REQ-4.3.
func TestOpenCallsInOrder_TS22_15(t *testing.T) {
	ctx := context.WithValue(context.Background(), openCtxKey{}, "v")
	p := newOpenProvider()
	spec := sandbox.Spec{Image: "img", Labels: map[string]string{"a": "b"}}
	src := sandbox.LocalDir{Path: "/host/project"}
	opts := sandbox.ToolOptions{SpillDir: "/abs"}

	sb, tools, err := sandbox.Open(ctx, p, spec, src, opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Create", "Sync", "Tools"}; !slices.Equal(p.calls, want) {
		t.Fatalf("calls = %v, want %v", p.calls, want)
	}
	if !reflect.DeepEqual(p.createSpec, spec) {
		t.Errorf("Create got %+v, want %+v", p.createSpec, spec)
	}
	if !reflect.DeepEqual(p.sb.syncSrc, sandbox.Source(src)) {
		t.Errorf("Sync got %+v, want %+v", p.sb.syncSrc, src)
	}
	if !reflect.DeepEqual(p.sb.toolsOpts, opts) {
		t.Errorf("Tools got %+v, want %+v", p.sb.toolsOpts, opts)
	}
	for name, got := range map[string]context.Context{"Create": p.createCtx, "Sync": p.sb.syncCtx, "Tools": p.sb.toolsCtx} {
		if got == nil || got.Value(openCtxKey{}) != "v" {
			t.Errorf("%s did not get the caller's context", name)
		}
	}
	if sb != sandbox.Sandbox(p.sb) {
		t.Errorf("Open returned %v, not the sandbox Create returned", sb)
	}
	if len(tools) != len(p.sb.tools) || &tools[0] != &p.sb.tools[0] {
		t.Errorf("Open returned %v, not the slice Tools returned", tools)
	}
}

// TS-22-16: Open with a nil src skips Sync. 22-REQ-4.2.
func TestOpenWithNoSource_TS22_16(t *testing.T) {
	p := newOpenProvider()
	sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{}, nil, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Create", "Tools"}; !slices.Equal(p.calls, want) {
		t.Fatalf("calls = %v, want %v", p.calls, want)
	}
	if sb != sandbox.Sandbox(p.sb) || len(tools) != 2 {
		t.Errorf("Open returned %v and %d tools", sb, len(tools))
	}
}

// TS-22-18: Open with a nil Provider returns a sandbox-prefixed error without
// panicking. 22-REQ-5.1.
func TestOpenWithNilProvider_TS22_18(t *testing.T) {
	sb, tools, err := sandbox.Open(context.Background(), nil, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
	if sb != nil || tools != nil {
		t.Errorf("Open returned %v and %v, want nil and nil", sb, tools)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "sandbox: ") {
		t.Errorf("error = %v, want one beginning %q", err, "sandbox: ")
	}
}

// TS-22-19: Open returns Create's error unchanged and calls no Sandbox
// method. 22-REQ-5.2.
func TestOpenReturnsCreateError_TS22_19(t *testing.T) {
	p := newOpenProvider()
	p.createErr = errors.New("create refused")
	sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
	if sb != nil || tools != nil {
		t.Errorf("Open returned %v and %v, want nil and nil", sb, tools)
	}
	if err != p.createErr {
		t.Errorf("error = %v, want Create's own error", err)
	}
	if want := []string{"Create"}; !slices.Equal(p.calls, want) {
		t.Errorf("calls = %v, want only %v: nothing to sync, equip or close", p.calls, want)
	}
}
