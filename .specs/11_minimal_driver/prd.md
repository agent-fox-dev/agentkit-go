---
spec_id: "11"
spec_name: "minimal_driver"
title: "Streamlined Driver Loop with Prefix Caching Breakpoints, Context Pruning, and Batch Tool Execution"
status: "active"
created_at: "2026-10-09T11:40:04.652932Z"
updated_at: "2026-10-09T11:40:04.652932Z"
intent_hash: "003f33cd88d55dbef4932cb8bf5591196030407aa3080b5e519914e291bc9649"
schema_version: 2
source: "docs/prd/10-cut-agentkit-down-to-the-hands.md"
---
## Intent

Implement the streamlined `agentkit.Config` and `New` driver loop with prompt prefix caching breakpoints, transcript tool result pruning, batch tool execution, and termination handling, replacing legacy session recording, steering queues, compaction round trips, and multi-provider registry complexity with a focused, robust driver.

## Goals

1. Replace `core.AgentConfig` (31 fields) and legacy constructors (`NewAgent`, `NewAgentWithHistory`) with a streamlined `agentkit.Config` (14-15 fields) and `agentkit.New(cfg Config) (*Agent, error)`.
2. Provide a single consumer-facing agent execution surface: `Run(ctx, prompt) (core.RunResult, error)`, `Stream(ctx, prompt) (*core.EventStream, error)`, `Messages() core.Messages`, `Usage() core.Usage`, and `ReachableTools() []core.Tool`.
3. Support prompt caching prefix messages via `Config.Prefix []core.Message`, sent after the system prompt on every request, with cache breakpoints applied to the last system block, last tool definition, last prefix block, and rolling last user message block.
4. Implement context window pruning via `Config.Prune PruneOptions`: when estimated context tokens reach `Threshold` fraction of the model context window, replace tool result bodies older than `KeepTurns` in the outbound view with a single line naming the tool and original size in bytes while preserving `ToolUseID` pairing and leaving the transcript on `Messages()` intact.
5. End runs with `RunStopError` naming the context window if the transcript size exceeds the model context window after pruning.
6. Preserve the batch execution pipeline: sequential prepare/validation, abort decision evaluated once before handlers start, concurrent handler execution without `errgroup`, and serialization under a batch-scoped finalize mutex.
7. Preserve the nested tool execution pipeline (spec 07): cycle detection, wrapper reachability resolution, shell tool guard detection across wrappers, context-attached `NestedCaller`, error results on blocked nested calls, and concurrent nested execution.
8. Enforce single synthetic result handling on `max_tokens` (`StopReasonLength`): do not execute any tool calls in the batch; respond to each with the exact fixed notice: `Tool call %q was not executed: the response hit the output token limit,\nso its arguments may be truncated. Re-issue the tool call with complete arguments.`
9. Enforce run stop conditions and reasons: `RunStopToolTerminate` (when an executed tool result votes `Terminate: true`), `RunStopMaxTurns`, `RunStopBudgetExceeded` (stopped before issuing a request that exceeds `MaxCostUSD`), `RunStopTimeout`, `RunStopAborted`, `RunStopRefusal`, and `RunStopError`.
10. Eagerly validate reachable shell tools at construction time in `New`: return `core.ErrUnguardedExecute` if any shell tool is reachable (directly or through wrappers) and `Config.Guard` is nil.
11. Verify all driver mechanisms through unit tests driven by `provider/faux`.

## Non-goals

- Multi-provider registries, discovery, or runtime provider switching (`ProviderRegistry`, `DefaultProviders`, `SetModel`).
- Interactive agent features: mid-run steering queues (`Steer`, `SteerText`), follow-up queues (`FollowUp`), turn continuation (`Continue`), or session pausing/holding.
- Resuming transcripts from disk or session recorders (`session.Recorder`, `resume.go`).
- Multi-turn history trees, snapshot branching, or rollback (`ConversationHistory`, `SnapshotBranch`).
- Model-based summarization round trips (`compaction.ModelSummarizer`, `compaction.NewContextTransform`).
- Middleware chains, rate limiters, or external retry wrappers (`middleware.Chain`, `middleware.Retry`).
- Forced tool choice (`tool_choice: "any"` or forced tool locks).
- Vocabulary shrinking in `core`, ordered schema parsing in `schema.Parse`, and pure-function `guard.Check` (deferred to scope 4: `core_schema_guard`).
- Documentation overhaul across `README.md`, `architecture.md`, and `configuration.md` (deferred to scope 4: `core_schema_guard`).

