---
spec_id: "03"
spec_name: "indexed_code_search"
title: "Indexed code_search tool in a separate codesearch module, with an optional index seam in tools"
status: "active"
created_at: "2026-10-01T19:30:41.631326Z"
updated_at: "2026-10-01T19:30:41.631326Z"
intent_hash: "19f9c7c4710d5286163ac2a900a793bbe543e99afdbe809f99f4a4afce23e007"
schema_version: 2
source: "docs/prd/05-add-an-indexed-code-search-module.md"
---
## Intent

Let a model ask ranked, file-grouped questions about a single working tree ("where is retry handled", "which files deal with compaction") through a `code_search` tool backed by an in-process zoekt index. The index lives in a separate nested module, `codesearch/`, so no embedder that does not import it pays for zoekt's dependency graph. The root module gets only a small, standard-library-only seam (`tools.Index`, `tools.Options.Index`) through which the index adds tools, learns about writes, and may serve `find_symbol`. The index never shows content that is out of date with respect to AgentKit's own tool calls.

## Goals

1. `code_search` accepts zoekt's query language and returns results grouped by file in zoekt's score order, within the 50 KB byte limit and the truncation-marker rules of REQ-TOOL-09 (REQ-TOOL-09b: every marker names a call that works).
2. The root module is unaffected: `go.mod` gains no `require`, `internal/policy`'s `allowedModules` is unchanged, `go list -deps ./...` in the root contains no new module, and an embedder that does not import `codesearch` pays nothing.
3. The index holds exactly the files `tools.Walk` yields with `IncludeHidden: false` (the `search_files` rule), restricted to regular, non-binary files of at most 1 MiB. It never uses zoekt's git indexer.
4. A `code_search` result never shows file content older than the last write or shell command made through AgentKit's tools.
5. When a `codesearch` index is built and complete, `find_symbol` answers from it with the same matching, ranking, limits and result shape as `02_symbol_navigation_tools`, and falls back to its own table otherwise.
6. The `codesearch` module builds and vets on linux/amd64, linux/arm64, darwin/arm64 and windows/amd64, and contains no cgo anywhere in its dependency graph.
7. The work is conditional on a go decision (Requirement 1); a failed risk check from Requirement 8 ends the spec instead of loosening a gate.

## Non-goals

- **Replacing or rerouting `search_files`.** Its RE2 dialect, walk order and ripgrep/native parity are pinned by tests; zoekt differs on all three, so zoekt gets its own tool.
- **A service, a cross-run index, or a web UI.** The index is built per index instance and its shards are deleted with it. A persistent or multi-repository server (such as `techquestsdev/code-search`) would be a later PRD on the `hub` side, reached over MCP.
- **Semantic or embedding search.** No model call happens inside a tool.
- **Searching more than one repository.**
- **Zoekt's git indexer, commit or branch search, and zoekt's own ctags invocation** (Design Decision 4).
- **Changing `search_files`, `FileNavigationTools()`, `ExecuteFallbackGuideline`, the default tool set or the prompt goldens.** With `Options.Index == nil` the output of `tools.All` is byte-for-byte what `02_symbol_navigation_tools` delivers.
- **Detecting changes made outside AgentKit's tools** (an editor, a `git checkout` run by the embedder, a background process). Freshness covers the tool calls in Requirement 5 and nothing else, as in `02_symbol_navigation_tools`.
- **Editing agent-fox or any embedder's allowlists.** An embedder opts in by passing the index and naming `code_search` in its own allowlist.
- **Documenting the seam in `docs/api.md`.** That file describes the HTTP network API (Design Decision 14).
- **The agent-fox measurement work itself** (its `tool_calls` and `tool_result_bytes` metrics). Only the decision it produces is consumed here.

## Background

Read from the repository:

