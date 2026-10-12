---
spec_id: "07"
spec_name: "nested_tool_calls"
title: "Nested Tool Calls, Reachability Declarations, and Transitive Policy Enforcement"
status: "active"
created_at: "2026-10-08T14:55:20.462183Z"
updated_at: "2026-10-08T14:55:20.462183Z"
intent_hash: "9e6e36a804498120ff9529ca46c6c6defa44ce0356935abcf2ede7a641ef611c"
schema_version: 2
source: "docs/prd/08-run-tool-calls-from-model-written-scripts.md"
---
## Intent

Enable tools to declare reachable child tools, validate call hierarchies against cycles and terminating tools, filter reachable sets through agent tool policies, enforce execution guards across wrappers, and execute nested tool calls through a uniform authorization, audit, event, and concurrency pipeline with parent tool-use ID tracking.

## Goals

1. Any `core.Tool` can declare the child tools it can reach via `ReachableTools []Tool`.
2. Constructing an agent (`NewAgent`, `NewAgentWithHistory`) or registering a tool (`RegisterTool`) with a cycle in reachable tools is refused with an error naming the cycle path.
3. Constructing an agent or registering a tool where a wrapper reaches a tool marked `Terminating: true` is refused with an error naming both the wrapper and the terminating tool.
4. An embedder can query the deduplicated transitive closure of all reachable tools for any tool list via `core.ReachableTools(tools []Tool) []Tool` and for an agent via `(*Agent) ReachableTools() []core.Tool`.
5. `ToolPolicy.Resolve` recursively filters `ReachableTools` by `NoTools`, `ToolNames`, and `ExcludeTools`. A wrapper tool that originally declared reachable tools and is left with an empty reachable set after filtering is dropped from the resolved tool list.
6. `checkExecuteGuard` inspects all transitively reachable tools. If an unguarded shell tool is reachable through a wrapper and `cfg.BeforeToolCall` is nil, execution fails before the first provider request with `core.ErrUnguardedExecute` naming both the wrapper and the shell tool.
7. Nested tool calls run through the identical pipeline as direct tool calls: prepare, validate, `BeforeToolCall`, plugin veto, execute handler, tracing span, audit log, `AfterToolCall`, and execution events, in that order.
8. `BeforeToolCallContext` and `AfterToolCallContext` carry `ParentToolUseID` and `ParentToolName`. Interceptors can block nested calls (returning an error result to the wrapper without aborting it), rewrite arguments, or vote to terminate.
9. An interceptor's terminate vote on a nested call terminates the wrapper's execution and propagates `Terminate = true` to the batch; an unmarked tool handler's terminate vote on a nested call is ignored, does not end the run, and is recorded as ignored in the nested audit record and wrapper result.
10. Nested calls issued concurrently run in parallel using goroutines and `sync.WaitGroup`, respect `Sequential` execution mode, preserve issued result ordering, isolate sibling failures, abort cleanly on context cancellation, emit `ToolExecution*` events with `ParentToolUseID`, and report spend immediately via `core.WithUsageReporter`.
11. The session log and transcript history record only the top-level wrapper call and result; nested tool calls are omitted from session persistence and turn stop policies (`stop.WhenToolCalled`).

## Non-goals

- **No Starlark runtime, script parsing, or sandboxed execution environment.** That is the responsibility of the third spec in this split, `code_mode`.
- **No tool output schema definitions or schema conformance testing.** That was delivered by the first spec of this split, `06_tool_output_schemas`.
- **No dynamic or progressive tool discovery functions inside scripts.** All reachable tools must be declared up front on `core.Tool.ReachableTools`.
- **No persistence or resumption of mid-wrapper nested tool execution.** Session persistence and resume operate solely at the turn boundary on top-level tool results.
- **No changes to agent-fox.** Agent-fox will integrate the `ReachableTools` query in its own PRD and codebase.

## Background

