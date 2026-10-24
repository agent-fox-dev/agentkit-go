# agentdemo: the agent loop, with no key and no network

`agentdemo` runs AgentKit's real agent loop end to end against `provider/faux`,
a scripted provider that replays canned model turns. Only the model is fake:
the loop, the tool executor and the Anthropic request encoder are the shipped
code. It shows, in four short sections, behaviour that a naive agent loop gets
wrong. Start here if you want to see the machinery without spending anything.

## Run it

```bash
go run ./examples/agentdemo
```

There are no flags. The output uses ANSI colours. The first section looks like
this:

```
── 1. The loop, streamed ─────────────────────────────────────────
Let me count those.  → calling word_count (call_1)
  ← {"data":{"count":9},"ok":true}
  [turn 0 ended: tool_use]
That sentence has 9 words.
  [turn 1 ended: stop]

final: "That sentence has 9 words."
turns: 2, stop: end_turn
```

## What it shows

1. **The loop, streamed.** A two-turn run consumed as an event stream
   (`TextDeltaEvent`, `ToolCallStartEvent`, `ToolResultEvent`, `TurnEndEvent`),
   then `stream.RunResult()`.
2. **Tool calls drive the loop, not `stop_reason`.** Some providers report
   `stop` alongside tool calls; the loop runs the tools whatever the stop
   reason says.
3. **A truncated tool call is never executed.** When a turn hits the output
   token limit, its tool calls get an error result asking the model to re-issue
   them, and the loop continues.
4. **Transcript repair.** An aborted turn and its orphaned tool result are
   dropped from the request sent to the provider; the stored history is left
   complete.

## The code that matters

A tool is a `core.Tool` with a schema built from combinators and a handler.
The loop wraps the handler's return in `{"ok":true,"data":…}`:

```go
core.Tool{
	Name:        "word_count",
	Description: "Count the words in a piece of text",
	InputSchema: schema.Object(
		schema.Prop("text", schema.String("The text to count words in")),
		schema.Opt("unique", schema.Bool("Count distinct words only")),
	),
	Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) { … },
}
```

A scripted model and the minimum agent. The faux provider is a
`core.ProviderClient`, so it goes in `Client`; with a real model you leave
`Client` out and set `Provider` and `Model` (see [`codingagent`](../codingagent)):

```go
p := faux.New(
	faux.Turn{Blocks: []core.ContentBlock{
		faux.FauxText("Let me count those."),
		faux.FauxToolCall("call_1", "word_count", `{"text":"the quick brown fox"}`),
	}, StopReason: core.StopReasonToolUse},
	faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("4 words.")}, StopReason: core.StopReasonStop},
)
agent, err := agentkit.New(agentkit.Config{
	Client:     p,
	Model:      faux.Model().ID,
	System:     "You are a helpful assistant.",
	Toolset:    agentkit.Toolset{Tools: []core.Tool{wordCount()}},
	MaxTurns:   8,
	MaxCostUSD: 0.50,
})
```

`p.ChunkSize` splits text into deltas so streaming is visible, and
`p.Requests()` returns what the loop sent.

`agentdemo` is a tour, not a template: for a minimal real program start with
[`codingagent`](../codingagent) or [`customtools`](../customtools).

## Further reading

- [Examples index](../README.md)
- [User guide](../../docs/guide.md): [testing without a model](../../docs/guide.md#testing-without-a-model), [bounding and watching a run](../../docs/guide.md#bounding-and-watching-a-run)
- [Architecture](../../docs/architecture.md#data-flow-of-one-run): how a run flows through the loop
