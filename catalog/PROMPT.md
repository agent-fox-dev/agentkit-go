# Prompt: refresh `catalog/catalog.json`

You are a coding agent. Your job is to bring `catalog/catalog.json` up to date
with the models each supported vendor currently offers, without breaking the
SDK. Read this whole file before touching anything. Follow the workflow in
order; do not skip the verification steps.

You are working in `agentkit-go`. Obey the repository `AGENTS.md` (feature
branch, conventional commits, no `Co-Authored-By`, `make check` before
committing, do not push feature branches).

## 0. What the catalog is, and is not

`catalog/catalog.json` is an embedded **snapshot** of metadata that no vendor
API returns in one place and the SDK needs *before* it sends a request: wire
API, base URL, context window, output cap, prices, modalities, reasoning
support, per-model compatibility flags, and the thinking-level ladder.

- It is **not an allowlist.** An unknown id under a known vendor clones that
  vendor's `default_model` row. So a missing model still works, but it is
  priced and clamped as its sibling. Your job is to make that guess rarely
  needed and never silently wrong.
- It is parsed strictly at first use (`catalog.Parse`). A corrupt file panics
  the first caller. A wrong *value* does not fail: it becomes a 400 deep in a
  session, or a mis-billed run. **Accuracy matters more than completeness.**
  Never invent a number. If you cannot find a value from an authoritative
  source, say so in the row's `note` and in your final report, and choose the
  conservative option described in §6.
- The vendors in scope are exactly those in the file today: `anthropic`,
  `openai`, `google`. Do not add a vendor unless the task explicitly says so
  (see §9).

## 1. Orient

Do these first:

1. `git status --short --branch`, `git log --oneline -10 -- catalog`. Start from
   a clean `main`, then `git checkout -b feature/catalog-refresh-YYYY-MM-DD`.
2. Read `catalog/catalog.go`, `catalog/clamp.go`, `catalog/resolve.go`. The
   header `note` in `catalog.json` is a long maintenance note; read it fully.
3. Read `docs/PROVIDERS.md` (pinned wire versions and rulings, especially
   L-4, L-6, L-7, L-9..L-12) and, for each vendor, the adapter's `Compat`
   struct (listed in §4). The adapters define what a row may contain.
4. Dump the current state so you can diff later:
   ```
   cp catalog/catalog.json /tmp/catalog.before.json
   go test ./catalog/... ./provider/... 2>&1 | tail -20     # baseline must be green
   ```
   If the baseline is red, stop and report; do not refresh on top of a broken tree.

## 2. Gather facts from authoritative sources

Use only the vendor's own documentation and pricing pages (web access is
required; if you do not have it, stop and say so rather than working from
memory — your training data is older than the models you are adding).
Record the URL and the date you read each fact; you will cite them in the
commit body.

For every vendor collect, **per model**:

| Fact | Where it usually is |
|---|---|
| Exact API model id (the string sent on the wire, including any `-preview` or dated suffix) | models overview / models list endpoint |
| Display name | models overview |
| Context window (tokens) | models overview |
| Max output tokens | models overview |
| Price per **1M tokens**: input, output, cache read, cache write (5-minute TTL for Anthropic) | pricing page |
| Long-context price tiers (a prompt over N tokens reprices the **whole request**) | pricing page footnotes |
| Input modalities (`text`, `image`) | models overview |
| Whether it reasons, and how reasoning is controlled: effort tokens, integer token budgets, or neither; whether reasoning can be turned off | reasoning / extended-thinking / thinking docs |
| Whether it rejects `temperature`/`top_p` | model docs, migration guides |
| Whether `max_tokens` or `max_completion_tokens` is required (OpenAI wire) | API reference |
| Status: current, legacy, deprecated (with shutdown date), retired | deprecations page |

Where a vendor has a list-models endpoint and you have a key, use it to confirm
ids — but prices, caps and capability flags are almost never in it; take those
from the docs.

Vendor-specific notes:

- **Anthropic** — pricing page lists base input, 5m cache write, 1h cache write,
  cache read, output. The catalog's `cache_write` is the **5-minute** rate;
  1h is computed in `provider.ComputeCost`, not stored. Note whether a model
  uses effort tokens (`output_config.effort`, adaptive thinking) or
  `budget_tokens`, and whether it accepts `{"type":"disabled"}`.
- **OpenAI** — the catalog targets the `openai-completions` wire
  (`base_url` `https://api.openai.com/v1`). Capture the `reasoning_effort`
  values each model accepts (including whether `none` is accepted), and
  long-context tiers (`cost.tiers`).
