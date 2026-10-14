package gatewayd_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// repo is the GitRepo the slot tests sync.
var repo = sandbox.GitRepo{URL: "https://example.test/r.git", Ref: "main", Depth: 1}

// unrestrictedSpec is a spec local accepts.
var unrestrictedSpec = sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}

// tarOf is a tar stream of files.
func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// toolOf is sb's proxy tool named name.
func toolOf(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range tl {
		if x.Name == name {
			return x
		}
	}
	t.Fatalf("no proxy tool %s", name)
	return core.Tool{}
}

// create creates a sandbox through p, closed at the end of the test.
func create(t *testing.T, p *gateway.Provider, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	sb, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// isSlotRefusal reports whether err is the gateway's slot refusal.
func isSlotRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "GitRepo must be the first Sync and come before Tools") &&
		!errors.Is(err, sandbox.ErrUnsupported) && !errors.Is(err, sandbox.ErrUnavailable)
}

// TS-19-50: each slot-taking action makes a later SyncGit refused, and the
// actions that take nothing leave the slot free. 19-REQ-7.5.
func TestSlotTaking_TS19_50(t *testing.T) {
	be := gwtest.NewSlot(nil, 50)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ag := sandboxpb.NewAgentClient(env.Raw(t))
	ctx := context.Background()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, first := range map[string]func(sb sandbox.Sandbox) error{
		"git": func(sb sandbox.Sandbox) error { _, err := sb.Sync(ctx, repo); return err },
		"tools": func(sb sandbox.Sandbox) error {
			_, err := sb.Tools(ctx, sandbox.ToolOptions{})
			return err
		},
		"refused tools": func(sb sandbox.Sandbox) error {
			_, err := ag.Tools(gwtest.Ctx(ctx, gwtest.Token, gateway.SandboxIDKey, sb.ID()), &sandboxpb.ToolsRequest{SpillDir: "relative"})
			if status.Code(err) != codes.InvalidArgument {
				return fmt.Errorf("the agent did not refuse Tools: %v", err)
			}
			return nil
		},
		"tar": func(sb sandbox.Sandbox) error {
			_, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, map[string]string{"a": "1"}))})
			return err
		},
		"localdir": func(sb sandbox.Sandbox) error { _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src}); return err },
	} {
		sb := create(t, p, unrestrictedSpec)
		if err := first(sb); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		n := len(be.Clones(sb.ID()))
		if _, err := sb.Sync(ctx, repo); !isSlotRefusal(err) {
			t.Errorf("after %s, GitRepo Sync = %v, want the slot refusal", name, err)
		}
		if got := len(be.Clones(sb.ID())); got != n {
			t.Errorf("after %s, the refused GitRepo ran the clone", name)
		}
	}
	for name, first := range map[string]func(sb sandbox.Sandbox) error{
		"export": func(sb sandbox.Sandbox) error {
			return sb.Export(ctx, ".", sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "out")})
		},
		"unsafe tar": func(sb sandbox.Sandbox) error {
			_, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, map[string]string{"../escape": "x"}))})
			if !errors.Is(err, sandbox.ErrUnsafeArchive) {
				return fmt.Errorf("an unsafe tar gave %v, want ErrUnsafeArchive", err)
			}
			return nil
		},
	} {
		sb := create(t, p, unrestrictedSpec)
		if err := first(sb); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := sb.Sync(ctx, repo); err != nil {
			t.Errorf("after %s, GitRepo Sync = %v, want success", name, err)
		}
	}
}

// TS-19-51: SyncGit on a free slot calls the backend's Sync, maps
// ErrUnsupported and other errors, and takes the slot only on success.
// 19-REQ-7.6.
func TestSyncGit_TS19_51(t *testing.T) {
	be := gwtest.NewSlot(nil, 51)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()

	sb1 := create(t, p, unrestrictedSpec)
	if _, err := sb1.Sync(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if c := be.Clones(sb1.ID()); len(c) != 1 || c[0].Repo != repo {
		t.Errorf("the backend cloned %+v, want %+v once", c, repo)
	}
	if _, err := sb1.Tools(ctx, sandbox.ToolOptions{}); err != nil {
		t.Errorf("Tools after the clone: %v", err)
	}

	be.SetCloneErr(fmt.Errorf("%w: no git", sandbox.ErrUnsupported))
	sb2 := create(t, p, unrestrictedSpec)
	if _, err := sb2.Sync(ctx, repo); !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("an unsupported clone gave %v, want ErrUnsupported", err)
	}

	be.SetCloneErr(errors.New("clone failed: 128"))
	sb3 := create(t, p, unrestrictedSpec)
	_, err := sb3.Sync(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "clone failed: 128") || errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("a failed clone gave %v, want a refusal carrying its message", err)
	}
	be.SetCloneErr(nil)
	if _, err := sb3.Sync(ctx, repo); err != nil {
		t.Errorf("the retry after a failed clone: %v; the slot should have stayed free", err)
	}

	lenv := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	lsb := create(t, lenv.Dial(t, gwtest.Token), unrestrictedSpec)
	if _, err := lsb.Sync(ctx, repo); !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("a GitRepo on plain local gave %v, want ErrUnsupported", err)
	}
}

// TS-19-52: concurrent SyncGit and slot-taking relayed RPCs on one sandbox
// always produce the outcome of some sequential order. 19-REQ-7.7.
func TestSlotSerialised_TS19_52(t *testing.T) {
	agents := gwtest.NewAgents(nil)
	be := gwtest.NewSlot(agents, 52)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	for seed := range uint64(101) {
		r := rand.New(rand.NewPCG(seed, 52))
		sb := create(t, p, unrestrictedSpec)
		var wg sync.WaitGroup
		var gitErr, toolsErr, tarErr error
		run := func(dst *error, fn func() error) {
			delay := time.Duration(r.IntN(10)) * time.Millisecond
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(delay)
				*dst = fn()
			}()
		}
		run(&gitErr, func() error { _, err := sb.Sync(ctx, repo); return err })
		run(&toolsErr, func() error { _, err := sb.Tools(ctx, sandbox.ToolOptions{}); return err })
		if seed%2 == 1 {
			run(&tarErr, func() error { _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)}); return err })
		}
		wg.Wait()
		if toolsErr != nil || tarErr != nil {
			t.Fatalf("seed %d: Tools %v, Tar %v", seed, toolsErr, tarErr)
		}
		rec := agents.ByID(sb.ID()).Rec
		clones := be.Clones(sb.ID())
		if gitErr == nil {
			if len(clones) != 1 {
				t.Fatalf("seed %d: SyncGit succeeded with %d clones", seed, len(clones))
			}
			for _, at := range append(rec.ToolsAt(), rec.SyncTarAt()...) {
				if at.Before(clones[0].End) {
					t.Fatalf("seed %d: a slot-taking RPC reached the agent at %v, before the clone ended at %v", seed, at, clones[0].End)
				}
			}
		} else if !isSlotRefusal(gitErr) || len(clones) != 0 {
			t.Fatalf("seed %d: SyncGit %v with %d clones, want the slot refusal and none", seed, gitErr, len(clones))
		}
	}
}
