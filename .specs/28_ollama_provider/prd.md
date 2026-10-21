---
spec_id: "28"
spec_name: "ollama_provider"
title: "Ollama provider on the native /api/chat API"
status: "active"
created_at: "2026-10-21T10:51:32.122926Z"
updated_at: "2026-10-21T10:51:32.122926Z"
intent_hash: "332871ef540aa3dfdf98e538628ed1fe065fc43fb6a6a0ab2c47051d5dbe0ab5"
schema_version: 2
source: "docs/prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md"
---
## Intent

An AgentKit agent can run on a model served by a local or self-hosted Ollama server. It reaches the server through Ollama's own Go package, `github.com/ollama/ollama/api`, and Ollama's native `/api/chat` endpoint. This spec delivers `provider/ollama`, a `core.ProviderClient` that does three things:

- turns a canonical request into an Ollama chat request;
- streams the response back as canonical events and an assistant message;
- handles Ollama's context window, thinking, tool calls, errors and usage the way Ollama documents them.

It also adds the Ollama package as an approved, pinned dependency. Choosing this provider from `agentkit.Config` is not part of this spec.

## Goals

1. **A two-turn tool loop completes against a local recording server.**
   - Setup: the provider is `ollama.New(m, ollama.Options{Env: {"OLLAMA_HOST": <server>}})`, where `m` is the row `catalog.Select("ollama", "STANDARD", "")` returns (`qwen3.8:27b`, 32,768 / 32,768, effort `medium`). The loop is a tool call, its result, then a final text.
   - Both recorded requests carry:
     - `model` `qwen3.8:27b`;
     - `options.num_ctx` 32768;
     - `truncate: false`;
     - `think: "medium"`;
     - `stream: true`;
     - the tools, with their schemas cut to what Ollama's tool type carries.
   - The second request also carries:
     - the first turn's tool-call id;
     - its arguments with their keys in the order the server sent them;
     - the result paired to that id, with the tool's name;
     - the first turn's thinking text on the assistant message.
2. **Usage is netted and priced at zero.** A final chunk reporting `prompt_eval_count` 354, `prompt_eval_cached_count` 256 and `eval_count` 120 becomes `core.Usage{InputTokens: 98, CacheReadTokens: 256, OutputTokens: 120, Requests: 1, CostUSD: 0}`, with the cost's presence bit set.
3. **Failures are named.**
   - A stream that ends without `done: true` ends the turn with `ollama.ErrStreamTruncated`, and its message carries no tool call.
   - A 404 ends it with `ollama.ErrModelNotFound`, naming the model and `ollama pull <model>`.
   - A refused connection ends it with `ollama.ErrUnreachable`, naming the host and `ollama serve`.
4. **No other provider's settings and no credential are used.**
   - The provider reads no variable except `OLLAMA_HOST`.
   - No recorded request carries an `Authorization` header. This holds even when `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY` and `OLLAMA_API_KEY` are set.
   - No other provider's value appears anywhere in a request.
5. **The dependency is approved and documented, and nothing else changes.**
   - `github.com/ollama/ollama` is a direct, approved requirement pinned at `v0.40.7`, with a ruling in `docs/DEPS.md`.
   - Every existing Anthropic and `provider/faux` test passes unchanged, and `make check` passes.

## Non-goals

These belong to other specs of this split:

- **`provider_selection`**:
  - choosing `ollama` from `agentkit.Config`;
  - building this provider inside `agentkit.New` or an exported resolution;
  - reporting the resolved provider, model and effort;
  - run-level "unpriced" flagging.
- **`provider_conformance`**:
  - the cross-provider conformance suite;
  - the opt-in live smoke test against a real Ollama server;
  - the examples' `AGENTKIT_*` variables and the README quick start.
- **`openrouter_provider`**: anything OpenRouter.

Out of scope altogether:

- Ollama's OpenAI-compatible `/v1/chat/completions` or Anthropic-compatible `/v1/messages` endpoints. Only native `/api/chat` is used.
- Ollama's hosted service (`https://ollama.com`, `OLLAMA_API_KEY`). It is refused (see Requirement 1).
- Managing Ollama: starting the server, pulling models, `keep_alive`, memory settings.
- Discovering limits from `/api/show`. Asking the server what length a model is loaded at.
- Retries. Explicit prompt-cache control.
- Images, audio, files and structured output (`format`).
- Parsing `<think>` tags out of visible content. Thinking is what Ollama reports in `message.thinking`.
- A request hook (`OnPayload`), custom headers, and a per-request timeout option.
- An effort clamping ladder.

## Background

**Ollama was here before.** Until PRD 10, AgentKit had a hand-written `provider/ollama` (an NDJSON wire read through `wire.NewNDJSON`). PRD 10 deleted it with the other non-Anthropic wires, because nothing used it. Traces remain:

- `core.APIOllamaChat` (`"ollama-chat"`) in `core/stopreason.go`;
- `provider.ToolResultText` (`provider/toolresult.go`), whose doc names "the rule the Ollama adapter applies";
- `docs/archive/wires.md` § "Ollama native", which says:
  - failure arrives as an error string inside a 200;
  - `OLLAMA_HOST` without a scheme needs `http://`;
  - a stream without `done: true` is truncated, not complete;
  - native thinking without a signature must not be demoted to text by repair.

The archive also records that Ollama's tool messages once paired by position with no id. The 2026-10-21 probe of `api` v0.40.7 (PRD 14) found tool calls carrying ids and results pairing by them. PRD 09 recorded that `ollama/api` fails on stream lines over about 512 KB.

**What exists to build on.**

- `provider/anthropic` is the template:
  - `Options` with `Env` and `Getenv`;
  - environment lookup in `providerEnv.Lookup` (`client.go`);
  - `Stream` refusing an already-done context, then `core.StreamChannel`;
  - a decode state with `emitStart`, `finish` (block-end events in block order, then `MessageEndEvent`) and `fail`. In `fail`, an abort gives `StopReasonAborted` and `core.ErrAborted` with no `ErrorEvent`. Anything else gives `StopReasonError`, a terminal `ErrorEvent`, then the message end;
  - `applyThinking`, which reads `core.Model.Efforts`.
- Spec 26 (`26_provider_catalog_tiers`) gives the `ollama` catalog row (`qwen3.8:27b`, 32,768 / 32,768, zero price, efforts `low`/`medium`/`high`/`max`), the fallback constants `catalog.OllamaDefaultContextWindow` and `catalog.OllamaDefaultMaxTokens` (32,768 each), and `catalog.Select`.
- Spec 27 (`openrouter_provider`) adds the second vendor-SDK provider, a provider-neutral tool-pairing repair in package `provider`, and the first round of policy-test edits for a new provider SDK.

**What Ollama does (the probe, 2026-10-21, `qwen3.8:27b` on a 32 GB Apple M4).**

- The loop completes at think levels low, medium, high and max. `max` is accepted silently.
- `done_reason` is `"stop"` on a turn that ends in a tool call.
- `prompt_eval_count` includes `prompt_eval_cached_count`.
- The tool schema type is a subset of JSON Schema: `additionalProperties`, `format` and bounds are dropped.
- The server ran the model at 32,768 tokens, its GPU-memory-sized default, although `/api/show` reported 262,144. `num_ctx` takes effect only when a model loads. A different value for an already-loaded model is silently ignored.

## Requirements

### 1. Construction, host and isolation

**Constructor.**
- `ollama.New(m core.Model, opts Options) (*Client, error)` builds the provider for one model. `*Client` implements `core.ProviderClient` and is safe for concurrent `Stream` calls.
- `(*Client).Host() string` returns the resolved base URL.
- `New` makes no network request.
- `New` fails when `m.ID` is empty after trimming.

**`Options` fields.**
- `Host` overrides `OLLAMA_HOST`.
- `Env` (map) and `Getenv` (func) control how variables are read; see below.
- `HTTPClient` is the `*http.Client` requests go through. Nil means a fresh client with the default transport.
- `Now` is the clock for message timestamps.
- `Warnf` receives the transcript-repair report. Nil discards it.

