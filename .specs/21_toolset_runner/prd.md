---
spec_id: "21"
spec_name: "toolset_runner"
title: "Toolset and Runner: the tool pipeline as a value, with the agent loop as one consumer"
status: "active"
created_at: "2026-10-16T04:41:35.870936Z"
updated_at: "2026-10-16T04:41:35.870936Z"
intent_hash: "d805a0ce6b4b0fb9c3e5fe7c6c4418eb41640feb6f686b86258f90b9b5656ac7"
schema_version: 2
source: "docs/prd/12-simplify-the-embedder-contract.md"
---
## Intent

Move the tool side of an agent out of the driver into a value an embedder can build, validate and use without a model. The tool side is the tools, their policy, the guard, the after-interceptor, and the pipeline that runs one call through them. That value is `agentkit.Toolset`, and `agentkit.Runner` runs it. The agent loop becomes one consumer of the runner, so the pipeline exists in one place. Code that is not AgentKit's own loop (another harness, a test, an embedder's scheduler) can then run tool calls, code mode included, through the same steps a model's calls go through. This is a deliberate breaking change to `agentkit.Config`. It does not change what any tool, guard or interceptor does.

## Goals

1. `agentkit.NewRunner(ts)` returns a `*Runner` whose `Call` and `Batch` run the full pipeline (preparation, `Guard`, the handler with a nested caller, `After`, execution events) with no `Agent` and no provider. A `code_mode` tool built over `tools.For(ws, …)` and called through `Runner.Call` runs a script that calls `read_file`. The nested call reaches `Toolset.Events` as a `ToolExecutionStartEvent` and a `ToolExecutionEndEvent` whose `ParentToolUseID` is the `code_mode` call's id.
2. `NewRunner` refuses exactly the tool sets `agentkit.New` refuses today, with the same error texts. The one exception is `core.ErrUnguardedExecute`'s wording, which now names `Toolset.Guard`. `agentkit.New` refuses a `Config` whose `Toolset` fails `NewRunner`, and it sends no request when it does.
3. Running an agent changes in no observable way. The root module's loop, batch, nested-call, golden and scripted tests pass with only their `Config` literals migrated (`Tools:` becoming `Toolset: Toolset{Tools: …}`, and so on), and they see the same transcript, the same event sequence and the same stop reasons.
4. The pipeline exists once. `*Agent` has no `executeBatch`. In the root package, only the runner's implementation calls `core.PrepareArguments` and the guard and handler wrappers (`callBefore`, `invokeHandler`, `callAfter`).
5. `examples/harness/server.go` no longer contains a tool-call pipeline: no `core.NewToolUse`, no `core.PrepareArguments`, no `core.WithNestedCaller`, no `core.NestedCaller` implementation and no handler recovery. The program and its MCP handler run calls through `Runner.Call`.
6. `make check` passes in every module in `MODULES`. The exported-API golden is regenerated and records `Toolset`, `NewRunner`, `Runner` and the new `Config`.

## Non-goals

- `core.Named`, `guard.Allow`, `faux.ToolCall`, `faux.Text` and `sandbox.Open`, and rewriting the examples onto them. Those belong to the later spec **`embedder_helpers`**. Examples change here only as far as the `Config` change and the harness's pipeline require.
- The nested `mcpserve` module, and deleting the MCP server code (`mcpServer`, `serve`, `servedMCP`, `mcpConfig`) from `examples/harness/server.go`. Those belong to **`mcp_serve`**. In this spec that code stays and calls the runner.
- The end-to-end rewrite of the README consumer-contract table and of the data-flow sections of `docs/architecture.md`, settling the golden across every changed package, and redoing PRD 12's measurement. Those belong to **`embedder_contract_docs`**. This spec updates documentation only where it names identifiers this spec changes.
- The workspace package, `tools.For` and `tools.References`. Spec `20_workspace_package` delivered them.
- Changing what a tool, `Guard`, `After`, `ToolPolicy`, the event types, `core.NestedCaller`, `core.CallNested` or the terminate-vote rules do. Also out of scope: adding new refusals to construction, such as duplicate tool names.
- A transcript, cost, turn count, stop reason, run slot (`ErrBusy`), timeout or concurrency limit on the runner. Those stay the loop's.
- A stream object on the runner. It emits to a function.
- Usage accounting for calls made outside a run.
- Applying the `max_tokens` truncation rule in the runner. Deciding not to run a truncated response's calls is the loop's decision and stays in `loop.go`.
- Keeping `Config.Tools`, `Config.Policy`, `Config.Guard` or `Config.After` as aliases, deprecated fields or forwarders.

