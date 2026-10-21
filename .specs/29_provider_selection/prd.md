---
spec_id: "29"
spec_name: "provider_selection"
title: "Choose the provider, model and effort from agentkit.Config"
status: "active"
created_at: "2026-10-21T11:03:27.256894Z"
updated_at: "2026-10-21T11:03:27.256894Z"
intent_hash: "d23c21c627d835260e383677a1310a4f581564d491226e9865e6f8444c9727de"
schema_version: 2
source: "docs/prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md"
---
## Intent

An embedder picks what runs an agent with three `agentkit.Config` settings: the provider name (`anthropic`, `openrouter` or `ollama`), the model (a tier name or the provider's own model id) and the effort. The agent then builds the matching provider client itself. Each provider reads only its own credentials and endpoint variables. The same resolution is also exported, so an embedder can see, check or wrap exactly what `agentkit.New` would use without building an agent. A run reports which provider, model and effort it actually used, and whether its cost could be known.

## Goals

1. **The two new providers are selected by name.**
   - `agentkit.New(Config{Provider: "openrouter", Model: "STANDARD", Getenv: …})` runs a prompt against a local recording server. `OPENROUTER_API_KEY` is set and `OPENROUTER_BASE_URL` points at the server. The recorded request carries model `qwen/qwen3.8-flash` and `reasoning.effort` `medium`.
   - The same with `Provider: "ollama"` and `OLLAMA_HOST` pointing at the server records model `qwen3.8:27b`, `think` `medium` and `options.num_ctx` 32768.
   - In both cases `Agent.Resolution()` reports the provider, tier `STANDARD`, the model id, effort `medium` and `Listed` true.
2. **Anthropic behaves as today.** `Provider: "anthropic"` or empty, with `Model: "claude-opus-5-5"`, sends a request body byte-identical to one from `anthropic.Provider(row, anthropic.Options{…})` built directly with the same environment.
3. **Bad configuration fails before any request.** `agentkit.Resolve` and `agentkit.New` return:
   - `catalog.ErrUnknownProvider` for an unknown provider name;
   - `catalog.ErrNoModel` for an empty model;
   - `catalog.ErrUnknownEffort` for an unknown effort;
   - `openrouter.ErrNoAPIKey` when the provider is `openrouter` and `OPENROUTER_API_KEY` is missing;
   - `anthropic.ErrNoCredentials` when the provider is `anthropic` and there is no Anthropic credential.

   `ollama` needs no credential. Recording servers count zero requests after any successful `Resolve` or `New`.
4. **Isolation is proven for every pair of providers.** All three providers' secrets are set, and each provider's base-URL variable points at its own recording server. A run on each provider in turn shows that:
   - only that provider's server receives requests;
   - neither of the other two secrets appears in any recorded header or body;
   - the injected `Getenv` is asked for no other provider's variable and no `AGENTKIT_*` variable.
5. **A test double or custom client still plugs in.** With `Config.Client` set, the agent calls that client. No credential variable is read. `Provider` and `Model` still choose the catalog row and effort.
6. **A run whose cost cannot be known is flagged.** `RunResult.Usage.Unpriced` is true for:
   - an Anthropic run on an unlisted model id that reported usage;
   - an OpenRouter run on an unlisted id where a response carried no cost.

   It is false for listed models, for OpenRouter responses that carried a cost, and for every Ollama run.
7. **Nothing else moves.** Every existing test passes after the mechanical rename of the client field. The root package no longer imports `github.com/anthropics/anthropic-sdk-go`. The exported-API golden is regenerated. `make check` passes.

## Non-goals

Later specs of this split cover the following:

- **`provider_conformance`**:
  - the cross-provider conformance suite: text, tool call and result, thinking, cut-off stream, error;
  - the opt-in live smoke tests;
  - the examples reading `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and `AGENTKIT_EFFORT`;
  - the README quick start that shows choosing a provider.

  This spec only keeps the existing examples and README snippets compiling and correct after the `Config` change.

Earlier specs own the following. This spec does not change them:

- **`26_provider_catalog_tiers`**: catalog rows, tiers, fallbacks and `catalog.Select`.
- **`27_openrouter_provider`** and **`28_ollama_provider`**: everything a provider does on the wire, including its own variable lookup, errors, retries, usage netting and reported cost.
- **`10_anthropic_wire`**: `anthropic.Resolve` and `anthropic.Provider`.

Out of scope altogether:

- Exposing provider options other than the environment through `Config`. This covers `HTTPClient`, `MaxRetries`, `Warnf`, Vertex options, `Betas`, `Headers` and so on. An embedder that needs them builds the provider itself and passes it as `Config.Client`.
- Reading a provider, model or effort from the environment in the library.
- A provider registry or plug-in mechanism. Cross-provider routing or fallback.
- Changing `TurnEndEvent.Usage`. It stays the provider's own usage of that turn.
- Detecting the length an Ollama model is actually loaded at.
- Changes to agent-fox.

## Background

**Today's driver** (`agent.go`).

- `Config` has twelve fields:
  - `Client *sdk.Client`, an Anthropic SDK client;
  - `Provider core.ProviderClient`, a test double or custom provider that wins when set;
  - `Model string`;
  - `Effort Effort`;
  - then `System`, `Prefix`, `Toolset`, `MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune` and `MaxTokens`.
- `New` checks its input in this order:
  1. it refuses a config with neither `Client` nor `Provider` ("needs an Anthropic client or provider");
  2. it refuses an empty `Model`;
  3. it validates the `Toolset` through `NewRunner`;
  4. it calls `catalog.Lookup(cfg.Model)` and discards the `listed` bool;
  5. without `Provider`, it builds `anthropic.Provider(m, anthropic.Options{Client: cfg.Client})`.
- Only `agent.go` imports the Anthropic SDK in the root's non-test code, and only for the `Config.Client` type.

**The loop** (`loop.go`).

- `callModel` sends `Effort: a.cfg.Effort` verbatim, with no validation or normalization.
- The run pushes `core.AgentStartEvent{Model: a.model.ID}`. The event's existing `Provider` and `API` fields are never filled.
- `Agent.priced` prices a turn at the catalog row when the provider left the `core.UsageCostUSD` bit clear, and always sets the bit. So a zero cost from an unlisted row cannot be told apart from a free one.
- There is no "unpriced" flag anywhere. The input's "flagged as unpriced, as on Anthropic today" describes something that does not exist yet (spec 26 Background says the same).

**The Anthropic provider** (`provider/anthropic`).

- `anthropic.Resolve(env Env)` chooses the deployment (direct, Vertex AI or Bedrock) and builds an SDK client from the environment. It makes no network request.
- `anthropic.Provider(m, Options{})` with a nil `Options.Client` resolves the same way on every request. It reads `Options.Env`, then `Options.Getenv`, then the process environment (`client.go`, `providerEnv`). A missing credential is only found on the first request, as an error in the stream.
- The provider always sets the cost bit from the catalog row (`stream.go`, `price`). Anthropic's API reports no cost of its own.

**What the earlier scopes deliver.**

- Spec 26: `catalog.Select(provider, model, effort) (Selection, error)`, which returns `Provider`, `Tier`, `Model`, `Effort` and `Listed`, with sentinel errors prefixed `agentkit:`.
- Spec 27: `openrouter.New(m, openrouter.Options{Env, Getenv, …}) (core.ProviderClient, error)`. It fails with `ErrNoAPIKey`. It leaves the cost bit clear on a response that carried no cost and has no catalog price, explicitly so this spec can build the run-level flag on it.
- Spec 28: `ollama.New` on the same pattern. It reads only `OLLAMA_HOST` and prices every response at zero with the cost bit set.

**Who uses the fields being changed.**

- About 75 `Config{Provider: …}` literals in root tests, plus:
  - `codemode/smoke_test.go`;
  - `sandbox/codemode_smoke_test.go` and `sandbox/integration_test.go`;
  - `examples/agentdemo`, `codemode`, `gateway`, `harness` and `sandbox`.
- `Config{Client: client}` with an SDK client in:
  - `examples/codingagent`, `customtools` and `mcp`;
  - `README.md`, `examples/README.md` and `docs/configuration.md`.
- Two tests pin the Agent to six methods: `TestAgentSurfaceIsSixMethods_TS11_9` and `TestAgentExportsSixMethods_TS21_13`.
- The root and `core` packages are recorded in `internal/policy/testdata/exported-api.golden`.

## Requirements

### 1. The `Config` fields that choose a model

- `Config.Client` is a `core.ProviderClient`. When it is set, the agent calls it instead of building a client. This is how `provider/faux`, test doubles and custom or wrapped providers plug in.
- `Config.Provider` is a `string` naming the provider: `anthropic`, `openrouter` or `ollama`. It is matched as `catalog.Select` matches it: trimmed and without regard to case. Empty means `anthropic`. It names the catalog provider even when `Client` is set.
- `Config.Model` is a tier name or the provider's own model id, and `Config.Effort` is one of the five efforts or empty. Both are as `catalog.Select` reads them.
- `Config.Env map[string]string` and `Config.Getenv func(string) string` supply credential and endpoint variables:
  - a non-empty `Env` value is used first; an empty one falls through;
  - next comes `Getenv` when it is set, otherwise the process environment;
  - an empty value counts as unset.

  This is the order every provider package already uses.
- The field order is: `Client`, `Provider`, `Model`, `Effort`, `Env`, `Getenv`, then the existing `System` … `MaxTokens` unchanged.
- The Anthropic SDK client field is removed. The root package's non-test code no longer imports `github.com/anthropics/anthropic-sdk-go`.

### 2. `agentkit.Resolve`: from a config to what will run

`agentkit.Resolve(cfg Config) (Resolution, error)` reads only `Client`, `Provider`, `Model`, `Effort`, `Env` and `Getenv`.

- It calls `catalog.Select(cfg.Provider, cfg.Model, cfg.Effort)`. Any error is returned unchanged, so `errors.Is` matches `catalog.ErrUnknownProvider`, `ErrNoModel` and `ErrUnknownEffort`, and the message names the value and the valid choices. This applies even when `Client` is set.
- `Resolution` has these fields:
  - `Provider string`: the canonical name;
  - `Tier catalog.Tier`: empty when a model id was named;
  - `Model core.Model`: the catalog row or fallback, whose `ID` is what the vendor receives;
  - `Effort Effort`: the effort requested, normalized; empty means none is sent;
  - `Listed bool`;
  - `Client core.ProviderClient`: the client a run would call.
- It makes no network request and keeps no global state. Each call builds a new client. Calling it twice with the same config gives equal selections.

### 3. Building the provider client and checking credentials

When `Config.Client` is nil, `Resolve` builds the client for the resolved provider from `Resolution.Model`, passing `Env` and `Getenv` through:

- **`anthropic`**:
  - it first runs `anthropic.Resolve` over an `anthropic.Env` that applies the lookup order of §1, so a missing credential or a Vertex selection without a project fails now, with that function's error;
  - it then builds `anthropic.Provider(model, anthropic.Options{Env: cfg.Env, Getenv: cfg.Getenv})`, which resolves the deployment per request exactly as an embedder's own provider does today.
- **`openrouter`**: `openrouter.New(model, openrouter.Options{Env: cfg.Env, Getenv: cfg.Getenv})`. Its error (for example `ErrNoAPIKey`, or a malformed `OPENROUTER_BASE_URL`) is returned unchanged.
- **`ollama`**: `ollama.New(model, ollama.Options{Env: cfg.Env, Getenv: cfg.Getenv})`. Its error, if any, is returned unchanged. No credential is read.

When `Config.Client` is set, `Resolution.Client` is that client, and no variable is read at all. An unreachable host is found on the first request, not by `Resolve`.

### 4. Credential and endpoint isolation

- `Resolve` reads no variable itself. Every lookup goes through the chosen provider's own code, and only the chosen provider's code runs. No value read for one provider is passed to another provider's client.
- The library never reads `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL`, `AGENTKIT_EFFORT` or any other selection variable.
- This holds at agent level for each ordered pair of providers:
  - an Anthropic credential (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`) never reaches the OpenRouter or Ollama server;
  - `OPENROUTER_API_KEY` never reaches the Anthropic or Ollama server;
  - `OLLAMA_API_KEY`, even when set, never reaches any server;
  - each run's requests go only to the chosen provider's host.

