# Replace hand-rolled code with maintained libraries

Status: **implemented** (steps 0–5). Each step landed on its own `feature/`
branch with `make check` green and its docs updated in the same branch. What
was delivered, and where it differs from the plan, is recorded in §6.

## 1. The problem

AgentKit was built under two rules: the root module requires nothing outside
the Go standard library, and cgo is never allowed. The first was already
relaxed to a preference; the second no longer holds. **cgo is allowed, and
third-party modules are allowed in the root module.**

Under the old rules a large amount of infrastructure was written by hand:
an MCP client and server with their own JSON-RPC, transports and protocol
types; a bounded JSON scanner and reflective binder; a TOML reader; a glob
matcher; two regex/ctags-driven outline backends. Each is code the project
maintains, tests and debugs, and none of it is what AgentKit is *about*.

The goal of this PRD is to **simplify the codebase and reduce LOC and
complexity** by replacing that code with well-maintained, widely-used
libraries, where a library carries the same guarantees. It is equally a record
of where a library was considered and rejected, so the question is not
re-asked.

Note: lifting the cgo ban by itself unlocks little. Exactly one replacement
below needs cgo (tree-sitter, §3 step 3). Most of the reduction comes from
pure-Go modules that the stdlib-only preference kept out.

## 2. Decisions

Settled with the project owner; not re-opened by the steps below.

| # | Question | Decision |
|---|---|---|
| D1 | How does cgo coexist with the four-target cross build? | **Build tags with a pure-Go fallback.** cgo code lives in `//go:build cgo` files; every package builds and works with `CGO_ENABLED=0`. |
| D2 | Where do new third-party modules go? | **The root module.** No new nested modules for these dependencies. |
| D3 | Minimum Go version | **Raised to 1.27** (needed for `encoding/json/v2`). |
| D4 | MCP behaviour changes from adopting the SDK | **Accepted:** older protocol versions are negotiated (client and server); unknown fields in request params are no longer rejected; the SDK's multi-round-trip retry cap and pagination cursor format are used. |

## 3. Plan, in execution order

Estimated totals: **about −6,200 source LOC and −3,000 test LOC**, with about
1,500 test LOC rewritten. Steps 1 and 2 are roughly 80% of the reduction.

### Step 0 — Policy and toolchain

Lands first; every other step depends on it.

- `go 1.27` in all six `go.mod` files (D3).
- **Policy tests.** Delete `TestNoCgoOutsideStdlib`, `TestCgoProbeIsArmed`
  (`internal/policy/deps_test.go`), the cgo scan in
  `codesearch/riskcheck_test.go`, and `TestGoModHasNoRequire_TS_04_58`
  (`internal/policy/spec04_test.go`, which still fails on any `require` line).
  Relax `TestOutline_StdlibOnly_TS_01_64`.
- **Keep** `TestCrossTargetBuildAndVet` with `CGO_ENABLED=0`: under D1 it now
  proves the pure-Go fallback builds on linux/amd64, linux/arm64,
  darwin/arm64 and windows/amd64.
- **Add** a host-only `CGO_ENABLED=1` build+vet gate, and a test asserting the
  cgo build actually selects the tree-sitter backend. Without it a broken build
  tag silently falls back to pure Go and every test stays green.
- **Tests pinned to removed docs.** `docs/DEPS.md`, `docs/errata/`,
  `docs/adr/`, `docs/GAPS.md` and `docs/PROVIDERS.md` were removed from the
  repository; `TestTheProviderLedgerMatchesTheCode` and
  `TestDependencyPolicyIsRecorded` (`internal/policy/ledger_test.go`) still
  require them and fail, which also fails `outline`'s
  `TestInternalPolicyGreen_TS_01_65`. Delete or rewrite every test that reads
  a removed doc (candidates: `internal/policy`, `codesearch/docs_test.go`,
  `tools/docs_test.go`, `outline/build_test.go`).
- **`~user` paths.** `tools/path.go` `Normalize` rejects `~user` only because
  `os/user` was cgo-backed. Support it via `os/user`, which has a pure-Go
  implementation when cgo is off.
- **Docs.** README dependency paragraph (it still says cgo is never allowed
  and links removed docs), `docs/architecture.md`, and the `REQ-GO-11`
  comments in the nested `go.mod` files.

### Step 1 — `mcp/` → `github.com/modelcontextprotocol/go-sdk`

The largest single reduction. Pure Go; root module (D2).

