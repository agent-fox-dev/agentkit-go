# Run the agent on Anthropic, OpenRouter or Ollama

Status: **proposed** (2026-10-21). Nothing here is implemented. Reverses
[PRD 10](10-cut-agentkit-down-to-the-hands.md)'s "multi-vendor support"
non-goal for two providers, OpenRouter and Ollama, and brings back the model
tiers that PRD 10 left in agent-fox. Every vendor fact below was measured
against the live services on 2026-10-21 (§ Background, "The probe").

## Intent

An embedder chooses what runs an agent with three settings: the **provider**
(`anthropic`, `openrouter` or `ollama`), the **model**, and the **effort**. A
tier name — `SIMPLE`, `STANDARD` or `ADVANCED` — picks a model and an effort
for the chosen provider, so whoever configures a run does not need to know
which model id is current; naming a model and an effort directly overrides the
tier. Each provider is reached through its own vendor's Go SDK and native API,
so each behaves the way its vendor documents, and no vendor's requests pass
through another vendor's endpoint or a translation layer.

## Goals

1. `agentkit.New(agentkit.Config{Provider: "openrouter", Model: "STANDARD", …})`
   runs a tool-using agent to a final answer on `qwen/qwen3.8-flash` at medium
   effort; the same with `Provider: "ollama"` runs on `qwen3.8:27b`; with
   `Provider: "anthropic"` (or empty) it behaves exactly as today.
2. Provider, model and effort are the only settings needed to choose a model.
   A tier fills in model and effort; an explicit effort overrides the tier's;
   an explicit model id bypasses the tier table.
3. Each provider runs on its vendor's SDK: Anthropic on
   `github.com/anthropics/anthropic-sdk-go`, OpenRouter on
   `github.com/OpenRouterTeam/go-sdk` pinned at v0.9.66, Ollama on
   `github.com/ollama/ollama/api`. A test proves no request for one provider is
   sent to another provider's host.
4. A credential for one provider never reaches another provider's host,
   proven by a test per pair.
5. `core.Usage` is right on every provider: input tokens net of cache reads,
   reasoning tokens a subset of output, and cost as the vendor reports it
   (OpenRouter) or as the catalog prices it.
6. One offline conformance suite runs the same logical turns — text, a tool
   call and its result, thinking, a cut-off stream, an error — through all
   three providers against local test servers. Opt-in live smoke tests run one
   tool loop per provider.
7. Every existing Anthropic and scripted-provider (`provider/faux`) behaviour
   and test is unchanged, and `make check` passes.

## Non-goals

- **Compatibility endpoints.** No OpenRouter "Anthropic skin", no Ollama
  `/v1/messages`, no OpenAI-compatible `/v1/chat/completions` on Ollama, no
  Anthropic wire pointed at a foreign base URL. (Constraint.)
- **Other providers.** No direct OpenAI, Google or other vendor. Vertex AI and
  Bedrock stay deployments of the `anthropic` provider, selected as today.
- **A provider registry or plug-in mechanism.** An embedder that needs another
  vendor still writes a `core.ProviderClient` and passes it as
  `Config.Client`.
- **Embedder-defined tiers.** The three tier names and their table are fixed
  per release; an embedder that wants another mapping names the model and
  effort directly.
- **Cross-provider routing or fallback**, and OpenRouter's own routing
  preferences, model fallbacks, plugins and server-side tools.
- **Explicit prompt-cache breakpoints** on OpenRouter and Ollama. Caching that
  the vendor applies by itself is still measured and reported.
- **Images, audio and files** on the two new providers.
- **Managing Ollama**: starting the server, pulling models, choosing its
  memory settings.
- **Ollama's hosted service** (`https://ollama.com`, `OLLAMA_API_KEY`). The
  `ollama` provider talks to a local or self-hosted server only.
- **Discovering model limits from the vendor.** Limits come from the catalog
  or the fixed fallbacks, never from OpenRouter's models list or Ollama's
  `/api/show`.