### 5. `agentkit.New`

- `New(cfg)` runs `Resolve(cfg)` first, then validates `cfg.Toolset` with `NewRunner`. Its errors come in this order:
  1. provider;
  2. model;
  3. effort;
  4. credentials;
  5. tool set.

  Each error is returned unchanged, and no request is sent.
- The agent uses the resolution throughout:
  - `Resolution.Model` is the row for pruning and the context-window check, for pricing and for stamping messages;
  - `Resolution.Client` is the client it calls;
  - every request carries `Resolution.Effort`, not the raw `Config.Effort`. So a tier's effort is sent, and `"HIGH"` is sent as `high`.
- A tier given as `Model` resolves to the tier's model id everywhere the agent names its model. This covers `AgentStartEvent`, message stamping and the context-window error.

### 6. Reporting what runs

- `(*Agent).Resolution() Resolution` returns the resolution the agent was built with. It is a copy: changing its `Model.Efforts` map or `Model.Compat` bytes does not affect the agent. It is safe to call from any goroutine during a run. The Agent's surface becomes seven methods.
- Each run's `core.AgentStartEvent` carries:
  - `Provider`: the resolved provider name;
  - `Model`: the resolved model id;
  - `API`: the API constant of the provider package `Resolve` built (`anthropic.API`, `openrouter.API` or `ollama.API`), or empty when `Config.Client` was given.

