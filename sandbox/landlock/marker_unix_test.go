//go:build unix

package landlock

import (
	"context"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-22: each sandbox gets a 16-hex id and a 0700 directory with 0700
// workspace, home and tmp and a 0600 marker under any umask. 18-REQ-4.1.
func TestSandboxDirModes_TS18_22(t *testing.T) {
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with)
	for _, um := range []int{0, 0o022, 0o077} {
		old := syscall.Umask(um)
		s, err := p.Create(context.Background(), sandbox.Spec{})
		syscall.Umask(old)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s.ID()) {
			t.Errorf("ID %q", s.ID())
		}
		d := filepath.Join(p.root, s.ID())
		for _, x := range []string{d, filepath.Join(d, "workspace"), filepath.Join(d, "home"), filepath.Join(d, "tmp")} {
			if got := perm(t, x); got != 0o700 {
				t.Errorf("umask %o: %s has mode %o, want 0700", um, x, got)
			}
		}
		if got := perm(t, filepath.Join(d, "agentkit-sandbox.json")); got != 0o600 {
			t.Errorf("umask %o: the marker has mode %o, want 0600", um, got)
		}
	}
}
