package podman

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

const testID = "0123456789abcdef"

// testCreated has a zone and nanoseconds, which the expiry label drops.
var testCreated = time.Date(2025, 1, 2, 3, 4, 5, 999, time.FixedZone("+01", 3600))

// effective is CheckSpec of spec against the provider's capabilities under o.
func effective(t *testing.T, o Options, spec sandbox.Spec) sandbox.Spec {
	t.Helper()
	eff, err := sandbox.CheckSpec(newResolved(o, "/usr/bin/podman", o.Engine, nil).Capabilities(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}

// golden compares args, one per line, with testdata/name.golden.
func golden(t *testing.T, name string, args []string) {
	t.Helper()
	testutil.Golden(t, "testdata/"+name+".golden", []byte(strings.Join(args, "\n")+"\n"))
}

// assertNoForbidden fails t on a host mount, privilege, seccomp override, valueless --env or host path in a.
func assertNoForbidden(t *testing.T, a []string) {
	t.Helper()
	wd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	for i, e := range a {
		next := ""
		if i+1 < len(a) {
			next = a[i+1]
		}
		if e == "-v" || strings.HasPrefix(e, "--volume") || strings.Contains(e, "type=bind") || e == "--privileged" ||
			strings.HasPrefix(e, "--cap-add") || strings.Contains(e, "seccomp") || e == "--env" && !strings.Contains(next, "=") ||
			strings.Contains(e, wd) || home != "/" && strings.Contains(e, home) {
			t.Errorf("element %d of %q is forbidden: %q", i, a, e)
		}
	}
}

// The init goldens show no -i, no --network none and no Spec.Env value (SECRET).
func TestArgsAndInitArgsMatchTheirGoldens(t *testing.T) {
	podman, docker := Options{Engine: EnginePodman}, Options{Engine: EngineDocker}
	full := Options{Engine: EnginePodman, Runtime: "krun", UserNS: "keep-id"}
	repo := sandbox.GitRepo{URL: "https://github.com/agent-fox-dev/agentkit-go", Ref: "main", Depth: 1}
	initSpec := sandbox.Spec{Image: "img:1", Labels: map[string]string{"team": "core"}, Env: map[string]string{"SECRET": "s3cr3t"},
		Resources: sandbox.Resources{CPUs: 2, MemoryBytes: 1073741824, PIDs: 128}}
	for name, c := range map[string]struct {
		o    Options
		spec sandbox.Spec
	}{
		"args_podman":       {podman, sandbox.Spec{Image: "img:1"}},
		"args_docker":       {docker, sandbox.Spec{Image: "img:1"}},
		"args_unrestricted": {podman, sandbox.Spec{Image: "img:1", Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}},
		"args_labels_env": {podman, sandbox.Spec{Image: "img:1", Labels: map[string]string{"zone": "b", "app": "a", "mid": "c"},
			Env: map[string]string{"HOME": "/workspace/.home", "X": "1", "A_B": "two words"}}},
		"args_krun_resources": {full, sandbox.Spec{Image: "img:1", Resources: sandbox.Resources{CPUs: 1.5, MemoryBytes: 268435456, PIDs: 64}}},
		"init_args_podman":    {full, initSpec},
		"init_args_docker":    {Options{Engine: EngineDocker, UserNS: "keep-id"}, initSpec},
	} {
		eff := effective(t, c.o, c.spec)
		build := func() []string { return Args(c.o, testID, testCreated, eff) }
		if strings.HasPrefix(name, "init") {
			build = func() []string { return InitArgs(c.o, testID, testCreated, eff, repo) }
		}
		if a := build(); !slices.Equal(a, build()) {
			t.Errorf("%s is not pure", name)
		} else {
			golden(t, name, a)
			assertNoForbidden(t, a)
		}
	}
}