### 7. Runs whose cost is unknown

- `core.Usage` gains `Unpriced bool`. It is true when at least one request the usage covers had a cost that neither the vendor reported nor the catalog priced, so `CostUSD` counts that request as zero. `Usage.Add` ORs it.
- The event JSON form (`core.UsageJSON`) carries it as `unpriced`.
- The agent marks a turn's usage `Unpriced` when it prices that turn, if all of the following hold:
  - the usage was reported (`Usage.Reported()`);
  - the resolved model has all four per-million rates at zero;
  - either the provider is `anthropic` (Anthropic reports no cost of its own), or the provider is `openrouter` and the response left the cost bit clear.
- A turn on `ollama` is never unpriced: zero is its real price. This rule follows the resolved provider name, whichever client is called.
- The flag reaches `RunResult.Usage`, `AgentDoneEvent.Result.Usage`, `AgentDoneEvent.Usage` and `Agent.Usage()`.
- Pricing itself is unchanged:
  - an unpriced turn still gets the computed cost (zero) with the cost bit set, as today;
  - `MaxCostUSD` still reads `CostUSD`.

### 8. Existing behaviour and migration

- `Provider` empty or `anthropic`, with a listed or unlisted Claude id, uses the same catalog row as today's `catalog.Lookup`. It sends the same request body, and its pruning, pricing and stop behaviour are unchanged.
- Tests that pass a test double rename `Provider:` to `Client:`. This covers:
  - the root package tests;
  - `codemode`, the `sandbox` module, the example programs, the nested example modules;
  - any `provider/openrouter` and `provider/ollama` tests that build an agent.

  Their assertions do not otherwise change.
