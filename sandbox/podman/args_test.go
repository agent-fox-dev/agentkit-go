package podman

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

const (
	testID      = "0123456789abcdef"
	defaultPath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// testCreated is TS-17-24's creation time: 03:04:05.000000999 at +01:00.
var testCreated = time.Date(2025, 1, 2, 3, 4, 5, 999, time.FixedZone("+01", 3600))

// effective is CheckSpec of spec against the podman capabilities.
func effective(t *testing.T, spec sandbox.Spec) sandbox.Spec {
	t.Helper()
	eff, err := sandbox.CheckSpec(newResolved(Options{Engine: EnginePodman}, "/usr/bin/podman", EnginePodman, &fakeRunner{}).Capabilities(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}

// goldenPodmanZero is Args for the zero effective spec with Image img:1.
var goldenPodmanZero = []string{
	"run", "--rm", "-i", "--init",
	"--name", "agentkit-0123456789abcdef",
	"--label", "agentkit.sandbox=0123456789abcdef",
	"--label", "agentkit.sandbox.expires=2025-01-02T03:04:05Z",
	"--log-driver", "none",
	"--network", "none",
	"--cap-drop", "ALL",
	"--security-opt", "no-new-privileges",
	"--read-only",
	"--tmpfs", "/tmp:rw,exec,nosuid,nodev,mode=1777",
	"--mount", "type=volume,source=agentkit-0123456789abcdef,target=/workspace",
	"--unsetenv-all",
	"--env", "HOME=/tmp", "--env", "LANG=C.UTF-8", "--env", defaultPath, "--env", "TERM=dumb", "--env", "TMPDIR=/tmp",
	"--entrypoint", "/usr/local/libexec/agentkit-sandboxd",
	"img:1",
	"--ttl", "1h0m0s", "--network", "none", "--workdir", "/workspace",
}

// assertNoForbidden fails t if a contains a flag Args and InitArgs must
// never emit, or names the test's working or home directory.
func assertNoForbidden(t *testing.T, a []string) {
	t.Helper()
	wd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	for i, e := range a {
		switch {
		case e == "-v" || e == "--volume" || strings.HasPrefix(e, "--volume=") || strings.HasPrefix(e, "-v="):
			t.Errorf("element %d is a volume flag: %q", i, e)
		case strings.Contains(e, "type=bind"):
			t.Errorf("element %d is a bind mount: %q", i, e)
		case e == "--privileged" || strings.HasPrefix(e, "--cap-add"):
			t.Errorf("element %d is %q", i, e)
		case strings.HasPrefix(e, "--security-opt") && i+1 < len(a) && strings.HasPrefix(a[i+1], "seccomp=") || strings.HasPrefix(e, "--security-opt=seccomp"):
			t.Errorf("element %d sets a seccomp profile", i)
		case e == "--env" && (i+1 >= len(a) || !strings.Contains(a[i+1], "=")):
			t.Errorf("element %d is an --env without a value", i)
		case wd != "" && strings.Contains(e, wd) || home != "" && home != "/" && strings.Contains(e, home):
			t.Errorf("element %d names a host path: %q", i, e)
		}
	}
}

// without is a with every occurrence of the run seq removed.
func without(a []string, seq ...string) []string {
	var out []string
	for i := 0; i < len(a); i++ {
		if i+len(seq) <= len(a) && slices.Equal(a[i:i+len(seq)], seq) {
			i += len(seq) - 1
			continue
		}
		out = append(out, a[i])
	}
	return out
}

// TS-17-24: Args for the zero effective spec under podman equals the golden
// and is pure. 17-REQ-4.1.
func TestArgsGolden_TS17_24(t *testing.T) {
	o := Options{Engine: EnginePodman}
	eff := effective(t, sandbox.Spec{Image: "img:1"})
	a1, a2 := Args(o, testID, testCreated, eff), Args(o, testID, testCreated, eff)
	if !slices.Equal(a1, a2) {
		t.Fatalf("Args is not pure:\n%q\n%q", a1, a2)
	}
	if !slices.Equal(a1, goldenPodmanZero) {
		t.Fatalf("Args =\n%q\nwant\n%q", a1, goldenPodmanZero)
	}
	assertNoForbidden(t, a1)
}

// TS-17-25: Args under docker is the podman golden minus --unsetenv-all.
// 17-REQ-4.2.
func TestArgsDocker_TS17_25(t *testing.T) {
	eff := effective(t, sandbox.Spec{Image: "img:1"})
	pod := Args(Options{Engine: EnginePodman}, testID, testCreated, eff)
	dock := Args(Options{Engine: EngineDocker}, testID, testCreated, eff)
	if !slices.Equal(without(pod, "--unsetenv-all"), dock) || slices.Contains(dock, "--unsetenv-all") ||
		!slices.Equal(dock, without(goldenPodmanZero, "--unsetenv-all")) {
		t.Fatalf("docker Args =\n%q\nwant the podman list without --unsetenv-all:\n%q", dock, pod)
	}
	assertNoForbidden(t, dock)
}

// TS-17-26: embedder labels follow the reserved labels sorted by key, and
// NetworkUnrestricted drops only --network none. 17-REQ-4.3, 17-REQ-4.4.
func TestArgsLabelsAndNetwork_TS17_26(t *testing.T) {
	o := Options{Engine: EnginePodman}
	a := Args(o, testID, testCreated, effective(t, sandbox.Spec{Image: "img:1", Labels: map[string]string{"zone": "b", "app": "a", "mid": "c"}}))
	i := slices.Index(a, "agentkit.sandbox.expires=2025-01-02T03:04:05Z")
	if i < 0 || !slices.Equal(a[i+1:i+7], []string{"--label", "app=a", "--label", "mid=c", "--label", "zone=b"}) {
		t.Fatalf("labels in %q are not sorted after the expires label", a)
	}
	assertNoForbidden(t, a)

	u := Args(o, testID, testCreated, effective(t, sandbox.Spec{Image: "img:1", Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}))
	n := len(goldenPodmanZero) - 6 // the agent's arguments are the last six elements
	want := append(without(goldenPodmanZero[:n], "--network", "none"), "--ttl", "1h0m0s", "--network", "unrestricted", "--workdir", "/workspace")
	if !slices.Equal(u, want) {
		t.Fatalf("NetworkUnrestricted Args =\n%q\nwant\n%q", u, want)
	}
	assertNoForbidden(t, u)
}

// TS-17-27: resource, runtime and userns flags follow --mount in their fixed
// order and only when set. 17-REQ-4.5, 17-REQ-4.6.
func TestArgsResourcesRuntime_TS17_27(t *testing.T) {
	o := Options{Engine: EnginePodman}
	afterMount := func(a []string) []string {
		m := slices.Index(a, "--mount")
		u := slices.Index(a, "--unsetenv-all")
		if m < 0 || u < 0 {
			t.Fatalf("no --mount or --unsetenv-all in %q", a)
		}
		return a[m+2 : u]
	}
	for _, c := range []struct {
		res  sandbox.Resources
		want []string
	}{
		{sandbox.Resources{CPUs: 1.5, MemoryBytes: 268435456, PIDs: 64}, []string{"--cpus", "1.5", "--memory", "268435456", "--memory-swap", "268435456", "--pids-limit", "64"}},
		{sandbox.Resources{CPUs: 1.5}, []string{"--cpus", "1.5"}},
		{sandbox.Resources{MemoryBytes: 268435456}, []string{"--memory", "268435456", "--memory-swap", "268435456"}},
		{sandbox.Resources{PIDs: 64}, []string{"--pids-limit", "64"}},
		{sandbox.Resources{CPUs: 2}, []string{"--cpus", "2"}},
		{sandbox.Resources{}, nil},
	} {
		a := Args(o, testID, testCreated, effective(t, sandbox.Spec{Image: "img:1", Resources: c.res}))
		if got := afterMount(a); !slices.Equal(got, c.want) {
			t.Errorf("Resources %+v: flags after --mount %q, want %q", c.res, got, c.want)
		}
		assertNoForbidden(t, a)
	}
	k := Args(Options{Engine: EnginePodman, Runtime: "krun", UserNS: "keep-id"}, testID, testCreated,
		effective(t, sandbox.Spec{Image: "img:1", Resources: sandbox.Resources{CPUs: 1.5, MemoryBytes: 268435456, PIDs: 64}}))
	want := []string{"--cpus", "1.5", "--memory", "268435456", "--memory-swap", "268435456", "--pids-limit", "64", "--runtime", "krun", "--userns=keep-id"}
	if got := afterMount(k); !slices.Equal(got, want) {
		t.Errorf("krun and UserNS: flags after --mount %q, want %q", got, want)
	}
	assertNoForbidden(t, k)
}

// envValues are the values that follow each --env in a.
func envValues(a []string) []string {
	var out []string
	for i := 0; i+1 < len(a); i++ {
		if a[i] == "--env" {
			out = append(out, a[i+1])
		}
	}
	return out
}

// TS-17-28: Spec.Env overrides a default, and the environment is rendered
// sorted as NAME=value. 17-REQ-4.7.
func TestArgsEnv_TS17_28(t *testing.T) {
	a := Args(Options{Engine: EnginePodman}, testID, testCreated,
		effective(t, sandbox.Spec{Image: "img:1", Env: map[string]string{"HOME": "/workspace/.home", "X": "1", "A_B": "two words"}}))
	want := []string{"A_B=two words", "HOME=/workspace/.home", "LANG=C.UTF-8", defaultPath, "TERM=dumb", "TMPDIR=/tmp", "X=1"}
	if got := envValues(a); !slices.Equal(got, want) {
		t.Fatalf("--env values %q, want %q", got, want)
	}
	assertNoForbidden(t, a)
}

// TS-17-47: InitArgs equals its golden under both engines and has no
// --network none, no -i and no Spec.Env value. 17-REQ-7.6.
func TestInitArgsGolden_TS17_47(t *testing.T) {
	repo := sandbox.GitRepo{URL: "https://github.com/agent-fox-dev/agentkit-go", Ref: "main", Depth: 1}
	eff := effective(t, sandbox.Spec{Image: "img:1", Labels: map[string]string{"team": "core"}, Env: map[string]string{"SECRET": "s3cr3t"},
		Resources: sandbox.Resources{CPUs: 2, MemoryBytes: 1073741824, PIDs: 128}})
	o := Options{Engine: EnginePodman, Runtime: "krun", UserNS: "keep-id"}
	golden := append([]string{
		"run", "--rm", "--init",
		"--name", "agentkit-0123456789abcdef-init",
		"--label", "agentkit.sandbox=0123456789abcdef",
		"--label", "agentkit.sandbox.expires=2025-01-02T03:04:05Z",
		"--label", "team=core",
		"--log-driver", "none",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,mode=1777",
		"--mount", "type=volume,source=agentkit-0123456789abcdef,target=/workspace",
		"--cpus", "2", "--memory", "1073741824", "--memory-swap", "1073741824", "--pids-limit", "128",
		"--runtime", "krun", "--userns=keep-id",
		"--unsetenv-all",
		"--env", "GIT_TERMINAL_PROMPT=0", "--env", "HOME=/tmp", "--env", "LANG=C.UTF-8", "--env", defaultPath,
		"--entrypoint", "git",
		"img:1",
	}, gitclone.Args(repo, "/workspace")...)
	a := InitArgs(o, testID, testCreated, eff, repo)
	if !slices.Equal(a, InitArgs(o, testID, testCreated, eff, repo)) {
		t.Fatal("InitArgs is not pure")
	}
	if !slices.Equal(a, golden) {
		t.Fatalf("InitArgs =\n%q\nwant\n%q", a, golden)
	}
	podNoKrun := InitArgs(Options{Engine: EnginePodman, UserNS: "keep-id"}, testID, testCreated, eff, repo)
	dock := InitArgs(Options{Engine: EngineDocker, UserNS: "keep-id"}, testID, testCreated, eff, repo)
	if !slices.Equal(without(podNoKrun, "--unsetenv-all"), dock) || slices.Contains(dock, "--unsetenv-all") {
		t.Fatalf("docker InitArgs =\n%q\nwant the podman list without --unsetenv-all:\n%q", dock, podNoKrun)
	}
	for _, l := range [][]string{a, podNoKrun, dock} {
		if slices.Contains(l, "-i") || len(without(l, "--network", "none")) != len(l) {
			t.Errorf("InitArgs has -i or --network none: %q", l)
		}
		for _, e := range l {
			if strings.Contains(e, "SECRET") || strings.Contains(e, "s3cr3t") {
				t.Errorf("InitArgs carries a Spec.Env value: %q", e)
			}
		}
		assertNoForbidden(t, l)
	}
}
