# compaction

A long conversation will eventually outgrow the model's context window.
**Compaction** keeps it going: once the transcript passes a threshold, the
oldest part is replaced *in the request* by a model-written summary, and the
recent tail is sent verbatim. The transcript itself is never rewritten. The
session log still holds every message, so scrollback still works and a later
run on a bigger model can still be sent the whole history.

This example runs with **no API key and no network**. It drives the real loop
and the real `compaction` transform against two scripted
[`provider/faux`](../../provider/faux) providers: one plays the conversation,
the other plays the same model writing summaries. The only thing it fakes is
the size of the context window, which it shrinks to 3,000 tokens so the
threshold comes up within a few turns.

```bash
go run ./examples/compaction
go test ./examples/compaction/
```

## What it shows

```
── 1. A conversation grows past the threshold ────────────────────────
context window 3000 tokens; Summarization fires above 1500 (50%)

  turn 1: history  2 msgs → sent  1 msgs, est   22 tokens
  turn 2: history  4 msgs → sent  3 msgs, est  563 tokens
  turn 3: history  6 msgs → sent  5 msgs, est  864 tokens
  turn 4: history  8 msgs → sent  7 msgs, est 1266 tokens
    [compaction error] compaction: agentkit: unusable summary: summary was truncated at the output limit
  turn 5: history 10 msgs → sent  9 msgs, est 1613 tokens
    [checkpoint] summarized messages 0..6, kept from entry 7b5d5e08…
  turn 6: history 12 msgs → sent  5 msgs, est  452 tokens  ← starts with the summary
...
── 4. A restart folds the checkpoint back out of the log ─────────────
  folded 12 messages; checkpoint present: true (prefix 7)
  next turn sent 7 of 14 messages  ← starts with the summary
```

1. **The trigger.** Before every model call the transform estimates the
   context. If the estimate is over `ThresholdFraction` of the window, it
   summarizes a prefix.
2. **A bad summary is rejected.** The first summary comes back truncated at
   `max_tokens`. `compaction.ValidateSummary` rejects it. `OnError` reports
   the rejection, no checkpoint is written, the request goes out over the
   full view, and the next turn tries again. The session is never aborted.
3. **The checkpoint is permanent.** Once it exists it is re-applied on every
   later request. The threshold only decides whether to *extend* it.
4. **It survives a restart.** The checkpoint is written to the session log as
   a `compaction` entry. Folding the log recovers it, and the resumed agent
   applies it before its first request, so the new process does not pay for
   another summary.

The cut in this run lands *inside* a turn. The user's question is in the
summarized part and the assistant's reply is in the kept tail. So the prefix
of that turn is summarized separately by the `TurnSummarizer` and joined with
`compaction.SplitSeparator`. Section 2 of the output shows the two kinds of
summarizer call.

## The code an application copies

The wiring has four steps, and their order matters:

```go
// 1. Make the history FIRST. The transform keeps its checkpoint there,
//    and the agent appends to it, so both must share it.
history := core.NewConversationHistory()

// 2. Bind the transform. The summarizers call the provider directly, not
//    through the agent.
p, _ := cfg.Providers.Get(cfg.Model.API)
client := core.ClientFunc(p.Stream)
cfg.TransformContext = compaction.NewContextTransform(compaction.Deps{
    Strategy:       compaction.Summarization{ThresholdFraction: 0.6},
    Summarizer:     compaction.ModelSummarizer(client, cfg.Model, 8000),
    TurnSummarizer: compaction.ModelTurnSummarizer(client, cfg.Model, 8000),
    History:        history,
    Model:          cfg.Model,
    OnError:        func(err error) { log.Print(err) },
    // 3. Optional: persist the checkpoint (see below).
    OnCheckpoint:   persist,
})

// 4. Construct with the SAME history.
agent, err := agentkit.NewAgentWithHistory(cfg, history)
```

That is the same as `installCompaction` in
[`triage/triage.go`](../triage/triage.go). In this example, the summarizer
client is a second faux provider so the two scripts stay readable. In your
application it is the registered provider of the model you are already using.

To persist the checkpoint, append a compaction entry. Its anchor is the
**entry id** of the first kept message, not an index:

```go
OnCheckpoint: func(cp core.CompactionCheckpoint) error {
    firstKept := history.EntryIDAt(cp.PrefixLen)
    return store.Append(session.NewCompactionEntry(cp.Summary, firstKept, previousSummary))
},
```

On resume, `session.OpenOrCreate` folds the log into a `Resume` whose
`History` already carries the checkpoint. Bind a new transform to
`resume.History` and build the agent with `agentkit.NewAgentFromSession`.

## Strategies

| Strategy | Does | Cut lands on |
|---|---|---|
| `compaction.Summarization{ThresholdFraction, KeepTokens}` | Summarizes the prefix and keeps `KeepTokens` verbatim. Defaults are 0.8 and 8000. | anything but a tool result |
| `compaction.TokenWindow{KeepTokens}` | Drops the prefix with no summary. | a user message |
| `compaction.TurnWindow{MaxTurns}` | Keeps the last N user turns and drops the rest. | a user message |
| `compaction.None{}` | Never compacts. | — |

## Gotchas

- **Do not use `agentkit.NewAgent` when the transform holds a history.**
  `NewAgent` makes its own history, so the transform's checkpoint lands
  somewhere the agent never reads. Use `NewAgentWithHistory(cfg, history)`.
- **Compaction is not middleware, on purpose.** A summary made through the
  middleware chain would be fingerprinted by `middleware.Caching`, gated by
  `middleware.Budget` and counted as a conversational turn. The summarizers
  call the provider directly. Their usage is still reported to the agent
  through `core.ReportUsage`, so `Agent.Usage()` and `stop.OverBudget` see the
  spend.
- **The estimate is anchored, not counted.** `compaction.EstimateContextTokens`
  takes the newest *provider-reported* usage and estimates only the messages
  after it, at 4 characters per token and a flat cost per image. It skips
  three kinds of anchor that would reset it to near zero: an aborted or
  errored turn, a zero usage, and a turn sent before the current checkpoint.
  With a provider that reports no usage, it falls back to counting
  characters.
- **Summaries are checked.** A summary is rejected if it is empty, truncated
  (`max_tokens`), aborted, an error, or contains a tool call. A rejection is
  not retried within the same turn. It is tried again on the next one.
- **Thinking from before the checkpoint is dropped from the view.** A signed
  thinking block is bound to the prefix it was produced under, and that
  prefix is gone. Text and tool calls stay.
- **`reserveTokens` bounds the summary.** The summary's `max_tokens` is
  `min(0.8 × reserve, model.MaxTokens)`. `ModelTurnSummarizer` gets half of
  that.

## See also

- [`compaction`](../../compaction): `NewContextTransform`, `Deps`, the
  strategies, `ModelSummarizer`, `ValidateSummary`, `EstimateContextTokens`
- [`core.ConversationHistory`](../../core/history.go): `Checkpoint`,
  `SetCheckpoint`, `EntryIDAt`
- [`session`](../../session): `NewCompactionEntry`, `Fold`, `Resume.HasCheckpoint`
- [`triage`](../triage), [`cleaner`](../cleaner) and
  [`flatline`](../flatline): the same wiring in finished applications
