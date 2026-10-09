# Erratum: spec 10 `anthropic_wire`

Where the delivered Anthropic wire differs from `.specs/10_anthropic_wire`, and
why.

## External API: the SDK is v1.79.1, not v0.1.0

**Spec.** `external_apis` names `github.com/anthropics/anthropic-sdk-go`
`v0.1.0` with `param.Field`-shaped request types and
`vertex.WithGoogleAuth(ctx, project, region)`.

**Code.** `go.mod` pins `v1.79.1`, the current release. Its request types use
`param.Opt`, and its Vertex helper is `WithGoogleAuth(ctx, region, project)`.
That helper panics when no Application Default Credentials exist, so
`provider/anthropic/client.go` uses `vertex.WithCredentials` with a token
source that looks the credentials up on the first request instead.

## 10-REQ-1.3 and 10-REQ-6.1: AgentKit writes the request body

**Spec.** The SDK client is the transport. Replayed `tool_use` input keeps its
exact bytes.

**Code.** Both hold, but not by building the request from SDK params: the
SDK re-encodes a `tool_use` block's `input` when it marshals params (key order
kept, whitespace lost). The provider encodes the body itself and sends it
through the client with `option.WithRequestBody`
(`provider/anthropic/stream.go`, `run`). The SDK still owns the transport,
auth, retries and SSE framing, and the provider decodes the SDK's stream
events (`ev.RawJSON()`). TS-10-3 drives a request through an SDK client's
middleware.

## The allowlist also approves `golang.org/x/oauth2`

The Vertex deployment's credentials (`vertex.WithCredentials` takes a
`*google.Credentials`) need `golang.org/x/oauth2` as a direct import, so
`internal/policy/deps_test.go` approves it beside the SDK.

## Behaviour the SDK now owns

- **An event with no `event:` line.** The hand-rolled reader recovered the
  type from the JSON. The SDK skips such an event, so the test that pinned the
  recovery (`TestAnEventWithNoEventLineFallsBackToItsJSONType`) was removed.
  Every event from the API names its type in its JSON as well, and test
  fixtures now do too.
- **The `anthropic-version` header on Vertex.** The SDK sends it and also puts
  `anthropic_version` in the body; the test no longer asserts the header is
  absent. The body checks (no `model`, `anthropic_version` present) remain.
- **Retries.** The SDK retries (twice by default). `RequestOptions.MaxRetries`
  and `anthropic.Options.MaxRetries` override the count;
  `RequestOptions.MaxRetryDelayMs` is no longer read.
- **Attribution headers.** AgentKit no longer adds `x-agentkit-version`;
  `AgentConfig.Attribution` and `AGENTKIT_TELEMETRY` have no effect.

## `anthropic.Options.VertexTokenSource`

Not in the spec. A Vertex request needs a Google token, and without this
option the only sources were an environment token or Application Default
Credentials — so a test of the Vertex path would reach the real ADC on the
machine running it, and mint a real token over the network. The option lets
an embedder (or a test) supply credentials explicitly.

## 10-REQ-2: what `Resolve` reads beyond the spec

- **Only the flag selects Vertex.** The previous wire also selected Vertex
  from `ANTHROPIC_VERTEX_PROJECT_ID` alone (with no Anthropic credential) and
  from a Vertex host in a base URL. 10-REQ-2.3 makes `CLAUDE_CODE_USE_VERTEX`
  the switch, so those heuristics and their tests were removed.
  `anthropic.Options.VertexProject` still selects Vertex in code.
- **Region fallbacks.** After `CLOUD_ML_REGION`, the Vertex location is read
  from `GOOGLE_CLOUD_LOCATION` and `CLOUDSDK_COMPUTE_REGION`, as before.
- **Bedrock region.** TS-10-6 resolves Bedrock from the flag alone, so the
  region defaults to `us-east-1` when neither `AWS_REGION` nor
  `AWS_DEFAULT_REGION` is set (`provider/anthropic/resolve.go`,
  `DefaultBedrockRegion`).
- **Bedrock credentials.** Static keys or `AWS_BEARER_TOKEN_BEDROCK` come from
  the `Env`. Without them, the AWS SDK's default chain is used, which reads the
  process environment and shared files — AWS credential discovery is the AWS
  SDK's, not this package's. The allowlist approves the AWS SDK modules.
- **`ANTHROPIC_OAUTH_TOKEN`** is a direct credential too, with the OAuth beta.
- **No credential is an error before any request.** The previous wire sent an
  unauthenticated request; now the provider ends the turn with
  `anthropic.ErrNoCredentials`'s text (10-REQ-2.6).
- `provider/auth.go` (`VendorAuth`, `ResolveAuth`) and
  `provider/anthropic/vertex.go` are deleted; the examples' credential
  pre-flight calls `anthropic.Resolve(anthropic.OSEnv{})`.

## 10-REQ-3: the catalog's field names and what an unlisted id loses

- **Field names.** TS-10-13 reads `m.MaxOutput` and `m.Cost.InputUSDPer1M`;
  `core.Model` has had `MaxTokens` (the output cap) and `Cost.Input` (USD per
  million tokens) all along, and the catalog test asserts those.