- **Library.** The official SDK (MCP org, maintained with Google), v1.8.0 at
  the time of writing. Verified: it supports protocol 2026-07-28 — the revision
  `mcp/` targets — plus 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05, and
  provides `server/discover`, per-request `_meta`, multi-round-trip input
  requests, `subscriptions/listen`, `Mcp-Method`/`Mcp-Name` headers,
  `CommandTransport`, stdio, streamable HTTP and legacy SSE (client and
  server), `RequireBearerToken`, and send/receive middleware.
- **Replaced** (≈4,000 of 6,023 source LOC): `jsonrpc.go`, `protocol.go`,
  `streamable.go`, `sse.go`, `httptransport.go`, `resources.go`,
  `client_resources.go`, `subscriptions.go`, most of `server.go`, `client.go`,
  `transport.go`, `serve.go`.
- **Kept** (≈1,800 LOC):
  - `pool.go` — adapting MCP tools to `core.Tool`, qualified names,
    `NativeTools` collision check, schema conversion, env/secret resolution.
  - `config.go`; `${VAR}` interpolation and the reduced subprocess env.
  - `proc_unix.go`/`proc_windows.go` — the SDK's close signals the PID only,
    not the process group.
  - Respawn of a dead stdio server (the SDK does not), the 50K-rune result
    cap, the call limit, audit.
  - Server: handler-signature adapter, sampling gate, API-key middleware
    (constant-time compare plus Origin check), `Run` (refuse to start without
    a key, bind to 127.0.0.1), a receive middleware bounding concurrent
    handlers per session (today `MaxConcurrentHandlers` = 64).
  - **Strict-JSON wrappers (≈250 LOC)** so REQ-SEC-11 still holds at the three
    trust boundaries: a custom SDK `Transport` on stdio built on
    `wire.FrameReader`; an HTTP middleware that bounds and validates inbound
    bodies; a `RoundTripper` that validates SSE `data:` events on responses.
    The SDK alone accepts duplicate keys (last wins).
- **Tests.** About 65 of 99 test functions exercise wire mechanics the SDK now
  owns (envelope strictness, SSE parsing, re-issue, pagination, cancellation,
  template matching, version negotiation): ≈ −2,400 test LOC. Pool, config,
  result cap, call limit, audit, auth, `Run`, interpolation and respawn tests
  stay, rewritten against the SDK.
- **Behaviour changes (D4).** Client and server negotiate older protocol
  versions — this delivers most of
  [PRD 03](03-add-a-classic-mcp-client.md) without further work. Unknown
  param fields are accepted (tool arguments are still validated against the
  input schema). Multi-round-trip retries rise from 3 to 10. Error codes for a
  broken stream and the pagination cursor format change. URI templates follow
  RFC 6570 (edge cases around `/` and empty variables change).
- **Public API.** About 40 protocol identifiers (`NewPipeTransport`,
  `StartStdio`, `Transport`, `ProtocolVersion`, protocol constants and types)
  go away; `ToolHandler`/`Content` change shape or are adapted. `Pool`,
  `NewPool`, `ServerConfig`, `Config`, `ParseConfig`, `ServerModeConfig`,
  `Server.Run` are unchanged. Five caller files (about 300 lines of edits):
  `mcp_wiring_test.go`, `examples/mcp`, `examples/mcpserver`,
  `internal/policy/ledger_test.go`.
- **Order inside the step.** Swap the client behind `Pool`, then the server
  behind `Server`, then add the wrappers, then delete obsolete tests.
- **Docs.** `docs/api.md`, `docs/configuration.md`, PRD 03's status.

### Step 2 — `wire/` scanner and binder → `encoding/json/v2`

Standard library since Go 1.27 (D3); no module added. After step 1, because
`mcp/` is `wire`'s largest caller.

- **Replaced.** `wire/scan.go` (582) and `wire/bind.go` (437) → ≈300 LOC on
  `encoding/json/v2` and `encoding/json/jsontext`: ≈ −700 LOC.
- **What v2 provides natively** (verified with a probe on go1.27.1):
  duplicate-key rejection, `RejectUnknownMembers(true)` (REQ-SEC-12.1),
  case-sensitive matching, `jsontext.Value` preserving member order and number
  text.
- **Kept / written around it.** Depth, container-length and node-count limits
  as a `ReadToken` loop over `StackDepth`/`StackIndex` (≈60 LOC); the 16 MiB
  cap as a length check before decoding; a custom int unmarshaler for the
  safe-integer range (REQ-SEC-12.2); the Validator hook (REQ-SEC-12.3); the
  `Rule`/`Path` error mapping; `frame.go` and `wire.go` unchanged.
  `AllowInvalidUTF8(true)` so a lone `\ud800` still decodes to U+FFFD
  (JavaScript peers emit them).