- `tools/tools.go`: `Options` holds `Workspace`, `SpillDir`, `DisableSpill`, `Env` and `Ignore`. `All(opts)` builds `fileTools` and returns the built-in tools in a pinned order; `defaultSpillDir(root)` names a per-workspace directory under `os.TempDir()` from a hash of the root. `write_file` and `edit_file` hold a per-path lock while writing. `FileNavigationTools()` is a static name list.
- `tools/search.go`: `search_files` is a grep with `SearchMatchCap = 100`, `MaxSearchContextLines = 20`, `SearchLineChars = 500`, binary detection by a NUL byte in the first 8 KiB, and `RenderSearchText` (grep-style, grouped by file, `N:` for a match and `N-` for context). **It has no per-file size limit**, so the index needs its own (Design Decision 9).
- `tools/limits.go`: `DefaultByteLimit` (50 KB), `TruncatedByLines`/`TruncatedByBytes`, and the unexported `capMarker`, which names the next call or says the cap is already the maximum.
- `tools/path.go`: `Workspace.Resolve` returns a symlink-resolved path inside the root or `ErrPathNotAllowed`; `Workspace.Rel` returns an OS-separator relative path.
- `difftest/go.mod` is the precedent for a nested module (`replace github.com/agentfox/agentkit-go => ..`). `docs/DEPS.md` rulings R1 and R2 say a nested module is the only mechanism that confines a dependency, and that cgo is rejected whatever the allowlist says. `internal/policy` holds `TestNoUnapprovedModules`, `TestNoCgoOutsideStdlib` (run with `CGO_ENABLED=1`, which is load-bearing), `TestCgoProbeIsArmed` and `TestCrossTargetBuildAndVet` (four targets, `CGO_ENABLED=0`); none of them sees a nested module.
- The `Makefile` runs `test`, `vet`, `lint` and `tidy` over the root and `difftest` only. `docs/configuration.md` documents `tools.Options` in a table.
- `.specs/01_outline_and_walk` (active, not yet present in the working tree) delivers `outline.Outline`/`OutlineMany`, `outline.File`/`Decl`/`Backend`, `tools.Walk`/`WalkOptions` and `tools.CtagsRunner`/`ErrCtagsUnavailable`. `.specs/02_symbol_navigation_tools` (active, not yet implemented) delivers `find_symbol`, `SymbolMatch`, the in-memory symbol table, `Options.Symbols`, and the dirty-marking hooks in `write_file`, `edit_file` and the three shell tools. This spec builds on all of them and cannot start until they have landed.
- The input's `Index.Symbols` returns `[]outline.Decl`, but `outline.Decl` has no file path, so `find_symbol` could not build a `SymbolMatch` from it. `SymbolQuery` is also used by the input and defined nowhere. Both are fixed here (Design Decision 6).
- The input says zoekt is invoked for ctags "as PRD 04 §5 requires" (explicit file list, `--options=NONE`, reduced environment). Zoekt's own builder starts ctags itself and cannot be held to that, so symbol data is supplied by the project's own `outline` instead (Design Decision 4).

## Requirements

### 1. The gate and the module

The module is built only if the go/no-go holds: on the navigation baseline plus at least two target repositories of 5 000 files or more, `search_files` is still at least 25% of read-phase tool calls, or at least a third of `search_files` results are truncated or retried within two turns in the same directory. The thresholds are fixed as written and are not tuned after the data is seen. The measured figures, the repositories and the date are the first content of `codesearch/README.md`. If the figures do not meet the thresholds, or any check in Requirement 8 fails, no code in this spec is merged; the PRD is closed with the numbers recorded in it.

`codesearch/` is a nested module `github.com/agentfox/agentkit-go/codesearch` with `replace github.com/agentfox/agentkit-go => ..`, a pinned zoekt release, and a `go.mod` header comment that states the boundary rule as `difftest/go.mod` does. Files: `index.go` (build and refresh), `search.go` (query to ranked result), `tool.go` (the `code_search` tool), `policy_test.go`. The single entry point is `codesearch.New(ws *tools.Workspace, opts Options) (tools.Index, error)`. `New` starts no goroutine, runs no process, and touches neither the walk nor the disk; it returns an error if `ws` is nil. The root module's `go.mod`, `go.sum` and `internal/policy` are not edited.

