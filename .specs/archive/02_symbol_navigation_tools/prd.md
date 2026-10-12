---
spec_id: "02"
spec_name: "symbol_navigation_tools"
title: "file_outline and find_symbol tools with a lazily built, always-fresh symbol table"
status: "active"
created_at: "2026-10-01T15:28:28.598588Z"
updated_at: "2026-10-01T15:28:28.598588Z"
intent_hash: "cd51fe7229fa66315c4d5d08fc0511d2918ab3455f85f11c090b4ff6664d9a73"
schema_version: 2
source: "docs/prd/04-add-symbol-navigation-tools.md"
---
## Intent

Give a model two direct answers to the questions it asks most often about code: "what is declared in this file, and on which lines" (`file_outline`) and "where is this name declared in the workspace" (`find_symbol`). Both are read-only built-in tools in `tools`, built on the `outline` package and the shared walk delivered by `01_outline_and_walk`. They agree with the other file tools about which files exist, and they never answer from stale data about files changed through AgentKit's own tools.

## Goals

1. `file_outline` returns a file's declarations as compact text with line ranges, so the model can follow it with `read_file` using the existing `offset` and `limit`. Unexported declarations are counted but not listed unless `include_private` is true.
2. `find_symbol` returns declarations whose name matches, ranked so that exact matches, exported names and non-test files come first, with a deterministic order and a truncation marker that names a call that works.
3. The symbol table behind `find_symbol` is built on the first call, held for the life of the tool set, and refreshed before an answer whenever `write_file`, `edit_file`, `execute`, `run_command` or `powershell` may have changed a file.
4. Building is bounded by a file count (default 50 000) and a wall-time limit (default 2 s). Both are `tools.Options` fields. A bound that is hit yields a usable answer marked `partial: true`, never an error and never a hang.
5. Every result names the backend that produced it, in `Data` and in the text header.
6. Both tools are in `All()` and in `FileNavigationTools()`. The pinned default tool set and the prompt goldens are updated in the same change.
7. The root module stays standard-library-only and cgo-free. `internal/policy` tests stay green with no new `allowedModules` entry.

## Non-goals

- The `outline` package, `tools.CtagsRunner` and `tools.Walk`. They belong to `01_outline_and_walk` and are consumed here as delivered.
- References, callers, type resolution and a language-server client. "Who calls X" stays `search_files` with `\bX\b`.
- Equal output from the three outline backends. Disagreement is diagnosed through the named backend.
- A persistent, ranked or cross-run index (a later PRD, `05`). The table lives in memory and dies with the tool set.
- Detecting changes made outside AgentKit's tools: an editor, `git checkout` run by the embedder, or a process that `execute` left running in the background. The table is fresh with respect to the tool calls listed in Requirement 5 and nothing else.
- Changing any existing tool's description, schema, result shape or marker text, or the shell guard (`guard.ShellToolNames`).
- Editing the `examples/` tool allowlists (for example `readOnlyTools` in `examples/cleaner`). They name tools explicitly and simply do not gain the new ones.
- Documenting the tools in `docs/api.md`. That file describes the HTTP network API (see Design Decision 17).

## Background

What exists today, from reading the code:

