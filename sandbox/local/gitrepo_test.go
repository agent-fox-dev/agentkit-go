package local_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-8: the local provider refuses a GitRepo with ErrUnsupported naming
// local, leaves the workspace empty, and stays usable; package local's code
// is unchanged. 17-REQ-1.7.
func TestLocalRefusesGitRepo_TS17_8(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	sb := create(t, sandbox.Spec{Network: unrestricted})
	_, err := sb.Sync(ctx, sandbox.GitRepo{URL: "https://github.com/agent-fox-dev/agentkit-go"})
	if !errors.Is(err, sandbox.ErrUnsupported) || !strings.Contains(err.Error(), "local") {
		t.Fatalf("Sync(GitRepo) = %v, want an error matching ErrUnsupported naming local", err)
	}
	ws := filepath.Join(sandboxDir(sb), "workspace")
	if ents, err := os.ReadDir(ws); err != nil || len(ents) != 0 {
		t.Fatalf("workspace after the refusal: %v entries, err %v; want it empty", len(ents), err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatalf("LocalDir Sync after the refusal: %v", err)
	}
}
