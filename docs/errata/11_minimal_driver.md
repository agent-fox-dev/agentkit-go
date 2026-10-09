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
