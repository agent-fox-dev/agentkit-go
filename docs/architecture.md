# Architecture

How the module is laid out, what imports what, and how a run flows through it.
The code is authoritative; when this file and a package's doc comment
disagree, fix this file.

## Shape

One Go module, `github.com/agent-fox-dev/agentkit-go` (`go 1.27`), plus
seven nested modules with their own `go.mod`, none of them in the root build
graph:

- [`codesearch/`](../codesearch), the zoekt-backed index;
- [`sandbox/`](../sandbox) (`…/sandbox`), the sandbox API, protocol, agent and
  providers, the gateway client among them, with their dependencies (gRPC,
  protobuf, the rsync protocol). It requires the root module through a
  `replace`; import paths are unchanged;
- [`sandbox/gatewayd/`](../sandbox/gatewayd) (`…/sandbox/gatewayd`), the
  gateway server and its `agentkit-gateway` command. It requires the root and
  sandbox modules through `replace ../../` and `replace ../`, directly
  requires nothing else but gRPC, and its graph holds no module the sandbox
  module's lacks. Go's `internal` rule goes by import path, so it uses the
  sandbox module's `internal/sandboxpb` and `internal/attach`;
- [`mcpserve/`](../mcpserve) (`…/mcpserve`), the MCP server for a `Runner`'s
  tools, on stdio or on loopback HTTP behind a bearer token, for a harness on
  the same machine. It requires the root module through `replace ../`,
  directly requires nothing else but the MCP Go SDK at the root's version, and
  its graph holds no module the root's lacks;
- [`examples/sandbox/`](../examples/sandbox), the example program that uses
  the sandbox module;
- [`examples/gateway/`](../examples/gateway), the example program that runs a
  gateway in process on a loopback port and dials it. It requires the root,
  sandbox and gatewayd modules through `replace`;
- [`examples/harness/`](../examples/harness), the example program that serves
  code mode and a sandbox's proxy tools to another harness (Claude Code, Pi)
  over MCP on a loopback port. It requires the root, sandbox and mcpserve
  modules through `replace`.

cgo is allowed only in `//go:build cgo` files, each with a pure-Go fallback;
`internal/policy` builds the four supported targets with cgo off and the host
with cgo on, for the root, the sandbox module, the gatewayd module, the
mcpserve module and the three examples that use them. Direct dependencies are
an allowlist ([`DEPS.md`](DEPS.md)), checked for the root, the sandbox module,
the gatewayd module and the mcpserve module.

AgentKit is two parts:

- **The hands**: `workspace`, `tools`, `outline`, `codesearch`, `codemode`,
  `mcp`, `guard`, and `sandbox`, which runs the built-in tools somewhere else.
  Each is usable without the driver.
- **One driver**: the root package, `agentkit`. It holds `Config`, `New`, the
  `Agent` and the loop, and the tool side the loop runs on: `Toolset`,
  `NewRunner` and `Runner`, with the batch engine and the nested caller behind
  it. Nothing else. The driver is the loop over a runner; the runner needs no
  driver.

The root and sandbox modules listen on no socket and serve no MCP. They are a
library: `agentkit-sandboxd` serves the agent service on its stdio, and two
nested modules listen: `agentkit-gateway` (`sandbox/gatewayd`) is the
gateway's gRPC server, and `mcpserve` serves a runner's tools over MCP, on
stdio or a loopback port, to a harness on the same machine. `internal/policy`
keeps that a checked invariant: no non-test
file of the root or the sandbox module refers to `net.Listen`,
`net.ListenTCP`, `net.ListenUnix`, `net.ListenPacket`, `net.ListenConfig`,
`tls.Listen` or `http.ListenAndServe*`. The library's only network traffic is
outbound: the Anthropic provider's requests, the MCP client's connections to
servers an embedder configures, and a gateway client's connection to its
gateway.

## Package graph

First-party imports, from
`go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./...` (tests excluded),
run in the root module and in `codesearch`, `sandbox`, `sandbox/gatewayd` and
`mcpserve`. The nested example modules (`examples/sandbox`, `examples/gateway`
and `examples/harness`) are programs and not listed. A package marked "tests
only" exists for tests: it holds only test files, or only test files import it.

| Package | Imports (first-party) | Notable third-party |
|---|---|---|
| `schema` | none | |
| `outline` | none | `github.com/tree-sitter/go-tree-sitter` and grammar modules, in `//go:build cgo` files |
| `wire` | none | |
| `internal/diag` | none | |
| `internal/toml` | `internal/diag` | `github.com/pelletier/go-toml/v2/unstable` |
| `internal/policy` (tests only) | none | |
| `internal/testkit` (tests only) | `core`, `schema` | |
| `internal/toolconformance` (tests only) | `core`, `schema` | |
| `core` | `schema` | |
| `catalog` | `core` | |
| `guard` | `core` | |
| `provider/faux` | `core` | |
| `provider` | `core`, `schema` | |
| `provider/anthropic` | `core`, `provider`, `schema`, `wire` | `github.com/anthropics/anthropic-sdk-go`, `golang.org/x/oauth2`, `github.com/aws/aws-sdk-go-v2` |
| `workspace` | none | `github.com/bmatcuk/doublestar/v4` |
| `tools` | `core`, `outline`, `schema`, `workspace` | `github.com/bmatcuk/doublestar/v4` |
| `codemode` | `core`, `schema`, `tools` | `go.starlark.net` |
| `mcp` | `core`, `schema`, `wire`, `internal/diag`, `internal/toml` | `github.com/modelcontextprotocol/go-sdk` |
| `mcpserve` (nested module) | `.` (root, `agentkit`), `core`, `mcp`, `wire` | `github.com/modelcontextprotocol/go-sdk` |
| `prompt` | `core`, `guard`, `tools` | |
| `.` (root, `agentkit`) | `core`, `catalog`, `guard`, `prompt`, `provider`, `provider/anthropic` | `github.com/anthropics/anthropic-sdk-go` |
| `examples/{agentdemo,codemode,codingagent,customtools,mcp}` (programs) | `.`, `catalog`, `codemode`, `core`, `guard`, `mcp`, `provider/anthropic`, `provider/faux`, `schema`, `tools`, `wire`, `workspace`, by program | |
| `codesearch` (nested module) | `core`, `outline`, `schema`, `tools`, `workspace` | `github.com/sourcegraph/zoekt` |
| `sandbox` (nested module; so are the rows below) | `core`, `sandbox/internal/buildver`, `tools`, `workspace` | |
| `sandbox/internal/sandboxpb` | none (generated) | `google.golang.org/grpc`, `google.golang.org/protobuf` |
| `sandbox/internal/tooldigest` | `core`, `schema` | |
| `sandbox/internal/buildver` | none | |
| `sandbox/internal/tarsafe` | none | |
| `sandbox/internal/stdioconn` | none | |
| `sandbox/internal/agentenv` | none | |
| `sandbox/internal/confine` | `sandbox`, `sandbox/internal/agentenv` | `github.com/landlock-lsm/go-landlock/landlock{,/syscall}`, in `//go:build linux` files |
| `sandbox/internal/rsyncx` | `sandbox/internal/{sandboxpb,tarsafe}`, `workspace`; `sandbox` in its Windows stubs | `github.com/gokrazy/rsync/{rsyncclient,rsyncd}`, in `//go:build !windows` files |
| `sandbox/internal/agent` | `core`, `sandbox`, `sandbox/internal/{agentenv,buildver,rsyncx,sandboxpb,tarsafe,tooldigest}`, `tools`, `workspace` | `google.golang.org/grpc` |
| `sandbox/internal/attach` | `core`, `sandbox`, `sandbox/internal/{buildver,rsyncx,sandboxpb,tarsafe,tooldigest}`, `tools`, `wire`, `workspace` | `google.golang.org/grpc`, `google.golang.org/protobuf` |
| `sandbox/internal/localtest` | `sandbox`, `sandbox/internal/{attach,sandboxpb}` | |
| `sandbox/local` | `sandbox`, `sandbox/internal/{agent,attach,localtest,stdioconn}` | |
| `sandbox/internal/gitclone` | `sandbox` | |
| `sandbox/podman` | `core`, `sandbox`, `sandbox/internal/{attach,gitclone,stdioconn}`, `tools` | |
| `sandbox/landlock` | `core`, `sandbox`, `sandbox/internal/{attach,buildver,gitclone,stdioconn}` | |
| `sandbox/images` (tests only) | none | |
| `sandbox/internal/gatewaywire` | `sandbox`, `sandbox/internal/sandboxpb` | |
| `sandbox/gateway` | `core`, `sandbox`, `sandbox/internal/{attach,gatewaywire,gitclone,sandboxpb}` | `google.golang.org/grpc` |
| `sandbox/gatewayd` (nested module; so are the two rows below) | `sandbox`, `sandbox/gateway`, `sandbox/gatewayd/internal/token`, `sandbox/internal/{attach,buildver,gatewaywire,sandboxpb}`, `wire` | `google.golang.org/grpc` |
| `sandbox/gatewayd/internal/token` | none | |
| `sandbox/gatewayd/cmd/agentkit-gateway` | `sandbox`, `sandbox/gateway`, `sandbox/gatewayd`, `sandbox/gatewayd/internal/token`, `sandbox/internal/buildver`, `sandbox/podman` | |
| `sandbox/cmd/agentkit-sandboxd` | `sandbox`, `sandbox/internal/{agent,buildver,confine,stdioconn}` | |
| `sandbox/internal/fakeagent` (tests only) | `sandbox/internal/{sandboxpb,tooldigest}`, `tools`, `workspace` | `google.golang.org/grpc` |
| `sandbox/internal/relaytest` (tests only) | `sandbox`, `sandbox/internal/{agent,attach,rsyncx,sandboxpb}` | `google.golang.org/grpc` |
| `sandbox/internal/treegen` (tests only) | `sandbox/internal/tarsafe`, `workspace` | |
| `sandbox/gatewayd/internal/gwtest` (tests only) | `sandbox`, `sandbox/gateway`, `sandbox/gatewayd`, `sandbox/internal/{attach,fakeagent,sandboxpb}`, `sandbox/local` | `google.golang.org/grpc` |

