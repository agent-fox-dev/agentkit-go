---
spec_id: "14"
spec_name: "sandbox_rsync_sync"
title: "Sandbox LocalDir sync and export over the rsync protocol"
status: "active"
created_at: "2026-10-12T13:50:30.384097Z"
updated_at: "2026-10-12T13:50:30.384097Z"
intent_hash: "0450e9573b63c204052b364e19ef1211fa508fed6d20f40d56d41a1caf5aa38d"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Let an embedder fill a sandbox's workspace from a directory on the host, re-sync it incrementally, and bring the model's edits back into a host directory. Both directions apply the project's ignore rules the way AgentKit's own file tools do. Both ends of the transfer are a Go implementation of the rsync protocol (`github.com/gokrazy/rsync`), one in the host's `sandbox` code and one in the sandbox agent, and the bytes travel over the agent protocol. Nothing has to be installed on either side, and no part of the transfer may confine or otherwise change the process that embeds AgentKit.

## Goals

1. Over `local`, `Sync(ctx, sandbox.LocalDir{Path: src, Ignore: tools.NoGlobalExcludes()})` leaves the workspace holding exactly these entries:
   - every entry `tools.Walk` lists for `src` with `IncludeHidden: true`;
   - plus `src/.git` in full;
   - minus every symlink whose target leaves `src`.

   This holds for every tree a seeded generator produces. The generator covers nested `.gitignore` files, `!` re-includes, `.git/info/exclude`, a nested repository, hidden files, internal and escaping symlinks, and names containing `*`, `?`, `[` and spaces. The test runs with `PATH` emptied, so no `rsync` binary is involved.
2. Sync a 100-file, 16 KiB-per-file tree, change one file, and sync again. The second `Sync` reports `Files == 1`, `Bytes ==` that file's size, and a `Sent` less than one tenth of the first sync's `Sent`.
3. With `Delete`, a file removed from `src` is removed from the workspace. Without `Delete` it stays. With `Delete`, a workspace file under a path the workspace's own `.gitignore` ignores survives.
4. `Export(ctx, ".", sandbox.LocalDir{Path: dst})` brings back a file a proxy `write_file` created. During the export:
   - nothing is written outside `dst`;
   - nothing under `dst/.git` is created, changed or removed;
   - no symlink whose target leaves `dst` is created.

   With `Delete`, the export removes the files in `dst` that the workspace lacks, but keeps those that `dst`'s own ignore rules match.
5. The agent refuses an `Rsync` stream whose start frame names a path outside the workspace. It also refuses one whose argument frame carries an option the library does not know, or one this module never uses. Each refusal is `InvalidArgument`, and the sandbox stays live.
6. After a `LocalDir` `Sync` and `Export` over `local`, the process that ran them still behaves as before:
   - it can create a file outside the workspace;
   - on Linux, it can read a file under `/sys`;
   - its working directory and umask are unchanged.

   No call to `rsyncd.NewServer` in the module lacks `rsyncd.DontRestrict()`.
7. Beyond what `13_sandbox_core` added, the root module's direct requirements gain exactly `github.com/gokrazy/rsync v0.3.8`. It is approved in `approvedDeps` and ruled in `docs/DEPS.md`. No module already in `go.mod` changes version. `make check` stays green, including the cross-target gate, the host-cgo gate and TS-12-14's line budget.
8. The exported surfaces of `agentkit`, `core`, `tools` and `codemode` are unchanged.

## Non-goals

- **Other scopes of this split.** Each of these is its own spec:
  - `sandbox_landlock_agent`: the agent's own Landlock ruleset, `Spec.RequireLandlock`, the ABI in `Hello`, and the `go-landlock` ruling. This spec only makes sure the rsync library applies no Landlock of its own.
  - `sandbox_images`: the published images.
  - `sandbox_podman`: the container provider, `GitRepo` and its init step.
  - `sandbox_landlock_provider`: the Landlock-confined child-process provider.
  - `sandbox_gateway`: relaying the `Rsync` stream through a gateway. This spec only keeps the stream relayable.
