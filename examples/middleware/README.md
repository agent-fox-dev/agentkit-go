# middleware

Middleware wraps **every model call** an agent makes. Each one is a plain
function:

```go
type Middleware func(next core.Handler) core.Handler
type Handler    func(ctx context.Context, req core.Request) *core.EventStream
```

It receives the canonical request before it reaches a provider and the stream
that comes back. It can change *what* is asked, retry the call, refuse it,
answer it from memory, or observe it. It cannot change how a provider encodes
the request. For that, use `RequestOptions.OnPayload`.

Five middleware ship in [`middleware`](../../middleware):

| Middleware | What it is for |
|---|---|
| `Retry(RetryOptions)` | Retries **transient** failures, chosen by the error text. Denylisted errors (quota, billing) are never retried. |
| `RateLimit(perSecond, burst)` | A token bucket over model calls. |
| `Budget(maxUSD, usageFn)` | Refuses a call **before** it is sent once the money is spent. |
| `Caching(CacheOptions)` | In-process dedup: a byte-identical request is answered from an LRU. |
| `Tracing(core.Tracer)` | One span per model call with model, tokens, cost, stop reason and the cache decision. |

The example runs with **no API key and no network**. Each section scripts a
[`provider/faux`](../../provider/faux) turn for the case that middleware
exists for: a 503, a burst, an exhausted budget, a repeated question. With a
credential, `--real` puts the whole recommended stack in front of a real model.

```bash
go run ./examples/middleware
go test ./examples/middleware/

export GEMINI_API_KEY=...            # or any vendor; see examples/README.md
AGENTKIT_MODEL=google/gemini-3.8-flash go run ./examples/middleware --real
```

## What it shows

```
── 1. the last registered middleware is the outermost ────────────────────
  C (registered last) →  B →  A (registered first) →  ← A (registered first)  ← B  ← C (registered last)

── 2. Retry: a transient failure is retried, a billing one is not ────────
  retry: backing off 200ms before attempt 2
  answer: "Recovered on the second attempt." after 2 provider calls
  Agent.Usage() = $0.006 — the discarded attempt was billed, so it is counted

── 4. Budget: refuse the NEXT call once the money is spent ───────────────
  call 3: spent so far $0.012 → ok (stop: end_turn)
  call 4: spent so far $0.012 → refused: budget of $0.0100 is already spent (stop: error)

── 5. Caching: identical requests are answered from memory ───────────────
  provider calls: 2; Level 2 hits 1, misses 2, saved $0.003

── 6. Tracing: one span per model call, cache decision included ──────────
  span agentkit.model_call: cache.hit=true cache.tier=dedup cost_usd=0.003 ... stop_reason=stop tool_count=0
```

With `--real` against Gemini:

```
── 7. the whole stack against a real model ───────────────────────────────
  span agentkit.model_call: cache.hit=false ... model=gemini-3.8-flash ... stop_reason=stop
  run 1: "Bug"
  span agentkit.model_call: cache.hit=true ...
  run 2: "Bug"
  provider calls: 1; cache hits 1, misses 1; avoided $0.00047
```

## Ordering: the last registered is the outermost

`cfg.Middleware` is applied so that the **last** element wraps all the
others. It sees the request first and the response last. The recommended
stack, from section 7:

```go
cfg.Middleware = []core.Middleware{
    middleware.RateLimit(2, 2),                          // innermost: every network attempt waits, retries included
    middleware.Retry(middleware.RetryOptions{MaxAttempts: 3}),
    middleware.Budget(0.05, spentSoFar),                 // refuse before sending once the money is gone
    middleware.Caching(middleware.CacheOptions{Meter: meter}), // a hit sends nothing, so it is served even past the budget
    middleware.Tracing(tracer),                          // outermost: sees every call, and whether it was a hit
}
```

- **Tracing outside Caching** is what gives a span its `cache.hit`,
  `cache.tier` and `cache.fingerprint` attributes. Registered the other way
  round, the span still has every other attribute and simply lacks those.
- **RateLimit inside Retry** means each retry attempt takes a token. Outside
  Retry, a retry storm would pass the limiter once.