- **Reading provider, model or effort from the environment in the library.**
  The examples do; the library takes them from `Config`.
- **An effort clamping ladder.** An effort is sent as asked, or not sent when
  the catalog row says the model does not take it.
- **Changes to agent-fox.** Adopting this in agent-fox is a follow-up in that
  repository.

## Background

**Today.** `provider/anthropic` is the only wire, on `anthropic-sdk-go`
v1.79.1, with Vertex AI and Bedrock selected by environment
(`anthropic.Resolve`). `agentkit.Config` has `Client *sdk.Client` (an
Anthropic SDK client) or `Provider core.ProviderClient` (a test double or a
custom provider), plus `Model` and `Effort`. `catalog/catalog.json` is already
keyed by vendor but holds only Anthropic rows; `catalog.Lookup` strips a
leading `anthropic/` from the id, and the wire sends the stripped id. An id the
catalog does not list is served with a 1,000,000-token window, a 128,000-token
output cap and adaptive thinking.

**The past.** Until PRD 10, AgentKit had five hand-written wires (Anthropic,
OpenAI Chat Completions, OpenAI Responses, Gemini, Ollama native, about 9,100
lines with their shared code), a provider registry, per-model wire selection,
vendor compatibility profiles and an effort clamping ladder. PRD 10 deleted
them because nothing used them; `docs/archive/wires.md` keeps what they
taught. The tiers lived in agent-fox-legacy (`internal/agentrun/models.go`):
a (vendor, tier) pair named a catalog model and a thinking level, and its ADR
08 made effort a setting of its own with the precedence this PRD keeps.
agent-fox today builds the Anthropic provider itself
(`internal/brain/agentkit_adapter.go`) from `AF_MODEL`.

**Why now.** Running the same agent on a cheaper hosted model (OpenRouter) or
a local one (Ollama) is wanted, and the configuration should stay three
settings, not the registry and profiles PRD 10 removed.

**The probe (2026-10-21).** One two-turn tool loop (read a file, answer from
it) through each new SDK, at every effort:

- *OpenRouter*, go-sdk v0.9.66, Chat Completions, `qwen/qwen3.8-flash`:
  - the loop completes at efforts low, medium, high and max; tool call ids look
    like `call_<hex>` and arguments arrive as a JSON string;
  - reasoning streams as many `reasoning_details` fragments; replaying them on
    the next turn as received works;
  - usage reports `cost` in USD, and `prompt_tokens` *includes*
    `cached_tokens` (256 of 354);
  - a 429 arrives either as an error from the request or **inside an HTTP 200
    stream** as an error chunk with no finish reason;
  - the SDK's default retry covers 5xx only, not 429, and backs off for up to
    an hour.
- *Ollama*, `api` v0.40.7, native `/api/chat`, `qwen3.8:27b` on a 32 GB Apple
  M4:
  - the loop completes at think levels low, medium, high and max (max accepted
    silently); tool calls carry an id and tool results pair by it;
  - `done_reason` is `"stop"` on a turn that ends in a tool call;
  - `prompt_eval_count` *includes* `prompt_eval_cached_count`;
  - the tool schema type is a subset of JSON Schema: `additionalProperties`,
    `format` and bounds are dropped on the wire;
  - `/api/show` reports a 262,144-token context, but the server ran the model
    at 32,768, its default sized to GPU memory; `num_ctx` takes effect only
    when the model loads, and a different value for an already-loaded model is
    silently ignored.

## Requirements

### 1. Choosing provider, model and effort

- `Config.Provider` names the provider: `anthropic`, `openrouter` or `ollama`,
  matched without regard to case. Empty means `anthropic`. Any other value
  makes `agentkit.New` fail with an error that names the value and the three
  known providers; no request is sent.
- `Config.Model` is a tier name or the provider's own model id. Empty makes
  `New` fail, as today.
