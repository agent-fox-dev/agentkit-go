# Architecture

How the module is laid out, what depends on what, and how a run flows through
it. The code is authoritative; when this file and a package's doc comment
disagree, fix this file.

## Shape

One Go module (`github.com/agent-fox-dev/agentkit-go`, `go 1.27`). Maintained
third-party modules are used where they replace hand-rolled infrastructure
([PRD 09](prd/09-replace-hand-rolled-code-with-libraries.md)). cgo is allowed
only in `//go:build cgo` files, and every package keeps a pure-Go fallback:
`internal/policy` builds the four supported targets with cgo off and the host
with cgo on. One nested module carries its own `go.mod` and is not in the root
build graph: [`codesearch/`](../codesearch).

The root package holds the `Agent` and nothing else. Everything else is a
sub-package.

## Package graph

Imports between first-party packages, taken from the source (tests excluded;
`internal/*` omitted). An arrow means "imports".

| Package | Imports |
|---|---|
| `core` | `jsonx`, `schema` |
| `schema` | `jsonx` |
| `outline` | nothing first-party; `github.com/tree-sitter/go-tree-sitter` and grammar modules in `//go:build cgo` files |
| `jsonx`, `wire` | nothing first-party |
| `catalog`, `guard` | `core` |
| `provider` | `core`, `schema`, `wire` |
| `provider/anthropic` | `core`, `provider`, `wire`; plus `github.com/anthropics/anthropic-sdk-go`, `golang.org/x/oauth2` (Vertex) and `github.com/aws/aws-sdk-go-v2` (Bedrock) |
| `provider/faux` | `core` |
| `tools` | `core`, `outline`, `schema`; plus `github.com/bmatcuk/doublestar/v4` |
| `codesearch` (nested module) | `tools`, `core`, `schema`, `outline`; plus `github.com/sourcegraph/zoekt` (confined to this module) |
| `mcp` | `core`, `schema`, `wire`; plus `github.com/modelcontextprotocol/go-sdk` |
| `prompt` | `core`, `guard`, `tools` |
| `codemode` | `core`, `schema`, `tools`; plus `go.starlark.net` (confined to this package; see [`DEPS.md`](DEPS.md)) |
| `.` (root, `agentkit`) | `core`, `guard`, `prompt` |

Rules that follow from it:

- `core` is the vocabulary and owns every interface seam (`Tool`,
  `ProviderClient`, `Middleware`). It imports no other part of the SDK except
  the two leaf encoders.
- Nothing imports the root package except `examples/` and tests.
- Provider packages are never imported by the root. A caller registers the wire
  APIs it wants, so a program that only wants the loop does not pull in
  `net/http`.

## Packages

