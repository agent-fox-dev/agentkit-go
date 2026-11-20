# Architecture

How AgentKit is built inside: how the modules are laid out, what imports what,
how a tool call and an agent run flow through the code, and the invariants to
keep when changing it. It is written for contributors and for embedders who
need to know exactly what happens; to *use* AgentKit, the
[user guide](guide.md) is the better start.

The code is authoritative; when this file and a package's doc comment
disagree, fix this file.

## Contents

- [Shape](#shape)
- [Package graph](#package-graph)
- [Packages](#packages)
- [The embedder's path](#the-embedders-path)
- [Data flow of one call](#data-flow-of-one-call)
- [Data flow of one run](#data-flow-of-one-run)
- [Invariants worth knowing before editing](#invariants-worth-knowing-before-editing)
- [Nested tool calls](#nested-tool-calls)
- [Code mode](#code-mode)
- [MCP serving](#mcp-serving)
- [The `agentkit-tools` command](#the-agentkit-tools-command)
- [MCP client](#mcp-client)
- [Symbol and reference navigation](#symbol-and-reference-navigation)
  - [`tools.References`](#toolsreferences)
- [Testing layout](#testing-layout)

## Shape

One Go module, `github.com/agent-fox-dev/agentkit-go` (`go 1.27`), plus
five nested modules with their own `go.mod`, none of them in the root build
graph:

- [`codesearch/`](../codesearch), the zoekt-backed index;
- [`mcpserve/`](../mcpserve) (`…/mcpserve`), the MCP server for a `Runner`'s
  tools, on stdio or on loopback HTTP behind a bearer token, for a harness on
  the same machine. It requires the root module through `replace ../`,
  directly requires nothing else but the MCP Go SDK at the root's version, and
  its graph holds no module the root's lacks;
- [`cmd/agentkit-tools/`](../cmd/agentkit-tools) (`…/cmd/agentkit-tools`),
  the `agentkit-tools` command (below). It requires the root, codesearch and
  mcpserve modules through `replace` and no third-party module directly;
- [`examples/indexedsearch/`](../examples/indexedsearch), the example program
  that walks through the code-search index on a sample project. It requires the
  root and codesearch modules through `replace`;
- [`examples/toolserver/`](../examples/toolserver), the example program that
  builds `agentkit-tools`, a tool of its module, and drives it in both modes.

cgo is allowed only in `//go:build cgo` files, each with a pure-Go fallback;
`internal/policy` builds every module in the Makefile's `MODULES` for the four
supported targets with cgo off and for the host with cgo on. Direct
dependencies are an allowlist ([`DEPS.md`](DEPS.md)), kept by review.

AgentKit is two parts:

- **The hands**: `workspace`, `tools`, `outline`, `codesearch`, `codemode`,
  `mcp` and `guard`. They work in the directory they are given; isolating
  them is the job of the sandbox the platform runs them in
  ([ADR 08](adr/08-leave-sandboxes-to-the-platform-that-runs-them.md)).
  Each is usable without the driver.
- **One driver**: the root package, `agentkit`. It holds `Config`, `New`, the
  `Agent` and the loop, and the tool side the loop runs on: `Toolset`,
  `NewRunner` and `Runner`, with the batch engine and the nested caller behind
  it. Nothing else. The driver is the loop over a runner; the runner needs no
  driver.

The root module listens on no socket and serves no MCP. It is a library: one
nested module listens, `mcpserve`, which serves a runner's tools over MCP, on
stdio or a loopback port, to a harness on the same machine. No non-test file
of the root module listens on a socket. The library's only network traffic is
outbound: the providers' requests and the MCP client's connections to servers
an embedder configures.

## Package graph

First-party imports, from
`go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./...` (tests excluded),
run in the root module and in `codesearch`, `mcpserve` and `cmd/agentkit-tools`.
The nested example modules `examples/indexedsearch` and `examples/toolserver`
are programs and not listed. A package marked "tests
only" exists for tests: it holds only test files, or only test files import it.

| Package | Imports (first-party) | Notable third-party |
|---|---|---|
| `schema` | none | |
| `outline` | none | `github.com/tree-sitter/go-tree-sitter` and grammar modules, in `//go:build cgo` files |
| `wire` | none | |
| `internal/diag` | none | |
| `internal/toml` | `internal/diag` | `github.com/pelletier/go-toml/v2/unstable` |
| `internal/policy` (tests only) | none | |
| `internal/testutil` (tests only) | none | |
| `internal/testkit` (tests only) | `core`, `schema` | |
| `internal/toolconformance` (tests only) | `core`, `schema` | |
| `internal/providerconformance` (tests only) | `.` (root, `agentkit`), `catalog`, `core`, `provider/{anthropic,ollama}`, `tools`, `workspace` | |
| `core` | `schema` | |
| `catalog` | `core` | |
| `guard` | `core` | |
| `provider/faux` | `core` | |
| `provider` | `core`, `schema` | |
| `provider/anthropic` | `core`, `provider`, `schema`, `wire` | `github.com/anthropics/anthropic-sdk-go`, `golang.org/x/oauth2`, `github.com/aws/aws-sdk-go-v2` |
| `provider/openrouter` | `core`, `provider` | `github.com/OpenRouterTeam/go-sdk`, and the `ajson` module it requires |
| `provider/ollama` | `catalog`, `core`, `provider` | `github.com/ollama/ollama/api` |
| `workspace` | none | `github.com/bmatcuk/doublestar/v4` |
| `tools` | `core`, `outline`, `schema`, `workspace` | `github.com/bmatcuk/doublestar/v4` |
| `codemode` | `core`, `schema`, `tools` | `go.starlark.net` |
| `mcp` | `core`, `schema`, `wire`, `internal/diag`, `internal/toml` | `github.com/modelcontextprotocol/go-sdk` |
| `mcpserve` (nested module) | `.` (root, `agentkit`), `core`, `mcp`, `wire` | `github.com/modelcontextprotocol/go-sdk` |
| `cmd/agentkit-tools` (nested module, a program) | `.` (root, `agentkit`), `codemode`, `codesearch`, `core`, `guard`, `mcpserve`, `tools`, `wire`, `workspace` | |
| `prompt` | `core`, `guard`, `tools` | |
| `.` (root, `agentkit`) | `core`, `catalog`, `guard`, `prompt`, `provider`, `provider/anthropic`, `provider/openrouter`, `provider/ollama` | |
| `examples/{agentdemo,codemode,codingagent,customtools,mcp}` (programs) | `.`, `codemode`, `core`, `guard`, `mcp`, `provider/anthropic`, `provider/faux`, `schema`, `tools`, `wire`, `workspace`, by program | |
| `codesearch` (nested module) | `core`, `outline`, `schema`, `tools`, `workspace` | `github.com/sourcegraph/zoekt` |

What follows from it:

- Every package that handles tools, messages or models imports `core`. The
  leaves `schema`, `outline`, `wire`, `workspace` and `internal/diag` import
  nothing of the module.
- `core` imports only `schema`. It is the vocabulary and owns the interface
  seams: `Tool`, `ProviderClient`, `NestedCaller`, the interceptor types.
- `workspace` imports nothing of the module: only the standard library and
  doublestar, so every package can depend on it.
- `tools` imports `core`, `outline`, `schema` and `workspace`. `codemode`
  imports `core` and `schema`, and `tools` for its output buffer
  (`tools.Accumulator`). `mcp` imports `core` and `schema`, and `wire` for its
  strict boundary. `guard` imports `core` and nothing else.
- The root imports `core`, `catalog`, `guard`, `prompt`, `provider` and the
  three providers (`provider/anthropic`, `provider/openrouter` and
  `provider/ollama`), and neither `tools` nor `workspace` in non-test code. It
  reaches `tools` only through `prompt`, which takes the file-navigation tool
  names and the shell guideline texts from `tools`. It imports no SDK itself:
  `Config.Client` is a `core.ProviderClient`, and `Resolve` (`resolve.go`) is
  the one place that names a provider package, to build the client of
  `Config.Provider`.
- The root package is imported by `mcpserve` and by programs: this module's
  `examples/` and the nested example module. No library package of the root
  module imports it, so the tools, the guard and code mode do not depend on
  the driver.
- `mcpserve` imports the root (for `agentkit.Runner`), `core`, `mcp` (for its
  pipe transport) and `wire`, and the MCP SDK. It imports neither `tools` nor
  `workspace`: it serves whatever tools a runner holds.
- `codesearch` imports `core`, `outline`, `schema`, `tools` and `workspace`,
  and not the root.

## Packages

| Package | Owns |
|---|---|
| `.` | `Config`, `New` and the `Agent` (`agent.go`; its surface is `Run`, `Stream`, `Messages`, `Usage`, `ReachableTools`, `Runner` and `Resolution`), `Resolve` and `Resolution` (`resolve.go`), the loop (`loop.go`), and the tool side the loop is built on: `Toolset`, `NewRunner` and `Runner` (`runner.go`), the runner's batch engine (`batch.go`), its nested caller (`nested.go`) and the unguarded-shell check (`execguard.go`). |
| `core` | Messages, content blocks, events and their JSON form (`MarshalEvent`), `EventStream`, `Tool`, `ToolResult`, `ToolPolicy`, argument preparation, interceptors, `ProviderClient`, `Request`, `StreamEvent`, `Model`, `Usage`, stop reasons, errors. |
| `schema` | JSON Schema values and typed combinators; `Parse` decodes a schema document keeping property order; `Validate` and `Coerce` check and repair tool arguments. |
| `catalog` | The embedded model catalog (`catalog.json`) of the three providers, `anthropic`, `openrouter` and `ollama`: context window, output cap, prices and thinking kind per row, with a per-provider default for an unlisted id, and a fixed tier table (SIMPLE, STANDARD, ADVANCED) per vendor that `Parse` checks against its rows. `Lookup` resolves a Claude id, the way `New` does; `Select` resolves a (provider, model, effort) triple for any of the three, offline. It imports `core` and the standard library only. |
| `provider` | What a wire needs and does not own: cost arithmetic (`ComputeCost`), the per-session tool-schema cache (`ToolPrefix`), deferred-tool splitting, and the tool-pairing repair that the chat-style wires share (`RepairToolPairing`: it never touches thinking, which each wire replays on its own terms). |
| `provider/anthropic` | The Anthropic Messages wire over the official SDK, direct, on Vertex AI or on Bedrock, chosen by `Resolve`; send-time transcript repair (`RepairTranscript`) and partial-JSON salvage of streamed tool arguments (`SalvageJSON`). |
| `provider/openrouter` | The OpenRouter wire: OpenRouter's native Chat Completions through its own Go SDK. `New` resolves the key and base URL, builds one SDK client and makes no request. AgentKit writes the request body itself and the SDK sends it, through an `HTTPClient` that also records each response's status, `Retry-After` and error body, because the SDK's typed errors carry none of them; AgentKit runs its own retry loop, replays reasoning as `reasoning_details` to the model that produced it, nets the usage, prices it from the response's `cost` or the model's row, and keeps the API key out of every error and warning. |
| `provider/ollama` | The Ollama wire: Ollama's native `/api/chat` through its own Go package. `New` resolves `OLLAMA_HOST` by the ollama CLI's rules (refusing a scheme other than http or https, a bad port, user information and the hosted service), builds one SDK client with `api.NewClient` and makes no request. Every request goes through a layer that deletes `Authorization` and records the response's status, because `Chat` returns no error for an error status with an empty body. It sends `num_ctx` and `truncate: false` on every request, cuts a tool schema to what the SDK's tool type carries, replays thinking to the model that produced it and a call's arguments with their keys in order and their values as written, nets the usage and prices it at zero, and names its failures: `ErrModelNotFound`, `ErrUnreachable`, `ErrContextWindow` and `ErrStreamTruncated`. |
| `provider/faux` | A scripted provider for offline tests and demos. |
| `outline` | Declarations of a source file with line ranges. Go uses `go/ast` in every build. With cgo, Python, JavaScript, TypeScript/TSX, Java, Kotlin, C#, Scala, Rust, C, C++, PHP, Ruby, Lua and shell are parsed by tree-sitter grammars; without cgo they are `none`. `IsTestFile` is the test-file convention the symbol and reference ranking in `tools` uses. |
| `workspace` | The workspace: the containment boundary (`Open`, `Resolve`, `Rel`, `CheckWriteTarget`), the layered ignore engine (`IgnoreOptions`, `NoGlobalExcludes`, `Ignore`, `Ignored`) and the directory walk (`Walk`), opened once and handed to the tools and the index. It imports only the standard library and doublestar. |
| `tools` | The built-in tools (`For`), the output accumulator, process control and the runner (`Run`, `RunArgv`), glob, the symbol table behind `find_symbol` and the reference engine behind `find_references` (`References`). |
| `guard` | The shell boundary: `Check` (a pure decision on an argv), `Commands` (the simple commands a shell line runs, read once for every policy), `Restricted` (the interceptor over both), `Allow` (`Restricted` with only the allowlist), `AllowAll`. |
| `codemode` | The code-mode tool (below). |
| `prompt` | `Build(system, tools)`: the base prompt, then the active tools' guidelines, deduplicated in first-seen order. |
| `mcp` | The MCP client over the official MCP Go SDK (below). The MCP server is the nested `mcpserve` module's. |
| `wire` | A bounded, strict parser for untrusted bytes on `encoding/json/jsontext`, and frame readers. |
| `internal/toml` | A lenient TOML reader for `mcp.ParseConfig`, over `go-toml/v2/unstable`. |
| `internal/diag` | The shared non-fatal `Diagnostic`. |
| `internal/policy` | Tests only: the cross-target and host build gates. |
| `internal/testutil` | The shared test helpers that need nothing of the module (file trees, a hermetic git environment, polling, JSON, golden files, `go run`). |
| `internal/testkit` | Test doubles over `core`: the scripted provider and tools. |
| `internal/toolconformance` | Tests only: the conformance case table of the built-in tools, run against `tools.For`. |
| `internal/providerconformance` | Tests only: the opt-in live tests of the three providers (below). Its only non-test file is `doc.go`. |
| `mcpserve` | The MCP server (nested module): one MCP tool per tool of an `agentkit.Runner`, every call through `Runner.Call`, served on stdio (`ServeStdio`) or on loopback HTTP behind a bearer token (`ServeLoopback`). |
| `cmd/agentkit-tools` | The `agentkit-tools` command (nested module): the tool set of one directory, served as an MCP session (`mcp`), as one call from a request file (`call`) or as a listing (`tools`). |

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
                    server's tools
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
agentkit.New   calls Resolve(cfg): catalog.Select(Provider, Model, Effort),
               then, with no Client, the chosen provider's constructor, which
               reads that provider's credential and endpoint through Env and
               Getenv and fails here when one is missing; then it calls
               NewRunner(cfg.Toolset): it checks every tool
               (exactly one of Handler/Execute, no reach cycle, no wrapper
               reaching a Terminating tool), resolves Toolset.Policy and
               refuses a reachable shell tool with no Toolset.Guard, so New
               refuses what NewRunner refuses. The errors come in the order
               provider, model, effort, credentials, tool set. The agent keeps
               the Resolution: its model row, its client, its effort and the
               API of the provider package that built the client
Agent.Run      = Stream, then wait for the RunResult
Agent.Stream   claims the run slot (core.ErrBusy if taken), applies
               Config.Timeout, starts runLoop on a goroutine
  runLoop (loop.go), per turn:
    1. beforeRequest: stop with RunStopBudgetExceeded if the run has spent
       Config.MaxCostUSD; then outboundView: prune old tool results when the
       estimate reaches Config.Prune.Threshold of the context window, and
       stop with RunStopError if it still exceeds the window
    2. callModel: prompt.Build(Config.System, tools) and a core.Request
       (Prefix, the view, ToolWires, MaxTokens, the Resolution's Effort);
       core.EventStreamOf(ProviderClient.Stream(ctx, req)); every provider
       event is forwarded onto the run's stream
    3. the assistant message is recorded and its usage priced at the resolved
       row when the provider left the cost unset (flagged Usage.Unpriced when
       that zero is not a price) and added to the run's and the agent's usage;
       an Error or Aborted stop reason ends the run here, before tool
       extraction
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
say — builds a runner and calls it, and serves the runner through `mcpserve`
with nothing else of the driver.

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

## The `agentkit-tools` command

`cmd/agentkit-tools` is a program over the hands, with no code of its own
beyond the wiring and the command line ([ADR 09](adr/09-serve-the-tools-from-one-command.md)):

```
--dir               checked first: workspace.Open accepts a root that does not exist
--index-dir         outside the served directory, created and probed; or
                    AGENTKIT_INDEX_DIR; or none
workspace.Open      the served directory
codesearch.New      the index (none where zoekt does not run), stored under the
                    index directory, else in a temporary directory
tools.For           the built-in tools with that index; the shell tools get
                    tools.ReducedEnv of the process environment
--tools/--read-only choose from them, in serving order
codemode.New        code_mode over exactly the chosen tools
agentkit.NewRunner  with guard.AllowAll or guard.Restricted (--guard)
  tools             definitions of Runner.Tools() as JSON
  call              one Runner.Call, its result and the text a session serves
  mcp               mcpserve.New(runner).ServeStdio
```

Every command builds the same tool set through one function, so `tools`,
`call` and `mcp` serve the same tools under the same guard. `call` runs one
call per process under `--timeout`, and `mcp` and `call` turn SIGINT and
SIGTERM into exits 130 and 143. The index is closed when the command ends:
a temporary one is removed with its directory, a stored one stays for the
next process, which loads and revalidates it instead of building
([the stored index](codesearch.md#the-stored-index)). The command discards
the standard logger, through which zoekt reports its shards.

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
  without cgo. Each site is attributed to its enclosing declaration. The
  tool's `path` argument scopes both the reported sites and the declaration
  lookup itself: a `path` holding only callers, not the declaration, matches
  no candidate and falls back to a workspace-wide text search, reporting
  `[0 declarations matched]`.
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

## Testing layout

- Tests sit beside their package, organised by behaviour: one file per area of
  the code, each test named for what it checks.
- `testdata/golden/` holds the Anthropic request-body golden: the exact bytes
  sent. It pins regression, not vendor truth.
- `internal/policy` holds the cross-target (cgo off) and host (cgo on) build
  gates, which `make check-release` runs.
- `provider/faux` scripts a model offline; the root tests and the examples run
  against it. Each provider package tests its wire against a fake of its
  vendor's API on a loopback `httptest` server.
- `internal/providerconformance` holds the live tests, `TestLive`: one subtest for each
  provider, which runs one real tool loop on the `STANDARD` tier against the
  provider's live service. The agent has the built-in `read_file` tool alone and
  is asked to read a file holding a random word and to answer with it, within 6
  turns, $0.50 and 10 minutes. `TestLive` skips under `-short` and unless
  `AGENTKIT_LIVE` is `1`, so `make check`, `make check-release` and a plain
  `go test` make no live request; `make provider-live-test` runs it. A subtest
  skips, naming what is missing, unless its provider is ready: credentials that
  `anthropic.Resolve` accepts, `OPENROUTER_API_KEY`, or an Ollama server at its
  host that has the tier's model.

How to run each of these, and which are opt-in, is in
[development.md](development.md#tests).
