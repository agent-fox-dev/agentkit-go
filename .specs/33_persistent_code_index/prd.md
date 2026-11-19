---
spec_id: "33"
spec_name: "persistent_code_index"
title: "Keep the code search index across processes"
status: "active"
created_at: "2026-11-19T15:50:22Z"
intent_hash: "53c73bd0f9d94781ee6fc7092d1d8fe8596f7d7489689544a7e4cef3dcc53d5d"
contract_hash:
  requirements.json: "b9b13ffb347d882d198bc272f9556fff4bb5265c542c1d80b6df4890c2c0d722"
  tasks.json: "1350afa7107a2cbb52b589242cd35657d0ee518ec5c1d5e30b8b20c658f1dadb"
schema_version: 3
source: "docs/prd/15-serve-the-tools-from-one-command-and-drop-the-sandbox.md"
---
# Keep the code search index across processes

## Intent

A caller that starts one `agentkit-tools` process per tool call must not pay
for a code search index on every call. The index and its symbol data are
kept on disk, outside the served directory, where the caller asks for them,
so a later process on the same directory reuses them and still answers as a
fresh build of the current tree would. An embedder that does not ask keeps
today's index, which lives and dies with its process.

## Goals

1. A second process on the same directory answers `code_search` and
   `find_symbol` from the index the first one built, without building again,
   and sees every file change made since.
2. The library offers the persistent directory as an option; with the option
   unset nothing changes.
3. `agentkit-tools` takes the directory from `--index-dir` or
   `AGENTKIT_INDEX_DIR`, has no default location, and refuses one inside the
   served directory.
4. Two processes at once never read a half-written index, and only one of
   them builds.
5. On a generated tree of 10 000 source files, a `code_search` single call
   that finds a complete index and no changed file takes at most one fifth
   of the wall time of the call that built it, on the same machine.

## Non-goals

- The `agentkit-tools` command itself, its modes and its other flags: spec
  32 (`tool_server_command`).
- Removing the sandbox module: spec 31 (`sandbox_removal`).
- A default index location, in the library or the command (PRD 15 D6).
- Writing the changes a process revalidated back into the stored index. The
  stored index is replaced only by a build or by the rebuild the dirty
  threshold triggers today.
- Indexing from a git tree instead of a directory.
- Sharing an index between machines, or between two served directories.
- Changes to `code_search`'s or `find_symbol`'s arguments, results or
  descriptions.

## Background

`codesearch` (its own module on zoekt) builds the `code_search` index on the
first `code_search` call. `codesearch.New(ws, Options)` touches no disk;
`Options` has `MaxFiles`, `MaxBytes`, `MaxBuildTime` (60 s by default; a
build can run 1.25 times it) and `TempDir`. A build writes zoekt shards into
a run directory, `<TempDir>/agentkit-codesearch-<sha256 of the workspace
root>/<run id>`, and keeps in memory what the shards do not hold: the set of
indexed files, an `outline.File` per file for symbols, the size, modification
time and indexing time of each file, the build statistics, and whether the
build was partial and why. `Index.Close` deletes the run directory. A build
stopped by a bound leaves a partial index that the process does not retry;
results carry `partial` and `partial_reason`, and `Symbols` declines to
answer from a partial index.

Changes reach a live index through `Invalidate` (called by `write_file`,
`edit_file` and the shell tools), which marks paths dirty; a dirty mark makes
the next query revalidate: files whose size or modification time changed,
new files and deleted files are searched through a small overlay shard, and
more than five percent dirty triggers a full rebuild. A file edited within
the 2-second timestamp granule of its indexing is compared by content, with a
hash whose seed is chosen per process (`maphash.MakeSeed`), so it cannot be
compared across processes. Nothing marks a fresh process's index dirty: it
starts empty and builds.

`tools`' `find_symbol` asks `Index.Symbols` first, which answers only from an
index an earlier `code_search` of the same process built, is not partial and
not closed, and never starts a build; otherwise `find_symbol` builds its own
symbol table for the process, bounded at 2 seconds.

