package landlock

import "github.com/agent-fox-dev/agentkit-go/sandbox"

// notes is Capabilities.Notes: what the confinement does not protect.
const notes = "landlock is a confinement, not an isolation: the host kernel, memory, CPU and process table are shared, and there are no resource limits. " +
	"NetworkNone denies TCP only; UDP and Multipath TCP are not restricted. " +
	"A process inside can connect to any Unix socket the embedder's user can reach, such as a D-Bus session bus, an ssh-agent, or a podman or docker socket, and act outside the confinement through it. " +
	"It can signal the user's processes, and a process that starts a new session can outlive Close. " +
	"Tool results name host paths. For untrusted code use sandbox/podman."

// Capabilities: a Landlock confinement with no network or the host's, no
// resource bounds, no image and no workspace choice; the agent applies
// Landlock. The value never depends on Options.
func (p *provider) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{
		Provider:  "landlock",
		Isolation: sandbox.IsolationConfinement,
		Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted},
		Landlock:  true,
		Notes:     notes,
	}
}
