# ADR 01: Keep the agent loop small and report through events

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken between 2026-05 and 2026-10)

## Context

The agent loop (`agentkit.New`, `Agent.Run`, the batch executor and the
nested caller) was built and then cut down over several specs: the driver
lost its hooks, middleware, stop policies and steering queues, the tool side
moved into a `Toolset` run by a `Runner`, and the providers moved behind
`Config.Provider`. Each spec was written against the code before it, and
where its text and the code disagreed the code kept what it did, or the
disagreement was settled once and recorded in an erratum. The errata are
gone; the settlements that still bind the code are here.

## Decision

**The event stream is the only observation.**

- An error a run survives (a panicking interceptor, handler or provider) is a
  `core.ErrorEvent` on the run's stream; how a run ended is on
  `AgentDoneEvent.Result`. The loop adds no terminal `ErrorEvent` of its own; a
  provider's failure event is forwarded as it came.
- The stream is `core.EventStream`'s default: unbounded, never dropping an
  event, never blocking the run. `Run` reads none of the events; they are held
  until it returns.
- A panicking handler is an error result whose text is
  `tool "NAME" panicked: …`, and also an `ErrorEvent`.
- Nothing emits `ToolExecutionUpdateEvent`: no tool streams partial output
  through the loop. Direct and nested calls emit start and end events; a
  nested call's events carry `ParentToolUseID`.

**A run is bounded by its context, and says which bound ended it.**

- There is no `Agent.Abort`; a run is stopped through its context. An
  aborted run's error wraps both `core.ErrAborted` and the context's error.
- `Config.Timeout` is the run context's cause (`context.WithTimeoutCause`),
  which is how the loop tells its own timeout (`core.RunStopTimeout`, an error
  wrapping `context.DeadlineExceeded`) from a caller's deadline.
- A run that ends on a failed turn returns an error whose text is the turn's
  `ErrorMessage` and which unwraps to the error the provider ended its stream
  with, so `errors.Is` finds the provider's sentinels through `Run`.
- `MaxCostUSD` reads the run's `Usage.CostUSD`. A provider that does not price
  its usage is priced by the loop at the resolved catalog row
  (`provider.ComputeCost`); a row with no price never trips the budget. The
  loop only ever raises `Usage.Unpriced`, never clears one a client set.

**The transcript and the request.**

- The transcript spans runs: `Messages()` is every message of every run, and a
  second `Run` continues it. `RunResult.Messages` is that run's messages.
- `Config.Prefix` travels on `core.Request.Prefix`, beside the history; it is
  never recorded in the transcript. Cache breakpoints are the wire's: the
  request carries none, and `provider/anthropic` stamps them when it encodes.
- The model is resolved once, in `New`, through `catalog.Select`; the request
  carries no model. The request's `MaxTokens` is `Config.MaxTokens`, or
  `DefaultMaxTokens`, and the provider caps it at the model's output cap. The
  loop prunes and refuses by the resolved row's context window.
- The window check runs before every request, pruned or not. The estimate is
  anchored at the latest assistant message whose usage reports a context size,
  plus what pruning took off each request since (so the threshold applies to
  the unpruned size and pruning stays on once it starts); unanchored, it counts
  4 characters per token over each message's JSON, the system prompt, the tool
  definitions and the prefix. A turn is an assistant message and the results
  that answer it; with N assistant messages in view, results of turns before
  `N - KeepTurns` are elided.

**The batch.**

- A batch runs concurrently unless a `Sequential` tool is in it.
- The run ends when any executed call votes `Terminate`, after the whole batch
  finishes. A blocked call has no vote of its own, but the Guard's `Terminate`
  beside `Block` ends the run, for direct and nested calls alike (so
  `guard.Options.TerminateOnBlock` works for both). A batch cancelled before
  its handlers start keeps the Guard's votes from preparation. Either way the
  run reports `RunStopToolTerminate`.
- The Guard's `TurnCount` is the number of turns completed before the turn
  whose calls are running, so a first-turn call sees 0.

**The tool side is a `Toolset`, run by a `Runner`.**

- `Tools`, `Policy`, `Guard` and `After` are fields of `agentkit.Toolset`;
  `Config` has one `Toolset` field. `New` checks the client and the model,
  then returns `NewRunner`'s error unchanged; `NewRunner` checks the tool
  hierarchy of the tools and of what the policy resolves, in that order, and
  the tool set is fixed for the runner's life.
- `(*Agent).Runner()` returns the runner the agent was built on. Calls made
  through it are outside every run: not in the transcript, not in `Usage`, no
  run slot.
- `Runner.Call` answers `aborted` only for a context already done before any
  handler starts; a running handler gets the cancelled context and reports its
  own outcome.

**Nested calls.**

- A call to a tool outside the wrapper's reachable set is an `unknown_tool`
  result (`<wrapper> cannot call "<name>": …`).
- `AfterToolCall` returning `Terminate` for a nested call ends the wrapper as
  a `BeforeToolCall` terminate vote does. Edits it makes to the result message
  do not reach the wrapper, which receives a `core.ToolResult`.

**A limit left open.** A reachability cycle that enters only through
`Policy.CustomTools` reaches `ToolPolicy.Resolve` unchecked, and
`pruneReachable` recurses on it until the stack overflows. A cycle in
`Toolset.Tools`, or in a non-nil `Policy.Tools`, is refused at construction.
Closing the gap is a change to `ToolPolicy.Resolve` or to the order of the
checks.

## Consequences

- An embedder observes a run through its events and its result, never
  through callbacks into the loop.
- A provider that never closes its stream hangs the run: the loop trusts a
  provider to honour its context.
- Where a spec and the loop disagree from here on, the code and this ADR say
  which won; no erratum is written (see `.specs/steering.md`).
