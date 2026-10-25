//go:build unix

package landlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
)

// Only a regular executable file counts, and every path tried is named.
func TestResolveAgentLooksBesideTheExecutableThenOnPATH(t *testing.T) {
	exeDir, work := evalSymlinks(t, t.TempDir()), evalSymlinks(t, t.TempDir())
	beside := filepath.Join(exeDir, "agentkit-sandboxd")
	pathAgent := script(t, t.TempDir(), "agentkit-sandboxd", "exit 0")
	link := filepath.Join(t.TempDir(), "exe-link")
	if err := os.Symlink(script(t, exeDir, "test.binary", "exit 0"), link); err != nil {
		t.Fatal(err)
	}
	exe := func() (string, error) { return link, nil }
	onPath := func(string) (string, error) { return pathAgent, nil }
	t.Chdir(work)
	script(t, work, "agent", "exit 0")
	if got, _ := resolveAgent("agent", exe, onPath); got != filepath.Join(work, "agent") {
		t.Errorf("AgentBinary agent = %q, want it made absolute", got)
	}
	for name, mk := range map[string]func() error{
		"an executable": func() error { return os.WriteFile(beside, []byte("#!/bin/sh\n"), 0o755) },
		"a directory":   func() error { return os.Mkdir(beside, 0o755) },
		"mode 0644":     func() error { return os.WriteFile(beside, nil, 0o644) },
	} {
		_ = os.RemoveAll(beside)
		if err := mk(); err != nil {
			t.Fatal(err)
		}
		want := pathAgent
		if name == "an executable" {
			want = beside
		}
		if got, _ := resolveAgent("", exe, onPath); got != want {
			t.Errorf("with %s beside the executable: %q, want %q", name, got, want)
		}
	}
	_ = os.RemoveAll(beside)
	s := testSeams(4, nil)
	s.executable = exe
	if _, err := newProvider(Options{Root: t.TempDir()}, s); err == nil || errors.Is(err, ErrNoLandlock) ||
		err.Error() != "landlock: no usable agentkit-sandboxd (tried "+beside+", agentkit-sandboxd on PATH); install it with: "+installLine(buildver.Version()) {
		t.Errorf("no agent: %v", err)
	}
	if got, tried := resolveAgent(work, exe, onPath); got != "" || tried[0] != work {
		t.Errorf("AgentBinary a directory: %q, tried %q", got, tried)
	}
}

// The --version run has an empty environment; protocol 1.N for N >= 2 is
// accepted; a refusal names go install of the matching version, or from source.
func TestNewChecksTheAgentVersion(t *testing.T) {
	for v, want := range map[string]string{"(devel)": "cd sandbox && go install ./cmd/agentkit-sandboxd",
		"v1.4.0": "go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@v1.4.0"} {
		if got := installLine(v); got != want {
			t.Errorf("installLine(%s) = %q", v, got)
		}
	}
	old := versionTimeout
	versionTimeout = 300 * time.Millisecond
	t.Cleanup(func() { versionTimeout = old })
	t.Setenv("AGENTKIT_TEST_SENTINEL", "leak")
	log := filepath.Join(t.TempDir(), "log")
	for _, c := range []struct{ body, want string }{
		{"echo 'agentkit-sandboxd v0.9.0 (protocol 1.2)'", ""},
		{"echo 'agentkit-sandboxd (devel) (protocol 1.7)'", ""},
		{"echo boom; exit 1", `exit status 1: output "boom"`},
		{"echo hello", `unrecognised --version output: output "hello"`},
		{"echo 'agentkit-sandboxd v1 (protocol 2.5)'", "it speaks protocol 2.5; this provider needs 1.2 or later"},
		{"echo 'agentkit-sandboxd v1 (protocol 1.1)'", "it speaks protocol 1.1"},
		{"exec /bin/sleep 60", "timed out after 300ms"},
	} {
		_ = os.Remove(log)
		agent := script(t, t.TempDir(), "agentkit-sandboxd", `printf '%s|%s\n' "$*" "$AGENTKIT_TEST_SENTINEL" >> `+log+"\n"+c.body)
		s := testSeams(4, nil)
		s.runVersion = runVersion
		p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: agent}, s)
		if b, _ := os.ReadFile(log); string(b) != "--version|\n" {
			t.Errorf("%q: the agent ran as %q, want one --version with an empty environment", c.body, b)
		}
		switch {
		case c.want == "" && (err != nil || p == nil):
			t.Errorf("%q: %v", c.body, err)
		case c.want != "" && (p != nil || err == nil || !strings.Contains(err.Error(), c.want) ||
			!strings.HasSuffix(err.Error(), "install a matching agent with: "+installLine(buildver.Version())) || errors.Is(err, ErrNoLandlock)):
			t.Errorf("%q: %v; want a refusal quoting %q", c.body, err, c.want)
		}
	}
}