`codesearch.Options` has: `Ignore tools.IgnoreOptions` (the embedder passes the same value it gave `tools.Options`), `Env []string` (ctags environment, nil means `tools.ReducedEnv(nil)`), `DisableCtags bool`, `Runner func(ctx context.Context, args []string) ([]byte, error)` (replaces the default ctags runner; the test seam), `MaxFiles int` (default 100 000), `MaxBytes int64` (default 1 GiB), `MaxBuildTime time.Duration` (default 60 s), and `TempDir string` (default `os.TempDir()`). A zero or negative limit means the default.

### 2. The seam in `tools`

`tools` gains, standard library only:

```go
type SymbolQuery struct { Name, Kind, Path string; Exact bool }
type SymbolAnswer struct {
    Matches      []SymbolMatch
    FilesIndexed int
}
type Index interface {
    Symbols(ctx context.Context, q SymbolQuery) (SymbolAnswer, bool, error)
    Tools() []core.Tool
    Invalidate(rel string)
    Close() error
}
```

and `Options.Index Index` (nil means no index). `SymbolQuery` carries `find_symbol`'s validated arguments, with `Path` a slash-separated workspace-relative directory (empty for the whole workspace) and `Kind` already lower-cased. `SymbolMatch` is the type `02_symbol_navigation_tools` defines. `tools` also exports `CapMarker(noun, param string, limit, max int, alternative string) string` and `ClampLimit(n, def, cap int) int`, thin wrappers over the existing unexported helpers, so `codesearch` produces markers with the shared wording instead of copying it.

`All()` appends `Index.Tools()` after the built-in tools when `Index` is set. If an index tool's name equals a built-in's name, `All` returns an error naming it. Index tools are not added to `FileNavigationTools()`.

`Invalidate(rel)` is called from the same points where `02_symbol_navigation_tools` marks its symbol table: after the write attempt in `write_file` and `edit_file` (before the per-path lock is released, on success and on failure) with the slash-separated workspace-relative path, and with `""` after `execute`, `run_command` or `powershell` returns, whatever its outcome. `Invalidate` must not block on a build or query in progress for longer than it takes to set a flag; it never returns an error and never panics on a closed index.

### 3. `code_search`

Arguments: `query` (required string), `path`, `max_files`, `context_lines` (optional). The tool is `Builtin`, parallel and read-only. Its description says "zoekt query syntax", never "RE2", and gives four examples: `sym:Runner`, `retry file:\.go$ -file:_test`, `lang:python "def load"`, `(compaction or summarize) case:no`. `PromptGuidelines`: "Use code_search for ranked questions about the codebase; search_files for an exhaustive regex scan."

Validation happens before any index work. `query` empty after trimming, over 1 024 bytes, or rejected by zoekt's parser is `invalid_arguments` (the parser's message is included). `context_lines` defaults to 2, is clamped to `tools.MaxSearchContextLines`, and a negative value is `invalid_arguments`. `max_files` defaults to 10 and is clamped to 25 with `tools.ClampLimit`. `path` goes through `Workspace.Resolve` (outside the workspace is `path_not_allowed`), must exist (else `read_failed`), and is applied as a conjunction with the parsed query, never by string concatenation, so an `or` in the query cannot escape it: a directory becomes an anchored `file:` prefix on its slash-separated workspace-relative name with regexp metacharacters escaped, a file becomes the same anchored to its end, and the workspace root adds no constraint. A cancelled context is `aborted` ("Operation aborted"). A build failure other than a bound (for example the shard directory cannot be created) is `index_failed`; a search failure is `search_failed`; a call after `Close` is `index_closed`. A query that runs longer than 10 s is cancelled and returns `search_failed` saying the query timed out and should be narrowed.

Results are files in zoekt's score order, at most `max_files`. Each file shows at most 3 chunks, in zoekt's order (best first) with each chunk's lines in line order, `context_lines` either side, overlapping chunks merged, and every line cut to `tools.SearchLineChars`. The text follows `RenderSearchText`'s shape (`N: matched line`, `N- context line`), with a header per file giving the workspace-relative slash path (control characters replaced with `?`), the file's total match count, a note when matches were not shown, and, where a match falls on a declaration's start line, the declaration's name (`Container.Name` for a member; at most 5 names). The first line of the text states the number of files, the index size, the symbol sources (files per `outline` backend), whether ctags was unavailable, and, when applicable, the partial and dirty-overlay notes. A query with no match is a successful result whose text says so and still shows that line.