| Package | Owns |
|---|---|
| `.` | The driver: `Config`, `New` and the `Agent` (`agent.go`, whose surface is `Run`, `Stream`, `Messages`, `Usage` and `ReachableTools`), the loop (`loop.go`), the tool batch executor (`batch.go`), nested tool calls (`nested.go`), the `execute` guard check (`execguard.go`). |
| `core` | Messages, content blocks, events and their discriminated JSON form (`MarshalEvent`, with a caller-supplied `MessageEncoder`), `EventStream`, `AgentConfig`, `Tool`, `ToolPolicy`, argument preparation, `Model`, `Usage`, stop reasons, errors. |
| `catalog` | Embedded Claude model catalog (`catalog.json`) and `Lookup`: context window, output cap, prices and thinking kind, with a usable default for an unlisted id. |
| `provider` | What a wire API needs and does not own: cost arithmetic, the per-session tool-schema cache (`ToolPrefix`) and deferred-tool splitting. Transport, retries and SSE framing are the Anthropic SDK's. |
| `provider/anthropic` | Anthropic Messages over the official SDK — direct, Claude on Vertex AI and on Bedrock, chosen by `Resolve` — including the send-time transcript repair (`RepairTranscript`) and partial-JSON salvage of streamed tool arguments (`SalvageJSON`). |
| `provider/faux` | Scripted provider for offline tests and demos. |
| `outline` | Source-file declaration listing with real line ranges. Go files use `go/ast` in every build. With cgo, Python, JavaScript, TypeScript/TSX, Java, Kotlin, C#, Scala, Rust, C, C++, PHP, Ruby, Lua and shell are parsed in-process by tree-sitter grammars driven by tags queries (`outline/treesitter*.go`, `//go:build cgo`); without cgo those files are `none`. Anything outside the extension table is `none` and is not read; `LangFor` also reads a `.h` header's content to tell C++ from C. `CommentAndStringSpans` gives `find_references` the comment and string ranges of a file from the same grammars. No first-party imports. |
| `tools` | Built-in tools, workspace containment, output accumulator, process control, glob (`github.com/bmatcuk/doublestar/v4` plus smart-case and bare-pattern basename matching), layered gitignore. `read_file` reads text only: a file whose leading bytes are a PNG, JPEG, GIF or WebP signature is refused with `unsupported_file`. `RunArgv` is the embedder's process runner (no shell, argv-based, with stdin, head/tail truncation, log file, reduced environment and a pinned outcome contract). `Walk` exposes the single shared directory traversal behind workspace confinement. `file_outline` returns a file's declarations with line ranges; `find_symbol` searches the workspace by declaration name, backed by a lazily built, bounded in-memory symbol table that is refreshed after `write_file`, `edit_file` and the shell tools run; `find_references` searches for callers and usages of declarations across the workspace with exact Go type resolution and outline attribution, backed by a lazily built, bounded in-memory reference cache (`referenceCache`). |
| `guard` | The `execute` authorization boundary: `Restricted`, `AllowAll`. |
| `codemode` | The code-mode tool: a sandboxed Starlark script runner over bound tools, with typed declarations generated from their schemas, errors as values, `parallel`, four limits, truncated and spilled output, and a ledger of the calls a script made. |
| `prompt` | Assembly of the system prompt (`Build(system, tools)`): the base prompt, then the active tools' guidelines, deduplicated in first-seen order. |
| `mcp` | AgentKit's MCP client over the official MCP Go SDK, which owns the protocol and version negotiation: the tool pool (qualified names, collision checks, schema conversion, `${VAR}` resolution), subprocess spawning with process-group kill and a reduced environment, respawn, result cap, call limit and sampling gate; and strict `wire` checks at the stdio and HTTP-response boundaries. There is no server. |
| `wire` | Bounded strict parser for untrusted bytes, on `encoding/json/jsontext`: a token loop adds the size, depth, container-length and node bounds to jsontext's grammar and duplicate-name rejection. Frame readers. |
| `jsonx` | Order-preserving JSON. |
| `schema` | JSON Schema value and typed combinators. |
| `internal/toml` | Lenient TOML reader for config: `github.com/pelletier/go-toml/v2/unstable` (pinned) parses; a small adapter folds it into an ordered `Table` with line numbers and "duplicate warns, last wins". |
| `internal/diag` | The shared non-fatal `Diagnostic`. |
| `internal/policy` | Tests only: the cross-target (cgo off) and host (cgo on) build gates. |
| `internal/testkit` | Shared test helpers. |
| `codesearch` | Nested module: zoekt-backed `code_search` tool, lazy index build, dirty-file overlay, `find_symbol` acceleration. Imports `tools`, `core`, `schema`, `outline` from the root and `github.com/sourcegraph/zoekt` (pinned). |

## Data flow of one run

```
agentkit.New  ── validates the tool hierarchy, resolves Config.Policy,
                 refuses an unguarded shell tool, looks up the model
Agent.Run / Stream
  └─ runLoop (loop.go)
       per turn:
         1. build Request: the assembled system prompt, the tools,
            Config.Prefix, then the transcript
         2. ProviderClient.Stream (Config.Provider, or the Anthropic
            provider over Config.Client) ── provider/anthropic repairs the
            transcript, encodes the wire body itself (exact bytes), sends
            through the SDK, and decodes the SDK's stream events into core
            events
         3. assistant message recorded in the transcript, events emitted
         4. tool_use blocks present? (never the stop reason decides)
              └─ batch.go: prepare (sequential: arguments, Guard) → execute
                 (parallel unless a Sequential tool is in the batch) →
                 finalize (After, metadata copy, one ToolResultMessage per
                 call); a tool with ReachableTools runs with a NestedCaller
                 on its context (nested.go, below)
  └─ terminal marker, AgentDoneEvent, RunResult
```