## Background

AgentKit was originally designed with an expansive, general-purpose agent loop supporting multiple providers, interactive steering queues, follow-up queues, session persistence, compaction summarization, and middleware chains (`agent.go`, `loop.go`, `batch.go`). This architecture grew to thousands of lines of synchronization logic, atomic states, and hooks.

In practice, AgentKit's sole consumer (`agent-fox`) runs unattended coding pipelines where each phase runs a fresh prompt to a stop condition, writes its own durable artifacts to git, and executes against Claude models. Scope 1 (`09_repository_cut`) pruned obsolete provider backends, tools, and dead packages (`compaction`, `session`, `middleware`, `stop`). Scope 2 (`10_anthropic_wire`) moved the Anthropic wire to the official SDK, introduced unified credential resolution (`anthropic.Resolve`), streamlined `catalog.Lookup`, and added strict tool definitions.

What survives of the loop is the part that is correct and not in vendor tool runners:
1. Batch tool execution semantics with concurrency and slot-indexed results.
2. The nested-call pipeline that Starlark code mode (`codemode`) requires.
3. Strict bounds: maximum turns, maximum cost in USD, and timeouts.
4. Ephemeral cache breakpoints on system, tools, prefix, and user messages.
5. Aging out old tool results via transcript pruning without modifying the durable history.

This spec implements that minimal driver loop in `agentkit`.

## Requirements

### Driver Configuration and Constructor

1. The package shall define `Config`:
   ```go
   type Config struct {
       Client     *anthropic.Client   // built by anthropic.Resolve or caller
       Provider   core.ProviderClient // double or custom provider; takes precedence if set
       Model      string
       Effort     Effort              // low | medium | high | xhigh | max
       System     string
       Prefix     []core.Message      // sent after system on every request, with a cache breakpoint
       Tools      []core.Tool
       Policy     core.ToolPolicy     // resolved through wrappers
       Guard      core.BeforeToolCall // required when any shell tool is reachable
       After      core.AfterToolCall
       MaxTurns   int
       MaxCostUSD float64
       Timeout    time.Duration
       Prune      PruneOptions        // zero value: off
       MaxTokens  int
   }
   ```
2. `New(cfg Config) (*Agent, error)` shall construct and validate the agent instance:
   - If both `cfg.Client` and `cfg.Provider` are nil, `New` shall return an error indicating that a client or provider must be supplied.
   - If `cfg.Model` is empty, `New` shall return an error indicating that a model identifier is required.
   - It shall validate the tool hierarchy: check that each tool specifies exactly one of `Handler` or `Execute`, detect reachability cycles, and reject wrappers reaching `Terminating` tools.
   - It shall resolve active tools using `cfg.Policy.Resolve(cfg.Tools)`.
   - If any resolved tool or transitively reachable tool (including tools reachable via code mode or wrappers) is a shell tool (as determined by `guard.IsShellTool`) and `cfg.Guard` is nil, `New` shall return `fmt.Errorf("%w (tool %q)", core.ErrUnguardedExecute, toolName)`.
3. An `Agent` shall support concurrent read of its lifetime stats and active tools, but shall reject overlapping calls to `Run` or `Stream` on the same instance with `core.ErrBusy`.
4. The public surface of `Agent` shall consist solely of:
   - `func (a *Agent) Run(ctx context.Context, prompt string) (core.RunResult, error)`
   - `func (a *Agent) Stream(ctx context.Context, prompt string) (*core.EventStream, error)`
   - `func (a *Agent) Messages() core.Messages`
   - `func (a *Agent) Usage() core.Usage`
   - `func (a *Agent) ReachableTools() []core.Tool`

### Prompt Assembly and Cache Breakpoints

5. On every request, the system prompt sent to the provider shall be assembled via `prompt.Build(cfg.System, resolvedTools)`, which combines the base system prompt with per-tool `PromptGuidelines` deduplicated in first-seen order.
6. The outbound messages sent to the provider shall prepend `cfg.Prefix` before the run's prompt and subsequent conversation turns.
7. Ephemeral cache control headers (`"cache_control": {"type": "ephemeral"}`) shall be maintained across:
   - The final content block of the system prompt.
   - The final tool definition in the tool list.
   - The final content block of the prefix messages (if `cfg.Prefix` is non-empty).
   - The final content block of the user message history.

