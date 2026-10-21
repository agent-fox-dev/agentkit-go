---
spec_id: "30"
spec_name: "provider_conformance"
title: "Cross-provider conformance suite, live smoke tests, and provider selection in the examples and README"
status: "active"
created_at: "2026-10-21T11:17:18.520866Z"
updated_at: "2026-10-21T11:17:18.520866Z"
intent_hash: "f63b83918d0051292954872bea33e382d48e5f2eca97684199c6f67d417b73c4"
schema_version: 2
source: "docs/prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md"
---
## Intent

The three providers AgentKit ships (`anthropic`, `openrouter` and `ollama`) should look the same to the agent loop. This spec proves that offline on every change: the same logical turns run through every provider and must produce the same canonical outcome. It also lets an operator check each provider against its live service when asked. The examples and the README quick start choose what runs a model the same way an embedder does, with three settings: provider, model and effort.

## Goals

1. `go test ./internal/providerconformance` runs seven named cases against each of the three providers: `text`, `tool_call`, `tool_call_stop`, `thinking`, `cut_off`, `error_status` and `error_in_stream`. That makes 21 subtests.
   - Every agent is built with `agentkit.New` from a provider name.
   - Every request goes to a local fake of that vendor's native API.
   - No network is used beyond loopback, and no credential comes from the process environment.
   - The suite runs under `-short`, so it is part of `make check`.
2. Any single case can be selected by name: the subtest path ends in `/<provider>/<case>`, for example `-run 'TestProviderConformance.*/ollama/thinking'`.
3. `make provider-live-test` sets `AGENTKIT_LIVE=1` and, for each provider whose prerequisites are present, runs one real tool loop on the `STANDARD` tier.
   - The loop reads a file and answers with the random word in it.
   - A provider without its prerequisites skips, and the skip message names what is missing.
   - `make check` and plain `go test` never run these tests.