- **`GitRepo` and any clone inside the sandbox.** They belong to `sandbox_podman`.
- **rsync's merge-file filter** (`--filter=':- .gitignore'`) as the way ignore rules are applied (Design Decision 1).
- **Syncing a `LocalDir` into a workspace subdirectory.** `Sync` always targets the workspace root. A subtree can still be synced as a `Tar` with `Path`.
- **Preserving owner, group, device nodes, FIFOs, sockets, hard links, xattrs or ACLs.** Protocol 27 has no xattrs or ACLs, and a source tree needs none of these.
- **Exposing the library's own transfer statistics or types** in the `sandbox` API.
- **Progress reporting, bandwidth limits, compression and resumable transfers.**
- **Full LocalDir support on Windows hosts and agents.** Windows must build; at run time it refuses `LocalDir` (steering: Linux and macOS required, Windows best effort).
- **Changing the driver, `core`, `tools` or `codemode`.**

## Background

**What the code has today.**
- `tools/ignore.go` is the module's definition of "what a workspace is". Its sources, lowest precedence first:
  - global excludes (`core.excludesFile`, then the XDG path);
  - `.git/info/exclude`;
  - every `.gitignore` from the scan root down.

  Its rules:
  - Deeper files win, and within a file the last match wins, which is how `!` re-includes.
  - A directory containing `.git` starts a new ignore root.
  - `hasGitComponent` always excludes `.git`.
  - `tools.NoGlobalExcludes()` gives an empty global layer, for hermetic use.
- `tools.Walk(ctx, ws, root, WalkOptions{Ignore, IncludeHidden}, fn)` (`tools/walk.go`) is the exported, workspace-confined walk over that engine:
  - It never follows symlinks.
  - It prunes ignored directories.
  - It skips unreadable entries without error.
- `tools.Workspace` (`tools/path.go`) resolves its root with `EvalSymlinks`.
- `internal/policy/deps_test.go` holds the direct-dependency allowlist `approvedDeps` (TS-09-16). TS-09-15 forbids `golang.org/x/net`, `x/image`, `x/time` and `code.dny.dev/ssrf` as direct requirements. `docs/DEPS.md` carries one ruling per module and a "Rejected" table.
- `internal/policy/crosstarget_test.go` builds and vets linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 with cgo off, and the host with cgo on.
- `core/size_test.go` (TS-12-14) caps the root module at 20,000 non-test lines of code, generated code included.

**What `13_sandbox_core` delivers.** It is written but not yet in the tree, and this spec builds on it:
- the `sandbox` API: `Source` and `Destination` as sealed interfaces implemented by `Tar` and `TarOut`, and `SyncResult{Files int; Bytes int64}`;
- the `agentkit.sandbox.v1` proto in `sandbox/internal/sandboxpb`:
  - five RPCs;
  - `ProtocolMinor = 0`;
  - `make proto` and a SHA-256 staleness file;
  - `MaxMessageBytes` of 16 MiB and 1 MiB data chunks;
- the agent in `sandbox/internal/agent`. It rebuilds its tool set after a successful `SyncTar`, because TS-02-39 shows the symbol table never revalidates on its own;
- the shared `Sandbox` implementation in `sandbox/internal/attach`. It has the live/unavailable/closed state machine, and `InvalidArgument`, `FailedPrecondition`, `NotFound` and `Unimplemented` count as deliberate refusals;
- `sandbox/internal/tarsafe`, whose `CleanRel` and symlink rule are shared by host and agent;
- `local.Unisolated()`, which runs the agent in-process over `os.Pipe` pairs;
- tests this spec must update:
  - TS-13-1 asserts exactly five RPCs;
  - TS-13-6 and the `Hello` test assert minor 0;
  - TS-13-65 compares `go list -m all` with the snapshot `internal/policy/testdata/go-list-m-all.txt` and the direct set with "pre-spec plus grpc and protobuf".

**What PRD 11 decided (D7, D8).**
- `LocalDir` is transferred with the rsync protocol through `gokrazy/rsync`:
  - `rsyncclient` on the host, which is always the client: the sender for `Sync`, the receiver for `Export`;
  - `rsyncd` in the agent, which is always the server, with the workspace as its one module.
