# Run tool calls from model-written scripts

## Intent

Let a model do a multi-step tool task by writing one short script that calls
the agent's tools as functions, so the intermediate results stay inside the
script and only what the script reports reaches the model's context. Every
call a script makes passes through the same authorization, audit and event
path as a call the model makes directly. A script is never a way around the
embedder's policy.

## Goals

1. A model can finish a task that needs N dependent or fan-out tool calls in
   one tool call. Its context grows by the script and the script's output,
   not by the N results.
2. Every call made from inside a script runs the BeforeToolCall interceptor,
   AfterToolCall, the audit record, tracing and the tool events, exactly as a
   direct call does. An interceptor that blocks a tool directly blocks it
   inside a script too.
3. A tool that can reach other tools declares them. Tool-policy resolution,
   the unguarded-shell check (`core.ErrUnguardedExecute`) and an embedder
   inspecting the resolved set all see the reachable tools, not only the
   wrapper's name.
4. Any tool can declare the shape of what it returns. Declaring it changes no
   request body: the provider request is byte-identical with or without
   output schemas.
5. Tools from an MCP pool can be called from a script, with the server's
   output schema and structured result passed through.
6. A script can run several tool calls concurrently, and a group of N
   independent calls takes about as long as the slowest one, not the sum.
7. The whole feature is testable offline against the scripted provider, with
   no API key.

## Non-goals

- Making code mode the default, or hiding the inner tools from the model.
  Whether the model also sees them directly is the embedder's choice, made
  through the existing tool policy.
- A general-purpose sandbox: no file system, network, environment, process
  or clock access from a script other than through the tools it was given.
