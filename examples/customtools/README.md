# customtools — writing your own tools well

A tool is how a model acts on your system: it sees a name, a description and a
JSON schema, emits arguments, and reads back a result. In AgentKit a tool is a
`core.Tool` value, and almost every field on it exists because of a specific
way tools go wrong in production — a model passing a string where an array was
declared, two writes racing in a parallel batch, an answer scraped out of
prose instead of validated arguments.

This example is the reference for those fields. The scenario is a tiny parts
inventory with four tools: `lookup_part` reads, `convert_units` does
arithmetic the model should not do in its head, `reserve_stock` mutates shared
state, and `submit_answer` ends the run with a structured answer.

## Run it

Three environment variables choose what runs the model, and the program reads
them itself and passes them to `agentkit.Config` unchanged. Each provider needs
only its own credential or server (the variables are in
[`examples/README.md`](../README.md#configuring-an-application)):

```bash
# Anthropic, the default provider
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/customtools
go run ./examples/customtools "Reserve four BRKT-90 and tell me their total mass in pounds."

# OpenRouter
export OPENROUTER_API_KEY=sk-or-...
AGENTKIT_PROVIDER=openrouter go run ./examples/customtools

# Ollama, on a server that has pulled the tier's model (ollama pull <model>)
AGENTKIT_PROVIDER=ollama go run ./examples/customtools

# A tier other than STANDARD, and an effort of your own
AGENTKIT_PROVIDER=ollama AGENTKIT_MODEL=SIMPLE AGENTKIT_EFFORT=high go run ./examples/customtools
```

| Variable | Default | Meaning |
|---|---|---|
| `AGENTKIT_PROVIDER` | empty, which is `anthropic` | `anthropic`, `openrouter` or `ollama` (`Config.Provider`). |
| `AGENTKIT_MODEL` | `STANDARD` | A tier (`SIMPLE`, `STANDARD`, `ADVANCED`) or the provider's own model id (`Config.Model`). |
| `AGENTKIT_EFFORT` | empty, which is the tier's effort | `low`, `medium`, `high`, `xhigh` or `max` (`Config.Effort`). |

No flags. The inventory knows `BOLT-M6`, `NUT-M6`, `BRKT-90` and `WSHR-M6`.

## What you'll see

Each tool call with the model's raw arguments, then whether your code returned
a result or an error result, then the submitted answer, and a summary of what
ran and what it used on stderr. The shape:

```
  → lookup_part({"ids":["BOLT-M6","BRKT-90"],"include_specs":true})
  ← lookup_part: ok (0ms)
  → reserve_stock({"id":"BOLT-M6","quantity":3})
  ← reserve_stock: ok (0ms)
  ...
  → submit_answer({"answer":"…","parts":["BOLT-M6","BRKT-90"]})
  ← submit_answer: ok (0ms)

answer: …
cited:  BOLT-M6, BRKT-90

[ollama · qwen3.8:27b · effort high · 4 turns · stop tool_terminate · in <n> / out <n> tokens · $0.00000]
```

The summary names the provider, the model id and the effort that
`Agent.Resolution()` reports. The cost is `cost unknown` when the run's usage is
flagged `Unpriced`: a model the catalog does not list has no price, and a zero is
then not a cost. A model that answers in text and never calls `submit_answer`
ends the run with `run ended without submit_answer (end_turn)` after what it
said.

A configuration that cannot run stops before any request, with the reason and
exit status 1:

```
error: anthropic: missing credentials: set ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or ANTHROPIC_OAUTH_TOKEN, ...
error: openrouter: OPENROUTER_API_KEY is not set (set it in the environment or in Options.Env)
error: agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama
```

## Walkthrough

| Concept | Where | What to copy |
|---|---|---|
| Schema as a value | `lookupPartTool` `InputSchema` | `schema.Object(schema.Prop(…), schema.Opt(…))`, `schema.Array(…).MinItemsN(1)`. AgentKit rewrites it per provider, coerces and validates arguments, and renders the expected shape into the error the model reads. A raw JSON blob can only be forwarded. Property order is preserved. |
| `Handler` shape | `lookupPartTool` | `func(ctx, json.RawMessage) (json.RawMessage, error)`. For tools that only answer. A returned Go error becomes an error result (`handler_error`), not a crash. |
| `Execute` shape | `convertUnitsTool` | `func(ctx, json.RawMessage) core.ToolResult` with `core.OKResult` / `core.ErrResult(code, msg)`. The only way to reach `Terminate`, `Metadata` and extra content blocks. Set exactly one of `Handler`/`Execute`; `agentkit.New` rejects both or neither. |
| Error results | `convertUnitsTool`, `reserveStockTool` | An error result goes back to the model and the run continues. Its message is the whole repair instruction: say what was wrong and what would be right. |
| Enums | `convertUnitsTool` | `schema.Enum(desc, values…)` pins vocabulary; cross-field rules still need a handler check. |
| Argument repair | `lookupPartTool` `PrepareArguments` | Runs first in the argument pipeline. Turns `"ids": "A, B"` or `{"id": "A"}` into an array. Must return a copy, not mutate its input. |
| Prompt guidelines | every tool's `PromptGuidelines` | Folded into the system prompt, deduplicated, and deleted with the tool. |
| Sequential execution | `reserveStockTool` `ExecutionMode: core.Sequential` | For tools with side effects. One sequential tool runs the whole batch in order on one goroutine, so don't mark read-only tools sequential. |
| Metadata | `reserveStockTool` `res.Metadata` | `core.ToolMetadata` reaches your instrumentation, never the model. |
| Terminating tool | `submitAnswerTool` | `res.Terminate = true`. Check `res.StopReason == core.RunStopToolTerminate` in `run()`. |
| Provider, model and effort | `selection()`, `run()` `agentkit.Config` | `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and `AGENTKIT_EFFORT` go to `Config.Provider`, `Model` and `Effort` unchanged; `New` builds the provider's client and rejects a bad value before any request. |
| Run bounds | `run()` `agentkit.Config` | `MaxTurns: 12` and `MaxCostUSD: 1.00` end the run if the model never calls `submit_answer`; the run then reports `core.RunStopMaxTurns` or `core.RunStopBudgetExceeded`. |
| Strict arguments | every tool | The Anthropic wire declares every tool `"strict": true`, so the model's arguments match the input schema. |

## Gotchas

- **`Terminate` is a vote, and one is enough.** When any call that ran votes
  to terminate, the run ends once the whole batch has finished. If the model
  calls `submit_answer` next to other tools, their results are still
  computed and recorded, but the model does not see them.
- The inventory map is unsynchronised on purpose. That is safe only because
  `reserve_stock` is sequential and `lookup_part` only reads.
- `ToolCallEndEvent.Block.Input` holds the model's original bytes, *before*
  repair, so you can see what `PrepareArguments` changed.

## Related

Packages: `agentkit` (`Config`, `New`), `core` (`Tool`, `ToolResult`,
`OKResult`, `ErrResult`, `ExecutionMode`,
`RunStopToolTerminate`), `schema`.
To test tools like these offline, script the model with `provider/faux`, as
[`agentdemo`](../agentdemo) does.
