---
spec_id: "27"
spec_name: "openrouter_provider"
title: "OpenRouter provider on the OpenRouter Go SDK"
status: "active"
created_at: "2026-10-21T10:40:29.359459Z"
updated_at: "2026-10-21T10:40:29.359459Z"
intent_hash: "d45f81729bb507ff530747dd9051cb1de7202e2dad3cf496a76c543894982b0a"
schema_version: 2
source: "docs/prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md"
---
## Intent

An AgentKit agent can run on a model served by OpenRouter. It reaches OpenRouter through OpenRouter's own Go SDK and OpenRouter's native Chat Completions API. This spec delivers `provider/openrouter`, a `core.ProviderClient` with these responsibilities:

- turn a canonical request into an OpenRouter request;
- stream the response back as canonical events and an assistant message;
- handle OpenRouter's credentials, errors, retries, reasoning replay, usage and reported cost the way OpenRouter documents them.

It also adds the SDK as an approved, pinned dependency. Choosing this provider from `agentkit.Config` is not part of this spec.

## Goals

1. A two-turn tool loop completes against a local recording server. The provider is `openrouter.New(m, openrouter.Options{Env: …})`, where `m` is a `core.Model` such as the row `catalog.Select("openrouter", "STANDARD", "")` returns, and `Env` holds `OPENROUTER_API_KEY` and `OPENROUTER_BASE_URL` pointing at the server. The loop is a tool call, its result, then a final text. The second recorded request carries:
   - the model id as written;
   - every tool's full JSON Schema;
   - the first turn's tool-call id and argument string, byte for byte;
   - the result paired to that id;
   - the first turn's `reasoning_details` fragments, as received.
2. Without `OPENROUTER_API_KEY`, `New` fails with `openrouter.ErrNoAPIKey`, whose message names the variable, and no request is made. The provider reads no variable except `OPENROUTER_API_KEY` and `OPENROUTER_BASE_URL`. No Anthropic or Ollama value appears in the recorded request.
3. Retries and authentication errors:
   - A 429 or 5xx is retried up to `Options.MaxRetries` times (default 2), honouring `Retry-After`. This applies both to an HTTP response and to an error chunk inside a 200 stream that arrives before any content.
   - A 401 or 403 ends the turn at once with an error naming `openrouter` and `OPENROUTER_API_KEY`.
   - No error text, event or warning contains the key.
4. Usage reported as `prompt_tokens` 354 (of which `cached_tokens` 256), `completion_tokens` 120 (of which `reasoning_tokens` 80) and `cost` 0.00012 becomes `core.Usage{InputTokens: 98, CacheReadTokens: 256, OutputTokens: 120, ReasoningTokens: 80, CostUSD: 0.00012}`, with the cost's presence bit set.
5. A stream that ends without a `finish_reason` ends the turn with `openrouter.ErrStreamTruncated`. The turn's message has `StopReasonError` and carries no tool call.
6. The dependency is in place and nothing else breaks:
   - `go.mod` requires `github.com/OpenRouterTeam/go-sdk v0.9.66` directly.
   - The allowlist and `docs/DEPS.md` approve it.
   - The policy tests that pin module graphs and the provider-directory list are restated for the new module and package.
   - Every other existing test passes unchanged, and `make check` passes.

## Non-goals

Later specs of this split cover the following:

- **`ollama_provider`**: the Ollama `core.ProviderClient` and its DEPS rulings. This spec adds the shared tool-pairing repair in `provider` (Requirement 4), which that spec may reuse. It does nothing else for Ollama.
- **`provider_selection`**:
  - changing `agentkit.Config` (`Provider` as a name, `Client core.ProviderClient`) and having `agentkit.New` build this provider;
  - the exported resolution an embedder calls without building an agent;
  - credential isolation proven across every provider pair at the agent level;
  - reporting the resolved provider, model and effort;
  - flagging a *run* as unpriced.
- **`provider_conformance`**: the cross-provider conformance suite, the opt-in live smoke test against openrouter.ai, the examples' `AGENTKIT_*` variables and the README quick start.

Out of scope altogether:

