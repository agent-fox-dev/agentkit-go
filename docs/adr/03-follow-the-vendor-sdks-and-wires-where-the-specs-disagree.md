# ADR 03: Follow the vendor SDKs and wires where the specs disagree

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken between 2026-07 and 2026-10)

## Context

The three providers (`provider/anthropic`, `provider/openrouter`,
`provider/ollama`) and the catalog that resolves a model for them were each
specified before the vendor's SDK could be read, and the specs marked their
SDK assumptions unverified. Reading and probing the SDKs showed signatures,
error types and encodings that differed from what the specs assumed, and
limits the vendors' APIs impose that no spec named. Each case was settled
once, in favour of the SDK as it is and of the vendor's wire, and recorded in
an erratum. The errata are gone; the settlements that still bind the code are
here.

## Decision

**The provider writes the request body; the SDK owns the transport.** Each
SDK re-encodes values a replay must keep byte for byte (Anthropic's
`tool_use` input loses its whitespace; OpenRouter's tool `parameters` is a
`map[string]any` and loses property order). So each provider encodes the
body itself and hands it to the SDK's client: Anthropic through
`option.WithRequestBody`, OpenRouter through the `HTTPClient` it gives the
SDK, which replaces the body serialized from a placeholder request. The SDK
keeps auth, retries and stream framing.

- `encoding/json` compacts a `json.RawMessage`, so the Anthropic body is
  produced by `encodeExact`, which marshals with placeholders and splices the
  raw bytes back; `BuildRequestJSON` returns it. An `OnPayload` hook that
  replaces the payload gets plain `json.Marshal`.
- Ollama's SDK re-renders arguments (a nested object's keys sorted, a number
  through `float64`), so a replayed call's arguments are set member by member
  as `json.RawMessage` (`eachMember`): top-level order, number literals and
  nested objects are kept; whitespace is compacted and `<`, `>`, `&` are
  escaped. Arguments the model sends are the SDK's rendering, with their
  top-level order.

**Anthropic.**

- The SDK is `anthropic-sdk-go` v1.x. Its Vertex helper panics without
  Application Default Credentials, so the client uses `vertex.WithCredentials`
  with a token source that looks the credentials up on the first request;
  `Options.VertexTokenSource` lets an embedder (or a test) supply them.
- The SDK retries, twice by default; `Options.MaxRetries` overrides it. An SSE
  event with no `event:` line is skipped by the SDK. An in-stream `error` event
  is reported as `anthropic: HTTP 200: <type>: <message>`, a `*StatusError`
  with code 200.
- `Resolve`: only `CLAUDE_CODE_USE_VERTEX` (or `Options.VertexProject`)
  selects Vertex; its location is `CLOUD_ML_REGION`, then
  `GOOGLE_CLOUD_LOCATION`, then `CLOUDSDK_COMPUTE_REGION`. Bedrock's region
  defaults to `us-east-1` without `AWS_REGION` or `AWS_DEFAULT_REGION`; its
  credentials are static keys or `AWS_BEARER_TOKEN_BEDROCK` from the `Env`,
  else the AWS SDK's default chain. `ANTHROPIC_OAUTH_TOKEN` is a direct
  credential, sent with the OAuth beta. No credential is
  `anthropic.ErrNoCredentials` before any request.
- The request carries an effort, never a budget: a budget model's
  `budget_tokens` is its row's budget for that effort, held below
  `max_tokens` and dropped below the vendor minimum of 1024. An empty effort
  omits thinking; an effort the row does not list is omitted, not clamped.
  `max_tokens` is capped at the row's output cap. `tool_choice: "none"` is
  sent.
- **Strict tools.** The API totals its strict-mode limits over a request (20
  strict tools, 16 union parameters) and answers 400 past either, and the
  strict rewrite widens every optional property into a union with null. So
  `strictTools` declares tools strict in the order they are sent while they
  fit; a tool that does not fit is sent `"strict": false` with the same
  rewritten schema, and later tools that fit are still strict. The order keeps
  the choice the same every turn, so the cached tool prefix does not change,
  and puts an embedder's tools first. A schema with no strict form is sent as
  written, non-strict. The API also refuses a strict grammar that compiles too
  large, a limit it publishes no figure for, so `Options.NonStrictTools` names
  tools never declared strict. A non-strict tool's arguments are still
  validated before it runs.

**OpenRouter.**

- The SDK (`OpenRouterTeam/go-sdk` v0.9.x) answers each error status with its
  own type carrying no status or headers, so the `HTTPClient` the provider
  hands it records each response's status, `Retry-After` and error body, and
  failures are classified from that record. A stream is requested with an
  `Accept: text/event-stream` override.
- The SDK reads `OPENROUTER_HTTP_REFERER` and `OPENROUTER_X_TITLE` unless
  set, so the provider sets both empty and looks up only its own two
  variables. Without `WithClient` the SDK times requests out at 60 seconds,
  which would cut a long stream, so the provider always passes its own client;
  with `Options.HTTPClient` nil that client has no timeout and only the
  caller's context ends a request.