- **Google** — rows are the **AI Studio** deployment (Vertex is selected by
  configuration, never by a row). Keep the `-preview` suffix exactly as the
  vendor ships it: dropping it resolves to a sibling clone and can mis-price by
  a large factor. `cache_write` is `0.0` because Google bills caching by
  storage-hour, a dimension the catalog does not model; keep that convention
  and say so in the note.

## 3. Decide what changes

Produce a change plan *before* editing, in a scratch file (`/tmp/catalog-plan.md`),
with four lists. Do not skip the "unchanged" list; it proves you checked.

1. **Add** — current models not in the catalog. Add every generally-available
   text/chat model a user would reasonably select. Skip: embeddings, image/audio
   generation, realtime/live models, fine-tune bases, deprecated aliases, and
   models that do not speak the vendor's configured `api` wire.
2. **Update** — existing rows whose price, window, cap, modalities, reasoning
   behaviour or compat flags changed. A price move (e.g. a tier cut) is the
   canonical case.
3. **Remove or keep** — models the vendor has *retired* (API returns
   not-found): remove them. Models merely *deprecated* but still served:
   **keep** them and put the shutdown date in the `note`. Old but live models
   stay; existing users pin them. Before removing, run
   `grep -rn "<model-id>" --include=*.go --include=*.md .` (see §7) — tests and
   examples may name it.
4. **Unchanged** — every remaining row, each marked "verified against <source>".

Also decide `default_model` per vendor (see §5).

## 4. Row format

```jsonc
"claude-sonnet-5": {                    // key == wire model id; "id" field optional, must equal the key
  "name": "Claude Sonnet 5",            // display name; defaults to the id
  "context_window": 1000000,            // tokens, >= 0
  "max_tokens": 128000,                 // output cap, >= 0
  "cost": { "input": 2.0, "output": 10.0, "cache_read": 0.2, "cache_write": 2.5,
            "tiers": [ { "threshold": 272000, "input": 4.0, "output": 18.0,
                         "cache_read": 0.4, "cache_write": 5.0 } ] },   // tiers optional
  "input": ["text", "image"],
  "reasoning": true,
  "compat": { ... },                    // per-API profile, see below; optional
  "thinking_level_map": { ... },        // see §4.2; optional when reasoning is false
  "note": "why any non-obvious value is what it is"
}
```

A row may override the vendor's `api`, `base_url`, `headers`; leave them out
unless the model genuinely differs from its vendor.

### 4.1 Validation rules the loader enforces

(`catalog.Parse`; a violation panics at first use and is named by JSON path.)

- `schema_version` must be `1`. **Do not change the schema** in a refresh; if a
  new vendor feature needs a new field, that is an SDK change, out of scope.
- Every vendor needs ≥ 1 model and a `default_model` that is one of its keys.
- Costs are USD **per million tokens**, non-negative.
- `cost.tiers[].threshold` are positive and **strictly ascending**; the
  threshold is *strictly exceeded* (a prompt of exactly 272000 tokens is
  not in the tier). Each tier restates all four rates and replaces the base
  rates for the whole request.
- `thinking_level_map` keys must be among `off, minimal, low, medium, high,
  xhigh, max` — a typo is rejected. If any level other than `off` has a non-null
  value, `reasoning` must be `true`.
- Model keys and vendor ids may not contain `/` or whitespace (vendor).
- JSON must be valid; `compat` must be valid JSON.

### 4.2 `thinking_level_map`

Maps the SDK's abstract level to the **wire value the adapter sends**.

- A **string** value = supported; that string goes on the wire. What the string
  means depends on the wire:
  - `anthropic-messages`, effort generation: effort tokens (`low`, `medium`,
    `high`, `xhigh`, `max`), sent as `output_config.effort` with adaptive
    thinking. `off` is the thinking *type* sent for a request of off:
    `"disabled"` where the model accepts it, `"between_tools"` where it
    rejects `disabled` and offers that instead (Claude Sonnet 5.5), or
    `null` where thinking cannot be turned off. Probe the API to find out
    which; the docs and the error messages disagree often enough.
  - `anthropic-messages`, budget generation: integer strings (`"1024"`,
    `"4096"`, …), sent as `budget_tokens`. The minimum is 1024. `off` →
    `"disabled"`.
  - `openai-completions`: `reasoning_effort` tokens; `off` → `"none"` where
    accepted.
  - `google-generative-ai`: the adapter does not yet speak `thinkingLevel`;
    every level is recorded **present-and-null** (see below).
  - **Never translate between effort tokens and budgets.** `budget_tokens` is a
    400 on the newest Claude models and an effort token is an error on the older
    ones. Use whichever generation the model's docs specify.
- **Present and `null`** = "checked; the model cannot do this / the wire we send
  cannot express it". **Absent** = "not checked". They behave identically at
  runtime (ruling P-28) but the difference is how a future reviewer tells a
  checked fact from an unfilled row. Prefer recording `null` when you
  verified.