The script-running initiative (`docs/prd/08-run-tool-calls-from-model-written-scripts.md`) requires wrapper tools (such as code mode) to invoke child tools on the agent's behalf while ensuring security invariants, audit trails, and execution policies cannot be bypassed.

What exists today:

- `core.Tool` (`core/tool.go`, line 33) defines tool metadata, handlers, and execution modes, but has no mechanism to declare reachable child tools or designate tools as terminating.
- `core.ToolWire` (`core/tool.go`, line 88) projects only `Name`, `Description`, `InputSchema`, and `ConstrainedSampling` to LLM providers.
- `core.ToolPolicy.Resolve` (`core/toolpolicy.go`, line 21) resolves tool availability (via `Tools`, `NoTools`, `ToolNames`, and `ExcludeTools`) against a flat list of registered tools, unaware of child tools encapsulated inside wrappers.
- `checkExecuteGuard` (`execguard.go`, line 14) verifies that shell tools (`guard.IsShellTool`) are not registered without a `BeforeToolCall` interceptor. A shell tool hidden inside a wrapper currently bypasses this check.
- `executeBatch` (`batch.go`, line 40) executes direct tool calls across three phases: sequential preparation (validation, interceptor, inline finalization), parallel execution (`invokeHandler`, tracing, audit), and serialized finalization (`AfterToolCall`, image normalization, events). Any tool handler calling tools directly outside `executeBatch` bypasses interception, auditing, events, and tracing.
- `BeforeToolCallContext` (`core/tool.go`, line 260) and `AfterToolCallContext` (`core/tool.go`, line 287) contain tool and turn context, but have no fields identifying whether a call is nested or which wrapper invoked it.
- `AuditEvent` (`core/audit.go`, line 26) records tool invocations with `ToolName`, `ToolUseID`, and `ArgumentsHash`, but cannot link nested calls to a parent wrapper call.
- `ToolExecutionStartEvent` and `ToolExecutionEndEvent` (`core/event.go`, lines 145 and 154) carry `ToolUseID` and `Name`, but lack parent identification.
- `NewAgent` (`agent.go`, line 111) and `RegisterTool` (`agent.go`, line 185) validate tool handler presence via `checkTool` (`agent.go`, line 150), but perform no hierarchy or cycle validation.
- `core.WithUsageReporter` (`core/usage.go`, line 143) provides an established pattern for propagating usage callbacks through `context.Context`.

## Requirements

### Reachable Tools Declaration and Validation

1. **`core.Tool` Reachable and Terminating Declarations:**
   - `core.Tool` gains `ReachableTools []Tool`: the list of tools reachable through this tool. Tools that declare none reach none.
   - `core.Tool` gains `Terminating bool`: marks a tool that intentionally terminates the agent run upon completion (e.g. `finish`, `submit_answer`).
   - `core.ToolWire` excludes `ReachableTools` and `Terminating`. Provider request payloads remain byte-identical.

2. **Cycle Detection at Agent Construction and Registration:**
   - In `NewAgent`, `NewAgentWithHistory`, and `RegisterTool`, reachable tool hierarchies are validated using depth-first cycle detection.
   - If any tool reaches itself directly or transitively (e.g. `A -> B -> A`), agent construction or registration fails immediately with an error naming the cycle path (e.g. `agentkit: reachable tools cycle detected: A -> B -> A`).

3. **Terminating Tool Reachability Restriction:**
   - If any wrapper tool declares a reachable tool where `Terminating == true` (directly or transitively), agent construction or registration fails immediately with an error naming both the wrapper and the terminating tool (e.g. `agentkit: terminating tool "finish" cannot be reached through wrapper "code_mode"`).

### Reachable Set Queries and Policy Resolution

4. **Transitive Reachable Tools Query API:**
   - `core.ReachableTools(tools []Tool) []Tool` returns the deduplicated transitive closure of all tools in `tools` plus all tools reachable through their `ReachableTools` hierarchies, preserving the order of first appearance.
   - `(*Agent) ReachableTools() []core.Tool` resolves the agent's current `ToolPolicy` and returns `core.ReachableTools(resolvedTools)`.

