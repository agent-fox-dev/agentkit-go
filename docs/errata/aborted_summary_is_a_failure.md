# Erratum: compaction's summary — abort, spend, and thinking in the kept tail

**Relates to:** REQ-GO-12 (rules 1–3), REQ-GO-16, REQ-PROV-11.
**Status:** raised in
[agent-fox-dev/agentkit-go#74](https://github.com/agent-fox-dev/agentkit-go/issues/74).

## An aborted summarization is a failure

**What the PRD says.** REQ-GO-16: "An **aborted** summarization is not a
failure: the text produced so far is kept."

**Why it cannot hold.** The same requirement rejects `max_tokens` because "the
summary is truncated mid-thought, and compaction is permanent — a truncated
summary looks like a success and then poisons every subsequent turn for the
life of the session." An abort mid-stream is that truncation. The abort is
rarely a decision about the summary: a phase timeout or a Ctrl-C that lands
while the loop is preparing the next turn cancels the summarization along with
everything else. The half-written text then became the checkpoint, was handed
to `OnCheckpoint` and persisted, and could not be undone on resume.

**What is implemented.** `ValidateSummary` returns `*ErrBadSummary` for
`StopReasonAborted`. The transform treats it like any other failed summary
(ruling P-39): no checkpoint, `OnCheckpoint` not called, the current view
returned, the error passed to `OnError`. The next turn that is over the
threshold summarizes again.

## Summarization spend counts as the agent's spend

**What the PRD says.** REQ-GO-12.3 keeps the summarization call off the
middleware path so that it is not "charged against the budget gate" or
fingerprinted as a conversational turn. The configuration reference says
`Agent.Usage` is the agent's cumulative usage for its lifetime.

**What the code did.** The summarizer dropped the response's usage, so
`Agent.Usage`, and with it `stop.OverBudget`, never saw a summarization: on a
1M-window model with a 0.6 threshold that is roughly 600K input tokens per
compaction, unaccounted.

**What is implemented.** `core.WithUsageReporter` / `core.ReportUsage` carry
a usage reporter on the context. The agent installs one around its
`TransformContext`, adding what is reported to `Agent.Usage`; it does not
fold it into the cache meter, because the request is not a turn and is sent
uncached. `ModelSummarizer` and `ModelTurnSummarizer` report every
response's usage, including one whose summary is then rejected, since that
request was billed too. A custom summarizer reports its own calls with
`core.ReportUsage`. The call still bypasses the middleware chain, so
`middleware.Budget` does not gate it — REQ-GO-12.3 is unchanged on routing —
but the next turn's budget gate and stop policy see what it cost.

## Thinking from before the checkpoint is left out of the view

**What the PRD says.** REQ-GO-12.1: the compacted view is the summary followed
by the kept tail, verbatim.

**Why it cannot hold.** A signed thinking block is bound to the conversation
that preceded it — system prompt, tools and every earlier message — and the
Anthropic API checks that binding on replay. A tail message kept after the
summary was produced before the summary existed, so its thinking fails the
check: "keep-tail" compaction is a documented breaking shape, rejected with a
400 `invalid_request_error` on enforced accounts (created on or after
2026-08-31, on Claude Fable 5.1, Claude Opus 5.5 and Claude Sonnet 5.5). The
first request after compacting fails, and because compaction is permanent,
every later one does too.

**What is implemented.** `ApplyCheckpoint` leaves out the thinking blocks
(including redacted ones) of every kept message whose original index is below
`CreatedAtLen` — produced before the checkpoint. Text and tool calls stay.
Thinking produced after the checkpoint, under the summary, is kept, so a
session's reasoning is preserved from the compaction onwards. A checkpoint
with `CreatedAtLen` zero (unknown) strips nothing. The transcript and the
session log are untouched; only the view sent to the model changes.
