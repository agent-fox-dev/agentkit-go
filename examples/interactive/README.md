# interactive — talking to an agent while it works

Most agent APIs are request/response: you send a prompt, wait, get an answer.
A real interactive client needs more — the user notices the agent heading the
wrong way and wants to redirect it *now*, or wants to queue the next question,
or stop it, or render its current state after reconnecting. Those controls
have to live inside the loop; a consumer cannot bolt them on from outside.

AgentKit's `Agent` exposes them as methods that are safe to call from any
goroutine while a turn is in flight:

| Method | Does |
|---|---|
| `SteerText(s)` | Inject a message into the *running* turn, delivered just before the next model request. |
| `FollowUpText(s)` | Queue a message for when the current work is exhausted; it continues the *same* run. |
| `Abort()` | Stop the turn at the next checkpoint, keeping what it produced. No context needed. |
| `Phase()`, `Idle()` | What the loop is doing right now; cheap, atomic, never blocks. |
| `Snapshot(ctx)` | A consistent view of the transcript, safe mid-turn. |

This example is a small REPL that wires each one to a command.

## Run it

Needs a credential for the model's vendor (default `anthropic/claude-sonnet-5`):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/interactive
```

No flags; `AGENTKIT_MODEL` overrides the model. Ask for something slow (the
`slow_work` tool sleeps 1–5 s per item), then type while it runs:

| Input | Effect |
|---|---|
| plain text, while busy | steer the running turn |
| plain text, while idle | start a new run |
| `/follow <text>` | queue a follow-up |
| `/abort` | stop the turn |
| `/phase` | print phase and idle state |
| `/snapshot` | print message count, revision, phase, producer |
| `/quit` | abort and exit |

## What you'll see

A session looks like this (the shape, from `consume` and the command switch):

```
> list every Go file here and summarise each one
[slow_work…]
just the loop files, skip the rest
[steering the running turn]
[slow_work done in 2003ms]
[turn 0 ended: tool_use]
/phase
[phase: <phase> · idle: false]
/snapshot
[snapshot: 4 messages · revision 7 · phase <phase> · complete: false]
[producer: <id>]
...
[end_turn · 3 turns · $<cost>] >
```

Without a credential it exits before reading any input:

```
error: no credential for vendor "anthropic": set one of ANTHROPIC_API_KEY, ...
```

## Walkthrough

1. **Queue modes** — `cfg.SteeringQueueMode` / `cfg.FollowUpQueueMode`:
   `core.QueueOneAtATime` (default; one message per drain point) or
   `core.QueueDrainAll`.
2. **One goroutine owns stdin for the whole program** (the `lines` channel).
   A REPL that only reads while idle can never deliver anything *into* a turn.
3. **The command switch** in `run()` calls `Abort`, `Phase`/`Idle`,
   `printSnapshot` and `FollowUpText` directly from the input goroutine.
4. **Steering** — `if !agent.Idle() { agent.SteerText(text) }`. Steering is
   drained at the head of the next iteration, never between an assistant
   message and its tool results.
5. **New runs** — `agent.Stream` on its own goroutine (`consume`), so input
   keeps flowing. If a turn is still finishing, `Stream` returns
   `core.ErrBusy` rather than queueing.
6. **`slowWorkTool`** honours `ctx.Done()`, so an abort cancels work in
   progress.

## Gotchas

- `Run`/`Stream` **fail** with `ErrBusy` while a turn is active; they never
  queue a new prompt. Decide in your code whether to retry, steer or reject.
- A snapshot taken while busy is *consistent*, not *complete*: the in-flight
  turn is not in it (`snap.Idle` says which).
- Compare `snap.ProducerID` before `snap.Revision`; revisions from different
  agents (or a restored one) are unordered.
- An abort is a normal outcome (`core.ErrAborted`); the run ends at a turn
  boundary with every tool result in place, so a session stays resumable.

## Related

Root package `agentkit` (`Agent.SteerText`, `FollowUpText`, `Abort`, `Phase`,
`Idle`, `Snapshot`), `core` (`QueueOneAtATime`, `QueueDrainAll`, `ErrBusy`,
`ErrAborted`). For streaming and abort alone, start with
[`streaming`](../streaming).