What follows from it:

- Every package that handles tools, messages or models imports `core`. The
  leaves `schema`, `outline`, `wire`, `workspace` and `internal/diag` import
  nothing of the module.
- `core` imports only `schema`. It is the vocabulary and owns the interface
  seams: `Tool`, `ProviderClient`, `NestedCaller`, the interceptor types.
- `workspace` imports nothing of the module: only the standard library and
  doublestar, so every package can depend on it (`internal/policy` checks it).
- `tools` imports `core`, `outline`, `schema` and `workspace`. `codemode`
  imports `core` and `schema`, and `tools` for its output buffer
  (`tools.Accumulator`). `mcp` imports `core` and `schema`, and `wire` for its
  strict boundary. `guard` imports `core` and nothing else.
- The root imports `core`, `catalog`, `guard`, `prompt`, `provider` and
  `provider/anthropic`, and neither `tools` nor `workspace` in non-test code.
  It reaches `tools` only through `prompt`, which takes the file-navigation
  tool names and the shell guideline texts from `tools`. The root imports the
  SDK because `Config.Client` is an SDK client; `New` builds the provider over
  it.
- The root package is imported by `mcpserve` and by programs: this module's
  `examples/` and the nested example modules. No library package of the root
  module imports it, so the tools, the guard, code mode and the sandboxes do
  not depend on the driver.
- `mcpserve` imports the root (for `agentkit.Runner`), `core`, `mcp` (for its
  pipe transport) and `wire`, and the MCP SDK. It imports neither `tools` nor
  `workspace`, nor the sandbox tree: it serves whatever tools a runner holds.
- `codesearch` imports `core`, `outline`, `schema`, `tools` and `workspace`,
  and not the root or the sandbox tree.
- The sandbox tree imports `core`, `schema`, `tools`, `workspace` and `wire`,
  and nothing outside it imports the sandbox tree except `examples/sandbox`,
  `examples/gateway` and `examples/harness` (nested modules, outside the
  root's check) and test helpers under `internal/` (`internal/policy` checks
  this, and that no non-test file outside it imports gRPC). `core`, `tools`,
  `codemode` and the driver do not know a sandbox exists.

## Packages

| Package | Owns |
|---|---|
| `.` | `Config`, `New` and the `Agent` (`agent.go`; its surface is `Run`, `Stream`, `Messages`, `Usage`, `ReachableTools`, `Runner`), the loop (`loop.go`), and the tool side the loop is built on: `Toolset`, `NewRunner` and `Runner` (`runner.go`), the runner's batch engine (`batch.go`), its nested caller (`nested.go`) and the unguarded-shell check (`execguard.go`). |
| `core` | Messages, content blocks, events and their JSON form (`MarshalEvent`), `EventStream`, `Tool`, `ToolResult`, `ToolPolicy`, argument preparation, interceptors, `ProviderClient`, `Request`, `StreamEvent`, `Model`, `Usage`, stop reasons, errors. |
| `schema` | JSON Schema values and typed combinators; `Parse` decodes a schema document keeping property order; `Validate` and `Coerce` check and repair tool arguments. |
| `catalog` | The embedded Claude model catalog (`catalog.json`) and `Lookup`: context window, output cap, prices, thinking kind, with a usable default for an unlisted id. |
| `provider` | What a wire needs and does not own: cost arithmetic (`ComputeCost`), the per-session tool-schema cache (`ToolPrefix`), deferred-tool splitting. |
| `provider/anthropic` | The Anthropic Messages wire over the official SDK, direct, on Vertex AI or on Bedrock, chosen by `Resolve`; send-time transcript repair (`RepairTranscript`) and partial-JSON salvage of streamed tool arguments (`SalvageJSON`). |
| `provider/faux` | A scripted provider for offline tests and demos. |
| `outline` | Declarations of a source file with line ranges. Go uses `go/ast` in every build. With cgo, Python, JavaScript, TypeScript/TSX, Java, Kotlin, C#, Scala, Rust, C, C++, PHP, Ruby, Lua and shell are parsed by tree-sitter grammars; without cgo they are `none`. `IsTestFile` is the test-file convention the symbol and reference ranking in `tools` uses. |
| `workspace` | The workspace: the containment boundary (`Open`, `Resolve`, `Rel`, `CheckWriteTarget`), the layered ignore engine (`IgnoreOptions`, `NoGlobalExcludes`, `Ignore`, `Ignored`) and the directory walk (`Walk`), opened once and handed to the tools and the index; the sandbox tree opens its own over a root it owns and takes a workspace's `Ignore()` options. It imports only the standard library and doublestar. |
| `tools` | The built-in tools (`For`), the output accumulator, process control and the runner (`Run`, `RunArgv`), glob, the symbol table behind `find_symbol` and the reference engine behind `find_references` (`References`). |
| `guard` | The shell boundary: `Check` (a pure decision on an argv), `Commands` (the simple commands a shell line runs, read once for every policy), `Restricted` (the interceptor over both), `Allow` (`Restricted` with only the allowlist), `AllowAll`. |
| `codemode` | The code-mode tool (below). |
| `prompt` | `Build(system, tools)`: the base prompt, then the active tools' guidelines, deduplicated in first-seen order. |
| `mcp` | The MCP client over the official MCP Go SDK (below). The MCP server is the nested `mcpserve` module's. |
| `wire` | A bounded, strict parser for untrusted bytes on `encoding/json/jsontext`, and frame readers. |
| `internal/toml` | A lenient TOML reader for `mcp.ParseConfig`, over `go-toml/v2/unstable`. |
| `internal/diag` | The shared non-fatal `Diagnostic`. |
| `internal/policy` | Tests only: the build gates and the dependency allowlist. |
| `internal/testkit` | Shared test helpers. |
| `internal/toolconformance` | Tests only: the spec 06 conformance case table, run against `tools.For` and against sandbox proxy tools. |
| `sandbox` | The host-side sandbox API: `Spec`, `Capabilities`, `Provider`, `Sandbox`, `CheckSpec`, `LocalDir`, `Tar`/`TarOut`, `LandlockInfo`, `AgentArgs`, the protocol version, `MaxMessageBytes` and the errors (below). |
| `sandbox/internal/sandboxpb` | The `agentkit.sandbox.v1` protocols, the agent's (`sandbox.proto`) and the gateway's (`gateway.proto`), and their checked-in generated code; `make proto` regenerates both. |
| `sandbox/internal/agent` | The agent service: runs `tools.For` over one workspace with an explicit environment. |
| `sandbox/internal/attach` | The one `Sandbox` implementation, for every provider: handshake, state machine, failure classification, proxy tools, tar transfer. |
| `sandbox/internal/tarsafe` | The tar entry rules both directions share, the `os.Root` extractor, the export validator and the archiver. |
| `sandbox/internal/tooldigest`, `sandbox/internal/buildver` | The definition digest host and agent share; the agent's version: the link-time stamp, else the sandbox module's version from the build info, else `(devel)`. |
| `sandbox/internal/stdioconn` | A `net.Conn` over a reader and a writer: stdin/stdout, or two `os.Pipe` pairs. |
| `sandbox/internal/confine` | The agent's Landlock confinement: `Plan`, the ruleset, `Setup`. Only `agentkit-sandboxd` imports it. |
| `sandbox/internal/agentenv` | Reads the agent's explicit environment: a variable, the temporary directory. |
| `sandbox/local` | The in-process provider with no isolation. |
| `sandbox/podman` | The Podman and Docker provider: a hardened container per sandbox, driven through the engine's command line. |
| `sandbox/landlock` | The Landlock provider: `agentkit-sandboxd` as a confined child process on Linux with Landlock ABI 4. |
| `sandbox/internal/gitclone` | `GitRepo` validation and the `git clone` argument list. |
| `sandbox/internal/gatewaywire` | The conversions between `Spec` and `Capabilities` and the gateway's wire messages, shared by its client and its server. |
| `sandbox/gateway` | The gateway client: `Dial` returns a `sandbox.Provider` whose sandboxes live on a remote gateway. |
| `sandbox/gatewayd` | The gateway server (nested module): authentication, ownership, the sandbox lifecycle and the Agent relay over one backend provider. |
| `sandbox/gatewayd/cmd/agentkit-gateway` | The gateway command: a TCP server over the Podman or Docker provider. |
| `mcpserve` | The MCP server (nested module): one MCP tool per tool of an `agentkit.Runner`, every call through `Runner.Call`, served on stdio (`ServeStdio`) or on loopback HTTP behind a bearer token (`ServeLoopback`). |
| `sandbox/cmd/agentkit-sandboxd` | The agent command. |

