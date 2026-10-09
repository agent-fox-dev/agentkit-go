# Findings from the deleted provider wires

Until [PRD 10](../prd/10-cut-agentkit-down-to-the-hands.md), AgentKit had five
hand-written wire APIs: Anthropic Messages, OpenAI Chat Completions
(`openai-completions`), OpenAI Responses (`openai-responses`), Google Gemini
`generateContent` (`google-generative-ai`) and Ollama's native `/api/chat`
(`ollama-chat`), plus a cross-provider conformance suite. PRD 10 deleted all
but Anthropic, which now runs on the official SDK. This page keeps what those
wires taught, so it is not rediscovered by whoever writes the next
`core.ProviderClient`.

Sources: the archived [0.4.2 PRD](agent-kit-prd.md) (REQ-LOOP-01/02,
REQ-PROV-05/11/12/15/17), [PRD 09](../prd/09-replace-hand-rolled-code-with-libraries.md)
§4 and §6, PRD 10 §1 and §4, and the commit messages that added and fixed the
wires (`fc1c2f6`, `e86c201`, `bff409c`, `44ef07c`, `98142cf`, `c6b5194`,
`2bb1a5b`, `da5b772`, `0992169`, `a4a701e`, `f8c1dbc`).

## The loop

- **Iterate on tool calls, not on the stop reason.** Gemini returns a
  STOP-family reason alongside `functionCall` parts, and several
  OpenAI-compatible gateways send `finish_reason: "stop"` (or none) with a
  populated `tool_calls` array. A loop gated on the stop reason silently
  drops those calls, and passes every Anthropic-only test. The driver still
  iterates on the presence of `tool_use` blocks.
- **One canonical result per call; grouping is a wire concern.** One
  transcript with three parallel calls produced three different bodies:
  Anthropic, one `user` message with three `tool_result` blocks; OpenAI,
  three `role: "tool"` messages keyed by `tool_call_id`; Gemini, one user
  content with three `functionResponse` parts. With two wires one shape can
  still look canonical; the third settled it. Hence one `ToolResultMessage`
  per call in `core`.
- **Some wires pair results by position.** Ollama's native tool message and
  Gemini's classic `functionCall`/`functionResponse` carry no id. Results
  pair to the calls by order, so a reorder silently swaps two answers, a
  partial batch cannot be expressed, `is_error` has nowhere to go, and two
  calls to the same tool are ambiguous. The wires synthesized ids, scoped per
  response so two turns could not collide, and the streaming and
  whole-response paths had to synthesize the same one. Synthetic results for
  unanswered calls had to be emitted in `tool_use` order for these wires.

## Usage and cost

- OpenAI and Google report the prompt total **including** cached tokens and
  must subtract them; Anthropic reports it excluding them and must not. Both
  mistakes are silent and overstate cost by up to about 90% on a well-cached
  loop, which then trips a budget early.
- The cached count lived in three places: `prompt_tokens_details.cached_tokens`
  (OpenAI, OpenRouter), `prompt_cache_hit_tokens` (DeepSeek) and a top-level
  `cached_tokens` (Moonshot).
- Gemini reports `thoughtsTokenCount` beside `candidatesTokenCount`; output is
  their sum.
- Chat Completions reports no usage on a stream without
  `stream_options.include_usage`.
- OpenAI Responses applies a service-tier multiplier to the computed cost
  (flex 0.5x, priority 2x). `provider.ApplyServiceTier` still carries it.

## OpenAI Chat Completions

- "OpenAI-compatible" is not a base-URL swap. Each vendor needed named quirk
  flags (a compatibility profile), each tied to a request that 400s, hangs or
  answers nothing: `max_tokens` vs `max_completion_tokens`, `store`, the
  `developer` role, `reasoning_effort`, the reasoning-replay format
  (`reasoning_content` echoed back for DeepSeek), the reasoning-budget field
  name (vLLM, Qwen, llama.cpp), `strict` on tools, long cache retention,
  untrustworthy `finish_reason`, `null` vs `""` assistant content, a user
  message right after a tool result, `name` on tool results, and Anthropic
  `cache_control` over this wire for OpenRouter `anthropic/*`.
- A flag resolved by the profile but read by nothing is the shape that rots
  silently; three were found that way.
- Arguments travel as a JSON **string**. Re-encoding them sorts the keys,
  changes the text the model is conditioned on, and shifts the prompt-cache
  prefix for the rest of the session. Replay the bytes as received.
- `prompt_cache_key` is clamped to 64 runes; the API rejects longer.
- An error result was marked with an `Error: ` prefix, as on Ollama, since
  the wire has no error flag.

