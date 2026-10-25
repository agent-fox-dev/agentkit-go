# Dependency rulings

Why a third-party module is a direct dependency of AgentKit, and on what
terms. The rule since
[PRD 09](prd/09-replace-hand-rolled-code-with-libraries.md) is that a
maintained library replaces hand-rolled infrastructure when it carries the
same guarantees. [PRD 10](prd/10-cut-agentkit-down-to-the-hands.md) turned the
list into an allowlist. Each module here is one where that was decided on
purpose.

## Contents

- [The allowlist](#the-allowlist)
- [`github.com/anthropics/anthropic-sdk-go`](#githubcomanthropicsanthropic-sdk-go)
- [`github.com/OpenRouterTeam/go-sdk`](#githubcomopenrouterteamgo-sdk)
- [`github.com/ollama/ollama`](#githubcomollamaollama)
- [`go.starlark.net`](#gostarlarknet)
- [`github.com/modelcontextprotocol/go-sdk`](#githubcommodelcontextprotocolgo-sdk)
- [`github.com/tree-sitter/go-tree-sitter` and the grammar modules](#githubcomtree-sittergo-tree-sitter-and-the-grammar-modules)
- [`github.com/bmatcuk/doublestar/v4`](#githubcombmatcukdoublestarv4)
- [`github.com/pelletier/go-toml/v2`](#githubcompelletiergo-tomlv2)
- [`golang.org/x/oauth2`](#golangorgxoauth2)
- [`github.com/aws/aws-sdk-go-v2`](#githubcomawsaws-sdk-go-v2)
- [`google.golang.org/grpc`](#googlegolangorggrpc)
- [`google.golang.org/protobuf`](#googlegolangorgprotobuf)
- [`github.com/gokrazy/rsync`](#githubcomgokrazyrsync)
- [`github.com/landlock-lsm/go-landlock`](#githubcomlandlock-lsmgo-landlock)
- [Rejected](#rejected)

## The allowlist

The table below lists the root module's approved direct dependencies. A pull
request that adds a direct requirement to a `go.mod` adds its row here, with
the reason, and is reviewed against it; no test enforces the list. An entry
ending in `/` approves every module under that path.

| Module | For |
|---|---|
| `github.com/anthropics/anthropic-sdk-go` | the Anthropic wire: transport, auth, retries, stream framing |
| `github.com/OpenRouterTeam/go-sdk` | the OpenRouter wire (`provider/openrouter`): client, authentication, server URL, sending, SSE framing |
| `github.com/ollama/ollama` | the Ollama wire (`provider/ollama`): Ollama's own client and request and response types for its native `/api/chat` API |
| `go.starlark.net` | code mode's sandboxed Starlark runtime |
| `github.com/modelcontextprotocol/go-sdk` | the MCP client and pool, and the MCP server of the `mcpserve` module |
| `github.com/tree-sitter/…`, `github.com/tree-sitter-grammars/…` | tree-sitter outlines: the runtime and its grammar modules |
| `github.com/bmatcuk/doublestar/v4` | glob matching in `tools.MatchGlob` and in `workspace`'s ignore engine |
| `github.com/pelletier/go-toml/v2` | TOML parsing for MCP configuration (`internal/toml`) |
| `golang.org/x/oauth2` | Google credentials for the Vertex AI deployment |
| `github.com/aws/aws-sdk-go-v2`, `github.com/aws/aws-sdk-go-v2/…` | AWS configuration and credentials for the Bedrock deployment |
| `google.golang.org/grpc` | the sandbox agent and gateway protocols' transport (sandbox and gatewayd modules) |
| `google.golang.org/protobuf` | the protobuf runtime of the generated sandbox protocol code |
| `github.com/gokrazy/rsync` | the in-process rsync protocol for sandbox `LocalDir` transfers (sandbox module) |
| `github.com/landlock-lsm/go-landlock` | the sandbox agent's own Landlock confinement (sandbox module) |

Adding a direct dependency means adding it there, with its reason, and a
ruling here. The same list covers the nested `sandbox` module, whose direct
dependencies (grpc, protobuf and, since spec 14, the rsync protocol) stay out
of the root module's graph.
It also covers the nested `sandbox/gatewayd` module (spec 19), which adds no
module: it requires only the root and sandbox modules and grpc directly
(protobuf only if a hand-written file imported it), and its graph holds
nothing the sandbox module's lacks. And it covers the nested `mcpserve` module
(spec 23), which adds no module either: it requires only the root module and
the MCP Go SDK directly, the SDK at the version the root requires, and its
graph holds nothing the root module's lacks. The
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

## `github.com/OpenRouterTeam/go-sdk`

**Used by.** `provider/openrouter`, for every request, and by nothing else in
any module.

**Version.** `v0.9.66`, pinned and moved deliberately. It is a beta 0.x SDK
generated by Speakeasy, so a minor version can change a type without notice;
an upgrade is a reviewed change that re-reads the request, stream and error
types.

**Why this module.** It is the vendor's own SDK for OpenRouter's native Chat
Completions API, which is what [PRD 14](prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md)
asks for: no compatibility endpoint, and no other vendor's SDK pointed at
OpenRouter. It owns the HTTP client, the `Authorization: Bearer` header, the
server URL and the SSE framing.

**What AgentKit still owns.**

- *Retries.* The provider runs its own retry loop and turns the SDK's off
  (`retry.Config{Strategy: "none"}`). The SDK's default retries 5xx only, not
  429, and backs off for up to an hour; it cannot see an error chunk inside a
  200 stream; and its typed errors (`*sdkerrors.TooManyRequestsResponseError`
  and the like) carry neither the HTTP status nor `Retry-After`. One loop
  handles both kinds of 429 and 5xx the same way and ends within a bounded time.
- *The request body.* The SDK's request types re-encode what must reach the
  wire as written: a tool's `parameters` is a `map[string]any`, which loses
  property order, and `reasoning_details` is a typed union that drops fields it
  does not model. AgentKit writes the body itself and the SDK sends it, through
  the `HTTPClient` the provider hands the SDK (`WithClient`), the way
  `provider/anthropic` sends its body through `option.WithRequestBody`. That
  client also records each response's status, `Retry-After` and error body for
  the retry loop. The rules the provider follows where the SDK differs from what its spec
  assumed are in [ADR 03](adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md).

**What is pulled in.** `go mod why -m` reports these modules for the root
module:

| Module | Version | Why |
|---|---|---|
| `github.com/spyzhov/ajson` | `v0.8.0` | required by the SDK's root package, so linked into `provider/openrouter` |
| `github.com/stretchr/testify` | `v1.11.1` → `v1.12.1` | not linked: the Anthropic SDK's tests import it. The SDK requires `v1.12.1`, so minimum version selection raises it |
| `go.yaml.in/yaml/v3` | `v3.0.5` | not linked: `testify` `v1.12.1` requires it |

No other version moved. The nested `sandbox` module replaces the root module, so
its graph gains the SDK and `ajson` (`testify` and `yaml/v3` are not in its
graph). Importing the SDK's root package
links the whole of its API surface, not only `Chat`.

## `github.com/ollama/ollama`

**Used by.** `provider/ollama`, for every request: it imports the module's
`api` package and nothing else of it.

**Version.** `v0.40.7`, pinned and moved deliberately. It is the version whose
tool calls carry ids and whose final chunk carries `prompt_eval_cached_count`,
both of which the provider relies on. An upgrade is a reviewed change that
re-reads the `api` types.

**Why this module.** It is Ollama's own client and types for the native
`/api/chat` API, which is what [PRD 14](prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md)
asks for: not Ollama's OpenAI-compatible or Anthropic-compatible endpoints, and
no other vendor's SDK pointed at Ollama. It owns the HTTP request, the NDJSON
line framing, the typed request and response, the insertion-ordered map of a
tool call's arguments and the `think` value.

**What AgentKit still owns.**

- *The host.* `api.ClientFromEnvironment` is not used: it reads `OLLAMA_HOST`
  from the process environment through `envconfig`, which bypasses
  `Options.Env` and `Options.Getenv` and breaks test isolation. The provider
  resolves the host itself by the CLI's rules and builds the client with
  `api.NewClient`.
- *The credential rule.* The provider sends none. The SDK has a signing path of
  its own: when `OLLAMA_AUTH` is set in the process environment, or the host
  is `ollama.com`, it reads `~/.ollama/id_ed25519`, adds an `Authorization`
  header and a `ts` query parameter, and fails the request before it is sent if
  the key is missing. The provider refuses `ollama.com`, and its transport
  deletes `Authorization` beneath the SDK; it cannot stop the SDK reading the
  variable or the key file, or the `ts` parameter, which the server receives.
- *Error status.* `Client.Chat` checks a status only on the lines of the body,
  so a 404, 5xx or 401 with an empty body returns `nil` with no chunk; a 401
  is an `api.AuthorizationError`, not an `api.StatusError`. The transport
  records each response's status so the provider can tell those from a
  truncated stream. The rules the provider follows where the API differs from what its spec
  assumed are in [ADR 03](adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md).

**What is pulled in.** `go list -deps ./provider/ollama` reaches these
non-standard modules besides the Ollama module itself (`api`, `auth`,
`envconfig`, `format`, `internal/orderedmap`, `types/model`, `version`):

| Module | Version | Why |
|---|---|---|
| `github.com/google/uuid` | `v1.6.0` | linked by `api` |
| `golang.org/x/crypto` | `v0.43.0` | `ssh`, linked by `auth` for the signing path above |
| `github.com/wk8/go-ordered-map/v2` | `v2.1.8` | the ordered map behind `ToolCallFunctionArguments` and `ToolPropertiesMap` |
| `github.com/bahlo/generic-list-go` | `v0.2.0` | required by `go-ordered-map`; already in the graph |
| `github.com/buger/jsonparser`, `github.com/mailru/easyjson` | `v1.1.2`, `v0.7.7` | required by `go-ordered-map` |
| `gopkg.in/yaml.v3` | `v3.0.1` | linked by `go-ordered-map` |

`go list -m all` grows by 64 lines (123 to 187): the Ollama module's own
`go.mod` names a server, a terminal UI and a database driver (`gin`,
`bubbletea`, `cobra`, `go-sqlite3` and their requirements), none of whose
packages `api` imports, so none is compiled in. Minimum version selection
raises `golang.org/x/crypto` to `v0.43.0`, `golang.org/x/exp` to
`v0.0.0-20250218142911-aa4b98e5adaa`, `golang.org/x/mod` to `v0.30.0`,
`golang.org/x/net` to `v0.46.0` and `golang.org/x/text` to `v0.30.0`, each
required by the Ollama module, and `gopkg.in/check.v1` to
`v1.0.0-20201130134442-10cb98267c6c`. `check.v1`, `github.com/kr/pretty` and
`github.com/rogpeppe/go-internal` are reached only through `gopkg.in/yaml.v3`'s
tests (`go mod why`), so they are not linked. The root `go.mod` lists 7 new
indirect lines and `go.sum` gains 35 and drops 6.

The nested `sandbox` module replaces the root module, so its graph gains the
Ollama module, `easyjson` and `go-ordered-map` (both linked) and `kr/pretty`
and `go-internal` (not linked), and raises only `gopkg.in/check.v1`: spec 14's
D-1 had already lifted `x/crypto`, `x/net`, `x/text` and `x/mod` there.
`mcpserve` raises the same three `x/*` versions the root does.

**The root package imports both providers (spec 29).** `agentkit.Resolve`
builds the OpenRouter and Ollama clients, so every module that imports the root
package compiles both SDKs. Go refuses that build (`go: updates to go.mod
needed`) unless the module lists the modules providing the packages, so
`sandbox`, `mcpserve`, `examples/gateway`, `examples/harness` and
`examples/sandbox` each gained seven `// indirect` lines (the two SDKs,
`ajson`, `easyjson`, `go-ordered-map`, `google/uuid` and `yaml.v3`) and their
`go.sum` hashes. In the `sandbox` module that expands the graph by the 57
modules the SDKs' `go.mod` files name and raises `testify` to `v1.12.1` and
`golang.org/x/exp` to `v0.0.0-20250218142911-aa4b98e5adaa`; none of the 57 is
linked (`go list -deps -test ./...` in the module), and the sandbox agent
binary links neither SDK. The root module's `go.mod` does not change.

**The SDK's line limit.** `Client.Chat` reads the response with a
`bufio.Scanner` whose largest line is 8,000,000 bytes (`maxBufferSize =
8 * format.MegaByte`, a megabyte being 1,000,000 here) and allocates that
buffer for each call. The 512 KB limit PRD 09 recorded does not apply to
`v0.40.7`; either limit is out of reach of a 32,768-token output cap, whose
longest line is one chunk.

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
  goroutines, and it builds for every target `internal/policy` builds.
- **A language models write.** It is a Python dialect. The tool's description
  spells out what differs (no exceptions, no imports, no classes).

**What is pulled in.** `codemode` imports `go.starlark.net/starlark`, and that
brings in `syntax`, `resolve` and two internal packages. The module's REPL
dependencies (`chzyer/readline`, `x/term`) and its protobuf support are not
imported.

**Alternatives considered.** An embedded JavaScript engine is larger, has
ambient capabilities to remove rather than none to add, and has no step
counter. A WebAssembly sandbox would need a compiler for the model's language
as well.

## `github.com/modelcontextprotocol/go-sdk`

**Used by.** `mcp`, and the nested `mcpserve` module, which serves a runner's
tools with the SDK's server and its Streamable HTTP handler (and the `mcp` and
`codemode` examples, which serve a tool from the SDK's in-process server).

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
and `internal/policy` builds both.

**What is pulled in.** `github.com/mattn/go-pointer`, and the C sources of
each grammar. Each grammar adds to the binary size.

## `github.com/bmatcuk/doublestar/v4`

**Used by.** `tools/glob.go` (`find_files`) and `workspace/glob.go` (the ignore engine, which keeps
its own error-tolerant wrapper because `workspace` imports nothing of the module).

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

**Used by.** The sandbox packages: the generated clients and servers of both
protocols in `sandbox/internal/sandboxpb`, the agent service in
`sandbox/internal/agent`, the host side in `sandbox/internal/attach`, and the
gateway client in `sandbox/gateway`. Since spec 19 also the gateway server,
`sandbox/gatewayd` and its `agentkit-gateway` command, a nested module of its
own that requires grpc directly at the sandbox module's version. Nothing
outside `sandbox/` imports it. Since spec 14 the sandbox tree is a nested
module, and grpc is a direct requirement of `sandbox/go.mod`; in the root
module it is indirect again, as it was before spec 13.

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
indirect.

## `google.golang.org/protobuf`

**Used by.** The generated code of both protocols in
`sandbox/internal/sandboxpb`, which the gateway client and the gatewayd
module reach through it; a direct requirement of the nested `sandbox`
module, and only an indirect one of `sandbox/gatewayd`, whose hand-written
files do not import it.

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

**Why this module.** Both ends of a transfer run in-process, in the host's
code and in the agent, so nothing has to be installed on either side; it
moves only changed files (the quick check over size and modification time,
and delta transfer); and the bytes travel over the agent protocol's own
`Rsync` stream, so a gateway can relay them like any other stream.

**How it is used, and why so narrowly.** v0.3.8's exclude rules are unusable
here: a pattern with `*`, `?` or `[` panics, matching is not anchored, an
excluded file drops its later siblings, and `--delete` ignores every rule
([ADR 05](adr/05-keep-the-sandbox-in-its-own-module-and-sync-through-a-filtered-view.md)). AgentKit therefore never passes it
a rule or `--delete`: the sending side serves a filtered, read-only `fs.FS`
through an `rsyncd` server, the receiving side runs an `rsyncclient`
receiver, and AgentKit decides what to delete.

**Every constructor restricts nothing.** `rsyncd.NewServer` applies Landlock
to the whole process when it has modules, and `rsyncclient` does as a
receiver, unless given `DontRestrict()`. In the local provider the process is
the embedder. Each is called in exactly one function of `rsyncx`, always with
`DontRestrict()`.

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
moved no version: the sandbox module's `go list -m all` was unchanged.

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
can make too little a `Create` failure with `Spec.RequireLandlock`. No code
calls a method named `BestEffort`.

## Rejected

PRD 09 §4 records libraries that were considered and rejected, so they are not
re-proposed without new information. Its `provider/*` row (vendor SDKs) was
reversed by PRD 10 for the one wire that remained, which runs on
`github.com/anthropics/anthropic-sdk-go`, and by PRD 14 for two more, the
OpenRouter wire on `github.com/OpenRouterTeam/go-sdk` and the Ollama wire on
`github.com/ollama/ollama`; every other ruling stands. Some rows
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
| `workspace/walk.go` | `charlievieth/fastwalk` | Parallel and unordered; callers need lexical order and the ignore engine needs parent before child. |
| `guard/guard.go` | `mvdan.cc/sh/v3/syntax` | Saves about 50 lines; the over-conservative scanner is easy to audit. Revisit if false rejections become a complaint. |
| Token estimate (compaction, deleted) | `pkoukk/tiktoken-go` | OpenAI's tokenizer is wrong for Claude and embeds about 1 MB. |
| `tools/edit.go` | go-diff, difflib, udiff | It does exact and folded matching, not diffing. |
| Middleware LRU (deleted) | `hashicorp/golang-lru/v2` | About 40 lines saved for a module; `container/list` fixed the same problem. |
| `tools/fold.go` | `x/text/unicode/norm` | NFKC does not fold smart quotes to ASCII. |
| `workspace/ignore.go` matcher | go-git `plumbing/format/gitignore` | Matches segments with `filepath.Match`, so `[!x]` stops negating and `**` does not backtrack; and it pulls go-git, go-billy and gcfg into the root. |
| Swift outline | `alex-pinkus/tree-sitter-swift` | Its Go module is an untagged commit whose test imports a path that does not exist, so `go mod tidy` fails for every importer. |
| `outline/lang.go` | `go-enry/go-enry/v2` | The extension table encodes deliberate per-backend exclusions. |
| Tracing | OpenTelemetry | The tracer was an interface already; an adapter adds code. (Deleted; the event stream replaces it.) |
