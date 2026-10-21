---
spec_id: "26"
spec_name: "provider_catalog_tiers"
title: "Catalog rows and model tiers for Anthropic, OpenRouter and Ollama"
status: "active"
created_at: "2026-10-21T10:27:53.198261Z"
updated_at: "2026-10-21T10:27:53.198261Z"
intent_hash: "2dc7b6db62c7fd7839968527f9b8bc589b08d8cc9cf758a91c0dca0abfef313f"
schema_version: 2
source: "docs/prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md"
---
## Intent

An embedder chooses what runs an agent with three settings: the provider (`anthropic`, `openrouter` or `ollama`), the model and the effort. A tier name (`SIMPLE`, `STANDARD` or `ADVANCED`) picks a current model and effort for the chosen provider. This spec gives the model catalog that vocabulary. It covers the catalog rows for each supported provider, the rule for how each provider's model ids are matched, the fallback limits for ids the catalog does not list, the fixed tier table, and one offline function. That function turns a (provider, model, effort) triple into the catalog row and effort a run will use. The OpenRouter and Ollama providers and the agent's provider selection are built on it.

## Goals

1. `catalog.Select("openrouter", "STANDARD", "")` returns:
   - provider `openrouter` and tier `STANDARD`;
   - model `qwen/qwen3.8-flash`, with a 1,000,000-token window, a 131,072-token output cap and prices of $0.15 / $0.47 / $0.016 per million input / output / cache-read tokens;
   - effort `medium`, and the row reported as listed.

   `Select("ollama", "STANDARD", "")` returns `qwen3.8:27b` with 32,768 / 32,768 and zero price, at `medium`. `Select("anthropic", "STANDARD", "")` and `Select("", "STANDARD", "")` both return `claude-sonnet-5-5` at `high`.
2. Provider, tier and effort names are matched without regard to case. An explicit effort overrides the tier's effort. An explicit model id bypasses the tier table.
3. Three kinds of bad input fail with errors that `errors.Is` can match: an unknown provider, an unknown effort and an empty model. Each message names the value given and the valid choices.
4. An id the catalog does not list for a provider is served with that provider's fixed fallback limits and no price, and is reported as not listed. It is never refused, and nothing touches the network.
5. `catalog.Parse` rejects a malformed tier block with a `*ParseError` that names the path of the failing field.
6. `catalog.Lookup` keeps its current behaviour, and so does every current caller of it (`agentkit.New`, the examples, the Anthropic provider tests). `make check` passes.

## Non-goals

These belong to later specs of this split:

- **`openrouter_provider`**: the OpenRouter `core.ProviderClient` on `github.com/OpenRouterTeam/go-sdk`, including how an effort and a model id reach the wire, reported cost, retries and `OPENROUTER_*` variables.
- **`ollama_provider`**: the Ollama `core.ProviderClient` on `github.com/ollama/ollama/api`, including sending the window as `num_ctx`, `truncate: false` and `OLLAMA_HOST`.
- **`provider_selection`**: changing `agentkit.Config` (`Provider` as a name, `Client core.ProviderClient`), reading credentials with isolation, building the provider client, the resolution an embedder can call without building an agent, reporting the resolved provider, model and effort, and flagging a run as unpriced.
- **`provider_conformance`**: the cross-provider conformance suite, the live smoke tests, the examples' `AGENTKIT_*` variables and the README quick start.

Out of scope altogether:

- Changing `agentkit.Config` or `agentkit.New` in this spec. The root package keeps calling `catalog.Lookup(cfg.Model)`.
- Reading environment variables or credentials, and any network request.
- Tiers defined by the embedder, or overriding the tier table at run time. The table is fixed per release.
- Looking up model limits from the vendor (OpenRouter's models list, Ollama's `/api/show`).
- An effort-clamping ladder. An effort the row does not take is not moved to another level.
- Other vendors (OpenAI, Google), a provider registry, or any change to the Anthropic rows.
- New module dependencies.

## Background

