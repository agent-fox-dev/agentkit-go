package landlock

import (
	"slices"
	"strings"
	"testing"
)

// cut splits NAME=value.
func cut(kv string) (string, string, bool) { return strings.Cut(kv, "=") }

// TS-18-30: the agent environment is exactly Spec.Env plus HOME, TMPDIR and
// a default PATH, sorted, with nothing from the embedder. 18-REQ-5.3.
func TestAgentEnv_TS18_30(t *testing.T) {
	t.Setenv("AGENTKIT_EMBEDDER_SENTINEL", "leak")
	dir := "/srv/root/0123456789abcdef"
	e1 := agentEnv(map[string]string{"B": "2", "A": "1"}, dir)
	want := []string{"A=1", "B=2", "HOME=" + dir + "/home", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TMPDIR=" + dir + "/tmp"}
	if !slices.Equal(e1, want) {
		t.Errorf("agentEnv = %q,\nwant %q", e1, want)
	}
	e2 := agentEnv(map[string]string{"PATH": "/custom"}, dir)
	n := 0
	for _, kv := range e2 {
		if strings.HasPrefix(kv, "PATH=") {
			n++
		}
	}
	if n != 1 || !slices.Contains(e2, "PATH=/custom") {
		t.Errorf("a Spec.Env PATH: %q", e2)
	}
	e3 := agentEnv(nil, dir)
	if len(e3) != 3 {
		t.Errorf("agentEnv(nil) = %q", e3)
	}
	for _, e := range [][]string{e1, e2, e3} {
		if !slices.IsSorted(e) || strings.Contains(strings.Join(e, " "), "AGENTKIT_EMBEDDER_SENTINEL") {
			t.Errorf("%q is unsorted or carries the embedder's environment", e)
		}
	}
}