5. **Tool Policy Resolution Across Wrappers:**
   - `ToolPolicy.Resolve(registered []Tool) []Tool` applies policy filtering (`NoTools`, `ToolNames`, `ExcludeTools`) recursively to the `ReachableTools` of every wrapper tool.
   - If a tool originally declared one or more reachable tools (`len(original.ReachableTools) > 0`) and filtering leaves it with zero reachable tools (`len(filtered.ReachableTools) == 0`), the wrapper tool itself is dropped from the resolved list.
   - Leaf tools (tools that originally had no reachable tools) are not dropped due to empty reachable sets.

6. **Unguarded Shell Check Across Wrappers:**
   - In `execguard.go`, `checkExecuteGuard` checks all transitively reachable tools of the resolved tool set.
   - If `cfg.BeforeToolCall` is nil and any wrapper reaches a shell tool (`guard.IsShellTool`), execution fails before the first provider request with `core.ErrUnguardedExecute` naming both the wrapper and the shell tool (e.g. `agentkit: a shell tool is registered but AgentConfig.BeforeToolCall is nil; wrapper "code_mode" reached shell tool "execute"`).

### Nested Execution Pipeline and Interception

7. **Nested Caller Context and API:**
   - `core` defines the nested execution interface and context propagation:
     - `type NestedCaller interface { Call(ctx context.Context, calls ...ToolUseBlock) ([]ToolResult, error) }`
     - `func WithNestedCaller(ctx context.Context, caller NestedCaller) context.Context`
     - `func CallNested(ctx context.Context, calls ...ToolUseBlock) ([]ToolResult, error)`
   - When `executeBatch` invokes any tool that has `len(t.ReachableTools) > 0`, it constructs a nested execution dispatcher bound to the parent tool-use ID, parent tool name, and resolved reachable tools, and attaches it to `ctx` via `WithNestedCaller`.
   - If a tool calls `CallNested` when no dispatcher is attached to `ctx`, it returns `core.ErrNoNestedCaller`.
   - If a wrapper attempts to call a tool name not present in its resolved reachable tools, the call fails inline with error code `"unknown_tool"`.

8. **Interceptor Isolation and Semantics:**
   - `core.BeforeToolCallContext` and `core.AfterToolCallContext` gain:
     - `ParentToolUseID string`
     - `ParentToolName string`
     (both empty for direct calls, populated for nested calls).
   - If `BeforeToolCall` returns `dec.Block == true`:
     - The nested call returns a `ToolResult{OK: false, Error: "blocked_by_policy", Detail: reason}` to the caller.
     - The nested block does not return a Go error from `CallNested` and does not abort the wrapper.
     - If `dec.Terminate == true` on the blocked nested call, execution of the wrapper is terminated, `CallNested` returns `core.ErrTerminated`, and the wrapper result carries `Terminate = true` to the batch.
   - If `BeforeToolCall` returns `dec.Arguments != nil`, the arguments for the nested call are rewritten before validation and execution.
   - If an unmarked tool handler returns `ToolResult{Terminate: true}` during a nested call:
     - The terminate vote is ignored (`res.Terminate = false`). The run does not end.
     - The nested call's audit record sets `TerminateIgnored = true`.
     - The nested result and wrapper result annotate in their details that the terminate vote was ignored.

### Concurrency, Cancellation, and Telemetry

9. **Nested Concurrency and Execution Modes:**
   - Calls passed together to `CallNested(ctx, calls...)` run concurrently using goroutines joined by `sync.WaitGroup`, unless agent parallel execution is disabled or any invoked tool has `ExecutionMode == core.Sequential`, in which case they run sequentially.
   - Results are returned in the exact slice order corresponding to the input `calls`.
   - Failure of one nested call does not cancel sibling nested calls in the same call group.
   - Cancelling the context aborts in-flight nested calls, returning `ToolResult{OK: false, Error: "aborted", Detail: "Operation aborted"}`.

