# ADR 05: Keep the sandbox in its own module and sync through a filtered view

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken between 2026-07 and 2026-09)

## Context

The sandbox specs (the core API and protocol, the rsync transfer, Landlock
inside the agent, the images, and the Podman and Landlock providers) assumed
that the sandbox lived in the root module and that `gokrazy/rsync` could
filter and protect a transfer. Neither held: the sandbox's dependencies (gRPC,
protobuf, rsync, go-landlock) did not belong in every embedder's build, and
reading `gokrazy/rsync` v0.3.8 showed that it cannot carry the specified
design. The decisions taken with the project owner, and the limits found
while testing on real engines, were recorded in errata. The errata are gone;
the rules the code follows are here.

## Decision

**The sandbox tree is its own module.** `sandbox/` has its own `go.mod`
(`github.com/agent-fox-dev/agentkit-go/sandbox`), requiring the root module
through `replace ../`; import paths are unchanged, and gRPC and protobuf are
its direct requirements, not the root's. `examples/sandbox` has its own module
for the same reason.

- The agent (`agentkit-sandboxd`) is built from the sandbox module; the image
  build context is the repository root, which the `replace` needs.
- `buildver.Version()` reports the sandbox module's version, and
  `sandbox.DefaultImage()` turns it into the image tag. Go resolves that
  module's `vX.Y.Z` from the tag `sandbox/vX.Y.Z`, so a release tags the root
  and the sandbox module at the same commit with the same version
  (`docs/releasing.md`).
- The image build also stamps `buildver.commit` and `buildver.buildTime`;
  `buildver.Build()` returns them and `agentkit-sandboxd --version` appends
  them when set.
- There is one base image (`containers/agentkit-sandbox/`, built with
  `make build-containers`) and no overlay image: an embedder builds its own
  image `FROM` the base. The agent lives at
  `sandbox.AgentPath`, `/usr/local/bin/agentkit-sandboxd`. `ErrNoAgent` names
  the image and that path and says to build `FROM` `sandbox.DefaultImage()`.

**The rsync library never sees a rule or `--delete`.** `gokrazy/rsync` panics
on a wildcard rule, matches rules by string equality, skips a file's later
siblings on a match, deletes on the receiver without consulting any filter,
and confines the whole process with Landlock unless told not to. So:

- The sending side always serves a filtered tree: an `rsyncd` server whose one
  module is a read-only `fs.FS` in which every excluded path does not exist.
  The receiving side runs an `rsyncclient` receiver writing through an
  `os.Root`. On Sync the host serves and the agent receives; on Export the
  agent serves and the host receives.
- Rules are workspace- or destination-relative paths, matched exactly as the
  path or an ancestor of it, plus the `.git` rule, which matches a `.git` path
  component at any depth; wildcard characters in names are literal.
- The `args` frame keeps the agent's side in rsync server-argument syntax, and
  carries the combined `rules` and, on Sync with delete, the host's transfer
  set (`present`); `RsyncDone` carries `present` for Export with delete.
- Deletion is AgentKit's own code on the receiving side: after the transfer it
  removes the entries no rule covers and the sender's set lacks, through an
  `os.Root`, never following a symlink.
- `rsyncd.NewServer` and `rsyncclient.New` are each called in exactly one
  function of `sandbox/internal/rsyncx`, always with `DontRestrict()`. The
  host is never confined; go-landlock is in every provider's import graph
  through `rsyncd`, which applies a ruleset only when a module asks for one.
- A symlink that `tarsafe.SymlinkEscapes` rejects is hidden at the moment it is
  served, so a link a sandboxed process creates after the plan cannot be
  exported.
- An export of a subdirectory takes its rules from a walk of the whole
  workspace, re-rooted at the export path, so the root's ignore files apply
  to it; an export path the workspace's rules ignore was named explicitly and
  takes only the ignore files inside it.
- The agent runs one rsync stream at a time. A stream that finds the slot
  held by a stream whose context is done waits for its release, up to 5
  seconds; a live holder is refused at once.

**Landlock in the agent.**

- A relative `--workdir` is made absolute against the working directory
  before it is checked, so `--workdir .` from `/` is refused like `/`.
- `SpecError.Error` omits `= value` for an error built outside `CheckSpec`
  (attach's `RequireLandlock` refusal), where the value is unexported.
- The Landlock provider's default `Root` (`<UserCacheDir>/agentkit-sandbox-landlock`)
  is refused after `Hello` where `HOME` is beneath a read-only system root such
  as `/opt`; such hosts need an explicit `Root`.

**The Podman and Docker provider.**

- The host-side TTL counts from the expiry label's creation time: `Create`
  gives attach `TTL - (now - created)`, so the destroy step runs at the label's
  expiry, and another process's `Reap` never removes a live container.
- Engine output is read among stderr lines: the version parser takes the first
  line beginning `podman version` or `Docker version`, `Reap` skips listing
  lines that are not a single word, and inspect output is read from its JSON
  line.
- If the sandbox is closed while a `GitRepo` init step runs, the provider
  removes the volume again after the step and returns the liveness error, so
  no unlabelled volume is left.
- Podman sets `HOSTNAME` (the container's id) even with `--unsetenv-all`, and
  its `--read-only` also mounts tmpfs at `/run`, `/var/tmp` and `/dev/shm`;
  the provider's fixed flag list cannot remove either, and the agent's
  ruleset keeps the extra mounts read-only for the tools.
- **A limit left open:** under memory pressure the cgroup OOM killer can pick
  `agentkit-sandboxd` over the tool process that exceeded `MemoryBytes`,
  making the sandbox unavailable. Every process in the container has the same
  `oom_score_adj`; the fix is for the agent to raise its tools'.

**`sandbox.Open`** returns the live sandbox and its proxy tools and discards
the `SyncResult` of its initial sync.

## Consequences

- An embedder that does not use sandboxes links none of gRPC, protobuf, rsync
  or go-landlock.
- A change to the transfer is made in AgentKit's filter, rules and deletion
  code, never by passing rules or `--delete` to the library; a newer
  `gokrazy/rsync` does not change that without a decision of its own.