## OpenAI Responses

- A separate implementation, not Chat Completions with a flag: it differs in
  the message model (items), tool-call identity, reasoning replay, caching
  and billing.
- One assistant turn is several items (reasoning, text, one per call). The
  system prompt is a top-level `instructions` string, and a tool is flat
  rather than nested under `function`; reusing the Chat Completions shape is
  a 400 that does not say so.
- Tool-call identity is composite, `callId|itemId`: `call_id` is what a
  `function_call_output` references, the item id is what reasoning replay
  lines up against.
- With `store: false`, reasoning must be replayed statelessly: request
  `include: reasoning.encrypted_content` whenever the model supports it, or
  the second turn fails with "item not found". Never replay an item without
  its blob; `summary: []` must be present.
- `function_call_output` must always carry `output`, even when empty.
- There is no stop-sequence parameter. The test was restated as "a stop
  sequence either takes effect or is reported", never silently inert.
- `additional_tools` does not exist; deferred tools were re-declared in
  prose.

## Google Gemini

- A function call closes the open text run, or the text before and after it
  stitches into one block in the wrong order.
- `functionResponse.response` is not opaque: a `{"$ref": name}` object inside
  it is Gemini's own reference syntax, and an unmatched name is a 400 for the
  whole request. A tool result holding a JSON Schema read from disk hit this.
  Objects with a `$`-prefixed key at any depth were wrapped under `output`.
- Thought signatures ride on text parts, on signature-only parts, and on the
  data part of a generated picture; dropping any of them breaks the chain on
  replay.
- Thinking takes a level token (`thinkingConfig.thinkingLevel`) or a budget
  (`thinkingBudget`), never both. Some families cannot stop thinking and take
  their lowest level for "off", never the `-1` dynamic budget.
- Explicit `CachedContent` is a network call: create it in the background,
  never block the loop on it, expire it locally, and on a 400 or 404 for a
  request that carried it, clear it and retry once uncached.
- Vertex AI is the same wire behind another host and credential, selected by
  configuration, not by a catalog row.

## Ollama native

- Failure arrives as a top-level error string inside a 200, which a transport
  layer cannot see.
- The stream is NDJSON; it was read through `wire`'s bounded frame reader.
- `OLLAMA_HOST` without a scheme needs `http://`.
- Its OpenAI-compatible `/v1` endpoint does not fully implement streaming
  tool calls or cache-token usage, which is why the native API had its own
  wire.

## Across all wires

- A stream that ends without its terminal signal (`message_stop`,
  `finish_reason`, `finishReason`, `done: true`) is truncated, not complete.
  Otherwise a partial tool call, salvaged into valid JSON, gets executed.
- Salvage of a cut-off argument object drops the incomplete member rather
  than closing the string, so `{"path":"/etc/pas` never becomes a valid call.
  `provider/anthropic` still does this (`SalvageJSON`).
- A tool with no schema is declared as an empty object, not `null`.
- Native reasoning without a signature (Gemini thoughts, Ollama thinking,
  OpenRouter reasoning deltas) needed a provider marker; without it the repair
  pass demoted it to text and re-sent it as visible content every turn.
- Replaying another vendor's signed thinking or tool-call id format is a 400;
  the repair pass downgraded or rewrote them when the target model changed.
  With one wire, PRD 10 reduced repair to the one rule a single-process run
  needs.
- The vendor SDKs were rejected in PRD 09 §4 because, across five wires, they
  saved only 15 to 25% and broke duplicate-key rejection, long `Retry-After`
  handling and the `OnPayload` seam. PRD 10 §4 reversed this for the single
  remaining wire: those costs came from having five.

## Conformance-suite lessons

- **One fixture, every dialect.** The suite ran the same logical turn through
  every provider for the properties stated once about "a provider":
  byte-faithful arguments, usage netting, event ordering, partial content
  plus failure, status text, `OnPayload` and credentials.
- **Select cases by name, not index.** Adding the Responses wire shifted an
  index and silently pointed an Ollama test at another provider; it kept
  running and reported a failure that had nothing to do with Ollama.
- **Prove a test can fail.** Mutations were introduced and confirmed red.
  Some did not discriminate (a change applied to both paths identically; an
  empty placeholder that made a seeding bug append nothing) and the tests
  were rewritten until they did.
- **Fixtures use the code's constants.** A golden fixture that typed an API
  string by hand had drifted from the constant and only worked because the
  adapter never read it.
- **A golden pins regression, not vendor truth.** Request goldens produced by
  AgentKit say nothing about whether the vendor still accepts the body; only
  a capture of the real API does.