- Any compatibility endpoint: OpenRouter's Anthropic skin, or the Anthropic wire pointed at OpenRouter. Only OpenRouter's native Chat Completions is used.
- Explicit prompt-cache breakpoints (`cache_control`). Caching that OpenRouter or the upstream applies on its own is still measured.
- OpenRouter routing preferences (`provider`), model fallbacks (`models`), plugins, web search, server-side tools and `transforms`.
- Images, audio and files.
- Discovering model limits from OpenRouter's models list. Limits come only from the `core.Model` passed in.
- App-attribution headers (`HTTP-Referer`, `X-Title`).
- The Anthropic provider's other seams: `Headers`, `OnPayload`, `OnResponse`, `Timeout`, `ToolPrefix`, `BillingLookup`, `CacheRetention` and `Transport`.
- `strict` tool declarations, and any rewriting of tool schemas.
- `wire.Guard` checks on inbound chunks (Design Decision 13).
- Salvaging truncated tool arguments (Design Decision 9).
- Accounting for BYOK upstream cost.
- Changes to `provider/anthropic`, `catalog`, `agentkit.Config` or the loop.
- Adding `provider/openrouter` to `internal/policy`'s `apiPackages` and its exported-API golden. `provider/anthropic` is not in it either.

## Background

**What exists.** `provider/anthropic` is the only real wire. It runs on `anthropic-sdk-go` v1.79.1, and its patterns are the ones this provider follows:

- `anthropic.Provider(m core.Model, opts Options) core.ProviderClient` builds a client for one model.
- `Stream` refuses a context that is already done. Otherwise it returns `core.StreamChannel(s)` over a `core.EventStream`.
- A `decodeState` emits start events and deltas as they arrive. After the stream ends it emits the block-end events in block order, then a `MessageEndEvent` (`finish`).
- A failure is one value: the partial message with `StopReasonError` and `ErrorMessage`, a terminal `ErrorEvent`, and the result's `Err` (`fail`).
- A cancelled caller context ends the turn as `StopReasonAborted` with `ErrorMessage` `"Request was aborted"` and `core.ErrAborted`.
- Errors carry a package prefix and the HTTP status (`StatusError{Code, Text}`), so a caller's policy can match `429`.
- `ErrStreamTruncated` marks a body that stops before its final event.
- `applyThinking` reads `core.Model.Efforts`. With a nil map the effort is sent by its own name. With a map, an absent or nil entry sends nothing. Nothing is clamped.
- Environment lookup goes through `Options.Env`, then `Options.Getenv`, then the process environment. An empty value counts as unset (`provider/anthropic/client.go`, `providerEnv`).
- Its transcript repair (`RepairTranscript`, `provider/anthropic/repair.go`) demotes unsigned or foreign thinking to visible text. That is right for Anthropic and wrong here.

**The core contract.**

- `core.ProviderClient.Stream(ctx, core.Request) (<-chan core.StreamEvent, error)`.
- `core.Usage.InputTokens` is net of cache reads and writes. `ReasoningTokens` is a subset of `OutputTokens`. `SetCost` sets the presence bit `UsageCostUSD`.
- `core.ThinkingBlock{Thinking, Signature, Redacted}`. `Signature` is opaque and "never inspected" by anything but its provider. The Anthropic provider already carries a redacted block's payload in it.
- `core.ToolUseBlock.Input` is the provider's argument bytes, verbatim. `core.NewToolUse` trims the bytes, checks they form a JSON object and keeps them.

**The loop** (`loop.go`):

- It iterates on the presence of tool-use blocks, never on the stop reason.
- It executes no call of a `StopReasonLength` turn.
- It short-circuits on `StopReasonError` and `StopReasonAborted`.
- `Agent.priced` prices a usage at the catalog row only when the provider left the cost bit clear.
- `MaxCostUSD` reads the summed cost.

**Package `provider`.**

- `ComputeCost(m, u)` prices an already-netted usage.
- `BillingModel` records the served model.
- `ToolResultText(v)` renders a tool result for a wire with no error flag, with an `Error: ` prefix. Its doc names the Chat Completions wire.

**What the deleted wires taught** (`docs/archive/wires.md`):

- Chat Completions reports no usage on a stream without `stream_options.include_usage`.
- Arguments travel as a JSON string and must be replayed as received.
- A tool with no schema is declared as an empty object.
- A stream without its terminal signal is truncated, not complete.
- OpenRouter's reasoning needed a provider marker, or repair demoted it to visible text every turn.

**The catalog** (spec `26_provider_catalog_tiers`) lists `openrouter` / `qwen/qwen3.8-flash`:

- 1,000,000-token window and 131,072-token output cap;
- $0.15 / $0.47 / $0.016 per million input / output / cache-read tokens;
- `Efforts` low, medium, high and max, each mapped to its own name (no xhigh);
- vendor `api` `"openrouter-chat"`.

An unlisted OpenRouter id comes with a 131,072 / 32,768 fallback, no price and a nil `Efforts`.

