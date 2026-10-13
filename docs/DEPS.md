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
| `google.golang.org/grpc` | the sandbox agent protocol's transport |
| `google.golang.org/protobuf` | the protobuf runtime of the generated sandbox protocol code |
| `github.com/gokrazy/rsync` | the in-process rsync protocol for sandbox `LocalDir` transfers (sandbox module) |
| `github.com/landlock-lsm/go-landlock` | the sandbox agent's own Landlock confinement (sandbox module) |

Adding a direct dependency means adding it there, with its reason, and a
ruling here. The same list covers the nested `sandbox` module, whose direct
dependencies (grpc, protobuf and, since spec 14, the rsync protocol) stay out
of the root module's graph; `TestSandboxDepsPromoted_TS13_65` checks both. The
`codesearch` module is separate and not covered: its zoekt graph stays out of
the root module's.

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

## `google.golang.org/grpc`

**Used by.** The sandbox packages: the generated client and server in
`sandbox/internal/sandboxpb`, the agent service in `sandbox/internal/agent`
and the host side in `sandbox/internal/attach`. Nothing outside `sandbox/`
imports it. Since spec 14 the sandbox tree is a nested module, and grpc is a
direct requirement of `sandbox/go.mod`; in the root module it is indirect
again, as it was before spec 13.

**Version.** `v1.64.1`, promoted from an indirect requirement at the version
already in `go.sum`, so `go list -m all` did not change. Upgrading it is a
separate ruling. `protoc-gen-go-grpc` v1.5.1 output needs v1.64.0 or later
(`grpc.SupportPackageIsVersion9`).

**Why this module.** PRD 11 (D2) chose an AgentKit gRPC protocol over MCP for
the sandbox, so tool schemas come from the `tools` package byte for byte and
`ToolMetadata` survives the trip. gRPC brings HTTP/2 multiplexing over one
pipe, per-call cancellation that reaches the agent as a cancelled context,
streaming for tar transfer, and message size limits. A hand-rolled framing
over stdio would have to rebuild each of those.

**What is pulled in.** `golang.org/x/net` (HTTP/2), `golang.org/x/sys`,
`golang.org/x/text` and `google.golang.org/genproto/googleapis/rpc`, all
already in the module graph through the Vertex AI dependencies. `x/net` stays
indirect (TS-09-15).

## `google.golang.org/protobuf`

**Used by.** The generated code in `sandbox/internal/sandboxpb`; a direct
requirement of the nested `sandbox` module.

**Version.** `v1.36.11`, promoted from an indirect requirement at the version
already in `go.sum`. `make proto` pins `protoc-gen-go` to the same version.
Upgrading it is a separate ruling.

**Why this module.** It is the runtime that `protoc-gen-go` output requires;
gRPC uses it to marshal every message.

**What is pulled in.** Nothing that was not already in the graph.

## `github.com/gokrazy/rsync`

**Used by.** `sandbox/internal/rsyncx` only, in the nested `sandbox` module;
the agent's `Rsync` service and the host's `attach` code reach it through
that package. Files that import it are `//go:build !windows`; Windows gets
stubs that refuse `LocalDir` at run time.

**Version.** `v0.3.8`. Adding it raised, by minimum version selection and
only in the sandbox module's graph, `golang.org/x/crypto` v0.40.0→v0.46.0,
`golang.org/x/net` v0.41.0→v0.47.0, `golang.org/x/text` v0.27.0→v0.32.0,
`golang.org/x/mod` v0.25.0→v0.30.0, `golang.org/x/tools` v0.34.0→v0.39.0 and
`github.com/BurntSushi/toml` v0.3.1→v1.6.0: v0.3.1 to v0.3.8 all require
`x/crypto` v0.46.0. The root module's graph does not change.
`TestRsyncDependency_TS14_42` pins exactly these moves.

**Why this module.** Both ends of a transfer run in-process, in the host's
code and in the agent, so nothing has to be installed on either side; it
moves only changed files (the quick check over size and modification time,
and delta transfer); and the bytes travel over the agent protocol's own
`Rsync` stream, so a gateway can relay them like any other stream.

**How it is used, and why so narrowly.** v0.3.8's exclude rules are unusable
here: a pattern with `*`, `?` or `[` panics, matching is not anchored, an
excluded file drops its later siblings, and `--delete` ignores every rule
(`docs/errata/14_sandbox_rsync_sync.md`). AgentKit therefore never passes it
a rule or `--delete`: the sending side serves a filtered, read-only `fs.FS`
through an `rsyncd` server, the receiving side runs an `rsyncclient`
receiver, and AgentKit decides what to delete.

**Every constructor restricts nothing.** `rsyncd.NewServer` applies Landlock
to the whole process when it has modules, and `rsyncclient` does as a
receiver, unless given `DontRestrict()`. In the local provider the process is
the embedder. Each is called in exactly one function of `rsyncx`, always with
`DontRestrict()`; `TestRsyncConstructorsDontRestrict_TS14_36` enforces it.

**Protocol 27 limits.** No xattrs, no ACLs, MD4 checksums; owner, group,
devices and hard links are not transferred (the options are `-r -l -t -p`).

