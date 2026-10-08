---
spec_id: "05"
spec_name: "find_references"
title: "find_references tool with Go type resolution and enclosing declaration attribution"
status: "active"
created_at: "2026-10-08T07:16:24.29887Z"
updated_at: "2026-10-08T07:16:24.29887Z"
intent_hash: "a0c68f192f79c56af21e6b30669ca9b63efa6ddfc8267725ead12b6b8e98932b"
schema_version: 2
source: "docs/prd/07-add-a-find-references-tool.md"
---
## Intent

Give a model and embedding applications the ability to find all usages and callers of a declaration across a workspace ("who calls this?"). The `find_references` tool combines exact Go type resolution (via `go/parser` and `go/types` with workspace-local imports and stubbed external dependencies) with lexical and outline-aware fallbacks for other languages, attributing every hit to its enclosing declaration while maintaining strict workspace confinement, standard-library-only root dependencies, and cache freshness across file edits.

## Goals

1. A model can discover callers and reference sites of a declaration across the workspace in a single tool call, with each site attributed to its enclosing declaration from the outline (or `<file>` if at top level), ranked so resolved calls precede lexical hits and text matches.
2. For Go code, reference resolution is exact within the workspace: method calls through receivers, interface method invocations, renamed imports, and embedded struct field promotions resolve to the correct declaration, and same-named identifiers on unrelated types or packages are excluded.
3. For non-Go languages, the tool provides identifier-boundary matches attributed to the enclosing declaration from the file outline, ignoring comments and string literals where tokenizers allow.
4. An embedder can query references programmatically without a model turn through an exported method `(*Workspace).References(ctx, target, opts)` on `tools.Workspace`.
5. The root module remains strictly standard-library-only and cgo-free (REQ-GO-11, NFR-COMPAT-06); Go resolution relies solely on `go/parser`, `go/types`, `go/ast`, and `go/token`, without importing `golang.org/x/tools` or external packages. All `internal/policy` tests pass.
6. The tool strictly obeys workspace confinement and ignore rules, utilizing the same ignore engine, hidden-entry exclusion, and path resolution as `find_files`, `search_files`, and `find_symbol`.
7. Reference data is kept fresh with respect to AgentKit tool mutations (`write_file`, `edit_file`, `execute`, `run_command`, `powershell`), invalidating affected packages and files without leaking memory across sessions.

## Non-goals

