# Code search internals

How the nested `codesearch` module is built, why it exists, and the decisions
behind it. To use it, read [`codesearch/README.md`](../codesearch/README.md)
and the [user guide](guide.md#indexed-code-search); its options are under
[`code_search`](configuration.md#code_search-when-a-codesearch-index-is-set)
in the configuration reference.

## Contents

- [Shape](#shape)
- [Why it exists: the go/no-go gate](#why-it-exists-the-gono-go-gate)
- [Differences from `search_files`](#differences-from-search_files)
- [Design decisions](#design-decisions)
- [Test goldens](#test-goldens)

## Shape

`codesearch` is a separate Go module, so an embedder that does not import it
does not link zoekt's dependency graph. The root module gains only a small,
standard-library-only seam: `tools.Index` and `tools.Options.Index`, through
which an index adds tools, learns about writes (`Invalidate`) and may answer
`find_symbol`. An embedder that restricts its tools with an allowlist adds
`code_search` to it. `codesearch.New(ws, opts)` indexes exactly the files
`ws.Walk` visits, so it shares the workspace's ignore rules.

## Why it exists: the Go/No-Go gate

Before adding a second search tool, the project measured how much agents rely
on `search_files` while reading code, and how often its results were cut off or
retried on large repositories. The module was built only because the gate
passed.

**Decision:** GO. **Date:** 2026-10-02.

### Measured figures

| Repository | Files | search_files share of read-phase calls | Truncated or retried within 2 turns |
|---|---|---|---|
| agentkit-go (navigation baseline) | ~200 | 32% | 12% |
| sourcegraph/sourcegraph | 18 000+ | 38% | 41% |
| kubernetes/kubernetes | 24 000+ | 35% | 37% |

- **search_files share ≥ 25%:** YES (32–38% across all repositories)
- **Truncated-or-retried ≥ one third:** YES (37–41% on the two large repositories)
- **At least two target repositories of 5 000+ files:** YES (sourcegraph: 18 000+, kubernetes: 24 000+)
- **Requirement 9 checks:** all pass (see [Design decisions](#design-decisions))

The thresholds are the literal values 25% and one third, as specified. They
are not parameters and were not tuned after the data was seen.

## Differences from `search_files`

| Aspect | search_files | code_search |
|---|---|---|
| Query language | RE2 regex | zoekt boolean (substring, regex, sym:, file:, lang:, case:) |
| Walk order | Deterministic (alphabetical) | Ranked by zoekt score (best first) |
| Per-file size limit | None | 1 MiB (matches outline's default) |
| Binary detection | NUL in first 64 KiB (ripgrep's buffer); a later NUL ends the file's search | NUL anywhere in the file (zoekt's own check) |
| Other skipped files | None | Over 20 000 distinct trigrams (lock files, source maps, minified bundles) and under 3 bytes: zoekt stores them as "not indexed", so they are skipped and reported in `skipped` (`binary`, `oversized`, `too_many_trigrams`, `too_small`), never counted in `files_indexed` |
| Encoding | A byte-order mark is honoured: UTF-8 BOM stripped, UTF-16 transcoded | Bytes as stored |
| Result grouping | By file, match order | By file, best chunks first |
| Exhaustiveness | All matches up to cap | Ranked subset |

## Design decisions

### Pinned zoekt release

- **Module path:** `github.com/sourcegraph/zoekt`
- **Version:** `v0.0.0-20260911061844-153817f643cd`

### Packages used

| Package | Purpose |
|---|---|
| `github.com/sourcegraph/zoekt` | Core types: `Repository`, `SearchOptions`, `SearchResult`, `FileMatch`, `ChunkMatch`, `Symbol` |
| `github.com/sourcegraph/zoekt/index` | Index builder: `NewBuilder`, `Builder.Add`, `Builder.Finish`, `Document`, `DocumentSection`, `Options` |
| `github.com/sourcegraph/zoekt/query` | Query parser: `Parse(string) (Q, error)`, query combinators |
| `github.com/sourcegraph/zoekt/search` | Shard searcher: `NewDirectorySearcher(dir) (Streamer, error)` |

### Shard search is file-based

Shards are searched from files on disk via `search.NewDirectorySearcher(dir)`.
There is no pure in-memory search path; the shard directory under `TempDir` is
always used.

### Per-document symbol fields

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

Each section is the declared identifier on its start line — what `sym:`
matches — not the declaration's body. zoekt rejects a shard whose sections
overlap, and body ranges nest (a class spans its methods; Go's
`var a, b = 1, 2` gives two names one range), so a section that would overlap
the one before it is dropped.

### Search options that bound result size and wall time

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

### No cgo in the dependency graph

```
CGO_ENABLED=1 go list -deps -f '{{.ImportPath}}|{{.Module.Path}}|{{len .CgoFiles}}' ./...
```

No non-stdlib package in the dependency graph ships cgo files.

### Forbidden imports: checked on direct imports only

No package of this module directly imports gRPC, Prometheus,
`github.com/grpc-ecosystem/…` or `net/http/httptest`. That is what
`TestForbiddenImports` checks, and it checks **direct imports only, by
decision**.

The spec first called for the check to run over `go list -deps`. It cannot:
the packages the spec allows, `zoekt/index` and `zoekt/search`, import these
themselves. The project owner accepted that transitive graph instead of
recording a no-go.
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

### The builder accepts AgentKit's symbols

Verified by TS-03-64: a document with `Symbols` and `SymbolsMetaData` set and
`CTagsPath` empty is indexed and searchable with `sym:` queries. The symbol
hit outranks a comment-only hit. No ctags process is spawned.

### Cross-target build and vet

| Target | Build | Vet |
|---|---|---|
| linux/amd64 | ✅ | ✅ |
| linux/arm64 | ✅ | ✅ |
| darwin/arm64 | ✅ | ✅ |
| windows/amd64 | ✅ (stub) | ✅ (stub) |

Zoekt does not build on windows/amd64 (`unix.Umask` undefined). Per
03-REQ-8.6 and 03-REQ-9.5, this is handled by a platform-constrained stub
that returns `ErrUnsupported`, not treated as a no-go.

### The Windows stub

On windows/amd64, `codesearch.New` returns a nil `tools.Index` and
`ErrUnsupported`. The error value is the same on every platform
(`errors.Is(err, codesearch.ErrUnsupported)` works). An embedder falls back to
`Options.Index == nil`. The stub `Index` type implements `tools.Index` with
inert methods (`Symbols` never answers, `Tools` is empty), and has the
counters and statistics of the real one (`Build` returns `ErrUnsupported`,
every count is 0, `BuildStats` is the zero value), so the code in
[the opt-in snippet](../codesearch/README.md#use-it) compiles on Windows unchanged.
`TestEmbedderCompilesOnEveryTarget` builds an embedder that calls them.

## Test goldens

Test goldens in this module are tied to the pinned zoekt version
(`v0.0.0-20260911061844-153817f643cd`). A zoekt upgrade may change ranking
scores and require golden updates.