**The probe of 2026-10-21** (go-sdk v0.9.66, Chat Completions, `qwen/qwen3.8-flash`):

- The loop completes at low, medium, high and max effort.
- Tool-call ids look like `call_<hex>`, and arguments arrive as a JSON string.
- Reasoning streams as many `reasoning_details` fragments, and replaying them as received works.
- `usage.cost` is in USD, and `prompt_tokens` includes `cached_tokens`.
- A 429 arrives either as a request error or inside a 200 stream, as an error chunk with no finish reason.
- The SDK's default retry covers 5xx only and backs off for up to an hour.

**Policy that the new module touches:**

- `internal/policy/deps_test.go`:
  - `approvedDeps` is the allowlist, and each entry needs a reason comment.
  - `preSpec13Direct` is the exact direct set of the root module that `TestSandboxDepsPromoted_TS13_65` asserts. The same test compares the root `go list -m all` with `testdata/go-list-m-all.txt`.
- The sandbox module replaces the root module at `../`, so its graph includes every requirement in the root `go.mod`. `TestRsyncDependency_TS14_42`, `TestLandlockPromoted_TS15_40` and `TestPodmanAddsNoDependency_TS17_60` compare it with `testdata/sandbox-go-list-m-all.txt`. TS-15-40 also requires that file to equal `sandbox-go-list-m-all.14.txt`. None of these tests is skipped under `-short`.
- `repository_cut_test.go` `TestProviderDefaults_TS09_11` requires the directories under `provider/` to be exactly `anthropic,faux`.

## Requirements

### 1. Constructing the provider

- **Constructor.** `openrouter.New(m core.Model, opts openrouter.Options) (core.ProviderClient, error)` builds a provider for one model.
- **`Options` fields:**
  - `Env map[string]string`;
  - `Getenv func(string) string`;
  - `HTTPClient *http.Client`;
  - `MaxRetries *int` (nil means `DefaultMaxRetries`, which is 2; a negative value means 0);
  - `Now func() time.Time`;
  - `Warnf func(format string, args ...any)`.
- **Exported constants:**
  - `API core.API = "openrouter-chat"`;
  - `APIKeyVar = "OPENROUTER_API_KEY"`;
  - `BaseURLVar = "OPENROUTER_BASE_URL"`;
  - `DefaultBaseURL = "https://openrouter.ai/api/v1"`;
  - `DefaultMaxRetries = 2`.
- **Lookup.** A variable is read from `Options.Env` first; an empty value falls through. Next comes `Options.Getenv` when it is set, otherwise the process environment. An empty value counts as unset. The provider reads `APIKeyVar` and `BaseURLVar` and no other variable.
- **`New` fails, before any request, when:**
  - the key is unset: the error wraps `ErrNoAPIKey`, whose text names `OPENROUTER_API_KEY`;
  - `OPENROUTER_BASE_URL` is set but is not an absolute `http` or `https` URL: the error names the variable and shows the value with any user-info removed;
  - `m.ID` is empty.

  `New` makes no network request. An unreachable host is found on the first request.
- **Endpoint.** The base URL is `OPENROUTER_BASE_URL` with any trailing `/` removed, or `DefaultBaseURL`. Every request is a `POST` to `<base>/chat/completions`. It goes only to that host and carries the key only as `Authorization: Bearer <key>`.
- **SDK client.** The provider builds one SDK client in `New`, with the key, the base URL and `HTTPClient` when it is set. The SDK's own retries are turned off (Requirement 7).
- **`Stream`.** It refuses a context that is already done, returning that context's error and no channel. Otherwise it returns a channel per the `core.ProviderClient` contract. Every failure after that is encoded in the stream. `Stream` is safe for concurrent use on one provider value.

### 2. Encoding the request

- **Body fields.**
  - `model` is `m.ID`, exactly as written. Nothing is stripped or rewritten.
  - `stream` is `true`, and `stream_options.include_usage` is `true`.
- **System prompt.** The `core.TextBlock`s of `Request.System` are joined with a blank line and sent as one leading `system` message. If there is no text, no system message is sent.
- **Message order.** `Request.Prefix` then `Request.Messages` are encoded in order, after repair (Requirement 4). The prefix gets no cache marker.
- **User messages.** A `core.UserMessage` becomes a `user` message with its text blocks in order. A user message with no text is skipped.
- **Assistant messages.** A `core.AssistantMessage` becomes an `assistant` message with:
  - its text blocks joined, as `content`, with no content when there is no text;
  - its tool calls, as `tool_calls`, each with `id`, `type: "function"`, `function.name` and `function.arguments`. The arguments are the `ToolUseBlock.Input` bytes as a string, never decoded and re-encoded;
  - `reasoning_details` per Requirement 4.

  An assistant message with no text, no tool calls and no reasoning to replay is skipped.
