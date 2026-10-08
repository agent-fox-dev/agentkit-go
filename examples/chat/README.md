# chat — the smallest useful AgentKit program

`chat` resolves a model, asks it one question, and prints the answer and what
it cost. It is the template every other example builds on: the four steps in
`run()` — resolve, register, set a stop policy, check credentials — are the
ones any embedding application has to take.

The interesting part is what the model id does *not* carry. A string like
`anthropic/claude-sonnet-5` says nothing about the wire API, base URL, context
window, price or request quirks; `catalog.ResolveModel` supplies all of that.
That is why AgentKit can clamp `max_tokens`, choose the right request shape and
report a real dollar cost rather than an estimate.

## Run it

Needs a credential for the vendor of the model you pick (default
`anthropic/claude-sonnet-5`, so `ANTHROPIC_API_KEY`):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/chat "Explain the Go memory model in three sentences."
```

With no argument it asks "In one sentence: what is an agent loop?". There are
no flags. `AGENTKIT_MODEL` picks another model — any catalog vendor, and any
unknown id under a known vendor (it inherits that vendor's default row):

```bash
AGENTKIT_MODEL=openai/gpt-5.6-terra    go run ./examples/chat "hello"
AGENTKIT_MODEL=google/gemini-3.8-flash go run ./examples/chat "hello"
```

## What you'll see

The answer on stdout, then a usage line on stderr in this shape:

```
[claude-sonnet-5 · 1 turns · in <n> / out <n> tokens · cached <n> · $<cost>]
```

Without a credential it stops before any request is built and names the
variables to set:

```
$ go run ./examples/chat "hello"
error: no credential for vendor "anthropic": set one of ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_OAUTH_TOKEN, ANTHROPIC_VERTEX_BASE_URL, ANTHROPIC_BASE_URL (for a gateway or a local server) (see examples/README.md)
```

## Walkthrough

All in `main.go`:

1. **Resolve the model** — `catalog.ResolveModel(modelSpec())`. This is the
   single entry point; it returns a `*core.Model` with API, base URL, limits,
   pricing and compatibility profile.
2. **Register wire APIs** — `agentkit.RegisterDefaults(&cfg, anthropic.Provider(…), openai.Provider(…), …)`.
   Nothing is registered by import side effect, so a program that only wants
   the loop never links `net/http`. The example registers all five so any
   `AGENTKIT_MODEL` works; register only the ones you use.
3. **Set a stop policy** — `stop.Any(stop.AfterTurns(10), stop.OverBudget(1.00))`.
   Budget is cumulative dollars for the run.
4. **Pre-flight the credential** — `checkCredentials` calls
   `provider.ResolveAuth(authFor(m), provider.Env{})` and fails only when the
   state is `CredentialNone`. `ambient` (a base URL, an instance role, ADC)
   must pass. `authFor` maps the model's API to its vendor table;
   OpenAI-compatible vendors share one table keyed by vendor name
   (`openai.AuthFor(m.Provider)`).
5. **Run** — `agentkit.NewAgent(cfg)` then `agent.Run(ctx, prompt)`. The
   result has `FinalText()`, `TurnCount` and `Usage` (tokens, cache reads,
   `CostUSD` priced against the model that served the request).

## Gotchas

- Resolution succeeding does not mean the model exists: an unknown id under a
  known vendor resolves by cloning and then fails at the vendor.
- Vendors with a provider but no catalog rows (Ollama, Groq, OpenRouter…) are
  an *unknown vendor* error from `ResolveModel`; build a `core.Model` yourself
  or supply your own catalog.
- A base URL with no key passes the pre-flight (`ambient`) and may then 401.

## Related

[`examples/README.md`](../README.md) for the full credential and base-URL
tables. Packages: `catalog`, `provider` (`ResolveAuth`), `provider/*`, `stop`,
the root `agentkit` package. Next: [`streaming`](../streaming).