When more files matched than `max_files`, the result carries `tools.CapMarker("files", "max_files", limit, 25, "narrow the query or add a path")` and `Metadata.Truncated` with `TruncatedByLines`. When the rendered result exceeds `tools.DefaultByteLimit`, whole files are dropped from the end (keeping at least one, whose chunks are then dropped from the end if it alone is too large), a bytes marker names a narrower call (`path`, `file:`, or fewer `context_lines`), and `TruncatedByBytes` is recorded. `Data` carries `files` (path, score, match count, chunks with line numbers and text, symbol names), `truncated`, `note`, `partial`, `symbol_sources`, `ctags_available`, `files_indexed` and `dirty_files`.

### 4. Building the index

The index is built lazily on the first `code_search` call, never at `New` and never by `find_symbol` (Design Decision 7). Files come from `tools.Walk` rooted at the workspace root with `IncludeHidden: false` and `Options.Ignore`, so every ignore layer applies; non-regular entries are skipped. A file is also skipped if it is larger than 1 MiB or has a NUL byte in its first 8 KiB; skipped files are counted in `Data`. Each file is added to the zoekt builder under its slash-separated workspace-relative name with its content and its symbol sections (Design Decision 4). There is no ctags child process owned by zoekt: `CTagsPath` is left empty.

Symbols come from `outline.OutlineMany` in batches of at most 100 files, with `outline.Options{Root: <workspace root>, Runner: <runner>}`. The runner is `Options.Runner`, or nil with `DisableCtags`, or otherwise `tools.CtagsRunner(Options.Env)` constructed on first use. Whatever backend `outline` used (go/ast, ctags, heuristic, none) is what supplies the sections, so Go files have symbol ranking without ctags. Without universal-ctags the result header says so and says non-Go symbols are the heuristic's column-0 declarations only; this is not an error.

Shards are written to `<TempDir>/agentkit-codesearch-<hash of root>/<run id>/`, where the hash is at least 64 bits of a cryptographic hash of the absolute root and the run id is unique per index instance. `Close` removes the run directory and is idempotent. At each build, run directories of the same hash that have not been modified for 24 hours are removed; a live index touches its own directory at most once an hour while it is queried, and never touches another run's. The directory is created with mode 0700.

Three bounds apply to a build: 100 000 files, 1 GiB of indexed content, and 60 s of wall time (a derived context passed to the walk, to `OutlineMany` and to the builder). When one stops the build, the index keeps what it has, `code_search` answers from it with `partial: true`, `partial_reason` (`files`, `bytes` or `time`), and a note telling the model to narrow `path` or use `search_files`. A partial index is not retried by later calls; only the dirty-threshold rebuild in Requirement 5 replaces it. A cancelled call context during a build is `aborted`, discards the partial build, and leaves the next call free to build again.

### 5. Freshness

`Invalidate(rel)` records `rel` as dirty. `Invalidate("")` marks the whole index for revalidation: the next query walks the workspace as in Requirement 4 and compares `(size, mtime)` with what was indexed, treating a file indexed within the previous 2 seconds as changed (the filesystem timestamp-granularity window `02_symbol_navigation_tools` uses). Files that changed or are new are dirty; files no longer yielded are dirty and gone. A dirty path that is not in the index, and any change to a file whose base name is `.gitignore`, also triggers revalidation, because only the walk's ignore engine can say whether a new file is visible.

At query time, every dirty path is removed from the indexed results. The still-visible dirty files are then searched with the same zoekt engine and the same parsed query: they are indexed into a small overlay shard (rebuilt only when the dirty set changes, with symbols from `outline` run on those files), and the overlay's hits are merged with the indexed hits by score, not appended after them, so a fresh edit cannot fall off the end of the `max_files` cap. The result reports `dirty_files`. A deleted file appears in no result.

When more than 5% of the indexed files are dirty, the whole index is rebuilt under the bounds of Requirement 4 instead of searching an overlay; the new index is swapped in only when it is ready, the old shards are closed and deleted after the swap, and queries arriving meanwhile wait (abandonably) and use the new index. A mark made while a build or revalidation is running is not lost: marks carry a generation counter and a pass clears only marks older than itself. A read-only phase never calls `Invalidate`, so it builds once and never revalidates.