- **Tool results.** Each `core.ToolResultMessage` becomes its own `tool` message, with `tool_call_id` set to the call's id and the content `provider.ToolResultText(v)`.
- **Tools.**
  - Each tool is declared as `{"type":"function","function":{"name","description","parameters"}}`.
  - `parameters` is the tool's full JSON Schema as `schema.Schema` marshals it, with property order, `additionalProperties`, `format` and bounds intact.
  - A tool with a nil schema is declared with `{"type":"object","properties":{}}`.
  - No `strict` flag is sent.
- **Tool choice.** `core.ToolChoiceAuto` is sent as `"auto"` and `core.ToolChoiceNone` as `"none"`. When the choice is unset, nothing is sent.
- **`max_completion_tokens`:**
  - `*Request.MaxTokens` when that is positive, capped at `m.MaxOutputTokens` when the cap is positive;
  - otherwise `m.MaxOutputTokens` when that is positive;
  - otherwise the field is omitted.
- **Sampling and stop.** `temperature` and `top_p` are sent when their pointers are set, including an explicit 0. `stop` is sent when `StopSequences` is non-empty.

### 3. Effort

- An empty `Request.Effort` sends no `reasoning` object, so the vendor's default applies.
- Otherwise the wire value follows `m.Efforts`, as in the Anthropic provider:
  - a nil map sends the effort by its own name;
  - a map entry that is absent or nil sends nothing;
  - a present entry sends its value.
- A value that parses as a positive integer is sent as `reasoning.max_tokens`. Any other value is sent as `reasoning.effort`.
- No effort is clamped to another level, and sending an effort never removes `temperature` or `top_p`.

### 4. Replaying the transcript

- **Shared repair.** Package `provider` gains one transcript repair for chat-style wires. It returns a view and never changes its input. It applies these rules, in order:
  1. Drop an assistant message whose stop reason is `Error` or `Aborted`.
  2. Drop a tool result whose call is not in a kept assistant message.
  3. For every tool call still unanswered, insert an error result whose text is `No result provided`. It goes in that call's position among the turn's results, before the next non-result message.

  It returns a count of what it changed. The OpenRouter provider runs it on the prefix and on the history of every request, and reports a non-empty count through `Options.Warnf`.
- **Ids.** Tool-call ids pass through unchanged, whichever model produced them.
- **Thinking.** Thinking is never sent as visible text.
  - An assistant message whose `Model` equals `m.ID` replays `reasoning_details`: the fragments of its thinking blocks, concatenated in block order.
  - A thinking block's fragments are its `Signature`, read as a JSON array. A `Signature` that is empty or not a JSON array contributes nothing.
  - A message from another model sends no `reasoning_details`.
  - A thinking block's `Thinking` text is never placed in `content`.

### 5. Decoding the stream

- **Message start and blocks.**
  - A `MessageStartEvent` is emitted once per turn, before any content event. A retried attempt does not emit another one.
  - The partial message carries `Model` set to `m.ID`, `Effort` set to `Request.Effort`, and `Timestamp` from `Options.Now` or the clock.
  - Block indices are assigned in order of first appearance, and events follow the order in which the vendor sends the deltas.
  - A new block opens when the kind of delta changes: text, thinking or a tool call.
- **Text.** A non-empty `delta.content` emits `TextStartEvent` at the start of a block and then `TextDeltaEvent`.
- **Thinking.**
  - Each element of `delta.reasoning_details` is appended to the open thinking block's fragment list, in arrival order and with every field it arrived with. A thinking block is opened if none is open.
  - The visible thinking text is `delta.reasoning` when the chunk carries it. Otherwise it is the `text` of `reasoning.text` fragments and the `summary` of `reasoning.summary` fragments. Encrypted fragments add no text.
  - Thinking text emits `ThinkingStartEvent` and `ThinkingDeltaEvent`, never text events.
  - A finished thinking block has `Thinking` set to the accumulated text, `Signature` set to the JSON array of its fragments (empty when none arrived), and `Redacted` false.
- **Tool calls.**
  - Fragments are accumulated by their `index`. The first fragment that carries the id and name emits `ToolCallStartEvent`.
  - Each `function.arguments` fragment emits `ToolInputDeltaEvent` with the fragment as received.
  - The arguments are the concatenation of the fragments, byte for byte, passed to `core.NewToolUse`. Empty arguments become `{}`.
  - Arguments that are not a JSON object become `{}`, and `Warnf` reports the call's id and name.
  - A call that arrives without an id gets `call_` followed by random hex, unique within the turn.