**Reading `OLLAMA_HOST`.**
- `Options.Host` is used when set. Otherwise `OLLAMA_HOST` is looked up the way `provider/anthropic` does it:
  1. `Options.Env` first, where an empty value falls through;
  2. then `Options.Getenv` when it is non-nil;
  3. otherwise the process environment.
- Empty or unset means `http://127.0.0.1:11434` (`ollama.DefaultHost`). The variable name is the exported constant `ollama.HostVar`.
- No other variable is read, and none at all when `Options.Host` is set.

**Parsing the host.** The value is trimmed and parsed by the rules Ollama's own CLI applies, so one value means the same server for `ollama` and for AgentKit:
- no scheme means `http` and default port 11434;
- `http://` without a port means port 80;
- `https://` without a port means port 443;
- a path is kept.

**Invalid hosts.** `New` fails with an error wrapping `ollama.ErrInvalidHost` when:
- the scheme is not `http` or `https`;
- the value cannot be parsed;
- the port is out of range;
- the host carries user information. The message shows the host with the password redacted;
- the host is `ollama.com` or a subdomain of it. The message says the hosted service is not supported.

**No credential.** The provider sends no credential.
- Its HTTP client wraps the transport in a layer that deletes any `Authorization` header before the request goes on. This covers anything the SDK might add.
- An embedder's own `HTTPClient.Transport` runs beneath that layer and may add headers of its own.

### 2. Request encoding

Each `Stream` call sends one request through the SDK client's `Chat` method, with `stream: true`.

- `model` is `m.ID`, exactly as written.
- **Messages, in order:**
  - one `system` message whose content is the text blocks of `Request.System` joined by a blank line, omitted when empty;
  - then `Request.Prefix`;
  - then the repaired `Request.Messages` (Requirement 4).
- **`options.num_ctx`** is sent on every request: `m.ContextWindow`, or `catalog.OllamaDefaultContextWindow` when that is not positive.
- **`truncate: false`** is sent on every request.
- **`options.num_predict`:**
  - the requested `MaxTokens` when it is positive and not above `m.MaxOutputTokens`;
  - otherwise `m.MaxOutputTokens` when that is positive;
  - otherwise the requested value when it is positive;
  - otherwise nothing.
- **Sampling.** `options.temperature`, `options.top_p` and `options.stop` are sent when set on the request. An explicit temperature of 0 is sent.
- `keep_alive`, `format` and images are never sent.

### 3. Tools and tool choice

- Each `core.ToolWire` is declared as a function tool with its name, its description, and the part of its input schema that Ollama's tool-parameter type carries. The provider produces that part by encoding the full schema as JSON and decoding it into the SDK's parameter type, so whatever the type does not model is dropped (`additionalProperties`, `format`, bounds).
- A tool with no schema is declared as an empty object schema (`{"type":"object","properties":{}}`).
- Tools keep their request order.
- A schema the SDK type cannot decode fails the turn before any request is sent, with an error naming the tool.
- **Tool choice.** Unset or `auto` sends the tools. `none` sends no `tools` key at all, because Ollama has no tool choice.
- Arguments are still validated against the full schema before any tool runs. This is existing loop behaviour and needs no new code here.

### 4. Replaying the transcript

- Before encoding, `Request.Messages` goes through the provider-neutral tool-pairing repair that `openrouter_provider` adds to package `provider`. It does three things:
  - drops failed or aborted assistant turns;
  - drops results orphaned by them;
  - adds a synthetic "No result provided" result for an unanswered call, in call order.

  When the repair changed anything, its report goes to `Warnf`. The input slice is never mutated.
- **A user message** is sent as role `user`, with its text as content.
- **An assistant message** is sent as role `assistant`:
  - `content` is its text;
  - `thinking` is the concatenated text of its thinking blocks, but only when the message's `Model` equals `m.ID` and the block is not redacted. Other thinking is dropped and never sent as content;
  - `tool_calls` lists one entry per `ToolUseBlock`, with the block's id, its position as index, its name, and arguments decoded from `ToolUseBlock.Input` with the key order of those bytes kept.
