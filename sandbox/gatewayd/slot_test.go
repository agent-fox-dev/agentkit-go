package gatewayd_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// isSlotRefusal reports whether err is the refusal of a GitRepo that is not the first Sync.
func isSlotRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "GitRepo must be the first Sync and come before Tools") &&
		!errors.Is(err, sandbox.ErrUnsupported) && !errors.Is(err, sandbox.ErrUnavailable)
}

// A GitRepo, the first Tools (refused or not), a Tar or a LocalDir Sync takes the slot; an Export or a refused Tar does not.
func TestTheGitRepoSlotIsTakenByTheFirstSyncOrTools(t *testing.T) {
	be := gwtest.NewSlot(nil, 50)
	env, _ := start(t, gatewayd.Config{Backend: be})
	p, ag, ctx := env.Dial(t, gwtest.Token), sandboxpb.NewAgentClient(env.Raw(t)), context.Background()
	tarOf := func(name string) sandbox.Tar {
		return sandbox.Tar{R: bytes.NewReader(testutil.Tar(t, map[string]string{name: "x"}))}
	}
	syncWith := func(src sandbox.Source) func(sandbox.Sandbox) error {
		return func(sb sandbox.Sandbox) error { _, err := sb.Sync(ctx, src); return err }
	}
	for _, c := range []struct {
		name  string
		first func(sb sandbox.Sandbox) error
		takes bool
	}{
		{"git", syncWith(repo), true},
		{"tools", func(sb sandbox.Sandbox) error { _, err := sb.Tools(ctx, sandbox.ToolOptions{}); return err }, true},
		{"refused tools", func(sb sandbox.Sandbox) error {
			_, err := ag.Tools(authed(gwtest.Token, gateway.SandboxIDKey, sb.ID()), &sandboxpb.ToolsRequest{SpillDir: "relative"})
			if status.Code(err) != codes.InvalidArgument {
				return fmt.Errorf("the agent did not refuse Tools: %v", err)
			}
			return nil
		}, true},
		{"tar", syncWith(tarOf("a")), true},
		{"localdir", syncWith(sandbox.LocalDir{Path: testutil.Tree(t, map[string]string{"f.txt": "hi"})}), true},
		{"export", func(sb sandbox.Sandbox) error {
			return sb.Export(ctx, ".", sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "out")})
		}, false},
		{"unsafe tar", func(sb sandbox.Sandbox) error {
			if err := syncWith(tarOf("../escape"))(sb); !errors.Is(err, sandbox.ErrUnsafeArchive) {
				return fmt.Errorf("an unsafe tar gave %v, want ErrUnsafeArchive", err)
			}
			return nil
		}, false},
	} {
		sb := create(t, p, unrestrictedSpec)
		if err := c.first(sb); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		n := len(be.Clones(sb.ID()))
		err := syncWith(repo)(sb)
		if c.takes && (!isSlotRefusal(err) || len(be.Clones(sb.ID())) != n) || !c.takes && err != nil {
			t.Errorf("after %s, a GitRepo Sync = %v with %d clones more", c.name, err, len(be.Clones(sb.ID()))-n)
		}
	}
}

// ErrUnsupported is refused UNSUPPORTED; another failure carries its message and leaves the slot free.
func TestSyncGitClonesWithTheBackendOnAFreeSlot(t *testing.T) {
	be := gwtest.NewSlot(nil, 51)
	env, _ := start(t, gatewayd.Config{Backend: be})
	p, ctx := env.Dial(t, gwtest.Token), context.Background()
	sb := create(t, p, unrestrictedSpec)
	if _, err := sb.Sync(ctx, repo); err != nil || len(be.Clones(sb.ID())) != 1 || be.Clones(sb.ID())[0].Repo != repo {
		t.Errorf("GitRepo Sync = %v; the backend cloned %+v, want %+v once", err, be.Clones(sb.ID()), repo)
	}
	be.SetCloneErr(fmt.Errorf("%w: no git", sandbox.ErrUnsupported))
	if _, err := create(t, p, unrestrictedSpec).Sync(ctx, repo); !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("an unsupported clone gave %v, want ErrUnsupported", err)
	}
	be.SetCloneErr(errors.New("clone failed: 128"))
	sb = create(t, p, unrestrictedSpec)
	if _, err := sb.Sync(ctx, repo); err == nil || !strings.Contains(err.Error(), "clone failed: 128") || errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("a failed clone gave %v, want a refusal carrying its message", err)
	}
	be.SetCloneErr(nil)
	if _, err := sb.Sync(ctx, repo); err != nil {
		t.Errorf("the retry after a failed clone: %v; the slot should have stayed free", err)
	}
	lenv, _ := start(t, gatewayd.Config{Backend: local.Unisolated()})
	if _, err := create(t, lenv.Dial(t, gwtest.Token), unrestrictedSpec).Sync(ctx, repo); !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("a GitRepo on plain local gave %v, want ErrUnsupported", err)
	}
}

// Either the clone ran first and no slot-taking RPC reached the agent before it ended, or SyncGit was refused without a clone.
func TestSyncGitAndTheSlotTakingRelaysNeverInterleave(t *testing.T) {
	agents := gwtest.NewAgents(nil)
	be := gwtest.NewSlot(agents, 52)
	env, _ := start(t, gatewayd.Config{Backend: be})
	p, ctx := env.Dial(t, gwtest.Token), context.Background()
	for seed := range uint64(24) {
		r := rand.New(rand.NewPCG(seed, 52))
		sb := create(t, p, unrestrictedSpec)
		var wg sync.WaitGroup
		errs := make([]error, 3)
		for i, fn := range []func() error{
			func() error { _, err := sb.Sync(ctx, repo); return err },
			func() error { _, err := sb.Tools(ctx, sandbox.ToolOptions{}); return err },
			func() error { _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)}); return err },
		} {
			delay := time.Duration(r.IntN(10)) * time.Millisecond // a random start order, not synchronisation
			wg.Go(func() { time.Sleep(delay); errs[i] = fn() })
		}
		wg.Wait()
		rec, clones := agents.ByID(sb.ID()).Rec, be.Clones(sb.ID())
		switch {
		case errs[1] != nil || errs[2] != nil:
			t.Fatalf("seed %d: Tools %v, Tar %v", seed, errs[1], errs[2])
		case errs[0] == nil && len(clones) == 1:
			for _, at := range append(rec.ToolsAt(), rec.SyncTarAt()...) {
				if at.Before(clones[0].End) {
					t.Fatalf("seed %d: a slot-taking RPC reached the agent at %v, before the clone ended at %v", seed, at, clones[0].End)
				}
			}
		case !isSlotRefusal(errs[0]) || len(clones) != 0:
			t.Fatalf("seed %d: SyncGit %v with %d clones", seed, errs[0], len(clones))
		}
		_ = sb.Close(ctx)
	}
}
