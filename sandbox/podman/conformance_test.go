package podman

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// The host-side TTL counts from the creation time the expiry label is
// written from, so the sandbox never outlives the label Reap reads: a slow
// Create does not extend it. Spec().TTL still reports the effective TTL.
// 17-REQ-6.6, 17-REQ-6.7, 17-REQ-8.3.
func TestHostTTLFromCreated(t *testing.T) {
	f := newFake(t)
	p := testProvider(f, Options{})
	// Create "took" 1.5 s of a 2 s TTL: created is read 1.5 s in the past,
	// every later reading is the real time.
	var calls atomic.Int32
	p.now = func() time.Time {
		if calls.Add(1) == 1 {
			return time.Now().Add(-1500 * time.Millisecond)
		}
		return time.Now()
	}
	s, err := p.Create(context.Background(), sandbox.Spec{Image: "img:1", TTL: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if s.Spec().TTL != 2*time.Second {
		t.Errorf("Spec().TTL = %v, want the effective 2s", s.Spec().TTL)
	}
	deadline := time.Now().Add(1200 * time.Millisecond)
	for !f.ran("volume", "rm", "agentkit-"+s.ID()) {
		if time.Now().After(deadline) {
			t.Fatal("the host-side TTL did not fire at created + TTL")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Engine output carries stderr too (a docker shim's banner, podman's WARN
// lines); detection and Reap find their line among it. 17-REQ-2.3, 17-REQ-8.1.
func TestEngineOutputWithStderrNoise(t *testing.T) {
	for out, want := range map[string]Engine{
		"Emulate Docker CLI using podman. Create /etc/containers/nodocker to quiet msg.\npodman version 5.2.1\n": EnginePodman,
		"WARN[0000] some warning\nDocker version 24.0.7, build afdd53b\n":                                        EngineDocker,
	} {
		if got, err := parseVersion(out); got != want || err != nil {
			t.Errorf("parseVersion(%q) = %q, %v; want %q", out, got, err, want)
		}
	}
	if _, err := parseVersion("WARN[0000] x\nnerdctl version 1.7.0"); err == nil {
		t.Error("parseVersion accepted output with no version line")
	}

	f := newFake(t)
	inner := listingAnswer(nil, []engineEntry{{name: "agentkit-aaaa", labels: labels("aaaa", rfc(reapT.Add(-time.Hour)))}})
	f.answer = func(ctx context.Context, argv []string) (tools.ExecResult, error) {
		res, err := inner(ctx, argv)
		res.Output = "WARN[0000] rootless: cgroup v1\n" + res.Output
		return res, err
	}
	p := testProvider(f, Options{})
	p.now = func() time.Time { return reapT }
	ids, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{"aaaa"}) {
		t.Errorf("Reap with WARN lines = %v, %v; want [aaaa]", ids, err)
	}
}

// A GitRepo Sync whose sandbox is closed while the init step runs removes
// the volume the init container may have re-created, and reports the
// closed sandbox. 17-REQ-5.8, 17-REQ-6.5, 17-REQ-7.2.
func TestGitRepoRacingClose(t *testing.T) {
	f := withGit(t, newFake(t))
	var s sandbox.Sandbox
	f.answer = initAnswer(func(context.Context) (tools.ExecResult, error) {
		_ = s.Close(context.Background())
		return exitResult(0, ""), nil
	})
	s = create(t, f, sandbox.Spec{})
	_, err := s.Sync(context.Background(), testRepo)
	if !errors.Is(err, sandbox.ErrClosed) {
		t.Errorf("Sync = %v, want ErrClosed", err)
	}
	_, i := initCmd(f)
	last := -1
	for j, c := range f.recorded() {
		if hasArgs(c.argv, "volume", "rm", "agentkit-"+s.ID()) {
			last = j
		}
	}
	if f.count("volume", "rm", "agentkit-"+s.ID()) < 2 || last <= i {
		t.Errorf("no volume rm after the init step: %v", f.recorded())
	}
}
