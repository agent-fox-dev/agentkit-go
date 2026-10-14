// Package buildver reports the AgentKit sandbox module version a binary was
// built from. The agent's Hello and the host's tool-mismatch message both use
// it. The answer is, in order: the link-time stamp, when the build set one;
// the module version from the build info; (devel). A host binary never sets
// the stamp, so it is unaffected by it.
package buildver

import "runtime/debug"

// modulePath is the sandbox module's path; the agent and the host code both
// live in it.
const modulePath = "github.com/agent-fox-dev/agentkit-go/sandbox"

// stamp is the version a build sets at link time, with
// -ldflags "-X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=vX.Y.Z"
// (the sandbox image build passes VERSION). No source assigns it.
var stamp string

// commit and buildTime are the source revision and the UTC build time a build
// sets at link time, like stamp (the sandbox image build passes BUILD and
// BUILD_TIME). No source assigns them.
var commit, buildTime string

// Version is the link-time stamp when one was set, else the sandbox module's
// version from the build info: the main
// module's when the binary is built from it, otherwise the dependency's (its
// replacement's, when replaced). It is (devel) when there is none.
func Version() string {
	if stamp != "" {
		return stamp
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	v := ""
	if bi.Main.Path == modulePath {
		v = bi.Main.Version
	} else {
		for _, d := range bi.Deps {
			if d.Path != modulePath {
				continue
			}
			v = d.Version
			if d.Replace != nil {
				v = d.Replace.Version
			}
		}
	}
	if v == "" {
		return "(devel)"
	}
	return v
}

// Build is the source revision and the build time stamped at link time; each
// is empty when the build set none.
func Build() (string, string) { return commit, buildTime }