- `tools/tools.go`: `All(opts)` builds `fileTools` through `newFileTools` and returns nine tools in a pinned order. `fileTools` holds `ws`, `locks` (a refcounted `pathLocks` keyed on the symlink-resolved path from `lockKey`) and `ig` (a memoized `IgnoreOptions`). `write_file` and `edit_file` resolve the path, take the lock, write, and release with `defer release()`. Both are `core.Sequential`. `executeTool`, `runCommandTool` and `PowerShell` take only `Options`, not `fileTools`, and tests construct them directly (`runCommandTool(Options{...})`).
- `FileNavigationTools()` returns `list_files`, `find_files`, `search_files`. `prompt/prompt.go` emits `ExecuteFallbackGuideline` only when none of those names is in the active set and `execute` is present.
- `tools/limits.go`: `clampLimit`, `capMarker` and the marker constructors (`FindMarker`, `SearchMarker`) name the exact next call and say so when the cap is already the maximum.
- `tools/search.go`: `RenderSearchText` is the precedent for `Text` for the model and `Data` for the embedder. Search skips hidden entries and non-regular files and detects binary files by a NUL byte in the first 8 KiB.
- `tools/path.go`: `Workspace.Resolve` returns a symlink-resolved absolute path inside the root or `ErrPathNotAllowed`. `read_file` uses it, then `os.Stat`, then `not_a_file` for a non-regular file. **`read_file` does not apply the ignore engine**: it reads an ignored file if asked. The input PRD says `file_outline` refuses an ignored path "with the same codes `read_file` uses"; no such code exists (Design Decision 1).
- `tools/ignore.go`: the ignore engine reads the global excludes, `.git/info/exclude` and `.gitignore` files only.
- `tools/reqtool06_test.go` pins the default tool names and order (`TestTheDefaultToolSetIsPlatformStable`). `prompt/golden_test.go` pins the assembled prompt in `prompt/testdata/golden/system_prompt_default.txt` and `system_prompt_no_navigation.txt`, and `TestGoldenPromptWithoutFileNavigationTools` removes the navigation tools by name.
- `schema.Object`, `schema.Prop`, `schema.Opt`, `schema.String`, `schema.Int` and `schema.Bool` build tool input schemas.

What `01_outline_and_walk` delivers (its tasks are pending in the working tree; there is no `outline/` directory yet): `outline.Outline(ctx, abs, src, opts) (File, error)`, `outline.OutlineMany(ctx, []Source, opts) ([]File, Stats, error)` with batching, `Decl`, `File`, `Backend`, `Options{Root, MaxFileBytes, Runner}`, the closed `Kind` set, signatures already sanitised and cut to 200 bytes, `tools.CtagsRunner(env)` with `ErrCtagsUnavailable`, and `tools.Walk(ctx, ws, root, WalkOptions{Ignore, IncludeHidden}, fn)`, whose callback sees directories as well as files.

## Requirements

### 1. `file_outline`

Schema: `path` (required string), `include_private` (optional bool, default false). `PromptGuidelines`: "Before reading a large file, outline it and read only the range you need." The tool is `Builtin`, `Parallel` and read-only.

The path goes through `Workspace.Resolve`. An unparsable argument, or an empty path, is `invalid_arguments`; a path outside the workspace is `path_not_allowed`; a stat failure is `read_failed`; a directory or other non-regular file is `not_a_file`, using `notAFile` so a directory message says to use `list_files`. A cancelled context is `aborted`. An error from `outline.Outline` (an unreadable file) is `outline_failed`.

The tool outlines the file itself from disk on every call, never from the symbol table, so it is always current. It calls `outline.Outline` with `Root` set to the workspace root and the same runner configuration the symbol table uses (Requirement 7). It does not apply the ignore engine or the hidden-entry rule, because `read_file` does not; the file it names is a file the model could already read.

`Text` is a header line followed by one line per listed declaration, in source order:

```
internal/agentrun/phase.go  (go/ast, 14 declarations, 6 unexported not listed)
  L93-107    type Observer interface
  L240-312   func (r *Runner) Run(ctx context.Context, p Phase) (Result, error)
```

