package sandbox_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

var unrestricted = sandbox.Network{Mode: sandbox.NetworkUnrestricted}

// isolate points TMPDIR, under which local keeps its sandboxes, at a fresh
// directory.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
}

// noLocalDir skips a test of a LocalDir transfer, which Windows refuses.
func noLocalDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("LocalDir is refused on Windows")
	}
}

// newLocal creates a local sandbox closed at the end of the test.
func newLocal(t *testing.T) sandbox.Sandbox {
	t.Helper()
	isolate(t)
	sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: unrestricted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// call runs the proxy tool name of sb with args and fails the test unless it
// succeeds.
func call(t *testing.T, sb sandbox.Sandbox, name, args string) core.ToolResult {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tl {
		if p.Name == name {
			r := p.Execute(context.Background(), []byte(args))
			if !r.OK {
				t.Fatalf("%s %s: %+v", name, args, r)
			}
			return r
		}
	}
	t.Fatalf("no proxy tool %s", name)
	return core.ToolResult{}
}

func lexists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
