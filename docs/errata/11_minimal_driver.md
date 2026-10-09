# Erratum: spec 11 `minimal_driver`

Where the delivered driver differs from `.specs/11_minimal_driver`, and why.

## The test pseudocode names types the code does not have

The tests follow the code's vocabulary:

- `core.Tool.ReachableTools` holds `[]core.Tool` values, not names. A
  `Handler` returns `(json.RawMessage, error)`; an `Execute` returns a
  `core.ToolResult`, whose rendered text is `Text`, not `Content`.
- `Config.Guard` is a `core.BeforeToolCall`: it takes a
  `core.BeforeToolCallContext` and returns a `core.BeforeToolCallDecision`
  (`Block`, `Terminate`, `Reason`). There is no `core.ToolCall`,
  `BeforeToolCallResult` or `ToolCallActionReject`; a rejection is
  `Block: true`.
- `RunResult.Turns` is `RunResult.TurnCount`; `Tool.Sequential` is
  `ExecutionMode: core.Sequential`; `ToolStartEvent` is
  `ToolExecutionStartEvent`; `core.UserMessage` is a struct, not a
  constructor.
- The spec's `bash` is not a shell tool to `guard.IsShellTool`
  (`guard.ShellToolNames` is `execute`, `run_command`, `powershell`), so
  TS-11-7 puts `run_command` behind the wrapper.

## 11-REQ-1: `Config` and `New`

- `Config.Client` is the SDK's `*anthropic.Client`
  (`github.com/anthropics/anthropic-sdk-go`); the driver reaches it through
  `provider/anthropic`, so the root package imports the SDK.
- `Effort` is `core.Effort` re-exported (`agentkit.Effort`,
  `agentkit.EffortLow` … `EffortMax`).
- `New` checks the tool hierarchy of `Config.Tools` and of what
  `Config.Policy` resolves (the policy may name tools of its own), and
  resolves the policy once: the tool set is fixed for the agent's life.

## 11-REQ-3: what the cut removed

The Agent's surface is `Run`, `Stream`, `Messages`, `Usage` and
`ReachableTools` (`agent.go`, `loop.go`; TS-11-9). Removed with the old
surface: `NewAgent`, `NewAgentWithHistory`, `RegisterTool`, `Tools`,
`SetPromptBlocks`/`PromptBlocks`, `SetModel`, `SetEffort`, `SetStopPolicy`,
`Config`, `ResolvedModel`, `History`, `Snapshot`, `Phase`, `Idle`, `Hold`,
`Abort`, the steering and follow-up queues, `RunMessage`,
`DefaultProviders`/`RegisterDefaults` (`providers.go`), hooks, middleware,
context transforms, stop policies and deferred responses. `core.AgentConfig`
itself stays in `core`: shrinking that vocabulary is deferred to scope 4.

- **Observation is the event stream.** With no `Hooks`, an error the run
  survives (a panicking interceptor or provider) is a `core.ErrorEvent` on
  the run's stream; a run that ends in error also ends its stream with one.
- **Parallel by default.** `Config` has no `ParallelTools`, so a batch runs
  concurrently unless a `Sequential` tool is in it; the spec's
  "`cfg.ParallelTools` is false" case has no field to set.
- **The transcript spans runs.** `Messages()` is every message of every run;
  a second `Run` continues it. `RunResult.Messages` is that run's messages.
- **No `Agent.Abort`.** A run is stopped through its context.
  `TestCallerCancellationAbortsTheRun` replaces the test that told `Abort`
  from a caller cancellation.

Tests of removed features were deleted rather than ported: steering and
follow-up, snapshots and holds, `SetModel`, stop policies (among them
TS-07-34, a stop policy watching nested calls), context transforms,
middleware, hooks and deferred responses. The tests of behaviour that
survives were ported to `New`.

## 11-REQ-4: the prefix travels beside the history, and `prompt.Build` changed

- **`Config.Prefix` is `core.Request.Prefix`.** The loop sets the request's
  `Prefix` (spec 10 added the field and its encoding) rather than splicing the
  prefix into `Messages`, so TS-11-15 asserts `req.Prefix` and then that the
  prompt is `req.Messages[0]`. The prefix is never recorded in the
  transcript.
- **Breakpoints are the wire's.** `core.Request` blocks carry no cache
  control; `provider/anthropic` stamps the four breakpoints when it encodes
  the request. TS-11-16 encodes the request the faux provider captured with
  `anthropic.BuildRequestJSON` and inspects the body.
- **`prompt.Build(system, tools)`** replaces `prompt.Build(prompt.Input)`.
  `Input.ExtraBlocks` is gone with `AgentConfig.PromptBlocks`; an embedder's
  extra text belongs in `Config.System`. The custom-prompt golden
  (`prompt/testdata/golden/system_prompt_custom.txt`) lost its project-context
  block, and the assertion that the block survives a custom prompt was
  removed with it.