### 6. `find_symbol` through the index

`Index.Symbols` answers from the per-file `outline.File` data the build collected, not from a zoekt query, using the matching rules of `02_symbol_navigation_tools` Requirement 2 (smart-case prefix, `exact`, qualified names, `kind`, `path`) so that `find_symbol` ranks and truncates the result itself with its own code and the contract is identical. It returns `ok=true` only when the index has been built by an earlier `code_search`, is not partial and not closed, and the matches number at most 1 000; otherwise `ok=false` with a nil error. Dirty files in scope are re-outlined before answering, under the same dirty-path rules as Requirement 5 (a revalidation is run if one is pending). The error return is used only for a cancelled context. `SymbolAnswer.FilesIndexed` feeds `find_symbol`'s header, which names the index as its backend. With `ok=false` `find_symbol` uses its own table as if no index were set.

### 7. Concurrency, lifetime and platform support

One lock guards index state; a call waiting behind a build or rebuild abandons the wait when its context ends and returns `aborted`. Queries run concurrently with each other under a read lock. `Close` waits for in-flight queries, then releases the zoekt searcher and deletes the run directory; after `Close`, tools return `index_closed` and `Symbols` returns `ok=false`. The embedder owns `Close`; `tools.All` returns no closer. A run directory abandoned because `Close` was never called is removed by a later run's sweep after 24 hours.

If the pinned zoekt does not build or run on `windows/amd64`, a file constrained to that platform provides `codesearch.New` returning an exported `ErrUnsupported`, while the module still builds there; an embedder falls back to `Options.Index == nil`. On every platform `ErrUnsupported` is the same exported error value, so an embedder can compare with `errors.Is`.

### 8. Risks settled before any code

The first piece of work pins a zoekt release and records, in `codesearch/README.md` and in the spec's external API notes, against that release: the module path and the packages for the index builder, query parser and searcher; whether a shard can be searched from memory or only from a file; the builder's per-document symbol fields and how they are expressed (byte offsets, not lines); the search options that bound result size and wall time; the `go list -m all` output; and the result of building and vetting for the four targets. Any of the following ends the spec as a no-go, recorded with the evidence, instead of being worked around: a cgo file anywhere in the dependency graph under `CGO_ENABLED=1`; `codesearch` itself directly importing a package that pulls in gRPC, Prometheus or an HTTP server (only zoekt's indexing, query and search packages are allowed, and the gRPC, Prometheus and sentry packages those import transitively are accepted: see `docs/errata/03_forbidden_imports_direct_only.md`); or the builder being unable to accept symbol data from outside, since that would force zoekt's own ctags process. Windows failing to build or run is not a no-go: it is the stub in Requirement 7.

### 9. Tests

All tests that need symbols use a fake `Runner` or `DisableCtags`; a test using real universal-ctags skips when it is absent. Tests that exercise the seam run with `Options.Index` set to a fake `tools.Index`.

- **Ranking and shape** on a fixture repository: a `sym:` hit outranks a comment hit; results are grouped by file; 3-chunk and `max_files` caps hold; `max_files` above 25 clamps; marker text is pinned like the existing marker goldens, including the at-the-cap form; the byte cap drops whole files and records `TruncatedByBytes`. Goldens are tied to the pinned zoekt version and say so.
- **Arguments**: empty, 1 025-byte and unparsable queries; negative `context_lines`; `path` outside the workspace refused before any build; `path` naming a file; an `or` query with a `path` stays confined to `path`.
- **Ignore parity**: the set of files the index holds equals the `tools.Walk` set (hidden off, regular, non-binary, at most 1 MiB) on a tree with nested `.gitignore` files, a nested repository, a hidden directory, a symlink, a binary file and an oversized file.
- **Freshness**: `edit_file` then `code_search` returns the new line and not the old; `write_file` of a new file appears; a file deleted through `execute` vanishes; a same-size edit through `execute` inside the 2 s window is seen; a `.gitignore` change through `write_file` is honoured; a mark during a build is not lost (a blocking fake `Runner` and a concurrent write); at 4% dirty no rebuild happens and at 6% one does, counted through a build counter; a fresh edit that would score below the cap still appears.
- **No ctags**: with ctags unavailable `code_search` works, the header says so, and Go files still carry symbols.
- **Bounds**: scaled-down `MaxFiles`, `MaxBytes` and `MaxBuildTime` each give `partial: true` with the right reason; a blocking `Runner` hits the time bound close to the deadline; a cancelled build is `aborted` and the next call builds again.
- **Lifecycle**: `New` touches neither disk nor processes; `Close` deletes the run directory and is idempotent; the sweep removes a 25-hour-old sibling directory and leaves a recent one; calls after `Close` return `index_closed`; concurrent queries pass under `-race`; a call abandoned behind a build returns `aborted`; `Invalidate` never blocks on a running build.
- **`find_symbol`**: before any `code_search`, `Symbols` is `ok=false` and `find_symbol` uses its table; after one, `find_symbol` returns the same matches in the same order as the table over the same fixture; a partial index and more than 1 000 matches are `ok=false`.
- **Seam in the root**: `All` with `Index` nil returns exactly the tool list `02_symbol_navigation_tools` pins; with an index it appends `code_search`; a duplicate name is an error; `write_file`, `edit_file` and the three shell tools invoke `Invalidate` with the right argument, on failure paths too.
- **Policy** (in `codesearch/policy_test.go`): its own `TestNoCgoOutsideStdlib` and `TestCgoProbeIsArmed` with `CGO_ENABLED=1`, a forbidden-import check over `codesearch`'s direct imports (gRPC, Prometheus, HTTP servers; zoekt's transitive graph is accepted, see `docs/errata/03_forbidden_imports_direct_only.md`), and a cross-target build and vet of the four targets; where the stub is used, a test asserts `ErrUnsupported`. The root `internal/policy` tests pass unmodified.
- **Build wiring**: `make test`, `vet`, `lint` and `tidy` cover `codesearch/`; `make check` passes with and without universal-ctags.