- The spec-11 and spec-21 tests that pinned the old shape are restated:
  - TS-11-1: the fourteen fields;
  - TS-11-2: no client and no Anthropic credential fails with `anthropic.ErrNoCredentials`;
  - TS-11-5: `Client` is called, and `Provider`/`Model` choose the row;
  - TS-11-9 and TS-21-13: seven methods, including `Resolution`.
- `examples/codingagent`, `customtools` and `mcp` drop their SDK-client resolution. They set `Model` and let `New` resolve, and still fail before any request with the variables to set.

### 9. Documentation and the exported API

- `docs/configuration.md`:
  - the `agentkit.Config` table, with `Client`, `Provider`, `Model`, `Effort`, `Env` and `Getenv`;
  - a section for `agentkit.Resolve`, `Resolution` and `Agent.Resolution`;
  - the isolation rule and how `Env`/`Getenv` reach each provider;
  - `New` failing early on missing credentials;
  - `Usage.Unpriced` in the `MaxCostUSD` row.
- `docs/api.md` and `docs/architecture.md`:
  - the root's imports (`provider/openrouter` and `provider/ollama` added, the Anthropic SDK no longer imported);
  - the Agent's seven methods;
  - the `Config` change.
- `README.md` and `examples/README.md` snippets that used an SDK client as `Config.Client` are corrected.
- `make doc` regenerates `docs/generated/agentkit-go.txt` and `docs/generated/core.txt`.
- The exported-API golden is regenerated with `AGENTKIT_UPDATE_API_GOLDEN=1`, changing only the root section (`Config`, `Resolve`, `Resolution`, `Agent.Resolution`) and the `core` lines for `Usage` and `UsageJSON`.

## Design Decisions