- **Tests.** The existing 962 lines of `wire` tests stay unchanged and are the
  proof of equivalence. Pin first: lone surrogates, integral floats in int
  fields, Validator timing, `Rule`/`Path` of every error.

### Step 3 — Tree-sitter outline backend (the only cgo code)

- **New.** `outline/treesitter.go` under `//go:build cgo`, using
  `github.com/tree-sitter/go-tree-sitter` (official binding) and per-language
  grammar modules, driven by each grammar's tags query
  (`@definition.function`, `@definition.class`, …).
- **Fallback (`!cgo`).** `go/ast` for Go, `none` for every other language.
- **Deleted.** `outline/ctags.go` (433), `outline/heuristic.go` (440),
  `tools/ctags.go` (165) and the ctags parts of `outline/lang.go`. This also
  removes the dependency on an external `ctags` binary, without which ≈30
  languages have no outline today.
- **Kept.** `outline/goast.go` — still the best backend for Go.
- **Reference scanner.** Under cgo, the comment/string mask in
  `tools/ref_scanner.go` (`buildCommentStringMask`, ≈180 LOC) comes from
  tree-sitter `comment`/`string` nodes, which fixes raw strings, C# verbatim
  strings, f-strings and heredocs. Without cgo, non-Go hits are classified
  `text`.
- **Contracts to preserve.** The closed `Kind` set and `Container` semantics
  (qualified `find_symbol` such as `Runner.run`); per-language `Exported`
  rules; signature = sanitised source line, 200 bytes; backend names; the
  "unknown extension → `none`, file not read" rule.
- **Gains.** Real `EndLine` (the heuristic reports 0, which also helps
  reference attribution); Swift, Scala, Dart, Zig become possible.
- **Costs.** ≈ −800 source LOC net; ≈1,500 test LOC rewritten (split into
  build-agnostic tests and `cgo`-tagged tests). 1–3 MB of binary per grammar.
  Some long-tail ctags languages (COBOL, Ada, VHDL, Pascal, Raku, Thrift) are
  dropped where grammar quality is poor. `codesearch`'s symbol source changes
  with it.
- **Open trade-off.** With `CGO_ENABLED=0`, non-Go languages lose outlines and
  comment/string classification. If the pure-Go build must stay useful for
  them, keep `heuristic.go` as the `!cgo` fallback, at a cost of ≈440 LOC of
  the reduction.

### Step 4 — Small swaps

Independent of each other; one branch each; all pure Go; root module.

| Area | Library | LOC | Notes |
|---|---|---|---|
| `internal/toml` (777) | `github.com/pelletier/go-toml/v2/unstable` + ≈150 LOC node→`Table` adapter | ≈ −580, ≈ −250 tests | Keeps line numbers in diagnostics (BurntSushi cannot) and "warn, last wins" on duplicates. `Table`/`Value` API unchanged, so the four callers (`skills`, `plugins` ×2, `mcp/config.go`) do not change. `unstable` is outside semver: pin it. |
| `tools/glob.go` (192) | `github.com/bmatcuk/doublestar/v4` | ≈ −150 | Keep smart-case and bare-pattern basename matching as a wrapper; keep `ExpandBraces` exported; switch `ignore.go` off `matchSegments`/`matchOne`. Check `a/**` vs `a` and dotfiles against existing tests first. |
| `HTMLToText` in `tools/fetch.go` | `golang.org/x/net/html` (or `github.com/JohannesKaufmann/html-to-markdown/v2`) | ≈ −60 | Mostly a correctness fix: entities decode twice (`&amp;amp;lt;` → `&lt;`), numeric entities are not decoded, `>` inside attributes breaks the stripper. |
| `middleware.RateLimit` | `golang.org/x/time/rate` | ≈ −45 | `Limiter.Wait(ctx)`; keep the error for a non-positive rate. |
| `imagex` scaling | `golang.org/x/image/draw` (+ `golang.org/x/image/webp`) | ≈ −35 | Replaces a per-pixel `At()` box filter: faster, better quality. WebP becomes decodable. Keep APNG/CMYK/IHDR checks and the quality ladder. |
| `tools/ignore.go` matcher *(optional)* | `github.com/go-git/go-git/v5/plumbing/format/gitignore` | ≈ −70 | For correctness (`\#`, `\!`, escaped trailing spaces), not size. Layering, nested-repo boundaries and global-excludes discovery stay. |
| `tools/ssrf.go` table *(optional)* | `code.dny.dev/ssrf` | ≈ −60, ≈ −300 tests | `DialContext`, resolve-then-check and the injectable `Resolve`/`DialAddr` stay. Error text loses the per-range reason. |