- `rsyncd.NewServer` applies Landlock to the whole calling process whenever it has modules, unless it is given `rsyncd.DontRestrict()`. In `local` that would confine the embedder and its test binary.
- The library speaks protocol 27: no xattrs, no ACLs, MD4 checksums.

**Why this needs deciding rather than copying.** PRD 11 proposed the dir-merge rule `:- .gitignore`, but rsync's merge files do not mean what `.gitignore` means:
- a pattern containing a slash is not anchored the same way;
- `!` is handled differently;
- `.git/info/exclude` and global excludes are not read;
- a nested repository does not reset the rules.

PRD 11's own acceptance test demands agreement with the `tools` ignore engine. Protocol 27 also has no sender-only or receiver-only rule modifiers, so every rule both hides a path and protects it from deletion.

## Requirements

### 1. `LocalDir` and the sync result

Package `sandbox` adds the type below. It implements both `Source` and `Destination`.

```go
type LocalDir struct {
    Path           string
    Ignore         tools.IgnoreOptions
    IncludeIgnored bool
    ExcludeGit     bool
    Delete         bool
}
```

- **`Path`** is a host directory. A relative path resolves against the process's working directory, and symlinks in it are resolved once.
- **`Ignore`** configures the host-side ignore engine. The zero value reads the real environment, global excludes included, exactly as `tools.Options.Ignore` does.
- **`IncludeIgnored`** turns ignore rules off on both sides.
- **`ExcludeGit`** leaves `.git` behind on `Sync`. It has no effect on `Export`, which always leaves `.git` behind.
- **`Delete`** removes, on the receiving side, entries the sending side does not have.

`SyncResult` gains two fields, `Deleted int` and `Sent int64`. For a `LocalDir` sync, the four fields mean:
- `Files`: regular files the transfer created or changed in the workspace;
- `Bytes`: those files' content bytes;
- `Deleted`: entries removed under `Delete`;
- `Sent`: rsync data bytes the host wrote to the stream.

For a `Tar` sync, `Files` and `Bytes` keep their 13 meaning, `Deleted` is 0 (a `Replace` is not counted), and `Sent` is the number of tar bytes read from `R` and sent.

### 2. The `Rsync` RPC

- **The RPC.** The `Agent` service gains `rpc Rsync(stream RsyncFrame) returns (stream RsyncFrame)`.
- **Frames.** `RsyncFrame` is a `oneof` of:
  - `start`: direction (`SYNC` or `EXPORT`; unspecified is refused), workspace-relative path, `include_ignored`, `exclude_git`, `delete`;
  - `plan`: the agent's exclude rules, and whether an export path is a regular file;
  - `args`: the server-side argument list;
  - `data`: bytes;
  - `done`: `files`, `bytes`, `deleted`.
- **Version.** `ProtocolMinor` becomes 1.
- **Generated code.** `make proto` regenerates the checked-in code and the hash file. TS-13-1 now expects six RPCs, with `Rsync` bidirectional. Tests that pin minor 0 now pin 1.
- **Frame order.**
  1. The host sends `start`.
  2. The agent answers `plan`.
  3. The host sends `args`.
  4. `data` frames flow both ways.
  5. The agent sends `done` and ends the RPC with status OK.

  A frame out of this order is refused with `InvalidArgument`.
- **Chunking.** A `data` frame carries at most 1 MiB.
- **The stream as a connection.** On each end, the stream is wrapped as the `io.ReadWriteCloser` the library expects. Writes are serialised, and `Close` on the host half-closes the send side. On the host, a `done` frame ends reads with `io.EOF`.
- **No state outside the stream.** All state of a transfer travels on this one stream, so a gateway can relay it like any other stream.

### 3. The transfer set

Each side computes exclude rules over its own tree. The sender's tree is the source. The receiver's tree is the destination: the workspace for `Sync`, and the host directory for `Export` (when it exists).

