package sandbox

// AgentArgs are agentkit-sandboxd's flags for the effective spec: --ttl,
// --network, and --workdir when Workdir is set. Providers start the agent
// with them, so its Landlock ruleset matches the spec's network mode.
func AgentArgs(spec Spec) []string {
	args := []string{"--ttl", spec.TTL.String(), "--network", spec.Network.Mode.String()}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	return args
}