- **A tool result** is one `tool` message per result, in transcript order:
  - content is `provider.ToolResultText` (an error result is prefixed `Error: `);
  - the tool's name;
  - the tool-call id it answers.

### 5. Effort

The effort becomes Ollama's `think` value. The catalog row is read the same way `provider/anthropic` reads it:

- an empty effort sends no `think`;
- a model whose `ThinkingKind` is `none` sends no `think`;
- a nil `Efforts` map sends the effort by its own name;
- with a map, an effort that is absent or nil sends nothing, and a listed effort sends its value, trimmed and lower-cased, as a string level.

So `xhigh` on the `qwen3.8:27b` row sends no `think`, and `high` on an unlisted model sends `"high"`. Nothing is clamped to another level.

### 6. Streaming decode

**Message start.** On the first chunk the provider pushes `MessageStartEvent`. The partial message holds `Model` = `m.ID`, `Effort` = the request's effort, and `Timestamp` from `Options.Now`.

**Text and thinking.**
- Non-empty `message.thinking` is streamed as thinking events (`ThinkingStartEvent`, `ThinkingDeltaEvent`). Non-empty `message.content` is streamed as text events.
- A switch between kinds closes the open block. Each completed block is appended to the partial message, followed by a `MessageUpdateEvent`.
- Reasoning is never reported as visible text.

**Tool calls.** Each tool call in a chunk becomes one `ToolUseBlock`:
- the vendor's id is kept;
- a call with no id gets a synthesized id that is unique within the response and across responses;
- `Input` is the arguments' JSON, with keys in wire order. It goes through `core.NewToolUse`, so absent arguments become `{}`;
- events: `ToolCallStartEvent`, then one `ToolInputDeltaEvent` carrying the whole argument JSON. The block is then appended.

**The final chunk.** On the chunk with `done: true`, after its own content is processed:
1. Block-end events (`TextEndEvent`, `ThinkingEndEvent`, `ToolCallEndEvent`) are pushed in block order.
2. The final message is built:
   - `StopReason`: `done_reason` `length` gives `StopReasonLength`. Otherwise a message with tool calls gives `StopReasonToolUse`. Otherwise `StopReasonStop`;
   - `RawStopReason` is `done_reason` verbatim;
   - `ResponseModel` is the chunk's model;
   - `Usage` follows Requirement 9.
3. `MessageEndEvent` is pushed and the stream ends.

### 7. Incomplete turns

- **Context already done.** `Stream` returns the context's error and no channel. Every later failure is carried in the stream: the failed message, then `Err` on the last item.
- **Truncated stream.** A stream that ends, or whose SDK call returns, without a `done: true` chunk ends the turn with `ErrStreamTruncated`:
  - `StopReasonError` and a terminal `ErrorEvent`;
  - the message keeps the text and thinking that streamed and carries no tool call.
- **Cancellation.** Cancelling the caller's context stops the request promptly. The turn ends with:
  - `StopReasonAborted`;
  - `ErrorMessage` "Request was aborted", the text `provider/anthropic` and `provider/faux` use;
  - `Err` = `core.ErrAborted`;
  - no `ErrorEvent`, and no tool call in the message.

### 8. Errors

Every error text starts with `ollama:`. Cancellation (Requirement 7) takes precedence over every case below.

- **Unreachable host.** A failure to connect (refused, DNS, no route) wraps `ErrUnreachable`. The text names the host, suggests `ollama serve`, and names `OLLAMA_HOST`.
- **Model not pulled.** A 404 wraps `ErrModelNotFound` and is also a `*ollama.StatusError` with `Code` 404. The text names the model and the host, includes Ollama's message, and suggests `ollama pull <model>`.
- **Context window.** An error whose Ollama message says the input exceeds the context length wraps `ErrContextWindow`. The text names the model and the `num_ctx` sent, includes Ollama's message, and says that a model already loaded at a smaller length keeps it until it unloads. This can arrive as a status error or as an in-stream error.
- **401 or 403.** A `*StatusError` whose text names the host, says AgentKit sends no credential to Ollama, and says to check `OLLAMA_HOST`.
- **Any other non-2xx.** A `*StatusError{Code, Text}` with the text `ollama: HTTP <code>: <Ollama's message>`.
- **An error line inside a 200 stream** ends the turn with Ollama's message, classified by the same rules.
- **No retries.** There is exactly one HTTP attempt per `Stream` call.
- No error text, event or warning contains the value of any environment variable other than the host.

