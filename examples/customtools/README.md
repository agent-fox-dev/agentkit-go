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

Needs a credential for the model's vendor (default `anthropic/claude-sonnet-5`):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/customtools
go run ./examples/customtools "Reserve four BRKT-90 and tell me their total mass in pounds."
```

No flags; `AGENTKIT_MODEL` overrides the model. The inventory knows
`BOLT-M6`, `NUT-M6`, `BRKT-90` and `WSHR-M6`.

## What you'll see

Each tool call with the model's raw arguments, then whether your code returned
a result or an error result, and finally the submitted answer. The shape:

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
```

Without a credential:

```
error: anthropic: missing credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, ...
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
| Run bounds | `run()` `agentkit.Config` | `MaxTurns: 12` and `MaxCostUSD: 1.00` end the run if the model never calls `submit_answer`; the run then reports `core.RunStopMaxTurns` or `core.RunStopBudgetExceeded`. |
| Constrained sampling | `submitAnswerTool` | `core.ConstrainedSampling{Type: core.ConstrainJSONSchema, Strict: core.StrictPrefer}`. |

## Gotchas

- **`Terminate` is a vote, and one is enough.** When any call that ran votes
  to terminate, the run ends once the whole batch has finished. If the model
  calls `submit_answer` next to other tools, their results are still
  computed and recorded, but the model does not see them.
- **Constrained sampling is declared, not enforced, here**: the Anthropic
  wire, the only one this module ships, ignores it.
- The inventory map is unsynchronised on purpose. That is safe only because
  `reserve_stock` is sequential and `lookup_part` only reads.
- `ToolCallEndEvent.Block.Input` holds the model's original bytes, *before*
  repair, so you can see what `PrepareArguments` changed.

## Related

Packages: `agentkit` (`Config`, `New`), `core` (`Tool`, `ToolResult`,
`OKResult`, `ErrResult`, `ExecutionMode`, `ConstrainedSampling`,
`RunStopToolTerminate`), `schema`, `provider/anthropic` (`Resolve`).
To test tools like these offline, script the model with `provider/faux`, as
[`agentdemo`](../agentdemo) does.
