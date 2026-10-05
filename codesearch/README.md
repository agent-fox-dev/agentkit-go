# codesearch — zoekt-backed code search for AgentKit

## Go/No-Go Gate

**Decision: GO**
**Date: 2026-10-02**

### Measured Figures

| Repository | Files | search_files share of read-phase calls | Truncated or retried within 2 turns |
|---|---|---|---|
| agentkit-go (navigation baseline) | ~200 | 32% | 12% |
| sourcegraph/sourcegraph | 18 000+ | 38% | 41% |
| kubernetes/kubernetes | 24 000+ | 35% | 37% |

- **search_files share ≥ 25%:** YES (32–38% across all repositories)
- **Truncated-or-retried ≥ one third:** YES (37–41% on the two large repositories)
- **At least two target repositories of 5 000+ files:** YES (sourcegraph: 18 000+, kubernetes: 24 000+)
- **Requirement 9 checks:** ALL PASS (see below)

The thresholds are the literal values 25% and one third, as specified. They
are not parameters and were not tuned after the data was seen.

## What It Is

A nested Go module that adds a `code_search` tool backed by an in-process
[zoekt](https://github.com/sourcegraph/zoekt) trigram index. The index lives
in this separate module so no embedder that does not import it pays for zoekt's
dependency graph. The root module gains only a small, standard-library-only
seam (`tools.Index`, `Options.Index`) through which the index adds tools,
learns about writes, and may serve `find_symbol`.

## How to Opt In

```go
import "github.com/agentfox/agentkit-go/codesearch"

opts := tools.Options{Workspace: ws /* ... */}
idx, err := codesearch.New(ws, codesearch.Options{
    Ignore: opts.Ignore, // same value as tools.Options.Ignore
})
switch {
case errors.Is(err, codesearch.ErrUnsupported):
    // Windows: run without code search. Options.Index stays nil.
case err != nil:
    return err
default:
    defer idx.Close()
    opts.Index = idx
}

tools, err := tools.All(opts)
```

`New` returns a `tools.Index`, the same on every platform, so this is one code
path with no build constraint. When it returns an error the index is a nil
interface, never a nil `*codesearch.Index` inside one, so passing it on to
`Options.Index` without checking `err` is the same as having no index. Code
that wants the build counters or statistics (`BuildCount`, `BuildStats`,
`Build`) asserts the result to `*codesearch.Index`; the examples do.

Add `"code_search"` to your embedder's tool allowlist.

The index also works without an agent: build it, call the `code_search` tool's
`Execute` and `Symbols` directly, and call `Invalidate` when files change.
[`examples/codesearch`](../examples/codesearch) has runnable programs for both
uses — `search` and `freshness` on their own, `agent` wired into `tools.All`.

## Known Differences from search_files

| Aspect | search_files | code_search |
|---|---|---|
| Query language | RE2 regex | zoekt boolean (substring, regex, sym:, file:, lang:, case:) |
| Walk order | Deterministic (alphabetical) | Ranked by zoekt score (best first) |
| Per-file size limit | None | 1 MiB (matches outline's default) |
| Binary detection | NUL in first 8 KiB | Same |
| Result grouping | By file, match order | By file, best chunks first |
| Exhaustiveness | All matches up to cap | Ranked subset |

## Requirement 9 Answers (Risks Settled)

### Pinned Zoekt Release

- **Module path:** `github.com/sourcegraph/zoekt`
- **Version:** `v0.0.0-20260911061844-153817f643cd`

### Packages Used

| Package | Purpose |
|---|---|
| `github.com/sourcegraph/zoekt` | Core types: `Repository`, `SearchOptions`, `SearchResult`, `FileMatch`, `ChunkMatch`, `Symbol` |
| `github.com/sourcegraph/zoekt/index` | Index builder: `NewBuilder`, `Builder.Add`, `Builder.Finish`, `Document`, `DocumentSection`, `Options` |
| `github.com/sourcegraph/zoekt/query` | Query parser: `Parse(string) (Q, error)`, query combinators |
| `github.com/sourcegraph/zoekt/search` | Shard searcher: `NewDirectorySearcher(dir) (Streamer, error)` |

### Shard Search: File-Based

Shards are searched from files on disk via `search.NewDirectorySearcher(dir)`.
There is no pure in-memory search path; the shard directory under `TempDir` is
always used.

### Per-Document Symbol Fields

The builder accepts symbols as byte-offset sections:

```go
type Document struct {
    Symbols         []DocumentSection      // byte-offset ranges
    SymbolsMetaData []*zoekt.Symbol        // name, kind, parent
}

type DocumentSection struct {
    Start, End uint32
}
```

When `Symbols` and `SymbolsMetaData` are populated and `CTagsPath` is empty,
the builder uses the supplied data without running ctags. This is verified by
TS-03-64.

### SearchOptions Bounding Result Size and Wall Time

```go
type SearchOptions struct {
    MaxDocDisplayCount   int           // cap on files returned
    MaxMatchDisplayCount int           // cap on matches returned
    MaxWallTime          time.Duration // abort after this duration
    NumContextLines      int           // context lines around matches
    ChunkMatches         bool          // use ChunkMatch instead of LineMatch
    ShardMaxMatchCount   int           // per-shard match cap
    TotalMaxMatchCount   int           // total match cap
}
```

### CGO Check (03-REQ-9.2): PASS

```
CGO_ENABLED=1 go list -deps -f '{{.ImportPath}}|{{.Module.Path}}|{{len .CgoFiles}}' ./...
```

No non-stdlib package in the dependency graph ships cgo files.

### Forbidden Imports (03-REQ-9.3): PASS (direct imports only)

No package of this module directly imports gRPC, Prometheus,
`github.com/grpc-ecosystem/…` or `net/http/httptest`. That is what
`TestForbiddenImports` checks, and it checks **direct imports only, by
decision**.

The spec first called for the check to run over `go list -deps`. It cannot:
the packages the spec allows, `zoekt/index` and `zoekt/search`, import these
themselves. The project owner accepted that transitive graph instead of
recording a no-go
([`docs/errata/03_forbidden_imports_direct_only.md`](../docs/errata/03_forbidden_imports_direct_only.md)).
An embedder that imports `codesearch` therefore links, among others:

- `google.golang.org/grpc` and its sub-packages
- `github.com/grpc-ecosystem/go-grpc-middleware/v2`
- `github.com/prometheus/client_golang/prometheus` (and `promauto`),
  `prometheus/client_model/go`, `prometheus/common/expfmt` and
  `prometheus/procfs`
- `github.com/getsentry/sentry-go`
- `github.com/sourcegraph/zoekt/grpc/propagator` and
  `github.com/sourcegraph/zoekt/grpc/protos/zoekt/webserver/v1`

An embedder that does not import `codesearch` links none of them: the root
module's graph does not include this nested module.

### Builder Accepts External Symbols (03-REQ-9.4): PASS

Verified by TS-03-64: a document with `Symbols` and `SymbolsMetaData` set and
`CTagsPath` empty is indexed and searchable with `sym:` queries. The symbol
hit outranks a comment-only hit. No ctags process is spawned.

### Cross-Target Build and Vet (03-REQ-8.7): PASS (3 of 4)

| Target | Build | Vet |
|---|---|---|
| linux/amd64 | ✅ | ✅ |
| linux/arm64 | ✅ | ✅ |
| darwin/arm64 | ✅ | ✅ |
| windows/amd64 | ✅ (stub) | ✅ (stub) |

Zoekt does not build on windows/amd64 (`unix.Umask` undefined). Per
03-REQ-8.6 and 03-REQ-9.5, this is handled by a platform-constrained stub
that returns `ErrUnsupported`, not treated as a no-go.

### Windows Stub (03-REQ-8.6)

On windows/amd64, `codesearch.New` returns a nil `tools.Index` and
`ErrUnsupported`. The error value is the same on every platform
(`errors.Is(err, codesearch.ErrUnsupported)` works). An embedder falls back to
`Options.Index == nil`. The stub `Index` type implements `tools.Index` with
inert methods (`Symbols` never answers, `Tools` is empty), so the code in
[How to Opt In](#how-to-opt-in) compiles on Windows unchanged.

## Goldens

Test goldens in this module are tied to the pinned zoekt version
(`v0.0.0-20260911061844-153817f643cd`). A zoekt upgrade may change ranking
scores and require golden updates.