1. **`Config.Provider` becomes the provider name, `Config.Client` becomes the `core.ProviderClient`, and the SDK client field is removed.** This is the input's Design Decision 6. A provider name selects a built-in provider with no registry, and an SDK-typed field would be Anthropic-only.
2. **`Config` gains `Env` and `Getenv`, placed after `Effort`.** The input says variables are looked up "Options.Env first, then Options.Getenv, then the process environment". Without these fields neither tests nor embedders could supply credentials without touching the process environment. Placing them before `System` keeps the tail of the golden line that TS-21-55 checks.
3. **The exported resolution is `agentkit.Resolve(cfg Config) (Resolution, error)`, and `New` is `Resolve` plus the tool side.** It takes the same `Config`, so "exactly what `New` would use" holds by construction. An embedder that wraps the client sets `cfg.Client = wrap(res.Client)` and calls `New`, which then reads no credential.
4. **`Resolution` mirrors `catalog.Selection` and adds `Client`. It has no static "unpriced" field.** Whether an OpenRouter run is priced depends on each response, so a static flag would be wrong for that provider. `Listed` is the exact static fact, and the run-level flag is in `core.Usage` (Decision 12).
5. **Errors from `catalog.Select` and from the provider constructors are returned unchanged.** They already name the value, the choices or the variable and carry sentinels, and spec 26 Decision 14 intended exactly this. It matches `New` returning `NewRunner`'s error unchanged.
6. **`Config.Provider` is validated even when `Config.Client` is set.** It picks the catalog row, the tier table and the unpriced rule, so an unknown name is a configuration error either way.
7. **Anthropic credentials are checked with `anthropic.Resolve` at `New`, but the provider is built with `Options{Env, Getenv}` rather than with the resolved SDK client.** The input wants credentials checked before a run. Passing a resolved `*sdk.Client` would make the provider report every deployment as direct in its errors and ignore `Env`, so its per-request behaviour would diverge from today's.
8. **The resolved effort is sent on every request, and unknown efforts are now rejected at `New`.** A tier's effort must reach the wire. `catalog.Select` already rejects and normalizes efforts. No existing caller passes anything but the five constants.
9. **The checks run in the order provider, model, effort, credentials, tool set.** This keeps today's "client and model first, then `NewRunner`" order, with the credential check where the old "no client" check was.
10. **The Agent gains a seventh method, `Resolution()`.** A caller holding only the agent (a logger, a budget gate) must be able to say what ran. Restating two method-count tests costs less than threading the config around.
11. **The existing `AgentStartEvent.Provider` and `API` fields are filled. No `Effort` field is added to the event.** The fields exist and are always empty today, so filling them changes no type. Effort is available from `Agent.Resolution()` and each assistant message's `Effort`, so `core` events and their JSON keys stay unchanged.
12. **The run-level flag is `core.Usage.Unpriced`, ORed by `Add`.** It is the only carrier that reaches `RunResult`, `AgentDoneEvent` and `Agent.Usage()` and stays set once any turn was unpriced. The cost presence bit cannot do this, because an OR loses it.
13. **The unpriced rule depends on the resolved provider name:**
    - Anthropic: unpriced when the row has no price.
    - OpenRouter: unpriced when the row has no price and the response carried no cost.
    - Ollama: never.

    This matches how each vendor bills: Anthropic reports no cost, OpenRouter's reported cost is authoritative, and Ollama runs on the embedder's hardware. Keying on the name rather than the client also covers an embedder that wraps the provider and passes it as `Client`.