- **Ignored input.** Choices other than index 0, empty deltas, role-only deltas and SSE keep-alive comments (`: OPENROUTER PROCESSING`) are ignored and are not content.
- **Completion.** A stream is complete when a choice has carried a `finish_reason`. Reading continues to the end of the body to collect usage. Then:
  1. block-end events (`TextEndEvent`, `ThinkingEndEvent`, `ToolCallEndEvent`) are emitted in block order, once each, with the whole block;
  2. a `MessageEndEvent` with the final message follows;
  3. the stream's result is that message.
- **Stop reasons.** `finish_reason` is kept verbatim in `RawStopReason` and mapped:
  - `stop` → `StopReasonStop`;
  - `length` → `StopReasonLength`;
  - `tool_calls` → `StopReasonToolUse`;
  - `content_filter` → `StopReasonRefusal`;
  - `error` is handled as an in-stream error (Requirement 6);
  - anything else → `StopReasonStop`.

  Tool calls are kept in the message whatever the finish reason. `ResponseID` and `ResponseModel` come from the chunks' `id` and `model`.

### 6. Truncated streams and errors inside the stream

- **Truncation.** A body that ends without any `finish_reason` fails the turn with `ErrStreamTruncated` ("openrouter: stream ended before finish_reason").
- **Error chunks.** A chunk carrying a top-level `error` object (`code`, `message`), or a choice with `finish_reason: "error"`, is an in-stream error.
  - When no content event has been emitted yet, the code is 429 or 500–599, and retries remain, the attempt is discarded and retried (Requirement 7).
  - Otherwise the turn fails with a `*StatusError` whose `Code` is the vendor's code and whose `Text` is `openrouter: error <code> in stream: <message>`.
- **A failed turn's message.**
  - It has `StopReasonError`, `ErrorMessage` set to the error text, and the text and thinking blocks streamed so far.
  - It has **no** tool-call blocks, so nothing from a failed stream can be executed or replayed.
  - It carries whatever usage had arrived, priced as in Requirement 8.
- **Event order on failure.** A terminal `ErrorEvent`, then the `MessageEndEvent`, then the result with `Err`, as in the Anthropic provider.

### 7. HTTP errors, retries and cancellation

- **Retry loop.** The provider runs its own retry loop around the SDK call, with the SDK's retries off.
  - A 429 or 5xx response is retried up to the configured count.
  - So is an in-stream 429 or 5xx before content (Requirement 6).
  - A 401 or 403 is not retried. It fails the turn with `openrouter: HTTP <code>: <vendor message>` followed by a note to check `OPENROUTER_API_KEY`.
  - Any other non-2xx is not retried. It fails with `openrouter: HTTP <code>: <vendor message>`.
  - The vendor message is `error.message` from the body when present.
  - All of these are `*StatusError` values carrying the status code.
- **Waiting between attempts.**
  - When the response carries `Retry-After` (seconds or an HTTP date) between 0 and 60 seconds, that is the wait.
  - Otherwise the wait is exponential: 0.5 s doubling to a cap of 8 s, with up to 25% jitter.
  - When retries run out, the last error ends the turn.
- **Transport failures.** A failure with no HTTP response is not retried. It fails the turn with `openrouter: <error>`.
- **Cancellation.** Cancelling the caller's context stops the request promptly: during the request, during streaming, or during a wait between attempts. The turn ends as `StopReasonAborted`, with `ErrorMessage` set to `"Request was aborted"` and the result's `Err` set to `core.ErrAborted`.
- **No credential in output.** No error text, `ErrorEvent`, `ErrorMessage` or `Warnf` output contains the API key. Any occurrence of the key in text the provider builds is replaced with `[redacted]`.

### 8. Usage and cost

- **Token counts.** From the chunk that carries `usage`:
  - `CacheReadTokens` is `prompt_tokens_details.cached_tokens`.
  - `CacheWriteTokens` is `prompt_tokens_details.cache_write_tokens`, when reported.
  - `InputTokens` is `prompt_tokens` minus both, floored at 0.
  - `OutputTokens` is `completion_tokens`.
  - `ReasoningTokens` is `completion_tokens_details.reasoning_tokens`, capped at `OutputTokens`.
  - `TotalTokens` is `total_tokens`.

  A presence bit is set for exactly the fields reported.