- **Budget inside Caching** lets a cache hit, which sends no request, through
  even when the budget is spent.

## The code an application copies

**Retry.** `MaxAttempts` counts the first attempt, and the default is 1, which
means no retries. Hidden retries multiply cost per turn. Retries are chosen
by the error *text* (`middleware.Retryable`), because truncated streams, DNS
failures and gateway bodies arrive as messages, not status codes. The
denylist is checked first, so `insufficient_quota` is never retried even
though it contains "rate limit". A server's `Retry-After` is honoured up to
`MaxServerDelay` (60s by default). An attempt that is thrown away was still
billed, and its usage is added to `Agent.Usage()`.

**Budget.** It reads spend through a function, and the spend lives on the
agent, which you are still constructing. Bind it late:

```go
var agent *agentkit.Agent
cfg.Middleware = []core.Middleware{
    middleware.Budget(0.50, func() core.Usage { return agent.Usage() }),
}
agent, err := agentkit.NewAgent(cfg)
```

`stop.OverBudget` and `middleware.Budget` complement each other.
`stop.OverBudget` ends a run cleanly *after* a turn, so a run can go over by
one turn. `Budget` refuses *before* a call, so it is the hard ceiling. Use
both.

**Caching.** Requests are fingerprinted from their serialized bytes. Within
one agent the history grows with every turn, so requests rarely repeat. The
cache pays off when many short-lived agents ask the same thing, such as a
classifier or a fan-out of subagents. **Share one `Caching` value** across
them: each call to `middleware.Caching(...)` makes a new, empty LRU.

**Two levels of cache statistics.** Level 2 (this middleware's hits and
misses) is counted on the `CacheMeter` you pass in `CacheOptions.Meter`.
Level 1 (the provider's own prompt cache) is counted on every agent from the
usage the provider reports. `Agent.CacheStats()` returns it with no
middleware at all:

```go
cs := agent.CacheStats()
cs.ProviderCacheReadTokens, cs.EstimatedSavingsUSD
```

To see Level 2 counts in `Agent.CacheStats()` as well, pass `agent.Meter()`
as the `Meter`. `Middleware` is fixed when the agent is constructed, so you
have to bind it late, the same way as Budget above.

**Tracing.** Implement `core.Tracer` (`StartSpan(name, fn func(core.Span) error) error`).
A span lives until `Span.End`, **not** until `fn` returns: a model call's span
ends when its response completes, on another goroutine. An OpenTelemetry
adapter calls its span's `End` from `Span.End`. Tool calls do not go through
middleware. They get spans from `AgentConfig.Tracer` (see
[`observability`](../observability)). Pass the same tracer to both to get one
trace.

## Gotchas

- **A cache hit reports the original response's usage.** The cached message
  is replayed with the `Usage` it was stored with. So `RunResult.Usage`,
  `Agent.Usage()`, a span's `cost_usd` and `stop.OverBudget` all count a hit
  as if it had been paid for again. Use `CacheMeter.Stats()` or a counting
  middleware (section 7) to tell hits from requests that were actually sent.
- **`Temperature > 0` bypasses the cache**, unless you set
  `CacheOptions.IgnoreTemperature`. Replaying a sampled answer the caller
  asked to be random is a correctness bug.
- **`RateLimit(0, …)` is an error, not "unlimited".** Every call through it
  fails with a message saying so.
- **One limiter per quota.** A `RateLimit` value made for each agent limits
  nothing. Make it once and share it.
- **Compaction summaries bypass middleware on purpose.** See
  [`compaction`](../compaction).
- **A panicking middleware does not crash the run.** It becomes an error turn,
  and `Hooks.OnError` receives the panic.

## See also

- [`middleware`](../../middleware): `Retry`, `Retryable`, `RateLimit`,
  `Budget`, `Caching`, `Fingerprint`, `Tracing`, `CacheMeter`, `CacheStats`
- [`core.Chain`](../../core/config.go): the ordering rule, in code
- [`testing`](../testing): testing your own middleware against `faux`