- `Config.Effort` is one of `low`, `medium`, `high`, `xhigh`, `max`, or empty.
- `Config.Client`, when set, is the `core.ProviderClient` the agent calls
  instead of building one from `Provider`; this is how tests (`provider/faux`)
  and custom providers plug in. `Provider` and `Model` still choose the catalog
  row that supplies limits and prices.
- The current `Config.Provider` (a `core.ProviderClient`) becomes
  `Config.Client`, and the Anthropic-only `Config.Client *sdk.Client` is
  removed. (Breaking; see Design Decisions.)
- The same resolution — tier, catalog row, credentials, provider client — is
  available to an embedder without building an agent, so one that wraps the
  provider (agent-fox hides tools this way) or checks credentials before a run
  gets exactly what `New` would use.

### 2. Tiers

- A tier name is `SIMPLE`, `STANDARD` or `ADVANCED`, matched without regard
  to case. A `Model` that matches a tier name is always the tier, never a
  model id.
- The tier table, per provider:

  | Provider | SIMPLE | STANDARD | ADVANCED |
  |---|---|---|---|
  | `anthropic` | `claude-sonnet-5-5`, medium | `claude-sonnet-5-5`, high | `claude-opus-5-5`, xhigh |
  | `openrouter` | `qwen/qwen3.8-flash`, low | `qwen/qwen3.8-flash`, medium | `qwen/qwen3.8-flash`, high |
  | `ollama` | `qwen3.8:27b`, low | `qwen3.8:27b`, medium | `qwen3.8:27b`, high |

- The table lives with the catalog, so a model change is a catalog change.
- Effort precedence: `Config.Effort` when set; otherwise the tier's effort
  when `Model` is a tier; otherwise none is sent and the vendor's default
  applies.
- The resolved provider, model id and effort are reported, so a run can say
  what it actually ran (the tier name alone is not enough).

### 3. Model ids and the catalog

- The model id is sent to the vendor exactly as written. Only on `anthropic`
  is a leading `anthropic/` accepted and removed, as today.