The tests live in `codesearch/index_test.go`
(`TestShardsLiveInARunDirectoryPerIndex`,
`TestCloseWaitsForTheFirstBuildAndRemovesItsRunDirectory`,
`TestBuildBoundsGiveAPartialIndexThatIsNotRetried`), `codesearch/dirty_test.go`
(`TestRevalidationFindsChangesMadeBehindTheToolsBack`,
`TestRevalidationInsideTheRacyWindowMarksOnlyRealEdits`),
`codesearch/symbols_test.go` (`TestSymbolsDeclinesWhenItCannotAnswerInFull`,
`TestFindSymbolGivesTheSameMatchesThroughTheIndex`) and
`codesearch/tool_test.go` (`TestTheFirstCodeSearchBuildsTheIndexOnce`), with
the fixtures of `codesearch/helpers_test.go`. The command of spec 32 lives in
`cmd/agentkit-tools`, with its tests beside it. The library reads no
`AGENTKIT_` variable (`docs/configuration.md`).

## Requirements

### A persistent index directory in the library

`codesearch.Options` gains `IndexDir`. Empty keeps today's behaviour: a run
directory under `TempDir` that `Close` deletes. Set, the index of a
workspace lives under `<IndexDir>/agentkit-codesearch-<sha256 of the
workspace root>/`, one such directory per served directory, so one index
directory holds the indexes of any number of them, kept apart. `Close` keeps
a stored index.

- `New` refuses an `IndexDir` that is the workspace root or lies inside it,
  after resolving symbolic links of the parts that exist, with an error that
  says it must be outside; it still touches no disk.
- A completed build is stored: the shards and everything the index keeps in
  memory beside them (the indexed files, the outlines, the size,
  modification time, indexing time and content hash of each file, the build
  statistics, the partial flag and reason), with a manifest naming the
  workspace root and the format version.
- A stored index becomes visible only when complete: it is written under a
  temporary name in the same directory and renamed into place, replacing the
  previous one.
- If the index directory cannot be created or written, the build is still
  used by the process from its run directory under `TempDir`, as today;
  `code_search` works, nothing is stored, and the result reports nothing
  unusual.

### Reuse and revalidation in a later process

- A process whose first `code_search` finds a complete stored index for its
  workspace root, in the current format, loads it instead of building, and
  revalidates before answering: files whose size or modification time
  changed since they were indexed, new files and deleted files are picked
  up. The answer is the same as from a fresh build over the current tree.
- A file indexed within the timestamp granule of its last modification is
  compared by content in the later process too: the stored content hash does
  not depend on the process that wrote it.
- A process that finds no index, an index whose manifest names another root,
  one written in another format version, or one it cannot read, builds a new
  one and replaces what was there. It reports nothing to the caller beyond
  the normal result.
- More than five percent of the files changed triggers the full rebuild it
  triggers today, and that rebuild is stored.

### Two processes and partial builds

- Two processes on the same workspace and index directory at once: one
  builds, holding a lock in the workspace's index directory; the other waits
  for the lock up to 1.25 times `MaxBuildTime` plus 5 seconds, then loads
  what the first stored. If the wait runs out, it builds for itself in its
  own run directory and stores nothing. Neither ever reads a half-written
  index. The lock is released when its holder exits, however it exits.
- A build stopped by its bounds stores a partial index, marked as such;
  results carry the partial marker as today. A later process that finds a
  partial stored index builds again, and replaces it.

### `find_symbol` from the stored index

`Index.Symbols` answers from a complete stored index in a process where no
`code_search` has run yet: it loads and revalidates the index, never builds
one, and returns the same matches as it would after a build. So a single
`find_symbol` call does not build a symbol table. It still declines a
partial stored index and a workspace with no stored index, as today.

### The command's index directory

- `agentkit-tools` takes `--index-dir <path>` on every subcommand, or the
  environment variable `AGENTKIT_INDEX_DIR` when the flag is absent; the flag
  wins. An empty value means unset. There is no default location: without
  either, the index lives in a temporary directory removed when the process
  ends, as for a library embedder today.
- The command creates the directory when it is missing.
- An index directory that is the served directory or lies inside it exits 2
  with one line on stderr saying it must be outside, so an index is never
  checkpointed or committed with the work.
- If the index directory cannot be created or written, the command says so
  once on stderr, naming the directory, and serves `code_search` with a
  temporary index for that process.
- Target: on a generated tree of 10 000 source files, a `code_search`
  single call that finds a complete index and no changed file takes at most
  one fifth of the wall time of the call that built the index.

### Docs

`docs/codesearch.md` describes the stored index (layout, manifest, lock,
reuse, revalidation, partial builds); `docs/configuration.md` lists
`codesearch.Options.IndexDir` and `AGENTKIT_INDEX_DIR`; `docs/cli.md`
documents `--index-dir` and the variable; `docs/guide.md` shows a relay that
names an index directory. `make doc` refreshes `docs/generated/`.

