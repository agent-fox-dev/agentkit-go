// Package buildver reports the AgentKit module version a binary was built
// from. The agent's Hello and the host's tool-mismatch message both use it.
package buildver

import "runtime/debug"

// modulePath is the root module's path.
const modulePath = "github.com/agent-fox-dev/agentkit-go"

// Version is the agentkit-go module version from the build info: the main
// module's when the binary is built from it, otherwise the dependency's. It
// is (devel) when there is none.
func Version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	v := ""
	if bi.Main.Path == modulePath {
		v = bi.Main.Version
	} else {
		for _, d := range bi.Deps {
			if d.Path == modulePath {
				v = d.Version
			}
		}
	}
	if v == "" {
		return "(devel)"
	}
	return v
}