Observation is the event stream: `Stream` returns it, and `Run` drains it.
Errors the run survives (a panicking interceptor or provider) are
`core.ErrorEvent`s on it. The only interception is `Config.Guard` and
`Config.After`, the authorization boundary.

## Invariants worth knowing before editing

- **One `ToolResultMessage` per call**; coalescing into a provider's wire shape
  is the provider's job.
- **The abort decision for a tool batch is made once**, before any handler
  starts.
- **The batch finalize mutex is batch-scoped**, never the agent mutex.
- **Panics in third-party code are contained** (tool handlers, interceptors,
  argument preparation, the provider) and reported as `core.ErrorEvent`s.
- **A shell tool with no `Guard`** is refused by `New` with
  `ErrUnguardedExecute`; `guard.AllowAll` is the explicit opt-out. A shell tool
  reachable through a wrapper counts too:
  `... (wrapper "code_mode" reached shell tool "execute")`.
- **Untrusted bytes go through `wire`**: bounds before allocation, duplicate
  keys rejected. Locally authored config decodes leniently and reports
  diagnostics. Where a library decodes untrusted bytes (the MCP SDK),
  `wire.Guard` checks them first at the transport boundary.
- **`core.Tool.OutputSchema` never reaches a provider.** It documents the shape
  of `ToolResult.Data` for programmatic callers; `core.ToolWire` has no such
  field, so request bodies are the same with or without it. It is not checked
  at runtime. `schema.Validate` does not evaluate a root combinator, so to
  check Data against a schema with a root `oneOf`, validate it as
  a property's value.
- **No global state, no `init()` registration.** The provider lives on the
  config.
- **The transcript is append-only.** `Agent.Messages()` returns a copy of it.
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
- **Policy.** `ToolPolicy.Resolve` applies `NoTools` (`builtin`), `ToolNames`
  and `ExcludeTools` to reachable tools at every depth.
  - A wrapper left reaching nothing is dropped, so an allowlist must name the
    children as well as the wrapper.
  - `ToolPolicy.Tools`, when set, is used verbatim, as before.
- **Queries.** `core.ReachableTools(tools)` is the deduplicated transitive
  closure in depth-first order. `Agent.ReachableTools()` is that closure over
  the resolved set, which is what a check like "this run is read-only" must
  inspect.
- **Dispatch.** `executeBatch` attaches a `nestedCaller` (`nested.go`) to the
  context of every tool that declares `ReachableTools`. The caller is bound to
  that call's id and name and to the wrapper's resolved reachable tools. The
  tool calls `core.CallNested(ctx, calls...)`; with no caller attached, it
  gets `core.ErrNoNestedCaller`.
- **Pipeline.** A nested call goes through the same steps as a direct one:
  1. prepare and validate the arguments;
  2. `BeforeToolCall`, whose context carries `ParentToolUseID` and
     `ParentToolName`;
  3. the handler;
  4. `AfterToolCall`;
  5. `ToolExecutionStart`/`End` events with `ParentToolUseID`.

  Each nested call gets a fresh id. Preparation runs in call order. Handlers run
  concurrently unless a `Sequential` tool is among the calls, and results come back in call order. Every nested call that
  opens on the stream closes there, including one blocked or cut short by a
  terminate vote.
- **Interceptors run concurrently.** Within one `CallNested`, preparation is
  ordered and `AfterToolCall` is serialized, as in a direct batch. But
  `BeforeToolCall` for different wrappers' nested calls, and nested
  `AfterToolCall` alongside a direct call's finalization, can run at the same
  time. Interceptors must be safe for concurrent use. An `AfterToolCall` must
  not run a tool through `CallNested` on the context it is handed: that call
  would wait on the finalization its own call holds.
