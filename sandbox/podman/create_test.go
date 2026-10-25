package podman

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func TestCreateMakesTheVolumeThenRunsArgs(t *testing.T) {
	f := newFake(t)
	o := Options{Engine: EnginePodman}
	spec := sandbox.Spec{Image: "img:1", Labels: map[string]string{"team": "core"}, TTL: 10 * time.Minute}
	s := create(t, f, spec)
	id := s.ID()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) || s.Agent().ProtocolMajor != sandbox.ProtocolMajor {
		t.Fatalf("ID %q, protocol major %d", id, s.Agent().ProtocolMajor)
	}
	cmds := f.recorded()
	if len(cmds) < 2 || cmds[0].start || !cmds[1].start {
		t.Fatalf("commands %v: want volume create, then the engine process", cmds)
	}
	created := createdAt(t, cmds[0].argv, spec.TTL)
	eff := effective(t, o, spec)
	if want := append(append([]string{"volume", "create"}, labelArgs(id, created, eff)...), "agentkit-"+id); !slices.Equal(cmds[0].argv[1:], want) {
		t.Errorf("volume create %q, want %q", cmds[0].argv[1:], want)
	}
	if want := Args(o, id, created, eff); !slices.Equal(cmds[1].argv[1:], want) {
		t.Errorf("run %q,\nwant %q", cmds[1].argv[1:], want)
	}
}

// A build with no published image refuses an empty Spec.Image before any command.
func TestCreateRunsTheDefaultImageWhenTheImageIsEmpty(t *testing.T) {
	f := newFake(t)
	if _, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{}); !errors.Is(err, sandbox.ErrNoPublishedImage) ||
		!strings.Contains(err.Error(), "Spec.Image") || len(f.recorded()) != 0 {
		t.Fatalf("Create = %v after %d commands, want ErrNoPublishedImage and none", err, len(f.recorded()))
	}
	p := testProvider(f, Options{})
	const img = "quay.io/agentfox/agentkit-sandbox:1.2.3"
	p.defaultImage = func() (string, error) { return img, nil }
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if run := f.recorded()[1].argv; run[slices.Index(run, sandbox.AgentPath)+1] != img || s.Spec().Image != img {
		t.Fatalf("run %q, Spec().Image %q; want %s after the agent path and reported", run, s.Spec().Image, img)
	}
}

// The timer counts from the expiry label's creation time, however long Create took.
func TestAnUnclosedSandboxIsDestroyedAtCreatedPlusTTL(t *testing.T) {
	f := newFake(t)
	p := testProvider(f, Options{})
	var calls atomic.Int32
	p.now = func() time.Time { // Create "took" 1.5 s of a 2 s TTL
		if calls.Add(1) == 1 {
			return time.Now().Add(-1500 * time.Millisecond)
		}
		return time.Now()
	}
	s, err := p.Create(context.Background(), sandbox.Spec{Image: "img:1", TTL: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	id, ctx := s.ID(), context.Background()
	if !testutil.Within(1200*time.Millisecond, func() bool { return f.ran("volume", "rm", "agentkit-"+id) }) ||
		!f.ran("rm", "-f", "agentkit-"+id) || !f.ran("rm", "-f", "agentkit-"+id+"-init") || s.Spec().TTL != 2*time.Second {
		t.Fatalf("no destroy step at created + TTL (Spec().TTL %v): %v", s.Spec().TTL, f.recorded())
	}
	gone := func(err error) bool {
		return errors.Is(err, sandbox.ErrUnavailable) || errors.Is(err, sandbox.ErrClosed)
	}
	_, terr := s.Tools(ctx, sandbox.ToolOptions{})
	_, serr := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()})
	if eerr := s.Export(ctx, ".", sandbox.LocalDir{Path: t.TempDir()}); !gone(terr) || !gone(serr) || !gone(eerr) {
		t.Errorf("after the TTL: Tools %v, Sync %v, Export %v", terr, serr, eerr)
	}
}