## The embedder's path

What is built from what, in the order an embedder builds it. Each layer owns
what its line says and takes the layer above as a value.

```
workspace.Open      the root every scenario opens first: containment
                    (Resolve, CheckWriteTarget), the ignore rules, the walk
tools.For           the built-in tools for that workspace; tools.Options sets
                    spill, environment, symbol bounds and an optional index
shaping the set     core.Named picks tools by name; codemode.New binds some of
                    them into one code_mode tool; mcp Pool.Tools adds an MCP
                    server's tools; sandbox.Open returns proxies that stand in
                    for tools.For and fills the sandbox from a source
agentkit.Toolset    the tool side of an agent as a value: Tools, the Policy
                    that selects from them, Guard, After and an Events sink
agentkit.NewRunner  checks the toolset, resolves the policy and owns the
                    pipeline that runs a tool call
  Runner.Call/Batch   run tool calls with no model
  agentkit.New        the agent loop over the runner: Config.Toolset goes to
                      NewRunner, and Agent.Runner returns the runner
  mcpserve.New        the runner's tools served over MCP, on stdio or on
                      loopback HTTP
```

Nothing below `agentkit.Toolset` knows which of the three uses it has. The
pipeline is the runner's, once: a call a model makes in a run, a call an
embedder makes with `Runner.Call` and a call an MCP client makes through
`mcpserve` pass through the same steps, described next.

## Data flow of one call

