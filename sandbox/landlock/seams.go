package landlock

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

// defaultSeams are the real platform, probe and helpers.
func defaultSeams() seams {
	return seams{goos: runtime.GOOS, probe: defaultProbe, runVersion: runVersion, executable: os.Executable, lookPath: exec.LookPath,
		resolver: resolverDir, procs: defaultProcTable(), start: defaultStart, remove: removeTree,
		initCommand: defaultInitCommand}
}

func (p *provider) Reap(context.Context) ([]string, error) { panic("not implemented") }