### 10. Documentation

In the same change: `codesearch/README.md` (what it is, the go/no-go figures that justified it, how to opt in with the one-line `Options.Index` and the allowlist entry, the Requirement 8 answers, the known differences from `search_files`); `docs/architecture.md` (the module boundary beside `difftest/` and `examples/flatline`, the package table and graph rows for `codesearch`); `docs/DEPS.md` (a new numbered ruling for zoekt confined to the nested module, pinned, with what it buys and why hand-rolling a trigram index with ranking is not credible, and the "nested modules" sentence extended); `docs/configuration.md` (an `Index` row in the `tools.Options` table, the `code_search` limits, and the `codesearch.Options` fields); `docs/cli.md` (the changed `make` target descriptions); and the root `README.md` where it lists modules. `docs/api.md` is not touched.

## Design Decisions

1. **Gate and spec are one thing.** The input is "proposed, gated". The spec is therefore written conditionally: the go figures are the first deliverable, and a no-go closes it. Thresholds are taken verbatim, because the input says they are reviewed with the data and not tuned after it.
2. **A separate nested module, not a build tag.** `docs/DEPS.md` R1 states that only a nested module confines a dependency; `difftest/` is the precedent. The root's `allowedModules` stays untouched.
3. **The seam is a standard-library interface in `tools`.** It lets `codesearch` import `tools` (never the reverse), keeps ADR 01's layering, and makes the index a one-line opt-in.
4. **Symbols come from `outline`, supplied to zoekt, and zoekt's own ctags is off.** The input requires ctags to be invoked with an explicit file list, `--options=NONE` and a reduced environment, which `tools.CtagsRunner` already guarantees and zoekt's builder does not. It also means Go files have ranking symbols from `go/ast` without ctags and that `find_symbol` data agrees with `02_symbol_navigation_tools`. This replaces the input's "without ctags the index has no symbol ranking".
5. **Dirty files are searched through an overlay shard built by the same engine, not by running "regex atoms" by hand.** Zoekt queries are boolean trees of substring, regex, symbol, file and language atoms; re-implementing their semantics would be a second matcher that could disagree with the first. The same query over the same engine cannot.
6. **`Symbols` returns `SymbolAnswer` of `SymbolMatch` and takes a defined `SymbolQuery`.** The input's `[]outline.Decl` has no path, and `SymbolQuery` was not defined. `find_symbol` keeps ownership of ranking and truncation so the contract is the same as without an index.
7. **`find_symbol` never triggers the index build.** Its contract is a 2 s default time bound; a first call that waited up to 60 s for a full zoekt build would break it. The index serves `find_symbol` only after a `code_search` has built it. This narrows the input's "built on the first `code_search` or `find_symbol` call".
8. **Fresh dirty hits are merged by score, not appended.** The input appends them after indexed hits, which under the 10-file default would drop the file the model just edited. Zoekt scores are not perfectly comparable across shards; that imprecision was judged cheaper than hiding fresh edits.
9. **The index has a 1 MiB per-file limit that `search_files` does not have.** The input says to follow `search_files`' per-file-size rule, but no such rule exists in `tools/search.go`. 1 MiB matches `outline`'s default `MaxFileBytes` and zoekt's own default. The difference from `search_files` is documented.
10. **A partial index is not retried.** A 60 s build repeated on every call would make a large repository unusable; the note tells the model to narrow `path` and the dirty-threshold rebuild is the only replacement. This differs from `02_symbol_navigation_tools`, whose partial passes are cheap and incremental.
11. **Shard sweep is by age, keyed on a 64-bit hash plus a per-run directory.** The input sweeps "shards left by a previous run" keyed on the hash, which would delete a live sibling's shards when two tool sets share a workspace. Per-run directories and a 24-hour staleness rule avoid that without platform-specific process checks. `defaultSpillDir`'s 32-bit FNV was not reused because a collision would sweep another workspace's directory.
12. **`path` is a conjunction, a file or a directory, and is validated like `search_files`.** The input's "`file:` constraint" is taken to be an anchored, escaped regexp built as a query node, not a string concatenation, because concatenation with an `or` would escape the constraint.
13. **`context_lines` clamps, a negative value is an error.** The input says "capped"; `search_files` rejects out-of-range values. Clamping above and rejecting below follows the input without a new error shape.
14. **Documentation goes to `codesearch/README.md`, `docs/architecture.md`, `docs/DEPS.md`, `docs/configuration.md`, `docs/cli.md` and the root `README.md`, not `docs/api.md`.** The input names `docs/api.md`, but it documents the HTTP API and the steering table reserves it for REST endpoints; `02_symbol_navigation_tools` made the same ruling. `docs/configuration.md` is where `tools.Options` is already documented.
15. **`tools` exports `CapMarker` and `ClampLimit`.** Steering says to reuse before writing; copying `capMarker` into `codesearch` would let the two diverge. A two-function export is the smallest root change that avoids it.
16. **`code_search` is not in `FileNavigationTools()`.** That list is static and the tool exists only when an embedder opts in; adding it would change `ExecuteFallbackGuideline` conditions for embedders with no index.
17. **A cgo or server-importing dependency is a no-go, not a negotiation.** The input says so for both, and `docs/DEPS.md` R2 says cgo is rejected regardless of allowlist; loosening the forbidden list after seeing the graph would defeat the gate. *Amended: the gate is a check over `codesearch`'s direct imports. zoekt's own index and search packages import gRPC, Prometheus and sentry, so a check over `go list -deps` could never pass, and the project owner accepted that transitive graph rather than record a no-go (`docs/errata/03_forbidden_imports_direct_only.md`). The forbidden list itself was not loosened.*
18. **Windows degrades through a stub, not a failure.** The input names `ErrUnsupported`; the cross-target gate requires the module to build everywhere, so the stub is constrained by build tag inside `codesearch` and tested.
19. **No `recommended_split`.** The module, seam, freshness and tool are one cohesive feature of ten requirements; the foundations were already split into specs 01 and 02.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `01_outline_and_walk` | depends on | Supplies `outline.OutlineMany`, `outline.Options`, `outline.File`/`Decl`/`Backend`, `tools.Walk` with `WalkOptions` (the only walk the index may use) and `tools.CtagsRunner`. |
| `02_symbol_navigation_tools` | depends on and modifies | Supplies `find_symbol`, `SymbolMatch`, the matching and ranking rules the index must reproduce, `Options.Symbols`, and the dirty-marking call sites that now also call `Index.Invalidate`. This spec adds the `Index` branch to `find_symbol` and extends `tools.Options`. |