- Calling models from a script (pi's classifiers and `models`). A subagent
  tool may be bound like any other tool; it is not special-cased.
- Persisting state between scripts (pi's `store`/`load`).
- Resuming a script that was interrupted mid-execution.
- Running a script from inside a script.
- Validating a tool's actual result against its declared output schema at
  run time.
- Progressive discovery of bound tools (search and describe functions
  callable from a script). Every bound tool is declared up front in this
  version.
- A memory limit on a script. The chosen runtime cannot enforce one; the
  step, time, call and output limits bound it instead.
- Any change to agent-fox. Which phases use this, and whether at all, is
  agent-fox's decision, made in its own PRD.

## Background

Cloudflare's Code Mode (blog.cloudflare.com/code-mode,
developers.cloudflare.com/agents/tools/codemode) and pi's `codemode` tool
(pi.dev/docs/latest/codemode) expose an agent's tools as typed functions
inside a sandboxed script runtime, give the model one "run this script" tool,
and return only the script's output. Their stated gains are fewer tokens on
chained calls and better handling of large tool sets. The second matters less
to agentkit's embedders (agent-fox phases carry 7–10 tools); the first
matters a lot. Reading cost dominates every agent-fox phase, and
`code_search`, `find_symbol`, the repo map and compaction all exist to reduce
it.

What exists today:

- `core.Tool` carries `InputSchema` and nothing about output. `ToolWire` is
  the provider-facing projection and already excludes every field that is
  not name, description, input schema and constrained sampling.
- `core.ToolResult` already separates the model-facing `Text` from the
  structured `Data`. A script needs `Data`.
- `batch.go` runs every call through prepare (arguments, validation, the
  interceptor), execute and finalize (AfterToolCall, audit, events). A
  handler called from anywhere else skips all of it.
- `execguard.go` and the tool policy (`core.ToolPolicy`) decide by tool
  name. A wrapper tool whose name is not `execute` hides an `execute`
  inside it from both.
- `core.WithUsageReporter` already lets work outside the loop (a delegated
  subagent) report spend as it happens.
- `mcp/protocol.go` already decodes a server's `outputSchema` and a
  result's `structuredContent`, and uses neither.
- `subagent` is the one existing tool that runs other work on the agent's
  behalf. Its child's tool calls belong to the child agent, not the parent.

## Requirements

### Output schemas

- Any tool may declare an output schema describing the structured result
  (`Data`) it returns. It is optional; a tool without one behaves exactly as
  today.
- An output schema never reaches a provider. The request body for a tool set
  is byte-identical whether or not its tools declare output schemas, on
  every wire API.
- Every built-in tool declares one. A conformance test runs each built-in
  and checks its `Data` against its declared schema, on success and on each
  error code the tool documents.
- A tool imported from an MCP server carries the server's `outputSchema`
  when the server sends one. A malformed one is dropped with a diagnostic
  naming the server and tool, and does not fail the connection.
- A script calling an MCP tool receives the result's `structuredContent`
  when present, and its text content otherwise.

### Declaring the tools a tool can reach

- A tool may declare the set of tools it can call. A tool that declares none
  reaches none, as today.
- Tool-policy resolution applies through the declaration. A tool excluded by
  name, or by `NoTools`, is unreachable through any wrapper, and a wrapper
  left with nothing to reach is dropped from the resolved set.
- The unguarded-shell check counts a shell tool reached through a wrapper.
  Such a run with no interceptor fails with `core.ErrUnguardedExecute`
  before the first request, naming both the wrapper and the shell tool.
- An embedder can ask for the full reachable set of a resolved tool list
  (every tool, plus everything every wrapper reaches, transitively), so that
  an invariant like agent-fox's `AssertReadOnly` can be checked against it.
- A wrapper may not reach itself, directly or through another wrapper. A
  cycle is refused when the agent is built, with an error naming the cycle.

### Nested calls

- A call made by a tool on the agent's behalf (a nested call) goes through
  the same preparation, validation, interceptor, handler, AfterToolCall,
  audit, tracing and events as a direct call, in that order.
- The interceptor can tell a nested call from a direct one, and sees the
  wrapper's tool-use ID and name. It can block, rewrite arguments or vote to
  terminate, as for a direct call.
- A blocked nested call returns to its caller the same error result the
  model would have received. It is not a Go error and does not end the
  wrapper's call.
- An interceptor's terminate vote on a nested call ends the wrapper's call,
  and the wrapper's result carries the vote to the batch.
- A tool the embedder marks as terminating cannot be reached through a
  wrapper: building an agent with such a declaration is refused, naming the
  tool. A nested call whose handler votes to terminate anyway (a tool that
  was not marked) does not end the run; its result is returned to the caller
  as usual, and the wrapper's result and the nested call's audit record both
  state that a terminate vote was ignored.
- Nested calls that a wrapper issues together run concurrently, under the
  same rules as a parallel batch: one failure does not cancel the others,
  each result is returned in the order the calls were issued, and a tool
  whose execution mode is sequential runs one call at a time.
- Cancelling the run cancels every nested call in flight; their results are
  the same aborted results a direct call gets.
- Each nested call has its own tool-use ID and emits the tool-execution
  events with the parent tool-use ID, so an embedder counting tool calls can
  tell direct calls from nested ones. An embedder that ignores the parent ID
  sees the events it sees today.
- The audit record of a nested call names the parent tool-use ID.
- The session log records the wrapper call and its result only. Nested
  calls are not logged as messages, and resuming a session never re-runs
  them.
- Turn budgets and stop policies, `stop.WhenToolCalled` included, see the
  wrapper's call and not the nested ones.
- Spend by a nested call (a bound subagent) is reported to the agent as it
  happens, as a direct subagent call's is.

### The code-mode tool

- A code-mode package offers one constructor: given a set of tools and
  options, it returns one tool that runs a script written by the model. The tool declares the given tools as the tools it reaches.
- Constraint: scripts are written in Starlark and run on `go.starlark.net`.
  The dependency is recorded as a ruling in `docs/DEPS.md`.
- The model sees one tool. Its description states the language, the rules
  of the runtime (no exceptions, how errors are returned, how to run calls
  concurrently, that side effects are not undone), and a declaration for
  each bound tool (name, description, input shape, output shape where
  declared), generated from the tools' own schemas so the two cannot drift.
  A tool without an output schema is declared as returning an untyped value.
- The constructor reports the size of the generated description, so an
  embedder can see what binding a large tool set costs before it sends it.
- Inside a script, each bound tool is a function taking keyword arguments
  that match its input schema. It returns the tool's structured result on
  success, and an error value carrying the tool's error code and detail on
  failure, including a blocked call. A script tells the two apart without
  any exception mechanism.
- A script can issue a group of calls to run concurrently and receive their
  results as a list in the order given. The number of calls in flight at
  once is bounded by an option with a default.
- A script has no access to anything outside its bound tools: no file
  system, network, environment variables, processes or wall clock. Loading
  another module is refused.
- Each run has limits, each with a default and an option: wall time,
  execution steps, the number of nested calls, and output size. Hitting a
  limit ends the script with an error result that names the limit, keeps
  the partial output, and lists the nested calls that completed.
- The model reads what the script prints and the value it returns, rendered
  as text. Output past the limit keeps its head and tail, is marked
  truncated in the result metadata, and is spilled to a file as the built-in
  tools spill theirs.
- A script that fails (a syntax error, a runtime error, a call to an unknown
  tool, an argument the tool's schema rejects) returns an error result with
  the message, the line, the partial output, and the nested calls that
  completed. Their side effects are not undone.
- A code-mode tool may not bind another code-mode tool; the cycle and
  nesting rules above refuse it when the agent is built.
- The tool's own descriptive text and guidelines are prompt documents,
  replaceable by name like the SDK's other prompt text (PRD 06 §12).
- An example program shows code mode over the built-in read tools and over
  an MCP pool, and runs with no API key against the scripted provider.
- `docs/architecture.md`, `docs/configuration.md` and `README.md` describe
  the tool, its options and its limits.

## Design Decisions

1. Code mode is a tool, not a loop mode. It needs no change to the loop's
   iteration rule, and an embedder can mix it with direct tools.
2. Nested dispatch, the reach declaration and output schemas live in the
   core packages, not the code-mode package, because they are seams any
   wrapper needs (an MCP proxy, a macro tool), not code-mode features.
3. Starlark, because it has no I/O unless the host adds it, counts execution
   steps, is deterministic, and is pure Go. That fits the SDK's "ordinary Go
   you can read" stance better than embedding a JavaScript engine. The cost
   is that models know Python better than Starlark's dialect of it, which the
   tool description has to make up for.
4. A blocked nested call is a tool error the script can handle, not an
   abort, because that is what the model gets for a direct blocked call.
5. Terminating tools are refused as nested calls when marked, and an
   unmarked terminate vote is ignored and reported. A run ends on a result
   the model chose to submit as a direct call, and an embedder's "the result
   is a tool call" contract holds whatever the script does.
6. The session log keeps only the wrapper call. It keeps resume simple and
   the log a record of what the model saw; the audit trail and the events
   carry every nested call for anyone who needs them.
7. Stop policies see only direct calls, matching the log.
8. Output schemas are documentation and are not enforced at run time. A
   conformance test holds the built-ins to theirs; enforcing every call
   would turn a schema drift into a broken tool.
9. Every bound tool is declared up front. The tool sets agentkit's
   embedders bind are small enough, and discovery functions can be added
   later without changing what a script that does not use them sees.

## Dependencies

- PRD 06 §12, prompt text as documents: the code-mode tool's text is one.
- PRD 03, the classic MCP client: output schema and structured content
  pass-through.
- `go.starlark.net`.
- agent-fox's `AssertReadOnly` (`internal/agentrun/policy.go`) is the first
  consumer of the reachable-set query. agent-fox changes nothing until its
  own PRD.