The header carries the workspace-relative slash path, the backend, the total declaration count, and, only when some were withheld, how many unexported declarations are not listed. Each line shows `L<start>-<end>` and the declaration's `Signature`. A declaration whose `EndLine` is 0, or equals `StartLine`, shows `L<start>`. A declaration with a non-empty `Container` is indented two further spaces unless its signature already contains the container's name (a Go method's receiver does). A file with no declarations renders the header and one line saying no declarations were found; when the backend is `none` the line also says why in a neutral form (language not recognised, file too large, or binary) and suggests `read_file` or `search_files`. `Data` carries `file` (the `outline.File`), `backend`, `declarations` (total) and `listed`.

### 2. `find_symbol`: arguments and matching

Schema: `name` (required string), `kind`, `path`, `exact` (bool), `max_results` (int), all optional. `PromptGuidelines`: "Use find_symbol to locate a declaration; search_files for usages and text." The tool is `Builtin`, `Parallel` and read-only.

Input bounds are checked before any walk. `name` must be non-empty after trimming and at most 256 bytes; otherwise `invalid_arguments`. `kind` is trimmed and lower-cased and must be one of the eleven `outline` kinds (`func`, `method`, `type`, `class`, `interface`, `enum`, `trait`, `const`, `var`, `module`, `macro`); otherwise `invalid_arguments` naming the allowed values. `path` goes through `Workspace.Resolve` (outside the workspace is `path_not_allowed`), must exist (else `read_file`-style `read_failed`), and must be a directory (a regular file is `invalid_arguments` saying to use `file_outline`).

Matching is on `Decl.Name`. By default it is a prefix match with smart-case: an all-lowercase `name` matches case-insensitively, any uppercase rune makes it case-sensitive. With `exact: true` the match must be exact and case-sensitive. A `name` containing a `.` is qualified: it is compared against `Container + "." + Name` of each declaration that has a container, under the same prefix, smart-case and `exact` rules; `Runner.Run` therefore finds method `Run` on `Runner`. A `name` without a dot is compared with `Name` alone, so `Run` finds methods too. `kind` keeps only declarations of that kind. `path` keeps only files under that directory.

### 3. `find_symbol`: ranking, limits, result shape

Matches are ordered by: exact before prefix (an exact match under smart-case counts as exact), exported before unexported, non-test files before test files, shorter path first, then path, then `StartLine`. A file is a test file by a fixed, documented rule: Go `_test.go`; Python `test_*.py` and `*_test.py`; JS/TS `*.test.*` and `*.spec.*`; Java, Kotlin, C# and Ruby files whose names end in `Test`, `Tests` or `_spec`; and any file under a directory named `test`, `tests`, `__tests__` or `spec`.

`max_results` defaults to 20 and is clamped to 50 (`clampLimit`). When more matches exist, only the first `max_results` are returned and the result carries a marker built like `FindMarker`: a new `SymbolMarker(limit)` in `tools/limits.go` using `capMarker("results", "max_results", limit, SymbolResultCap, ...)`, so below the cap it names `max_results=<next>` and at the cap it says 50 is the maximum and advises narrowing with `path`, `kind` or `exact`. `ToolMetadata.Truncated` is set with `TruncatedByLines`.

`Data` carries `symbols` (a slice of `SymbolMatch` with JSON tags `path`, `backend`, `kind`, `name`, `container`, `signature`, `exported`, `start_line`, `end_line`), `truncated`, `note` when truncated, `backends` (a map from backend name to the number of indexed files, within the queried scope, that it produced), `files_indexed`, and `partial` when it applies. `Text` is a header, then one line per match, then any marker or partial note:

```
2 symbols matching "Run"  (index: 412 files; go/ast 380, heuristic 20, none 12)
internal/agentrun/phase.go:240-312  method  func (r *Runner) Run(ctx context.Context, p Phase) (Result, error)
```

No match is a successful result whose text says so and still shows the index line. Paths in `Text` have control characters replaced with `?`; `Data` keeps the real path.

### 4. The symbol table: build, scope and bounds

The table is created empty with `fileTools` and built on the first `find_symbol` call; no walk, subprocess or file read happens at `All()` time or when only `file_outline` is used. It is always built over the workspace root, so every ignore layer from the root applies, and `path` is a filter on it (Design Decision 5). It covers the files `tools.Walk` yields with `IncludeHidden: false`, the same rule as `search_files`, restricted to regular files. It therefore reports nothing under dot-prefixed directories and nothing through symlinks.

Declarations come from `outline.OutlineMany` in batches of at most 100 files. The table keeps, per file, its `outline.File`, size, modification time and the time it was indexed.

Two bounds apply to every build or refresh pass: `SymbolOptions.MaxFiles` (default 50 000) and `SymbolOptions.MaxDuration` (default 2 s); a zero or negative value means the default. The file bound counts regular files the walk yields, whatever their language; the pass stops at the first file past the limit. The time bound covers the walk and the outlining together. It is enforced with a derived context passed to `OutlineMany`, so a stuck ctags process is killed at the deadline, and the batch it was working on is left unindexed. When either bound stops a pass, the table keeps what it has and the result carries `partial: true`, `partial_reason` (`files` or `time`) and a note, produced by a new `SymbolPartialMarker`, telling the model to narrow with `path` or fall back to `search_files`. A partial table is not final: the next call runs another pass that skips files whose size and mtime are unchanged and indexes the ones not yet seen, so repeated calls make progress. The bound is the only reason a result is partial; a cancelled call context is `aborted`, not a partial answer.

When `path` is given and the table is not yet complete, the pass walks from the workspace root but returns `SkipDir` for every directory that is neither an ancestor of nor inside `path`, so a narrow query indexes only what it needs and the ignore layers above `path` still apply.

### 5. Freshness

Before answering, `find_symbol` brings the table up to date. A fresh table with nothing marked is answered from memory with no walk.

- **`write_file` and `edit_file`**: after the write attempt, still holding the per-path lock, the tool marks the file's workspace-relative path dirty. It is marked after the write and before the lock is released, on success and on failure, so a refresh can never observe the file before the write finished.
- **`execute`, `run_command` and `powershell`**: after the command returns, whatever its outcome, the whole table is marked for revalidation. This is done by wrapping the three tools' `Execute` in `All()`; the standalone constructors and their tests are unchanged.
- **Refreshing a dirty path** that is already in the table: it is stat'ed and, if it is still a regular file, re-outlined; if it is gone or no longer regular, it is dropped. A dirty path that is not in the table (a new file), or whose base name is `.gitignore`, changes which files exist and therefore escalates to whole-table revalidation, because only the walk's ignore engine can say whether a new file is visible.
- **Revalidation** walks the scope (the whole workspace, or the part under `path` as in Requirement 4) and compares size and mtime for every file. It re-outlines only files that changed or are new, and drops entries for files not seen in a completed walk. A file whose mtime lies within 2 s of the moment it was indexed is always re-outlined on revalidation, so an edit that keeps the size and falls inside the filesystem's timestamp granularity is not missed. Revalidation clears the whole-table mark only when it covers the whole workspace and completes within the bounds; a pass scoped by `path` makes only that subtree fresh and leaves the mark set.
- **Races**: a mark made while a build or refresh is running is not lost. Each mark advances a generation counter, and a pass clears only the marks that predate it.

### 6. Concurrency and cancellation

All table state is guarded by one lock that a waiting call can abandon when its context ends (a call blocked behind a long build returns `aborted`, not after the build). Builds and refreshes are serialized; reading the table to answer a query happens under the same lock. Both tools return `aborted` with detail "Operation aborted" when the call context is cancelled, matching the other file tools. Marking from `write_file`, `edit_file` and the shell tools never blocks on a build in progress for longer than it takes to set a flag.

### 7. Wiring, options and defaults

`tools.Options` gains `Symbols SymbolOptions` with `MaxFiles int`, `MaxDuration time.Duration`, `DisableCtags bool` and `Runner func(ctx context.Context, args []string) ([]byte, error)`. By default both tools use `CtagsRunner(opts.Env)`, constructed lazily on first use so `All()` spawns nothing, giving ctags where universal-ctags is installed and heuristics where it is not, as `search_files` does with `rg`. `DisableCtags` forces `Runner` nil; a non-nil `Runner` replaces the default, which is the seam tests use. `Options.Ignore` (already memoized in `fileTools`) is what the walk uses.

`All()` returns `read_file`, `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`, `execute`, `run_command`, `powershell`, in that order. `FileNavigationTools()` returns `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`. `ExecuteFallbackGuideline` is unchanged and, by the existing condition in `prompt/prompt.go`, is emitted only when none of the five is present and `execute` is.

Neither tool declares a write capability, is `Sequential`, or appears in `guard.ShellToolNames`. The doc comments on `All()` and `FileNavigationTools()` that say "trio" are updated.

### 8. Rendering and security

Argument bounds are those of Requirements 1 and 2. Nothing the tools do runs a shell, and ctags is only ever invoked through `outline` and `CtagsRunner`, which pass an explicit absolute file list and `--options=NONE` (their own spec's contract). Signatures arrive sanitised from `outline`; the tools additionally replace control characters in paths they print, and never print a signature longer than `outline` produced. A malformed ctags line is counted and skipped inside `outline`, not trusted here. A ctags failure is not a tool failure: the files fall back to another backend and the backend field says so.

### 9. Tests

- **Matching and ranking**: table-driven tests over a fixture tree: prefix and exact, smart-case, `exact` case-sensitivity, qualified names, `kind` and `path` filters, every ranking tier in isolation and in combination, and determinism across repeated calls.
- **Limits and markers**: truncation at the default, at a custom value and at the cap, with the marker text pinned like the existing marker goldens; `max_results` above 50 clamps.
- **Input bounds**: empty and 257-byte `name`, unknown `kind`, a `path` outside the workspace refused before any walk (no `Walk` call), a file given as `path`.
- **Agreement with the other tools**: the set of files indexed equals the set `search_files` selects over a tree with nested `.gitignore` files, a nested repository, a hidden directory and a symlink; `file_outline` works on an ignored file that `read_file` can read.
- **Freshness**: edit a file through `edit_file` and see the new declaration; create a file through `write_file` and see it; delete one through `execute` and see it vanish; edit through `execute` with identical size inside the racy window; change a `.gitignore` through `write_file`; a mark during a build is not lost (a blocking fake `Runner` and a concurrent `write_file`).
- **Bounds**: a synthetic 60 000-file tree returns `partial: true` with `partial_reason` `files` within the deadline (skipped under `-short`, with a scaled-down `MaxFiles` variant that always runs); a blocking fake `Runner` with a short `MaxDuration` returns `partial: true` with reason `time` close to the deadline and kills the call; a second call after a partial pass indexes more files.
- **Laziness and concurrency**: `All()` and `file_outline` start no walk and no subprocess; concurrent `find_symbol` calls under `-race`; a call abandoned while another build runs returns `aborted`.
- **`file_outline`**: header and line format, `include_private`, container indentation, `none` rendering, directory and outside-workspace errors, an oversized file.
- **Backends**: tests run with `DisableCtags` or a fake `Runner` so results do not depend on the machine; a test that uses real universal-ctags skips when it is absent, as the ripgrep tests do.
- **Wiring**: `TestTheDefaultToolSetIsPlatformStable` is updated to the eleven names; a test that `FileNavigationTools()` has the five names and that the prompt builder suppresses the execute fallback when only `file_outline` is present; `TestGoldenPromptWithoutFileNavigationTools` filters the two new names; `system_prompt_default.txt` is regenerated with `-update` and its diff reviewed (it gains the two guidelines in tool order; `system_prompt_no_navigation.txt` does not change).
- **Policy**: `internal/policy` tests pass unmodified; `go.mod` gains nothing; `make check` passes with and without universal-ctags.

### 10. Documentation

In the same change: `README.md` adds the two tools to the tool list and states in "What is not built" that references and callers remain unbuilt; `docs/architecture.md` notes the symbol table and its place under `tools`; `docs/configuration.md` adds the `Symbols` row to the `tools.Options` table, the two names to the `tools.All` sentence, and the new limits (20 default and 50 cap for `find_symbol`, 50 000 files, 2 s); `docs/GAPS.md` records the gap that remains (references, callers) with a pointer to this spec's Non-goals. `docs/api.md` is not touched.

## Design Decisions

1. **`file_outline` does not refuse ignored paths.** The input says an ignored path is refused "with the same codes `read_file` uses", but `read_file` never consults the ignore engine and has no such code. A tool that refused what `read_file` accepts would send the model on a detour; the consistency that matters (Goal 5 of the input) is for the set of files `find_symbol` reports.
2. **`file_outline` reads from disk, never from the table.** It costs one file read, is never stale, and does not force a whole-workspace build to answer a question about one file.
3. **Ctags is on by default in the tools, through a lazily built `CtagsRunner`, with `DisableCtags` and a `Runner` override.** The input locates ctags "the way `search_files` locates `rg`", which is default-on. Lazy construction keeps `All()` free of subprocesses, and the override gives tests a deterministic backend.
4. **The bounds are `Options.Symbols` fields with zero meaning default.** This follows `Options.SpillDir` and `clampLimit`; a negative value cannot mean "unlimited" because the point of the bounds is that an unbounded build can hang a tool call.
5. **The table is built over the workspace root and `path` is a filter, with a pruned walk when the table is incomplete.** Building from `path` as the root would drop the `.gitignore` files above it (the engine only loads layers from its own root), so `find_symbol` and `search_files` would disagree. Pruning keeps the ignore layers and still makes "narrow with `path`" cheaper, which is what the partial note tells the model to do.
6. **The hidden-entry rule is `search_files`'s (`IncludeHidden: false`).** The input says to follow `search_files`. The cost, stated in Requirement 4, is that nothing under `.github/` or other dot directories is indexed; `file_outline` still works there.
7. **The file bound counts every regular file the walk yields.** The input says "at most 50 000 files" without saying which. Counting files in unknown languages makes the bound a bound on walk work, which is what takes time; a table that counted only source files would let a tree of a million PDFs walk unbounded.
8. **Dirty-path refresh for known files, whole-table revalidation for new files and `.gitignore`.** Only the walk's ignore engine can say whether a new file is visible, so replicating that decision per path would be a second ignore engine. A rewrite of an existing file, the common case, stays cheap.
9. **The shell tools are wrapped in `All()`, not changed.** `executeTool`, `runCommandTool` and `PowerShell` take `Options`, are exported or constructed directly by tests, and have no `fileTools`. Wrapping leaves their constructors and tests untouched; a tool built by hand has no table to invalidate.
10. **Shell tools mark after the command returns, whatever the outcome.** A timeout or non-zero exit can leave files half-changed, so any outcome invalidates.
11. **Writes mark after the write, before the lock is released.** Marking first would let a concurrent refresh re-outline the old content and clear the mark, which is exactly the stale answer the input forbids.
12. **A racy-mtime window of 2 s forces re-outline on revalidation.** Size-and-mtime comparison misses a same-size edit within the filesystem's timestamp granularity (1 to 2 s on some filesystems); git handles the same hole the same way. The cost is re-outlining recently indexed files after a shell command.
13. **A partial table is completed by later calls, not frozen.** Otherwise one slow cold-cache first call would leave every later answer partial for the session. Skipping unchanged files makes each pass cheaper than the first.
14. **The time bound is a derived context given to `OutlineMany`.** A deadline checked only between batches would let one hung ctags batch overrun it; the derived context makes `CtagsRunner` kill the process group. Batches interrupted that way stay unindexed and are retried on the next call.
15. **Declarations under a `Container` are indented unless the signature already shows the container.** The input says "grouped under their container" but its example shows Go methods flat, with the receiver in the signature. One rule satisfies both and is testable.
16. **`Kind` validation lists the eleven `outline` kinds in `tools`, with a test tying it to `outline`'s kinds.** `outline` is not required to export a list, and the filter must reject typos instead of returning nothing.
17. **Docs go to `README.md`, `docs/architecture.md`, `docs/configuration.md` and `docs/GAPS.md`, not `docs/api.md`.** `api.md` documents the HTTP API, and the steering table reserves it for REST endpoints; `configuration.md` is where `tools.Options` is already documented.
18. **New tools go after `search_files` in `All()`.** They are navigation tools and read best next to `search_files`. The tool list is the head of the cached prompt prefix, but the new guidelines change the prompt for every consumer in any case, so appending at the end would not avoid the invalidation.
19. **No `recommended_split`.** The work is the tools, table, wiring and docs; the package and the walk were split off already.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `01_outline_and_walk` | depends on | Supplies `outline.Outline`, `outline.OutlineMany`, `outline.Options`, `outline.Source`, `outline.File`/`Decl`/`Backend` and the closed kind set; `tools.CtagsRunner` and `ErrCtagsUnavailable`; and `tools.Walk` with `WalkOptions`, which is the only walk the table may use. Its non-goals deferred `FileNavigationTools()`, `All()` and the pinned default tool set to this spec. |

## Verified External API

All external symbols are standard library (`context`, `encoding/json`, `io/fs`, `os`, `path/filepath`, `sort`, `strings`, `sync`, `time`, `unicode`); the repository's `go.mod` declares `go 1.26.5`, and no module is added. In-repository symbols were read from source.

| Symbol | Signature | Status |
|---|---|---|
| `core.ErrResult`, `core.OKResult`, `core.ToolResult{Data, Text, Metadata, OK, Error, Detail}`, `core.ToolMetadata{Truncated, TruncatedBy}` | as used throughout `tools/tools.go` | verified (read call sites) |
| `schema.Object`, `schema.Prop`, `schema.Opt`, `schema.String`, `schema.Int`, `schema.Bool` | as used in `tools/search.go` and `tools/tools.go` | verified (read call sites) |
| `(*Workspace).Resolve(p string) (string, error)`, `(*Workspace).Rel(abs string) string`, `ErrPathNotAllowed` | `tools/path.go` | verified (read) |
| `clampLimit(n, def, cap int) int`, `capMarker(noun, param string, limit, max int, alternative string) string`, `notAFile(shown string, mode fs.FileMode) string`, `lockKey`, `(*pathLocks).acquire` | `tools/tools.go`, `tools/limits.go` | verified (read) |
| `IgnoreOptions`, `(IgnoreOptions).cached()`, `fileTools.ig` | `tools/ignore.go`, `tools/tools.go` | verified (read) |
| `outline.Outline(ctx context.Context, abs string, src []byte, opts Options) (File, error)` | per `01_outline_and_walk` PRD | NOT FOUND in the working tree (no `outline/` directory; that spec's tasks are pending). Signature assumed from its PRD. |
| `outline.OutlineMany(ctx context.Context, srcs []Source, opts Options) ([]File, Stats, error)`, `outline.Source{Abs string; Src []byte}`, `outline.Options{Root string; MaxFileBytes int64; Runner func(context.Context, []string) ([]byte, error)}` | per `01_outline_and_walk` PRD and tasks | NOT FOUND in the working tree; assumed from the spec. The type of `MaxFileBytes` is not stated there and is irrelevant here because this spec leaves it at its default. |
| `tools.Walk(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error`, `WalkOptions{Ignore IgnoreOptions; IncludeHidden bool}` | per `01_outline_and_walk` PRD | NOT FOUND in the working tree (the three private `filepath.WalkDir` loops still exist); assumed from the spec. |
| `tools.CtagsRunner(env []string) func(ctx context.Context, args []string) ([]byte, error)`, `tools.ErrCtagsUnavailable` | per `01_outline_and_walk` PRD | NOT FOUND in the working tree; assumed from the spec. Whether it probes eagerly or lazily is not guaranteed, so this spec wraps its construction in a lazy call. |