14. **Pricing and the cost bit are otherwise unchanged.** `MaxCostUSD`, `CostUSD` and the cost bit behave as today for every existing caller. The flag is additive.
15. **The examples only migrate.** `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and `AGENTKIT_EFFORT` and the provider-choice quick start belong to `provider_conformance`. Here the examples only stop using the removed field, so the tree compiles.
16. **This spec's tests use one text turn per provider against minimal recording servers.** That is enough to check host, model id, effort and credentials. Tool loops, thinking, truncation and errors across providers are the conformance suite of `provider_conformance`.
17. **The environment lookup order is the one every provider package uses:**
    1. a non-empty `Env` value;
    2. `Getenv` when it is set, with no process fallback;
    3. otherwise the process environment.

    One rule across `Config` and the three providers means one isolation story. A test with `Getenv` set is fully hermetic.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `26_provider_catalog_tiers` | depends on | `catalog.Select`, `Selection`, `Tier`, the provider constants and `ErrUnknownProvider` / `ErrUnknownEffort` / `ErrNoModel` are the resolution `Resolve` applies. |
| `27_openrouter_provider` | depends on | `openrouter.New`, `Options{Env, Getenv}`, `openrouter.API` and `ErrNoAPIKey` build and check the `openrouter` client. Its "cost bit clear when unpriced" rule feeds §7. Its agent-level test's `Config{Provider: client}` is renamed to `Client`. |
| `28_ollama_provider` | depends on | `ollama.New`, `Options{Env, Getenv}` and `ollama.API` build the `ollama` client. Any of its tests that build an agent are renamed to `Client`. |
| `10_anthropic_wire` | depends on | `anthropic.Resolve`, `anthropic.Env`, `anthropic.Provider`, `Options.Env` / `Getenv`, `ErrNoCredentials` and `anthropic.API` are used unchanged. |
| `11_minimal_driver` | modifies | TS-11-1 (the `Config` fields), TS-11-2 (refusal with no client), TS-11-5 (`Provider` over `Client`) and TS-11-9 (six methods) are restated for the new `Config` and the seventh method. |
| `21_toolset_runner` | modifies | TS-21-13 pins six Agent methods. TS-21-55 reads the golden's `Config` line, and its tail substring still holds. |
| `24_embedder_contract_docs` | modifies | The exported-API golden's root and `core` sections are regenerated. TS-24-5 and TS-24-16 compare against the regenerated file. |

## Verified External API

This spec calls no third-party package directly. The SDKs are reached only through the provider packages. These are the in-repository symbols it relies on.

**Read in the tree:**

- `anthropic.Resolve(env Env) (*sdk.Client, Source, error)` (`provider/anthropic/resolve.go` L100). `anthropic.Env` is `interface{ Lookup(name string) (string, bool) }` (L58).
- `anthropic.ErrNoCredentials` (L53).
- `anthropic.Provider(m core.Model, opts Options) core.ProviderClient` (`provider/anthropic/stream.go` L103). `Options.Env map[string]string` and `Options.Getenv func(string) string`.
- `anthropic.API core.API = "anthropic-messages"` (`provider/anthropic/anthropic.go` L24).
- `core.AgentStartEvent{SessionID, Provider string; API core.API; Model string}` (`core/event.go` L73).
- `core.Usage` and `Usage.Add` (`core/usage.go`). `core.UsageJSON` and `usageJSON` (`core/eventjson.go` L230-250).

**NOT FOUND (delivered by earlier specs of this split, not yet in the tree). The signatures below are assumed from those specs' artifacts.**

- From `.specs/26_provider_catalog_tiers/requirements.json`, 26-REQ-6.1 and 7.1:
  - `catalog.Select(provider, model string, effort core.Effort) (catalog.Selection, error)`;
  - `catalog.Selection{Provider string; Tier Tier; Model core.Model; Effort core.Effort; Listed bool}`;
  - `catalog.Tier`;
  - `catalog.ProviderAnthropic`, `ProviderOpenRouter`, `ProviderOllama`;
  - `catalog.ErrUnknownProvider`, `ErrUnknownEffort`, `ErrNoModel`.
- From `.specs/27_openrouter_provider/tasks.json`:
  - `openrouter.New(m core.Model, opts openrouter.Options) (core.ProviderClient, error)`;
  - `openrouter.Options{Env map[string]string; Getenv func(string) string; HTTPClient *http.Client; MaxRetries *int; Now func() time.Time; Warnf func(string, ...any)}`;
  - `openrouter.API core.API = "openrouter-chat"`;
  - `openrouter.ErrNoAPIKey`;
  - `openrouter.APIKeyVar`, `openrouter.BaseURLVar`.
- **Unverified.** Spec 28's artifacts are not yet written. These are assumed from the `ollama_provider` PRD summary:
  - `ollama.New(m core.Model, opts ollama.Options) (core.ProviderClient, error)`;
  - `ollama.Options{Env map[string]string; Getenv func(string) string; …}`;
  - `ollama.API` (expected to equal `core.APIOllamaChat`, `"ollama-chat"`).

  If `ollama.New` returns no error, `Resolve` treats it as infallible.