### Step 5 — Simplifications needing no library *(optional)*

- Delete the ripgrep backend in `tools/search.go` (≈ −370 LOC plus parity
  tests). It re-walks the tree to count files anyway, and `codesearch` covers
  large repositories.
- Merge the duplicated SSE/NDJSON readers (`mcp/sse.go` if it survives step 1,
  `wire/frame.go`, `provider/sse.go`, `provider/ndjson.go`).
- Find-references cleanup: dead aliases (`filterSites`, `isPkgDirty`,
  `renderReferencesToolResult`), the package-global `lastWorkspace`,
  `resolveGoReferences(args ...any)`, four duck-typed index interfaces, and
  the per-file scan of every package's `Defs`/`Uses` (quadratic).

## 4. Considered and rejected

Recorded so they are not re-proposed without new information.

| Area | Candidate | Why not |
|---|---|---|
| `provider/*` (≈8,900 LOC) | anthropic-sdk-go, openai-go, `google.golang.org/genai`, `ollama/api` | Saves only 15–25%: transcript conversion, repair, cache stamping, compat flags, thinking mapping and usage netting stay. Breaks REQ-SEC-12.6 (duplicate `stop_reason` is last-wins in the SDKs), REQ-PROV-13 (SDKs ignore a long `Retry-After` instead of abandoning with a typed error), REQ-PROV-18 (`OnPayload`), and the golden request bodies. `ollama/api` fails on lines over ≈512 KB. The SDKs belong in `difftest/` as the independent reference NFR-TEST-06 asks for, which it currently lacks. |
| `provider/salvage.go` | JSON-repair libraries | They *close* a truncated string; salvage *drops* the incomplete member so `{"path":"/etc/pas` never becomes a valid call (REQ-LOOP-10, REQ-TOOL-11). |
| `provider/transport.go` | `cenkalti/backoff`, `go-retryablehttp` | The code is the header policy (`x-should-retry`, `retry-after-ms`, dates, downward-only jitter, factory rebuild per attempt); the curve is ≈10 LOC. Saves < 60 LOC. |
| `provider/sse.go`, `ndjson.go` | `r3labs/sse`, `launchdarkly/eventsource` | They reconnect and replay `Last-Event-ID`, which REQ-OBS-09 forbids. |
| `provider/credentials.go` | `oauth2.ReuseTokenSource`, `singleflight` | It is a store-backed read-modify-write with a re-check under lock, not a token cache. |
| `schema/` | `santhosh-tekuri/jsonschema`, `google/jsonschema-go`, `invopop/jsonschema` | Only ≈300 LOC of validation is replaceable; the ordered value type, `StrictSubset`, null deletion, coercion and model-facing error hints stay. Revisit only if `$ref`/`pattern`/`format` become requirements. |
| `jsonx/` | `wk8/go-ordered-map` | Replaces only the object case of a closed value tree used in 200+ places. |
| `session/` | SQLite, bbolt | The JSONL format is the contract (REQ-SESS-01); a database adds locking and migrations and still needs the branch tree and fold. |
| `tools/ref_gotypes.go` | `golang.org/x/tools/go/packages` | Needs the `go` tool on PATH and the network, inherits the environment (REQ-SEC-08), and type-checking dependencies blows the 2 s budget. |
| `tools/walk.go` | `charlievieth/fastwalk` | Parallel and unordered; callers need lexical order and the ignore engine needs parent-before-child. |
| `guard/guard.go` | `mvdan.cc/sh/v3/syntax` | Net ≈ −50 LOC; the current over-conservative scanner is easy to audit. Revisit if false rejections become a complaint. |
| `compaction` token estimate | `pkoukk/tiktoken-go` | OpenAI's tokenizer is wrong for Claude and Gemini and embeds ≈1 MB. |
| `tools/edit.go` | go-diff, difflib, udiff | It does exact/folded matching, not diffing. |
| `middleware` LRU | `hashicorp/golang-lru/v2` | ≈ −40 LOC for a module; `container/list` fixes the O(n) `touch` equally well. |
| `tools/fold.go` | `x/text/unicode/norm` | NFKC does not fold smart quotes to ASCII. |
| `tools/ignore.go` matcher | go-git `plumbing/format/gitignore` | Matches segments with `filepath.Match`: `[!x]` would stop negating and `**` would not backtrack (`a/**/b/c` misses `a/b/x/b/c`), and it pulls go-git, go-billy and gcfg into the root. The doublestar matcher from step 4 already handles `\#` and `\!`; the escaped-trailing-space bug was fixed in place. |
| Swift outline | `alex-pinkus/tree-sitter-swift` | Its Go module is an untagged branch commit whose test imports a path that does not exist, so `go mod tidy` fails for every importer. |
| `outline/lang.go` | `go-enry/go-enry/v2` | The table encodes deliberate per-backend exclusions. |
| Tracing | OpenTelemetry | `core.Tracer` is already an interface; an adapter adds code. |

