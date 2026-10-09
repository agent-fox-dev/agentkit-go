# Architecture

How the module is laid out, what imports what, and how a run flows through it.
The code is authoritative; when this file and a package's doc comment
disagree, fix this file.

## Shape

One Go module, `github.com/agent-fox-dev/agentkit-go` (`go 1.27`), plus one
nested module with its own `go.mod`, [`codesearch/`](../codesearch), which is
not in the root build graph. cgo is allowed only in `//go:build cgo` files,
each with a pure-Go fallback; `internal/policy` builds the four supported
targets with cgo off and the host with cgo on. The root module's direct
dependencies are an allowlist ([`DEPS.md`](DEPS.md)).

AgentKit is two parts:

- **The hands**: `tools`, `outline`, `codesearch`, `codemode`, `mcp` and
  `guard`. Each is usable without the driver.
- **One driver**: the root package, `agentkit`. It holds `Config`, `New`, the
  `Agent`, the loop, the batch executor and nested calls, and nothing else.

AgentKit serves no network API. It is a library: it has no MCP server and
nothing in it listens on a socket. Its only network traffic is outbound: the
Anthropic provider's requests, and the MCP client's connections to servers an
embedder configures.

## Package graph

First-party imports, from
`go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./...` (tests excluded).

| Package | Imports (first-party) | Notable third-party |
|---|---|---|
| `schema` | none | |
| `outline` | none | `github.com/tree-sitter/go-tree-sitter` and grammar modules, in `//go:build cgo` files |
| `wire` | none | |
| `internal/diag` | none | |
| `internal/toml` | `internal/diag` | `github.com/pelletier/go-toml/v2/unstable` |
| `core` | `schema` | |
| `catalog` | `core` | |
| `guard` | `core` | |
| `provider/faux` | `core` | |
| `provider` | `core`, `schema` | |
| `provider/anthropic` | `core`, `provider`, `wire` | `github.com/anthropics/anthropic-sdk-go`, `golang.org/x/oauth2`, `github.com/aws/aws-sdk-go-v2` |
| `tools` | `core`, `outline`, `schema` | `github.com/bmatcuk/doublestar/v4` |
| `codemode` | `core`, `schema`, `tools` | `go.starlark.net` |
| `mcp` | `core`, `schema`, `wire`, `internal/diag`, `internal/toml` | `github.com/modelcontextprotocol/go-sdk` |
| `prompt` | `core`, `guard`, `tools` | |
| `.` (root, `agentkit`) | `core`, `catalog`, `guard`, `prompt`, `provider`, `provider/anthropic` | `github.com/anthropics/anthropic-sdk-go` |
| `codesearch` (nested module) | `core`, `outline`, `schema`, `tools` | `github.com/sourcegraph/zoekt` |

What follows from it:

- Every package that handles tools, messages or models imports `core`. The
  leaves `schema`, `outline`, `wire` and `internal/*` import none of it.
- `core` imports only `schema`. It is the vocabulary and owns the interface
  seams: `Tool`, `ProviderClient`, `NestedCaller`, the interceptor types.
- `tools` imports `outline` and `schema`. `codemode` imports `core` and
  `schema`, and `tools` for its output buffer (`tools.Accumulator`). `mcp`
  imports `core` and `schema`, and `wire` for its strict boundary.
- The root does not import `tools` directly. It reaches it only through
  `prompt`, which takes the file-navigation tool names and the shell
  guideline texts from `tools`. The root imports the
  SDK because `Config.Client` is an SDK client; `New` builds the provider
  over it.
- Nothing imports the root package except `examples/`.

## Packages

| Package | Owns |
|---|---|
| `.` | `Config`, `New` and the `Agent` (`agent.go`; its surface is `Run`, `Stream`, `Messages`, `Usage`, `ReachableTools`), the loop (`loop.go`), the tool batch executor (`batch.go`), nested tool calls (`nested.go`), the unguarded-shell check (`execguard.go`). |
| `core` | Messages, content blocks, events and their JSON form (`MarshalEvent`), `EventStream`, `Tool`, `ToolResult`, `ToolPolicy`, argument preparation, interceptors, `ProviderClient`, `Request`, `StreamEvent`, `Model`, `Usage`, stop reasons, errors. |
| `schema` | JSON Schema values and typed combinators; `Parse` decodes a schema document keeping property order; `Validate` and `Coerce` check and repair tool arguments. |
| `catalog` | The embedded Claude model catalog (`catalog.json`) and `Lookup`: context window, output cap, prices, thinking kind, with a usable default for an unlisted id. |
| `provider` | What a wire needs and does not own: cost arithmetic (`ComputeCost`), the per-session tool-schema cache (`ToolPrefix`), deferred-tool splitting. |
| `provider/anthropic` | The Anthropic Messages wire over the official SDK, direct, on Vertex AI or on Bedrock, chosen by `Resolve`; send-time transcript repair (`RepairTranscript`) and partial-JSON salvage of streamed tool arguments (`SalvageJSON`). |
| `provider/faux` | A scripted provider for offline tests and demos. |
| `outline` | Declarations of a source file with line ranges. Go uses `go/ast` in every build. With cgo, Python, JavaScript, TypeScript/TSX, Java, Kotlin, C#, Scala, Rust, C, C++, PHP, Ruby, Lua and shell are parsed by tree-sitter grammars; without cgo they are `none`. |
| `tools` | The built-in tools, workspace containment, the output accumulator, process control and the runner (`Run`, `RunArgv`), glob, the layered ignore engine, `Walk`, the symbol table behind `find_symbol` and the reference engine behind `find_references`. |
| `guard` | The shell boundary: `Check` (a pure decision on an argv), `Restricted` (the interceptor over it), `AllowAll`. |
| `codemode` | The code-mode tool (below). |
| `prompt` | `Build(system, tools)`: the base prompt, then the active tools' guidelines, deduplicated in first-seen order. |
| `mcp` | The MCP client over the official MCP Go SDK (below). There is no server. |
| `wire` | A bounded, strict parser for untrusted bytes on `encoding/json/jsontext`, and frame readers. |
| `internal/toml` | A lenient TOML reader for `mcp.ParseConfig`, over `go-toml/v2/unstable`. |
| `internal/diag` | The shared non-fatal `Diagnostic`. |
| `internal/policy` | Tests only: the build gates and the dependency allowlist. |
| `internal/testkit` | Shared test helpers. |

