# deferred

Some work does not need an answer now. Vendors run batch APIs for exactly
this: you submit a request, get a job id back, and collect the answer hours
later, usually at a discount. **Deferred submission** is AgentKit's support
for that pattern. A run can end holding a **receipt** instead of an answer.
The receipt (`core.DeferredHandle`) is saved in the session log, so the
process can exit. Later, possibly in a different process, `RedeemDeferred`
collects the answer and continues the loop from it as if it had arrived
live.

AgentKit ships the type, the stop reason and the redemption call. It does
**not** ship a poller. When to come back is your decision: a cron job, a
queue consumer, a webhook from the vendor.

```bash
go run ./examples/deferred
go test ./examples/deferred/
```

## Read this first: no shipped wire supports it yet

None of the five shipped wire APIs (Anthropic, OpenAI chat completions,
OpenAI responses, Google, Ollama) registers `FetchDeferred` today. Section 1
of the example probes all five, and every probe answers `false`. So the
example demonstrates the capability the only honest way: with **a provider of
your own**. Its `batch` provider fronts a simulated job queue shaped like a
vendor batch API and delegates the actual answering to a scripted
[`provider/faux`](../../provider/faux) model.

Everything on the agent side is the shipped code: the probe, the clean
`RunStopDeferred` end, the handle in the session log, the poll-after guard,
and `RedeemDeferred` running the loop from the answer. That is also why there
is no `--real` mode. There is no real wire to point it at. The README's last
section sketches how to write one against a vendor batch API.

## What it shows

```
── 1. probe before you submit ────────────────────────────────────────────
  anthropic-messages   SupportsDeferred = false
  ...
  batch                SupportsDeferred = true   ← the provider below

── 2. process 1 submits and exits ────────────────────────────────────────
  run ended: stop=deferred, err=nil, handle present=true
  handle: id=job_001 model=batch/batch-1 expires in 24h0m0s, poll after 300ms, data={"queue":"overnight"}

── 3. process 2 finds the handle in the log ──────────────────────────────
  folded 2 messages; last assistant turn holds handle job_001: true

── 4. redeem: too early, not ready, ready ────────────────────────────────
  now:          agentkit: deferred handle "job_001" is not due yet (poll_after_ms 300); redeem it then
  after 300ms: agentkit: redeeming deferred handle "job_001": batch: job job_001 is still running
  after the job ran: 2 turns, stop=end_turn
    assistant   "I'll count the open incidents first." + calls count_incidents{"status":"open"}
    tool_result {"data":{"count":3,"max_severity":"sev-2"},"ok":true}
    assistant   "Weekly report: 3 open incidents, none above sev-2."
```

## The code an application copies

**Submitting:**

```go
if !agent.SupportsDeferred() {
    // submit normally: a provider that ignores the option answers at once,
    // and you would wait for a handle that never comes
}
cfg.RequestOptions.Deferred = &core.DeferredRequest{Window: 24 * time.Hour}
res, err := agent.Run(ctx, prompt)  // err == nil, res.StopReason == core.RunStopDeferred
h, ok := res.DeferredHandle()       // also saved in the session log
```

**Redeeming, later and possibly in another process:**

```go
store, resume, _ := session.OpenOrCreate(path, session.Options{})
h, ok := pendingHandle(resume.Messages) // the newest assistant message, if it is a receipt
agent, _ := agentkit.NewAgentFromSession(cfg, resume, resolve)
agent.RegisterTool(...)                 // the answer may call tools; the loop runs them

res, err := agent.RedeemDeferred(ctx, h)
```

`RedeemDeferred` makes **one attempt**. It refuses without sending anything
when:

- the handle has **expired** (`ExpiresAt`), so the answer is gone and the
  request has to be submitted again,
- the provider's **poll-after delay** has not passed (`IssuedAt +
  PollAfterMS`). `IssuedAt` is stamped when the receipt arrives and saved
  with it, so the delay holds across a restart,
- the agent's model is not the one the handle was **issued for**, because a
  handle redeemed against another model returns someone else's answer at
  best.

A fetch that fails (not ready, a network error, an aborted context) returns an
error and **records nothing**. Retry with the same handle whenever you
choose. On success, the answer is *appended* after the receipt, so the log
reads submission, then receipt, then answer. Its tool calls run, and the loop
continues to the model's next turn.

**Writing a deferred-capable provider.** It is an ordinary `core.APIProvider`
with `FetchDeferred` set:

```go
core.APIProvider{
    API:    "batch",
    Stream: func(ctx, m, req, o) *core.EventStream {
        if req.Deferred == nil { /* answer live */ }
        // submit; answer with a message whose StopReason is
        // core.StopReasonDeferred and whose Deferred is the handle
    },
    FetchDeferred: func(ctx, m, h, o) *core.EventStream {
        // look the job up by h.ID; stream back the finished AssistantMessage,
        // or an error stream if it is not ready
    },
}
```

For a real vendor batch API, `Stream` posts the encoded request and returns
the vendor's job id as `DeferredHandle.ID`. Put any vendor detail you need
for retrieval in `Data`, which is saved too. `FetchDeferred` retrieves the
result and decodes it into a `core.AssistantMessage`.

## Gotchas

- **`RequestOptions.Deferred` applies to every model call while it is set.**
  In section 3 the redeeming agent leaves it unset, so the turns *after* the
  redeemed answer run live. Leave it set to defer those as well, and each
  later turn ends with a new receipt.
- **A deferred stop without a handle is an error.** A provider that answers
  `StopReasonDeferred` with no handle gives you nothing to redeem. The run
  fails with `core.ErrDeferredUnsupported` instead of looking like an empty
  completion.
- **Redeem from an agent built from the session**, with the tools the answer
  may call. The redemption claims the agent's run slot, so it cannot
  interleave with a live `Run`.
- **There is no poller, on purpose.** Nothing sleeps, retries or watches a
  handle. A library that guessed a schedule would spend your latency budget
  on a schedule it made up.

## See also

- [`deferred.go`](../../deferred.go): `Agent.SupportsDeferred`,
  `Agent.RedeemDeferred`
- [`core/provider.go`](../../core/provider.go): `DeferredRequest`,
  `DeferredHandle` (`PollReadyAt`, `Expired`),
  `ProviderRegistry.SupportsDeferred`, `APIProvider.FetchDeferred`
- `core.RunResult.DeferredHandle`, `core.RunStopDeferred`,
  `core.StopReasonDeferred`, `core.ErrDeferredUnsupported`
- [`session`](../session): the log the handle lives in