- **Ignored entries.** These are the entries that `tools.Walk` with `IncludeHidden: true` does not visit. Each ignored directory is excluded once, at the highest ignored level. Paths with a `.git` component are never produced by this step. The host uses `LocalDir.Ignore`; the agent uses `tools.NoGlobalExcludes()`, as its tools do. There are no ignore rules when `IncludeIgnored` is set, or when the export path names a single file.
- **Escaping symlinks are always excluded.** These are symlinks whose target is absolute or, resolved lexically against the link's own directory, leaves the transfer root. This is the same rule as `tarsafe`'s.
- **`.git`.** On `Sync` with `ExcludeGit`, and always on `Export`, an unanchored `.git` rule is added, which excludes every `.git` directory or file at any depth. Otherwise `.git` is transferred whole.
- **Unreadable entries.** Entries the walk skips because they are unreadable end up excluded, rather than failing the transfer.
- **Combining.** The host takes the union of both sides' rules, removes duplicates and sorts them. Each rule is anchored at the transfer root, with rsync's wildcard characters escaped so that it matches only its own path. All rules go into the client's arguments as `--exclude` options.
- **Size cap.** If the combined rules exceed 8 MiB, the transfer is refused before any file data moves. The error names the size and the limit.
- **Effect.** A path that either side excludes is neither sent nor deleted.

### 4. Sync of a `LocalDir`

**Before any RPC,** `Sync(ctx, LocalDir{...})` checks the sandbox state as 13 does. It refuses, with an error naming the path, a `Path` that is empty, missing, or not a directory. It then walks the source.

**The transfer.**
- The client runs with `rsyncclient.WithSender()`.
- Its options are `-r -l -t -p`, with `--delete` added when `Delete` is set, plus the rules.
- The contents of `Path` map onto the workspace root.
- The agent serves the workspace as a writable module.

**After success,** the agent does three things:
- It computes the `done` statistics by comparing size and modification time of the regular files it saw before and after the transfer. Tool writes in the same window may be counted, and the documentation says so.
- If a tool set is configured, it rebuilds it with the same options, as after `SyncTar`.
- It sends `done`.

The host returns `SyncResult` with `Sent` from its own byte count.

**Other properties.**
- `Sync` may be called any number of times, before or after `Tools`.
- When `ctx` is cancelled, `Sync` returns an error wrapping `ctx.Err()`. Files already transferred stay, and the sandbox stays live.

### 5. Export to a `LocalDir`

**The path.**
- It goes through `tarsafe.CleanRel`.
- A path missing from the workspace is `NotFound`, and the error names it.
- A path that is a symlink is refused.
- A directory's contents map onto the destination.
- A regular file is written into the destination under its base name, ignoring the ignore rules. `Delete` with a file path is refused.

**The destination.** It must be an existing directory, or a missing directory whose parent exists; in that case it is created. A destination that exists but is not a directory is refused, and the error names it.

**The transfer.**
- The host is the receiver: the client runs without `WithSender`, with the same option set as `Sync`.
- The agent serves the workspace read-only.
- The host's own rules over the destination protect its ignored files, its `.git` and its escaping symlinks from being overwritten, written through, or deleted.

**Trust model.**
- Workspace content is untrusted.
- The agent binary is trusted.
- Host-side safety rests on two things: the agent's exclusion of escaping symlinks, and the host's own receiver-side rules.

These tests pin the export behaviour, using content planted through `execute`:
- an escaping symlink in the workspace is never created on the host;
- a workspace directory that shadows a host symlink pointing outside `dst` writes nothing outside;
- a `.git/hooks` or `.git/config` written in the sandbox never reaches `dst`.

### 6. The agent's `Rsync` service

**The `start` frame.** The agent validates it:
- The direction must be known.
- A `SYNC` path must be empty.
- An `EXPORT` path must pass `CleanRel` and exist.
- `delete` with a file path is refused.

An invalid start frame gets `InvalidArgument`, or `NotFound` for a missing export path.

**One stream at a time.** At most one `Rsync` stream runs per sandbox. A second one gets `FailedPrecondition`.

**The `args` frame.** The agent parses it with the library's own option parser.
- An option the parser does not know is refused with `InvalidArgument`, naming the option.
- A known option outside the set this module produces is refused the same way. That set is: recursion, links, times, permissions, delete, exclude rules, and the server and sender role flags.
- The sender flag must match the direction: present for `EXPORT`, absent for `SYNC`.
- Path operands may name only the module root or the start frame's path.

**The server.** Exactly one function in `sandbox/internal/rsyncx` constructs it, always with `rsyncd.DontRestrict()`. Its one module is the workspace, writable exactly when the direction is `SYNC`.