- **Requests.** `Requests` is 1 whatever the number of attempts.
- **Billed model.** `BilledModel` is the served `model` when it differs from `m.ID`.
- **Cost.**
  - When the response reports `usage.cost`, that value is the cost, set with `SetCost`.
  - Otherwise, when `m` has any non-zero price, the cost is `provider.ComputeCost(&m, usage)`.
  - Otherwise `CostUSD` stays 0 and its presence bit stays clear, so a caller can tell an unpriced response from a free one.
- **No usage.** A response that reports no usage leaves the usage unreported.

### 9. The dependency and the repository's policy

- **Requirement.** `go.mod` requires `github.com/OpenRouterTeam/go-sdk v0.9.66` directly. A policy test, like TS-10-1, asserts that exact version and that the requirement is not indirect. Only `provider/openrouter` imports the SDK.
- **Allowlist.** `approvedDeps` in `internal/policy/deps_test.go` gains the module, with a reason comment above it.
- **Ruling.** `docs/DEPS.md` gains a row in the allowlist table and a ruling section that covers:
  - who uses it;
  - the pin and why: a beta 0.x SDK, upgraded only deliberately;
  - why this module: the vendor's own SDK for its native API;
  - what AgentKit still owns: its own retry loop, and why;
  - every module it adds to the root graph, as `go mod why -m` reports them.

  The note above the "Rejected" table is updated, because the vendor-SDK reversal now covers two wires.
- **Graph snapshots.** The snapshot tests are restated to accept exactly the modules the SDK adds:
  - `preSpec13Direct` gains the SDK;
  - `testdata/go-list-m-all.txt` and `testdata/sandbox-go-list-m-all.txt` are regenerated;
  - TS-15-40 compares only the `.14` snapshot's lines and go-landlock, not whole-file equality;
  - TS-14-42's `.13` subset check still holds.

  If the SDK moves the version of a module already in a graph, the ruling records the move and the tests pin it, as spec 14's D-1 did.
- **Nested modules.** Every nested module in `MODULES` is tidied, so its `go.sum` covers the new module.
- **Provider directories.** `TestProviderDefaults_TS09_11` is restated to expect `anthropic,faux,openrouter`.
- **No model metadata in code.** The non-test sources of `provider/openrouter` contain no model id, window, output cap or price. Everything comes from the `core.Model` passed to `New`.

### 10. Tests and documentation

- **Offline tests.** `provider/openrouter` tests run against an `httptest` server that records each request and replays canned SSE bodies, with `OPENROUTER_BASE_URL` pointed at it. They cover:
  - the model id as written;
  - the full tool schemas and `nil` schema;
  - tool choice;
  - effort, including a nil map, an absent level and a numeric budget value;
  - `max_completion_tokens` capping;
  - sampling and stop;
  - paired tool results and error-prefixed results;
  - argument strings replayed byte for byte;
  - `reasoning_details` replayed as received, same-model only, never as text;
  - the repair rules;
  - the `Authorization` header and the absence of `ANTHROPIC_*` and `OLLAMA_*` values;
  - the variables asked for;
  - the default base URL, checked through an `HTTPClient` transport that records the host without touching the network;
  - event order and block indices;
  - keep-alive comments;
  - every stop-reason mapping;
  - tool calls under `finish_reason: "stop"`;
  - truncation;
  - in-stream errors before and after content;
  - HTTP 401, 403, 402, 429 with `Retry-After`, 5xx and retry exhaustion;
  - `MaxRetries` 0;
  - cancellation during a stream and during a wait;
  - usage netting and the three cost cases;
  - no credential in any output.

  Waits in tests stay short through `Retry-After: 0` or an unexported backoff hook.
- **Shared repair.** The repair function in `provider` has its own tests.
- **Documentation.**
  - `docs/configuration.md` gains an `openrouter.Options` section: fields, variables, lookup order, what is sent, errors and retry policy.
  - `docs/api.md` and `docs/architecture.md` (the package table and the import-layering table) list `provider/openrouter` and the new `provider` repair.
  - `make doc` regenerates `docs/generated/provider/openrouter.txt` and `docs/generated/provider.txt`.

## Design Decisions