4. `examples/codingagent`, `examples/customtools` and `examples/mcp` read `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and `AGENTKIT_EFFORT`. An offline test of each example runs it with `AGENTKIT_PROVIDER=ollama AGENTKIT_MODEL=SIMPLE AGENTKIT_EFFORT=high` against a local Ollama-shaped server. Each test asserts that:
   - the server records model `qwen3.8:27b` and `think` `"high"`;
   - the example exits 0;
   - its summary line names `ollama`, `qwen3.8:27b` and `high`.
5. The README quick start picks a provider by name. It has no `anthropic.Resolve` and no SDK import. The READMEs of the three examples above show one run on each provider.
6. Every existing test passes unchanged, and `make check` passes.

## Non-goals

- **Anything earlier specs of this split own.** That covers:
  - the catalog rows, the tiers and `catalog.Select` (`26_provider_catalog_tiers`);
  - each provider's own wire tests, retries, credentials and errors (`27_openrouter_provider`, `28_ollama_provider`);
  - the `agentkit.Config` change, credential isolation between providers, `Agent.Resolution()` and the run-level unpriced flag (`29_provider_selection`).

  This suite uses those features. It does not test them again in depth, and it adds no provider behaviour.
- **Retries and cancellation as conformance cases.** Only OpenRouter retries, so there is no shared expectation to assert. Cancellation is already tested per provider in specs 10, 27 and 28.
- **A conformance kit for embedders' own `core.ProviderClient` implementations.** The suite is internal and has no exported API.
- **The offline examples.** `agentdemo`, `codemode`, `sandbox`, `gateway` and `harness` stay on `provider/faux` and read none of the `AGENTKIT_*` selection variables.
- **Live tests in CI, `make check` or `make check-release`.** No CI workflow changes.
- **Live tests per Anthropic deployment.** The `anthropic` live test runs on whatever deployment `anthropic.Resolve` picks from the environment (direct, Vertex AI or Bedrock).
- **Changing the examples' tools, prompts or bounds**, beyond how they choose the model.
- **Changes to `catalog`, `provider/*`, the loop or `agentkit.Config`.**

## Background

**The providers.** After specs 26 to 29, `agentkit.New(agentkit.Config{Provider: "openrouter", Model: "STANDARD", Getenv: …})` builds the matching provider client itself:
- `anthropic` uses `provider/anthropic` on `anthropic-sdk-go`, reading `ANTHROPIC_*` and the Vertex and Bedrock variables;
- `openrouter` uses `provider/openrouter` on OpenRouter's Go SDK and Chat Completions, reading `OPENROUTER_API_KEY` and `OPENROUTER_BASE_URL`;
- `ollama` uses `provider/ollama` on `github.com/ollama/ollama/api` and native `/api/chat`, reading `OLLAMA_HOST`.

Each provider spec tests its own wire against its own recording server. Nothing yet runs the same logical turn through all three, so nothing shows that they agree with each other.

**What the loop relies on** (`loop.go`):
- It continues on the presence of tool-use blocks, never on the stop reason.
- It short-circuits before executing tools on `StopReasonError` and `StopReasonAborted`. The run error is then `errors.New(assistant.ErrorMessage)`, so sentinel identity survives only on the provider's forwarded `ErrorEvent.Err`.
- It forwards every provider event onto the run's stream between `TurnStartEvent` and `TurnEndEvent`.
- It prices usage at the catalog row only when the provider left the cost bit clear.

**The normative event order.** `provider/faux` documents itself as "the executable specification of the streaming protocol", with this order:
1. `MessageStart`;
2. for each block, `<Kind>Start` and its deltas, then a `MessageUpdate` snapshot;
3. the block-end events, in block order, after the stream has ended;
4. `MessageEnd`.

`provider/anthropic` follows it (`stream.go` `finish`). The specs for `openrouter` and `ollama` require the same order.

There is one difference on failure:
- faux, when scripted with `Err`, still emits its block-end events and then the terminal `ErrorEvent`;
- the Anthropic provider's `fail` emits no block-end events.

So only turns that end normally can be compared with faux.

**Known differences between the providers.** The specs allow these, and the suite must tolerate them:
- **Cut-off stream.** Anthropic's failed message keeps the tool-use blocks it completed or salvaged (`partialContent`). OpenRouter and Ollama drop every tool call from a failed turn. In both cases the loop never executes them.
- **In-stream errors.** Anthropic reports an `event: error` as `<type>: <message>`, without the `anthropic:` prefix (`decode.go` `wireError.String`).
- **Usage.** Ollama reports no reasoning-token count.

**Precedents in the tree:**
- `internal/toolconformance` is a test-only case table, selected by name and run against two implementations.
- `internal/testkit.EchoTool` is a counting tool.
- `runner_metadata_smoke_test.go` and `provider/anthropic/stream_test.go` (`sseBody`) render Anthropic SSE for an `httptest` server.
- `examples/codemode/main_test.go` runs an example with `go run .`.
- `sandbox/gatewayd/podman_optin_test.go` shows the opt-in skip pattern (skip under `-short`, skip without the prerequisite, with a hint naming the make target).

**Policy that shapes where the code goes:**
- `internal/policy/nolisten_test.go` (TS-19-63) forbids listening APIs in the root module's non-test files.
- `core/size_test.go` (TS-12-14) holds the root module to 20,000 lines of library code. It excludes internal packages that no non-test file imports.
- Spec 27's TS-27-50 allows only `provider/openrouter` to import the OpenRouter SDK.

**The examples today.** `codingagent`, `customtools` and `mcp`:
- read only `AGENTKIT_MODEL`, with default `anthropic/claude-sonnet-5`;
- call `catalog.Lookup` for their summary line;
- resolve an Anthropic SDK client, which `29_provider_selection` removes.

`examples/README.md` and `docs/configuration.md` document `AGENTKIT_MODEL` as "examples only". The README quick start still builds a client with `anthropic.Resolve`.

## Requirements

### 1. The suite and where it runs

- The suite lives in a new tests-only package, `internal/providerconformance`:
  - `doc.go` holds only the package doc comment;
  - all code, the `httptest` servers included, is in `_test.go` files.

  No non-test file of the root module therefore gains a listener. No package imports this one, so it stays outside the line budget.
- `TestProviderConformance` runs every case against every provider as subtests named `<provider>/<case>`. Each subtest is independent and may run in parallel. Each finishes within a 30-second context or fails.
- **How each subtest builds its agent.** It calls `agentkit.New` with:
  - `Provider`: the provider name;
  - `Model`: `"STANDARD"`;
  - `Getenv`: a map-backed function holding only that provider's variables, pointed at that subtest's own fake: `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL`; `OPENROUTER_API_KEY` and `OPENROUTER_BASE_URL`; or `OLLAMA_HOST`;
  - a toolset of one counting tool, `echo` (`internal/testkit.EchoTool`);
  - `MaxTurns` 4.

  The process environment is never read.
- **Expected values.** The expected model id and effort come from `catalog.Select(provider, "STANDARD", "")`. No model id is written in the suite.

### 2. The vendor fakes

- Each provider has one fake: an `httptest` server that does two things:
  - It replays scripted logical turns, one per request, rendered in that vendor's native wire format:
    - Anthropic Messages SSE at `POST /v1/messages`;
    - OpenRouter Chat Completions SSE at `POST <base>/chat/completions`, where the base URL is `<server>/api/v1`;
    - Ollama NDJSON at `POST /api/chat`.
  - It records every request: method, path, headers and body.
- A request to any other path fails the subtest. So does a request beyond the script.
- **A logical turn** is built from these parts:
  - an ordered list of blocks: text, thinking or tool call (id, name, argument JSON);
  - how it ends: the vendor's normal stop, the vendor's tool-call stop, cut off with no final signal, an HTTP error status with a message, or an in-stream error with a message after the blocks;
  - optional usage: prompt tokens including cache reads, cache reads, output tokens, and a reported cost.
- **Rendering.** Each fake renders a logical turn the way its vendor sends it:
  - text and thinking arrive in two deltas;
  - tool arguments arrive in two fragments on Anthropic and OpenRouter, and whole on Ollama;
  - usage is in the vendor's own fields and netting: Anthropic reports input net of cache reads, while OpenRouter and Ollama report prompt totals that include them;
  - only OpenRouter reports a cost;
  - each response echoes the requested model id.
- **Decoding.** Each fake also decodes a recorded request body into a vendor-neutral summary:
  - the model id;
  - the effort in the vendor's form: Anthropic `output_config.effort`, OpenRouter `reasoning.effort`, Ollama `think`;
  - the declared tool names;
  - the user texts;
  - for each assistant message: its visible text, its tool calls (id, name, arguments), and its replayed thinking, both as text and in the raw vendor form (Anthropic thinking plus signature, OpenRouter `reasoning_details`, Ollama `thinking`);
  - the tool results, each with the call id it answers and its text, in order.
- **Independence from the code under test.** The fakes use the standard library alone, with `encoding/json` and the vendors' documented field names. They import no vendor SDK and none of the providers' own test helpers, so they check the providers against the wire format rather than against themselves.

### 3. Checks common to every case

- Every recorded request carries the selected model id and effort in the vendor's form.
- The provider events of a turn are the run's events after that turn's `TurnStartEvent`, up to and including its first `MessageEndEvent`.
- **Turns that end normally.** Take the turn's provider events, remove the `MessageUpdateEvent`s, and collapse each run of consecutive delta events of one block into one. The resulting event types must match, in order, what `provider/faux` emits for the same blocks and stop reason.

  The block-end events must also carry whole payloads:
  - `TextEndEvent.Text` and `ThinkingEndEvent.Thinking` hold the full scripted text;
  - `ToolCallEndEvent.Block` has the scripted id and name, and an `Input` that is JSON-equal to the scripted arguments.
- **Turns that fail.** The last two provider events are a terminal `ErrorEvent`, then a `MessageEndEvent` whose message has `StopReasonError`.
- **Request count.** The fake records exactly the number of requests the case scripts. An unexpected retry or an extra turn fails the case.

### 4. The cases

| Case | Scripted turns | Expectations beyond §3 |
|---|---|---|
| `text` | One text turn, "Hello from the conformance suite.", ending with the vendor's normal stop. Usage: 100 prompt tokens, of which 60 are cache reads, and 20 output tokens. OpenRouter also reports a cost of 0.00042. | `StopReasonStop`; `RunStopEndTurn` with no error; `FinalText` is the scripted text; one request, carrying the user prompt. The turn's usage is `InputTokens` 40, `CacheReadTokens` 60, `OutputTokens` 20, with `ReasoningTokens` ≤ `OutputTokens` and the cost bit set. The cost is the reported cost where the vendor reports one, and otherwise `provider.ComputeCost` at the selected row. The run's usage is not flagged unpriced. |
| `tool_call` | Turn 1: tool call `call_conformance_1`, `echo`, `{"v":"conformance"}`, ending with the vendor's tool-call stop (Anthropic `tool_use`, OpenRouter `tool_calls`, Ollama `stop`). Turn 2: text "All done." | `echo` ran once, with the scripted arguments. The first request declares `echo`. The second request carries that call (id, name, arguments) on the assistant message, followed by a tool result for `call_conformance_1` whose text contains `echoed`. `RunStopEndTurn` with no error, `TurnCount` 2, `FinalText` "All done." |
| `tool_call_stop` | As `tool_call`, but turn 1 ends with the vendor's plain stop (Anthropic `end_turn`, OpenRouter `stop`, Ollama `stop`). | Same as `tool_call`: the loop continues on the tool call whatever the stop reason. |
| `thinking` | Turn 1: thinking "Plan: call echo once.", then the `tool_call` call. On Anthropic the thinking block carries the signature `sig-conformance`. On OpenRouter it arrives as two `reasoning.text` fragments of `reasoning_details`. Turn 2: text. | Turn 1 emits thinking events. No text event carries any of the thinking. In the transcript, the first assistant message is a `ThinkingBlock` holding the full thinking, then the tool use, and has no text block containing the thinking. In the second request, the thinking is replayed in the raw vendor form exactly as received: Anthropic thinking and signature; OpenRouter `reasoning_details` JSON-equal to the sent fragments; Ollama `thinking` equal to the text. The thinking does not appear in that message's visible content. |
| `cut_off` | Text "Partial answer", then a tool call: arguments cut mid-way on Anthropic and OpenRouter, a whole tool-call chunk on Ollama. The body then ends without the final signal (Anthropic `message_stop`, OpenRouter `finish_reason`, Ollama `done: true`). | The terminal `ErrorEvent.Err` matches the provider's `ErrStreamTruncated` (`errors.Is`). `RunStopError` with a non-nil error. `echo` never ran, and no `ToolExecutionStartEvent` was emitted. The last assistant message has `StopReasonError`, and its text contains "Partial answer". One request. |
| `error_status` | HTTP 400 with the vendor's error body and message `conformance-bad-request`. | The terminal `ErrorEvent.Err` is the provider's `*StatusError` with `Code` 400 (`errors.As`). Its text starts `<provider>: HTTP 400` and contains the vendor message. `RunStopError`. One request: a 400 is retried by no provider. No tool ran. |
| `error_in_stream` | Text "Working on it", then the vendor's in-stream error with message `conformance-upstream-failure`: Anthropic `event: error`, an OpenRouter error chunk with code 502, an Ollama `{"error":…}` line. | The terminal `ErrorEvent` text contains the vendor message. `RunStopError`. The last assistant message keeps "Working on it". One request: an error after content is retried by no provider. No tool ran. |

- **Failures.** The suite asserts only behaviour that specs 10, 11, 27 and 28 already require. When a case fails, the provider is fixed, or an erratum is recorded for the provider's spec. The case is not weakened for one provider.
- **The only per-provider branches** are the vendor rendering and decoding in §2, and the source of the expected cost in `text`.

### 5. Live smoke tests

- **Location.** `TestLive` in `internal/providerconformance` (in a `_test.go` file) has one subtest per provider.
- **Gates.** The whole test skips:
  - under `-short`;
  - unless `AGENTKIT_LIVE` is `1`. The skip message names the variable and `make provider-live-test`.
- **Prerequisites.** Each subtest skips, naming what is missing:
  - `anthropic` skips when `anthropic.Resolve(anthropic.OSEnv{})` fails, and shows that error.
  - `openrouter` skips when `OPENROUTER_API_KEY` is empty.
  - `ollama` resolves its host with `ollama.New(row, ollama.Options{})` and `Host()`. It skips if `New` fails. It then sends `GET <host>/api/tags` with a 2-second timeout, and skips:
    - when the host is unreachable or answers with a non-200 status, naming the host and `ollama serve`;
    - when the tier's model is not listed, naming `ollama pull <model>`.
- **The run.** Each subtest:
  1. writes `secret.txt`, holding a fresh random word, into a temporary workspace;
  2. builds the agent with `agentkit.New`: `Provider` set to the name, `Model` `"STANDARD"`, the process environment (no `Getenv`), only the built-in `read_file` tool (`tools.For` then `core.Named`), `MaxTurns` 6, `MaxCostUSD` 0.50 and `Timeout` 10 minutes;
  3. asks the model to read the file and reply with the word.
- **Assertions:**
  - The run ends `RunStopEndTurn` with no error.
  - At least one `read_file` call succeeded.
  - The final text contains the word.
  - `Agent.Resolution()` reports the provider, tier `STANDARD`, and the model id and effort `catalog.Select` gives.
  - Input and output tokens are positive, and the run is not flagged unpriced.
  - The cost is positive on `anthropic` and `openrouter`, and zero on `ollama`.

  The subtest logs the model, effort, turns, tokens and cost.
- **Make target.** `make provider-live-test` runs `AGENTKIT_LIVE=1 go test -count=1 -v -timeout 30m -run TestLive ./internal/providerconformance`. It is not part of `check` or `check-release`.

### 6. The examples choose provider, model and effort

- `examples/codingagent`, `examples/customtools` and `examples/mcp` each read three variables from the process environment and pass them unchanged to `agentkit.Config`:

  | Variable | Config field | Default |
  |---|---|---|
  | `AGENTKIT_PROVIDER` | `Provider` | empty, which means `anthropic` |
  | `AGENTKIT_MODEL` | `Model` | `STANDARD` |
  | `AGENTKIT_EFFORT` | `Effort` | empty: the tier's effort |

  Each example keeps its own few lines for this. They share no helper, as `examples/README.md` requires.
- **What the examples drop.** None of the three calls `catalog.Lookup` or `anthropic.Resolve`, or imports `anthropic-sdk-go`.
- **Bad configuration.** A bad value or a missing credential surfaces as `agentkit.New`'s error, before any request. Examples include an unknown provider, an unknown effort and a missing `OPENROUTER_API_KEY`. The example prints `error: …` and exits 1.
- **The summary line.** Each example prints the provider, model id and effort from `Agent.Resolution()`, alongside turns, tokens and cost. When the run's usage is flagged unpriced, the line shows `cost unknown` instead of a dollar figure.
- Each example's header comment shows one invocation per provider.

### 7. Example tests

- Each of the three examples gains a `main_test.go`. It starts a local Ollama-shaped `httptest` server that answers one text turn as NDJSON and records requests. It then runs the example as a program (`go run .`) with the variables set, without network or a credential, in two subtests:
  - **Override precedence.** With `AGENTKIT_PROVIDER=ollama`, `OLLAMA_HOST=<server>`, `AGENTKIT_MODEL=SIMPLE` and `AGENTKIT_EFFORT=high`:
    - the example exits 0;
    - stdout carries the server's text;
    - the summary names `ollama`, `qwen3.8:27b` and `high`;
    - the recorded request has model `qwen3.8:27b` and `think` `"high"`.
  - **Unknown provider.** With `AGENTKIT_PROVIDER=openai`:
    - the example exits non-zero;
    - the output names `openai` and the three known providers;
    - the server records no request.
- The subprocess environment is the test's environment plus these values, so a developer's own `OPENROUTER_*` or `ANTHROPIC_*` values cannot change the outcome. Only Ollama variables are consulted on the `ollama` path.

### 8. README quick start and documentation

**`README.md`:**
- The opening paragraph names the three providers.
- The quick start's Go snippet builds the agent with `Provider`, `Model: "STANDARD"` and a toolset, with no client and no SDK import. A comment names the other two providers. The error comment says a missing credential is reported by name, for example `OPENROUTER_API_KEY`.
- A short table gives what each provider reads: `ANTHROPIC_*` (or Vertex or Bedrock), `OPENROUTER_API_KEY`, `OLLAMA_HOST`.
- The quickstart command block and the "Make targets" table list `make provider-live-test`.

**`examples/README.md`:**
- "Configuring an application" is rewritten around provider, model and effort, including the tier table and the variable tables (`OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL`, `OLLAMA_HOST`).
- "Other variables" lists `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` (new default `STANDARD`) and `AGENTKIT_EFFORT`.
- The intro no longer says the model-calling examples need an Anthropic credential.
- Troubleshooting gains entries for a missing `OPENROUTER_API_KEY`, an unreachable Ollama host and a model that has not been pulled.

**`examples/{codingagent,customtools,mcp}/README.md`:** "Run it" shows one run per provider, and "What you'll see" shows the new summary line.

**`docs/configuration.md`:**
- The "Other variables" table gains `AGENTKIT_PROVIDER` and `AGENTKIT_EFFORT` (examples only) and `AGENTKIT_LIVE` (live tests only).
- A short "Live provider tests" paragraph covers the gate, each provider's prerequisites, the per-provider bounds and `make provider-live-test`.

**`docs/architecture.md`:**
- The package graph and Packages table gain `internal/providerconformance (tests only)`, with its first-party imports.
- The examples row lists their actual imports.
- "Testing layout" describes the conformance suite and the live tests.

This spec adds no public Go API, so `docs/api.md` and `docs/generated/` do not change.

## Design Decisions

1. **The suite runs through `agentkit.New` with a provider name, not through each `core.ProviderClient` directly.** That is the path an embedder takes. The real loop then produces the tool call and its paired result, so "the same logical turns through all three providers" covers resolution, provider and loop together.
2. **It lives in `internal/providerconformance`, with every line of code in `_test.go` files.** A root internal test cannot import a package that imports the root (the reason `internal/testkit` avoids the root). Keeping the `httptest` servers in test files keeps to the spirit of the no-listen rule. A tests-only internal package is outside the root line budget.
3. **The vendor fakes are written against the wire formats with `encoding/json`, not with vendor SDKs or the providers' test helpers.** A fake built from the code under test would agree with any bug in it. TS-27-50 also forbids importing the OpenRouter SDK outside `provider/openrouter`.
4. **The environment is a map-backed `Config.Getenv` holding only that provider's variables.** This keeps the suite hermetic on a machine with real keys exported, and lets subtests run in parallel. `t.Setenv` would forbid parallel subtests.
5. **The reference for event order is `provider/faux`'s skeleton, with deltas collapsed and snapshots dropped. It applies only to turns that end normally.** faux declares itself normative. Delta counts depend on the vendor's chunking, and only some providers emit `MessageUpdateEvent`s. On failure, faux and Anthropic already differ about block-end events, so failed turns assert the terminal `ErrorEvent` and `MessageEnd` pair instead.
6. **The input's five turns become seven cases.** The tool call is run under both the vendor's tool stop and its plain stop, because "the loop continues on tool calls whatever the stop reason" is a cross-provider promise. "An error" is split into an HTTP status before content and an in-stream error after content, because those are different code paths in every provider.
7. **`error_status` uses HTTP 400.** It is the one error that no provider retries, so "exactly one request" holds for all three. OpenRouter's 429 and 5xx retries are provider-specific and are tested in spec 27.
8. **`cut_off` asserts that the tool is never executed, not that the message has no tool call.** Anthropic keeps salvaged tool-use blocks in a failed message, and OpenRouter and Ollama drop them. Both are correct under their specs, and never executing a partial call is the guarantee that matters.
9. **`error_in_stream` checks that the vendor message is contained, not that the text has the provider prefix.** Anthropic's in-stream errors carry no `anthropic:` prefix today. Changing a provider is out of scope.
10. **The cases use tier `STANDARD`, and expected values come from `catalog.Select`.** A tier change is then a catalog change and does not break the suite.
11. **The `text` case uses one logical usage (100 prompt tokens with 60 cache reads, 20 output) rendered in each vendor's own netting, and expects the same canonical usage.** Input net of cache reads is the cross-provider rule most likely to drift, and a single set of numbers shows it.
12. **Live tests need an explicit `AGENTKIT_LIVE=1` and skip under `-short`, as well as needing each provider's prerequisites.** Many developer machines export `ANTHROPIC_API_KEY` or run `ollama serve`. Gating on presence alone would spend money, and run a 27B model, inside every `make check-release`.
13. **The live tests run on tier `STANDARD` with `read_file` alone and a random word.** This is the input's probe loop (read a file, answer from it). The random word proves the answer came from the tool, and the bounds cap cost at $0.50 per provider.
14. **Before the Ollama live test runs, it checks `GET /api/tags` on the host that `provider/ollama` resolves.** The input asks for "a reachable server with the tier model". The library's no-discovery rule does not bind a test, and the request uses the same host resolution as the provider.
15. **The examples default `AGENTKIT_MODEL` to `STANDARD` and pass values through unchanged.** A tier works on every provider, whereas `anthropic/claude-sonnet-5` would be an OpenRouter id under `openrouter` and meaningless under `ollama`. Validation is the library's job.
16. **Only the three model-calling examples read the variables.** The others are scripted on faux by design and must stay offline.
17. **The example tests use an Ollama-shaped server and `go run .`.** Ollama needs no credential, and `examples/codemode` already tests itself as a program. `SIMPLE` with `high` shows that an explicit effort overrides the tier's.
18. **This spec owns the final form of the README quick start and of the examples' model selection.** It replaces whatever interim wording `29_provider_selection` left when it removed `Config.Client *sdk.Client`. The README's consumer-contract tables stay with that spec.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `26_provider_catalog_tiers` | depends on | `catalog.Select` and the `STANDARD` and `SIMPLE` tier rows supply every expected model id, effort and price that the cases and live tests assert. |
| `27_openrouter_provider` | depends on | The OpenRouter behaviour the cases assert, plus `openrouter.ErrStreamTruncated` and `*openrouter.StatusError`. Its TS-27-50 forbids importing the SDK here. |
| `28_ollama_provider` | depends on | The Ollama behaviour the cases assert, plus `ollama.ErrStreamTruncated`, `*ollama.StatusError`, and `ollama.New(...).Host()` for the live prerequisite. |
| `29_provider_selection` | depends on | Several pieces: `Config.Provider` as a name; a hermetic `Config.Getenv`; `agentkit.New` building each provider; `Agent.Resolution()`; the run-level unpriced flag; the examples already moved off `*sdk.Client`. |
| `10_anthropic_wire` | depends on | The Anthropic stream, failure and status-error conventions (`ErrStreamTruncated`, `*StatusError`, `fail`/`finish`), and `anthropic.Resolve` for the live prerequisite. |
| `11_minimal_driver` | depends on | The loop's continuation on tool presence, its short-circuit on error, and its forwarding of provider events, all of which the cases assert end to end. |

## Verified External API

No new Go module is added. The suite depends on symbols from earlier specs of this split, which are not in the tree yet, and on three vendor wire formats.

**First-party symbols from specs 26–29.** None is in the tree on 2026-10-21. Confirm the landed names before writing the tests.

| Symbol | Assumed shape | Status |
|---|---|---|
| `catalog.Select` | `Select(provider, model string, effort core.Effort) (Selection, error)`; `Selection{Provider string; Tier Tier; Model core.Model; Effort core.Effort; Listed bool}` | NOT FOUND (spec 26) |
| `openrouter.ErrStreamTruncated`, `openrouter.StatusError` | sentinel; `*StatusError{Code int; Text string}` | NOT FOUND (spec 27) |
| `ollama.New`, `(*ollama.Client).Host`, `ollama.ErrStreamTruncated`, `ollama.StatusError` | `New(m core.Model, opts Options) (*Client, error)`; `Host() string`; sentinel; `*StatusError{Code int; Text string}` | NOT FOUND (spec 28) |
| `agentkit.Config.Provider`, `Config.Getenv`, `Agent.Resolution()`, run-level unpriced flag | `Provider string`; `Getenv func(string) string`; a resolution reporting provider, tier, model, effort and `Listed`; `core.Usage.Unpriced bool` (per the provider_selection notes) | NOT FOUND (spec 29) |
| `anthropic.ErrStreamTruncated`, `anthropic.StatusError`, `anthropic.Resolve`, `anthropic.OSEnv` | as in `provider/anthropic/stream.go` and `resolve.go` | verified in tree |
| `faux.New`, `faux.Turn`, `faux.FauxText`, `faux.FauxThinking`, `faux.FauxToolCall`, `(*faux.Provider).Stream` | as in `provider/faux/faux.go` | verified in tree |

**Wire formats the fakes render and decode:**

| Vendor | Shape assumed | Source |
|---|---|---|
| Anthropic | SSE `event: <type>` and `data: {…}` lines: `message_start` (with `usage.input_tokens` and `cache_read_input_tokens`); `content_block_start`, `_delta` (`text_delta`, `thinking_delta`, `signature_delta`, `input_json_delta`) and `_stop`; `message_delta` (`stop_reason`, `usage.output_tokens`); `message_stop`; `error`. HTTP error body `{"type":"error","error":{"type","message"}}`. Request fields: `model`, `messages[].content[]` (`text`, `thinking{thinking,signature}`, `tool_use{id,name,input}`, `tool_result{tool_use_id,content}`), `output_config.effort`, `tools[].name` | verified against in-repo fixtures (`provider/anthropic/stream_test.go`, `runner_metadata_smoke_test.go`, `request_test.go`) |
| OpenRouter | SSE `data: {chunk}` with `id`, `object` `chat.completion.chunk`, `created` and `model`, and `choices[0].delta` holding `content`, `reasoning`, `reasoning_details[]{type,text,format,index}` and `tool_calls[]{index,id,type,function{name,arguments}}`, plus `finish_reason`. A usage chunk carries `prompt_tokens`, `completion_tokens`, `total_tokens`, `prompt_tokens_details.cached_tokens`, `completion_tokens_details.reasoning_tokens` and `cost`, followed by `data: [DONE]`. Error chunk and HTTP error body: `{"error":{"code","message"}}`. Request fields: `model`, `messages[]{role,content,tool_calls,tool_call_id,reasoning_details}`, `reasoning.effort`, `tools[].function.name` | unverified: taken from spec 27's PRD and test spec and from the input's probe |
| Ollama | NDJSON lines `{"model","message":{"role","content","thinking","tool_calls":[{"id","function":{"index","name","arguments":{…}}}]},"done":false}`. Final line `{"done":true,"done_reason":"stop","prompt_eval_count","prompt_eval_cached_count","eval_count"}`. Error line and HTTP error body: `{"error":"…"}`. `GET /api/tags` returns `{"models":[{"name","model"}]}`. Request fields: `model`, `messages[]{role,content,thinking,tool_calls,tool_name,tool_call_id}`, `think`, `tools[].function.name` | unverified: taken from spec 28's test spec and from the input's probe; `prompt_eval_cached_count` and `tool_call_id` are probe-only |