### 9. Usage and cost

These come from the `done: true` chunk.

- `InputTokens` is `prompt_eval_count − prompt_eval_cached_count`, floored at 0.
- `CacheReadTokens` is the cached count, set only when it is positive.
- `OutputTokens` is `eval_count`.
- Reasoning tokens are not reported by Ollama and are not set.
- `Requests` is 1 on every turn, failed turns included.
- **Cost.** Whenever the done chunk reported usage, the cost is set to 0 with its presence bit. Ollama runs on the embedder's hardware, so a run on Ollama is priced at zero, not unpriced. This holds for listed and unlisted ids alike.
- A turn that failed before its done chunk carries no token fields.

### 10. Dependency, policy and documentation

**`go.mod`.** `github.com/ollama/ollama` `v0.40.7` becomes a direct requirement of the root module. The following change in `internal/policy/deps_test.go`:
- `approvedDeps` gains it, with a reason comment on the line above;
- `preSpec13Direct` gains it;
- a new test asserts the exact version and that it is not indirect, like `TestAnthropicSDKIsPinned_TS10_1`.

**Snapshots and restated tests.**
- The module-graph snapshots are regenerated: `testdata/go-list-m-all.txt`, `sandbox-go-list-m-all.txt`, and `.14.txt` as TS-15-40 requires.
- Where minimum version selection raises a version that an existing test pins (TS-13-65's grpc and protobuf versions, TS-14-42's baseline lines), the test is restated with the new version recorded as an accepted upgrade, the way `d1Upgrades` records spec 14's. No test is deleted.
- `TestProviderDefaults_TS09_11` expects the provider directories `anthropic,faux,ollama,openrouter`.
- `make tidy` updates every nested module.

**`docs/DEPS.md`** gets an allowlist row and a ruling with these parts:
- *Used by*;
- *Version*, pinned;
- *Why this module*: it is Ollama's own client and types for the native API;
- *What is pulled in*: each non-standard module that `go list -deps ./provider/ollama` reaches. It also states that `go list -m all` grows by the Ollama module's requirements, whose packages are not compiled in. It records that the SDK's line limit of about 512 KB cannot be hit with a 32,768-token output cap. It records that `api.ClientFromEnvironment` is not used, because it reads the process environment directly.

**Other documentation.**
- `docs/configuration.md` gains an `ollama.Options` section covering:
  - the fields;
  - the `OLLAMA_HOST` rules and refusals;
  - what is sent (`num_ctx`, `truncate: false`, `think`, `num_predict`, the tool-schema subset);
  - the caveat about a model already loaded at another length;
  - the zero cost.
- `docs/architecture.md`: a package-graph row for `provider/ollama` (imports `catalog`, `core`, `provider`; third-party `github.com/ollama/ollama/api`) and a Packages row.
- `docs/api.md`: a package row.
- `make doc` writes `docs/generated/provider/ollama.txt`.

## Design Decisions

