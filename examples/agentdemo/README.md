# agentdemo — the whole loop, with no key and no network

`agentdemo` runs the real AgentKit loop end to end against `provider/faux`, a
scripted provider that replays canned turns. Nothing is mocked except the
model: the loop, the tool executor, the Anthropic wire encoder and its
transcript repair are the shipped code.

Each of its four sections demonstrates one behaviour that a naive agent loop
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
── 4. Repairing what an aborted turn leaves behind (REQ-PROV-11) ─
  repairs: 1 failed turns dropped, 1 orphaned results dropped
...
  History still holds all three messages; only the VIEW was repaired.
```

## The four sections

| # | Function | What it shows |
|---|---|---|
| 1 | `demoStreamingLoop` | A two-turn tool-using run consumed as an event stream (`TextDeltaEvent`, `ToolCallStartEvent`, `ToolResultEvent`, `TurnEndEvent`), then `stream.RunResult()`. |
| 2 | `demoStopReasonTrap` | The loop iterates on the *presence* of tool calls, not on `stop_reason` — some gateways return `STOP` alongside tool calls. |
| 3 | `demoTruncatedToolCalls` | A tool call in a turn that hit the output cap (`StopReasonLength`) is never executed; the model gets an error result and the loop continues. |
| 4 | `demoTranscriptRepair` | Send-time repair (`anthropic.BuildRequest`) of an aborted turn and its orphaned result; history is untouched, only the request view changes. |

## Code worth copying

- **A tool** — `wordCount()` (top of `main.go`): `core.Tool` with an
  `InputSchema` built from `schema.Object`/`schema.Prop`/`schema.Opt`, a
  `PromptGuidelines` line, and a `Handler` that unmarshals its arguments and
  returns JSON. The loop wraps the handler's return in `{"ok":true,"data":…}`.
- **A scripted provider** — `faux.New(faux.Turn{Blocks: …, StopReason: …})`
  with `faux.FauxText` and `faux.FauxToolCall`. `p.ChunkSize` splits text into
  deltas so streaming is visible; `p.Requests()` returns what the loop sent.
- **The minimum config** — `newAgent`: `agentkit.New(agentkit.Config{…})`
  with `Provider: p` (the faux provider is a `core.ProviderClient` itself),
  `Model: faux.Model().ID`, a `System` prompt, the `Tools`, and `MaxTurns` /
  `MaxCostUSD` bounding turns and spend. With a real model you set
  `Client` to the client `anthropic.Resolve(anthropic.OSEnv{})` returns
  instead of `Provider` (see [`codingagent`](../codingagent)).

## Gotchas

- `agentdemo` is a tour, not a template. For a minimal real program start with
  [`codingagent`](../codingagent) or [`customtools`](../customtools).

## Related packages

`core` (messages, events, `Tool`), `provider/faux`, `provider/anthropic`
(`BuildRequest`), `schema`.
