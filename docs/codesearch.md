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
- [The stored index](#the-stored-index)
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

## The stored index

By default the index lives for one process: its shards are in a run directory
under `Options.TempDir`, and `Close` deletes it. `Options.IndexDir` keeps it on
disk instead, so a later process on the same workspace reuses it. The
`agentkit-tools` command sets it from `--index-dir` or `AGENTKIT_INDEX_DIR`
([cli.md](cli.md#flags)); the package itself reads no environment variable.

**Where it lives.** One `IndexDir` holds the indexes of any number of
workspaces, each in `IndexDir/agentkit-codesearch-<sha256 of the workspace
root>/`:

| Entry | Holds |
|---|---|
| `manifest.json` | The root, the format version (`1`), whether the index is partial and why, and the name of the current index directory. |
| `index-<id>/` | Copies of the shards, and `state.json`: the indexed files, their outlines, each file's size, modification time, indexing time and content hash, the build statistics, and each shard's size and SHA-256. |
| `lock` | The lock one process at a time holds while it loads or builds and stores. |

`IndexDir` must lie outside the workspace, after resolving the symbolic links
of its existing parts: `New` refuses one that is the root or inside it,
otherwise the walk would index the shards.

**Storing.** A completed build, and the rebuild that more than five percent
of dirty files triggers, are written under a temporary name
(`index-<id>.tmp`), renamed into place, and only then named by a new
`manifest.json`, itself replaced by a rename; the index directories it no
longer names are removed. A reader therefore follows a manifest to a complete
index or to none. A store directory that cannot be created or written stores
nothing, and the process keeps its run directory as it would without
`IndexDir`. Changes a process finds by revalidation are not written back:
only a build or a rebuild replaces the stored index.

**Loading.** The first query (or `Symbols`, which never builds) of an index
with nothing built loads the stored index when its manifest names this root
and the current format version and it is not partial. The shards are copied
into the process's own run directory and checked against the sizes and
SHA-256 sums `state.json` records, so a store replaced by another process
never deletes a file this one searches. The loaded index counts no build and
is revalidated as a whole before it answers: files whose size or
modification time changed, new files and deleted files are picked up, and a
file indexed within the 2-second timestamp granule of its last write is
compared by content. The content hash is the first eight bytes of the
content's SHA-256, so it compares in any process. A store that is missing,
names another root or another format version, is partial, or does not read
back is built anew and replaced, and the caller sees the normal result.

**Two processes at once.** The `lock` file carries an advisory `flock`, which
the kernel releases when its holder exits, however it exits. A second process
waits for it, abandonably with its context, up to 1.25 × `MaxBuildTime` plus
5 seconds (the longest a build runs, and a margin for its store), then loads
what the first stored. Past that bound it builds for itself in its own run
directory and stores nothing. The build's wall-time bound starts after the
wait. The lock is not available where zoekt is not ([the Windows
stub](#the-windows-stub)).

**Partial builds.** A build stopped by `MaxFiles`, `MaxBytes` or
`MaxBuildTime` stores its index marked partial; the process that built it
serves it with the partial marker, and the next process builds again and
replaces it.

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
the builder uses the supplied data without running ctags; a test checks it.

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

### Forbidden imports: direct imports only

No package of this module directly imports gRPC, Prometheus,
`github.com/grpc-ecosystem/…` or `net/http/httptest`; that is kept by review,
and it holds for **direct imports only, by decision**.

The spec first called for the rule to cover `go list -deps`. It cannot:
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

A test checks that a document with `Symbols` and `SymbolsMetaData` set and
`CTagsPath` empty is indexed and searchable with `sym:` queries. The symbol
hit outranks a comment-only hit. No ctags process is spawned.

### Cross-target build and vet

| Target | Build | Vet |
|---|---|---|
| linux/amd64 | ✅ | ✅ |
| linux/arm64 | ✅ | ✅ |
| darwin/arm64 | ✅ | ✅ |
| windows/amd64 | ✅ (stub) | ✅ (stub) |

Zoekt does not build on windows/amd64 (`unix.Umask` undefined). This is
handled by a platform-constrained stub that returns `ErrUnsupported`, not
treated as a no-go. `internal/policy`'s cross-target gate, which
`make check-release` runs, builds and vets the module for every target.

### The Windows stub

On windows/amd64, `codesearch.New` returns a nil `tools.Index` and
`ErrUnsupported`. The error value is the same on every platform
(`errors.Is(err, codesearch.ErrUnsupported)` works). An embedder falls back to
`Options.Index == nil`. The stub `Index` type implements `tools.Index` with
inert methods (`Symbols` never answers, `Tools` is empty), and has the
counters and statistics of the real one (`Build` returns `ErrUnsupported`,
every count is 0, `BuildStats` is the zero value), so the code in
[the opt-in snippet](../codesearch/README.md#use-it) compiles on Windows unchanged.

## Test goldens

Test goldens in this module are tied to the pinned zoekt version
(`v0.0.0-20260911061844-153817f643cd`). A zoekt upgrade may change ranking
scores and require golden updates.