1. **Requests go through the SDK's `api.Client.Chat`, built with `api.NewClient(base, httpClient)`, never `api.ClientFromEnvironment`.** The input requires Ollama's own package. `ClientFromEnvironment` reads the process environment through `envconfig`, which bypasses `Options.Env` and `Getenv` and breaks test isolation.
2. **Responses are decoded from the SDK's typed `ChatResponse`, not from raw NDJSON, so there is no `wire.Guard` and no bounded frame reader.** The SDK does not expose the bytes. Argument key order relies on the SDK's argument type keeping insertion order, and `Input` is its JSON encoding. Number literals may be re-rendered, which the input accepts by asking only for key order.
3. **`New` returns `(*Client, error)` and fails only for an empty model id or an invalid host.** This mirrors `openrouter.New`. Failing at construction surfaces a bad `OLLAMA_HOST` before any run.
4. **`OLLAMA_HOST` is parsed by Ollama's CLI rules (default port 11434 without a scheme; 80 or 443 with one), reimplemented here.** One value must mean the same server for `ollama` and for AgentKit, and `envconfig.Host()` cannot be pointed at `Options.Env`.
5. **A host on `ollama.com`, or one carrying user information, is refused.** The hosted service is a non-goal. User information in a URL is a credential that Go's HTTP client would send as basic auth, against the "no credential is read or sent" rule.
6. **The transport layer deletes `Authorization` beneath the SDK and above the embedder's transport.** It enforces "no credential sent" whatever the SDK does internally, and still lets an embedder that fronts Ollama with an auth proxy add a header deliberately.
7. **`Options.Host` overrides `OLLAMA_HOST`.** It gives tests and `provider_selection` an explicit endpoint, like `anthropic.Options.BaseURL`.
8. **`num_ctx` is sent on every request. A model with no window gets `catalog.OllamaDefaultContextWindow`, so `provider/ollama` imports `catalog`.** The window the agent prunes against must be the one the model loads at. Spec 26 says no Ollama limit is written in Go outside `catalog`.
9. **`num_predict` follows the Anthropic `max_tokens` rule (the requested value when positive and within the cap, else the cap).** It is the same contract for `Request.MaxTokens` on every provider.
10. **The tool schema is cut down by encoding it and decoding it into the SDK's parameter type.** "The part of the schema its API carries" is then exactly what the pinned SDK models, and that grows with the SDK without code here.
11. **Tool choice `none` sends no tools.** Ollama has no tool-choice field (input §5).
12. **The transcript repair is `openrouter_provider`'s provider-neutral one. Thinking is replayed only on unredacted blocks from turns of the same model.** One repair serves both new providers. Thinking from another vendor or model is meaningless or opaque to Ollama, and turning it into text would re-send it as visible content (`wires.md`).
13. **A tool call with no id gets a synthesized id that is unique within and across responses.** The loop needs a unique id per call, and `wires.md` records that per-response scoping prevents collisions. Results are also sent with the tool's name, so a server that pairs by name or position still lines them up.
14. **`done_reason` `length` maps to `StopReasonLength` even when there are tool calls. Otherwise tool calls map to `StopReasonToolUse`, and otherwise the result is `StopReasonStop`.** The loop already refuses to run calls from a length-truncated turn. The probe shows `stop` accompanying tool calls.
15. **A truncated or aborted turn's message carries no tool call.** It matches `openrouter_provider`, so the later conformance suite asserts one behaviour, and a partial call can never be executed.
16. **No retries.** A local server's failures (not running, model not pulled, out of memory, over-long input) are not transient, and the input specifies retries for OpenRouter only.
17. **404 means model not pulled. A context-window error is recognised by its message saying the input exceeds the context length. A dial failure means unreachable.** These are the three failures the input asks to explain. Each keeps Ollama's own message, so an error that is misclassified still shows what Ollama said.
18. **Cost is set to 0 with its presence bit whenever usage is reported.** The input says Ollama cost is zero. A zero that is reported keeps the agent from re-pricing it and keeps the run from being flagged unpriced.
19. **`CacheReadTokens` is set only when positive, and reasoning tokens are never set.** The SDK's integer fields cannot tell an absent count from zero, and Ollama reports no reasoning count.
20. **The SDK module is pinned at `v0.40.7`.** That is the version probed with tool-call ids and `prompt_eval_cached_count`. An upgrade is a deliberate change.
21. **The package lives in the root module (input DD2). Version moves caused by minimum version selection are recorded as accepted upgrades, and tests are restated, not deleted.** This follows the precedent of spec 14's `d1Upgrades`.
22. **`ollama.API` is `core.APIOllamaChat`.** The constant already exists, and spec 26 gives the catalog vendor the same `ollama-chat` string.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `26_provider_catalog_tiers` | depends on | Supplies the `ollama` row, `catalog.OllamaDefaultContextWindow` / `OllamaDefaultMaxTokens` and `catalog.Select`, which the tests and the `num_ctx` fallback use. |
| `27_openrouter_provider` | depends on | Adds the provider-neutral tool-pairing repair in package `provider` that this provider reuses, and makes the first edits to the policy files (`approvedDeps`, `preSpec13Direct`, the snapshots, `TestProviderDefaults_TS09_11`) that this spec extends. |
| `09_repository_cut` | modifies | `TestProviderDefaults_TS09_11` now expects `provider/ollama` among the provider directories. The direct-dependency allowlist (TS-09-16) gains the Ollama module. |
| `13_sandbox_core` | modifies | TS-13-65's root direct set and `go-list-m-all.txt` snapshot change. Its grpc and protobuf version pins are restated if minimum version selection moves them. |
| `14_sandbox_rsync_sync` | modifies | TS-14-42's sandbox graph snapshot is regenerated, and any version moves are recorded as accepted upgrades. |
| `15_sandbox_landlock_agent` | modifies | TS-15-40 requires `sandbox-go-list-m-all.txt` to equal `.14.txt`. Both are restated. |
| `17_sandbox_podman` | modifies | TS-17-60 compares the sandbox graph to the regenerated snapshot. |