1. **`New` returns `(core.ProviderClient, error)` and resolves the key and base URL at construction.** The input requires that a missing key fail before any request with an error naming the variable. `anthropic.Provider` cannot return an error and resolves on every request, so it is not the model to copy here. `provider_selection` can call `New` to check credentials without running anything.
2. **There are no `APIKey` or `BaseURL` fields. `Options.Env` carries explicit values.** The input fixes the lookup order Env, then Getenv, then the process environment. A map entry is already the explicit override, and one path means one isolation rule to test.
3. **The SDK is the transport: client, authentication, server URL, sending and SSE framing. Its retries are turned off, and the provider runs its own retry loop.** The SDK cannot see an error chunk inside a 200 stream, its default retry skips 429, and its backoff can last an hour. One loop handles both kinds of error the same way.
4. **The retry policy mirrors the Anthropic SDK:** 2 retries by default, 0.5 s doubling to 8 s with jitter, and `Retry-After` honoured up to 60 s. The input asks for "the same default as the Anthropic provider", and its SDK uses this curve. A longer `Retry-After` falls back to the curve, so the turn fails within a bounded time.
5. **Only an error before any content is retried.** A retry after content would re-emit text the consumer has already rendered.
6. **`reasoning_details` fragments are stored as a JSON array in `core.ThinkingBlock.Signature` and replayed only to the same model id.** `Signature` is the opaque, provider-issued field, and a redacted Anthropic block already uses it this way. A new core field would change an `apiPackages` package and the golden for one provider. A non-empty array is also the "provider marker" the archive says reasoning needs. The fragments are kept unmerged because the probe replayed them as received and that worked.
7. **The tool-pairing repair is shared in package `provider`, and its rules never touch thinking.** The Anthropic repair turns unsigned or foreign thinking into visible text, which the input forbids here. Rules 2, 2b and 6 are needed by every chat-style wire, and the Ollama spec comes next. `provider` is documented as the home of what every wire needs. The Anthropic provider keeps its own repair unchanged.
8. **Tool-call ids pass through unchanged.** OpenRouter accepts any id string, and rewriting would rename calls the model remembers making.
9. **Arguments that are not a JSON object become `{}` with a warning, and nothing is salvaged.** This matches the Anthropic provider's handling of a non-object. A truncated call arrives under `finish_reason: "length"`, whose calls the loop never executes, or under a missing finish reason, whose calls this provider drops (Decision 10). So salvage would only make a call displayable.
10. **A failed turn keeps its text and thinking but drops all tool calls.** A Chat Completions tool call is not known to be complete until `finish_reason`. The input requires that a partial call is never executed.
11. **Cost comes first from OpenRouter's reported `cost`, then from the row's price, and otherwise the cost bit stays clear.** The reported cost is what the account is charged. Leaving the bit clear is the only signal a provider has that a response was unpriced, and `provider_selection` builds the run-level flag on it.
12. **Usage is requested with `stream_options.include_usage: true`.** The archive records that Chat Completions sends no usage on a stream without it. OpenRouter's own `usage.include` is not used.
13. **`wire.Guard` is not applied to inbound chunks.** The SDK decodes the stream. Obtaining raw bytes would mean wrapping the SDK's HTTP body reader. The host is a fixed TLS endpoint or a proxy the embedder chose.
14. **Tool schemas are sent in full, as `schema.Schema` marshals them, with no `strict` flag.** The input requires the full JSON Schema, and the Anthropic provider's strict-subset rewrite answers Anthropic's limits, not OpenRouter's.
15. **`max_completion_tokens` is the request's bound capped at the row's output cap. With no request value it is the cap, and with neither it is omitted.** The input says "never above the catalog's output cap". Unlike Anthropic, the field is optional on this wire.
16. **A row's numeric effort value is sent as `reasoning.max_tokens`, and a name as `reasoning.effort`.** It follows the same `core.Model.Efforts` reading as Anthropic, so a budget-style row would not produce an invalid `effort` string. The shipped row uses names.
17. **The system blocks are joined into one `system` message.** The agent sends one block, and several leading system messages are rejected by some upstreams.
18. **Errors use the `openrouter:` prefix:**
    - `ErrNoAPIKey`;
    - `ErrStreamTruncated`;
    - `*StatusError{Code, Text}` for HTTP and in-stream vendor errors;
    - the abort text `"Request was aborted"` with `core.ErrAborted`.

    This mirrors the Anthropic provider, so the loop and callers treat both providers alike.
