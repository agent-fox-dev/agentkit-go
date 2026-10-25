package landlock

import (
	"slices"
	"testing"
)

// A default PATH unless Spec.Env sets one; nothing from the embedder.
func TestAgentEnvAddsThePrivateDirectories(t *testing.T) {
	t.Setenv("AGENTKIT_EMBEDDER_SENTINEL", "leak")
	dir := "/srv/root/0123456789abcdef"
	home, tmp := "HOME="+dir+"/home", "TMPDIR="+dir+"/tmp"
	for _, c := range []struct {
		env  map[string]string
		want []string
	}{
		{nil, []string{home, "PATH=" + defaultPATH, tmp}},
		{map[string]string{"B": "2", "A": "1"}, []string{"A=1", "B=2", home, "PATH=" + defaultPATH, tmp}},
		{map[string]string{"PATH": "/custom"}, []string{home, "PATH=/custom", tmp}},
	} {
		got := agentEnv(c.env, dir)
		if !slices.Equal(got, c.want) || envValue(got, "PATH") != c.want[len(c.want)-2][5:] {
			t.Errorf("agentEnv(%v) = %q, want %q", c.env, got, c.want)
		}
	}
	if envValue([]string{"A=1"}, "PATH") != "" {
		t.Error("envValue found an unset variable")
	}
}