**Failures and lifetime.**
- A failure inside the library while the stream is intact becomes `FailedPrecondition`, carrying the library's message.
- The agent's TTL cancels an in-flight stream, as it does any RPC.
- On Windows the agent answers `Rsync` with `FailedPrecondition`.

### 7. Nothing confines or alters the embedding process

Building or running the rsync client or server changes no process-wide state:
- no Landlock;
- no chroot;
- no change of working directory, user, or umask.

An AST test over the root module's non-test files fails if any `rsyncd.NewServer` call is outside `sandbox/internal/rsyncx`, or lacks `rsyncd.DontRestrict()` among its arguments.

A behavioural test re-executes the test binary as a child, so that a regression confines only the child. The child runs a `LocalDir` `Sync` and `Export` over `local`, then checks:
- it can create a file in a fresh directory outside the workspace;
- on Linux, it can read a file under `/sys`;
- its working directory and umask are unchanged.

### 8. Failure handling and compatibility

**Without an RPC.** These cases fail before any stream opens:
- An unavailable or closed sandbox returns `ErrUnavailable` or `ErrClosed`, as 13 specifies.
- An agent whose `ProtocolMinor` is below 1 gets an error matching `ErrUnsupported` that names both versions. If such an agent is reached anyway, 13's `Unimplemented` handling still applies.
- On a Windows host, `Sync` and `Export` of a `LocalDir` return an error matching `ErrUnsupported` that names the platform.

**During a transfer.**
- A deliberate refusal from the agent is returned as an error, and the sandbox stays live.
- A gRPC transport failure makes the sandbox unavailable, as 13 specifies.
- A host-side library error cancels the stream and is returned wrapped. The sandbox stays live.
- A stream that ends with OK but no `done` frame is a malformed response, and so a transport failure.
- There is no retry.

### 9. Dependency, ruling and gates

**`go.mod` and the allowlist.**
- `github.com/gokrazy/rsync v0.3.8` becomes a direct requirement, and its requirements are added as indirect ones.
- No version of a module already in `go.mod` changes. The regenerated `go-list-m-all.txt` snapshot differs from 13's only by added lines.
- TS-13-65's expected direct set gains `github.com/gokrazy/rsync`.
- `approvedDeps` gains the module, with a one-line reason comment.
- `golang.org/x/net` stays out of the direct set (TS-09-15).
- `go-landlock` stays indirect in this scope.

**The `docs/DEPS.md` ruling** records:
- who uses the module (`sandbox/internal/rsyncx`, the agent and `attach`);
- the version;
- why this module: both ends in-process, delta transfer, nothing to install, and the same code path through a gateway;
- what is pulled in, with `go mod why -m` for each new module, saying which ones are linked;
- the protocol 27 limits;
- the rule that every server is built with `DontRestrict()`.

The "Rejected" table gains a row for an external `rsync` binary.

**Build gates.**
- Files that import the library carry `//go:build !windows`; Windows gets stubs.
- The cross-target gate, the host-cgo gate (with no new environment variables) and TS-12-14 pass.
- If TS-12-14 does not fit, 13's Design Decision 5 applies: move the tree into a nested module rather than raise the budget.

### 10. Documentation and example

Per steering, run `make doc`, then update:
- `README.md`: the Sandboxes section shows `LocalDir` sync and export.
- `docs/configuration.md`:
  - the `LocalDir` fields and their meaning in each direction;
  - the `SyncResult` fields;
  - the ignore and `.git` rules;
  - the warning that `IncludeIgnored` with `Delete` on `Export` removes host-ignored files such as `.env`;
  - the note that a `.git/config` with a token in a remote URL travels into the sandbox unless `ExcludeGit` is set.
- `docs/architecture.md`:
  - the `Rsync` stream and its frame order;
  - the transfer-set rule;
  - the trust model;
  - the invariant "no code in this module confines the process that embeds it".
- `docs/DEPS.md`: the ruling above.

`examples/sandbox` switches its `Sync` and `Export` from tar to `LocalDir`, and its test stays offline and green.

## Design Decisions

