package landlock

import (
	"os"
	"path/filepath"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// agentArgs are the agent's arguments: sandbox.AgentArgs of eff, whose
// Workdir is the host workspace, then --landlock-ro for each ExtraRO, then
// the resolver directory under NetworkUnrestricted, then --landlock-rw for
// each ExtraRW. It is pure.
func agentArgs(eff sandbox.Spec, extraRO, extraRW []string, resolver string) []string {
	args := sandbox.AgentArgs(eff)
	for _, p := range extraRO {
		args = append(args, "--landlock-ro", p)
	}
	if resolver != "" && eff.Network.Mode == sandbox.NetworkUnrestricted {
		args = append(args, "--landlock-ro", resolver)
	}
	for _, p := range extraRW {
		args = append(args, "--landlock-rw", p)
	}
	return args
}

// resolverDir is the directory of the file path resolves to, when that
// directory lies beneath /run or /var/run and is neither of them, such as
// systemd-resolved's /run/systemd/resolve; otherwise "". Without it, DNS
// does not work under NetworkUnrestricted on such hosts.
func resolverDir(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return classifyResolver(resolved)
}

// classifyResolver is resolved's directory when it lies strictly beneath
// /run or /var/run, else "".
func classifyResolver(resolved string) string {
	dir := filepath.Dir(resolved)
	for _, base := range []string{"/run", "/var/run"} {
		if dir != base && within(base, dir) {
			return dir
		}
	}
	return ""
}