## Verified External API

The module depends on zoekt, whose source is not in this repository: there is no `codesearch/` directory, no vendored copy, and no `go.sum` entry. Every zoekt symbol below is **unverified**. The signatures are recalled from the library and the input, and Requirement 8 requires them to be checked against the pinned release before any code depends on them. The input warns that the packages have been reorganised, and the module path (`github.com/sourcegraph/zoekt`) and the package names (`index`, `build`, `query`, `shards` have both existed) must be confirmed.

| Symbol | Assumed shape | Status |
|---|---|---|
| zoekt module and pinned version | `github.com/sourcegraph/zoekt`, a release tag chosen in the first task | unverified |
| Index builder (`build.NewBuilder` or `index.NewIndexBuilder`) | accepts a repository description and documents with a name, content, language and symbol sections, then writes one or more shards | unverified |
| Document symbol fields (`Document.Symbols`, `Document.SymbolsMetaData`) | byte-offset sections plus metadata; when present the builder does not run ctags | unverified; Requirement 8 makes a miss a no-go |
| `query.Parse(string) (query.Q, error)` and `query.NewAnd`, file-regexp and substring query constructors | parse zoekt syntax; combine with a path constraint as a conjunction | unverified |
| Searcher (`shards.NewDirectorySearcher(dir)` or `index.NewSearcher(IndexFile)`) with `Search(ctx, q, *SearchOptions) (*SearchResult, error)` | `SearchResult.Files []FileMatch` with file name, score, chunk or line matches | unverified |
| `SearchOptions` (context lines, chunk matches, max displayed documents, max wall time) | bounds on result size and time | unverified |
| Memory search of a shard | possible through an `IndexFile` over a byte slice, or only from a file | unverified; the shard directory under `TempDir` is used either way |
| `tools.Walk(ctx, ws, root, WalkOptions{Ignore, IncludeHidden}, fn)` | per `01_outline_and_walk` | NOT FOUND in the working tree (the spec's tasks are pending); assumed from the spec |
| `tools.CtagsRunner(env) func(ctx, args) ([]byte, error)`, `tools.ErrCtagsUnavailable` | per `01_outline_and_walk` | NOT FOUND in the working tree; assumed from the spec |
| `outline.OutlineMany(ctx, []Source, Options) ([]File, Stats, error)`, `outline.Options{Root, MaxFileBytes, Runner}`, `outline.File`, `outline.Decl` | per `01_outline_and_walk` | NOT FOUND in the working tree; assumed from the spec |
| `tools.SymbolMatch` and the `find_symbol` hooks | per `02_symbol_navigation_tools` | NOT FOUND in the working tree; assumed from the spec |
| `tools.Workspace.Resolve`, `Workspace.Rel`, `tools.IgnoreOptions`, `tools.ReducedEnv`, `tools.MaxSearchContextLines`, `tools.SearchLineChars`, `tools.DefaultByteLimit`, `tools.TruncatedByLines`/`TruncatedByBytes` | as in `tools/path.go`, `tools/ignore.go`, `tools/exec.go`, `tools/search.go`, `tools/limits.go` | verified (read); `Rel` returns OS separators, so `filepath.ToSlash` is applied |
| `tools.CapMarker`, `tools.ClampLimit` | new exported wrappers of `capMarker(noun, param string, limit, max int, alternative string) string` and `clampLimit(n, def, cap int) int` | to be added by this spec; unexported originals verified by reading |
| `core.Tool`, `core.ErrResult`, `core.OKResult`, `core.ToolResult`, `core.ToolMetadata`, `schema.Object`/`Prop`/`Opt`/`String`/`Int` | as used throughout `tools/` | verified (read call sites) |
| Standard library: `context`, `crypto/sha256`, `errors`, `os`, `path/filepath`, `regexp`, `sync`, `time` | stable | stdlib |