**What the catalog is today.**
- `catalog/catalog.json` has `schema_version` 1 and `catalog_version` `2026-09-30`. It has one vendor, `anthropic`, with 13 Claude rows.
- `catalog.Parse` (`catalog/catalog.go`) validates the file and builds a `byCanonical` index keyed `"<vendor>/<id>"`. Bad input fails with `*ParseError{Path, Reason}`, which matches `ErrCorruptCatalog`. A vendor id may not contain a slash, but a model id may: the test catalog in `catalog/catalog_test.go` already carries an `openrouter` vendor with ids such as `deepseek-ai/DeepSeek-V3`.
- `toModel` turns each row's `thinking_level_map` into `core.Model.Efforts`. A misspelled level is rejected at load. A listed row always gets a non-nil `Efforts` map.
- `catalog.Lookup(id)` trims the id, strips a leading `anthropic/` and looks it up among the `anthropic` rows only. An unlisted id gets a fixed default and `false`: `DefaultContextWindow` (1,000,000), `DefaultMaxTokens` (128,000), `ThinkingKindAdaptive`, no price and a nil `Efforts`.
- The vendor field `default_model` is still required by `Parse`, but `Lookup` no longer clones it.

**How the effort is read.** `provider/anthropic` `applyThinking`:
- With a nil `Efforts`, the effort is sent by its own name.
- With a map, an effort that is absent or null is not sent, and nothing is clamped.

**No tiers, and no "unpriced" flag.** PRD 10 removed the tier aliases, and the tier table lived on in agent-fox-legacy. No Go code in this repository knows a tier name. The input says an unlisted id leaves a run "flagged as unpriced, as on Anthropic today", but no such flag exists: `agentkit.New` throws away `Lookup`'s `bool` (`agent.go`: `m, _ := catalog.Lookup(cfg.Model)`). Reporting this belongs to `provider_selection`. This spec only reports whether a row is listed.

**Why now.** PRD 14 (`docs/prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md`) adds OpenRouter and Ollama, each on its own vendor SDK, and brings the tiers back. It reverses PRD 10's multi-vendor non-goal for these two providers only. Every later scope of that PRD needs the same facts:
- which providers exist;
- what a tier means for each provider;
- what window, output cap and price a model id has.

The input says those facts live in the catalog, so a model change is a catalog change. They can be built and tested before any new wire exists, with no new dependency.

**What else exists.** `core.APIOllamaChat` (`"ollama-chat"`) already exists in `core/stopreason.go`. `catalog` is not in `internal/policy`'s `apiPackages`, so new exported identifiers in it do not change `internal/policy/testdata/exported-api.golden`.

## Requirements

### 1. Provider names

- The catalog knows three providers. They are exported as constants: `catalog.ProviderAnthropic = "anthropic"`, `catalog.ProviderOpenRouter = "openrouter"` and `catalog.ProviderOllama = "ollama"`. `catalog.Providers()` returns them in that order, as a new slice on every call.
- A provider name given to `Select` is trimmed of surrounding whitespace and matched without regard to case. An empty or whitespace-only name means `anthropic`.
- Any other name fails with an error that wraps `catalog.ErrUnknownProvider`. The message names the value as given and the three providers, for example: `agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama`.
- The set of providers is fixed in code, not taken from the catalog's vendors. A catalog vendor that is not one of the three cannot be selected.
- A known provider whose vendor section is missing still selects, with fallback descriptors only. This can only happen with a catalog given to `Parse`.

### 2. Rows for OpenRouter and Ollama

The embedded `catalog.json` gains two vendor sections, keyed by provider name. Every `anthropic` row stays byte-for-byte the same.

- **`openrouter`**
  - Vendor: `name` "OpenRouter", `api` `"openrouter-chat"`, `base_url` `https://openrouter.ai/api/v1`, `default_model` `qwen/qwen3.8-flash`.
  - One row, `qwen/qwen3.8-flash`:
    - `context_window` 1000000 and `max_tokens` 131072;
    - `cost` input 0.15, output 0.47, cache_read 0.016, cache_write 0;
    - `input` `["text"]` and `reasoning` true;
    - `thinking_level_map`: `low`, `medium`, `high` and `max`, each mapped to the same name; `xhigh` absent.
  - The row's `note` gives the source of the price (OpenRouter's list, 2026-10-21) and the efforts probed.
