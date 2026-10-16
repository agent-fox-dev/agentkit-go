package sandbox

import (
	"context"
	"io"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
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
	// each tool tools.For returns, in the same order. The options are fixed
	// by the first call: a later call with different options fails with
	// ErrToolsConfigured. Tools fails with ErrToolMismatch when the agent's
	// tool definitions differ from the host's.
	Tools(ctx context.Context, opts ToolOptions) ([]core.Tool, error)
	// Export writes a workspace subtree to dst. For a TarOut, every entry is
	// checked with the same rules as Sync; the first refused entry stops it
	// with ErrUnsafeArchive. For a LocalDir, path is a workspace directory,
	// whose contents map onto dst, or a regular file, written as
	// dst/<base name>. Workspace content is untrusted and the agent binary
	// trusted: the agent never sends an escaping symlink or .git, and the
	// host's own rules over dst keep dst's ignored files, its .git and its
	// escaping symlinks from being overwritten, written through or deleted.
	// An export has no ordering guarantee with tool calls running at the same
	// time.
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
	// Landlock is the Landlock ruleset the agent applied to itself, from
	// Hello. An agent older than protocol 1.2 reports none: ABI 0 and the
	// note "agent protocol 1.N does not report Landlock".
	Landlock LandlockInfo
}

// ToolOptions configures the agent's tool set; see tools.Options.
type ToolOptions struct {
	Symbols tools.SymbolOptions
	// SpillDir is an absolute path inside the sandbox. Empty means
	// agentkit-spill under the TMPDIR of the tools' environment.
	SpillDir     string
	DisableSpill bool
}

// SyncResult reports what a Sync did.
//
// For a LocalDir, Files counts the regular files the transfer created or
// changed in the workspace and Bytes their content bytes, from the agent
// comparing size and modification time before and after (a tool writing in
// the same window may be counted); Deleted counts the entries removed under
// Delete; Sent is the rsync data bytes the host wrote to the stream.
//
// For a Tar, Files and Bytes are the regular files written and their content
// bytes, Deleted is 0 even when Replace removed entries, and Sent is the tar
// bytes read from R and sent.
type SyncResult struct {
	Files   int
	Bytes   int64
	Deleted int
	Sent    int64
}

// Source is what Sync reads from. This package provides Tar, LocalDir and
// GitRepo; a provider without an init step refuses a GitRepo with
// ErrUnsupported.
type Source interface{ isSource() }

// Destination is what Export writes to. This package provides TarOut and
// LocalDir.
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

// GitRepo is a public git repository cloned into the workspace by the
// provider's init step. It is a Source. A provider with an init step
// accepts it only as the first Sync and before the first Tools call, and
// reports a SyncResult of zeros; a provider without one refuses it with an
// error matching ErrUnsupported. A private repository is cloned on the host
// and synced as a LocalDir.
type GitRepo struct {
	URL   string // https only
	Ref   string // a branch or tag; empty means the remote's default branch
	Depth int    // > 0: shallow clone of that depth; 0: full history
}

func (GitRepo) isSource() {}

// LocalDir is a host directory: a Source that Sync fills the workspace from,
// and a Destination that Export writes into. The bytes travel over the rsync
// protocol, and only what changed moves. Both directions apply the same
// ignore rules AgentKit's own file tools do, never transfer a symlink whose
// target leaves the transfer root, and never export .git. Windows refuses
// LocalDir with ErrUnsupported.
//
// Trust model: workspace content is untrusted and the agent binary is
// trusted. On Export the host's safety rests on two things: the agent never
// sends an escaping symlink, and the host's own rules over the destination
// keep its ignored files, its .git and its escaping symlinks from being
// overwritten, written through or deleted.
type LocalDir struct {
	// Path is the host directory. A relative path resolves against the
	// working directory at call time, and symlinks in it are resolved once.
	// On Sync it must be an existing directory; on Export it must be one, or
	// be missing with an existing parent, and is then created.
	Path string
	// Ignore configures the host's ignore engine. The zero value reads the
	// real environment, global excludes included, exactly as the zero
	// workspace.Options.Ignore does; workspace.NoGlobalExcludes() is hermetic,
	// and ws.Ignore() passes a workspace's own, memoized options on. The agent
	// always uses workspace.NoGlobalExcludes().
	Ignore workspace.IgnoreOptions
	// IncludeIgnored turns ignore rules off on both sides.
	IncludeIgnored bool
	// ExcludeGit leaves .git behind on Sync. Export always leaves .git
	// behind.
	ExcludeGit bool
	// Delete removes, on the receiving side, the entries the sending side
	// does not have, except what either side's rules exclude.
	Delete bool
}

func (LocalDir) isSource()      {}
func (LocalDir) isDestination() {}

// TarOut receives a tar stream of an exported subtree, with entry names
// relative to the exported path.
type TarOut struct {
	W io.Writer
}

func (TarOut) isDestination() {}