- `reasoning_details` decode into the SDK's typed union; the provider keeps
  and replays the fields the SDK models (`type`, `text`, `summary`, `data`,
  `signature`, `format`, `index`, `id`). A field the SDK does not model is
  not replayed.
- The SDK types the prompt, completion and total token counts as plain
  integers, so they are marked reported only when not zero; cache, reasoning
  and cost counts are optional and marked exactly. A choice with
  `finish_reason: "error"` and no error object fails the turn with a
  `*StatusError` of code 0: `openrouter: error in stream: finish_reason
  "error"`.

**Ollama.**

- The host is parsed by `parseHost`, which follows the CLI's rules wherever
  the CLI does not guess, and refuses (wrapping `ErrInvalidHost`) where it
  does: a scheme other than exactly `http` or `https`, a port that is not
  digits from 1 to 65535, a query or fragment, a value `net.SplitHostPort`
  cannot split, user information (shown with its password as `xxxxx`), and
  `ollama.com` or any subdomain in any case. A blank `Options.Host` falls
  through to `OLLAMA_HOST`.
- No credential reaches the server: `credentialFree` deletes `Authorization`
  before the embedder's transport sees the request, so only a header that
  transport adds itself arrives. The SDK signs a request for `ollama.com`,
  which the host rules refuse, and whenever `OLLAMA_AUTH` is set in the process
  environment: it then reads `~/.ollama/id_ed25519` (failing before sending
  when the key is missing) and its `Authorization` header is deleted. Requests go
  through a copy of the embedder's `http.Client` with only its `Transport`
  replaced, so its `Timeout`, `CheckRedirect` and `Jar` are kept; a nil client
  has no overall timeout.
- A tool schema is cut to the SDK's `ToolFunctionParameters`: a property's
  `additionalProperties`, `format` and `minimum` are dropped, keywords inside
  `items` or `$defs` survive, a missing `properties` is an empty map and a
  missing type is `object`. With a tool choice of none no schema is decoded. A
  schema the type cannot read ends the turn before the request, naming the
  tool.
- A synthesized tool-call id is `ollama_<8 random hex digits>_<n>`, the digits
  per `Client`, so transcripts written by two clients do not collide; a vendor
  id that repeats within a response is replaced too.
- `prompt_eval_count` and `eval_count` cannot be told from zero, so a done
  chunk with neither and no cached count reports no usage; otherwise the input
  is the prompt count less the cached count, floored at 0, and the cost is 0
  with its bit set.
- Only `Request.Messages` is passed through `provider.RepairToolPairing`; the
  prefix is encoded as given. An assistant message with nothing to send is
  left out. A string effort is sent as `think`; an empty one sends nothing.
- A turn is decided in this order: a done chunk means it finished; otherwise a
  cancelled caller's context means aborted; otherwise `failureOf` classifies
  what the SDK returned. The SDK returns no error for an error status with an
  empty body, so the status the transport recorded is used: 404 is
  `ErrModelNotFound`, 401 and 403 are a `*StatusError` (a 401 arrives as
  `api.AuthorizationError`, with no message), any other is
  `ollama: HTTP <code>: <text>`, and only a 2xx with no done chunk is
  `ErrStreamTruncated`. A failed dial (DNS included) is `ErrUnreachable`; a
  reset while reading is a plain error that keeps the text so far. A message
  naming both "context length" and "exceed" is `ErrContextWindow`. The
  failure wraps every error it stands for, so `errors.Is` and `errors.As` find
  each. Every text starts `ollama:`. There is no retry.
- The SDK reads a line of at most 8,000,000 bytes and allocates that buffer
  per call.
- `ollama.New` returns `*ollama.Client`; the root assigns it to
  `core.ProviderClient` only after the error check.

**Shared repair.** `provider.RepairToolPairing` imports only `core` and
repairs tool pairing alone. It keeps a result only in the run of results that
directly follows the assistant message that made the call, and only the first
result for each call; any other is dropped and counted in `DroppedResults`.

**The catalog.**

- `Lookup` of an id the catalog does not list returns the default descriptor
  with zero cost, Vertex's dated ids among them.
- `Select`'s sentinels carry the first words of their message and are wrapped
  with `"%w %q; …"`, so the text is, for example, `agentkit: unknown provider
  "openai"; known providers are anthropic, openrouter and ollama`.
- `Parse` accepts a row with no `context_window` or `max_tokens` (it rejects
  only a negative one), and `Select` and `Lookup` return a listed row as the
  catalog has it, so a parsed catalog with a zero limit gives a zero limit.
  Every row of the embedded catalog has positive limits.

## Consequences

- An SDK upgrade is checked against these rules: they record what the SDK did
  at the pinned version, and a new version may make one unnecessary or wrong.
- A provider's errors are its own sentinels and `*StatusError`s, reachable
  through `Agent.Run` (see ADR 01).
