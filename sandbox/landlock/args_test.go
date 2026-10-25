package landlock

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The resolver directory is passed only under NetworkUnrestricted; nothing names the sandbox directory.
func TestAgentArgsAddTheExtraRootsInOrder(t *testing.T) {
	ws := "/srv/root/0123456789abcdef/workspace"
	for _, c := range []struct {
		mode sandbox.NetworkMode
		want []string
	}{
		{sandbox.NetworkNone, []string{"--landlock-ro", "/opt/a", "--landlock-ro", "/opt/b", "--landlock-rw", "/srv/c"}},
		{sandbox.NetworkUnrestricted, []string{"--landlock-ro", "/opt/a", "--landlock-ro", "/opt/b", "--landlock-ro", "/run/systemd/resolve", "--landlock-rw", "/srv/c"}},
	} {
		eff := sandbox.Spec{TTL: 30 * time.Minute, Network: sandbox.Network{Mode: c.mode}, Workdir: ws}
		got := agentArgs(eff, []string{"/opt/a", "/opt/b"}, []string{"/srv/c"}, "/run/systemd/resolve")
		if want := append(sandbox.AgentArgs(eff), c.want...); !slices.Equal(got, want) || argAfter(got, "--workdir") != ws {
			t.Errorf("%v: %q,\nwant %q", c.mode, got, want)
		}
		for _, a := range got {
			if a == "/srv/root" || a == "/srv/root/0123456789abcdef" {
				t.Errorf("%v: %q exposes the sandbox directory", c.mode, got)
			}
		}
	}
}

// It is the resolved resolv.conf's directory when strictly beneath /run or /var/run.
func TestResolverDirIsOnlyARunSubdirectory(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(regular, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{regular, filepath.Join(dir, "dangling"), dir} {
		if got := resolverDir(p); got != "" {
			t.Errorf("resolverDir(%s) = %q, want none", p, got)
		}
	}
	for p, want := range map[string]string{
		"/run/systemd/resolve/stub-resolv.conf": "/run/systemd/resolve", "/var/run/foo/resolv.conf": "/var/run/foo",
		"/run/resolv.conf": "", "/var/run/resolv.conf": "", "/etc/resolv.conf": "", "/runx/a/resolv.conf": "",
	} {
		if got := classifyResolver(p); got != want {
			t.Errorf("classifyResolver(%s) = %q, want %q", p, got, want)
		}
	}
}