1. **Ignore rules are expanded path by path with the `tools` ignore engine, not applied by rsync's `:- .gitignore` merge rule.** Merge-file semantics differ from gitignore's: anchoring, `!`, `.git/info/exclude`, global excludes and nested repositories. Agreement with the engine, which PRD 11's own test demands, can only hold if the engine decides.
2. **Each side computes rules over its own tree, and the union applies to both sending and deletion.** Protocol 27 has no sender-only or receiver-only rule modifiers. Receiver-side rules are what keep `Delete` from wiping ignored files such as `.env` or a sandbox build directory. The result, "a path ignored on either side is left alone", is conservative and easy to state.
3. **The stream opens with a start → plan → args handshake.** The agent's rules have to reach the client's arguments, so that every rule takes the library's ordinary client-filter path. Keeping all state on one stream keeps it relayable by the gateway.
4. **Raw server arguments are allowed, but checked against the library's parser and an allowlist of options this module uses.** This follows PRD 11's design and this scope's explicit refusal of unknown options. It adds least privilege: a known but unused option such as one that deletes source files is refused too.
5. **The options are `-r -l -t -p` (plus `--delete`), not `-a`.** Owner, group and device nodes mean nothing in a workspace, and `-t` is what makes the quick check, and so incremental re-syncs, work.
6. **Escaping symlinks are never transferred in either direction, by the same rule as tar.** One symlink rule for the whole sandbox. It also means the host never gains a symlink pointing out of its destination.
7. **`.git` is sent on `Sync` by default (PRD 11), but never exported.** Git metadata written by an untrusted process can run code on the host the next time git runs there, through hooks or `core.fsmonitor`. An embedder who wants the model's commits can export a bundle file the model made, or a tar.
8. **Sync statistics come from the agent comparing size and modification time before and after, not from the library's statistics type.** Under the quick check a transferred file always changes one of the two, so the count is exact. This keeps a third-party type out of the public API, and does not depend on a statistics surface that could not be verified.
9. **A library failure while the stream is intact leaves the sandbox live.** Only gRPC transport failures mean the agent is gone, which is 13's definition of unavailable.
10. **At most one `Rsync` stream at a time per sandbox.** Two receivers writing into one workspace would interleave unpredictably, and before-and-after statistics would be meaningless.
11. **`DontRestrict()` on every server, from a single constructor, pinned by an AST test and by a child-process test.** Landlock is irreversible. A regression must fail one clear test instead of confining the rest of the test binary or an embedder. Working directory and umask are checked too, because an in-process library that changes them breaks the embedder in the same silent way.
12. **Library-importing files are `!windows`, with stubs that refuse at run time.** Steering requires Linux and macOS but only best effort on Windows. The cross-target gate must still build windows/amd64, and the library's Windows support is unverified.
13. **`ProtocolMinor` becomes 1, and the host checks it before opening the stream.** 13 requires a minor bump for a new RPC. The pre-check avoids walking a large source tree only to hit `Unimplemented`.
14. **`LocalDir` gains `Ignore tools.IgnoreOptions`, while the agent uses `NoGlobalExcludes`.** The host should honour the developer's global excludes, as `find_files` does. Tests need a hermetic option. Inside the sandbox there is no user configuration to honour.
15. **Export semantics.** A directory's contents go into the destination. A single file goes in under its base name, regardless of ignore rules, and `Delete` with a file is refused. A symlink path is refused. The destination is created only when its parent exists. This mirrors 13's tar export and catches mistyped destinations.
16. **The combined rules are capped at 8 MiB.** That is half of `MaxMessageBytes`, so both the plan and the args frames fit, and it fails clearly rather than at the transport.
17. **`gokrazy/rsync` is pinned at v0.3.8, and existing versions must not move.** That is the version PRD 11 examined, and the snapshot diff makes "nothing else changed" checkable.
18. **Trust model: the agent binary is trusted, workspace content is not.** The agent is read-only image code that its children cannot easily tamper with. The host cannot parse rsync's file list itself, so its protection is its own receiver rules plus tests with planted content.
19. **Unreadable source entries are excluded, not fatal.** This matches `tools.Walk`. A sync should not fail on one root-owned build artifact.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | depends on, modifies | Builds on its `sandbox` API, `sandboxpb`, the agent, `attach`, `tarsafe`, `local` and its state rules. This spec makes these changes to it: adds the `Rsync` RPC and `ProtocolMinor = 1`; adds `LocalDir` to the sealed `Source` and `Destination`; adds `SyncResult.Deleted` and `SyncResult.Sent`, with `Sent` filled for tar; extends the post-sync tool-set rebuild to rsync; switches `examples/sandbox` to `LocalDir`; and updates TS-13-1, the minor-version assertions and TS-13-65's snapshot and direct set. |
| `09_repository_cut` | modifies | Its direct-dependency allowlist (`approvedDeps`, TS-09-16) gains `github.com/gokrazy/rsync`; TS-09-15 must stay green. |
| `12_core_schema_guard` | depends on | TS-12-14's 20,000-line budget is a constraint this scope must meet. |

