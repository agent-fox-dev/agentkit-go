package landlock

import (
	"context"
	"os"
	"os/exec"
	"runtime"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// defaultSeams are the real platform, probe and helpers.
func defaultSeams() seams {
	return seams{goos: runtime.GOOS, probe: defaultProbe, runVersion: runVersion, executable: os.Executable, lookPath: exec.LookPath}
}

func (p *provider) Capabilities() sandbox.Capabilities { panic("not implemented") }

func (p *provider) Create(context.Context, sandbox.Spec) (sandbox.Sandbox, error) {
	panic("not implemented")
}

func (p *provider) Reap(context.Context) ([]string, error) { panic("not implemented") }