### Context Pruning (`Prune`)

8. The package shall define `PruneOptions`:
   ```go
   type PruneOptions struct {
       Threshold float64 // fraction of context window (e.g. 0.35); <= 0 disables pruning
       KeepTurns int     // number of recent turns whose tool results are kept unpruned
   }
   ```
9. When `cfg.Prune.Threshold > 0`, before issuing each outbound model request:
   - The driver shall estimate the token count of the outbound view using anchored token estimation (latest reported assistant usage + 4 chars/token for subsequent messages, falling back to 4 chars/token over the entire view if unanchored).
   - If estimated tokens divided by the model's context window (retrieved via `catalog.Lookup(cfg.Model)`) meets or exceeds `cfg.Prune.Threshold`:
     - Tool results in the outbound view from turns older than `currentTurn - cfg.Prune.KeepTurns` shall have their content replaced with a single notice:
       `fmt.Sprintf("[result of %s (%d bytes) elided; call again if needed]", result.ToolName, originalContentBytes)`
     - Pairing between `ToolResultMessage.ToolUseID` and assistant `ToolUseBlock.ID` shall be strictly preserved.
     - The durable transcript returned by `a.Messages()` and `RunResult.Messages` shall remain intact and unmodified.
10. If, after pruning, the estimated transcript tokens exceed the model's context window, the driver shall immediately end the run with `RunStopError` returning an error naming the model and context window capacity.

### Turn Iteration and Synthetic Truncation

11. Turn continuation shall be determined exclusively by the presence of `ToolUseBlock` elements in the assistant message, never by `StopReason`.
12. If an assistant message contains one or more `ToolUseBlock` elements and its `StopReason` is `StopReasonLength` (output token limit reached):
    - No tool handlers in the batch shall be executed.
    - Each tool call in the message shall receive a synthetic `ToolResultMessage` with content:
      `fmt.Sprintf("Tool call %q was not executed: the response hit the output token limit,\nso its arguments may be truncated. Re-issue the tool call with complete arguments.", call.Name)`
    - The synthetic results shall be appended to the transcript and the driver shall continue to the next turn.
13. If an assistant message contains no `ToolUseBlock` elements and `StopReason` is `StopReasonRefusal`:
    - The driver shall end the run with `RunStopRefusal` and an error wrapping `core.ErrRefusal`.
14. If an assistant message contains no `ToolUseBlock` elements and no error or refusal, the run shall end normally with `RunStopEndTurn`.

### Batch Tool Execution

15. When an assistant message contains `ToolUseBlock` elements (and output tokens were not truncated), the driver shall execute them via `executeBatch`:
    - **Phase 1 (Prepare):** Sequential execution on the loop goroutine. Each call is validated against its tool schema via `core.PrepareArguments`. `Config.Guard` (`BeforeToolCall`) is evaluated. A blocked call or validation error is finalized immediately as an error result without running the handler.
    - **Phase 2 (Abort Decision):** Evaluated once on the loop goroutine before any handler goroutine starts. If `ctx.Err() != nil`, all remaining unfinalized calls are marked aborted with `abortedResult` and zero handlers execute. If `ctx.Err() == nil`, all remaining handlers are executed.
    - **Phase 3 (Execute):** Handlers run concurrently, one goroutine per call, joined by `sync.WaitGroup` (never `errgroup`). If any tool in the batch is marked `Sequential` or `cfg.ParallelTools` is false, handlers run sequentially in call order.
    - **Phase 4 (Finalize):** Finalization, `Config.After` (`AfterToolCall`), and event emission run serialized under a batch-scoped mutex. Results are placed by original slot index so transcript order matches assistant call order regardless of execution finish order.
16. If any executed tool result in the batch has `Terminate: true`:
    - The batch shall finish execution completely.
    - The run shall terminate after the batch with `RunStopToolTerminate`.
    - A blocked or rejected call shall not cause termination.

### Nested Tool Execution (Spec 07 Seam)

