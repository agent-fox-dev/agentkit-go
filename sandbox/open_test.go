package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

type ctxKey struct{}

// recorder is a Provider and the one Sandbox it hands out, recording the
// calls Open makes, in order, with what each was given. Its other Sandbox
// methods come from the nil embedded interface and are never called.
type recorder struct {
	sandbox.Sandbox
	calls                      []string
	ctxs                       map[string]context.Context
	spec                       sandbox.Spec
	src                        sandbox.Source
	opts                       sandbox.ToolOptions
	tools                      []core.Tool
	createErr, syncErr         error
	toolsErr, closeErr         error
	closeCtxErr, closeCtxValue any
}

func newRecorder() *recorder {
	return &recorder{ctxs: map[string]context.Context{}, tools: []core.Tool{{Name: "read_file"}, {Name: "list_files"}}}
}

func (r *recorder) note(call string, ctx context.Context) {
	r.calls = append(r.calls, call)
	r.ctxs[call] = ctx
}

func (r *recorder) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{Provider: "recorder"}
}
func (r *recorder) Reap(context.Context) ([]string, error) { return nil, nil }

func (r *recorder) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	r.note("Create", ctx)
	r.spec = spec
	if r.createErr != nil {
		return nil, r.createErr
	}
	return r, nil
}

func (r *recorder) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	r.note("Sync", ctx)
	r.src = src
	return sandbox.SyncResult{Files: 1}, r.syncErr
}

func (r *recorder) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	r.note("Tools", ctx)
	r.opts = opts
	if r.toolsErr != nil {
		return nil, r.toolsErr
	}
	return r.tools, nil
}

func (r *recorder) Close(ctx context.Context) error {
	r.note("Close", ctx)
	r.closeCtxErr, r.closeCtxValue = ctx.Err(), ctx.Value(ctxKey{})
	return r.closeErr
}

func TestOpenCreatesSyncsAndEquipsInOrder(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	spec := sandbox.Spec{Image: "img", Labels: map[string]string{"a": "b"}}
	src, opts := sandbox.LocalDir{Path: "/host/project"}, sandbox.ToolOptions{SpillDir: "/abs"}
	for _, c := range []struct {
		src   sandbox.Source
		calls []string
	}{{src, []string{"Create", "Sync", "Tools"}}, {nil, []string{"Create", "Tools"}}} {
		r := newRecorder()
		sb, tools, err := sandbox.Open(ctx, r, spec, c.src, opts)
		if err != nil || !slices.Equal(r.calls, c.calls) {
			t.Fatalf("source %v: calls %v, %v; want %v", c.src, r.calls, err, c.calls)
		}
		if !reflect.DeepEqual(r.spec, spec) || !reflect.DeepEqual(r.src, c.src) || r.opts != opts {
			t.Errorf("source %v: the steps got %+v, %+v, %+v", c.src, r.spec, r.src, r.opts)
		}
		for call, got := range r.ctxs {
			if got.Value(ctxKey{}) != "v" {
				t.Errorf("%s did not get the caller's context", call)
			}
		}
		if sb != sandbox.Sandbox(r) || &tools[0] != &r.tools[0] {
			t.Errorf("Open returned %v and %v, not what Create and Tools returned", sb, tools)
		}
	}
}