- **Results, not errors.** A tool the wrapper does not reach is
  `unknown_tool`. A block is `blocked_by_policy` with the reason, and a
  cancelled call is `aborted` with `Operation aborted`. `CallNested` returns
  an error only when an interceptor votes to end the run (`Block` with
  `Terminate`, or `AfterToolCall`'s `Terminate`). In that case the error is
  `core.ErrTerminated`, and the wrapper's result carries `Terminate` to the
  batch.
- **Terminate votes.** A nested handler's own terminate vote is dropped. The
  nested result's detail says `terminate vote ignored`, and the wrapper's says
  `nested terminate vote ignored`.
- **Out of the transcript.** Nested calls emit no `ToolResultEvent` and never
  enter the transcript.
  Usage a nested handler reports through `core.ReportUsage` still reaches the
  agent at once.

## Code mode

`codemode.New(tools, opts)` returns one tool, `code_mode` by default, that
runs a model-written Starlark script. The tool declares `tools` as its
`ReachableTools`, so the tool policy, the shell guard and the cycle checks
treat it as the wrapper it is. Its description is generated from the bound
tools' own input and output schemas (`RenderSignature`), and `BuildInfo`
reports what it costs.

A run:

1. **Thread.** The tool's handler starts a fresh `starlark.Thread`. Its
   globals are Starlark's own builtins, one function per bound tool, and
   `parallel`, `call` and `is_error`. `load` is refused.
2. **Calls.** A tool function takes keyword arguments only. It encodes them
   as JSON and calls `core.CallNested`, so each call goes through the agent's
   nested-call pipeline: interceptors and events, with the `code_mode` call as
   parent. `parallel` sends at most
   `MaxConcurrentCalls` calls per `CallNested`, and the dispatcher runs them
   concurrently.
3. **Results.** A success returns `Data`, converted to Starlark through JSON,
   or `{"text": Text}` when there is no `Data`. A failure, including a block,
   returns a `tool_error` value the script checks with `is_error`; nothing is
   raised.
4. **Limits.** Four limits stop a script, each with its own error:
   - `MaxTimeout`, a context deadline that cancels the thread: `timeout`;
   - `MaxSteps`, the thread's step limit: `step_limit_exceeded`;
   - `MaxCalls`, checked before each dispatch: `call_limit_exceeded`;
   - `MaxOutputBytes`: `output_limit_exceeded`.

   A terminate vote from an interceptor stops the script and sets `Terminate`
   on its result.
5. **Output.** Printed lines and the return value (`main()`, else `result`)
   go into a `tools.Accumulator` in middle-truncation mode, the shell tools'
   buffer. Output past the limit keeps its head and tail and is spilled to
   `SpillDir`.
6. **Result.** The result is the output text, with `Data` holding `output`,
   `return_value` and `calls_completed`. A failed or stopped script returns
   its error code (`syntax_error`, `runtime_error`, `invalid_arguments`, a
   limit, `aborted`) with the line, the partial output and the calls that
   completed. Their side effects are not undone.

Calling code mode outside an agent run fails the script's first tool call
with `ErrNoNestedCaller`: code mode never runs a tool around the agent's
pipeline.

## MCP client

The protocol is implemented by the official Go SDK,
[`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).
It speaks revision `2026-07-28` (no handshake; version and capabilities in
every request's `params._meta`) and negotiates down to the `initialize`
handshake of an earlier revision for a server that has not migrated.

`mcp.Pool.Connect` consumes servers over stdio (`command`), Streamable HTTP
(`url`) or the 2024-11-05 HTTP+SSE transport (`url` with `transport = "sse"`);
tool names are qualified as `<server name>__<tool>` unless `tool_prefix` says
otherwise. `mcp.Connect` opens a connection over any SDK transport
(`mcp.NewPipeTransport` joins a client to an in-process server), and
`ServerConnection.Session` returns the live SDK session for anything the pool
does not wrap (resources, prompts). Configuration keys and limits:
[`configuration.md`](configuration.md).

What AgentKit adds on the client:

- a stdio server runs in its own process group with exactly the configured
  environment, and is killed as a group on close; its stderr is forwarded line
  by line to `ConnectionOptions.Warnf`;
- a dead server is re-spawned (or re-opened) at the next call, at most
  `per_session_reconnect_limit` times, and never by re-sending a call that was
  cut off;
- every inbound frame (stdio), JSON response body and SSE event (HTTP) is
  checked strictly before the SDK decodes it;
- the HTTP client sends the configured headers and does not follow redirects,
  because those headers may carry a bearer token;
- results are capped at 50,000 characters, calls are counted against
  `per_session_call_limit`, and sampling is advertised only for a server with
  `allow_sampling` and answered only when `ConnectionOptions.Sampling` is set;
- a tool's `outputSchema` becomes `core.Tool.OutputSchema`. A schema whose
  root declares a type other than `object` is wrapped as
  `{"value": <schema>}`, matching how such a result's `Data` is shaped.
  A keyword the converter does not model, such as a type array, `anyOf`, or
  an `enum` with no type, becomes an unconstrained schema, so it never
  rejects valid `structuredContent`. A schema that is not a JSON object is
  dropped, the tool is still imported, and `Pool.Diagnostics()` gains a
  `SeverityError` entry, for example
  `mcp: server "srv" tool "broken": output schema dropped: a schema must be a JSON object, got "not a valid schema object"`;
- a call's result `Data` is the server's `structuredContent` when it is a JSON
  object, `{"value": <structuredContent>}` when it is any other JSON value, and
  `{"text": <joined text>, "content": <raw content blocks>}` when there is
  none. On success `ToolResult.Text` is the text blocks joined in order. A
  result with `isError` set is `Error: "tool_error"` with the joined text as
  `Detail`.

Constants: call limit 1000 per session, reconnect limit 3, timeout 30 s per
connect, list and call.

## Symbol and reference navigation

The `tools` package provides in-memory, workspace-confined symbol lookup and reference finding:

- `find_symbol` searches workspace declarations by name, backed by a lazily built, bounded in-memory symbol table (`symbolTable`).
- `find_references` finds usages and callers of declarations across the workspace. It combines exact Go type resolution (`go/parser`, `go/types` with workspace-local imports and synthetic external stubs; a use is `resolved` only when it resolves to the object declared at the target's position, and a use go/types cannot resolve because its type comes from a stubbed import is `lexical`) with outline-attributed lexical matching for other languages (a hit is `lexical` outside the comments and strings tree-sitter finds, and `text` in a build without cgo), attributing each site to its enclosing declaration from the outline (or `<file>` at top level).
- **Reference caching**: A thread-safe in-memory cache (`refCache`, of type `referenceCache`), created on the first `find_references` call and kept for the life of the tool set, holds the last complete type-check of the workspace's Go packages, their parsed files, file outlines and per-name candidate file lists. `write_file` and `edit_file` mark the written file and its Go package dirty; the shell tools (`execute`, `run_command`) mark everything for revalidation; a changed `.gitignore` or `.ignore` drops the Go check and the candidate lists. Before each query, deleted files are purged, the Go check is rebuilt if any Go package is dirty (re-parsing only the dirty packages' files), dirty files already outlined are outlined again, and candidate lists are dropped once anything changed. Nothing is written to disk.
- **Bounds**: each reference pass takes one file per Go file walked and per file read while looking for candidates from a budget of `SymbolOptions.MaxFiles` files and `SymbolOptions.MaxDuration` (50 000 and 2 s by default; `Workspace.References` always uses the defaults). The clock is also checked between Go package checks and between scanned candidate files; resolving uses in packages already checked is not interrupted. When the budget runs out the pass stops, keeps the sites found so far and sets `Partial`, and `find_references` appends `SymbolPartialMarker`. A Go check or candidate list cut short is not cached.

## Testing layout

- Unit and property tests sit beside their package.
- `testdata/golden/` holds the Anthropic request-body golden: the exact bytes
  sent, unindented. It pins regression, not vendor truth.
- `internal/policy` holds the cross-target (cgo off) and host (cgo on) build
  gates and the direct-dependency allowlist.
- `provider/faux` scripts a model offline; the root tests and the examples run
  against it.

Run everything with `make check` (fmt, vet, lint, test for the root and
`codesearch` modules).