- `off: null` means the model cannot stop thinking; the adapter then omits the
  thinking key for a request of `off`.
- The clamp goes *upward first* (a request for `minimal` on a ladder that starts
  at `low` becomes `low`), then downward, and never down to `off`. So do not
  fill a level the model lacks just to "complete" the ladder.
- **Diff this field per model on every refresh.** An entry that silently
  disappears turns a clamped request into a 400; one that silently appears sends
  a level the model does not know.

### 4.3 `compat` keys (per wire API; unknown keys are errors or ignored)

Each wire has its own disjoint vocabulary. Only write a key when the model
deviates from the adapter's default; restating a default is noise (a test
checks Astra does not restate `use_max_tokens`).

| Wire | Adapter struct | Keys |
|---|---|---|
| `anthropic-messages` | `provider/anthropic/anthropic.go` `compat` | `supports_sampling` (false → drop `temperature`/`top_p`; set on the Claude 4.7+ generation), `supports_long_cache_retention` |
| `openai-completions` | `provider/openai/openai.go` `Compat` | `use_max_tokens`, `supports_store`, `supports_developer_role`, `supports_reasoning_effort`, `supports_strict_tools`, `supports_temperature`, `supports_long_cache_retention`, `supports_finish_reason`, `allows_null_assistant_content`, `allows_user_after_tool_result`, `requires_tool_result_name`, `thinking_format`, `thinking_token_budget_field`, `cache_control_format` |
| `openai-responses` | `provider/openairesponses/responses.go` `Compat` | `use_instructions_field`, `supports_reasoning_items`, `supports_encrypted_reasoning`, `supports_service_tier`, `supports_prompt_cache_key`, `supports_function_strict`, `supports_store_flag`, `supports_reasoning_summary`, `supports_sampling_params` |
| `google-generative-ai` | `provider/google/google.go` `Compat` | `can_disable_thinking`, `min_thinking_budget`, `max_thinking_budget`, `include_thoughts` |
| `ollama-chat` | `provider/ollama/ollama.go` `Compat` | `supports_think`, `supports_tool_name`, `keep_alive` |

**Re-read those structs at refresh time** — they are the source of truth, this
table can lag. The OpenAI completions adapter **rejects any compat key it does
not declare**, failing the request naming the key; a misspelt key there breaks
every call to that model. Do not use a key from another wire's vocabulary.

## 5. `default_model`

It is the row cloned for any unknown id under the vendor, so it should be "the
most representative current row … the middle of the vendor's range", not the
cheapest or the largest. If the current default is still current and mid-range,
keep it. Change it only when the vendor's line-up has shifted (for example the
default has been retired, or a new mid-tier model has replaced it). Changing it
changes how every unknown id is priced, so explain the reason in the commit body.
Other code names the defaults (`examples/*`, README) — see §7.

## 6. Conventions and judgement calls

- **Order** the models newest/most capable first within a vendor, matching the
  existing file (flagship, then mid-tier, then small, then legacy).
- **Keep `note` fields meaningful.** Each note explains any value a reader
  might "fix" wrongly: why a level is null, why a cache price differs from the
  usual multiple, why an id carries a suffix. New rows with unusual behaviour
  need notes; do not write filler. Update a row's note when you change the
  fact it describes (for example a price cut date and old price).
- **Uncertain value?** Prefer the conservative choice: a *lower* `max_tokens`
  (the clamp just limits output), the *higher* known price (a budget gate then
  errs toward stopping), `supports_sampling: false` only if the vendor says so.
  Say what was unverified in the note.
- Number formatting: plain JSON numbers, `2.0` not `2`. Keep 2-space indentation
  and the existing inline-object style for `cost` and `headers` so the diff stays
  readable. Do not reorder keys in rows you did not change.
- **Bump `catalog_version`** to today's date (`YYYY-MM-DD`). Update the header
  `note` only if a vendor-wide fact in it changed (for example Google's adapter
  now speaks `thinkingLevel`, or a vendor moved reasoning control again);
  otherwise leave it alone.
- Do not edit `schema_version`, the vendor `api` / `base_url` / `headers`
  (including `anthropic-version`) in this task. A wire version bump is a
  provider change governed by the regeneration checklist in
  `docs/PROVIDERS.md`; if you find the vendor has moved it, report that instead.

## 7. Keep the rest of the repository consistent

The catalog is referenced elsewhere. After editing, find every reference to
any id you **removed or renamed**, and to the `default_model` values:

```
grep -rn "<old-id>" --include=*.go --include=*.md --include=*.json .
```

Known consumers as of writing (re-check; this list can lag):

