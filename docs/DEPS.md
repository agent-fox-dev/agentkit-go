# Dependency rulings

Why a third-party module is a direct dependency of AgentKit, and on what
terms. The rule since
[PRD 09](prd/09-replace-hand-rolled-code-with-libraries.md) is that a
maintained library replaces hand-rolled infrastructure when it carries the
same guarantees. [PRD 10](prd/10-cut-agentkit-down-to-the-hands.md) turned the
list into an allowlist. Each module here is one where that was decided on
purpose.

## The allowlist

`internal/policy/deps_test.go` lists the root module's approved direct
dependencies in `approvedDeps`, and `TestDirectDependenciesAreApproved_TS09_16`
fails, naming the module, when `go list -m` reports a direct dependency that
is not on it. An entry ending in `/` approves every module under that path.

| Module | For |
|---|---|
| `github.com/anthropics/anthropic-sdk-go` | the Anthropic wire: transport, auth, retries, stream framing |
| `go.starlark.net` | code mode's sandboxed Starlark runtime |
| `github.com/modelcontextprotocol/go-sdk` | the MCP client and pool |
| `github.com/tree-sitter/…`, `github.com/tree-sitter-grammars/…` | tree-sitter outlines: the runtime and its grammar modules |
| `github.com/bmatcuk/doublestar/v4` | glob matching in `find_files` and the ignore engine |
| `github.com/pelletier/go-toml/v2` | TOML parsing for MCP configuration (`internal/toml`) |
| `golang.org/x/oauth2` | Google credentials for the Vertex AI deployment |
| `github.com/aws/aws-sdk-go-v2`, `github.com/aws/aws-sdk-go-v2/…` | AWS configuration and credentials for the Bedrock deployment |

Adding a direct dependency means adding it there, with its reason, and a
ruling here. The `codesearch` module is separate and not covered: its zoekt
graph stays out of the root module's.

## `github.com/anthropics/anthropic-sdk-go`

**Used by.** `provider/anthropic`, for every request.

**Version.** `v1.79.1`, pinned and moved deliberately.

**Why this module.** It owns what a hand-rolled wire had to: the HTTP
transport, retries with `Retry-After`, SSE framing, and the Vertex AI request
rewriting and OAuth. AgentKit still writes the request body itself and sends
it through the client (`option.WithRequestBody`), because the SDK's request
types re-encode a replayed `tool_use` input and its bytes must reach the wire
as the model wrote them.

**What is pulled in.** The Vertex option brings `golang.org/x/oauth2` and
Google's API transport; the module also requires the AWS SDK for its Bedrock
option.

## `go.starlark.net`

**Used by.** `codemode`, the tool that runs model-written scripts. Nothing
else imports it.

**Version.** `v0.0.0-20261005163335-bcb1a1a55bf9`. The module publishes
pseudo-versions only; there are no tagged releases to pin to.

**Why this module.** A code-mode script is written by a model and runs inside
the host process, so the interpreter has to be safe to run without trusting
the script:

- **No host I/O of its own.** Starlark has no file, network, environment,
  process or clock primitives. A script can reach only the builtins the host
  passes in, and `codemode` passes in the bound tools and three helpers.
  Module loading goes through a host hook, which `codemode` sets to refuse
  every module.
- **A step budget.** `Thread.SetMaxExecutionSteps` counts execution steps and
  cancels the thread when the budget is spent. A runaway loop therefore ends
  as `step_limit_exceeded` rather than by burning a CPU until the wall-time
  limit fires.
- **Cancellable.** `Thread.Cancel` stops a running script from another
  goroutine, which is how the wall-time limit and a cancelled run stop one.
- **Deterministic and pure Go.** There is no cgo and no background
  goroutines, and it builds for every target `internal/policy` checks.
- **A language models write.** It is a Python dialect. The tool's description
  spells out what differs (no exceptions, no imports, no classes).

**What is pulled in.** `codemode` imports `go.starlark.net/starlark`, and that
brings in `syntax`, `resolve` and two internal packages. The module's REPL
dependencies (`chzyer/readline`, `x/term`) and its protobuf support are not
imported. `codemode`'s `TestDepsDefaultsAndIsolation_TS08_44` checks this.

