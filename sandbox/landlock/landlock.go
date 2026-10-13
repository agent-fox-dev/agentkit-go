// Package landlock is a sandbox provider for Linux hosts whose kernel offers
// Landlock ABI 4 (Linux 6.7) or later. It runs agentkit-sandboxd as a child
// process of the embedder, over pipes, and the agent's own Landlock ruleset
// is the only boundary: a private workspace, HOME and TMPDIR, read and
// execute on the system directories, and no TCP under NetworkNone. It needs
// no container engine, no image and no privileges.
//
// It is a confinement, not an isolation: it shares the host kernel, memory,
// CPU and process table, bounds no resources, and leaves UDP, Unix sockets
// and signals unrestricted. Its Capabilities say so. On any other platform,
// and on an older kernel, New refuses with ErrNoLandlock and names
// sandbox/podman, the provider for untrusted code. The package never
// confines the embedder's own process.
package landlock

import (
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Options configures the provider.
type Options struct {
	// Root holds one directory per sandbox. Empty means
	// <os.UserCacheDir()>/agentkit-sandbox-landlock.
	Root string
	// AgentBinary is the agentkit-sandboxd to run. Empty means the file
	// agentkit-sandboxd beside os.Executable(), then the one on PATH.
	AgentBinary string
	// ExtraRO are absolute directories that the agent, and every process a
	// tool starts, may read and execute beneath (--landlock-ro).
	ExtraRO []string
	// ExtraRW are absolute directories they may also write beneath
	// (--landlock-rw).
	ExtraRW []string
}

// ErrNoLandlock: this host cannot confine a sandbox with Landlock ABI 4. New
// wraps it for another platform, an older kernel or a failing probe, and
// Create for an agent whose report falls short; an embedder can fall back to
// sandbox/podman on it.
var ErrNoLandlock = errors.New("landlock: this host cannot confine a sandbox with Landlock ABI 4")

// seams are what offline tests replace; each provider holds its own, read
// only after New.
type seams struct {
	goos       string
	probe      func() (int, error)
	runVersion func(bin string) (string, error)
	executable func() (string, error)
	lookPath   func(string) (string, error)
	// resolver is the resolver directory for the resolv.conf path given.
	resolver func(path string) string
	procs    procTable
	start    func(req startReq) (*agentProc, net.Conn, error)
	remove   func(dir string) error
}

// startReq is what the starter starts: bin with args and env in dir.
type startReq struct {
	bin       string
	args, env []string
	dir       string
}

// provider is the landlock provider.
type provider struct {
	seams            seams
	root             string // absolute, symlink-free
	bin              string // the agent binary
	extraRO, extraRW []string
}

// New returns the provider after checking, in order and stopping at the
// first failure: the platform, the kernel's Landlock ABI (probed once), Root,
// ExtraRO and ExtraRW, and the agent binary and its --version. The version
// check is the only process it starts.
func New(o Options) (sandbox.Provider, error) {
	p, err := newProvider(o, defaultSeams())
	if err != nil {
		return nil, err
	}
	return p, nil
}

// newProvider is New over s.
func newProvider(o Options, s seams) (*provider, error) {
	if s.goos != "linux" {
		return nil, fmt.Errorf("%w: GOOS %s has no Landlock; use sandbox/podman", ErrNoLandlock, s.goos)
	}
	abi, err := s.probe()
	if err != nil {
		return nil, fmt.Errorf("%w: probing Landlock: %w%s", ErrNoLandlock, err, probeHint(err))
	}
	if abi < sandbox.LandlockTCPABI {
		return nil, fmt.Errorf("%w: kernel Landlock ABI %d; this provider needs ABI %d (Linux 6.7) or later; use sandbox/podman",
			ErrNoLandlock, abi, sandbox.LandlockTCPABI)
	}
	root, err := resolveRoot(o.Root)
	if err != nil {
		return nil, err
	}
	extraRO, err := checkExtra("ExtraRO", o.ExtraRO, root)
	if err != nil {
		return nil, err
	}
	extraRW, err := checkExtra("ExtraRW", o.ExtraRW, root)
	if err != nil {
		return nil, err
	}
	bin, err := checkAgent(o, s)
	if err != nil {
		return nil, err
	}
	return &provider{seams: s, root: root, bin: bin, extraRO: extraRO, extraRW: extraRW}, nil
}

// probeHint explains a probe errno.
func probeHint(err error) string {
	switch {
	case errors.Is(err, syscall.ENOSYS):
		return " (the kernel has no Landlock)"
	case errors.Is(err, syscall.EOPNOTSUPP):
		return " (Landlock is not enabled in lsm=)"
	case errors.Is(err, syscall.EPERM):
		return " (the call is blocked, typically by a container's seccomp filter)"
	}
	return ""
}
