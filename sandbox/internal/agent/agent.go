// Package agent is the AgentKit side of a sandbox: it serves the
// agentkit.sandbox.v1 Agent service over one connection and runs the
// built-in tools against one workspace, with an explicit environment.
// agentkit-sandboxd wraps it as a command; the local provider runs it
// in-process.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// Config is what an agent is built from.
type Config struct {
	// Workdir is the workspace directory. It must exist.
	Workdir string
	// Env is the tools' environment, verbatim. The agent never uses its
	// process's own.
	Env []string
	// TTL is how long the agent serves, measured from New.
	TTL time.Duration
	// Landlock is the outcome of the caller's Landlock confinement, which
	// Hello reports. It is data: this package never applies Landlock, since
	// the local provider runs it inside the embedder's process.
	Landlock sandbox.LandlockInfo
}

// Agent serves the Agent service. Build one with New.
type Agent struct {
	sandboxpb.UnimplementedAgentServer

	root  string // the workspace, symlinks resolved
	env   []string
	ttl   time.Duration
	start time.Time
	// build makes the tool set; tests replace it.
	build func(*workspace.Workspace, tools.Options) ([]core.Tool, error)

	landlock sandbox.LandlockInfo

	mu  sync.RWMutex
	set *toolSet // nil until the first successful Tools RPC

	// rsync is the one Rsync stream slot.
	rsync rsyncSlot
}

// New checks cfg and returns an agent. The workspace must already exist and
// be a directory.
func New(cfg Config) (*Agent, error) {
	fi, err := os.Stat(cfg.Workdir)
	if err != nil {
		return nil, fmt.Errorf("agent: workspace %s: %w", cfg.Workdir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("agent: workspace %s is not a directory", cfg.Workdir)
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("agent: TTL must be positive")
	}
	root, err := filepath.EvalSymlinks(cfg.Workdir)
	if err != nil {
		return nil, fmt.Errorf("agent: workspace %s: %w", cfg.Workdir, err)
	}
	env := append([]string{}, cfg.Env...) // non-nil: nil would mean the process's environment
	return &Agent{root: root, env: env, ttl: cfg.TTL, start: time.Now(), build: tools.For, landlock: cfg.Landlock}, nil
}
