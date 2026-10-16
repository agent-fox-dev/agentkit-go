package sandbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// madeSandbox wraps a provider and records whether the sandbox's directory
// existed when Create returned it, which is what tells a cleanup from a
// sandbox that was never made.
type madeSandbox struct {
	sandbox.Provider
	dir     string
	existed bool
}

func (m *madeSandbox) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := m.Provider.Create(ctx, spec)
	if err == nil {
		m.dir = filepath.Join(os.TempDir(), "agentkit-sandbox-local", sb.ID())
		_, statErr := os.Stat(m.dir)
		m.existed = statErr == nil
	}
	return sb, err
}

// TS-22-37 (smoke, 22-PATH-3): Open fails after Create on local with a GitRepo
// source and leaves nothing behind. sandbox.Open, the local provider, the
// sandbox agent and internal/attach are real: local made the sandbox's
// directory, refused the GitRepo Sync with ErrUnsupported, and Open's close
// with a context that cannot be cancelled removed the directory.
func TestSmokeOpenFailsAfterCreateAndCleansUp_TS22_37(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local's directories are the thing under test")
	}
	isolate(t)
	p := &madeSandbox{Provider: local.Unisolated()}

	sb, tools, err := sandbox.Open(context.Background(), p, sandbox.Spec{Network: unrestricted},
		sandbox.GitRepo{URL: "https://example.invalid/repo.git", Ref: "main", Depth: 1}, sandbox.ToolOptions{})

	if sb != nil || tools != nil {
		t.Errorf("Open returned %v and %v, want nil and nil", sb, tools)
	}
	if !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("error = %v, want one matching sandbox.ErrUnsupported", err)
	}
	if !p.existed {
		t.Errorf("local had not made the sandbox directory %s when Create returned: the test saw no cleanup", p.dir)
	}
	if _, statErr := os.Stat(p.dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the sandbox directory %s remains after the failed Open: %v", p.dir, statErr)
	}
}
