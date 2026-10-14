package landlock

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-28: the agent argument list is AgentArgs with the host workspace,
// then ExtraRO, the resolver directory and ExtraRW, in order. 18-REQ-5.1.
func TestAgentArgs_TS18_28(t *testing.T) {
	r, id := "/srv/root", "0123456789abcdef"
	a, b, c, d := "/opt/a", "/opt/b", "/srv/c", "/run/systemd/resolve"
	eff := sandbox.Spec{TTL: 30 * time.Minute, Workdir: filepath.Join(r, id, "workspace")}
	got := agentArgs(eff, []string{a, b}, []string{c}, d)
	want := append(sandbox.AgentArgs(eff), "--landlock-ro", a, "--landlock-ro", b, "--landlock-rw", c)
	if !slices.Equal(got, want) {
		t.Errorf("NetworkNone: %q,\nwant %q", got, want)
	}
	if i := slices.Index(got, "--workdir"); i < 0 || got[i+1] != eff.Workdir {
		t.Errorf("no --workdir %s in %q", eff.Workdir, got)
	}
	if !slices.Equal(agentArgs(eff, []string{a, b}, []string{c}, d), got) {
		t.Error("agentArgs is not pure")
	}
	eff.Network.Mode = sandbox.NetworkUnrestricted
	want = append(sandbox.AgentArgs(eff), "--landlock-ro", a, "--landlock-ro", b, "--landlock-ro", d, "--landlock-rw", c)
	if got := agentArgs(eff, []string{a, b}, []string{c}, d); !slices.Equal(got, want) {
		t.Errorf("NetworkUnrestricted: %q,\nwant %q", got, want)
	}
}

// TS-18-25: no agent argument or environment value exposes the sandbox
// directory itself, only its three subdirectories. 18-REQ-4.4.
func TestNothingExposesSandboxDir_TS18_25(t *testing.T) {
	r, id := "/srv/root", "0123456789abcdef"
	dir := filepath.Join(r, id)
	sub := []string{filepath.Join(dir, "workspace"), filepath.Join(dir, "home"), filepath.Join(dir, "tmp")}
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted} {
		eff := sandbox.Spec{TTL: time.Hour, Network: sandbox.Network{Mode: mode}, Workdir: sub[0]}
		args := agentArgs(eff, []string{"/opt/ro"}, []string{"/srv/rw"}, "/run/systemd/resolve")
		var values []string
		for i, a := range args {
			if (a == "--workdir" || a == "--landlock-ro" || a == "--landlock-rw") && i+1 < len(args) {
				values = append(values, args[i+1])
			}
		}
		for _, kv := range agentEnv(nil, dir) {
			if k, v, _ := cut(kv); k == "HOME" || k == "TMPDIR" {
				values = append(values, v)
			}
		}
		if len(values) < 5 { // --workdir, --landlock-ro, --landlock-rw, HOME, TMPDIR
			t.Fatalf("%v: only %q to check", mode, values)
		}
		for _, v := range values {
			if v == r || v == dir || !slices.Contains(sub, v) && within(v, dir) {
				t.Errorf("%v: %q exposes the sandbox directory", mode, v)
			}
		}
	}
}
