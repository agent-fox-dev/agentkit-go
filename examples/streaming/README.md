# streaming — render a run live, and stop it from outside

`agent.Run` blocks and hands you the finished result. `agent.Stream` returns at
once with a channel of events — text deltas, thinking deltas, tool calls, tool
results, turn boundaries — so a UI can show the run as it happens. This example
prints a tool-using run token by token and lets Ctrl-C abort it cleanly.

Two things are easy to get wrong, and they are why the example exists:

- **Events come in two classes.** *Incremental* events (`TextDeltaEvent`,
  `ThinkingDeltaEvent`) are an optimisation — they may be coalesced, and a
  non-streaming provider sends none at all. *Authoritative* events
  (`TextEndEvent`, `TurnEndEvent`, …) are complete and final for the item they
  name. When the authoritative event arrives, **replace** what you
  accumulated from deltas; applying both double-counts.
- **Abort is out-of-band.** `agent.Abort()` takes no context, so a signal
  handler, RPC handler or UI loop that does not own the `Stream` goroutine can
  stop the turn. It is idempotent and a no-op when idle.

## Run it

Needs a credential for the model's vendor (default `anthropic/claude-sonnet-5`):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/streaming "Write a haiku about mutexes, then count its syllables."
```

No flags; `AGENTKIT_MODEL` overrides the model. Press **Ctrl-C** mid-run: the
run stops at the next checkpoint, the partial answer stays in the transcript,
and the program exits 0.

## What you'll see

Text on stdout as it streams, tool activity inline, turn summaries on stderr.
The shape, from the `switch` in `run()`:

```
<haiku text, streamed>
[model is calling count_syllables]
[count_syllables finished: ok, 0ms]
[turn 0: tool_use, <n> output tokens]
...
[done: end_turn · 2 turns · $<cost>]
```

After Ctrl-C you get `[interrupt: stopping at the next checkpoint]` and
`[aborted after N turns; $… spent]`. Without a credential:

```
error: no credential for vendor "anthropic": set one of ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, ...
```

## Walkthrough

- **The abort goroutine** (`signal.Notify` … `agent.Abort()` in `run()`): the
  whole pattern for out-of-band cancellation is five lines.
- **`agent.Stream(ctx, prompt)`** returns immediately. The producer never
  blocks on the consumer, so a slow terminal cannot stall the model call or
  trip the provider's idle timeout.
- **The event switch**: `TextDeltaEvent` is printed and appended to
  `textSoFar`; `TextEndEvent` resets `textSoFar` and takes `e.Text` whole.
  Copy that pairing. `ToolCallStartEvent`, `ToolExecutionEndEvent` (with
  `IsError`, `ElapsedMS`), `TurnEndEvent` and `ErrorEvent` round it out.
- **`stream.RunResult()`** is fed by the terminal event, not by consumption —
  a caller that reads no events can still get the result.
- **Abort is not a failure**: check `errors.Is(err, core.ErrAborted)` (or
  `context.Canceled`) and treat it as a normal outcome; `res` still carries
  turn count and cost.
- **`syllableTool()`** uses `Execute` (returning `core.OKResult` /
  `core.ErrResult`) rather than `Handler`; see [`customtools`](../customtools)
  for when to pick which.

## Gotchas

- Never treat deltas as the source of truth — a provider without streaming
  emits only authoritative events.
- `Run`/`Stream` while a turn is in flight returns `ErrBusy`; use
  `Steer`/`FollowUp` to talk to a running turn (see
  [`interactive`](../interactive)).

## Related

Packages: `core` (event types, `ErrAborted`), the root `agentkit` package
(`Stream`, `Abort`), `schema`, `stop`. Setup is identical to [`chat`](../chat).