- TS-11-14's deduplication, and the breakpoints on the system prompt, tools
  and last user block, were in place before this task; only the prefix
  assertions failed first.

## 11-REQ-5: pruning and the context window

- **The window check is not conditional on `Prune`.** 11-REQ-5.5 reads "after
  pruning"; the driver checks the estimate before every request, pruned or
  not (`outboundView` in `loop.go`), since a request larger than the window
  fails at the API either way.
- **What the estimate counts.** The anchor is the latest assistant message
  whose usage reports a context size (`Usage.ContextTokens`). The 4
  characters per token are counted over each message's JSON form, and the
  unanchored fallback counts `Config.Prefix` with the transcript. After
  pruning, the anchored estimate is lowered by the elided bytes that the
  anchor's usage had counted.
- **Turns.** A turn is an assistant message and the results that answer it;
  with N assistant messages in the view, results of turns before
  `N - KeepTurns` are elided. "Original bytes" is the length of the result's
  text.
- TS-11-17 checks declarations from task 1 and passed before this task.

## 11-REQ-6: already the loop's behaviour

Turn continuation on `tool_use` presence, the truncation notice, the refusal
stop and the normal end were in place before spec 11 (`runLoop`,
`synthesizeTruncated` in `loop.go`). TS-11-22..25 passed when written; each
was then seen to fail against a mutated loop (continuation gated on the stop
reason, truncation not detected, refusal not mapped).

## 11-REQ-8: termination is any executed vote, and the Guard keeps its own

- **Any, not all.** The batch used to end the run only when every call voted
  `Terminate` (`core.BatchTerminates`, an AND). 11-REQ-8.1 ends it when any
  executed call votes, after the whole batch finishes (`executeBatch` in
  `batch.go`; TS-11-31). The tests that pinned the AND
  (`TestBatchTerminationIsAnAndNotAnOr`, TS-04-46's batch test) now pin
  this. `core.BatchTerminates` is no longer used by the driver and stays in
  `core` until scope 4 shrinks it.
- **A blocked call has no vote of its own** (11-REQ-8.3, TS-11-32): its tool
  never ran. The Guard's own `Terminate`, set beside `Block`, still ends the
  run, as a nested interceptor's does (11-REQ-9.5) and as
  `guard.Options.TerminateOnBlock` documents. Reading 11-REQ-8.3 as dropping
  that vote too would make `TerminateOnBlock` a no-op for direct calls while
  it still worked for nested ones.
- TS-11-26..30 and TS-11-32 passed before this task: the four phases, the
  single abort decision and slot order were already the batch's behaviour.
  Only TS-11-31 failed first.

## 11-REQ-9: the nested pipeline is spec 07's

`nested.go` already attached the caller, enforced the reachable set,
stamped `ParentToolUseID`, returned a block as a result and failed later
calls with `core.ErrTerminated` after an interceptor's terminate vote.
TS-11-33..37 passed when written, and each was seen to fail against a
mutated `nested.go`. Two details follow the code rather than the
pseudocode: there is no `NestedCallerFromContext` or `Allows`, so TS-11-33
calls the reachable tools through `core.CallNested`; and an unreachable
tool is the `unknown_tool` result (`wrapper cannot call "forbiddenTool"`),
not a result saying "unreachable". A terminate vote counts beside `Block`,
as for a direct call (TS-11-37's interceptor blocks and votes).

## 11-REQ-10: bounds, and what the driver adds to `core`

- **`core.RunStopTimeout`** is new (`core/stopreason.go`): `core` had no
  reason for a run that outlived its deadline. The run context carries
  `errRunTimeout` as its cause (`context.WithTimeoutCause`), which is how
  the driver tells its own `Timeout` from a caller's deadline; the error
  wraps `context.DeadlineExceeded`.
- **An aborted run's error wraps both** `core.ErrAborted` and the context's
  error (`context.Canceled` or `context.DeadlineExceeded`), so a caller can
  test either.
- **Pricing.** `MaxCostUSD` reads the run's `Usage.CostUSD`. A provider that
  does not price its usage (`provider/faux`, a custom one) is priced by the
  driver at the catalog row (`provider.ComputeCost`); the Anthropic
  provider already prices its own. An id the catalog does not list has no
  price, so the budget never trips for it — TS-11-40 and TS-11-49 use the
  listed `claude-opus-5-5` rather than the spec's `claude-3-5-sonnet`.
- **Panics are reported.** A panicking handler was already an error result;
  it is now also a `core.ErrorEvent`. The result's text is
  `tool "NAME" panicked: …`, so TS-11-42 checks for
  `panicked: unexpected explosion` rather than `panic: unexpected explosion`.
- TS-11-43 passed before this task: `core.EventStream` never blocks its
  producer.
