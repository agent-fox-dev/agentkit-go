package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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

	syncErr, toolsErr, closeErr error

	syncCtx   context.Context
	syncSrc   sandbox.Source
	toolsCtx  context.Context
	toolsOpts sandbox.ToolOptions
	// closeCtxErr and closeCtxVal are what Close saw on its context when it
	// was called, since a context can be cancelled after the call returns.
	closeCtxErr error
	closeCtxVal any
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
	if s.syncErr != nil {
		return sandbox.SyncResult{}, s.syncErr
	}
	return sandbox.SyncResult{Files: 3, Bytes: 30}, nil
}

func (s *openSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	s.p.calls = append(s.p.calls, "Tools")
	s.toolsCtx, s.toolsOpts = ctx, opts
	if s.toolsErr != nil {
		return nil, s.toolsErr
	}
	return s.tools, nil
}

func (s *openSandbox) Export(context.Context, string, sandbox.Destination) error { return nil }

func (s *openSandbox) Close(ctx context.Context) error {
	s.p.calls = append(s.p.calls, "Close")
	s.closeCtxErr, s.closeCtxVal = ctx.Err(), ctx.Value(openCtxKey{})
	return s.closeErr
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

// openFailure fails the step ("Sync" or "Tools") of p's sandbox with err.
func openFailure(p *openProvider, step string, err error) {
	if step == "Sync" {
		p.sb.syncErr = err
	} else {
		p.sb.toolsErr = err
	}
}

// TS-22-20: a failing Sync or Tools closes the sandbox once and returns the
// step error unchanged. 22-REQ-5.3.
func TestOpenClosesOnAStepFailure_TS22_20(t *testing.T) {
	for _, step := range []string{"Sync", "Tools"} {
		p := newOpenProvider()
		stepErr := errors.New(step + " failed")
		openFailure(p, step, stepErr)
		sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
		if sb != nil || tools != nil {
			t.Errorf("%s: Open returned %v and %v, want nil and nil", step, sb, tools)
		}
		if err != stepErr {
			t.Errorf("%s: error = %v, want the step's own error", step, err)
		}
		want := []string{"Create", "Sync", "Close"}
		if step == "Tools" {
			want = []string{"Create", "Sync", "Tools", "Close"}
		}
		if !slices.Equal(p.calls, want) {
			t.Errorf("%s: calls = %v, want %v", step, p.calls, want)
		}
	}
}

// TS-22-21: a step failure plus a Close failure returns an error that matches
// both. 22-REQ-5.4.
func TestOpenJoinsTheCloseError_TS22_21(t *testing.T) {
	for _, step := range []string{"Sync", "Tools"} {
		p := newOpenProvider()
		stepErr, closeErr := errors.New(step+" failed"), errors.New("close failed")
		openFailure(p, step, stepErr)
		p.sb.closeErr = closeErr
		sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
		if sb != nil || tools != nil {
			t.Errorf("%s: Open returned %v and %v, want nil and nil", step, sb, tools)
		}
		if !errors.Is(err, stepErr) || !errors.Is(err, closeErr) {
			t.Errorf("%s: error = %v, want one matching the step error and the Close error", step, err)
		}
		if n := strings.Count(strings.Join(p.calls, ","), "Close"); n != 1 {
			t.Errorf("%s: Close was called %d times, want 1 (calls %v)", step, n, p.calls)
		}
	}
}

// TS-22-22: cleanup runs with an uncancelled context that carries the
// original values, even when ctx is already cancelled or expired. 22-REQ-5.5.
func TestOpenCleansUpOnADeadContext_TS22_22(t *testing.T) {
	dead := map[string]func() (context.Context, context.CancelFunc){
		"cancelled": func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), openCtxKey{}, "v"))
			cancel()
			return ctx, cancel
		},
		"expired": func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.WithValue(context.Background(), openCtxKey{}, "v"), time.Now().Add(-time.Second))
		},
	}
	for name, mk := range dead {
		ctx, cancel := mk()
		defer cancel()
		p := newOpenProvider()
		stepErr := errors.New("sync failed")
		p.sb.syncErr = stepErr
		_, _, err := sandbox.Open(ctx, p, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
		if !slices.Contains(p.calls, "Close") {
			t.Errorf("%s: the sandbox was not closed (calls %v)", name, p.calls)
		}
		if p.sb.closeCtxErr != nil {
			t.Errorf("%s: Close got a context that was already done: %v", name, p.sb.closeCtxErr)
		}
		if p.sb.closeCtxVal != "v" {
			t.Errorf("%s: Close got a context without the caller's values (%v)", name, p.sb.closeCtxVal)
		}
		if !errors.Is(err, stepErr) {
			t.Errorf("%s: error = %v, want the step error", name, err)
		}
	}
}

// TS-22-23 (property): Open's error matches every sentinel the failing step
// matches, with or without a Close failure. 22-REQ-5.6. The space is small,
// so it is enumerated: both steps, five sentinels, Close ok or failing.
func TestOpenKeepsTheStepSentinels_TS22_23(t *testing.T) {
	sentinels := []error{sandbox.ErrUnsupported, sandbox.ErrToolMismatch, sandbox.ErrUnsafeArchive, sandbox.ErrProtocol, sandbox.ErrUnavailable}
	for _, step := range []string{"Sync", "Tools"} {
		for _, x := range sentinels {
			for _, closeFails := range []bool{false, true} {
				p := newOpenProvider()
				openFailure(p, step, fmt.Errorf("while %s: %w", step, x))
				closeErr := errors.New("close failed")
				if closeFails {
					p.sb.closeErr = closeErr
				}
				sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
				if sb != nil || tools != nil {
					t.Errorf("%s/%v/close fails %v: Open returned %v and %v", step, x, closeFails, sb, tools)
				}
				if !errors.Is(err, x) {
					t.Errorf("%s/%v/close fails %v: error %v does not match the sentinel", step, x, closeFails, err)
				}
				if closeFails && !errors.Is(err, closeErr) {
					t.Errorf("%s/%v: error %v does not match the Close error", step, x, err)
				}
			}
		}
	}
}