19. **The SDK pin is enforced by a policy test, and the graph snapshots are regenerated rather than bypassed.** The repository treats every new module and version move as a reviewed decision. The sandbox module's graph includes the root's requirements, so its snapshots move too.
20. **`provider/openrouter` stays out of `apiPackages`.** `provider/anthropic` is not in the golden either. The provider's surface is reviewed through `docs/generated` and `docs/configuration.md`.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `26_provider_catalog_tiers` | depends on | Supplies the `openrouter` row (window, output cap, prices, `Efforts` low/medium/high/max) and the unlisted-id fallback that the tests build `core.Model` values from. Also supplies the `"openrouter-chat"` api string that `openrouter.API` matches. |
| `09_repository_cut` | modifies | `TestProviderDefaults_TS09_11` pins `provider/` to `anthropic,faux`, and TS-09-16's allowlist gains the SDK. |
| `10_anthropic_wire` | depends on | The Anthropic provider's stream, failure, abort and status-error conventions and its `Efforts` reading are the patterns this provider mirrors. `provider/anthropic` itself is not changed. |
| `13_sandbox_core` | modifies | `TestSandboxDepsPromoted_TS13_65` asserts the root direct set (`preSpec13Direct`) and the `go-list-m-all.txt` snapshot, and both gain the SDK. |
| `14_sandbox_rsync_sync` | modifies | `TestRsyncDependency_TS14_42` compares the sandbox module's graph with `sandbox-go-list-m-all.txt`, which is regenerated. |
| `15_sandbox_landlock_agent` | modifies | `TestLandlockPromoted_TS15_40` requires the sandbox snapshot to equal the `.14` copy. That check is restated. |
| `17_sandbox_podman` | modifies | `TestPodmanAddsNoDependency_TS17_60` compares with the regenerated sandbox snapshot. |

## Verified External API

`github.com/OpenRouterTeam/go-sdk` is not yet in `go.mod` or `go.sum`, and the module cache is outside the repository. Nothing below could be read, so **every symbol is unverified**. The implementer reads the module at v0.9.66 first. Any difference that changes behaviour is recorded in `docs/errata/<spec>.md`, and the recording-server tests pin what reaches the wire.

| Capability the provider needs | Assumed symbol (Speakeasy-generated SDK) | Status |
|---|---|---|
| Build a client | `openrouter.New(opts ...openrouter.SDKOption) *openrouter.OpenRouter` | unverified |
| API key | `openrouter.WithSecurity(apiKey string) SDKOption` (sends `Authorization: Bearer`) | unverified |
| Base URL | `openrouter.WithServerURL(serverURL string) SDKOption` | unverified |
| Own HTTP client | `openrouter.WithClient(client HTTPClient) SDKOption`, where `HTTPClient` has `Do(*http.Request) (*http.Response, error)` | unverified |
| Turn SDK retries off | `openrouter.WithRetryConfig(retry.Config{Strategy: "none"})` (package `…/retry`) | unverified |
| Streaming chat completion | `(*Chat).Send(ctx, request) (response, error)`, where the request type has `Stream: true` and the response exposes an event stream of chunk values | unverified |
| Iterate chunks | an event-stream value with `Next() bool`, `Value()` (or `Current()`), `Err() error` and `Close() error` | unverified |
| Request fields the wire needs | `model`, `messages` (`system`/`user`/`assistant` with `tool_calls` and `reasoning_details`/`tool` with `tool_call_id`), `tools[].function.parameters` (arbitrary JSON Schema), `tool_choice`, `max_completion_tokens`, `temperature`, `top_p`, `stop`, `reasoning.effort`/`reasoning.max_tokens`, `stream`, `stream_options.include_usage` | unverified: whether each SDK type carries the value without loss (schema property order, every `reasoning_details` field) |
| Chunk fields | `id`, `model`, `choices[].delta.{content,reasoning,reasoning_details,tool_calls[].{index,id,function.{name,arguments}}}`, `choices[].finish_reason`, top-level `error.{code,message}`, `usage.{prompt_tokens,completion_tokens,total_tokens,cost,prompt_tokens_details.{cached_tokens,cache_write_tokens},completion_tokens_details.reasoning_tokens}` | unverified (wire facts from the input's probe) |
| HTTP error | a typed error (`sdkerrors.APIError` or similar) exposing the status code, the response headers (`Retry-After`) and the body | unverified |

Repository symbols this work uses, read in the code:

- `core.ProviderClient`, `core.StreamChannel(*EventStream) <-chan StreamEvent` and `core.NewEventStream(StreamOptions{})`;
- `(*EventStream).Push` and `End(StreamResult{Message, Err})`;
- `core.NewToolUse(id, name string, raw json.RawMessage) (ToolUseBlock, error)`;
- `core.Usage.SetField` / `SetCost` and `core.UsageWire.Into`;
- `provider.ComputeCost(*core.Model, core.Usage) float64`;
- `provider.BillingModel(requested *core.Model, served string, lookup func(string) *core.Model) (*core.Model, string)`;
- `provider.ToolResultText(core.ToolResultMessage) string`;
- `(*schema.Schema).MarshalJSON`.
