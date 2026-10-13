package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-15: Capabilities report the engine name, isolation, networks,
// resources, flags and plain Notes. 17-REQ-2.6, 17-REQ-2.7.
func TestCapabilities_TS17_15(t *testing.T) {
	for _, c := range []struct {
		o         Options
		provider  string
		isolation sandbox.Isolation
	}{
		{Options{Engine: EnginePodman}, "podman", sandbox.IsolationContainer},
		{Options{Engine: EngineDocker}, "docker", sandbox.IsolationContainer},
		{Options{Engine: EnginePodman, Runtime: "krun"}, "podman", sandbox.IsolationMicroVM},
	} {
		p := newResolved(c.o, "/usr/bin/"+string(c.o.Engine), c.o.Engine, &fakeRunner{})
		got := p.Capabilities()
		if got.Provider != c.provider || got.Isolation != c.isolation {
			t.Errorf("%+v: Provider %q, Isolation %v; want %q, %v", c.o, got.Provider, got.Isolation, c.provider, c.isolation)
		}
		if !slices.Equal(got.Network, []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}) {
			t.Errorf("Network = %v", got.Network)
		}
		if got.Resources != (sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true}) {
			t.Errorf("Resources = %+v", got.Resources)
		}
		if !got.Image || !got.Workdir || !got.Landlock {
			t.Errorf("Image %t, Workdir %t, Landlock %t; want all true", got.Image, got.Workdir, got.Landlock)
		}
		for _, kw := range []string{"allowlist", "DiskBytes", "default", "tmpfs", "MemoryBytes", "Spec.Env", "command line", "seccomp", "Landlock"} {
			if !strings.Contains(got.Notes, kw) {
				t.Errorf("Notes lack %q: %s", kw, got.Notes)
			}
		}
	}
}
