package podman

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

// stubBinary makes an executable named name in a fresh directory and returns
// the directory.
func stubBinary(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub engine is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// versionRunner answers --version with out.
func versionRunner(out string) *fakeRunner {
	return &fakeRunner{answer: func(_ context.Context, argv []string) (tools.ExecResult, error) {
		return exitResult(0, out), nil
	}}
}

// TS-17-10: New resolves the binary through EngineEnv's PATH and runs one
// --version command with a 30-second timeout, only when Engine is empty.
// 17-REQ-2.1.
func TestNewResolvesAndDetects_TS17_10(t *testing.T) {
	dir := stubBinary(t, "podman")
	env := []string{"PATH=" + dir}
	t.Setenv("PATH", t.TempDir()) // the test process's PATH has no podman

	fake := versionRunner("podman version 5.2.1")
	p, err := newWithRunner(Options{Engine: EnginePodman, EngineEnv: env}, fake)
	if err != nil || p == nil {
		t.Fatalf("New(Engine podman) = %v, %v", p, err)
	}
	if n := len(fake.recorded()); n != 0 {
		t.Fatalf("New with Engine set ran %d commands, want 0", n)
	}

	fake = versionRunner("podman version 5.2.1")
	p, err = newWithRunner(Options{EngineEnv: env}, fake)
	if err != nil || p == nil {
		t.Fatalf("New(detect) = %v, %v", p, err)
	}
	cmds := fake.recorded()
	if len(cmds) != 1 {
		t.Fatalf("New with Engine empty ran %d commands, want 1", len(cmds))
	}
	want := []string{filepath.Join(dir, "podman"), "--version"}
	if !slices.Equal(cmds[0].argv, want) || cmds[0].timeout != 30*time.Second {
		t.Errorf("the detection command is %q with timeout %v, want %q with 30s", cmds[0].argv, cmds[0].timeout, want)
	}
	if got := p.Capabilities().Provider; got != "podman" {
		t.Errorf("detected engine %q, want podman", got)
	}
}

// TS-17-11: New fails with an install hint, running nothing, when the
// binary does not resolve. 17-REQ-2.2.
func TestNewMissingBinary_TS17_11(t *testing.T) {
	env := []string{"PATH=" + t.TempDir()}
	for _, c := range []struct{ binary, name string }{{"", "podman"}, {"nerdctl-missing", "nerdctl-missing"}} {
		fake := &fakeRunner{}
		p, err := newWithRunner(Options{Binary: c.binary, EngineEnv: env}, fake)
		if p != nil || err == nil {
			t.Fatalf("New(Binary %q) = %v, %v; want nil and an error", c.binary, p, err)
		}
		for _, want := range []string{c.name, "install podman or docker", "Options.Binary"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("New(Binary %q): %q does not contain %q", c.binary, err, want)
			}
		}
		if n := len(fake.recorded()); n != 0 {
			t.Errorf("New(Binary %q) ran %d commands", c.binary, n)
		}
	}
}

// TS-17-13: unrecognised --version output is refused with the output
// quoted. 17-REQ-2.4.
func TestNewUnknownEngine_TS17_13(t *testing.T) {
	env := []string{"PATH=" + stubBinary(t, "podman")}
	p, err := newWithRunner(Options{EngineEnv: env}, versionRunner("nerdctl version 1.7.0"))
	if p != nil || err == nil || !strings.Contains(err.Error(), "nerdctl version 1.7.0") {
		t.Fatalf("New = %v, %v; want nil and an error quoting the output", p, err)
	}
	for _, out := range []string{"nerdctl version 1.7.0", "", "Podman Engine 5"} {
		if _, err := parseVersion(out); err == nil || !strings.Contains(err.Error(), `"`+out+`"`) {
			t.Errorf("parseVersion(%q) = %v, want an error quoting it", out, err)
		}
	}
}

// TS-17-14: New refuses an unknown Engine, an unsupported Runtime and krun
// with docker, naming the option and running nothing. 17-REQ-2.5.
func TestNewRefusesOptions_TS17_14(t *testing.T) {
	env := []string{"PATH=" + stubBinary(t, "podman")}
	for _, c := range []struct {
		o    Options
		want []string
	}{
		{Options{Engine: "containerd"}, []string{"Engine", "containerd"}},
		{Options{Runtime: "crun-vm"}, []string{"Runtime", "crun-vm", "VM disk image"}},
		{Options{Runtime: "runc"}, []string{"Runtime", "runc", "crun-vm", "VM disk image"}},
		{Options{Runtime: "krun", Engine: EngineDocker}, []string{"Runtime", "krun", "docker"}},
	} {
		fake := &fakeRunner{}
		c.o.EngineEnv = env
		p, err := newWithRunner(c.o, fake)
		if p != nil || err == nil {
			t.Fatalf("New(%+v) = %v, %v; want nil and an error", c.o, p, err)
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("New(%+v): %q does not contain %q", c.o, err, w)
			}
		}
		if n := len(fake.recorded()); n != 0 {
			t.Errorf("New(%+v) ran %d commands", c.o, n)
		}
	}
}
