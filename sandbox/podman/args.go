package podman

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

const (
	// defaultPATH is the container's PATH unless Spec.Env sets one.
	defaultPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	// tmpfsSpec is the writable directory the tools use besides the
	// workspace: /tmp, executable (go test runs binaries from TMPDIR) and
	// world-writable, so any image user can use it as HOME and TMPDIR.
	// Podman's --read-only also mounts tmpfs at /run, /var/tmp and /dev/shm,
	// which the agent's Landlock ruleset keeps read-only.
	tmpfsSpec = "/tmp:rw,exec,nosuid,nodev,mode=1777"
	// labelID and labelExpires are the reserved labels on every container
	// and volume the provider creates; the key prefix agentkit.sandbox. is
	// the provider's.
	labelID      = "agentkit.sandbox"
	labelExpires = "agentkit.sandbox.expires"
)

// name is the sandbox container's and volume's name.
func name(id string) string { return "agentkit-" + id }

// initName is the init step's container name.
func initName(id string) string { return "agentkit-" + id + "-init" }

// expires is the sandbox's expiry, created + TTL, in RFC 3339, UTC, whole
// seconds: the value of the agentkit.sandbox.expires label.
func expires(created time.Time, ttl time.Duration) string {
	return created.Add(ttl).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// labelArgs are the two reserved labels, then the embedder's sorted by key.
func labelArgs(id string, created time.Time, spec sandbox.Spec) []string {
	args := []string{"--label", labelID + "=" + id, "--label", labelExpires + "=" + expires(created, spec.TTL)}
	for _, k := range slices.Sorted(maps.Keys(spec.Labels)) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	return args
}

// hardeningArgs are the flags every container the provider runs carries,
// up to and including the read-only root.
func hardeningArgs() []string {
	return []string{"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only"}
}

// mountArgs are the /tmp tmpfs and the workspace volume.
func mountArgs(id string, spec sandbox.Spec) []string {
	return []string{"--tmpfs", tmpfsSpec, "--mount", "type=volume,source=" + name(id) + ",target=" + spec.Workdir}
}

// resourceArgs bound the non-zero Resources fields, in a fixed order. Swap
// equals memory, so swap cannot widen the bound.
func resourceArgs(r sandbox.Resources) []string {
	var args []string
	if r.CPUs != 0 {
		args = append(args, "--cpus", strconv.FormatFloat(r.CPUs, 'f', -1, 64))
	}
	if r.MemoryBytes != 0 {
		m := strconv.FormatInt(r.MemoryBytes, 10)
		args = append(args, "--memory", m, "--memory-swap", m)
	}
	if r.PIDs != 0 {
		args = append(args, "--pids-limit", strconv.FormatInt(r.PIDs, 10))
	}
	return args
}

// runtimeArgs select the krun runtime and the user namespace, when set.
func runtimeArgs(o Options) []string {
	var args []string
	if o.Runtime == "krun" {
		args = append(args, "--runtime", "krun")
	}
	if o.UserNS != "" {
		args = append(args, "--userns="+o.UserNS)
	}
	return args
}

// unsetenvArgs drop the image's own environment, which only podman can do.
func unsetenvArgs(o Options) []string {
	if o.Engine == EngineDocker {
		return nil
	}
	return []string{"--unsetenv-all"}
}

// envArgs render vars as --env NAME=value, sorted by name. --env NAME
// without a value would read the engine client's own environment.
func envArgs(vars map[string]string) []string {
	var args []string
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		args = append(args, "--env", k+"="+vars[k])
	}
	return args
}

// containerEnv is Spec.Env plus each default it does not set.
func containerEnv(env map[string]string) map[string]string {
	vars := map[string]string{"HOME": "/tmp", "LANG": "C.UTF-8", "PATH": defaultPATH, "TERM": "dumb", "TMPDIR": "/tmp"}
	maps.Copy(vars, env)
	return vars
}

// Args is the argument list after the engine binary that runs the sandbox's
// agent: `run --rm -i --init`, the name, the reserved and embedder labels,
// `--log-driver none` (the agent's stdout is the protocol stream), `--network
// none` unless the spec is NetworkUnrestricted, `--cap-drop ALL`,
// `--security-opt no-new-privileges`, `--read-only`, a tmpfs at /tmp, the
// workspace volume at Workdir, the resource flags, `--runtime krun` and
// `--userns`, `--unsetenv-all` on podman, the environment, and
// `--entrypoint sandbox.AgentPath` followed by the image and
// sandbox.AgentArgs. spec is the effective spec, with Image set. An empty
// Engine counts as podman. It is pure, and no other flag is ever emitted:
// no host mount, no privilege, no seccomp override.
func Args(o Options, id string, created time.Time, spec sandbox.Spec) []string {
	args := []string{"run", "--rm", "-i", "--init", "--name", name(id)}
	args = append(args, labelArgs(id, created, spec)...)
	args = append(args, "--log-driver", "none")
	if spec.Network.Mode != sandbox.NetworkUnrestricted {
		args = append(args, "--network", "none")
	}
	args = append(args, hardeningArgs()...)
	args = append(args, mountArgs(id, spec)...)
	args = append(args, resourceArgs(spec.Resources)...)
	args = append(args, runtimeArgs(o)...)
	args = append(args, unsetenvArgs(o)...)
	args = append(args, envArgs(containerEnv(spec.Env))...)
	args = append(args, "--entrypoint", sandbox.AgentPath, spec.Image)
	return append(args, sandbox.AgentArgs(spec)...)
}

// InitArgs is the argument list after the engine binary for the init step
// that clones repo into the workspace volume: `run --rm --init`, the init
// container's name, the same labels, hardening, tmpfs, volume, resource,
// runtime and userns flags as Args, `--unsetenv-all` on podman, an
// environment of GIT_TERMINAL_PROMPT=0 and the HOME, LANG and PATH defaults,
// and `--entrypoint git` followed by the image and gitclone.Args. It runs on
// the engine's default network, never the sandbox's, and has no -i and no
// Spec.Env value. spec is the effective spec, with Image set. It is pure.
func InitArgs(o Options, id string, created time.Time, spec sandbox.Spec, repo sandbox.GitRepo) []string {
	args := []string{"run", "--rm", "--init", "--name", initName(id)}
	args = append(args, labelArgs(id, created, spec)...)
	args = append(args, "--log-driver", "none")
	args = append(args, hardeningArgs()...)
	args = append(args, mountArgs(id, spec)...)
	args = append(args, resourceArgs(spec.Resources)...)
	args = append(args, runtimeArgs(o)...)
	args = append(args, unsetenvArgs(o)...)
	args = append(args, envArgs(map[string]string{"GIT_TERMINAL_PROMPT": "0", "HOME": "/tmp", "LANG": "C.UTF-8", "PATH": defaultPATH})...)
	args = append(args, "--entrypoint", "git", spec.Image)
	return append(args, gitclone.Args(repo, spec.Workdir)...)
}