17. When a tool with `ReachableTools` is executed, the driver shall attach a `core.NestedCaller` to the handler's context.
18. Calls made through `NestedCaller.Call` shall:
    - Only permit tools in the wrapper's resolved reachable set.
    - Execute validation and `BeforeToolCall` interceptors.
    - Emit execution events carrying `ParentToolUseID`.
    - Return an error `ToolResult` if a nested call is blocked by an interceptor, without returning a Go error to the caller, allowing wrapper scripts (such as Starlark code mode) to handle the error value and complete.
    - Terminate the wrapper if an interceptor votes to terminate during a nested call.

### Run Limits and Bounds

19. `Config.Timeout`: If `Timeout > 0`, the driver shall derive the run context with `context.WithTimeout`. If the deadline expires, the run shall terminate with `RunStopTimeout`.
20. `Config.MaxTurns`: If `MaxTurns > 0` and the turn count reaches `MaxTurns`, the run shall terminate with `RunStopMaxTurns`.
21. `Config.MaxCostUSD`: If `MaxCostUSD > 0`:
    - Before issuing each model request, if accumulated cost meets or exceeds `MaxCostUSD`, the driver shall terminate before the request with `RunStopBudgetExceeded`.
22. Panic containment: Any panic in a tool handler, interceptor, or hook shall be caught, converted to an error result, and reported on the event stream without crashing the process.
23. The event stream (`*core.EventStream`) returned by `Stream` shall never block the producer on slow or detached consumers.

## Design Decisions

1. **Provide both `Client *anthropic.Client` and `Provider core.ProviderClient` on `Config`:** The primary driver wire uses the official Anthropic SDK client from spec 10, but unit tests and custom-vendor embedders require injecting `provider/faux` or custom doubles. Providing an optional `Provider` field allows clean test injection without mock HTTP servers.
2. **Anchored context token estimation for pruning:** Without an external tokenization dependency (prohibited by repo steering), transcript size is estimated using the latest assistant message's reported usage as an anchor plus 4 characters per token for subsequent messages, falling back to 4 chars/token heuristic.
3. **Outbound-only pruning with intact `Messages()` transcript:** Pruning replaces result text only in the slice sent to the model API. Keeping the underlying transcript on `Messages()` intact ensures embedders retain the true, unmodified log of operations and outputs.
4. **Error termination if transcript exceeds context window post-pruning:** Rather than making a model API call that is guaranteed to fail with an HTTP 400 context length error, the driver fails fast with `RunStopError` naming the context window limit.
5. **Phase 2 single abort decision in batch execution:** Checking `ctx.Err()` once on the loop goroutine before handlers spawn ensures deterministic batch behavior: cancellation mid-batch results in either 0 or N handlers running, never a partial split.
6. **Synthetic tool results for `StopReasonLength` without handler execution:** Modern Claude models that truncate outputs may produce partial tool call arguments. Executing partial arguments causes subtle bugs; generating the synthetic retry text gives the model a chance to re-issue the call cleanly.
7. **Eager shell tool guard check at construction time in `New`:** Unrestricted shell access is a critical safety hazard. Checking directly in `New` (including tools reachable via code mode or wrappers) prevents accidental unguarded execution before any network call or event stream starts.

## Dependencies

| Spec | Status | Reason |
|---|---|---|
| `09_repository_cut` | active | Removes obsolete packages (`compaction`, `session`, `middleware`, `stop`), cleans up dead tools, and renames module. |
| `10_anthropic_wire` | active | Rewrites `provider/anthropic` over official SDK, provides `anthropic.Resolve`, Claude `catalog.Lookup`, `Effort`, strict tool declarations, and cache breakpoints. |
| `07_nested_tool_calls` | active | Provides `ReachableTools`, `NestedCaller`, and wrapper resolution semantics for batch execution. |
| `08_code_mode` | active | Provides Starlark code mode tool which executes nested tool calls and depends on interceptor error values. |

## Verified External API

The driver calls the official Anthropic Go SDK client introduced in spec 10 (`github.com/anthropics/anthropic-sdk-go`).

### `github.com/anthropics/anthropic-sdk-go` (unverified)
```go
package anthropic

type Client struct {
    Messages *MessageService
}

type MessageService struct {}
func (s *MessageService) New(ctx context.Context, params MessageNewParams, opts ...option.RequestOption) (*Message, error)
func (s *MessageService) NewStreaming(ctx context.Context, params MessageNewParams, opts ...option.RequestOption) *ssestream.Stream[MessageStreamEvent]
```
