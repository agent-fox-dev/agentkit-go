package local_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

var unrestricted = sandbox.Network{Mode: sandbox.NetworkUnrestricted}

// isolate points TMPDIR at a fresh directory and returns local's root under
// it.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	return filepath.Join(os.TempDir(), "agentkit-sandbox-local")
}

// create makes a local sandbox closed at the end of the test.
func create(t *testing.T, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	sb, err := local.Unisolated().Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// dirOf is the directory of a local sandbox.
func dirOf(sb sandbox.Sandbox) string {
	return filepath.Join(os.TempDir(), "agentkit-sandbox-local", sb.ID())
}

// proxy is the proxy tool name of sb.
func proxy(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tl {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no proxy tool %s", name)
	return core.Tool{}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
