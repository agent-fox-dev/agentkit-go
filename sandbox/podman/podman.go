// Package podman is AgentKit's first sandbox provider that isolates. Each
// sandbox's agentkit-sandboxd runs as the only process of a hardened,
// ephemeral podman or docker container that holds one workspace volume:
// no host mount, a read-only root, no capabilities, no new privileges, an
// explicit environment and, by default, no network. The provider drives the
// engine's command line (never the Docker or Podman Go API clients), and
// the argument lists it runs are pure functions, Args and InitArgs, that
// can be checked offline.
//
// A sandbox's lifetime is bounded twice: attach's host-side timer destroys
// it at its TTL, and the agent's own --ttl ends the container, which --rm
// removes, even when the host is gone. The volume of a host that died
// carries an expiry label, and Reap removes it once expired.
package podman

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Engine is the flavour of container command line the provider drives.
type Engine string

const (
	// EnginePodman is podman, including a docker shim that runs podman.
	EnginePodman Engine = "podman"
	// EngineDocker is docker.
	EngineDocker Engine = "docker"
)

// Options configures the provider.
type Options struct {
	// Binary is the engine command; empty means "podman". A bare name is
	// resolved through EngineEnv's PATH.
	Binary string
	// Engine is the engine's flavour; empty means detected once from
	// `<Binary> --version`.
	Engine Engine
	// Runtime is "" (the engine's default OCI runtime) or "krun" (a libkrun
	// micro-VM; podman only).
	Runtime string
	// UserNS is passed as --userns=<UserNS> when set.
	UserNS string
	// EngineEnv is the environment of every engine command; nil means
	// os.Environ(). Nothing in it reaches a container: only explicit
	// --env NAME=value does.
	EngineEnv []string
}

// detectTimeout bounds the one `--version` New runs.
const detectTimeout = 30 * time.Second

// provider is the podman provider.
type provider struct {
	opts   Options // Engine resolved
	bin    string  // the resolved engine binary
	engine Engine
	env    []string
	r      runner
	// now and defaultImage are seams for tests.
	now          func() time.Time
	defaultImage func() (string, error)
}

// New returns the provider. It resolves Options.Binary through EngineEnv's
// PATH, refuses unsupported options, and runs no engine command other than
// one `<Binary> --version`, and that only when Options.Engine is empty.
func New(o Options) (sandbox.Provider, error) {
	env := o.EngineEnv
	if env == nil {
		env = os.Environ()
	}
	return newWithRunner(o, realRunner{env: env})
}

// newWithRunner is New over the runner r.
func newWithRunner(o Options, r runner) (sandbox.Provider, error) {
	if err := checkOptions(o); err != nil {
		return nil, err
	}
	env := o.EngineEnv
	if env == nil {
		env = os.Environ()
	}
	if o.Binary == "" {
		o.Binary = "podman"
	}
	bin, err := lookPath(o.Binary, env)
	if err != nil {
		return nil, err
	}
	engine := o.Engine
	if engine == "" {
		ctx, cancel := context.WithTimeout(context.Background(), detectTimeout)
		defer cancel()
		res, err := r.run(ctx, []string{bin, "--version"}, detectTimeout)
		if err != nil {
			return nil, fmt.Errorf("podman: %s --version: %w", bin, err)
		}
		if engine, err = parseVersion(res.Output); err != nil {
			return nil, err
		}
		if o.Runtime == "krun" && engine == EngineDocker {
			return nil, fmt.Errorf("podman: Options.Runtime %q needs podman, and %s is docker", o.Runtime, bin)
		}
	}
	return newResolved(o, bin, engine, r), nil
}

// checkOptions refuses an unknown Engine, a Runtime other than "" or krun,
// and krun with docker.
func checkOptions(o Options) error {
	switch o.Engine {
	case "", EnginePodman, EngineDocker:
	default:
		return fmt.Errorf("podman: Options.Engine %q is neither %q nor %q", o.Engine, EnginePodman, EngineDocker)
	}
	switch o.Runtime {
	case "", "krun":
	default:
		return fmt.Errorf("podman: Options.Runtime %q is not supported: use \"\" or \"krun\" (crun-vm is unsupported because it boots VM disk images, not OCI images with an entrypoint)", o.Runtime)
	}
	if o.Runtime == "krun" && o.Engine == EngineDocker {
		return fmt.Errorf("podman: Options.Runtime %q needs podman, not docker", o.Runtime)
	}
	return nil
}

// newResolved is the provider for an already-resolved binary and engine; it
// looks nothing up and runs nothing.
func newResolved(o Options, bin string, engine Engine, r runner) *provider {
	o.Engine = engine
	env := o.EngineEnv
	if env == nil {
		env = os.Environ()
	}
	return &provider{opts: o, bin: bin, engine: engine, env: env, r: r, now: time.Now, defaultImage: sandbox.DefaultImage}
}

// Capabilities: a container (a micro-VM with krun), no network or the
// engine's default network, CPU, memory and PID bounds, an image, a
// workdir, and an agent that can apply Landlock.
func (p *provider) Capabilities() sandbox.Capabilities {
	iso := sandbox.IsolationContainer
	if p.opts.Runtime == "krun" {
		iso = sandbox.IsolationMicroVM
	}
	return sandbox.Capabilities{
		Provider:  string(p.engine),
		Isolation: iso,
		Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted},
		Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true},
		Image:     true,
		Workdir:   true,
		Landlock:  true,
		Notes: "there is no host allowlist: NetworkAllowlist is refused, and NetworkUnrestricted is the engine's default network; " +
			"DiskBytes is not bounded and is refused; a zero Resources field takes the engine's default; " +
			"/tmp is a memory-backed tmpfs that counts against MemoryBytes; " +
			"Spec.Env values appear on the engine's command line on the host, so credentials do not belong there; " +
			"the engine's default seccomp profile may withhold Landlock, which the agent then reports",
	}
}

// Reap is not implemented yet.
func (p *provider) Reap(context.Context) ([]string, error) {
	return nil, fmt.Errorf("podman: Reap: not yet implemented: %w", sandbox.ErrUnsupported)
}