- **`core.ThinkingKind`.** 10-REQ-3.4 names `ThinkingKindAdaptive`. It is
  defined in `core/provider.go` with `ThinkingKindNone` and
  `ThinkingKindBudget`, on `core.Model.Thinking`, and the catalog derives it
  from each row's level map (token counts mean budget, effort names mean
  adaptive). It arrived with the catalog task rather than the Effort task,
  because the catalog's tests need it.
- **Unlisted ids lose their price.** The previous catalog cloned the
  vendor's default row for an unknown id, price included. `Lookup` returns the
  10-REQ-3.4 default instead, with zero cost — Vertex's dated ids
  (`claude-sonnet-5@20260401`) among them.
- **No clamping, from the catalog task on.** Deleting `catalog/clamp.go`
  removed thinking-level clamping a task early: a level the row does not list
  is omitted (10-REQ-4.4). Two thinking tests that pinned a clamp now pin the
  omission. `max_tokens` is capped at the row's output cap in the encoder; the
  context-window term was already zero (spec 09).
- TS-10-15 passed before any change: the provider already carried no model
  id, price or token limit.

## 10-REQ-4: Effort replaces ThinkingLevel on the request path

- `core.Effort` replaces `ThinkingLevel` on `core.Request`, `AgentConfig`
  (`Effort`), `AssistantMessage` (provenance) and `ConfigView`;
  `Agent.SetThinkingLevel` is `Agent.SetEffort`. The `ThinkingLevel` type
  remains as the catalog level map's key, for spec 12 to retire.
- **No budget on the request.** TS-10-18 sends `ThinkingBudget: 4096`. The
  request carries only an effort; a budget model's `budget_tokens` is its
  catalog row's budget for that effort (`claude-sonnet-4-5`: low is 4096), as
  PRD 10 §2 describes ("the catalog row says which models still take a
  budget"). The budget is still held below `max_tokens` and dropped below the
  vendor minimum of 1024, both wire rules.
- **TS-10-17's model.** The pseudocode uses `claude-sonnet-4-5`, which the
  catalog lists as a budget model; the test uses the adaptive
  `claude-sonnet-5-5`.
- **"Off" is gone.** There is no effort for no thinking; an empty effort
  omits the parameter. The rows' `off` wire values (`disabled`,
  `between_tools`) are no longer sent, and the tests of them were removed.
- TS-10-16 (the constants) and TS-10-19 (omission on a model without
  thinking) passed before the encoder changed: the first checks declarations,
  and the second was already the behaviour.

## 10-REQ-5: tool projection and tool choice

- **The internal fields never could reach the wire.** `core.Request.Tools` is
  `[]core.ToolWire`, which has no `OutputSchema`, `ReachableTools` or
  `Terminating`, so TS-10-21 passed before any change; `strict: true` was the
  only new field (TS-10-20).
- **`tool_choice: "none"` is kept.** `core` has no forced choice to omit
  (`ToolChoiceAuto`, `ToolChoiceNone`, unset), so TS-10-22 also passed at
  once. `none` forbids tools rather than forcing one, and is still sent.
- **The golden moved a task early.** `testdata/golden/request_anthropic.json`
  gained `"strict": true` on its tool here, because the golden test would
  otherwise fail; the golden task rewrites the fixture.

## 10-REQ-6: exact bytes need an encoder of their own, and Request.Prefix

- **`encoding/json` compacts raw bytes.** The encoder already put the
  model's bytes into `input`, but `json.Marshal` compacts the output of any
  `json.RawMessage`, so whitespace was lost on every replay. The body is now
  produced by `encodeExact` (`provider/anthropic/anthropic.go`), which
  marshals with placeholders and splices the raw bytes back, for `tool_use`
  input and for blocks replayed verbatim alike. `BuildRequestJSON(req, model)`
  returns it; `json.Marshal` of a `BuildRequest` result still compacts. An
  `OnPayload` hook that replaces the payload gets plain `json.Marshal`.
- **`core.Request.Prefix`** is new: messages sent after the system prompt and
  before the history, with a breakpoint on their last block. The loop does not
  set it yet; spec 11's `Config.Prefix` feeds it. When the prefix ends and the
  history starts with the same role, the history's first message joins the
  prefix's last.
- TS-10-24 passed before any change: the last system block and the last tool
  already carried the breakpoint.

## 10-REQ-7: translation helpers and the request count

- **`core.Usage.Requests`** is new: one per response, summed by `Usage.Add`.
  The spec's `CacheCreationTokens` is the existing `CacheWriteTokens`.
- **`TranslateMessage(m, msg)`** takes the model as well as the SDK message,
  because the usage is priced at the model's rates. It and
  `TranslateUsage(start, delta)` decode the SDK values' own JSON through the
  decoder the stream uses, so a `tool_use` input keeps its bytes.
- TS-10-28 passed before any change: `core.EventStream` never blocks its
  producer, and the provider already pushed through it.