func TestOpenRefusesANilProviderAndReturnsCreatesErrorUnchanged(t *testing.T) {
	sb, tools, err := sandbox.Open(context.Background(), nil, sandbox.Spec{}, nil, sandbox.ToolOptions{})
	if sb != nil || tools != nil || err == nil || !strings.HasPrefix(err.Error(), "sandbox: ") {
		t.Errorf("a nil Provider: %v, %v, %v; want a sandbox error", sb, tools, err)
	}
	r := newRecorder()
	r.createErr = errors.New("create refused")
	sb, tools, err = sandbox.Open(context.Background(), r, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
	if sb != nil || tools != nil || err != r.createErr || !slices.Equal(r.calls, []string{"Create"}) {
		t.Errorf("a failing Create: %v, %v, %v after %v; want Create's error alone", sb, tools, err, r.calls)
	}
}

func TestOpenClosesTheSandboxOnceWhenAStepFails(t *testing.T) {
	for _, step := range []string{"Sync", "Tools"} {
		for _, closeFails := range []bool{false, true} {
			r := newRecorder()
			stepErr := fmt.Errorf("while %s: %w", step, sandbox.ErrToolMismatch)
			if step == "Sync" {
				r.syncErr = stepErr
			} else {
				r.toolsErr = stepErr
			}
			if closeFails {
				r.closeErr = errors.New("close failed")
			}
			name := fmt.Sprintf("%s fails, Close fails %v", step, closeFails)
			sb, tools, err := sandbox.Open(context.Background(), r, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
			if sb != nil || tools != nil || !errors.Is(err, sandbox.ErrToolMismatch) {
				t.Errorf("%s: %v, %v, %v; want nil, nil and the step's error", name, sb, tools, err)
			}
			if closeFails != (err != stepErr) || (closeFails && !errors.Is(err, r.closeErr)) {
				t.Errorf("%s: %v, want the step's error unchanged, or joined with Close's", name, err)
			}
			want := []string{"Create", "Sync", "Close"}
			if step == "Tools" {
				want = []string{"Create", "Sync", "Tools", "Close"}
			}
			if !slices.Equal(r.calls, want) {
				t.Errorf("%s: calls %v, want %v", name, r.calls, want)
			}
		}
	}
}

func TestOpenCleansUpWithTheCallersValuesButNotItsCancellation(t *testing.T) {
	base := context.WithValue(context.Background(), ctxKey{}, "v")
	cancelled, cancel := context.WithCancel(base)
	cancel()
	expired, cancel2 := context.WithDeadline(base, time.Now().Add(-time.Second))
	defer cancel2()
	for name, ctx := range map[string]context.Context{"cancelled": cancelled, "expired": expired} {
		r := newRecorder()
		r.syncErr = ctx.Err()
		_, _, err := sandbox.Open(ctx, r, sandbox.Spec{}, sandbox.LocalDir{Path: "/x"}, sandbox.ToolOptions{})
		if !errors.Is(err, ctx.Err()) || !slices.Contains(r.calls, "Close") {
			t.Errorf("%s: %v after %v; want the step's error and a Close", name, err, r.calls)
		}
		if r.closeCtxErr != nil || r.closeCtxValue != "v" {
			t.Errorf("%s: Close got a context done with %v carrying %v", name, r.closeCtxErr, r.closeCtxValue)
		}
	}
}

// madeDir wraps local and records the sandbox directory Create made, so the
// test sees a cleanup rather than a sandbox that was never made.
type madeDir struct {
	sandbox.Provider
	dir     string
	existed bool
}

func (m *madeDir) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := m.Provider.Create(ctx, spec)
	if err == nil {
		m.dir = filepath.Join(os.TempDir(), "agentkit-sandbox-local", sb.ID())
		m.existed = lexists(m.dir)
	}
	return sb, err
}

// Through the real local provider and agent: local refuses a GitRepo Sync,
// and Open's cleanup removes the sandbox it had made.
func TestOpenOnLocalLeavesNothingBehindWhenSyncIsRefused(t *testing.T) {
	isolate(t)
	p := &madeDir{Provider: local.Unisolated()}
	sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{Network: unrestricted},
		sandbox.GitRepo{URL: "https://example.invalid/repo.git"}, sandbox.ToolOptions{})
	if sb != nil || tools != nil || !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("Open = %v, %v, %v; want nil, nil and ErrUnsupported", sb, tools, err)
	}
	if !p.existed || lexists(p.dir) {
		t.Errorf("the sandbox directory existed after Create %v, exists after Open %v; want true, false", p.existed, lexists(p.dir))
	}
}