## Verified External API

The module cache is outside the repository, so none of these signatures could be read. All are **unverified**. They come from the input's probe of `api` v0.40.7 and from the package's long-standing public shape. The implementer confirms each with `go doc github.com/ollama/ollama/api` before relying on it.

| Symbol | Assumed signature / shape | Status |
|---|---|---|
| `api.NewClient` | `func NewClient(base *url.URL, http *http.Client) *Client` | unverified |
| `(*api.Client).Chat` | `func (c *Client) Chat(ctx context.Context, req *ChatRequest, fn ChatResponseFunc) error`. Calls `fn` once per streamed line. Returns nil at end of body even without `done: true`. Returns `api.StatusError` for a status ≥ 400. Returns a plain error carrying the message for an `{"error": …}` line inside a 200. | unverified |
| `api.ChatResponseFunc` | `type ChatResponseFunc func(ChatResponse) error` | unverified |
| `api.ChatRequest` | `Model string; Messages []Message; Stream *bool; Tools Tools; Options map[string]any; Think *ThinkValue; Truncate *bool` (`Truncate` per the probe) | unverified |
| `api.ThinkValue` | `struct{ Value any }`, marshalled as a bool or a string level | unverified |
| `api.Message` | `Role, Content, Thinking string; ToolCalls []ToolCall; ToolName string; ToolCallID string` (`ToolCallID` per the probe) | unverified |
| `api.ToolCall` / `api.ToolCallFunction` | `ToolCall{ID string; Function ToolCallFunction}`; `ToolCallFunction{Index int; Name string; Arguments ToolCallFunctionArguments}` | unverified |
| `api.ToolCallFunctionArguments` | Keeps insertion order through `UnmarshalJSON` / `MarshalJSON` (an ordered map) | unverified; Design Decision 2 depends on it |
| `api.Tool`, `api.Tools`, `api.ToolFunction`, `api.ToolFunctionParameters` | `Tool{Type string; Function ToolFunction}`; `ToolFunction{Name, Description string; Parameters ToolFunctionParameters}`; parameters decodable from JSON Schema, keeping type, description, enum, items, properties and required | unverified |
| `api.ChatResponse` / `api.Metrics` | `ChatResponse{Model string; Message Message; Done bool; DoneReason string; Metrics}`; `Metrics{PromptEvalCount, PromptEvalCachedCount, EvalCount int; …}` (`PromptEvalCachedCount` per the probe) | unverified |
| `api.StatusError` | `struct{ StatusCode int; Status string; ErrorMessage string }` with `Error() string` | unverified |
| `api.ClientFromEnvironment`, `envconfig.Host` | Exist and read `OLLAMA_HOST` from the process environment | unverified; deliberately not used |