- **`ollama`**
  - Vendor: `name` "Ollama", `api` `"ollama-chat"`, `base_url` `http://127.0.0.1:11434`, `default_model` `qwen3.8:27b`.
  - One row, `qwen3.8:27b`:
    - `context_window` 32768 and `max_tokens` 32768;
    - all four prices zero;
    - `input` `["text"]` and `reasoning` true;
    - the same `thinking_level_map` as the OpenRouter row.
  - The row's `note` says three things:
    - 32,768 is the length the model ran at on the reference machine (32 GB Apple M4), not the 262,144 that `/api/show` reports;
    - the Ollama provider sends this window as `num_ctx`;
    - the price is zero because the model runs on the embedder's own hardware.
- `catalog_version` becomes `2026-10-21`. The top-level `note` still contains the REQ-CAT-06 regeneration ritual and now mentions the tier table.
- The file still has no OpenAI or Google section. It contains none of the strings `gpt-`, `gemini`, `"openai"` or `"google"`.

### 3. Model ids and fallback descriptors

- A model id is trimmed of surrounding whitespace and otherwise used as written.
- On `anthropic`, `Select` resolves an id through the existing `Lookup`, so a leading `anthropic/` is accepted and removed, as today.
- On `openrouter` and `ollama`, the id is looked up among that provider's rows only, and `Model.ID` is the id as written. For example, `anthropic/claude-sonnet-5-5` under `openrouter` is an OpenRouter id: it keeps its prefix and never matches an `anthropic` row. A row of one provider is never returned for another provider.
- An unlisted id is served, not refused. It is reported with `Listed` false and `Model.ID` set to the id, and gets its provider's fallback:

  | Provider | Window | Output cap | Constants |
  |---|---|---|---|
  | `anthropic` | 1,000,000 | 128,000 | `DefaultContextWindow`, `DefaultMaxTokens` (unchanged) |
  | `openrouter` | 131,072 | 32,768 | `OpenRouterDefaultContextWindow`, `OpenRouterDefaultMaxTokens` |
  | `ollama` | 32,768 | 32,768 | `OllamaDefaultContextWindow`, `OllamaDefaultMaxTokens` |

- Every fallback has all four prices zero, `ThinkingKindAdaptive` and a nil `Efforts` map, so an effort is sent by its own name. As today on Anthropic, a fallback is a documented guess, not a lookup at the vendor.

### 4. The tier table

- `catalog.Tier` is a string type with the constants `TierSimple = "SIMPLE"`, `TierStandard = "STANDARD"` and `TierAdvanced = "ADVANCED"`.
- A model string is a tier when the whole string, trimmed, matches one of the three names without regard to case. A model string that matches a tier name is always the tier, never a model id.
- In `catalog.json`, each vendor may carry an optional `tiers` object. Its keys are the three tier names in upper case, and each value is `{"model": "<id of a row of this vendor>", "effort": "<low|medium|high|xhigh|max>"}`.
- The embedded table:

  | Provider | SIMPLE | STANDARD | ADVANCED |
  |---|---|---|---|
  | `anthropic` | `claude-sonnet-5-5`, medium | `claude-sonnet-5-5`, high | `claude-opus-5-5`, xhigh |
  | `openrouter` | `qwen/qwen3.8-flash`, low | `qwen/qwen3.8-flash`, medium | `qwen/qwen3.8-flash`, high |
  | `ollama` | `qwen3.8:27b`, low | `qwen3.8:27b`, medium | `qwen3.8:27b`, high |

