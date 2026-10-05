# Add an indexed code search module

Status: **proposed, gated.** This PRD is written now so the design is settled
early. It is built only if the go/no-go in §2 passes. Where it sits relative
to agent-fox is recorded in agent-fox
[ADR 07](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/adr/07-split-code-navigation-between-agentkit-and-agent-fox.md).
It builds on [PRD 04](04-add-symbol-navigation-tools.md).

## 1. The problem

After PRD 04, a model can outline a file and find a declaration. What it
still cannot do is ask a **ranked** question about the codebase: *where is
retry handled*, *which files deal with compaction*. `search_files` is a
grep. It returns up to 100 matching lines in walk order, with no notion that
a match in a declaration name outranks one in a comment, or that a file
with twelve matches is more relevant than twelve files with one each. On a
large repository the useful hit is often past the cap. The model then
narrows the regex and tries again, and each retry costs a turn.

[zoekt](https://github.com/sourcegraph/zoekt) (Go, Apache-2.0) is a trigram
index built for exactly this. It has a query language (`sym:`, `file:`,
`lang:`, `case:`, boolean operators and regex), ranking that uses symbol
information from universal-ctags, and results grouped by file with the
best-scoring chunks first. It is Go and usable as a library, so it can run
in-process with no daemon.

[techquestsdev/code-search](https://github.com/techquestsdev/code-search)
wraps zoekt in a multi-repository service: an API server, an indexer that
clones from forges, PostgreSQL or MySQL, Redis and a web UI. That is a
deployment, not a library, and it targets organisation-wide search across
remote repositories. It is out of scope here (§3). The working tree of a
single agent run needs the engine, not the service.

## 2. Go / no-go

This module adds zoekt's whole dependency graph to any embedder that imports it,
so it is built only on evidence. The evidence is agent-fox's per-phase
`tool_calls` and `tool_result_bytes` (agent-fox PRD 06, item A), collected
after PRD 06 and PRD 04 have shipped, on the navigation baseline plus at
least two target repositories of 5 000 files or more.

**Go** when, on the large repositories, either:

- `search_files` calls are still at least 25% of read-phase tool calls, or
- at least a third of `search_files` results are truncated, or followed
  within two turns by another `search_files` call in the same directory
  (a retry).

**No-go** otherwise. The PRD is then closed, with the numbers recorded in
it. The thresholds are this PRD's first guess. They are reviewed together
with the data, not tuned after it.

## 3. Goals and non-goals

**Goals.**

1. A `code_search` tool that takes zoekt's query language and returns
   ranked, file-grouped results within the same size limits as the other
   tools (REQ-TOOL-09).
2. The root module is unaffected: no new requirement in its `go.mod`, no
   change to `allowedModules`, and no cost to an embedder that does not
   import the module.
3. The index covers exactly what the other file tools see: the working tree,
   including uncommitted and untracked-but-not-ignored files. It walks
   through PRD 04's `tools.Walk`, never zoekt's git indexer, which indexes
   commits.
4. A result never shows file content that is out of date (§5.3).
5. `find_symbol` can use the index as its backend when the module is
   present, with the same contract as PRD 04.

**Non-goals.**

- **Replacing `search_files`.** Its contract is RE2, walk order and
  ripgrep/native parity, and it has a test suite pinning that. zoekt's
  regex dialect, ranking and ordering differ. Putting zoekt behind
  `search_files` would break its parity promise, so zoekt gets its own tool.
- **A long-running service, cross-run persistence or a web UI.** The index
  is built per tool set and discarded with it. An index that outlives the
  run, and code-search's multi-repository server, are possible later as a
  separate PRD on the `hub` side, reached over MCP.
- **Semantic or embedding search.** No model calls inside a tool.
- **Searching more than one repository.**

## 4. Shape

### 4.1 A separate module

```
agentkit-go/
  codesearch/
    go.mod        module github.com/agentfox/agentkit-go/codesearch
                  require github.com/sourcegraph/zoekt <pinned>
                  replace github.com/agentfox/agentkit-go => ..
    index.go      build and refresh
    search.go     query → ranked result
    tool.go       the code_search core.Tool
    policy_test.go
```

This mirrors `difftest/`, whose `go.mod` comment already states the rule:
*"Whatever that costs in dependencies must not land in the graph of anyone
who imports AgentKit (REQ-GO-11), and the only way to guarantee that is a
module boundary rather than a build tag."*

### 4.2 The seam in the root module

The root defines the interface, standard library only, in `tools`:

```go
// Index is an optional accelerator for the file tools.
type Index interface {
    // Symbols answers find_symbol. ok=false means "use the built-in table".
    Symbols(ctx context.Context, q SymbolQuery) (res []outline.Decl, ok bool, err error)
    // Tools returns extra tools backed by this index (code_search).
    Tools() []core.Tool
    // Invalidate is called with a workspace-relative path after a write,
    // or with "" after any shell tool ran.
    Invalidate(rel string)
    Close() error
}

type Options struct {
    // …existing fields…
    Index Index // nil: no index, PRD 04 behaviour
}
```

`All()` appends `Index.Tools()` when `Index` is set. `codesearch.New(ws,
opts) (tools.Index, error)` is the module's single entry point. agent-fox,
or any embedder, opts in with one line in its tool construction, plus
`code_search` in whichever phase's allowlist should have it.

## 5. Behaviour

### 5.1 `code_search`

```
code_search(query: string, path?: string, max_files?: int, context_lines?: int)
```

- `query` is zoekt's query syntax, documented in the tool description with
  four examples: `sym:Runner`, `retry file:\.go$ -file:_test`,
  `lang:python "def load"`, `(compaction or summarize) case:no`.
- `path` is added as a `file:` constraint on the workspace-relative prefix,
  after `Workspace.Resolve` has confirmed it is inside the workspace.
- Results come back grouped by file, in zoekt's score order. Each file shows
  at most 3 chunks, with `context_lines` (default 2, capped at
  `MaxSearchContextLines`) around each match. `max_files` defaults to 10 and
  is capped at 25. The 50 KB byte cap and the REQ-TOOL-09 truncation marker
  apply as in `search_files`.
- The text is rendered like `RenderSearchText`, with a per-file header
  showing the match count and, where a match is in a declaration, the
  symbol's name.
- `PromptGuidelines`: *"Use code_search for ranked questions about the
  codebase; search_files for an exhaustive regex scan."*

### 5.2 Building the index

- The index is built lazily, on the first `code_search` or `find_symbol`
  call. Files come from `tools.Walk`, with the same ignore, hidden-entry,
  binary and per-file-size rules as `search_files`. Each file is added
  through zoekt's index builder with its workspace-relative name.
- Symbols come from universal-ctags when it is on PATH, invoked as PRD 04
  §5 requires (explicit file list, `--options=NONE`, reduced environment).
  Without ctags the index has no symbol ranking. The tool says so in its
  result header, and `find_symbol` keeps using PRD 04's table.
- Shards are written under a per-workspace directory below `os.TempDir()`,
  named by a hash of the root, as `defaultSpillDir` does. They are removed
  by `Close`, and by a sweep of shards left by a previous run, keyed on
  that hash, at the next build.
- Bounds: 100 000 files, 1 GiB of indexed content, 60 s of build time. When
  a bound is hit, the build stops. `code_search` then answers from the
  partial index with `partial: true` and a note to narrow `path`.

### 5.3 Freshness

- `Invalidate(rel)` records a dirty path. `Invalidate("")` (after a shell
  tool) makes the next query compare `(size, mtime)` across the walk.
- At query time, dirty files are removed from zoekt's results and searched
  directly. The query's regex atoms are run over the current content of the
  dirty files, and those hits are merged after the indexed hits. When more
  than 5% of files are dirty, the index is rebuilt instead.
- Read-only phases never call `Invalidate`, so in practice they build the
  index once per phase.

## 6. Risks to settle before building

These are open, and each must be answered in the spec's `external_apis`
against the pinned zoekt version, not assumed:

- **The library API.** zoekt's packages have been reorganised over time,
  with builder, query parser and searcher moved between packages. The
  module path, the builder entry point, and whether a shard can be searched
  from memory or only from a file must be checked at the pinned version.
- **cgo.** The module needs its own copy of `TestNoCgoOutsideStdlib`, run
  with `CGO_ENABLED=1`. A cgo dependency anywhere in zoekt's graph is a
  no-go.
- **Windows.** zoekt memory-maps shards. NFR-COMPAT-06's matrix includes
  `windows/amd64`. If zoekt does not build or run there, `codesearch.New`
  returns `ErrUnsupported` on Windows. The module still builds, behind a
  build-constrained stub, and the embedder falls back to PRD 04 behaviour.
- **Dependency weight.** `go list -m all` for the module is recorded in
  this PRD's spec. Packages that pull in servers (gRPC, Prometheus, the
  web server) must not be imported by `codesearch`. Only the indexing,
  query and search packages are allowed. *Scope, as delivered: this is a
  direct-imports check. Those indexing and search packages pull gRPC,
  Prometheus and sentry in transitively, and that graph is accepted; see
  [`docs/errata/03_forbidden_imports_direct_only.md`](../errata/03_forbidden_imports_direct_only.md).*
- **Regex dialect.** zoekt's regex support is close to RE2 but not
  identical. The tool description says "zoekt query syntax", never "RE2".

## 7. Tests

- Golden results for a fixture repository: ranking puts a `sym:` hit above
  a comment hit, and grouping and caps hold.
- Ignore parity: the set of files the index holds equals what `tools.Walk`
  yields on the same fixture.
- Freshness: `edit_file` then `code_search` returns the new line and not
  the old one. Below and above the 5% threshold, a rebuild happens only above
  it.
- No-ctags path: with `ctags` absent from PATH the tool works and says
  symbol ranking is off.
- Module policy: no cgo, and the root module's `internal/policy` unchanged
  and green.
- Cross-target: `go build ./...` in `codesearch/` on all four targets. On
  any target where the stub is used, a test asserts `ErrUnsupported`.

## 8. Documentation

- `codesearch/README.md`: what it is, the go/no-go result that justified
  it, how to opt in, and the risks in §6 as answered.
- `docs/api.md`: `tools.Index` and `Options.Index`.
- `docs/architecture.md`: the module boundary, beside `difftest/`.
- `docs/DEPS.md`: zoekt, pinned, with the reason.
