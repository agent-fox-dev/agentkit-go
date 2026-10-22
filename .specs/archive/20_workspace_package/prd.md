---
spec_id: "20"
spec_name: "workspace_package"
title: "The workspace package: containment, ignore rules and the walk, with tools built for a workspace"
status: "active"
created_at: "2026-10-15T20:16:45.947372Z"
updated_at: "2026-10-15T20:16:45.947372Z"
intent_hash: "909b8efa77edf1c346a1413a549fb0f8c35d4c092c8efec0e3aa8b5d07fb52ac"
schema_version: 2
source: "docs/prd/12-simplify-the-embedder-contract.md"
---
## Intent

Make the workspace (the containment boundary, its ignore rules and its directory walk) a root-module package of its own, `workspace`, that every scenario opens first. Ignore configuration is given once, to the workspace. The built-in tools, the code-search index and the sandbox tree are built *for* a workspace rather than configuring one through `tools.Options`. This is a deliberate breaking change to the `tools` API. It changes what is exported and from where. It does not change what any tool, walk or containment check does.

## Goals

1. A root-module package `github.com/agent-fox-dev/agentkit-go/workspace` exports `Open`, `Options`, `Workspace` (methods `Root`, `Resolve`, `Rel`, `CheckWriteTarget`, `Ignore`, `Walk`, `Ignored`), `IgnoreOptions`, `NoGlobalExcludes`, `WalkOptions`, `Normalize`, `ErrPathNotAllowed`, `ErrPathMalformed` and `ErrNoHome`. It imports only the standard library and `github.com/bmatcuk/doublestar/v4`, and a test enforces that.
2. `tools.For(ws, opts)` returns the same eleven built-in tools, in the same order and with byte-identical definitions, as `tools.All` returns today, plus `Index.Tools()` when an index is set. The sandbox definition digests (`sandbox/internal/tooldigest`) of every tool are unchanged.
3. Ignore configuration is given in exactly one place, `workspace.Open`. Every directory walk made by `tools` and `codesearch` uses that workspace's rules.
4. These identifiers no longer exist: `tools.Workspace`, `tools.NewWorkspace`, `tools.Walk`, `tools.WalkOptions`, `tools.IgnoreOptions`, `tools.NoGlobalExcludes`, `tools.Normalize`, `tools.ErrPathNotAllowed`, `tools.ErrPathMalformed`, `tools.ErrNoHome`, `tools.All`, `tools.Options.Workspace`, `tools.Options.Ignore`, `(*tools.Workspace).References` and `codesearch.Options.Ignore`.
5. `make check` passes in every module listed in `MODULES`. The exported-API golden is regenerated and now covers `workspace`. Spec 13's hash freeze is retired, and an erratum records why.

## Non-goals

- `agentkit.Toolset`, `agentkit.NewRunner`, `Runner` and the new `agentkit.Config`. Those belong to the later spec **`toolset_runner`**. `agentkit.Config` keeps `Tools`, `Policy`, `Guard` and `After` in this spec.
- `core.Named`, `guard.Allow`, `faux.ToolCall`, `faux.Text` and `sandbox.Open`, and rewriting the examples onto them. Those belong to **`embedder_helpers`**. Examples change here only as far as the renames force.
- The nested `mcpserve` module. That belongs to **`mcp_serve`**.
- The end-to-end rewrite of the README consumer-contract table and of the data-flow sections of `docs/architecture.md`, settling the golden across every changed package, and redoing PRD 12's measurement. Those belong to **`embedder_contract_docs`**. This spec updates the docs only for the identifiers it changes.
- Changing what any tool does or declares. Also out of scope: changing the ignore semantics (sources, precedence, nested-repository boundaries, `.git` always excluded, `.ignore` not a source), the containment rules, the sandbox protocol (`sandbox.ProtocolMinor` stays 2) and the `.proto` files.
- Making `tools.Search`, `tools.SearchIn` or `tools.CountCandidates` take a workspace.
- A virtual filesystem under the workspace (PRD 11 D1), or a cache of parsed ignore files that is kept across calls.
- Compatibility aliases or forwarders in `tools` for the moved names.
- Editing historical PRDs (`docs/prd/*`) or errata other than `docs/errata/13_sandbox_core.md`.

## Background

PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`, decision D3) found that the type every scenario starts from lives inside `tools` and is configured through `tools.Options`. The same ignore configuration therefore has to be passed several times. Today:

- `tools/path.go` holds `Workspace` (one exported field, `Root string`), `NewWorkspace(root)`, `Resolve`, `CheckWriteTarget`, `Rel` and `Normalize`, plus the sentinels `ErrPathNotAllowed` ("tools: path_not_allowed"), `ErrPathMalformed` and `ErrNoHome`.
- `tools/walk.go` holds `Walk(ctx, ws, root, WalkOptions{Ignore, IncludeHidden}, fn)` and the unexported `walk(ctx, root, igOpts, includeHidden, fn)`. The ignore engine is rooted at the *walk root*, and `rel` is relative to it. `TestSearchReadsOnlyItsOwnIgnoreSources` pins that a parent directory's `.gitignore` does not apply.
- `tools/ignore.go` holds `IgnoreOptions{Getenv, Home, GitConfig}`, `NoGlobalExcludes()`, the memoizer `cached()` and the layered `ignoreEngine`. The engine matches through `globMatch` in `tools/glob.go`, which calls `doublestar.Match`.
- `tools.Options` carries `Workspace` and `Ignore`. `tools.All` falls back to the current directory when `Workspace` is nil.
- Ignore configuration is passed again in `codesearch.Options.Ignore`, `sandbox.LocalDir.Ignore` and `rsyncx.RuleOptions.Ignore`.
- Not every walk inside `tools` honours `Options.Ignore`. The reference engine (`ref_cache.go`, `ref_gotypes.go`, `ref_scanner.go`) walks with `WalkOptions{}`. Its index-candidate filter builds `newIgnoreEngine(ws.Root, IgnoreOptions{})`, which loads only the root layer. Both therefore read the real environment, whatever the embedder configured.
- `(*Workspace).References` (`tools/references.go`) is a tool engine hanging off the containment type, and it drags `outline` into it.

The consumers in this repository are the root module's tests and examples, `codesearch` (one `tools.Walk` in `index.go`, one in `dirty.go`, `New(ws *tools.Workspace, …)`, the Windows stub, and dozens of test call sites) and the sandbox tree. The sandbox tree covers `sandbox.LocalDir`, `internal/rsyncx`, `internal/agent`, `internal/attach`, `internal/fakeagent` and `internal/treegen`, plus tests in `sandbox`, `sandbox/cmd/agentkit-sandboxd` and `sandbox/gatewayd`.

`internal/policy/layout_test.go` (TS-13-67) pins the exported API of `.`, `core`, `tools` and `codemode` to `testdata/exported-api.golden`. TS-14-44, TS-15-42 and TS-17-61 also require that golden to hash to spec 13's `goldenSHA13`.

## Requirements

### R1. Opening a workspace

`workspace.Open(root string, opts Options) (*Workspace, error)` turns `root` into an absolute path. An empty root means the process working directory. When the root exists, its symlinks are resolved. A root that does not exist is kept in its lexical absolute form and is not an error. `Open` reads no ignore file and starts no subprocess. It fails only when the working directory or the absolute path cannot be determined.

`(*Workspace).Root() string` returns the resolved root. `Workspace` has no exported fields. A `*Workspace` is immutable after `Open` and safe for concurrent use by any number of goroutines. A `Workspace` not obtained from `Open` is unsupported.

`workspace.Options` has one field, `Ignore IgnoreOptions`. Its zero value reads the real ignore environment, global excludes included.

### R2. Containment and normalization move unchanged

`Resolve`, `Rel`, `CheckWriteTarget` and `workspace.Normalize` behave exactly as their `tools` counterparts do today:

- normalization runs before canonicalization;
- symlinks are resolved against the deepest existing ancestor;
- dangling links are followed to their targets;
- link following stops after 40 hops;
- containment is compared segment by segment;
- `CheckWriteTarget` re-resolves the whole path;
- `~` and `~user` are expanded, and an unknown user is refused;
- a NUL byte is refused;
- an unresolvable HOME is refused rather than falling back to a relative path.

Errors wrap `workspace.ErrPathNotAllowed`, `workspace.ErrPathMalformed` or `workspace.ErrNoHome`. The sentinel texts become `workspace: path_not_allowed`, `workspace: path_malformed` and `workspace: home directory could not be resolved`. Every other part of each message is unchanged: a `path_not_allowed` message still names the root and ends with the next-step sentence. The tools still report these failures with the result code `path_not_allowed`. The existing tests of this behaviour move with the code and assert the same outcomes.

### R3. The workspace owns its ignore rules

`workspace.IgnoreOptions` has the same fields and meaning as `tools.IgnoreOptions` today: `Getenv func(string) string`, `Home func() (string, error)` and `GitConfig func() string`, where a nil `GitConfig` runs `git config`. `workspace.NoGlobalExcludes()` returns options with an empty global layer, as today.

`Open` memoizes the `core.excludesFile` lookup. A workspace runs at most one `git config` subprocess, and only when its rules are first used. `(*Workspace).Ignore() IgnoreOptions` returns the workspace's memoized options so they can be passed on, for example to `sandbox.LocalDir.Ignore`. The ignore engine's semantics are unchanged:

- sources, lowest precedence first: the global excludes file, `.git/info/exclude`, then `.gitignore` files from the scan root down;
- the last matching pattern wins, so `!` re-includes;
- a directory containing `.git` starts a new ignore root;
- any `.git` path segment is always ignored;
- `.ignore` files are not a source.

### R4. Walking

`(*Workspace).Walk(ctx context.Context, sub string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error` replaces `tools.Walk`. `WalkOptions` has one field, `IncludeHidden bool`. The walk always uses the workspace's ignore rules.

How `sub` is handled:

- `""` and `"."` mean the root.
- A relative `sub` is joined to the root.
- An absolute `sub` is accepted as it is.
- `sub` is then cleaned and must lie within the root, compared segment by segment. Otherwise `Walk` returns an error wrapping `ErrPathNotAllowed` before calling `fn`. It returns the same error on a nil receiver.
- `sub` is neither normalized nor symlink-resolved. A caller holding a model-supplied path passes it through `Resolve` first, as `find_files` does today.

What the walk visits is exactly what `tools.Walk` visits today:

- `rel` is slash-separated and relative to `sub`.
- The ignore engine is rooted at `sub`: ignore files above `sub` do not apply, while the global layer and the repository exclude file apply as today.
- Dot-prefixed entries are skipped unless `IncludeHidden` is set.
- Symlinks are not followed.
- Unreadable entries are skipped without error.
- A missing `sub`, or a `sub` that is a file, yields no calls and a nil error.
- The context is checked for every entry, and its error is returned.
- `filepath.SkipDir` and `filepath.SkipAll` from `fn` are honoured. Any other error from `fn` is returned.

### R5. Asking whether a path is ignored

`(*Workspace).Ignored(rel string, isDir bool) bool` takes a workspace-relative path, with either separator. It returns true when `rel`, or any ancestor directory of `rel`, is excluded by the workspace's rules, evaluated the way a walk from the root evaluates it:

- each ancestor is matched with the ignore files of the directories above it, loaded from the root down;
- nested-repository boundaries are honoured;
- an ancestor that is ignored makes `rel` ignored;
- any `.git` segment makes `rel` ignored.

Edge cases:

- `""` and `"."` return false.
- A path that is absolute, or that escapes the root after cleaning, returns true (fail closed).
- A dot-prefixed name is not "ignored". Hidden entries are a walk option, not a rule.

`Ignored` reads the relevant ignore files on every call and keeps no cache between calls. One property must hold for any tree without unreadable entries, including nested `.gitignore` files, negations and nested repositories: the set of entries `ws.Walk(ctx, "", WalkOptions{IncludeHidden: true}, …)` visits equals the set of `filepath.WalkDir` entries for which `Ignored(rel, d.IsDir())` is false.

### R6. Tools are built for a workspace

`tools.For(ws *workspace.Workspace, opts Options) ([]core.Tool, error)` replaces `tools.All`.

- `tools.Options` keeps `SpillDir`, `DisableSpill`, `Env`, `Symbols` and `Index`, with today's defaults. The default spill directory is still derived from a hash of the root, so it is the same directory for the same root.
- A nil `ws` returns an error naming the missing workspace, and no tools. There is no fallback to the working directory.
- `For` returns `read_file`, `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`, `execute` and `run_command`, in that order. Their names, descriptions, input and output schemas, guidelines and execution modes are byte-identical to today's.
- `Index.Tools()` is appended after them when `Index` is set, with the same duplicate-name error as today.
- The shell tools run in `ws.Root()`.
- The individual tool constructors stay unexported.

Every directory walk the tools make goes through `ws.Walk` or `ws.Ignored` with the workspace's rules. That covers `find_files`, `search_files`, the symbol-table build and refresh, the reference engine's Go package load and candidate scan, and the filter over an index's candidate files. The memoization that `fileTools` did through `Options.Ignore.cached()` is now done by the workspace (R3).

### R7. References and the search entry points

`tools.References(ctx context.Context, ws *workspace.Workspace, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)` replaces `(*Workspace).References`, with the same semantics:

- the pass is bounded by the default `SymbolOptions`;
- it uses no reference cache and no index;
- a nil `ws` returns an error;
- an `opts.Path` outside the workspace returns an error wrapping `workspace.ErrPathNotAllowed`, with an empty result;
- a cancelled context returns the context's error.

`ReferenceOptions`, `ReferenceSite` and `ReferenceResult` are unchanged.

`tools.Search(ctx, root, p)`, `tools.SearchIn(ctx, root, p, ig)` and `tools.CountCandidates(ctx, root, p)` keep their root-path signatures and their results. The only change is that `SearchIn`'s `ig` parameter has the type `workspace.IgnoreOptions`. Internally they walk `root` through a workspace opened at `root` with those options.

### R8. The old names are gone, and the package graph is enforced

The identifiers in Goal 4 are deleted. `tools` keeps no alias, type alias, variable or forwarding function for any of them.

`workspace` imports no first-party package: only the standard library and `github.com/bmatcuk/doublestar/v4`. It carries private copies of the few helpers it needs (the error-tolerant `doublestar.Match` wrapper and the hidden-name test). `tools` imports `workspace`, and `MatchGlob` and `ExpandBraces` stay in `tools`. A test in `internal/policy` fails if a non-test file of `workspace` imports anything outside that set. The root `agentkit` package still imports neither `tools` nor `workspace` in non-test code.

### R9. Every consumer in the repository is migrated

- **`codesearch`**:
  - `New(ws *workspace.Workspace, opts Options) (tools.Index, error)` changes signature, in both `index.go` and `stub_windows.go`.
  - `Options.Ignore` is removed.
  - The build walk and the revalidation walk use `ws.Walk` with the workspace's rules.
  - The nil-workspace error is unchanged.
  - The ignore-parity property holds: the indexed file set equals what `ws.Walk` visits with hidden entries off.
- **`sandbox`**:
  - `LocalDir.Ignore` has the type `workspace.IgnoreOptions`, with unchanged semantics.
  - `internal/rsyncx.RuleOptions.Ignore` has the same type, and `visitFrom` walks through `workspace.Open` and `Walk`.
  - `internal/agent` builds its tool set with `tools.For`, over a workspace opened at its root with `workspace.NoGlobalExcludes()` and its explicit environment. Its test seam `build` takes the workspace and the options. Its rsync rules keep using `NoGlobalExcludes`.
  - `internal/attach` (`hostTools`) and `internal/fakeagent` build with `tools.For`, over a throwaway workspace opened with `NoGlobalExcludes`.
  - `internal/treegen` walks through `workspace`.
  - Host-side and agent-side tool digests still agree. A host built from this change still accepts an agent built before it, because the definitions did not change.
  - No `.proto` file or protocol version changes.
- **Tests and examples**: the tests in `sandbox`, `sandbox/cmd/agentkit-sandboxd`, `sandbox/gatewayd`, the root package, `prompt`, `provider/anthropic`, `codemode`, `outline`, `tools`, `codesearch` and `examples/harness` move to the new names.
  - The tests of the moved code move into package `workspace`.
  - `examples/codemode` renames its `workspace()` helper, which would shadow the package, and uses `tools.For` and `ws.Root()`.
  - `examples/codingagent` uses `workspace.Open` and `tools.For`.

### R10. API golden, erratum and documentation

- **API golden**:
  - `internal/policy`'s `apiPackages` gains `"workspace"`.
  - `testdata/exported-api.golden` is regenerated with `AGENTKIT_UPDATE_API_GOLDEN=1`, so it records the new surfaces of `tools` and `workspace`.
  - The `goldenSHA13` hash comparisons in TS-14-44, TS-15-42 and TS-17-61 are removed. Those tests keep comparing the current API to the golden, and TS-17-61 keeps its guard check.
  - TS-13-67 remains the change detector.
  - TS-19-64's check that `internal/policy/testdata` matches the last commit passes once the regenerated golden is committed.
- **Erratum**: `docs/errata/13_sandbox_core.md` gains an entry. It names PRD 12 and this spec, says the frozen exported-API invariant is retired, and says the golden stays as a change detector.
- **Documentation**, updated in the same change, without rewriting the sections owned by `embedder_contract_docs`:
  - `docs/api.md`: a `workspace` row.
  - `README.md`: the quickstart's first two statements, the packages table, the `tools.All` row of the consumer-contract table becoming `tools.For` with a `workspace.Open` row, and the `codesearch` row.
  - `docs/architecture.md`:
    - package-graph rows for `workspace`, `tools`, `codesearch`, the sandbox packages that now import `workspace`, and the test-only packages `internal/fakeagent` and `internal/treegen`;
    - the Packages table;
    - the `Workspace.References` heading;
    - the mentions of `tools.All` and `tools.Walk`.
  - `docs/configuration.md`: a `workspace.Options` section; `tools.Options` without `Workspace` and `Ignore`; `codesearch.Options` without `Ignore`; the type of `LocalDir.Ignore`.
  - `docs/cli.md`: the `agentkit-sandboxd` line.
  - `docs/DEPS.md`: the doublestar row now names `workspace`.
  - `codesearch/README.md`, `examples/codingagent/README.md`, and the doc comments in `sandbox`.
  - `make doc` is run, and `docs/generated/` (including a new `workspace.txt`) is committed.
- **Line budget**: the root module stays within TS-12-14's 20,000-line budget.

## Design Decisions

1. **`WalkOptions` loses its `Ignore` field.** PRD 12 sketched `WalkOptions` "as today", but a per-walk `Ignore` on a workspace that owns its rules would be a second way to say the same thing. A zero value would also silently read the real environment and defeat the workspace's configuration.
2. **`Walk`'s `sub` may be relative or absolute, is checked lexically, and keeps today's semantics: `rel` is relative to `sub` and the ignore engine is rooted at `sub`.** `find_files` passes the absolute path `Resolve` returned, every other caller walks the root, and the per-subdirectory ignore behaviour is pinned by an existing test.
3. **`Ignored` evaluates from the workspace root, applies ancestors' ignore files and nested-repository boundaries, fails closed for an escaping path, and keeps no cache.** This makes it agree exactly with a root walk, which gives a testable property, and freshness is worth more than speed for a question asked once per candidate.
4. **`Open` memoizes the `git config` lookup, and `Ignore()` returns the memoized options.** This moves the `cached()` behaviour (one subprocess per tool set, TS-02-57) to the object that now owns the rules, and lets callers pass the options on without paying again.
5. **`Open` keeps `NewWorkspace`'s tolerance: an empty root means the working directory, and a missing root is not an error.** Parity is the requirement, and the sandbox and the tests open roots that are created later.
6. **`Root` becomes a method, and `Workspace` has no exported fields.** A struct that now carries ignore state must not be constructible with a root nobody resolved (PRD 12 §1).
7. **The sentinels move with a `workspace:` message prefix, and the tool result code stays `path_not_allowed`.** Errors in this codebase carry their package's prefix, no test or golden pins the old text, and the code a model or policy branches on is unchanged.
8. **No aliases are left in `tools`.** PRD 12 D6 rejects keeping the old API alongside the new one, and compile errors at every call site are what make the migration complete.
9. **`tools.For` requires a non-nil workspace and has no working-directory fallback.** PRD 12 D3 makes the workspace an argument, and a silent default is the kind of hidden choice the rename exists to remove.
10. **Every walk in `tools` uses the workspace's rules, including the reference engine and the index-candidate filter, which used to read the real environment and only the root layer.** This removes an inconsistency rather than adding behaviour. Results change only for an embedder who configured non-default ignore options, or for nested `.gitignore` files that the old candidate filter skipped.
11. **`Search`, `SearchIn` and `CountCandidates` stay root-path functions. Only `SearchIn`'s ignore parameter changes type, and they walk through a workspace opened at `root`.** PRD 12 does not ask to reshape them, and `Open` is cheap enough to call per search.
12. **`tools.References` keeps `(*Workspace).References`'s semantics exactly: default bounds, no cache, no index.** Spec 05's erratum records those semantics, and the move is about placement, not behaviour.
13. **`codesearch.Options.Ignore` is removed, and the index uses the workspace's rules.** Its doc already says "the same value the embedder passes to tools.Options". Keeping it would preserve the double configuration PRD 12 exists to remove.
14. **`sandbox.LocalDir` keeps an `Ignore` field, now of type `workspace.IgnoreOptions`, rather than taking a `*Workspace`.** A host directory being synced is not a workspace the tools run over, and PRD 12's migration table specifies exactly this change.
15. **The sandbox agent keeps `NoGlobalExcludes`, and the definitions and protocol do not change.** Digest equality between host and agent is the compatibility contract with agents in already-published images.
16. **The golden is regenerated in this spec, `workspace` joins `apiPackages`, and the three hash pins are retired now rather than in `embedder_contract_docs`.** `make check` cannot pass otherwise, and leaving the moved declarations untracked would blind the change detector. The later spec settles the golden across the remaining packages.
17. **This spec writes the erratum entry, and `embedder_contract_docs` completes it.** The invariant is broken here, so the record belongs here.
18. **Examples are migrated only as far as the renames force.** Rewriting them on helpers belongs to `embedder_helpers`, and rewriting them on the runner belongs to `toolset_runner`.
19. **Historical PRDs and errata other than spec 13's are not edited.** They record decisions at the time they were made, and rewriting them would falsify the audit trail.
20. **The package-graph rule for `workspace` is enforced by a test in `internal/policy`.** That is where the repository keeps its executable build invariants, and PRD 12 states the rule (`workspace` never imports `tools`) as a requirement.
21. **`workspace` carries private copies of the glob wrapper and the hidden-name test, while `MatchGlob` and `ExpandBraces` stay in `tools`.** They are a few lines each, and the alternative is an exported helper or an import from `tools` that the graph rule forbids.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | modifies | TS-13-67's `apiPackages` and `exported-api.golden` change. `docs/errata/13_sandbox_core.md` gains the entry retiring the API freeze. TS-13-16 and TS-13-36 build tools through the renamed API. |
| `14_sandbox_rsync_sync` | modifies | `sandbox.LocalDir.Ignore` and `rsyncx.RuleOptions.Ignore` change type (TS-14-1 pins the field type). TS-14-44's golden hash pin is retired. |
| `15_sandbox_landlock_agent` | modifies | TS-15-42's golden hash pin is retired. |
| `17_sandbox_podman` | modifies | TS-17-61's golden hash pin is retired. Its guard check stays. |
| `19_sandbox_gateway` | modifies | The gatewayd parity tests (TS-19-67) and transfer tests build tools and `LocalDir`s through the renamed API. TS-19-64's clean-testdata check covers the regenerated golden. |
| `09_repository_cut` | modifies | `tools/repository_cut_test.go` (TS-09-8, TS-09-9) calls `tools.All(tools.Options{})` and moves to `tools.For` over an opened workspace. |
| `08_code_mode` | modifies | `codemode/smoke_test.go` and `examples/codemode` build tools through the renamed API. |

## Verified External API

| Package | Symbol | Signature | Status |
|---|---|---|---|
| `github.com/bmatcuk/doublestar/v4` (v4.10.2, root `go.mod`) | `doublestar.Match` | `func Match(pattern, name string) (bool, error)` | Unverified against source (the module cache is outside the repository). The signature is taken from the existing call site `ok, err := doublestar.Match(pattern, name)` in `tools/glob.go` (`globMatch`), which compiles today. |

Standard-library symbols the move relies on (`sync.OnceValue`, `filepath.WalkDir`, `filepath.EvalSymlinks`, `filepath.Rel`, `os.UserHomeDir`, `os/user.Lookup`) are already used by the code that moves.