- Implementing a Language Server Protocol (LSP) client or running `gopls`. Resolution is an in-process Go type-check over workspace sources with external imports stubbed.
- Exact type resolution for languages other than Go. Outlines and tokenizers provide enclosing declaration attribution and identifier boundaries; they do not perform full semantic type analysis for non-Go code.
- Cross-module or external dependency resolution. References into or out of external libraries (`GOROOT`, `GOPATH`, external module cache) are not resolved.
- A persistent or cross-session on-disk index. The reference cache lives in memory for the lifetime of the tool set.
- Altering the behavior, parameters, schemas, or output formats of existing tools (`find_symbol`, `search_files`, `file_outline`).
- Building call graphs or repository maps within AgentKit itself (that is the embedder's domain).
- Deciding which agent-fox phases receive the tool (that remains agent-fox policy per ADR 07).

## Background

Today, AgentKit provides declaration discovery within a single file via `file_outline` and workspace-wide declaration lookup via `find_symbol` (delivered by spec `02_symbol_navigation_tools`). However, answering "who calls this?" currently forces models to fall back to `search_files` with regex patterns like `\bName\b`.

Reading the codebase reveals several key conventions and components:
- `tools/tools.go`: `All(opts)` builds `fileTools` and returns tools in a pinned order. `FileNavigationTools()` returns a static list of navigation tool names (`list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`).
- `tools/path.go`: `Workspace` defines workspace confinement with `Resolve` and `Rel`. All file access must be contained within `Workspace.Root`.
- `tools/symbols.go`: `symbolTable` provides a thread-safe, lazily built, bounded in-memory cache of declarations, using `SymbolOptions.MaxFiles` (default 50 000) and `SymbolOptions.MaxDuration` (default 2 s). Mutations in `write_file` and `edit_file` call `markTableDirty(rel)`, and shell tools call `markTableRevalidateAll()`.
- `tools/symbol_rank.go`: Implements deterministic ranking and `isTestFile(relPath)` to classify Go (`_test.go`), Python (`test_*.py`, `*_test.py`), JS/TS (`*.test.*`, `*.spec.*`), and other language test files and directories (`test/`, `tests/`, `spec/`).
- `tools/limits.go`: Implements truncation markers via `capMarker`, `SymbolMarker`, and `SymbolPartialMarker`.
- `outline/outline.go`: Defines `Decl`, `File`, `Kind`, and `OutlineMany`, providing declaration start/end lines and container relationships.
- `internal/policy/deps_test.go`: Enforces cgo-freedom and standard-library purity in the root module.

The `find_references` tool extends these foundations by adding exact Go type resolution and outline-attributed lexical navigation.

## Requirements

### 1. `find_references` Tool Registration and Schema

The tool must be named `find_references` and registered in `tools.All()`. It must be added to `tools.FileNavigationTools()`, expanding the navigation tool set from five to six names. The tool must have `Builtin: true`, `ExecutionMode: Parallel`, and read-only status (not listed in `guard.ShellToolNames`).

Input Schema:
- `name` (string, required): Declaration name to find references for. May be unqualified (`Run`) or qualified with container (`Runner.Run`). Maximum 256 bytes.
- `path` (string, optional): Directory path to scope the reference search within. Must resolve inside `Workspace.Root` via `Workspace.Resolve` and exist as a directory; otherwise returns `path_not_allowed` or `invalid_arguments`.
- `kind` (string, optional): Kind filter for the referenced declaration (`func`, `method`, `type`, `class`, `interface`, `enum`, `trait`, `const`, `var`, `module`, `macro`).
- `include_tests` (bool, optional, default `true`): When false, reference sites in test files (per `isTestFile`) are omitted.
- `max_results` (int, optional, default `30`, cap `100`): Maximum reference sites to return.

Prompt Guidelines:
- Must specify: `"Use find_symbol for where a name is declared, find_references for who uses it, and search_files for text."`

### 2. Declaration Resolution from Symbol Table

When `find_references` receives a `name`:
1. It queries the in-memory symbol table (sharing `symbolTable` from `find_symbol`) to locate matching declarations.
2. If `kind` or `path` is specified, candidate declarations are filtered accordingly.
3. If multiple declarations match, the tool selects the highest-ranked declaration using `find_symbol` ranking criteria (exact name match, exported, non-test file, shorter path, earlier line).
4. If no declaration matches the name, the tool does not fail; it performs a lexical/text reference search across workspace files, labels the backend as `text`, and indicates in the output header that zero declarations were matched.

### 3. Go Type Resolution Engine (`resolved` Confidence)

For Go files in the workspace:
1. Packages are parsed using `go/parser` and checked using `go/types`.
2. A workspace-local importer maps imports matching the workspace module path (read from the root `go.mod` if present, or workspace-relative directories) to workspace source packages.
3. Any external import path (standard library `fmt`, `os`, or third-party packages) is answered by the importer with an empty synthetic `*types.Package` defining no symbols.
4. `types.Config` must set `FakeImportC = true`, and `Config.Error` must collect errors rather than aborting. Type checking is incomplete by design and must never fail or panic.
5. Identifiers at use sites are resolved via `types.Info.Uses` and `types.Info.Defs`:
   - Method calls through receivers (`r.Run(...)`) resolve to the receiver method.
   - Interface method invocations resolve to the interface method declaration.
   - Calls across renamed package imports resolve to the target declaration.
   - Embedded struct field and method promotions resolve to the promoted declaration.
   - Methods on unrelated types or same-named identifiers in other packages are excluded.
6. A Go identifier matching the target that resolves to the target declaration's `types.Object` is assigned `resolved` confidence.
7. An identifier in Go source whose type cannot be fully resolved because its type originates from a stubbed external import is classified as `lexical` rather than dropped.

### 4. Lexical and Text Reference Matching (`lexical` and `text` Confidence)

For non-Go files, or Go files where full type resolution is not applicable:
1. Candidate files mentioning `target.Name` are found using the workspace walk (respecting `.gitignore` and hidden entries) or through the optional codesearch index if configured.
2. In files covered by the `outline` package (Python, TypeScript, JavaScript, Rust, C, C++, C#, Java, Kotlin, Ruby), the file is scanned for identifier-boundary matches (`\b<name>\b`).
3. Matches occurring in source code outside comments and string literals are assigned `lexical` confidence.
4. Matches occurring inside comments or string literals, or in files that cannot be outlined, are assigned `text` confidence.
5. If the target declaration was not found in the symbol table, all matching sites across all files are assigned `text` confidence.

### 5. Enclosing Declaration Attribution

Every reference site must be attributed to the declaration enclosing its line number:
1. For each file containing a reference site, the file's declaration outline (`outline.File.Decls`) is inspected.
2. The site's 1-based line number is mapped to the innermost declaration where `StartLine <= Line <= EndLine`.
3. If an enclosing declaration is found, the site's `Enclosing` field is set to that `outline.Decl`, and its formatted signature or `Kind Name` is rendered.
4. If the site is at the top level of the file outside any declaration, `Enclosing` is a zero `outline.Decl` with `Kind: "file"`, rendered as `<file>`.

### 6. Ranking, Bounds, and Truncation

Ordering of reference sites:
1. Confidence: `resolved` hits first, then `lexical`, then `text`.
2. Within the same confidence: non-test files (per `isTestFile`) before test files.
3. Within test status: alphabetical workspace-relative path.
4. Within file: ascending 1-based line number, then ascending column.

Bounds and Truncation:
1. Type-checking and outline passes are bounded by `SymbolOptions.MaxFiles` (default 50 000) and `SymbolOptions.MaxDuration` (default 2 s).
2. If a bound is exceeded or context expires, the pass stops gracefully, returns sites collected so far, and marks the result with `Partial: true` and an advisory partial marker (`SymbolPartialMarker`).
3. If the total sites exceed `max_results` (clamped between 1 and 100, default 30), the site list is truncated to `max_results`, `Truncated: true` is set, and a truncation marker is appended using `CapMarker("references", "max_results", limit, 100, "narrow with path or kind")`.

### 7. Result Rendering and Structured Output

The tool returns a `core.ToolResult` with:

Text Format:
1. Header line: `find_references <name>  (<backend>, <N> references in <M> files; <K> text matches)`
   - When partial: appends partial note.
2. File groups: slash-separated relative path on its own line.
3. Site lines indented under file:
   `  L<line>  <confidence>  <enclosing>  <source_line>`
   - `<enclosing>` is padded/formatted to align, showing the signature or kind/name, or `<file>`.
   - `<source_line>` is trimmed of leading/trailing whitespace, capped at 200 bytes, with ASCII control characters stripped or replaced with spaces.
4. Any truncation or partial markers appended at the end.

Structured Data (`ToolResult.Data`):
- `target`: `outline.Decl` of the referenced target (or empty if unresolved).
- `sites`: Slice of structured reference objects containing:
  - `path`: string (workspace-relative, slash-separated)
  - `line`: int (1-based)
  - `column`: int (1-based byte offset)
  - `confidence`: string (`"resolved"`, `"lexical"`, `"text"`)
  - `enclosing`: `outline.Decl`
  - `source`: string (trimmed, max 200 bytes)
- `backend`: string (`"go/types"`, `"lexical"`, or `"text"`)
- `partial`: bool
- `truncated`: bool
- `packages_checked`: int
- `errors`: int (type-checking errors collected from stubbed imports)

### 8. Freshness, Invalidation, and Caching

1. The reference cache is created lazily on first reference lookup and stored in memory for the lifetime of the tool set.
2. File mutations through `write_file` and `edit_file` mark the written path dirty upon lock release.
3. Command executions via `execute`, `run_command`, and `powershell` mark all cached entries for revalidation.
4. Before answering a query:
   - Dirty Go packages are re-parsed and re-checked.
   - Dirty non-Go files are re-outlined.
   - Files deleted since the last pass are purged from cache.
5. If `.gitignore` is modified, the candidate file cache is revalidated.

### 9. Exported Embedder Seam on `tools.Workspace`

Export the following types and method on `*Workspace`:

```go
type ReferenceOptions struct {
    Path         string // Subdirectory filter ("" for entire workspace)
    IncludeTests bool   // Whether to include test files (default false in struct, tool defaults to true)
    MaxResults   int    // 0 or negative defaults to 30; capped at 100
}

type ReferenceSite struct {
    Path       string       // Slash-separated workspace-relative path
    Line       int          // 1-based line number
    Column     int          // 1-based byte column
    Confidence string       // "resolved", "lexical", or "text"
    Enclosing  outline.Decl // Enclosing declaration, or Kind "file" at top-level
    Source     string       // Trimmed source line, max 200 bytes, no control chars
}

type ReferenceResult struct {
    Target          outline.Decl
    Sites           []ReferenceSite
    Backend         string
    Partial         bool
    PackagesChecked int
    Errors          int
    Truncated       bool
}

func (ws *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)
```

The method must enforce workspace confinement, return identical results to `find_references`, and allow programmatic analysis (e.g., repository maps, static analysis) without requiring model execution.

### 10. Policy Compliance and Documentation Updates

1. Root module purity: No new entries in root `go.mod`. No external imports. `internal/policy` tests (`TestNoCgoOutsideStdlib`, `TestCgoProbeIsArmed`) must pass. Cross-compilation targets (`linux/amd64`, `linux/arm64`, `darwin/arm64`, `windows/amd64`) must build.
2. System prompt tests and goldens:
   - `prompt/testdata/golden/system_prompt_default.txt` and `system_prompt_custom.txt` updated to include `find_references` guideline.
   - Navigation tool tests in `tools/wiring_test.go` and `prompt/golden_test.go` updated to assert 6 tools in `FileNavigationTools()`.
3. Documentation updates:
   - `docs/architecture.md`: Document `find_references` and the reference table beside the symbol table.
   - `docs/api.md`: Document `Workspace.References`, `ReferenceOptions`, `ReferenceSite`, and `ReferenceResult`.
   - `README.md`: Add `find_references` to the package tools table; remove "references and callers" from "What is not built".
   - `docs/GAPS.md`: Move "Symbol references and callers" from Deferred to Fixed, citing this spec.

## Design Decisions

1. **Target Declaration Disambiguation**: When a queried `name` matches multiple symbols in the symbol table, the tool filters by `kind` and `path` first. If multiple candidates remain, it selects the top-ranked declaration based on `find_symbol` ranking rules (exact name, exported, non-test file, shorter path, earlier line). If no declaration matches, it falls back to a workspace-wide text search without failing.
   *Why*: Matches the precedent set by `find_symbol` while maintaining graceful degradation for undeclared identifiers.

2. **Workspace-Local Go Import Resolution with Stubbed External Packages**: The Go importer resolves import paths belonging to the workspace module from local sources, while all external import paths (standard library and third-party) return an empty synthetic `*types.Package` with `Config.FakeImportC = true` and non-fatal error collection.
   *Why*: AgentKit must remain strictly standard-library-only, cannot invoke `go list` or shell processes, and must never touch `GOROOT`, `GOPATH`, or the network.

3. **Demoting Incompletely Typed Identifiers to `lexical`**: In Go source, if an identifier matches the target name but cannot be resolved by `go/types` because its receiver or parameter type originated from a stubbed external package, it is reported with `lexical` confidence rather than dropped.
   *Why*: Dropping unresolved sites would create false negatives when integrating with external frameworks, while reporting them as `resolved` would provide false certainty.

4. **Lexical Matching for Non-Go Languages via Outline Attribution**: For non-Go languages, candidate files are tokenized or regex-matched at identifier boundaries (`\b<name>\b`), filtering comments and strings where outline tokenizers allow, and attributed to the innermost enclosing declaration in `outline.Decl`.
   *Why*: Delivers structured, declaration-attributed caller information for all supported languages without requiring full type engines for each language.

5. **In-Memory Caching and Freshness via Existing File Mutation Hooks**: The reference cache is retained in-process within `fileTools` across queries, using the invalidation hooks already wired into `write_file`, `edit_file`, and shell commands to mark packages dirty.
   *Why*: Minimizes query latency on large repositories while guaranteeing that model file edits are never served stale caller data.

6. **Candidate File Discovery with Optional Index**: Candidate files are discovered through the workspace walk using `tools.Walk` with layered ignore checks; if `tools.Options.Index` is provided and implements candidate lookup, it is queried to accelerate candidate file identification.
   *Why*: Preserves result parity between native and indexed runs without breaking the frozen `tools.Index` interface.

7. **Exported Seam directly on `Workspace`**: `Workspace.References` is exported on `*Workspace`, while `find_references` handles model argument parsing, symbol table resolution, string trimming, and text rendering.
   *Why*: Decouples semantic caller analysis from the model-facing tool envelope, allowing embedders to inspect references directly without parsing JSON or text outputs.

8. **Test File Classification via `isTestFile`**: The tool reuses `tools.symbol_rank.go`'s `isTestFile` helper to filter test hits when `include_tests` is false and to sort non-test hits ahead of test hits when true.
   *Why*: Guarantees consistent test file classification across both `find_symbol` and `find_references`.

9. **Deterministic Result Sorting**: Results are sorted strictly by confidence tier (`resolved` < `lexical` < `text`), then non-test before test, then slash-separated path, then line number, then column.
   *Why*: Guarantees byte-identical output across repeated runs on identical trees, preventing flaky tests and non-deterministic agent trajectories.

10. **Result Limits and Partial Execution Bounds**: Maximum results defaults to 30 and caps at 100 with `CapMarker` truncation markers. Time and file counts are bounded by `SymbolOptions.MaxDuration` (2 s) and `SymbolOptions.MaxFiles` (50 000), returning partial results marked `partial: true` upon timeout.
    *Why*: Prevents denial of service on massive repositories and guarantees prompt response times within agent execution loops.

## Dependencies

| Existing Spec | Reason |
|---|---|
| `01_outline_and_walk` | Provides `outline` package (`outline.Outline`, `outline.Decl`, `outline.File`), `tools.Walk`, and `tools.CtagsRunner`. |
| `02_symbol_navigation_tools` | Provides `find_symbol`, `symbolTable`, `SymbolOptions`, `SymbolMatch`, `FileNavigationTools()`, and dirty-marking hooks in `write_file`, `edit_file`, and shell tools. |
| `03_indexed_code_search` | Provides optional `tools.Index` integration and index candidate acceleration. |
| `04_runner_and_tool_metadata` | Defines tool metadata conventions and prompt guideline assembly. |
