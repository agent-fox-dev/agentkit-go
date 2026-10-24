# customtools: writing your own tools well

A tool is how a model acts on your system: it sees a name, a description and a
JSON schema, sends arguments, and reads back a result. In AgentKit a tool is a
`core.Tool` value, and this example is the reference for its fields: schemas
built from combinators, the two handler shapes, error results the model can
recover from, argument repair, sequential execution for tools with side
effects, and a tool that ends the run with a structured answer. The scenario is
a tiny parts inventory. It calls a real model, so it needs a provider and its
credential.

## Run it

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/customtools
go run ./examples/customtools "Reserve four BRKT-90 and tell me their total mass in pounds."
```

There are no flags; everything on the command line is the prompt. The
inventory knows `BOLT-M6`, `NUT-M6`, `BRKT-90` and `WSHR-M6`.

`AGENTKIT_PROVIDER` (`anthropic`, `openrouter` or `ollama`), `AGENTKIT_MODEL`
(a tier or a model id, default `STANDARD`) and `AGENTKIT_EFFORT` choose the
model. Only the examples read these variables. Each provider reads its own
credential: `ANTHROPIC_API_KEY` (or another Anthropic credential),
`OPENROUTER_API_KEY`, or `OLLAMA_HOST`. See
[choosing a provider](../../docs/guide.md#choosing-a-provider).

Each tool call is printed with the model's raw arguments and whether your code
returned a result or an error result, then the submitted answer:

```
  → lookup_part({"ids":["BOLT-M6","BRKT-90"],"include_specs":true})
  ← lookup_part: ok (0ms)
  → reserve_stock({"id":"BOLT-M6","quantity":3})
  ← reserve_stock: ok (0ms)
  ...
answer: …
cited:  BOLT-M6, BRKT-90

[anthropic · <model id> · effort <effort> · 4 turns · stop tool_terminate · in <n> / out <n> tokens · $<cost>]
```

A model that never calls `submit_answer` ends with
`run ended without submit_answer (<stop reason>)`.

## What it shows

| Tool | Concept |
|---|---|
| `lookup_part` | The `Handler` shape (`json.RawMessage` in and out); a schema with `schema.Array(…).MinItemsN(1)`; `PrepareArguments` turning `"ids": "A, B"` or `{"id": "A"}` into an array before validation. |
| `convert_units` | The `Execute` shape, returning `core.OKResult` or `core.ErrResult(code, msg)`; `schema.Enum`; an error message written as a repair instruction. |
| `reserve_stock` | `ExecutionMode: core.Sequential` for a tool with side effects; `Metadata` for your instrumentation, never seen by the model. |
| `submit_answer` | `res.Terminate = true` ends the run with `core.RunStopToolTerminate` and an answer that arrived as validated arguments. |

Every tool also carries `PromptGuidelines`, folded into the system prompt and
removed with the tool.

## The code that matters

```go
core.Tool{
	Name:          "reserve_stock",
	Description:   "Reserve a quantity of a part, decrementing the stock on hand",
	ExecutionMode: core.Sequential, // the whole batch then runs in order
	InputSchema: schema.Object(
		schema.Prop("id", schema.String("Part id to reserve")),
		schema.Prop("quantity", schema.Int("How many units to reserve").Min(1)),
	),
	Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
		// ...
		if p.OnHand < args.Quantity {
			return core.ErrResult("insufficient_stock", fmt.Sprintf(
				"%s has %d on hand, %d requested; reserve %d or fewer", …))
		}
		return core.OKResult(map[string]any{"reserved": args.Quantity, …})
	},
}
```

Things to know:

- Set exactly one of `Handler` and `Execute`; `agentkit.New` rejects both or
  neither. Only `Execute` can set `Terminate` and `Metadata`.
- An error result is not a Go error: the model reads it and the run continues.
- `Terminate` is a vote: if any call in a batch casts it, the run ends after
  the batch, and the model never sees the other calls' results.
- One sequential tool makes its whole batch sequential, so don't mark
  read-only tools sequential.

To test tools like these offline, script the model with `provider/faux`, as
[`agentdemo`](../agentdemo) does.

## Further reading

- [Examples index](../README.md)
- [User guide: writing your own tools](../../docs/guide.md#writing-your-own-tools)
- [Configuration](../../docs/configuration.md): `agentkit.Config` and run bounds