`Runner.Call(ctx, name, input)` is a batch of one: it makes a
`core.ToolUseBlock` with a generated `call_` id and runs it through
`Runner.Batch`, which is the engine (`batch.go`) the agent loop calls too. The
steps are the same for one call, for several, and for the calls of a model's
turn. A `tools/call` that an MCP client makes through `mcpserve` is this call
too: [MCP serving](#mcp-serving) describes how it reaches the runner.

```
prepare       sequential, in call order, on the calling goroutine. Per call:
                1. ToolExecutionStartEvent
                2. look the tool up by name (unknown_tool)
                3. argument preparation (core.PrepareArguments): the tool's
                   own PrepareArguments, explicit nulls dropped, coercion,
                   then validation against the input schema
                   (invalid_arguments)
                4. Toolset.Guard: it may block the call (blocked_by_policy),
                   vote Terminate with the block, or replace the arguments
              A call that ends at 2, 3 or 4 is finalized on the spot.
abort         one decision, after prepare and before any handler starts: if
              ctx is done, every call not yet finalized is aborted ("Operation
              aborted") and no handler runs
execute       one goroutine for each call, all joined before the batch
              returns; in call order instead when at most one handler runs or
              a Sequential tool is among the calls. A tool that declares
              ReachableTools runs with a nested caller on its context
finalize      for each call as its handler returns, under a mutex scoped to
              the batch: Toolset.After (it may replace the call's vote), then
              ToolExecutionEndEvent, then ToolResultEvent
```

The result is one `core.ToolResult` for each call, in call order. A call that
failed at any step is a result with `OK` false: an unknown tool, bad
arguments, a block, a panic and an abort each have an error code, and none is
a Go error. `ToolResult.Terminate` carries the call's final vote: the
handler's, replaced by `After`'s when it gave one, true for a wrapper whose
nested call an interceptor terminated, and the guard's for a call it blocked.
`Batch` acts on no vote; ending a run is the agent's.

Events go to one place. A call made through a runner emits to
`Toolset.Events`, which may be nil; the calls of a run emit to the run's
stream instead. A panic in the sink drops the event being delivered.

What the runner does not own: the transcript, usage, the turn count, the run
slot and the stop reason. Those belong to the `Agent`, so a call made through
a runner is in no transcript, adds nothing to `Usage` and does not claim the
run slot. A nested call (code mode) is a call too: it takes the same steps
with its parent's id on its events (below).

## Data flow of one run

```
agentkit.New   checks the model side (a Client or a Provider, and a Model),
               then calls NewRunner(cfg.Toolset): it checks every tool
               (exactly one of Handler/Execute, no reach cycle, no wrapper
               reaching a Terminating tool), resolves Toolset.Policy and
               refuses a reachable shell tool with no Toolset.Guard, so New
               refuses what NewRunner refuses; then it looks up the model in
               the catalog and builds the provider
Agent.Run      = Stream, then wait for the RunResult
Agent.Stream   claims the run slot (core.ErrBusy if taken), applies
               Config.Timeout, starts runLoop on a goroutine
  runLoop (loop.go), per turn:
    1. beforeRequest: stop with RunStopBudgetExceeded if the run has spent
       Config.MaxCostUSD; then outboundView: prune old tool results when the
       estimate reaches Config.Prune.Threshold of the context window, and
       stop with RunStopError if it still exceeds the window
    2. callModel: prompt.Build(Config.System, tools) and a core.Request
       (Prefix, the view, ToolWires, MaxTokens, Effort);
       core.EventStreamOf(ProviderClient.Stream(ctx, req)); every provider
       event is forwarded onto the run's stream
    3. the assistant message is recorded; an Error or Aborted stop reason
       ends the run here, before tool extraction
    4. tool_use blocks present? (the stop reason never decides)
         max_tokens: none run; each gets the fixed "not executed" result
         (this stays in the loop; the runner never sees these calls)
         otherwise the turn's calls go to the runner's batch engine
           (Runner.run, batch.go) with the run's stream as the event sink,
           the assistant message and the turn count for the guard, and the
           agent's usage reporter on the context: prepare -> abort ->
           execute -> finalize, as in "Data flow of one call"; the loop
           records one ToolResultMessage per call
    5. afterTurn: stop on a terminate vote (RunStopToolTerminate), a done
       context (RunStopTimeout or RunStopAborted), no tool calls
       (RunStopEndTurn, or RunStopRefusal after a refusal), or
       Config.MaxTurns (RunStopMaxTurns)
  endRun: RunResult, AgentDoneEvent; the slot is freed before the stream ends
```

Observation is the event stream. `Stream` returns it; `Run` waits for its
result without reading the events. Errors the run survives (a panicking tool,
interceptor or provider) are `core.ErrorEvent`s on it. The only interception
is `Toolset.Guard` and `Toolset.After`.

`Agent.Runner()` returns the runner the loop runs its calls through, so an
embedder can make calls beside a run, through the same toolset. The third use
of a runner is [MCP serving](#mcp-serving), whose call is described there once.

## Invariants worth knowing before editing

- **The loop iterates on the presence of `tool_use` blocks**, never on the
  stop reason. Only `error` and `aborted` short-circuit.
- **One `ToolResultMessage` per call.** Coalescing into the wire's shape is
  the provider's job.
- **The abort decision for a tool batch is made once**, after prepare and
  before any handler starts. A cancelled batch runs all of its prepared
  handlers or none.
- **The pipeline exists once**, in the runner (`Runner.run`, `batch.go`). A
  model's call in a run, `Runner.Call`, `Runner.Batch` and a `tools/call`
  through `mcpserve` all go through it, so each is prepared, guarded,
  finalized and reported the same way. There is no second copy of the steps
  for the agent, the embedder and the server to keep in step.
- **Calls through a runner are outside every run.** They are in no transcript,
  add nothing to `Usage`, claim no run slot and emit to `Toolset.Events`, not
  to a run's stream. A runner is safe for concurrent use; an `Agent` runs one
  prompt at a time.
- **`Guard` and `After` belong to the toolset**, not to the agent. `Config` has
  neither: `Config.Toolset.Guard` and `Config.Toolset.After` are the only
  interception, for the agent's calls and for every call made through a runner
  alike.
- **Every call gets a result.** Unknown tool, invalid arguments, a block, a
  panic, an abort: each is a result with `IsError` set.
- **The batch finalize mutex is batch-scoped**, never the agent mutex, and no
  user code runs while the agent mutex is held.
- **Panics in third-party code are contained** (tool handlers, interceptors,
  argument preparation, the provider) and reported as `core.ErrorEvent`s. A
  panicking `Guard` blocks the call. A panic in the `Toolset.Events` sink is
  contained too, but the event is dropped and not reported: the sink that would
  receive the report is the one that panicked.
- **A shell tool with no `Guard`** is refused by `NewRunner`, and so by `New`,
  with `core.ErrUnguardedExecute`; `guard.AllowAll` is the explicit opt-out. A
  shell tool reachable through a wrapper counts too:
  `... (wrapper "code_mode" reached shell tool "execute")`.
- **Untrusted bytes go through `wire`**: bounds before allocation, duplicate
  keys rejected. The Anthropic provider guards each stream event and response
  body with `wire.Guard`; the MCP client guards every frame and body before
  the SDK decodes it. Locally authored config decodes leniently.
- **`core.Tool.OutputSchema` never reaches a provider.** `core.ToolWire` has
  no such field. It is not checked at runtime.
- **No global state, no `init()` registration.** The provider lives on the
  agent.
- **The transcript is append-only.** `Agent.Messages()` returns a copy;
  pruning changes only the request.
- **One run at a time.** A `Run` or `Stream` that overlaps another fails with
  `core.ErrBusy`; `Messages`, `Usage`, `ReachableTools` and `Runner` are safe
  during a run.

## Nested tool calls

A tool can call other tools on the model's behalf, for example a script runner
that turns tools into functions. It declares them up front in
`core.Tool.ReachableTools`, and nothing else is reachable.

- **Registration.** `NewRunner`, and so `New`, refuses a hierarchy in which a
  tool reaches itself:
  `agentkit: reachable tools cycle detected: toolA -> toolB -> toolA`. It
  also refuses one in which a wrapper reaches a `Terminating` tool:
  `agentkit: terminating tool "finish" cannot be reached through wrapper "code_mode"`.
  Cycles are found by name along the path, because tools are values.
- **Policy.** `ToolPolicy.Resolve` applies `NoTools`, `ToolNames` and
  `ExcludeTools` to reachable tools at every depth. A wrapper left reaching
  nothing is dropped, so an allowlist must name the children as well as the
  wrapper. `ToolPolicy.Tools`, when non-nil, is used verbatim.
- **Queries.** `core.ReachableTools(tools)` is the deduplicated transitive
  closure in depth-first order. `Runner.Reachable()` and
  `Agent.ReachableTools()` are that closure over the resolved set, which is
  what a check like "this run is read-only" must inspect.
- **Dispatch.** The runner's batch engine attaches a `nestedCaller`
  (`nested.go`) to the context of every tool that declares `ReachableTools`,
  whether the call came from an agent run, `Runner.Call` or `Runner.Batch`.
  It is bound to that call's id and name and to the wrapper's reachable tools.
  The tool calls `core.CallNested(ctx, calls...)`; with no caller attached (a
  tool that declares no `ReachableTools`, or one invoked by something that is
  neither an agent run nor a runner), it gets `core.ErrNoNestedCaller`.
- **Pipeline.** A nested call goes through the same steps as a direct one:
  1. prepare and validate the arguments;
  2. `Toolset.Guard`, whose context carries `ParentToolUseID` and
     `ParentToolName`;
  3. the handler;
  4. `Toolset.After`;
  5. `ToolExecutionStart`/`End` events with `ParentToolUseID`, on the run's
     stream, or on `Toolset.Events` for a call made through a runner.

  Each nested call gets a fresh id. Preparation runs in call order. Handlers
  run concurrently unless a `Sequential` tool is among the calls, and results
  come back in call order. Every nested call that opens on the stream closes
  there, including one blocked or cut short by a terminate vote.
- **Interceptors run concurrently.** Within one `CallNested`, preparation is
  ordered and `After` is serialized, as in a direct batch. But `Guard` for
  different wrappers' nested calls, and a nested `After` alongside a direct
  call's finalization, can run at the same time. Interceptors must be safe
  for concurrent use. An `After` must not run a tool through `CallNested` on
  the context it is handed: that call would wait on the finalization its own
  call holds.
- **Results, not errors.** A tool the wrapper does not reach is
  `unknown_tool`. A block is `blocked_by_policy` with the reason, and a
  cancelled call is `aborted` with `Operation aborted`. `CallNested` returns
  an error only when an interceptor votes to end the run (`Block` with
  `Terminate`, or `After`'s `Terminate`). The error is `core.ErrTerminated`,
  and the wrapper's result carries `Terminate` to the batch.
- **Terminate votes.** A nested handler's own terminate vote is dropped. The
  nested result's detail says `terminate vote ignored`, and the wrapper's says
  `nested terminate vote ignored`.
- **Out of the transcript.** Nested calls emit no `ToolResultEvent` and never
  enter the transcript. Usage a nested handler reports through
  `core.ReportUsage` still reaches the agent at once.

## Code mode

`codemode.New(tools, opts)` returns one tool, `code_mode` by default, that
runs a model-written Starlark script, together with a `BuildInfo` (description
size, bound tool count). The tool declares `tools` as its `ReachableTools`, so
the tool policy, the shell guard and the cycle checks treat it as the wrapper
it is. Its description is generated from the bound tools' own input and output
schemas (`RenderSignature`).

A run:

1. **Thread.** The handler starts a fresh `starlark.Thread`. Its globals are
   Starlark's own builtins, one function per bound tool, and `parallel`,
   `call` and `is_error`. `load` is refused.
2. **Calls.** A tool function takes keyword arguments only. It encodes them
   as JSON and calls `core.CallNested`, so each call goes through the agent's
   nested-call pipeline with the `code_mode` call as parent. `parallel` sends
   at most `MaxConcurrentCalls` calls per `CallNested`.
3. **Results.** A success returns `Data`, converted to Starlark through JSON,
   or `{"text": Text}` when there is no `Data`. A failure, including a block,
   returns a `tool_error` value the script checks with `is_error`; nothing is
   raised.
4. **Limits.** Four limits stop a script, each with its own error code:
   `MaxTimeout` (`timeout`), `MaxSteps` (`step_limit_exceeded`), `MaxCalls`
   (`call_limit_exceeded`, checked before each dispatch) and
   `MaxOutputBytes` (`output_limit_exceeded`). A terminate vote from an
   interceptor stops the script and sets `Terminate` on its result.
5. **Output.** Printed lines and the return value (`main()`, else `result`)
   go into a `tools.Accumulator` in middle-truncation mode. Output past the
   limit keeps its head and tail and is spilled to `SpillDir`.
6. **Result.** The output text, with `Data` holding `output`, `return_value`
   and `calls_completed`. A failed or stopped script returns its error code
   (`syntax_error`, `runtime_error`, `invalid_arguments`, a limit, `aborted`)
   with the line, the partial output and the calls that completed. Their side
   effects are not undone.

Code mode runs outside an agent run through a `Runner`: `Runner.Call(ctx,
"code_mode", script)` runs the script and each tool call it makes through the
same pipeline an agent run uses, with no agent and no provider, and the nested
calls reach `Toolset.Events` with the `code_mode` call as their parent.
`core.ErrNoNestedCaller` is what a tool gets when it declares no
`ReachableTools`, or when something that is neither an agent run nor a runner
invokes it: code mode never runs a tool around the pipeline. An embedder that
is its own driver — one that serves the tools to another harness over MCP,
say — builds a runner and calls it. [`examples/harness`](../examples/harness)
does exactly that, serving the runner through `mcpserve`, and nothing else of
the driver.

## MCP serving

`mcpserve` is the other direction: a nested module that serves one runner's
tools to a harness that is not AgentKit's own loop (Claude Code, Pi, any MCP
client on the same machine). It is the third use of a runner (see "The
embedder's path"), and this section is the one place the served call is
described; the data-flow sections link here.

```
transport    ServeStdio: newline-delimited JSON on the process's stdin and
             stdout (mcp.NewPipeTransport); wire.Guard holds every inbound
             frame to wire.Defaults() before the SDK decodes it.
             ServeLoopback: Streamable HTTP on 127.0.0.1, on a port the system
             chooses
bearer       HTTP only, before anything else: Authorization: Bearer <token>,
             compared in constant time, else 401 and the body is never read;
             then the path (only /mcp), then the body, bounded (413) and
             checked by wire.Guard (400), and only then the SDK's handler
tools/list   answered from the tools registered by mcpserve.New: Runner.Tools()
             read once, one MCP tool for each, under the tool's name,
             description and json.Marshal of its input schema
tools/call   Runner.Call(ctx, name, the request's argument bytes), and nothing
             else, so the steps of "Data flow of one call" apply: the guard
             sees a direct call and a code_mode script's nested call alike
result       the core.ToolResult becomes LLMText() and the result's text
             blocks, with isError set when the call failed: a guard block is
             an error result and never a protocol error
```

`mcpserve.New` refuses, naming the tool, a schema the SDK would reject by
panicking. The SDK closes a session gracefully and cancels a call only when its
client asks, so each session and each endpoint carries a lifetime, passed as a
value of the context the SDK server is connected with, which a call's context
also obeys; closing a session or an endpoint cancels the calls still running.

## MCP client

The protocol is implemented by the official Go SDK,
[`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk),
which negotiates the protocol version with each server.

`mcp.Pool.Connect` consumes servers over stdio (`command`), Streamable HTTP
(`url`) or the 2024-11-05 HTTP+SSE transport (`url` with `transport = "sse"`).
`Pool.Tools` adapts their tools to `core.Tool`, named `<server name>__<tool>`
unless `tool_prefix` says otherwise, and fails with `ErrNameCollision` on a name that
collides with an existing tool, a name in `Pool.NativeTools` or another
server's tool. `mcp.Connect` opens a connection
over any SDK transport (`mcp.NewPipeTransport` joins a client to an
in-process server), and `ServerConnection.Session` returns the live SDK
session for anything the pool does not wrap. Configuration keys and limits:
[`configuration.md`](configuration.md).

What AgentKit adds on the client:

- a stdio server runs in its own process group with exactly the configured
  environment, and is killed as a group on close; its stderr is forwarded line
  by line to `ConnectionOptions.Warnf`;
- `${VAR}` references in `env` and `headers` are resolved at connect time; one
  that resolves to nothing is an `*UnresolvedVariableError` and nothing is
  spawned;
- a dead server is re-spawned (or re-opened) at the next call, at most
  `per_session_reconnect_limit` times, and never by re-sending a call that was
  cut off;
- every inbound frame (stdio), JSON response body and SSE event (HTTP) is
  checked strictly before the SDK decodes it;
- the HTTP client sends the configured headers and does not follow redirects,
  because those headers may carry a bearer token;
- results are capped at 50,000 characters (`ResultCharCap`), calls are
  counted against `per_session_call_limit`, and sampling is advertised only
  for a server with `allow_sampling` and answered only when
  `ConnectionOptions.Sampling` is set;
- a tool's `outputSchema` becomes `core.Tool.OutputSchema`. A schema whose
  root type is not `object` is wrapped as `{"value": <schema>}`, matching how
  such a result's `Data` is shaped. A schema that is not a JSON object is
  dropped, the tool is still imported, and `Pool.Diagnostics()` gains a
  `SeverityError` entry
  (`mcp: server "srv" tool "broken": output schema dropped: ...`);
- a call's `Data` is the server's `structuredContent` when it is a JSON
  object, `{"value": <structuredContent>}` for any other JSON value, and
  `{"text": <joined text>, "content": <raw content blocks>}` when there is
  none. A result with `isError` set is `Error: "tool_error"` with the joined
  text as `Detail`.

Constants: `DefaultCallLimit` 1000 per session, `DefaultReconnectLimit` 3,
`DefaultTimeout` 30 s per connect, list and call.

## Symbol and reference navigation

- `find_symbol` searches workspace declarations by name, backed by a lazily
  built, bounded in-memory symbol table, refreshed after `write_file`,
  `edit_file` and the shell tools run.
- `find_references` finds usages and callers of a declaration. Go uses exact
  type resolution (`go/parser`, `go/types`, workspace-local imports, external
  imports stubbed as empty packages): a use is `resolved` when it resolves to
  the target, and `lexical` when its type comes from a stubbed import. Other
  languages use outline-attributed lexical matching: a hit is `lexical`
  outside the comments and strings tree-sitter finds, and `text` in a build
  without cgo. Each site is attributed to its enclosing declaration.
- **Caching.** The tool keeps a reference cache (`referenceCache`) for the
  life of the tool set: the last Go type-check, parsed files, outlines and
  per-name candidate file lists. `write_file` and `edit_file` mark the file
  and its Go package dirty; the shell tools mark everything for revalidation;
  a changed `.gitignore` or `.ignore` drops the Go check and the candidate
  lists. Nothing is written to disk.
- **Bounds.** A pass is bounded by `SymbolOptions.MaxFiles` and
  `SymbolOptions.MaxDuration` (50 000 files and 2 s by default). When the
  budget runs out the pass keeps the sites found so far and sets `Partial`.

### `tools.References`

Embedders can search references without a model through
`tools.References`:

```go
func References(ctx context.Context, ws *workspace.Workspace, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)

type ReferenceOptions struct {
	Path         string // subdirectory filter; "" for the whole workspace
	IncludeTests bool   // false in the struct; the tool defaults to true
	MaxResults   int    // 0 or negative means 30; capped at 100
}
```

`ReferenceResult` carries `Target`, `Sites`, `Backend` (`go/types`,
`lexical` or `text`), `Partial`, `PackagesChecked`, `Errors` and `Truncated`.
Each `ReferenceSite` has `Path` (slash-separated, workspace-relative), `Line`
and `Column` (1-based), `Confidence` (`resolved`, `lexical` or `text`),
`Enclosing` (the innermost declaration spanning the site, or `Kind: "file"` at
top level) and `Source` (the trimmed line, at most 200 bytes).

It enforces workspace containment, validates `Path`, and ranks and truncates
as the `find_references` tool does. It always uses the default
`SymbolOptions` bounds; when one is reached it returns the sites so far with
`Partial` set and a nil error. Unlike the tool it keeps no cache between
calls. `PackagesChecked` counts the Go package type-checks the pass used, and
`Errors` the parse and type errors they collected; both are 0 when no Go code
was checked.

## Sandboxes

`sandbox` lets an embedder run the built-in tools inside a sandbox: an
ephemeral environment that holds exactly one workspace and one agent
process. Containment comes from where the tools run; the driver does not
change. PRD 11 (`docs/prd/11-run-tools-in-an-isolated-sandbox.md`) holds the
design; spec 13 built the foundation, and later scopes add the providers that
isolate. `sandbox.Open` sits over `Provider.Create`, `Sandbox.Sync` and
`Sandbox.Tools`, running them in that order and closing the sandbox if `Sync`
or `Tools` fails; the steps below describe what each of them does.

```
Provider.Create   CheckSpec(Capabilities, Spec): every field the provider does
                  not honour is refused (*SpecError, ErrUnsupported); then the
                  provider starts an agent and hands attach a connection
attach            gRPC client over that connection (one-shot dialer, never
                  redialled); Hello: a different protocol major is refused
                  (ErrProtocol), any minor accepted; TTL timer started
Sandbox.Sync      Tar: a TarStart chunk, then the tar in chunks of at most
                  1 MiB; the agent extracts through tarsafe as the bytes
                  arrive. LocalDir: one Rsync stream (below)
Sandbox.Tools     Tools RPC; the host builds its own tools.For and compares
                  names, order and definition digests (ErrToolMismatch);
                  returns one proxy per tool
proxy Execute     Call RPC with the model's bytes verbatim; the agent runs the
                  tool with the RPC context; Data and Metadata come back as
                  JSON bytes and pass wire.Guard
Sandbox.Export    TarOut: ExportTar; the host re-validates every entry before
                  the destination sees it. LocalDir: one Rsync stream
Sandbox.Close     timer stopped, connection closed, the provider's destroy step
```

**The agent service.** `agentkit.sandbox.v1` has one service, `Agent`, with
`Hello`, `Tools`, `Call`, client-streaming `SyncTar`, server-streaming
`ExportTar` and, since protocol 1.1, bidirectional `Rsync`; since protocol
1.2 `HelloResponse` carries the agent's `LandlockReport`. Tool input, result data and metadata travel as JSON bytes, so
schemas stay the `tools` package's own and new `ToolMetadata` fields need no
proto change; `CallRequest` and `CallResponse` are self-contained. Every
message is bounded by `sandbox.MaxMessageBytes` (16 MiB, the same number as
`wire.Defaults`). The agent builds `tools.For` over a workspace opened at its root, with its explicit
environment (never its process's own), `workspace.NoGlobalExcludes()`, and a spill directory under
that environment's `TMPDIR`; the options are fixed by the first `Tools` RPC,
and a successful sync rebuilds the tool set so the symbol table sees the new
files. At its TTL it cancels every in-flight call, which kills the process
groups those calls started, and stops. `agentkit-sandboxd` serves it on stdin
and stdout; `local` runs it in-process over two `os.Pipe` pairs through the
same adapter.

**What a proxy guarantees.** A proxy carries the host's own definition
(`Name`, `Description`, both schemas, `PromptGuidelines`, `Builtin`,
`ExecutionMode`, `PrepareArguments`, `Label`), so the model sees exactly what
`tools.For` would show; it keeps no local `Execute`. Its result looks to the
driver like a local one:

- a cancelled host context returns `aborted` / `Operation aborted`, the batch
  executor's own result, and the sandbox stays live;
- an input over `MaxMessageBytes` returns `arguments_too_large` without an
  RPC, and the sandbox stays live;
- a transport failure, or `Data`/`Metadata` that fail `wire.Guard` or do not
  decode, returns `sandbox_unavailable` — for that call and every later one;
- an agent's `unknown_tool` is passed through.

Proxies are safe for concurrent use and serialise nothing on the host; the
batch executor decides what runs in sequence. `checkShellGuard` sees a proxy
`execute` by name, so `New` refuses proxy shell tools with no `Guard`, and
`codemode.New` binds proxies unchanged.

**The state machine.** A sandbox is live, then unavailable, then closed.

| From | To | When |
|---|---|---|
| live | unavailable | an RPC error other than the agent's deliberate refusals (`InvalidArgument`, `FailedPrecondition`, `NotFound`, `Unimplemented`) while the host context is live; a malformed response; the agent's connection ending; the TTL |
| live, unavailable | closed | `Close` (idempotent; a later `Close` returns nil) |

A failing `TarOut` destination is the caller's error and changes nothing.
Once unavailable or closed, `Tools`, `Sync` and `Export` return
`ErrUnavailable` or `ErrClosed` without an RPC, and proxies return
`sandbox_unavailable` (detail `sandbox closed` after `Close`). There is no
retry and no reconnect. A refusal — an unsafe tar entry, a missing export
path, differing tool options — never changes the state. The context given to
`Create` bounds creation only.

**Tar transfer.** One function, `tarsafe.Validator.Check`, applies the entry
rules on both sides: only regular files, directories and symlinks that
resolve inside the target; absolute names, `..` components, hard links,
devices, FIFOs, other types, escaping symlinks and paths through them are
refused; setuid, setgid and sticky bits are cleared. Extraction writes through
an `os.Root`, so no write follows a symlink out. A refused entry ends a sync
with `ErrUnsafeArchive`, and entries already extracted stay. A transfer has no
ordering guarantee with tool calls running at the same time.

**LocalDir transfer.** `sandbox.LocalDir` moves a host directory over the
rsync protocol, `github.com/gokrazy/rsync` behind `sandbox/internal/rsyncx`,
the only package that imports it. That library can neither apply exclude
rules safely nor keep excluded paths from deletion
(`docs/errata/14_sandbox_rsync_sync.md`), so it only ever moves the files of
a tree that already is the transfer set: the sending side serves a filtered,
read-only `fs.FS` view of its tree (`rsyncx.Filtered`) with an `rsyncd`
server, and the receiving side runs an `rsyncclient` receiver into its
directory. On Sync the host serves and the agent receives into the workspace;
on Export the agent serves and the host receives into the destination. The
library never sees a rule or `--delete`; deletion is AgentKit's own
(`rsyncx.DeleteAbsent`, through an `os.Root`, never following a symlink).

One `Rsync` stream carries one transfer, its frames in this order:

```
host  -> agent  start  direction (SYNC or EXPORT), path (empty for SYNC),
                       include_ignored, exclude_git, delete
agent -> host   plan   the agent's rules over its tree; file: the export path
                       is a regular file
host  -> agent  args   the agent's server-side argument list (--sender exactly
                       for EXPORT, --delete as the declared intent), the
                       combined rules and, for a SYNC with delete, the host's
                       transfer set
both            data   rsync protocol bytes, at most 1 MiB a frame
agent -> host   done   files, bytes, deleted for SYNC; the transfer set for an
                       EXPORT with delete
```

A frame out of this order, or a stream that ends with status OK and no done
frame, is a malformed response (`malformed response: rsync: <reason>`) and
makes the sandbox unavailable. The agent refuses a bad start or args frame
with `InvalidArgument` (a missing export path with `NotFound`) before it
builds any server, runs one stream at a time (a second is
`FailedPrecondition`; one that arrives while a cancelled stream is still
unwinding waits for it, up to 5 seconds), and ends a library failure with `FailedPrecondition`
carrying the library's message; all of these keep the sandbox live. A failure
of the host's own side of the library cancels the stream and returns
`sandbox: rsync client: <the library's error>`, the sandbox live. An agent
below protocol 1.1 and a Windows host are refused before any walk or stream.

*The transfer set.* Each side computes rules over its own tree: the entries
`workspace.Walk` (hidden files included) does not visit, one rule per ignored
directory at its highest level and none under `.git` (the host walks with
`LocalDir.Ignore`, the agent with `workspace.NoGlobalExcludes()`; an export of a
subdirectory honours the workspace's ignore files above it); every symlink
`tarsafe.SymlinkEscapes` rejects, relative to the transfer root; every entry
it cannot read; and `.git`, which excludes a `.git` component at any depth, on
a Sync with `ExcludeGit` and on every Export. `IncludeIgnored`, or an export
of a single regular file, drops the ignore rules only. A rule is a literal
path (wildcard characters are plain characters) and excludes its subtree. The
host combines both sides' rules into their sorted union; a path either side
excludes is not sent, not overwritten, not written through and not deleted on
the receiving side. The combined rules, and a sender's transfer set, are
capped at 8 MiB: the agent refuses its own oversized rules with
`FailedPrecondition` instead of sending plan, and the host refuses oversized
combined rules before args.

*Trust.* Workspace content is untrusted; the agent binary is trusted. On
Export the host's safety rests on two things: the agent never sends an
escaping symlink or `.git` (the filtered view judges every symlink as it is
served, so a link planted after the plan is hidden too), and the host's own
rules over the destination
keep its ignored files, its `.git` and its escaping symlinks from being
overwritten, written through or deleted. The agent serves each stream with
exactly one module over the workspace root, writable only for SYNC
(`rsyncx.ModulesFor`), so an EXPORT stream cannot change the workspace.

**Kernel confinement inside the agent.** The containment of a sandbox is
the provider's; on Linux the agent adds a second boundary underneath it, a
Landlock ruleset on its own process that every process a tool starts
inherits and none can lift. `agentkit-sandboxd` is the only code that
applies it, through `sandbox/internal/confine`, which nothing else imports:

```
main      parse flags; --version answers without probing
confine   validate the flag roots (a refused value: exit 2)
          probe the kernel's ABI once; Plan picks the ABI (0, 2, 3 or 4)
          and whether TCP is handled (only at 4, under --network none)
          resolve the roots; apply landlock.V2/V3/V4 strictly (Restrict
          with TCP, RestrictPaths without; never BestEffort); no_new_privs
          on every thread (a failure after a good probe: exit 1)
main      redirect stdout, agent.New(Config{Landlock: outcome}), serve
Hello     the outcome as LandlockReport; attach fills AgentInfo.Landlock
attach    RequireLandlock: refuse unless Landlock.Satisfies(network mode),
          closing the client and destroying the sandbox once
```

The ruleset grants every right beneath the workspace, `HOME`, the
temporary directory and each `--landlock-rw`; read and execute beneath the
system directories and each `--landlock-ro`; read beneath `/proc`; and read
and write on five device nodes (`/dev/null` above all: every shell tool's
stdin is the null device). Under `--network none` at ABI 4 it denies every
TCP bind and connect, **loopback included**: Landlock rules are by port and
cannot tell local from remote. `docs/configuration.md` has the full table.
Landlock does not reach resources (memory, CPU, processes stay the
container's job), UDP or Unix sockets, Multipath TCP (which Go's
`net.Listen` uses by default, so a test that wants a denial disables it), or
host allowlists (`NetworkAllowlist` gets no TCP rule from the agent).

*The agent's Landlock ruleset is applied before any other work.* "Before"
cannot be observed from outside, so an AST test over the command pins it:
`confine.Setup` precedes every `redirectStdout`, `agent.New` and `go`
statement. The agent package never applies Landlock; it receives the outcome
as data, because `local` runs it inside the embedder's process.

*No code in this module confines the process that embeds it.* Both library
constructors, `rsyncd.NewServer` and `rsyncclient.New`, are called in exactly
one `rsyncx` function each, always with `DontRestrict()`: without it the
library applies Landlock to the whole process, which under `local` is the
embedder. Nothing in the sandbox module calls `chroot` or changes the
working directory or the user id. The library's receiver reads the umask
once per process by setting it to 0 and straight back
(`internal/receiver/permissionsumask.go`); the umask is unchanged after it,
but a file another goroutine creates in that instant gets mode bits from a
zero umask; `internal/policy` checks the
constructors, and a re-executed test child checks the process stays
unconfined after a Sync and an Export.

**D3: the script runs on the host.** PRD 11's decision D3 (a) is what spec 13
implements: a code-mode script over proxy tools runs in the host's Starlark
interpreter, and each call it makes goes through the host's nested-call
pipeline and `Toolset.Guard` before it becomes a `Call` RPC. The guard
therefore sees every call a script makes, inside a sandbox or not. Running the
interpreter in the sandbox with authorization called back to the host
(D3 (c)) is deferred to the second release; only the self-contained `Call`
messages are designed for it.

**Sandbox image.** One Containerfile, `containers/agentkit-sandbox/Containerfile`,
builds the published image from the repository root, in two stages:

```
build   ubi10/go-toolset, GOTOOLCHAIN=auto; CGO_ENABLED=1 (tree-sitter, psx's cgo variant)
base    ubi10/s2i-core + git-core, bash, ca-certificates + build's agent at AgentPath
```

The agent is built in the nested `sandbox` module with `-trimpath`,
`-buildvcs=false` and `-ldflags -X …/sandbox/internal/buildver.stamp=$VERSION`:
`buildver.Version()` returns the stamp when it is set, so a published agent
reports its release version, and an unstamped build `(devel)`. The agent is
`/usr/local/bin/agentkit-sandboxd`, owned by root with mode 0755, and the
tools run as uid 1001, which cannot replace the binary that confines it (14's
trust model). The layout lines up with the agent's Landlock roots:
`/workspace` (the workspace), `/home/agentkit` (`HOME`) and `/tmp` (the
temporary directory) are writable, and `git`, `bash` and the CA bundle sit
under the read-only system directories. The image declares no `VOLUME`, which
would make anonymous volumes no provider reaps, and only `HOME` as `ENV`.
Providers pass `--entrypoint sandbox.AgentPath` (an embedder's image built
`FROM` the base may set its own entrypoint) and `--init` for zombie reaping.
[`releasing.md`](releasing.md) describes publishing it.

**The Podman and Docker provider.** `sandbox/podman` runs each sandbox's
agent as the only process of a container that holds one named volume, with
no host mount, a read-only root, no capabilities, `no-new-privileges`, an
explicit environment (on docker the image's own `ENV` is added, since docker
cannot drop it) and, by default, no network. Its argument lists are
pure functions (`podman.Args`, `podman.InitArgs`), golden-tested offline, and
every engine command goes through an unexported runner seam that the
offline tests replace with a fake serving an in-process agent.

```
Create   CheckSpec → provider checks → volume create agentkit-<id> (labels)
         → <engine> run --rm -i --init … --entrypoint AgentPath <image> AgentArgs
           (own process group; stdin pipe, private stdout pipe, 8 KiB stderr tail)
         → attach.Dial over stdioconn → Hello → host TTL timer
Close    attach teardown → destroy: wait ≤5 s / kill group → rm -f both names
         → volume rm (retried ≤10 s while in use)
```

A `GitRepo` sync runs a second, short-lived container, `agentkit-<id>-init`,
with `--entrypoint git` on the same volume and the engine's default network,
while the agent's container sits idle beside it under its own policy: the
clone's network never reaches the agent. It is allowed only before anything
else has written to the workspace and before the tool set exists. The TTL is
enforced on both sides: attach's host timer runs the destroy step, and the
agent's `--ttl` ends the container, which `--rm` removes, even when the host
is gone. The host timer counts from the creation time the volume's
`agentkit.sandbox.expires` label is written from, not from the end of
`Create`, so the sandbox never outlives its label. The volume of a host that
died keeps that label, and `Reap` removes containers and volumes only once
the expiry has passed, so it never touches a sandbox that a live process on
another host still uses, as long as the hosts' clocks agree. A `GitRepo`
sync that finds the sandbox closed or expired when its clone ends removes
the volume again, since the init container's mount may have re-created it.
When the handshake fails with a non-zero exit other than the engine's own
125, a `--version` run of the image without
`--init` (whose init process would decide the exit status) tells an image
with no agent (126 or 127, reported as `sandbox.ErrNoAgent` with the
`COPY --from` line that fixes it) from an agent that failed to start.

**The Landlock provider.** `sandbox/landlock` runs `agentkit-sandboxd` as a
child process of the embedder, and the agent's own Landlock ruleset is the
only boundary. `New` probes the kernel once with the raw version query
(syscall 444), not with `confine` or go-landlock, which only the agent may
import, and checks the agent binary's `--version`.

```
Create   CheckSpec → refuse Provider keys, HOME/TMPDIR → <Root>/<id>/ + creator marker
         → workspace/ home/ tmp/ → agentkit-sandboxd in a new session (Setsid),
           --workdir <workspace>, ExtraRO/resolver/ExtraRW roots, Spec.Env+HOME+TMPDIR+PATH
         → marker names the agent → attach.Dial → report: ABI ≥ 4, TCP, roots, Root contained
Close    attach teardown → wait ≤5 s / SIGKILL group → SIGKILL the session (≤5 s)
         → remove <Root>/<id>/ (no symlink followed, rwx restored)
```

The agent leads its own session because `tools.Run` gives every command its
own process group: killing the agent's group would miss a background job a
tool left behind, but the job stays in the session, and the destroy step
kills the session by scanning `/proc`. A process that calls `setsid` escapes
that. A `GitRepo` sync starts a second agent with TCP allowed, beside the
idle sandbox agent, and runs `git` through its `run_command` tool, so the
clone is confined too and its network never reaches the sandbox's agent.
The TTL is enforced on both sides: attach's host timer runs the destroy step,
and the agent's `--ttl` ends it when the host is gone; a crashed host leaves
a directory whose marker names the agent by pid, start time and boot id, and
`Reap` removes it once that owner is gone, killing what outlived it, but
never a sandbox whose agent is alive. What the provider gives up: Unix
sockets, including a D-Bus session bus, an ssh-agent and engine sockets;
UDP; Multipath TCP; signals to the user's processes; host paths in tool
results; and file descriptors the embedder opened without close-on-exec.
Landlock at the agent's ABI cap of 4 cannot express those, so the provider
reports `IsolationConfinement` and its `Notes` say so.

**The gateway.** A gateway lets an embedder create and use sandboxes on
another host. `sandbox/gatewayd` holds one backend provider and that
provider's credentials; the embedder holds only the gateway's URL and bearer
token (`gateway.Dial`), and the sandbox holds neither. To the embedder and the
model a gateway sandbox is attach's sandbox like any other: the same
proxies, state machine and errors.

```
embedder   gateway.Dial: URL and token rules, one gRPC connection, Capabilities
           Provider.Create: CheckSpec, MaxTTL, Provider JSON → Gateway.Create
gatewayd   authenticate from the headers, before any message is read (SHA-256
           of the token, constant time) → ceiling, TTL,
           capacity → Backend.Create → entry {owner, created, expires}
embedder   attach.New over the shared connection, agentkit-sandbox-id metadata
gatewayd   relay: route by id and owner → the backend sandbox's own agent
           connection (attach.ClientConn through Unwrap), no incoming metadata
agent      the backend's agent, unaware of the gateway
```

*Two services, one package.* `gateway.proto` is a second file of
`agentkit.sandbox.v1` with its own version (`gateway.ProtocolMajor` 1,
`ProtocolMinor` 0) and its own recorded hash; the Agent service, its file and
`sandbox.ProtocolMinor` are untouched, so agents need no rebuild. Its six
unary RPCs (`Capabilities`, `Create`, `SyncGit`, `List`, `Destroy`, `Reap`)
carry the sandbox id in their messages; Agent RPCs carry it in the
`agentkit-sandbox-id` metadata key, which `attach.Config.Metadata` adds to
every call. A refusal travels as a typed `Refusal` in the response, which the
client turns back into `*sandbox.SpecError`, `ErrNoAgent`,
`ErrNoPublishedImage`, `ErrProtocol` or `gateway.ErrCapacity`.

*The typed relay.* The gateway serves an `Agent` implementation that forwards
`Hello`, `Tools`, `Call`, `SyncTar`, `ExportTar` and `Rsync` message by
message, unknown fields included, under the incoming RPC's context, so
cancellation and deadlines reach the agent. It reads what it must: the
`Rsync` start frame's direction and the outcome of each slot-taking RPC. An
Agent RPC the gateway's build does not know answers `Unimplemented` until the
gateway is upgraded.

*Metadata hygiene.* The relay calls the agent with an empty outgoing
metadata set: the agent never sees `authorization` or `agentkit-sandbox-id`.
The server keeps only each token's SHA-256 as its owner identity and logs only
the first 8 hex characters; no log line carries a token, an `Env` value, a
tool input or a tool result.

*Ownership.* A sandbox belongs to the token that created it. The relay,
`SyncGit`, `Destroy` and `List` key every lookup by owner and id, so another
token's id behaves exactly like one that does not exist: `List` omits it,
`Destroy` answers OK and does nothing, the relay answers `Unavailable`. No
existence oracle leaks.

*The `GitRepo` slot.* A `GitRepo` is the backend's provider-side init step,
so it is the Gateway RPC `SyncGit`, and the server keeps spec 17's slot rules
itself: relayed RPCs bypass the backend wrapper's own bookkeeping. A
successful `SyncGit`, the first relayed `Tools`, a relayed `SyncTar` that
succeeds and a relayed `SYNC` `Rsync` that ends OK take the slot; while it is
free, `SyncGit` holds a write lock and those relays a read lock, so the two
never interleave. A later `SyncGit` is refused with "GitRepo must be the
first Sync and come before Tools".

*Transport failures and the state machine.* attach decides liveness by status
code, and the client hides its shared connection from attach's watcher
(`noWatch`), so liveness is decided per RPC: a network blip with no call in
flight loses nothing. Every condition on the gateway's side (a missing,
malformed, unknown or foreign id, a backend sandbox no longer live) answers
`Unavailable`, never one of the four codes attach reads as the agent's own
refusal, so a gone sandbox becomes `sandbox_unavailable` rather than a "live"
sandbox that cannot work. Upstream, the agent's `InvalidArgument`,
`FailedPrecondition`, `NotFound` and `Unimplemented` pass through, and every
other error becomes `Unavailable`. A cut client connection, a cut backend
connection and a gateway-side destroy all end the call in flight and every
later call with `sandbox_unavailable`.

*Lifecycle.* `Create` prunes entries whose backend sandbox is no longer live,
then refuses with `CAPACITY` when recorded sandboxes plus creates in progress
reach `MaxSandboxes`. A sandbox whose caller has gone by the time the backend
returns is closed and never recorded, as is one whose backend exposes no
agent connection. Each entry expires by its own timer at
`created + TTL`; any access that finds the backend sandbox dead removes it;
`Destroy` and `Shutdown` remove the rest. Each removal closes the backend
sandbox and logs the reason (`client`, `expired`, `unavailable`,
`shutdown`). `Serve` also runs the backend's own `Reap` at start and every
`ReapInterval`, which catches what a crashed gateway left behind. On the
client, a handshake that fails after `Gateway.Create` runs attach's destroy
step, a context that ends before `Create` returns destroys the sandbox, and
`Provider.Close` destroys every sandbox it created before closing the
connection. Nothing reattaches: a crashed embedder's sandboxes live until their
TTL unless it lists and destroys them.

*No socket in the library.* The client package is in the sandbox module,
which listens on nothing; only the nested modules call `net.Listen`: gatewayd,
in the command, and mcpserve, in `ServeLoopback`. `internal/policy` fails when
a non-test file of the root or the sandbox module refers to a listening API.

**`local` is not isolation.** Its capabilities say so (`IsolationNone`, only
`NetworkUnrestricted`, nothing else honoured), and it refuses the zero
`Spec` because the zero network is `none`. It applies no Landlock ruleset
(`Capabilities.Landlock` is false, so `RequireLandlock` is refused before
anything is made) and its agent reports ABI 0 with the note
`in-process agent (local provider): no confinement`. It keeps its files under
`<os.TempDir()>/agentkit-sandbox-local/<id>/` with private `home/` and
`tmp/`, and `Reap` removes directories whose creating process has gone.

## Testing layout

- Unit and property tests sit beside their package.
- `testdata/golden/` holds the Anthropic request-body golden: the exact bytes
  sent. It pins regression, not vendor truth.
- `internal/policy` holds the cross-target (cgo off) and host (cgo on) build
  gates, the direct-dependency allowlist and the exported-API golden
  (`testdata/exported-api.golden`): one section for each package in
  `apiPackages`, compared by TS-13-67, TS-14-44, TS-15-42 and TS-17-61. It is a
  change detector, regenerated and committed with any deliberate change to
  those surfaces (`docs/errata/13_sandbox_core.md`).
- `provider/faux` scripts a model offline; the root tests and the examples run
  against it.

Run the fast loop with `make check` (fmt, vet, lint, `go test -short` for every module:
the root, `codesearch`, `sandbox`, `sandbox/gatewayd`, `mcpserve`,
`examples/sandbox`, `examples/gateway` and `examples/harness`). `make check-release` runs the full
tests, including the slow `internal/policy` gates that `-short` skips: the
cross-target build and vet, the host cgo build and the nested `make` runs.
