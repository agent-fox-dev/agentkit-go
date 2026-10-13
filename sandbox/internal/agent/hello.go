package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agentenv"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Hello reports the protocol and agent versions, the workspace root, the
// outline backend, whether git is on the tools' PATH, and the Landlock
// ruleset the caller applied.
func (a *Agent) Hello(context.Context, *sandboxpb.HelloRequest) (*sandboxpb.HelloResponse, error) {
	return &sandboxpb.HelloResponse{
		ProtocolMajor:  sandbox.ProtocolMajor,
		ProtocolMinor:  sandbox.ProtocolMinor,
		Version:        buildver.Version(),
		Workdir:        a.root,
		OutlineBackend: outlineBackend,
		Git:            lookPathIn(a.env, "git"),
		Landlock:       landlockReport(a.landlock),
	}, nil
}

// landlockReport is l as the protocol's LandlockReport, field for field.
func landlockReport(l sandbox.LandlockInfo) *sandboxpb.LandlockReport {
	return &sandboxpb.LandlockReport{Abi: uint32(l.ABI), KernelAbi: uint32(l.KernelABI), Tcp: l.TCP, Note: l.Note,
		Writable: slices.Clone(l.Writable), ReadOnly: slices.Clone(l.ReadOnly)}
}

// lookPathIn reports whether file is an executable on the PATH of env. It
// never reads the process's own PATH, as exec.LookPath would.
func lookPathIn(env []string, file string) bool {
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	for _, dir := range filepath.SplitList(agentenv.Value(env, "PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, file))
		if err == nil && fi.Mode().IsRegular() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0) {
			return true
		}
	}
	return false
}