**Alternatives considered.** An embedded JavaScript engine is larger, has
ambient capabilities to remove rather than none to add, and has no step
counter. A WebAssembly sandbox would need a compiler for the model's language
as well.

## `github.com/modelcontextprotocol/go-sdk`

**Used by.** `mcp` only (and the `mcp` and `codemode` examples, which serve a
tool from the SDK's in-process server).

**Version.** `v1.8.0`.

**Why this module.** It is the official MCP Go SDK. It owns the protocol:
JSON-RPC framing, the stdio, Streamable HTTP and HTTP+SSE transports, and
version negotiation, so one client talks to servers on current and older
protocol revisions. It replaced about 4,000 lines of hand-rolled protocol code
(PRD 09 step 1). What it does not do, `mcp` adds around it: process-group
kill, respawn of a dead server, the result cap and call limit, `${VAR}`
resolution, and strict `wire` checks on every inbound frame and body, because
the SDK alone accepts duplicate keys.

**What is pulled in.** `github.com/google/jsonschema-go`,
`github.com/yosida95/uritemplate/v3`, `github.com/segmentio/encoding`,
`golang.org/x/oauth2` and a few `golang.org/x` modules.

## `github.com/tree-sitter/go-tree-sitter` and the grammar modules

**Used by.** `outline/treesitter.go`, which is `//go:build cgo`. The
`find_references` comment and string masks come from the same grammars.

**Version.** `go-tree-sitter v0.25.0`; one module per grammar: bash, C, C#,
C++, Java, JavaScript, PHP, Python, Ruby, Rust, Scala and TypeScript under
`github.com/tree-sitter/`, Kotlin and Lua under
`github.com/tree-sitter-grammars/`.

**Why this module.** It is the official Go binding. Each grammar's tags query
gives real declarations with real line ranges, which the regex and ctags
backends it replaced (PRD 09 step 3) could not, and it removed the dependency
on an external `ctags` binary. It is the only cgo code in the module; without
cgo, `outline` falls back to `go/ast` for Go and `none` for everything else,
and `internal/policy` checks both builds.

**What is pulled in.** `github.com/mattn/go-pointer`, and the C sources of
each grammar. Each grammar adds to the binary size.

## `github.com/bmatcuk/doublestar/v4`

**Used by.** `tools/glob.go` (`find_files`) and `tools/ignore.go`.

**Version.** `v4.10.2`.

**Why this module.** It matches `**` with backtracking and handles character
classes and escapes the way gitignore needs. AgentKit keeps smart-case and
bare-pattern basename matching as a thin wrapper over `doublestar.Match`
(PRD 09 step 4).

**What is pulled in.** Nothing; it has no dependencies.

## `github.com/pelletier/go-toml/v2`

**Used by.** `internal/toml`, whose one caller is `mcp.ParseConfig`.

**Version.** `v2.4.3`, pinned: `internal/toml` uses the `unstable` package,
which is outside semver.

**Why this module.** Its `unstable` parser exposes the parse tree with
positions, so diagnostics keep their line numbers and a duplicate key can warn
with the last value winning, which a decode-into-struct library cannot. A
small adapter folds the tree into an ordered `Table` (PRD 09 step 4).

**What is pulled in.** Nothing.

## `golang.org/x/oauth2`

**Used by.** `provider/anthropic` (`resolve.go`, `stream.go`):
`oauth2.TokenSource` for `Options.VertexTokenSource`, a static token from
`ANTHROPIC_AUTH_TOKEN`, and `golang.org/x/oauth2/google` for Application
Default Credentials.

**Version.** `v0.35.0`.

**Why this module.** The SDK's Vertex option takes `*google.Credentials`, so
the provider has to name these types to supply credentials itself and to
defer the Application Default Credentials lookup to the first request.

**What is pulled in.** `cloud.google.com/go/compute/metadata`. The SDK's
Vertex option already requires the module; importing it directly adds
nothing.

## `github.com/aws/aws-sdk-go-v2`

**Used by.** `provider/anthropic/resolve.go`, for the Bedrock deployment:
`aws.Config`, static keys from `credentials`, and `config.LoadDefaultConfig`
for the AWS credential chain.

**Version.** `aws-sdk-go-v2 v1.38.0`, `config v1.27.27`,
`credentials v1.17.27`.

**Why this module.** The SDK's Bedrock option takes an `aws.Config`. Building
it from the environment, shared files or instance roles is the AWS SDK's job;
re-implementing SigV4 credentials would be neither smaller nor safer.

**What is pulled in.** `github.com/aws/smithy-go` and the AWS SDK's internal,
SSO, SSO-OIDC and STS modules, which `config` needs for the credential chain.

## Rejected

PRD 09 §4 records libraries that were considered and rejected, so they are not
re-proposed without new information. Its `provider/*` row (vendor SDKs) was
reversed by PRD 10 for the one wire that remains, which now runs on
`github.com/anthropics/anthropic-sdk-go`; every other ruling stands. Some rows
name code that has since been deleted; they are kept as the record.

| Area | Candidate | Why not |
|---|---|---|
| Provider argument salvage | JSON-repair libraries | They close a truncated string; salvage drops the incomplete member, so `{"path":"/etc/pas` never becomes a valid call. |
| Provider transport retry | `cenkalti/backoff`, `go-retryablehttp` | The code was the header policy, not the curve; savings under 60 lines. (The transport is now the SDK's.) |
| Provider SSE / NDJSON readers | `r3labs/sse`, `launchdarkly/eventsource` | They reconnect and replay `Last-Event-ID`, which the streaming contract forbids. |
| Provider credentials | `oauth2.ReuseTokenSource`, `singleflight` | The store was a read-modify-write with a re-check under lock, not a token cache. (Deleted with the store.) |
| `schema/` | `santhosh-tekuri/jsonschema`, `google/jsonschema-go`, `invopop/jsonschema` | Only about 300 lines of validation are replaceable; the ordered value type, `StrictSubset`, null deletion, coercion and model-facing error hints stay. Revisit only if `$ref`, `pattern` or `format` become requirements. |
| `jsonx/` (now `schema.Parse`) | `wk8/go-ordered-map` | Replaces only the object case of a closed value tree. |
| `session/` (deleted) | SQLite, bbolt | The JSONL format was the contract; a database adds locking and migrations. |
| `tools/ref_gotypes.go` | `golang.org/x/tools/go/packages` | Needs the `go` tool on PATH and the network, inherits the environment, and type-checking dependencies blows the 2 s budget. |
| `tools/walk.go` | `charlievieth/fastwalk` | Parallel and unordered; callers need lexical order and the ignore engine needs parent before child. |
| `guard/guard.go` | `mvdan.cc/sh/v3/syntax` | Saves about 50 lines; the over-conservative scanner is easy to audit. Revisit if false rejections become a complaint. |
| Token estimate (compaction, deleted) | `pkoukk/tiktoken-go` | OpenAI's tokenizer is wrong for Claude and embeds about 1 MB. |
| `tools/edit.go` | go-diff, difflib, udiff | It does exact and folded matching, not diffing. |
| Middleware LRU (deleted) | `hashicorp/golang-lru/v2` | About 40 lines saved for a module; `container/list` fixed the same problem. |
| `tools/fold.go` | `x/text/unicode/norm` | NFKC does not fold smart quotes to ASCII. |
| `tools/ignore.go` matcher | go-git `plumbing/format/gitignore` | Matches segments with `filepath.Match`, so `[!x]` stops negating and `**` does not backtrack; and it pulls go-git, go-billy and gcfg into the root. |
| Swift outline | `alex-pinkus/tree-sitter-swift` | Its Go module is an untagged commit whose test imports a path that does not exist, so `go mod tidy` fails for every importer. |
| `outline/lang.go` | `go-enry/go-enry/v2` | The extension table encodes deliberate per-backend exclusions. |
| Tracing | OpenTelemetry | The tracer was an interface already; an adapter adds code. (Deleted; the event stream replaces it.) |
