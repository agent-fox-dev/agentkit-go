package podman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// versionRunner answers every command with out, or fails with err.
func versionRunner(out string, err error) *fakeRunner {
	return &fakeRunner{answer: func(context.Context, []string) (tools.ExecResult, error) { return exitResult(0, out), err }}
}

// The binary is found on EngineEnv's PATH, never the process's.
func TestNewResolvesTheBinaryAndDetectsTheEngineOnlyWhenUnset(t *testing.T) {
	dir := stubBinary(t, "podman")
	env := []string{"PATH=" + dir}
	t.Setenv("PATH", t.TempDir())

	fake := versionRunner("podman version 5.2.1", nil)
	if p, err := newWithRunner(Options{Engine: EngineDocker, EngineEnv: env}, fake); err != nil || len(fake.recorded()) != 0 ||
		p.Capabilities().Provider != "docker" {
		t.Fatalf("New(Engine docker) = %v, %v after %d commands; want docker and none", p, err, len(fake.recorded()))
	}
	fake = versionRunner("podman version 5.2.1", nil)
	p, err := newWithRunner(Options{EngineEnv: env}, fake)
	if err != nil {
		t.Fatal(err)
	}
	cmds := fake.recorded()
	if len(cmds) != 1 || !slices.Equal(cmds[0].argv, []string{filepath.Join(dir, "podman"), "--version"}) || cmds[0].timeout != 30*time.Second {
		t.Errorf("detection ran %+v, want one --version of the resolved binary with 30s", cmds)
	}
	if got := p.Capabilities().Provider; got != "podman" {
		t.Errorf("detected %q, want podman", got)
	}
}

func TestNewRefusesBadOptionsAndUnknownEngines(t *testing.T) {
	podmanDir := []string{"PATH=" + stubBinary(t, "podman")}
	for _, c := range []struct {
		name string
		o    Options
		r    *fakeRunner
		want []string
	}{
		{"missing default binary", Options{EngineEnv: []string{"PATH=" + t.TempDir()}}, &fakeRunner{}, []string{`"podman"`, "install podman or docker", "Options.Binary"}},
		{"missing named binary", Options{Binary: "nerdctl-missing", EngineEnv: podmanDir}, &fakeRunner{}, []string{"nerdctl-missing", "install podman or docker"}},
		{"unknown engine", Options{Engine: "containerd", EngineEnv: podmanDir}, &fakeRunner{}, []string{"Engine", "containerd"}},
		{"crun-vm", Options{Runtime: "crun-vm", EngineEnv: podmanDir}, &fakeRunner{}, []string{"Runtime", "crun-vm", "VM disk image"}},
		{"runc", Options{Runtime: "runc", EngineEnv: podmanDir}, &fakeRunner{}, []string{"Runtime", "runc", "crun-vm"}},
		{"krun with docker", Options{Runtime: "krun", Engine: EngineDocker, EngineEnv: podmanDir}, &fakeRunner{}, []string{"krun", "docker"}},
		{"krun on detected docker", Options{Runtime: "krun", EngineEnv: podmanDir}, versionRunner("Docker version 24.0.7", nil), []string{"krun", "is docker"}},
		{"unknown --version", Options{EngineEnv: podmanDir}, versionRunner("nerdctl version 1.7.0", nil), []string{`"nerdctl version 1.7.0"`}},
		{"--version fails", Options{EngineEnv: podmanDir}, versionRunner("", errors.New("exec failed")), []string{"--version", "exec failed"}},
	} {
		p, err := newWithRunner(c.o, c.r)
		if p != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want an error", c.name, p, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", c.name, err, w)
			}
		}
		if n := len(c.r.recorded()); n > 1 || n == 1 && c.r.answer == nil {
			t.Errorf("%s: New ran %d commands", c.name, n)
		}
	}
	if _, err := New(Options{Binary: "/nonexistent/podman"}); err == nil || !strings.Contains(err.Error(), "/nonexistent/podman") {
		t.Errorf("New with a missing binary path = %v", err)
	}
}

// The version line is found among the stderr noise the output carries.
func TestParseVersionIdentifiesTheEngine(t *testing.T) {
	for out, want := range map[string]Engine{
		"podman version 5.2.1\n":                       EnginePodman,
		"Docker version 20.10.24+dfsg1, build 297e128": EngineDocker,
		"Emulate Docker CLI using podman. Create /etc/containers/nodocker to quiet msg.\npodman version 5.2.1\n": EnginePodman,
		"WARN[0000] some warning\nDocker version 24.0.7, build afdd53b\n":                                        EngineDocker,
		"": "", "Podman Engine 5": "", "WARN[0000] x\nnerdctl version 1.7.0": "",
	} {
		got, err := parseVersion(out)
		if got != want || (err == nil) != (want != "") || err != nil && !strings.Contains(err.Error(), `"`+strings.ReplaceAll(out, "\n", `\n`)+`"`) {
			t.Errorf("parseVersion(%q) = %q, %v; want %q (or an error quoting the output)", out, got, err, want)
		}
	}
}

func TestLookPathFindsOnlyExecutableRegularFiles(t *testing.T) {
	dir := stubBinary(t, "podman")
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		binary, path, want string
	}{
		{"podman", "relative::" + dir, filepath.Join(dir, "podman")}, {"podman", dir + "/missing:" + dir, filepath.Join(dir, "podman")},
		{filepath.Join(dir, "podman"), "", filepath.Join(dir, "podman")}, {"plain", dir, ""}, {plain, "", ""}, {dir, "", ""}, {"podman", "relative", ""},
	} {
		got, err := lookPath(c.binary, []string{"PATH=/elsewhere", "PATH=" + c.path})
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("lookPath(%q, PATH=%q) = %q, %v; want %q", c.binary, c.path, got, err, c.want)
		}
	}
}

// Capabilities name the engine and say a krun sandbox is a micro-VM.
func TestCapabilitiesDescribeTheEngine(t *testing.T) {
	for _, o := range []Options{{Engine: EnginePodman}, {Engine: EngineDocker}, {Engine: EnginePodman, Runtime: "krun"}} {
		got, iso := newResolved(o, "/usr/bin/x", o.Engine, nil).Capabilities(), sandbox.IsolationContainer
		if o.Runtime == "krun" {
			iso = sandbox.IsolationMicroVM
		}
		if got.Provider != string(o.Engine) || got.Isolation != iso || !got.Image || !got.Workdir || !got.Landlock ||
			!slices.Equal(got.Network, []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}) ||
			got.Resources != (sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true}) {
			t.Errorf("%+v: Capabilities = %+v", o, got)
		}
	}
}
