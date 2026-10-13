package sandbox

import (
	"context"
	"io"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// Provider creates sandboxes.
type Provider interface {
	// Capabilities describes what the provider isolates and honours.
	Capabilities() Capabilities
	// Create checks spec with CheckSpec, then creates and attaches to a
	// sandbox. ctx bounds creation only, not the sandbox's lifetime.
	Create(ctx context.Context, spec Spec) (Sandbox, error)
	// Reap removes sandboxes left behind by processes that have gone, and
	// returns their ids.
	Reap(ctx context.Context) ([]string, error)
}

// Sandbox is one live sandbox: one workspace and one agent. It is live, then
// unavailable (after a transport failure, when the agent ends, or at the
// TTL), then closed. Once unavailable or closed, Tools, Sync and Export
// return ErrUnavailable or ErrClosed without contacting the agent, and every
// proxy tool returns sandbox_unavailable.
type Sandbox interface {
	ID() string
	// Spec is the effective spec: defaults applied, and Workdir the
	// workspace root the agent reported.
	Spec() Spec
	// Agent is what the agent reported when the sandbox was created.
	Agent() AgentInfo
	// Sync puts files into the workspace. A refused or malformed tar entry
	// ends the sync with ErrUnsafeArchive; entries extracted before it stay
	// in the workspace. A sync has no ordering guarantee with tool calls
	// running at the same time.
	Sync(ctx context.Context, src Source) (SyncResult, error)
	// Tools configures the agent's tool set and returns one proxy tool for
	// each tool tools.All returns, in the same order. The options are fixed
	// by the first call: a later call with different options fails with
	// ErrToolsConfigured. Tools fails with ErrToolMismatch when the agent's
	// tool definitions differ from the host's.
	Tools(ctx context.Context, opts ToolOptions) ([]core.Tool, error)
	// Export writes a workspace subtree to dst, after checking every entry
	// with the same rules as Sync; the first refused entry stops it with
	// ErrUnsafeArchive. An export has no ordering guarantee with tool calls
	// running at the same time.
	Export(ctx context.Context, path string, dst Destination) error
	// Close destroys the sandbox. It is idempotent: a later Close returns
	// nil.
	Close(ctx context.Context) error
}

// AgentInfo is what the agent reports in its Hello.
type AgentInfo struct {
	// Version is the agent's module version, or (devel).
	Version       string
	ProtocolMajor uint32
	ProtocolMinor uint32
	// Workdir is the workspace root, symlinks resolved.
	Workdir string
	// OutlineBackend is the outline backend for non-Go files: tree-sitter
	// or none.
	OutlineBackend string
	// Git reports whether git resolves on the tools' PATH.
	Git bool
}

// ToolOptions configures the agent's tool set; see tools.Options.
type ToolOptions struct {
	Symbols tools.SymbolOptions
	// SpillDir is an absolute path inside the sandbox. Empty means
	// agentkit-spill under the TMPDIR of the tools' environment.
	SpillDir     string
	DisableSpill bool
}

// SyncResult reports what a Sync wrote.
type SyncResult struct {
	// Files is the number of regular files written.
	Files int
	// Bytes is the number of bytes of file content written.
	Bytes int64
}

// Source is what Sync reads from. This package provides Tar.
type Source interface{ isSource() }

// Destination is what Export writes to. This package provides TarOut.
type Destination interface{ isDestination() }

// Tar is a tar stream to extract into the workspace. Path is the
// workspace-relative, slash-separated directory to extract into; "" and "."
// mean the workspace root. With Replace, the existing contents of Path are
// removed first and Path itself is kept, or created. Entries are moved
// verbatim: no ignore filtering.
type Tar struct {
	R       io.Reader
	Path    string
	Replace bool
}

func (Tar) isSource() {}

// TarOut receives a tar stream of an exported subtree, with entry names
// relative to the exported path.
type TarOut struct {
	W io.Writer
}

func (TarOut) isDestination() {}