## 5. Acceptance

For every step:

- `make check` is green, including `TestCrossTargetBuildAndVet`
  (`CGO_ENABLED=0`, four targets) and, from step 0 on, the host
  `CGO_ENABLED=1` gate.
- Behaviour that the replaced code guaranteed is pinned by a test *before* the
  swap, and that test is unchanged after it — unless D4 or this PRD names the
  change.
- Docs touched by the step (`README.md`, `docs/api.md`,
  `docs/configuration.md`, `docs/architecture.md`) are updated on the same
  branch.
- The commit message for each new module says what it buys and why
  hand-rolling it is not credible.

## 6. Outcome

Across steps 0–5: **−8,138 net source LOC and −7,001 net test LOC** in Go
files (+2,645/−10,783 source, +2,227/−9,228 tests). The plan estimated −6,200
and −3,000; the find-references cleanup and the ctags/heuristic deletion
removed more than expected, the `wire` swap less.

| Step | Delivered | Differs from plan |
|---|---|---|
| 0 | cgo/stdlib gates and doc-prose tests removed; `go 1.27`; `TestHostCgoBuildAndVet` added; `~user` expanded via `os/user`. | `TestTS_04_32_ExistingTestFilesUnchanged` (froze `tools_test.go`) and the `require`-line half of `TestPolicyAndCrossTarget_TS05_51` were also removed. |
| 1 | `mcp/` on `modelcontextprotocol/go-sdk` v1.8.0: 6,023 → 2,096 source, 3,715 → 1,747 test LOC. Strict-JSON checks on stdio frames, inbound HTTP bodies and client responses. | Beyond D4: tool lists are cached only on a positive `ttlMs`; input-schema properties decode in alphabetical order and large integers in `structuredContent` as float64; HTTP over-size/malformed bodies answer 413/400; stdio has no batches and no parse-error reply; HTTP serving is stateless; `transport = "sse"` is accepted. `NewConnection`, `Discover`, `RefreshTools`, `StartStdio` and the protocol types are gone; `Connect` and `ServerConnection.Session` are new. |
| 2 | `wire` scanner and binder on `encoding/json/v2`/`jsontext`: 1,019 → 644 LOC. Existing wire tests unchanged; 418 lines of pin tests added first. | Saving is ≈−375, not ≈−700: the `Value` tree stays because callers use it. A malformed value exactly at the node limit is now rule `syntax`, not `nodes`; a member name starting with `[` gets `$.[` paths. `Guard` allocates more (≈24 vs 10 per SSE event). `wire.Bind` has no production caller. |
| 3 | Tree-sitter backend under `//go:build cgo` for Python, JS, TS/TSX, Java, Kotlin, C#, Scala, Rust, C, C++, PHP, Ruby, Lua and shell; `go/ast` for Go; `none` without cgo. ctags runner and heuristic backend deleted; reference-scanner masks come from tree-sitter. | Swift dropped (§4) along with the ctags long tail (listed in `docs/architecture.md`). `OutlineMany` returns `([]File, error)`; ctags options are gone from `outline`, `tools` and `codesearch`. go-tree-sitter's `ParseCtx` segfaults on mid-parse cancellation, so parses cancel through the progress callback. ≈+29 MB test binary for 14 grammars. |
| 4 | go-toml/v2 `unstable`, doublestar, `x/net/html`, `x/time/rate`, `x/image/draw` + WebP decode, `code.dny.dev/ssrf`. | gitignore matcher kept (§4). TOML nesting limit is go-toml's. SSRF blocks every IPv6 address outside 2000::/3. Oversized WebP is re-encoded as JPEG. |
| 5 | ripgrep backend removed; Ollama reads NDJSON through `wire.FrameReader`; find-references internals 2,881 → 1,548 LOC (one type-check per query, no globals, one index interface). | No SSE reader was left to merge after step 1. |

**Found during step 5, not fixed (behaviour changes):** spec 05's
enclosing-declaration attribution for non-Go hits (TS-05-25–27, TS-05-56) and
its file/time bounds (TS-05-33) were implemented only in code the tool never
called; their tests exercised that dead code and were removed with it. Non-Go
sites report `<file>` and queries are unbounded. The reference cache rebuilds
packages and outlines that queries never read, and its candidate-file list is
not reset by `write_file`/`edit_file`.