## Background

PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`, decisions D1 and D2) found that the pipeline that runs a tool call exists only inside the agent loop. Anything that is not AgentKit's model loop therefore rebuilds it.

What exists today:

- **`agent.go`.** `Config` holds the model side (`Client`, `Provider`, `Model`, `Effort`, `System`, `Prefix`, `MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune`, `MaxTokens`) and the tool side (`Tools`, `Policy`, `Guard`, `After`) in one struct. `New` validates the tool side inline:
  - `checkTool` and `checkToolTree` check every `cfg.Tools` entry: exactly one of `Handler` and `Execute`, no cycle by name, no wrapper reaching a `Terminating` tool.
  - `cfg.Policy.Resolve(cfg.Tools)` resolves the set, and each resolved tool is checked again.
  - `checkShellGuard` (`execguard.go`) refuses a reachable shell tool when `Guard` is nil, with `core.ErrUnguardedExecute`.
  - `New` keeps the resolved set in `Agent.tools`. `Agent.ReachableTools()` returns `core.ReachableTools(a.tools)`.
- **`batch.go`.** `(*Agent).executeBatch(ctx, s *core.EventStream, assistant, calls, turnCount)` runs a turn's calls in three phases:
  1. Prepare runs in order. It pushes `ToolExecutionStartEvent`, looks the tool up, calls `core.PrepareArguments` inside a recover, and runs `Guard` through `callBefore`, which fails closed on a panic. Unknown tools, invalid arguments and blocks are finalized inline here.
  2. The abort decision is made once.
  3. The handlers run (`invokeHandler`, which turns panics and handler errors into results), concurrently unless the batch is demoted to sequential. Each call is then finalized under a batch-scoped mutex: `After` through `callAfter`, then `ToolExecutionEndEvent` and `ToolResultEvent`.

  It returns one `ToolResultMessage` per call and an OR over the terminate votes. Panics are reported as `core.ErrorEvent` through `streamReporter(s)`. The usage reporter `core.WithUsageReporter(ctx, a.addUsage)` is attached here.
- **`nested.go`.** `nestedEnv`, built per batch from a copy of `Config` together with the stream, assistant, turn count and reporter, creates a `nestedCaller` for each call to a tool with `ReachableTools`. The caller gives every nested call a fresh `nested_…` id and runs the same steps. Its events carry `ParentToolUseID` and it emits no `ToolResultEvent`. A `Block`+`Terminate`, or an `After` vote to terminate, makes later calls return `core.ErrTerminated` and sets the wrapper's `Terminate`. A nested handler's own vote is dropped and annotated.
- **`loop.go`.** `runTools` either synthesizes the `max_tokens` "not executed" results or calls `executeBatch`. `callModel` sends `core.ToolWires(a.tools)` and `prompt.Build(a.cfg.System, a.tools)`.
- **`core`.** `core.CallNested` returns `core.ErrNoNestedCaller` when no caller is attached. `docs/architecture.md` (Code mode) therefore says that "calling code mode outside an agent run fails … with `ErrNoNestedCaller`" and tells an embedder to implement `core.NestedCaller` itself.
- **`examples/harness/server.go`.** It does exactly that. `toolServer.call` re-implements id assignment, `core.NewToolUse`, `core.PrepareArguments`, the guard, the handler call with panic recovery and the nested-caller attachment. `nested.Call` re-implements concurrent or sequential nested dispatch. Neither runs `After` or emits events. `main.go`, `prompt.go` and `main_test.go` call `srv.direct(ctx, name, input)`, and the MCP handler does too.

Pinned invariants this spec touches:

- TS-11-1 asserts that `Config` has fifteen fields.
- TS-11-9 asserts that `*Agent` exports exactly `Run`, `Stream`, `Messages`, `Usage` and `ReachableTools`.
- TS-13-67 (`internal/policy/layout_test.go`) compares the exported API of `.`, `core`, `tools`, `codemode` and `workspace` to `internal/policy/testdata/exported-api.golden`. Spec 20 retired the hash freeze, so the golden is a change detector.
- TS-12-14 (`core/size_test.go`) caps `core` at 1,600 lines of code and the root module at 20,000.

## Requirements

### R1. `Toolset` and the new `Config`

The root package exports `Toolset` with five fields:

- `Tools []core.Tool`
- `Policy core.ToolPolicy`
- `Guard core.BeforeToolCall`
- `After core.AfterToolCall`
- `Events func(core.Event)`

`Tools`, `Policy`, `Guard` and `After` mean exactly what the `Config` fields of the same names mean today.

`Config` loses `Tools`, `Policy`, `Guard` and `After` and gains one field, `Toolset Toolset`. Its fields, in order, are `Client`, `Provider`, `Model`, `Effort`, `System`, `Prefix`, `Toolset`, `MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune`, `MaxTokens`. A zero `Toolset` is valid and means an agent with no tools.

### R2. `NewRunner` validates a toolset

`NewRunner(ts Toolset) (*Runner, error)` performs today's construction checks in today's order:

1. every entry of `ts.Tools` passes `checkTool`;
2. `ts.Policy.Resolve(ts.Tools)` is computed;
3. every resolved tool passes `checkTool`;
4. the resolved set passes `checkShellGuard(ts.Guard, resolved)`.

It returns the same errors as today:

- `agentkit: tool %q has neither Handler nor Execute`
- `agentkit: tool %q sets both Handler and Execute; exactly one`
- `agentkit: reachable tools cycle detected: a -> b -> a`
- `agentkit: terminating tool %q cannot be reached through wrapper %q`
- an error wrapping `core.ErrUnguardedExecute` with the suffix ` (tool %q)` or ` (wrapper %q reached shell tool %q)`

`core.ErrUnguardedExecute` keeps its identity. Its text changes from "Config.Guard is nil" to "Toolset.Guard is nil", and nothing else in it changes. On any failure `NewRunner` returns a nil `*Runner`.

`NewRunner` copies `ts.Tools`, so a caller who changes its own slice afterwards does not change the runner. A `*Runner` is immutable after construction and safe for concurrent use by any number of goroutines. Only a `*Runner` obtained from `NewRunner` or `Agent.Runner` is supported.

### R3. What a runner exposes

- `(*Runner).Tools() []core.Tool` returns the resolved set in policy order: what a model, an MCP server or a prompt should see. Each call returns a fresh slice.
- `(*Runner).Reachable() []core.Tool` returns `core.ReachableTools` over the resolved set.
- `Agent.ReachableTools()` returns the same thing as `a.Runner().Reachable()`.

### R4. `Runner.Call` runs one call

`(*Runner).Call(ctx context.Context, name string, input json.RawMessage) core.ToolResult` runs one call as a batch of one (R5).

- **Id.** The call's id is generated as `call_` followed by 16 lowercase hex characters, using the package's existing `newID`.
- **Input.** An empty or whitespace-only `input` means `{}`.
- **Outcomes.** Every outcome is a result. `Call` never returns a Go error and never panics.
  - A name that is not in `Tools()` gives `unknown_tool`, with the same text a run produces today.
  - An input that is not a JSON object, or arguments that fail validation, give `invalid_arguments`.
  - A guard block gives `blocked_by_policy` with the guard's reason. The default reason is `blocked by policy`.
  - Arguments returned by the guard that JSON cannot carry give `invalid_arguments`.
  - A handler panic gives `panic`.
  - A `Handler` error gives `handler_error`.
  - A context that is done before the handler would start gives `aborted` with `Operation aborted`.
- **Guard context.** `Guard` receives `ToolName`, `ToolUseID`, `Tool`, `Arguments` and `RawInput` as in a run, with `Batch` holding the one call, `Index` 0, `Assistant` nil, `TurnCount` 0 and no parent.
- **Terminate.** The returned result's `Terminate` is the call's final vote (R5).

### R5. `Runner.Batch` runs a batch with the driver's rules

`(*Runner).Batch(ctx context.Context, calls ...core.ToolUseBlock) []core.ToolResult` runs the calls with the batch rules the agent applies today:

- A `ToolExecutionStartEvent` is emitted for each call, in order, during a sequential prepare phase that also runs argument preparation and `Guard` in call order.
- The abort decision is made once, after prepare and before any handler starts. If the context is done, every call that is not yet finalized is closed as `aborted`, a call the prepare loop never reached included, and no handler runs.
- Handlers run concurrently, one goroutine per call joined by a `sync.WaitGroup`. They run in call order when there is at most one, or when a `Sequential` tool is among the calls.
- Finalization (`After`, the end event, the result event) is serialized under a mutex scoped to the batch.
- Exactly one result comes back per call, in call order, whatever order the handlers finish in.
- Calls whose handlers do not run (unknown tool, invalid arguments, a block, an abort) are finalized inline with the same codes as R4.

Call ids are used verbatim, even when empty. `Guard` receives `Batch` = `calls`, `Index` = the call's position, `Assistant` nil and `TurnCount` 0. Zero calls return an empty slice and emit nothing.

A result's `Terminate` is the call's final vote:

- the handler's `Terminate`, unless `After` returned a non-nil `Terminate`, which replaces it;
- `true` for a wrapper whose nested call was terminated by an interceptor;
- for a call `Guard` blocked, the guard decision's `Terminate`.

The text of a returned result (`LLMText`) equals the text block of the `ToolResultMessage` the same call produces in a run, unless an `After` interceptor edited that message in place. `After`'s in-place edits to `AfterToolCallContext.Result` reach that message, which goes to a run's transcript and to the `ToolResultEvent`. They do not reach the returned `core.ToolResult`.

### R6. Nested calls run through the runner

Every call to a tool that declares `ReachableTools` runs with a `core.NestedCaller` on its context, whether it came from `Call`, `Batch` or an agent run. The nested caller behaves exactly as `nested.go`'s does today:

- each nested call gets a fresh `nested_…` id;
- the wrapper reaches only its declared, policy-resolved tools, and any other tool is `unknown_tool`;
- `Guard` and `After` see `ParentToolUseID` and `ParentToolName`;
- a block is a result;
- a `Block`+`Terminate`, or an `After` that votes `Terminate`, makes `CallNested` return `core.ErrTerminated` and sets the wrapper's `Terminate`;
- a nested handler's own vote is dropped and annotated (`terminate vote ignored: …` on the nested result, `nested terminate vote ignored` on the wrapper's);
- cancellation gives `aborted`;
- nested events carry `ParentToolUseID`;
- nested calls emit no `ToolResultEvent`.

A wrapper such as `code_mode` therefore works from `Runner.Call` with no agent. `core.ErrNoNestedCaller` remains the answer only for a tool that declares no `ReachableTools`, or for a tool invoked by something that is neither an agent nor a runner.

### R7. Events

For calls a runner runs outside an agent run, `Toolset.Events` receives the same execution events a run's stream receives for those calls:

- `ToolExecutionStartEvent` and `ToolExecutionEndEvent` for every call, with `ParentToolUseID` set on nested ones;
- `ToolResultEvent` for each top-level call, carrying the message as `After` left it;
- `core.ErrorEvent` for each panic the pipeline recovered (in `PrepareArguments`, `Guard`, a handler or `After`).

A nil `Events` discards them. `Events` may be called from several goroutines at once, so it must be safe for concurrent use and should not block. For any one call, its start event precedes its end event. `(*core.EventStream).Push` is a conforming sink. A panic raised by `Events` is recovered and that event is dropped. It is not reported again, because the sink that would receive the report is the one that panicked.

During an agent run, the run's calls emit to the run's stream and `Toolset.Events` is not called for them.

### R8. The agent is built on a runner

- **`New`.** `New` checks `Client`/`Provider` and `Model` first, as today, and then calls `NewRunner(cfg.Toolset)`. It returns that error unchanged and sends no request.
- **Tools in requests.** The agent keeps the runner. Request tool definitions (`core.ToolWires`), `prompt.Build` and the fixed-token estimate all use `Runner.Tools()`.
- **`Agent.Runner`.** `(*Agent).Runner() *Runner` returns the agent's runner, the same pointer on every call, and is safe while a run is in flight. Calls made through it are not part of any run:
  - they do not enter the transcript;
  - they are not counted in `Agent.Usage`;
  - they do not claim the run slot;
  - their events go to `Toolset.Events`.
- **The loop.** `runTools` hands a turn's calls to the runner's batch implementation. It passes the run's stream as the event sink, the assistant message and the turn count for the guard context, and a context carrying `core.WithUsageReporter(ctx, a.addUsage)`. It receives the per-call `ToolResultMessage`s, as `After` left them, together with the votes, and records the messages as today. The run ends on a terminate vote when any call voted (unchanged). The `max_tokens` path (`synthesizeTruncated`) stays in `loop.go`.
- **Behaviour.** A run's transcript, event sequence and order, stop reasons, model-visible texts and error texts are unchanged.
- **One implementation.** The batch executor and the nested caller exist once, as the runner's implementation (they hang off the runner, not `*Agent`). `Runner.Batch`, `Runner.Call` and the loop share it.
- **Usage outside a run.** Outside a run the runner attaches no usage reporter. A context that already carries one is passed through unchanged.

### R9. Every consumer moves to the new shape

- **Root package tests.**
  - Tests move to `Config{…, Toolset: Toolset{…}}`.
  - The `runBatch` helper in `batch_test.go` drives the runner's batch implementation.
  - TS-11-1 asserts the twelve fields above.
  - TS-11-9 asserts the six exported methods `Run`, `Stream`, `Messages`, `Usage`, `ReachableTools` and `Runner`.
  - New tests cover R2 to R8, including the code-mode-through-`Runner.Call` case in Goal 1, the error parity in Goal 2, and the property that `Batch`'s result texts equal the run's message texts when `After` is nil.
- **Other tests.** `metadata_message_test.go`, `codemode/smoke_test.go`, `sandbox/integration_test.go` and `sandbox/codemode_smoke_test.go` move to the new `Config`.
- **Examples.** `examples/agentdemo`, `codemode`, `codingagent`, `customtools`, `gateway`, `mcp`, `sandbox` and `examples/harness/harness.go` (the scripted harness) build `Config{Toolset: …}`.
- **`examples/harness`.**
  - `server.go` loses `toolServer.call`, the `nested` type, and the handler and panic recovery.
  - `main.go` builds `code_mode` over the proxies and passes it with the proxies to `agentkit.NewRunner`, under the configured guard.
  - `renderSpec`, `readTestCommands`, `runChecks`, the final `git status` call and the MCP tool handler run calls through `Runner.Call`.
  - `systemPrompt` reads `Runner.Tools()`.
  - The per-call log and the call and blocked counts the example prints are produced by the toolset's hooks: a `Guard` that wraps the configured guard, plus `After` and/or `Events`. The log format may change where the hooks cannot reproduce it exactly, and `main_test.go` is updated to match.
  - The example's offline run still completes with "Harness example completed successfully".

### R10. Golden, documentation and errata

- **API golden.** `internal/policy/testdata/exported-api.golden` is regenerated with `AGENTKIT_UPDATE_API_GOLDEN=1` and committed.
- **Generated docs.** `make doc` is run and `docs/generated/` is committed.
- **Doc comments.** The doc comments of `core.ErrNoNestedCaller`, `core.CallNested` and `core.WithNestedCaller` say "an agent run or a `Runner`" where they say "an agent run" today. The package doc of the root package names `Toolset` and `Runner`.
- **Documentation**, updated in the same change, without the end-to-end rewrites owned by `embedder_contract_docs`:
  - `docs/api.md`: the root row names `Toolset`, `NewRunner` and `Runner`.
  - `docs/configuration.md`:
    - the `agentkit.Config` table has a `Toolset` row in place of the four tool rows;
    - a new `agentkit.Toolset` section covers the five fields, `NewRunner`'s refusals and the `Events` contract;
    - "Run behaviour" names `Toolset.Guard` and `Toolset.After`.
  - `docs/architecture.md`:
    - the root row of the Packages table;
    - the `Config.Policy`/`Guard`/`After` and `batch.go`/`nested.go`/`executeBatch` mentions in "Data flow of one run", "Invariants" and "Nested tool calls";
    - the Agent surface;
    - the Code mode paragraph on `ErrNoNestedCaller`, which now points to `Runner.Call`.
  - `README.md`:
    - the `agentkit.New` statements in the quickstart, code-mode and sandbox snippets;
    - the "driver is optional" paragraph;
    - the consumer-contract rows for `agentkit.Config` and the `Agent` methods, plus rows for `Toolset`, `NewRunner` and `Runner`;
    - the root row of the packages table.
  - `examples/README.md`, the example READMEs that name `Config.Tools` or `Config.Guard`, `examples/harness/README.md` (its `ErrNoNestedCaller` paragraph), and the root line in `AGENTS.md`.
- **Erratum.** `docs/errata/11_minimal_driver.md` gains an entry. It names PRD 12 and this spec, and records that the tool fields moved to `Toolset`, that `Agent` gained `Runner`, and that the batch executor and nested caller are now the runner's.
- **Line budget.** TS-12-14's budgets still hold.

## Design Decisions

1. **Breaking `Config`, with no aliases.** PRD 12 D1 and D6 reject keeping the four fields as sugar, and compile errors at every call site are what make the migration complete.
2. **`NewRunner` reuses `checkTool`, `Resolve` and `checkShellGuard` in today's order, and `New` returns its error unchanged.** Error parity is the acceptance criterion, and tests match on these texts.
3. **`core.ErrUnguardedExecute`'s text names `Toolset.Guard`.** The old text names a field that no longer exists. `errors.Is` callers are unaffected, and no test pins the text.
4. **`Runner.Call` is `Batch` of one call, with a generated `call_<16 hex>` id and empty input meaning `{}`.** One code path gives one set of outcomes. The id prefix follows `newID`'s existing `nested_` convention, and the empty-input rule matches `core.NewToolUse` and the nested caller.
5. **Outside a run, `Guard` sees `Assistant` nil and `TurnCount` 0, with `Batch` and `Index` filled from the arguments.** PRD 12 left this to spec time. The arguments supply `Batch` and `Index` for free, and inventing an assistant message would mislead a guard. A nil `Assistant` is how a guard can tell that no model made the call.
6. **The exported `Batch` returns `[]core.ToolResult`, with the final vote in `Terminate`. The loop uses an unexported entry point to the same implementation, which also returns the `ToolResultMessage`s.** The loop needs `After`'s in-place edits to the message for the transcript, while programmatic callers need `Data` and the vote. Two entry points over one engine serve both without a second copy.
7. **`After`'s in-place edits to `Result` do not reach the `core.ToolResult` that `Call` and `Batch` return.** That is how nested calls behave today (`nested.go` discards the message). The edited message is still observable through `ToolResultEvent`, and the message has no clean mapping back to a `ToolResult`.
8. **`Toolset.Events` receives the full execution sequence a run's stream gets (start, end, `ToolResultEvent` for top-level calls, `ErrorEvent` for recovered panics), not only start and end.** One emitter keeps a run's event sequence byte-for-byte unchanged (Goal 3). It is also the only way an embedder can observe a recovered panic, or see the message `After` produced, outside a run.
9. **`Events` may be called concurrently, and a panic in it is recovered and the event dropped.** The batch and the nested callers already emit from several goroutines. Serializing user code behind a runner-wide lock risks deadlock when a sink calls back into the runner. "Panics in third-party code are contained" is an existing invariant.
10. **During a run, the run's stream replaces `Toolset.Events` for that run's calls.** PRD 12 D2 says so, and it keeps a run's observers on one ordered stream.
11. **Calls through `Agent.Runner()` are outside every run: no transcript, no usage, no run slot.** The runner owns none of those (PRD 12 §2). Making them `ErrBusy` during a run would break the "safe during a run" promise and the serving use case.
12. **No usage reporter is attached outside a run, and a caller's own reporter on the context is passed through.** There is no agent total to add to, and `core.ReportUsage` is already a no-op without a reporter.
13. **The `max_tokens` truncation path stays in the loop.** It is a decision about a model's response, not about running a call, and PRD 12 assigns the runner no model concerns.
14. **The unknown-tool text stays `no tool named %q is available in this run; …` for runner calls too.** It is model-visible text pinned by `unknown_tool_test.go`, and keeping one code path is worth more than the wording outside a run.
15. **`Runner` is immutable and copies `ts.Tools`. Only runners from `NewRunner` or `Agent.Runner` are supported.** This matches `New`'s defensive copy today and spec 20's rule for `Workspace`. A zero `Runner` has no validated tool set to stand on.
16. **`batch.go` and `nested.go` keep their file names and become the runner's implementation. The exported runner surface goes in a new `runner.go` with `checkTool`/`checkToolTree`, and `execguard.go` stays.** This keeps the diff readable and the history traceable, and PRD 12 D2 says those files are the runner's implementation.
17. **The harness keeps its log and counters, rebuilt from `Guard`/`After`/`Events` hooks, and its MCP code stays until `mcp_serve`.** The pipeline is the part of the example this spec removes. Its observability is the example's own, and the split assigns the MCP wiring to the later spec.
18. **The golden is regenerated here, and spec 11's erratum gains an entry.** `make check` cannot pass otherwise. This spec changes 11-REQ-1's `Config` and TS-11-9's surface, and the erratum file is where the delivered driver's differences from spec 11 are recorded.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `20_workspace_package` | depends on | The tests, the harness example and the code-mode acceptance test build tools with `workspace.Open` and `tools.For`, which spec 20 delivers. Its regenerated golden is the baseline this spec regenerates again. |
| `11_minimal_driver` | modifies | `Config`'s shape (11-REQ-1, TS-11-1), the `Agent` surface (TS-11-9, which gains `Runner`), and the batch executor and nested caller moving under the runner. `docs/errata/11_minimal_driver.md` gains an entry. |
| `08_code_mode` | modifies | Code mode now runs outside an agent through `Runner.Call`. `docs/architecture.md`'s `ErrNoNestedCaller` paragraph changes, and `codemode/smoke_test.go` moves to the new `Config`. |
| `13_sandbox_core` | modifies | TS-13-67's `exported-api.golden` is regenerated. `sandbox/integration_test.go` (the unguarded-proxy-shell refusal) and `sandbox/codemode_smoke_test.go` move to `Config{Toolset: …}`. |

## Verified External API

This spec adds no external dependency. The only third-party code it touches is the MCP Go SDK, which `examples/harness/server.go` already uses (its own `go.mod` requires `github.com/modelcontextprotocol/go-sdk v1.8.0`). There the handler body changes to call `Runner.Call`, and the SDK calls stay as they are.

| Package | Symbol | Signature as used | Status |
|---|---|---|---|
| `github.com/modelcontextprotocol/go-sdk/mcp` | `(*Server).AddTool` | `srv.AddTool(&sdk.Tool{Name, Description, InputSchema}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error))` | Unverified (the module cache is outside the repository). Taken from the existing call site in `examples/harness/server.go`, which compiles today. |
| same | `CallToolRequest.Params.Arguments` | read as `json.RawMessage`-compatible bytes and passed as tool input | Unverified, from the same call site. |
| same | `CallToolResult` | `&sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: …}}, IsError: bool}` | Unverified, from the same call site. |

The root module's own symbols that the work relies on (`core.PrepareArguments(t Tool, c ToolUseBlock) (PreparedArguments, error)`, `core.NewToolUse(id, name string, raw json.RawMessage) (ToolUseBlock, error)`, `core.WithNestedCaller`, `core.CallNested`, `core.ReachableTools`, `core.WithUsageReporter(ctx, func(core.Usage))`, `(core.ToolPolicy).Resolve([]Tool) []Tool`, `guard.IsShellTool`) were read in the repository.
