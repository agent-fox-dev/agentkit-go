//go:build unix

package landlock

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-32: an agent that exits before Hello yields the exit status and only
// the last 8 KiB of its stderr, and the directory is removed. 18-REQ-5.4,
// 18-REQ-5.6.
func TestAgentExitsEarly_TS18_32(t *testing.T) {
	agent := script(t, t.TempDir(), "agentkit-sandboxd", `if [ "$1" = --version ]; then echo 'agentkit-sandboxd v0 (protocol 1.2)'; exit 0; fi
{ printf HEAD-MARKER; i=0; while [ $i -lt 2000 ]; do printf 0123456789; i=$((i+1)); done; printf TAIL-MARKER; } >&2
exit 2`)
	s := realVersionSeams()
	s.start, s.procs = defaultStart, anyPID{}
	p, err := newProvider(Options{Root: t.TempDir(), AgentBinary: agent}, s)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := p.Create(context.Background(), sandbox.Spec{})
	const prefix = "landlock: agentkit-sandboxd exited with status 2: "
	if sb != nil || err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("Create = %v, %v; want an error beginning %q", sb, err, prefix)
	}
	tail := strings.TrimPrefix(err.Error(), prefix)
	if !strings.HasSuffix(strings.TrimSpace(tail), "TAIL-MARKER") || strings.Contains(tail, "HEAD-MARKER") || len(tail) > 8192 {
		t.Errorf("the carried tail is %d bytes, ending %q", len(tail), tail[max(0, len(tail)-40):])
	}
	if got := entries(t, p.root); len(got) != 0 {
		t.Errorf("Root holds %v", got)
	}
}
