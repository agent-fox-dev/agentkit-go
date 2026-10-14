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

// script writes an executable shell script holding body.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// realVersionSeams are Linux seams with probe 4 and the real version runner.
func realVersionSeams() seams {
	s := testSeams(4, nil)
	s.runVersion = defaultSeams().runVersion
	return s
}

// TS-18-12: the agent binary resolves as AgentBinary, then beside the
// resolved executable, then on PATH, accepting only regular executable
// files. 18-REQ-2.5.
func TestResolveAgent_TS18_12(t *testing.T) {
	exeDir, pathDir, work := t.TempDir(), t.TempDir(), t.TempDir()
	script(t, exeDir, "agentkit-sandboxd", "exit 0")
	pathAgent := script(t, pathDir, "agentkit-sandboxd", "exit 0")
	exe := script(t, exeDir, "test.binary", "exit 0")
	link := filepath.Join(t.TempDir(), "exe-link")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	executable := func() (string, error) { return link, nil }
	lookPath := func(string) (string, error) { return pathAgent, nil }

	t.Chdir(work)
	if err := os.Mkdir("bin", 0o755); err != nil {
		t.Fatal(err)
	}
	script(t, filepath.Join(work, "bin"), "agent", "exit 0")
	if got, _ := resolveAgent("bin/agent", executable, lookPath); got != filepath.Join(evalSymlinks(t, work), "bin", "agent") && got != filepath.Join(work, "bin", "agent") {
		t.Errorf("(a) = %q, want the absolute bin/agent", got)
	}
	if got, _ := resolveAgent("", executable, lookPath); got != filepath.Join(evalSymlinks(t, exeDir), "agentkit-sandboxd") {
		t.Errorf("(b) = %q, want the file beside the resolved executable", got)
	}
	if err := os.Remove(filepath.Join(exeDir, "agentkit-sandboxd")); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveAgent("", executable, lookPath); got != pathAgent {
		t.Errorf("(c) = %q, want the PATH candidate", got)
	}
	if err := os.Mkdir(filepath.Join(exeDir, "agentkit-sandboxd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveAgent("", executable, lookPath); got != pathAgent {
		t.Errorf("(d, directory) = %q, want the PATH candidate", got)
	}
	if err := os.Remove(filepath.Join(exeDir, "agentkit-sandboxd")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exeDir, "agentkit-sandboxd"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := resolveAgent("", executable, lookPath); got != pathAgent {
		t.Errorf("(d, mode 0644) = %q, want the PATH candidate", got)
	}
}

// TS-18-14: New runs --version once with an empty environment and accepts
// protocol 1.N for N >= 2. 18-REQ-2.7.
func TestVersionCheck_TS18_14(t *testing.T) {
	t.Setenv("AGENTKIT_TEST_SENTINEL", "leak")
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	for _, line := range []string{"agentkit-sandboxd v0.9.0 (protocol 1.2)", "agentkit-sandboxd (devel) (protocol 1.7)"} {
		_ = os.Remove(log)
		agent := script(t, t.TempDir(), "agentkit-sandboxd", `printf '%s|%s\n' "$*" "$AGENTKIT_TEST_SENTINEL" >> `+log+"\necho '"+line+"'")
		p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: agent}, realVersionSeams())
		if err != nil || p == nil {
			t.Fatalf("%q: %v, %v", line, p, err)
		}
		if b, _ := os.ReadFile(log); string(b) != "--version|\n" {
			t.Errorf("%q: the agent ran as %q, want one --version with an empty environment", line, b)
		}
	}
	for line, want := range map[string][2]int{"agentkit-sandboxd v0.9.0 (protocol 1.2)\n": {1, 2}, "agentkit-sandboxd (devel) (protocol 1.7)": {1, 7}} {
		if maj, min, err := parseVersion(line); err != nil || maj != want[0] || min != want[1] {
			t.Errorf("parseVersion(%q) = %d, %d, %v", line, maj, min, err)
		}
	}
	if versionTimeout != 10*time.Second {
		t.Errorf("versionTimeout = %v", versionTimeout)
	}
	if agentBin != "" {
		if _, err := newProvider(Options{Root: t.TempDir(), AgentBinary: agentBin}, realVersionSeams()); err != nil {
			t.Errorf("the built agent: %v", err)
		}
	}
}

// TS-18-15: a version run that fails, hangs, prints garbage or reports an
// unsupported protocol makes New refuse, quoting the output and the install
// command. 18-REQ-2.8.
func TestVersionRefused_TS18_15(t *testing.T) {
	old := versionTimeout
	versionTimeout = 500 * time.Millisecond
	t.Cleanup(func() { versionTimeout = old })
	for _, c := range []struct{ body, want string }{
		{"echo boom; exit 1", "boom"},
		{"echo hello", "hello"},
		{"echo 'agentkit-sandboxd v1 (protocol 2.5)'", "protocol 2.5"},
		{"echo 'agentkit-sandboxd v1 (protocol 1.1)'", "protocol 1.1"},
		{"exec /bin/sleep 60", "timed out"},
	} {
		agent := script(t, t.TempDir(), "agentkit-sandboxd", c.body)
		start := time.Now()
		p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: agent}, realVersionSeams())
		if p != nil || err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), installLine(buildver.Version())) || errors.Is(err, ErrNoLandlock) {
			t.Errorf("%q: %v, %v; want a refusal quoting %q and the install line", c.body, p, err, c.want)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("%q took %v", c.body, time.Since(start))
		}
	}
}