### End-to-end flows

1. **Relay from outside.** A relay writes a request file and runs
   `agentkit-tools call --dir <checkout> --index-dir <dir> --request <file>`
   for `code_search`, twice. The first process builds and stores the index;
   the second loads it, does not build, and returns the same files.
2. **A change between two processes.** A harness session with
   `--index-dir` searches, then edits a file with `edit_file` and closes. A
   later single call searches for the edit: it loads the stored index,
   revalidates and finds the edited file.
3. **Two processes at once.** Two single calls start at the same moment on
   a fresh index directory. One builds, the other waits and loads; both
   return the same result, and the index was built once.

## Design Decisions

1. **The index directory has no default location** (owner's decision, PRD
   15 D6). The caller that wants reuse names it; Parley's route always does.
2. **The option is `codesearch.Options.IndexDir`, and the library reads no
   environment variable** (decided from `docs/configuration.md`, which says
   the library reads no `AGENTKIT_` variable): `AGENTKIT_INDEX_DIR` is the
   command's.
3. **The library refuses an `IndexDir` inside the workspace too** (decided
   here): otherwise the walk would index the shards themselves. The command
   checks first, to give its own message and exit 2.
4. **A store that fails falls back silently in the library; the command
   probes the directory first** (decided here): the library has no channel
   to a caller's stderr, so the command creates the directory and writes and
   removes a probe file before it starts, prints its one line on failure and
   passes no `IndexDir`.
5. **The waiting process's bound is 1.25 × `MaxBuildTime` + 5 s** (decided
   from `Options.MaxBuildTime`'s documented maximum build time); after it, the
   waiter builds for itself without storing, so a stuck builder cannot stall
   it forever. The lock is an advisory file lock, which the kernel releases
   when its holder exits.
6. **Revalidated changes are not written back** (decided here, also a
   non-goal): only a build or a threshold rebuild replaces the stored index,
   so a reader never races a partial update.
7. **A partial stored index is rebuilt, not served, by a later process**
   (PRD 15: "A later process tries the build again"). The process that built
   it keeps serving it, as today.
8. **The stored content hash is process-independent** (decided from
   `dirty.go`, whose per-process `maphash` seed cannot compare across
   processes): a hash function with a fixed definition, such as SHA-256.
9. **A manifest and a format version decide compatibility** (decided here):
   an index whose manifest names another root, another format version, or
   which does not parse is treated as absent.
10. **The wall-time target is a slow test** (decided here): it generates
    10 000 files and compares two wall times, so it is skipped under
    `-short` and runs in `make check-release`, the owner's run.
11. **Profile: full.** Reuse is verified across real processes of the
    command.
12. **Depends on spec 32**: the command's flag lands in the command it adds.

## Dependencies

- Spec 32 `tool_server_command` (active): the command that takes
  `--index-dir`.
- PRD 15 (`docs/prd/15-serve-the-tools-from-one-command-and-drop-the-sandbox.md`):
  the source; this package is its third scope.
- Archived spec 03 (`indexed_code_search`): the index this package persists,
  its bounds, revalidation and partial builds unchanged in contract.
- Parley (`github.com/agent-fox-dev/parley`): its route names the index
  directory.

## Verified External API

Read in this repository's sources:

- `codesearch.New(ws *workspace.Workspace, opts codesearch.Options) (tools.Index, error)`;
  `codesearch.Options{MaxFiles, MaxBytes, MaxBuildTime, TempDir}`;
  `(*codesearch.Index).Build`, `BuildCount`, `RevalCount`, `RunDir`,
  `IndexedFiles`, `Symbols`, `Invalidate`, `Close`.
- `tools.Index` (`Symbols(ctx, SymbolQuery) (SymbolAnswer, bool, error)`,
  `Tools`, `Invalidate`, `Close`).
- `github.com/sourcegraph/zoekt/search.NewDirectorySearcher(dir string)`, as
  `codesearch/overlay.go` calls it.
- `workspace.Workspace.Root() string`.

## Glossary

- **Index directory**: where the persistent `code_search` index and symbol
  data are kept, outside the served directory (`IndexDir`,
  `--index-dir`, `AGENTKIT_INDEX_DIR`).
- **Stored index**: one workspace's complete or partial index in the index
  directory, with its manifest.
- **Served directory**: the directory given with `--dir`, opened as the
  workspace.
