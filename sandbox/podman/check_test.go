package podman

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Each refusal is a SpecError from the engine, before any engine command.
func TestCreateRefusesWhatTheContainerCannotHonour(t *testing.T) {
	img := func(s sandbox.Spec) sandbox.Spec { s.Image = "img:1"; return s }
	label := func(k, v string) sandbox.Spec { return img(sandbox.Spec{Labels: map[string]string{k: v}}) }
	env := func(k, v string) sandbox.Spec { return img(sandbox.Spec{Env: map[string]string{k: v}}) }
	workdir := func(w string) sandbox.Spec { return img(sandbox.Spec{Workdir: w}) }
	allowlist := sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{{Host: "example.com", Port: 443}}}
	for _, c := range []struct {
		engine       Engine
		spec         sandbox.Spec
		field, names string // field is "" for an accepted spec
	}{
		{EngineDocker, img(sandbox.Spec{Network: allowlist}), "Network.Mode", ""},
		{EnginePodman, img(sandbox.Spec{Resources: sandbox.Resources{DiskBytes: 1 << 30}}), "Resources.DiskBytes", ""},
		{EnginePodman, img(sandbox.Spec{Provider: map[string]any{"zeta": 1, "alpha": 2}}), "Provider.alpha", "alpha"},
		{EnginePodman, label("agentkit.sandbox.expires", "x"), "Labels", "agentkit.sandbox.expires"},
		{EnginePodman, label("agentkit.sandbox.owner", "x"), "Labels", "reserved"}, {EnginePodman, label("", "x"), "Labels", `""`},
		{EnginePodman, label("a=b", "x"), "Labels", "a=b"}, {EnginePodman, label("a b", "x"), "Labels", "a b"},
		{EnginePodman, label("a\tb", "x"), "Labels", `a\tb`}, {EnginePodman, label("ok", "v\nw"), "Labels", "ok"},
		{EnginePodman, label("agentkit.sandboxes", "x"), "", ""},
		{EnginePodman, workdir("workspace"), "Workdir", "absolute"}, {EnginePodman, workdir("/workspace/"), "Workdir", "clean"},
		{EnginePodman, workdir("/a/../workspace"), "Workdir", "clean"}, {EnginePodman, workdir("/"), "Workdir", "root"},
		{EnginePodman, workdir("/tmp"), "Workdir", "tmpfs"}, {EnginePodman, workdir("/tmp/w"), "Workdir", "tmpfs"},
		{EnginePodman, workdir("/proc/x"), "Workdir", "/proc"}, {EnginePodman, workdir("/sys/x"), "Workdir", "/sys"},
		{EnginePodman, workdir("/dev/x"), "Workdir", "/dev"}, {EnginePodman, workdir("/work,space"), "Workdir", "','"},
		{EnginePodman, workdir("/work\x01"), "Workdir", "control"},
		{EnginePodman, workdir("/home/agentkit/ws"), "", ""}, {EnginePodman, workdir("/tmpfoo"), "", ""},
		{EnginePodman, sandbox.Spec{Image: "-it"}, "Image", "-it"}, {EnginePodman, sandbox.Spec{Image: "--privileged"}, "Image", ""},
		{EnginePodman, sandbox.Spec{Image: "img name"}, "Image", ""}, {EnginePodman, sandbox.Spec{Image: "img\n"}, "Image", ""},
		{EnginePodman, sandbox.Spec{Image: "img\x7f"}, "Image", ""},
		{EnginePodman, env("HOME", "home"), "Env.HOME", "home"}, {EnginePodman, env("HOME", "/home/agentkit"), "Env.HOME", "/home/agentkit"},
		{EnginePodman, env("HOME", "/var/tmp"), "Env.HOME", ""}, {EnginePodman, env("TMPDIR", "/workspacefoo"), "Env.TMPDIR", ""},
		{EnginePodman, env("TMPDIR", "/tmpx"), "Env.TMPDIR", ""}, {EnginePodman, env("HOME", "/tmp/h"), "", ""},
		{EnginePodman, env("HOME", "/workspace/.home"), "", ""}, {EnginePodman, env("TMPDIR", "/workspace"), "", ""},
	} {
		f := newFake(t)
		s, err := testProvider(f, Options{Engine: c.engine}).Create(context.Background(), c.spec)
		if c.field == "" {
			if err != nil {
				t.Errorf("Create(%+v) = %v, want it accepted", c.spec, err)
			} else {
				_ = s.Close(context.Background())
			}
			continue
		}
		var se *sandbox.SpecError
		if !errors.As(err, &se) || se.Field != c.field || se.Provider != string(c.engine) || !errors.Is(err, sandbox.ErrUnsupported) ||
			!strings.Contains(se.Reason, c.names) || len(f.recorded()) != 0 {
			t.Errorf("Create(%+v) = %v after %d commands, want a %s SpecError on %s naming %q", c.spec, err, len(f.recorded()), c.engine, c.field, c.names)
		}
	}
}