- `catalog/*_test.go` — tests pin specific rows: `claude-opus-4-5`,
  `claude-sonnet-4-5`, `claude-haiku-4-5`, `claude-sonnet-4-6`,
  `claude-opus-5`, `claude-fable-5-1`, `gpt-6-astra`, and assert exactly three
  vendors. Tests that encode a *fact about a row* (for example "Astra's `off` is
  present-null", "Fable's `off` is present-null") are deliberate pins of vendor
  behaviour. If the vendor's behaviour changed, update the row **and** the test
  with the reason; if you only removed a retired model, repoint the test at a
  model with the same property. Never weaken a test to make it pass without
  stating why the pinned fact is no longer true.
- `provider/anthropic/stream_test.go`, `provider/openai/openai_test.go` — resolve
  real rows through the catalog and iterate every row; they will catch an
  unreadable compat key or bad thinking map.
- `examples/*/main.go` and the README/`examples/README.md` — default
  `anthropic/claude-sonnet-5` and sample `openai/gpt-5.6-terra`,
  `google/gemini-3.8-flash` specs. If a default model is retired or renamed,
  update these and the docs in the same change.
- `docs/PROVIDERS.md` — only if a pinned fact changed (see §6, last bullet).
- `docs/configuration.md` / `examples/README.md` describe the catalog as
  "anthropic, google, openai" — update if the vendor set changed.

## 8. Verify (all must pass)

1. Valid JSON and strict load:
   ```
   python3 -m json.tool catalog/catalog.json > /dev/null
   go test ./catalog/...
   ```
2. Every row resolves and every adapter accepts its compat/thinking map:
   ```
   go test ./provider/... ./...
   ```
   (`provider/openai` iterates every catalog row; it fails on an unknown compat key.)
3. Semantic diff. Write a short script (Python is fine) that loads
   `/tmp/catalog.before.json` and the new file and prints, per model:
   added / removed / changed fields. **Review the output line by line** and check
   each change has a cited source. Pay special attention to the two fields the
   header `note` marks as mandatory diffs:
   - `thinking_level_map` — any level appearing, disappearing, or switching
     between `null`, effort token and integer;
   - `cost` — including `tiers` and any price that moved by more than ~2×
     (re-read the source; a 10× change is usually a units mistake — the unit
     is USD per **million** tokens).
4. Sanity checks you can script:
   - every `default_model` is a key of its vendor;
   - for each model, `max_tokens <= context_window`;
   - `cache_read <= input` and `cache_write >= 0`;
   - each tier's rates are ≥ the base rates it replaces;
   - no model with `reasoning: false` has a non-null level above `off`;
   - Anthropic rows use either all-effort-token or all-integer maps, never a mix.
5. Resolution smoke test:
   ```
   go run - <<'GO'
   package main
   import ("fmt"; "github.com/agentfox/agentkit-go/catalog")
   func main(){ c:=catalog.Default(); for _,v:=range c.Vendors(){ fmt.Println(v, c.DefaultModelID(v), c.Models(v)) } }
   GO
   ```
   (Save as a temporary file under `/tmp` inside the module if `go run -` is
   unavailable; delete it afterwards.)
6. `make check`.

If a test fails, decide whether the **row** or the **test** is wrong by
reading the test's comment and the vendor source — do not just edit the
assertion.

## 9. Out of scope — stop and report instead

- Adding a new vendor (needs a provider package, a credential table and
  docs; see `docs/PROVIDERS.md`). Note that Ollama, OpenRouter, Groq, DeepSeek
  and others have wire implementations but deliberately no catalog rows.
- Changing `schema_version` or the file format or any Go code.
- Changing wire versions, endpoints, beta headers or attribution.
- Models the vendor has announced but not made generally available.
- Regenerating goldens (`testdata/golden/*`); if a change makes a golden
  differ, stop and report — it means a row influenced a request body and a
  human must read that diff (see the checklist in `docs/PROVIDERS.md`).

## 10. Commit and report

Commit only the files you changed (`catalog/catalog.json`, plus tests, examples
and docs changed under §7), with a conventional message:

```
chore(catalog): refresh models to YYYY-MM-DD

Added:    <ids>
Updated:  <id>: <what> (was X, now Y) [source]
Removed:  <ids> (retired <date>) [source]
Default:  <vendor> default_model unchanged | changed from A to B because …
Sources:  <url> (read YYYY-MM-DD), …
```

Follow the session-completion rules in `AGENTS.md` (merge to `main` locally,
clean working tree, no push of the feature branch).

Finish with a handoff note that lists: what you added/changed/removed, anything
you could **not** verify and how you handled it (§6), any vendor-side change
that is out of scope (§9), and the output of the semantic diff from §8.3.