- The catalog holds rows per provider. New rows:
  - `openrouter` / `qwen/qwen3.8-flash`: 1,000,000-token window, 131,072-token
    output cap, priced at $0.15 / $0.47 per million input / output tokens and
    $0.016 per million cache reads (OpenRouter's list, 2026-10-21).
  - `ollama` / `qwen3.8:27b`: 32,768-token window (see §7) and 32,768-token
    output cap; zero price; thinking taken as a level.
- An id the catalog does not list is served, not refused, with fixed fallback
  limits and no price, and the run is flagged as unpriced, as on Anthropic
  today. Resolution makes no network request to find limits.
  - OpenRouter: 131,072-token window, 32,768-token output cap. OpenRouter's
    reported cost still applies (§8), so a run is unpriced only when a
    response carries no cost.
  - Ollama: 32,768-token window, also sent as `num_ctx`, and a 32,768-token
    output cap.

### 4. Credentials and endpoints

- `anthropic`: unchanged — `anthropic.Resolve`, Vertex AI, Bedrock and their
  variables.
- `openrouter`: the key is `OPENROUTER_API_KEY`. Without it, resolution fails
  before any request with an error naming the variable. `OPENROUTER_BASE_URL`
  optionally replaces `https://openrouter.ai/api/v1`, for a proxy or a test
  server.
- `ollama`: the host is `OLLAMA_HOST`, default `http://127.0.0.1:11434`; a
  host without a scheme gets `http://`. No credential is read or sent: a
  local or self-hosted Ollama server needs none.
- **Isolation.** Each provider reads only its own variables. An
  `ANTHROPIC_*` value is never sent to OpenRouter or Ollama, and an
  `OPENROUTER_API_KEY` never to Anthropic or Ollama. (Constraint.)
- Variables are looked up as today: `Options.Env` first, then
  `Options.Getenv`, then the process environment.
- Resolution makes no network request; an unreachable host is found on the
  first request.

### 5. Running a turn on OpenRouter and Ollama

- **Streaming.** Text, thinking and tool calls stream as `core` events in the
  order the vendor sends them.
- **Tool calls and results.** Each tool call keeps the vendor's id, and each
  result is sent back paired by that id. On OpenRouter the argument bytes are
  forwarded as received, never re-encoded; on Ollama the argument object keeps
  its key order.
- **The loop continues on tool calls**, whatever stop or done reason
  accompanies them.
- **A stream that ends without its final signal** (`finish_reason` on
  OpenRouter, `done: true` on Ollama) ends the turn with an error. A partial
  tool call from such a stream is never executed.
- **Thinking.** Reasoning text is reported as thinking, never as visible
  text. On the next turn it is sent back to the same provider in the form the
  vendor gave it: OpenRouter's `reasoning_details` as received, Ollama's
  thinking text on the assistant message. Repair never turns it into text.
- **Tool schemas.** OpenRouter receives each tool's full JSON Schema. Ollama
  receives the part of the schema its API carries; constraints outside it
  (`additionalProperties`, `format`, bounds) are dropped on the wire, and
  arguments are still validated against the full schema before any tool runs.
- **Request settings.** `MaxTokens` caps the response (OpenRouter
  `max_completion_tokens`, Ollama `num_predict`), never above the catalog's
  output cap. Temperature, top-p and stop sequences are sent when set.
- **Tool choice.** `auto` is the default on both. `none` is sent as such on
  OpenRouter; Ollama has no tool choice, so `none` sends no tools.
- **Effort.** OpenRouter receives the effort as `reasoning.effort`. Ollama
  receives it as the `think` level. When the catalog row says the model does
  not take an effort, none is sent.

### 6. Errors and retries

- **Authentication.** A 401 or 403 ends the turn with an error that names the
  provider and the variable to check.
- **OpenRouter retries.** A 429 or 5xx response is retried with bounded
  backoff, honouring `Retry-After`, up to the provider's retry count
  (`Options.MaxRetries`, with the same default as the Anthropic provider). A
  429 or 5xx that arrives as an error chunk inside a stream before any content
  is retried the same way; after content has streamed, it ends the turn with
  an error carrying the vendor's code and message.
- **Ollama.** An unreachable host ends the turn with an error naming the host
  and suggesting `ollama serve`. A model that is not pulled ends it with an
  error naming the model and suggesting `ollama pull <model>`. Any other error
  carries Ollama's own message.
- **Cancellation.** Cancelling the run's context stops the request promptly
  on every provider.
- No error message, log line or event contains a credential.

### 7. Ollama's context window

Ollama runs a model at the context length fixed when the model loads: its
GPU-memory-sized default unless the loading request asked for another. The
window the agent prunes against has to match the one running.

- Every request sends the catalog row's window (or the fallback, §3) as
  `num_ctx`, so a model this provider loads runs at that length.
- Every request sends `truncate: false`. A request longer than the running
  context then fails with Ollama's error, which ends the turn naming the
  model and the window, instead of Ollama silently dropping the start of the
  transcript.
- `qwen3.8:27b`'s row is 32,768 tokens, the length it ran at on the reference
  machine (32 GB Apple M4).
- A model already loaded at another length by some other client keeps that
  length until it unloads; Ollama ignores `num_ctx` for it. This is
  documented, not detected: when the running length is smaller, an
  over-long request fails loudly as above.

### 8. Usage and cost

- Input tokens are net of cache reads: OpenRouter `prompt_tokens −
  cached_tokens`; Ollama `prompt_eval_count − prompt_eval_cached_count`.
  Cache reads are reported as such.
- Reasoning tokens, where reported, are a subset of output tokens.
- **OpenRouter cost** is the `cost` OpenRouter reports for the request. When
  a response carries none, the catalog price applies; with neither, the cost
  is zero and the run is flagged unpriced.
- **Ollama cost** is zero: the model runs on the embedder's own hardware.
- `MaxCostUSD` stops a run on the cost reported this way.

### 9. Documentation and examples

- `docs/configuration.md`: the three settings, the tier table, the new
  variables and the isolation rule.
- `docs/api.md` and `docs/architecture.md`: the new providers and the
  `Config` change. `README.md`: the quick start shows choosing a provider.
- `docs/DEPS.md`: a ruling for the OpenRouter SDK and for the Ollama `api`
  package and each module it brings in.
- The examples take `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and
  `AGENTKIT_EFFORT`, and their READMEs show a run on each provider.

### 10. Tests

- Offline tests for each new provider against a local HTTP server that
  records the request: model id as written, tools and schemas, effort, token
  cap, paired tool results, replayed reasoning, credential headers.
- The conformance suite of Goal 6, selecting cases by name.
- Opt-in live smoke tests, one tool loop per provider, run only when the
  provider's variables (and, for Ollama, a reachable server with the tier
  model) are present; skipped otherwise.

## Design Decisions

1. **Each provider on its own vendor's SDK and native API.** The user's
   constraint: each provider behaves as its vendor documents, and a vendor's
   change is absorbed by its SDK.
2. **Both new providers live in the root module**, so choosing one is a
   string in `Config` and needs no registry; every embedder downloads the
   Ollama `api` package's dependencies.
3. **The OpenRouter SDK is pinned at v0.9.66.** It is beta and may break in a
   0.x minor release; an upgrade is a deliberate change.
4. **Tiers are presets, not a separate model.** They resolve to a model id and
   an effort; everything else comes from the catalog row of that id.
5. **One model per non-Anthropic provider for all tiers**, differing by
   effort (low / medium / high): `qwen/qwen3.8-flash` on OpenRouter,
   `qwen3.8:27b` on Ollama. `qwen3.8-flash-next`, Ollama's "flash" model,
   needs more than 105 GB and does not run on the reference machine.
6. **`Config.Provider` becomes the provider's name**; the client field is
   `Config.Client core.ProviderClient`, and `*sdk.Client` leaves `Config`.
   agent-fox is the only consumer, and its change is one field.
7. **OpenRouter's reported cost is authoritative** over the catalog price:
   it is what the account is charged, including provider routing.
8. **The library reads no selection variables.** Credentials come from the
   environment as today; provider, model and effort come from `Config`.
9. **Ollama's window is sent, not discovered.** `num_ctx` from the catalog on
   every request and `truncate: false`: no extra request, and a mismatch
   fails loudly rather than losing history silently.
10. **Unlisted ids get fixed conservative limits** (OpenRouter 131,072 /
    32,768; Ollama 32,768 / 32,768) rather than a lookup at the vendor, so
    resolution never touches the network.
11. **Only local or self-hosted Ollama.** The hosted service is billed by
    subscription and needs a key, which would make cost and credential
    handling a second case for little use.

## Dependencies

- **PRD 10** — reverses its multi-vendor non-goal for OpenRouter and Ollama;
  keeps the rest of its cut (no registry, no compatibility profiles).
- **Spec 10 (`10_anthropic_wire`)** — the Anthropic provider and
  `anthropic.Resolve` are unchanged; the catalog's id handling and row keys
  change.
- **PRD 12 (embedder contract, specs 20–24)** — `agentkit.Config` changes
  (Design Decision 6).
- **`docs/DEPS.md` and `internal/policy`** — two new direct dependencies need
  rulings, and the exported-API golden changes.
- **External:** OpenRouter's Chat Completions API and Ollama's `/api/chat`, as
  measured on 2026-10-21; Ollama ≥ 0.40.7 for tool-call ids.
- **agent-fox** — consumes the new resolution in a follow-up.