- `Parse` validates a `tiers` block. Each failure is a `*ParseError` matching `ErrCorruptCatalog`, with a path such as `vendors.ollama.tiers.STANDARD.model`. `Parse` rejects:
  - a key that is not exactly one of the three names, including a lower-case spelling;
  - a block that is missing one of the three tiers;
  - an empty `model`, or a `model` that is not a row of the same vendor;
  - an `effort` that is not one of `low`, `medium`, `high`, `xhigh` or `max`;
  - an `effort` the named row does not take, meaning it is absent or null in the row's `thinking_level_map`, including a row with no level map at all.
- A vendor without a `tiers` block loads and has no tiers.
- `schema_version` stays 1.

### 5. `Select`: resolving provider, model and effort

`catalog.Select(provider, model string, effort core.Effort) (Selection, error)` resolves against `Default()`. `(*Catalog).Select` does the same against a catalog returned by `Parse`. The inputs are checked in this order: provider, then model, then effort.

- **Effort.** The effort is trimmed and matched without regard to case against the five `core.Effort` constants, then normalized to the lower-case constant. Empty is valid and means "none asked". Any other value fails with an error that wraps `catalog.ErrUnknownEffort` and names the value and the five efforts.
- **Model.** A model that is empty after trimming fails with an error that wraps `catalog.ErrNoModel`. The message names the three tiers and says a model id is also accepted.
- **Tier.** When the model is a tier:
  - `Selection.Tier` is the canonical tier and `Selection.Model` is the row of the tier's model id. `Listed` is true, because `Parse` guarantees the row exists.
  - `Selection.Effort` is the explicit effort when one is given, and otherwise the tier's effort.
  - Naming a tier on a provider whose catalog section has no tiers fails with an error that names the provider and the tier. This only happens with a catalog given to `Parse`.
- **Model id.** When the model is not a tier, `Selection.Tier` is `""`, the row or fallback comes from §3, and `Selection.Effort` is the explicit effort or `""`.
- **`Selection`.** The result has these fields:
  - `Provider`: the canonical provider name.
  - `Tier`.
  - `Model`: a `core.Model`, whose `ID` is the id the vendor will receive.
  - `Effort`.
  - `Listed`.

  `Effort` is the effort to request. Whether it is sent, and in what form, is the provider's reading of `Model.Efforts`, as on Anthropic today. For example, `xhigh` on the `ollama` row is requested but not sent.
- **Guarantees.**
  - Every `Model` returned is a deep copy (`cloneModel`).
  - `Select` is safe for concurrent use.
  - It reads no environment variable and does no I/O or network request.
  - It never panics on any input string against a valid catalog.
  - Every successful result has a positive window and output cap.

### 6. What stays as it is

- `catalog.Lookup` and `(*Catalog).Lookup` keep their behaviour: anthropic rows only, the optional `anthropic/` prefix, and the 10-REQ-3.4 fallback.
- `agentkit.New` and every other caller of `Lookup` are not touched by this spec.
- The `catalog.Vendor` constant stays. Its doc comment changes from "the one vendor the catalog lists" to the vendor `Lookup` resolves against.
- The catalog tests that pinned a Claude-only file are restated:
  - `Vendors()` is `[anthropic ollama openrouter]`, sorted as today.
  - `KnownVendor` is true for those three and false for `openai`.
  - TS-10-11 asserts that the only vendors are the three providers and that the file has no OpenAI or Google content.
- `catalog` still imports only `core` and the standard library.
- No model id, limit or price for the new providers is written in Go outside the `catalog` package. The fallback constants live in `catalog`.

### 7. Documentation

- `docs/configuration.md`, "Model catalog" section:
  - the catalog version;
  - a row table per provider;
  - the per-provider fallbacks;
  - the tier table;
  - `catalog.Select`: the precedence of explicit effort, then tier effort, then none, how names are matched, and the three errors.
- `docs/api.md` and `docs/architecture.md`: the `catalog` package is described as the catalog of the three providers, with tiers and `Select`.
- The package doc comment of `catalog` is updated, and `make doc` regenerates `docs/generated/catalog.txt`.

## Design Decisions