## Data flow of one run

```
agentkit.New   checks every tool (exactly one of Handler/Execute, no reach
               cycle, no wrapper reaching a Terminating tool), resolves
               Config.Policy, refuses a reachable shell tool with no Guard,
               looks up the model in the catalog, builds the provider
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
         otherwise batch.go: prepare (sequential: arguments, Guard)
           -> abort decision (once) -> execute (parallel unless a
           Sequential tool is in the batch) -> finalize (After, events,
           one ToolResultMessage per call); a tool with ReachableTools
           runs with a NestedCaller on its context (nested.go)
    5. afterTurn: stop on a terminate vote (RunStopToolTerminate), a done
       context (RunStopTimeout or RunStopAborted), no tool calls
       (RunStopEndTurn, or RunStopRefusal after a refusal), or
       Config.MaxTurns (RunStopMaxTurns)
  endRun: RunResult, AgentDoneEvent; the slot is freed before the stream ends
```

Observation is the event stream. `Stream` returns it; `Run` waits for its
result without reading the events. Errors the run survives (a panicking tool,
interceptor or provider) are `core.ErrorEvent`s on it. The only interception
is `Config.Guard` and `Config.After`.

## Invariants worth knowing before editing

- **The loop iterates on the presence of `tool_use` blocks**, never on the
  stop reason. Only `error` and `aborted` short-circuit.
- **One `ToolResultMessage` per call.** Coalescing into the wire's shape is
  the provider's job.
- **The abort decision for a tool batch is made once**, after prepare and
  before any handler starts. A cancelled batch runs all of its prepared
  handlers or none.
- **Every call gets a result.** Unknown tool, invalid arguments, a block, a
  panic, an abort: each is a result with `IsError` set.
- **The batch finalize mutex is batch-scoped**, never the agent mutex, and no
  user code runs while the agent mutex is held.
- **Panics in third-party code are contained** (tool handlers, interceptors,
  argument preparation, the provider) and reported as `core.ErrorEvent`s. A
  panicking `Guard` blocks the call.
- **A shell tool with no `Guard`** is refused by `New` with
  `core.ErrUnguardedExecute`; `guard.AllowAll` is the explicit opt-out. A
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
  `core.ErrBusy`; `Messages`, `Usage` and `ReachableTools` are safe during a
  run.

## Nested tool calls

A tool can call other tools on the model's behalf, for example a script runner
that turns tools into functions. It declares them up front in
`core.Tool.ReachableTools`, and nothing else is reachable.

- **Registration.** `New` refuses a hierarchy in which a tool reaches itself:
  `agentkit: reachable tools cycle detected: toolA -> toolB -> toolA`. It
  also refuses one in which a wrapper reaches a `Terminating` tool:
  `agentkit: terminating tool "finish" cannot be reached through wrapper "code_mode"`.
  Cycles are found by name along the path, because tools are values.
- **Policy.** `ToolPolicy.Resolve` applies `NoTools`, `ToolNames` and
  `ExcludeTools` to reachable tools at every depth. A wrapper left reaching
  nothing is dropped, so an allowlist must name the children as well as the
  wrapper. `ToolPolicy.Tools`, when non-nil, is used verbatim.
- **Queries.** `core.ReachableTools(tools)` is the deduplicated transitive
  closure in depth-first order. `Agent.ReachableTools()` is that closure over
  the resolved set, which is what a check like "this run is read-only" must
  inspect.
- **Dispatch.** `executeBatch` attaches a `nestedCaller` (`nested.go`) to the
  context of every tool that declares `ReachableTools`. It is bound to that
  call's id and name and to the wrapper's reachable tools. The tool calls
  `core.CallNested(ctx, calls...)`; with no caller attached, it gets
  `core.ErrNoNestedCaller`.
- **Pipeline.** A nested call goes through the same steps as a direct one:
  1. prepare and validate the arguments;
  2. `Config.Guard`, whose context carries `ParentToolUseID` and
     `ParentToolName`;
  3. the handler;
  4. `Config.After`;
  5. `ToolExecutionStart`/`End` events with `ParentToolUseID`.

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

Calling code mode outside an agent run fails the script's first tool call
with `ErrNoNestedCaller`: code mode never runs a tool around the agent's
pipeline.

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

### `Workspace.References`

Embedders can search references without a model through
`(*tools.Workspace).References`:

```go
func (w *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)

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

- Unit and property tests sit beside their package.
- `testdata/golden/` holds the Anthropic request-body golden: the exact bytes
  sent. It pins regression, not vendor truth.
- `internal/policy` holds the cross-target (cgo off) and host (cgo on) build
  gates and the direct-dependency allowlist.
- `provider/faux` scripts a model offline; the root tests and the examples run
  against it.

Run everything with `make check` (fmt, vet, lint, test for the root and
`codesearch` modules).