10. **Events, Audit, Tracing, and Log Isolation:**
    - Each nested call is assigned a unique tool-use ID.
    - `core.ToolExecutionStartEvent`, `core.ToolExecutionUpdateEvent`, and `core.ToolExecutionEndEvent` gain `ParentToolUseID string` (marshalled with `omitempty`). Nested calls emit these execution events with `ParentToolUseID` populated.
    - Nested calls do not emit `core.ToolResultEvent` to the event stream.
    - `core.AuditEvent` gains `ParentToolUseID string` and `TerminateIgnored bool`. Each nested call is audited with its parent tool-use ID, argument hash, error status, and elapsed duration.
    - Each nested call executes within a tracer span (`"agentkit.tool_call"`) recording `tool_name`, `tool_use_id`, `parent_tool_use_id`, and status.
    - Nested calls preserve `core.WithUsageReporter(ctx, a.addOffLoopUsage)` so any subagent spend inside a nested call is credited to the agent immediately.
    - The durable session store (`SessionStore`), conversation history (`ConversationHistory`), and turn stop policies (`stop.WhenToolCalled`) record and evaluate only the parent wrapper call and its result, completely ignoring nested calls.

## Design Decisions

1. **`ReachableTools` is declared as `[]Tool` on `core.Tool`.** Storing full tool objects rather than bare name strings preserves tool schemas, execution modes, and handlers within wrapper configurations, allowing wrappers to operate without querying a global registry.
2. **Cycle detection and terminating tool checks run at agent construction.** Failing fast at `NewAgent` and `RegisterTool` prevents invalid execution trees, deadlocks, and illegal termination bypasses from manifesting as subtle runtime errors mid-run.
3. **`core.ReachableTools` returns a deduplicated transitive slice.** Downstream consumers (including agent-fox's `AssertReadOnly` and security verifiers) require a single canonical representation of all reachable tools regardless of nesting depth.
4. **Empty wrappers are pruned during policy resolution.** If all reachable tools of a wrapper are excluded by `NoTools`, `ToolNames`, or `ExcludeTools`, leaving the wrapper in the resolved set would present the model with a dead tool; dropping it keeps the model prompt clean.
5. **Unguarded shell check fails before the first request.** Reaching a shell tool through a wrapper without an interceptor creates the exact security loophole `core.ErrUnguardedExecute` exists to prevent; inspecting reachable tools transitively closes this gap.
6. **Nested execution dispatch is provided via context.** Injecting `NestedCaller` into `context.Context` during wrapper execution allows tool implementations (and future script runners) to dispatch nested calls through the standard core pipeline without tight coupling between packages.
7. **Blocked nested calls return error results, not Go errors.** Returning `ToolResult{OK: false, Error: "blocked_by_policy"}` matches how direct blocked calls are returned to callers, allowing wrappers to inspect the failure and handle policy rejections gracefully.
8. **Interceptor terminate votes terminate wrappers; unmarked handler terminate votes are ignored.** Interceptors represent the authoritative embedder security policy and must be able to halt execution; tool handlers are untrusted and should only terminate runs if explicitly declared as top-level terminating tools.
9. **Nested calls omit `ToolResultEvent` and session log records.** The session log must reflect only the conversational messages exchanged with the model; execution events (`ToolExecution*`) and audit logs provide the complete record for monitoring and debugging without polluting resume transcripts.

## Dependencies

| Spec | Why this spec depends on or modifies it |
|---|---|
| `06_tool_output_schemas` | Builds on `OutputSchema` on `core.Tool` to ensure wrapper tools and nested execution observe typed structured outputs. |
| `04_runner_and_tool_metadata` | Uses shell tool metadata definitions and `guard.IsShellTool` for transitive unguarded shell checks. |