1. **The PRD is split into five specs, and this one comes first.** The catalog is the vocabulary every later scope uses: provider names, rows, fallbacks and tiers. It needs no external SDK and changes no current behaviour. The two providers come next, then the `agentkit.Config` wiring that depends on both, then conformance, examples and the README.
2. **Provider names are fixed in code, and each matches a catalog vendor id of the same spelling.** A provider is something this build can run. Taking the set from catalog data would let a vendor section name a provider that has no client.
3. **The tier table is an optional per-vendor `tiers` block in `catalog.json`, and `schema_version` stays 1.** The input puts the table with the catalog so that a model change is a catalog change. The field is optional and added alongside the existing ones, and `encoding/json` ignores it in an older reader.
4. **`Parse` checks every tier against its row: the model must be a row of the same vendor, and the effort must be one the row takes.** A tier that silently sends no effort, or points at an unlisted model, is the kind of hand-editing mistake the catalog already rejects at load (for example a misspelled level).
5. **Effort precedence is explicit, then tier, then none, and `Selection.Effort` is the requested effort.** Whether it reaches the wire stays the provider's reading of `Model.Efforts`, exactly as Anthropic's `applyThinking` works today. No clamping ladder is added.
6. **`Select` rejects an effort outside the five levels and normalizes case.** Today an unknown effort is either dropped silently or sent and rejected by the vendor. The new entry point can fail early at no cost to existing callers, because `Lookup` and `agentkit.New` are unchanged.
7. **Names are matched without regard to case and trimmed. Model ids are only trimmed.** The input asks for case-insensitive provider and tier names. A vendor model id is case- and prefix-sensitive, so it is kept as written. Trimming is harmless: no vendor id has surrounding whitespace, and `Lookup` already trims.
8. **Only `anthropic` strips an `anthropic/` prefix, through the existing `Lookup`.** On OpenRouter `anthropic/...` is a real model id, and rows are never shared across providers.
9. **The fallback limits are exported Go constants in `catalog`, next to the existing `DefaultContextWindow` and `DefaultMaxTokens`.** They are fixed fallbacks, not model rows, and keeping them in code matches the 10-REQ-3.4 precedent. Fallbacks have a nil `Efforts`, so an effort is sent as asked.
10. **`catalog.Lookup` is not changed. `Select` is the new per-provider entry point.** This keeps 10-REQ-3.3 to 3.5 and every current caller intact. `provider_selection` moves `agentkit.New` onto `Select`.
11. **The new rows list `low`, `medium`, `high` and `max`, and leave out `xhigh`.** Those four are the levels the 2026-10-21 probe ran successfully on both models, and `xhigh` was not probed. Leaving it out means it is not sent, and adding it later is a catalog edit.
12. **The vendor `api` strings are `openrouter-chat` and `ollama-chat`.** The field is required metadata that nothing reads at run time. `ollama-chat` matches the existing `core.APIOllamaChat`. `openrouter-chat` avoids calling OpenRouter's native API "openai-completions", given the non-goal of compatibility endpoints.
13. **Run-level "unpriced" reporting is not part of this spec. The catalog reports `Listed` only.** No flag exists in the code today, and for OpenRouter whether a run is priced depends on the cost each response reports, which only the provider and the agent see.
14. **Errors are sentinels wrapped with `fmt.Errorf("%w: …")` and prefixed `agentkit:`: `ErrUnknownProvider`, `ErrUnknownEffort` and `ErrNoModel`.** This matches the package's existing style (`ErrCorruptCatalog`), and `agentkit.New` can later return them unchanged.
15. **The new rows accept text input only.** Images, audio and files on the new providers are a non-goal of PRD 14.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `10_anthropic_wire` | modifies | 10-REQ-3.1 required a Claude-only `catalog.json`. This spec adds `openrouter` and `ollama` sections (still no OpenAI or Google), so TS-10-11 and `TestCatalogAccessors` are restated. 10-REQ-3.3 to 3.6 (`Lookup`, the Anthropic fallback, no model metadata in `provider/anthropic`) stay as they are. |
