package codemode_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/codemode"
)

// TS-08-44: the defaults are the documented ones, and the runtime's only new
// dependency is go.starlark.net — none of its packages that reach the host.
func TestDepsDefaultsAndIsolation_TS08_44(t *testing.T) {
	o := codemode.DefaultOptions()
	if o.Name != "code_mode" || o.MaxTimeout != 30*time.Second || o.MaxSteps != 100_000 || o.MaxCalls != 50 ||
		o.MaxConcurrentCalls != 8 || o.MaxOutputBytes != 102400 || o.SpillDir == "" || o.DisableSpill {
		t.Fatalf("defaults = %+v", o)
	}
	out, err := exec.Command("go", "list", "-deps", "github.com/agentfox/agentkit-go/codemode").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "go.starlark.net/") {
			switch pkg {
			case "go.starlark.net/starlark", "go.starlark.net/syntax", "go.starlark.net/resolve",
				"go.starlark.net/internal/compile", "go.starlark.net/internal/spell":
			default:
				t.Errorf("codemode depends on %s; only the interpreter packages are wanted", pkg)
			}
		}
		if strings.HasPrefix(pkg, "github.com/chzyer/readline") || strings.HasPrefix(pkg, "google.golang.org/protobuf") {
			t.Errorf("codemode pulls in %s", pkg)
		}
	}
}
