# agentdemo — the whole loop, with no key and no network

`agentdemo` runs the real AgentKit loop end to end against `provider/faux`, a
scripted provider that replays canned turns. Nothing is mocked except the
model: the loop, the tool executor, the wire encoders, transcript repair, the
session log and delegation are the shipped code.

Each of its seven sections demonstrates one behaviour that a naive agent loop
gets wrong — and that the original specification got wrong before it was
corrected. Run it first if you want to see the machinery without spending
anything, then read each `demo*` function next to its output.

## Run it

No API key, no network:

```bash
go run ./examples/agentdemo
```

There are no flags. The output uses ANSI colours.

## What you'll see

```
── 1. The loop, streamed ─────────────────────────────────────────
Let me count those.  → calling word_count (call_1)
  ← {"data":{"count":9},"ok":true}
  [turn 0 ended: tool_use]
That sentence has 9 words.
  [turn 1 ended: stop]

final: "That sentence has 9 words."
turns: 2, stop: end_turn

── 2. Why the loop ignores stop_reason (REQ-LOOP-01) ─────────────
  stop_reason tool_use       → tool executed: true
  stop_reason stop           → tool executed: true
  stop_reason (none)         → tool executed: true
...
── 5. Repairing what a killed process leaves behind (REQ-PROV-11)
  repairs: 1 failed turns dropped, 1 orphaned results dropped
...
── 7. Delegation hands out a FRESH child per call (REQ-MULTI-02/04)
  three concurrent delegations: c1=ok  c2=ok  c3=ok
  parent: "All three done."
```

## The seven sections

| # | Function | What it shows |
|---|---|---|
| 1 | `demoStreamingLoop` | A two-turn tool-using run consumed as an event stream (`TextDeltaEvent`, `ToolCallStartEvent`, `ToolResultEvent`, `TurnEndEvent`), then `stream.RunResult()`. |
| 2 | `demoStopReasonTrap` | The loop iterates on the *presence* of tool calls, not on `stop_reason` — Gemini and some gateways return `STOP` alongside tool calls. |
| 3 | `demoTruncatedToolCalls` | A tool call in a turn that hit the output cap (`StopReasonLength`) is never executed; the model gets an error result and the loop continues. |
| 4 | `demoWireAsymmetry` | One transcript encoded by `anthropic.BuildRequest` (all results in one user message) and `openai.BuildRequest` (one `role:"tool"` message per result). |
| 5 | `demoTranscriptRepair` | Send-time repair of an aborted turn and its orphaned result; the durable log is untouched, only the request view changes. |
| 6 | `demoKillAndResume` | Two "processes" share a `session.OpenOrCreate` JSONL log; the second folds it and is built with `agentkit.NewAgentFromSession`. |
| 7 | `demoDelegation` | `subagent.Tool` takes a *factory*, so three parallel delegations each get a fresh child agent. |

## Code worth copying

- **A tool** — `wordCount()` (top of `main.go`): `core.Tool` with an
  `InputSchema` built from `schema.Object`/`schema.Prop`/`schema.Opt`, a
  `PromptGuidelines` line, and a `Handler` that unmarshals its arguments and
  returns JSON. The loop wraps the handler's return in `{"ok":true,"data":…}`.
- **A scripted provider** — `faux.New(faux.Turn{Blocks: …, StopReason: …})`
  with `faux.FauxText` and `faux.FauxToolCall`. `p.ChunkSize` splits text into
  deltas so streaming is visible; `p.Requests()` returns what the loop sent
  (section 6 uses it to prove the resumed request carried the history).
- **The minimum config** — `newAgent` / `baseConfig`: a `Model`
  (`faux.Model()`), a `StopPolicy`, and a `Providers` registry mapping
  `faux.API` to `p.APIProvider()`. With a real vendor you would register its
  provider instead (see [`chat`](../chat)).
- **Resume** — `demoKillAndResume`: `session.OpenOrCreate` returns the store
  *and* the folded `resume`; pass both to `NewAgentFromSession` with a resolver
  that maps the recovered provider/API/model triple to a `*core.Model`.

## Gotchas

- `agentdemo` is a tour, not a template. For a minimal real program start with
  [`chat`](../chat); for testing your own agent code with `faux`, see
  [`testing`](../testing).
- A non-empty session store passed to `NewAgent` is an error — resuming is
  always `NewAgentFromSession`.
- Section 6 writes its log to a temporary directory and removes it on exit.

## Related packages

`core` (messages, events, `Tool`), `provider/faux`, `provider/anthropic` and
`provider/openai` (`BuildRequest`), `schema`, `session`, `stop`, `subagent`.
