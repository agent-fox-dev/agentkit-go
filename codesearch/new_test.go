package codesearch_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codesearch"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// PRD §1: the single entry point is New(ws, opts) (tools.Index, error). A
// failed New yields a nil interface, not a nil *Index inside one, so an
// embedder that does not check err first still gets Options.Index == nil.
func TestNewFailureYieldsANilInterface(t *testing.T) {
	var idx tools.Index
	var err error
	// A nil workspace is an error on every platform; on windows every call is.
	idx, err = codesearch.New(nil, codesearch.Options{})
	if err == nil {
		t.Fatal("New(nil, ...) returned no error")
	}
	if idx != nil {
		t.Fatalf("New returned %T(%v) with its error, want a nil interface", idx, idx)
	}

	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Index: idx})
	if err != nil {
		t.Fatalf("tools.All with the failed New's result: %v", err)
	}
	for _, tl := range all {
		if tl.Name == "code_search" {
			t.Error("tools.All added code_search for an index that failed to construct")
		}
	}
}

// 03-REQ-8.6: an embedder falls back to Options.Index == nil, which assumes a
// single cross-platform code path. testdata/embedder is that code path, with no
// build constraint; it must pass go vet for every supported target, including
// windows/amd64, where the index is a stub.
func TestEmbedderCompilesOnEveryTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-target build gate skipped under -short")
	}
	root := moduleRoot(t)

	for _, target := range crossTargets {
		target := target
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			cmd := exec.Command("go", "vet", "./testdata/embedder")
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"GOOS="+target.goos,
				"GOARCH="+target.goarch,
				"CGO_ENABLED=0",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("the embedder does not compile for %s/%s: %v\n%s",
					target.goos, target.goarch, err, strings.TrimSpace(string(out)))
			}
		})
	}
}