**What is pulled in** (`go mod why -m`; "linked" means in the agent's and
host's binary through `rsyncx`):

| Module | Why | Linked |
|---|---|---|
| `github.com/landlock-lsm/go-landlock` | `rsyncd` → `internal/restrict` (and, since spec 15, `confine` directly) | yes, never called by `rsyncd` (`DontRestrict`) |
| `kernel.org/pub/linux/libs/security/libcap/psx` | go-landlock's syscalls | on Linux |
| `github.com/mmcloughlin/md4` | the receiver's protocol-27 checksums | yes |
| `github.com/google/renameio/v2` | the receiver's atomic writes | yes |
| `github.com/google/shlex` | `rsyncclient` → `internal/maincmd` | yes |
| `github.com/coreos/go-systemd` | `internal/maincmd` (socket activation) | on Linux |
| `github.com/BurntSushi/toml` | `internal/rsyncdconfig` | yes |

## `github.com/landlock-lsm/go-landlock`

**Used by.** `sandbox/internal/confine` directly, in the nested `sandbox`
module, and only from `//go:build linux` files; a `!linux` stub reports ABI
0. Only `agentkit-sandboxd` imports `confine`, so the agent confines its own
process before any other work and nothing the `local` provider runs inside an
embedder can reach it. `rsyncd` imports it too (`internal/restrict`), and
`rsyncx` always passes `DontRestrict()`, so that path never applies a ruleset.

**Version.** `v0.0.0-20250303204525-1544bccde3a3`, the version gokrazy/rsync
v0.3.8 already selected. Spec 15 promoted it to a direct requirement and
moved no version: the sandbox module's `go list -m all` is unchanged
(`TestLandlockPromoted_TS15_40`).

**Why this module.** A ruleset must reach every thread of a Go process, and
the runtime makes threads behind the program's back. go-landlock applies
`no_new_privs` and the ruleset to all threads at once: through
`syscall.AllThreadsSyscall` in a non-cgo build, and through psx, which wraps
`pthread_create`, in a cgo build.

**What is pulled in.** `kernel.org/pub/linux/libs/security/libcap/psx`
(on Linux, for the all-thread calls) and `golang.org/x/sys`. Both stay
indirect.

**Why `BestEffort` is never used.** It downgrades to whatever the kernel
offers and succeeds silently, without restricting anything, on a kernel
without Landlock. The agent instead applies a configuration of exactly the
ABI it chose (V2, V3 or V4), reports that choice in `Hello`, and an embedder
can make too little a `Create` failure with `Spec.RequireLandlock`. An
`internal/policy` test forbids any call of a method named `BestEffort`.

## Rejected

PRD 09 §4 records libraries that were considered and rejected, so they are not
re-proposed without new information. Its `provider/*` row (vendor SDKs) was
reversed by PRD 10 for the one wire that remains, which now runs on
`github.com/anthropics/anthropic-sdk-go`; every other ruling stands. Some rows
name code that has since been deleted; they are kept as the record.

| Area | Candidate | Why not |
|---|---|---|
| Sandbox container provider (`sandbox/podman`) | `github.com/docker/docker/client` (the Docker Go client) | PRD 11 D4: the provider drives the CLI. The client brings a large dependency graph into the sandbox module for what a pure argument builder and `tools.RunArgv` already do, against the allowlist rule. |
| Sandbox container provider (`sandbox/podman`) | `github.com/containers/podman/v5/pkg/bindings` (the Podman Go bindings) | PRD 11 D4, as for the Docker client: a very large dependency graph (much of podman itself), and it reaches only podman. |
| Sandbox agent confinement | raw Landlock syscalls through `golang.org/x/sys/unix` | The syscalls are simple; reaching every thread is not. A per-thread `landlock_restrict_self` leaves the runtime's other threads unconfined, and doing it right means re-implementing go-landlock's all-thread plumbing (psx under cgo, `AllThreadsSyscall` without). |
| Sandbox `LocalDir` transfer | an external `rsync` binary | It must be installed on both sides, at compatible versions; it runs as a subprocess outside the agent protocol, so a gateway cannot relay it as a stream; and the agent would hand the embedder's environment to another program. |
| Provider argument salvage | JSON-repair libraries | They close a truncated string; salvage drops the incomplete member, so `{"path":"/etc/pas` never becomes a valid call. |
| Provider transport retry | `cenkalti/backoff`, `go-retryablehttp` | The code was the header policy, not the curve; savings under 60 lines. (The transport is now the SDK's.) |
| Provider SSE / NDJSON readers | `r3labs/sse`, `launchdarkly/eventsource` | They reconnect and replay `Last-Event-ID`, which the streaming contract forbids. |
| Provider credentials | `oauth2.ReuseTokenSource`, `singleflight` | The store was a read-modify-write with a re-check under lock, not a token cache. (Deleted with the store.) |
| `schema/` | `santhosh-tekuri/jsonschema`, `google/jsonschema-go`, `invopop/jsonschema` | Only about 300 lines of validation are replaceable; the ordered value type, `StrictSubset`, null deletion, coercion and model-facing error hints stay. Revisit only if `$ref` or `format` become requirements (`pattern` is now validated, with the standard library's `regexp`). |
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