## Verified External API

**In-repository symbols (read):**
- `tools.Walk(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error`. It refuses a nil `ws` or a root outside it, never follows symlinks, prunes ignored directories and skips unreadable entries.
- `tools.WalkOptions{Ignore IgnoreOptions; IncludeHidden bool}`.
- `tools.IgnoreOptions{Getenv func(string) string; Home func() (string, error); GitConfig func() string}`. The zero value reads the real environment.
- `tools.NoGlobalExcludes() IgnoreOptions`.
- `tools.NewWorkspace(root string) (*Workspace, error)`.

**From `13_sandbox_core` (specified, not yet in the tree):**
- `sandbox.SyncResult{Files int; Bytes int64}`, the sealed `sandbox.Source` and `sandbox.Destination`, `sandbox.ErrUnsupported`, `ErrUnavailable`, `ErrClosed`, `MaxMessageBytes`, `ProtocolMinor`;
- `tarsafe.CleanRel(p string) (string, error)`;
- `agent.Config{Workdir string; Env []string; TTL time.Duration}`;
- the `attach` package and `local.Unisolated()`.

**`github.com/gokrazy/rsync` v0.3.8.** Not in `go.mod` today. Every signature below is **unverified**, because the module source is outside the repository; they are the ones PRD 11 implies:
- `rsyncclient.New(args []string, opts ...Option) (*Client, error)`;
- `rsyncclient.WithSender() Option`;
- `(*Client).Run(ctx context.Context, conn io.ReadWriteCloser, paths []string)`, which returns an error (any other return values unverified);
- `ServerCommandOptions`, which produces the server-side argument list for a client configuration. Whether it is a function or a method, and its exact signature, are unverified;
- `rsyncd.NewServer(modules []Module, opts ...Option) (*Server, error)`;
- `rsyncd.DontRestrict() Option`;
- `rsyncd.Module{Name string; Path string; Writable bool}`;
- `rsyncd.NewConnection(r io.Reader, w io.Writer, name string)`, which returns a connection value;
- `(*Server).HandleConnArgs(ctx context.Context, conn, module, args []string) error`;
- the library's option parser, used to recognise known options. Assumed to recognise `-r -l -t -p`, `--delete`, `--exclude`, `--filter` and `--include`. Whether it honours `--exclude` on the sending side and `--delete` on the receiving side is unverified; Goals 1, 3 and 4 test both.

**`github.com/landlock-lsm/go-landlock` and `kernel.org/pub/linux/libs/security/libcap/psx`.** Pulled in indirectly through `rsyncd`; not called by this spec. **Unverified**: whether psx's cgo variant links under the host-cgo gate without extra `CGO_LDFLAGS_ALLOW`.

**`google.golang.org/grpc` v1.64.1.** **Unverified**, because the source is outside the repository. Code generated by `protoc-gen-go-grpc` v1.5.1 is assumed to expose a bidirectional stream as `grpc.BidiStreamingServer[RsyncFrame, RsyncFrame]` (`Send`, `Recv`) and `grpc.BidiStreamingClient[RsyncFrame, RsyncFrame]` (`Send`, `Recv`, `CloseSend`).

**Standard library** (Go 1.27). **Unverified**: `syscall.Umask(mask int) int` (Unix), `os.Getwd`, `path/filepath.WalkDir`, `io/fs.DirEntry`.
