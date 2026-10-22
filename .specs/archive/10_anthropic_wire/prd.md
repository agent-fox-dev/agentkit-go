---
spec_id: "10"
spec_name: "anthropic_wire"
title: "Rewrite Anthropic Wire over Official SDK with Vertex and Bedrock Resolution, Catalog Lookup, and Golden Tests"
status: "active"
created_at: "2026-10-09T11:26:14.24786Z"
updated_at: "2026-10-09T11:26:14.24786Z"
intent_hash: "b62b2934e23a57241cd81f261808d004f884bc404ea953f718c95a7e1eb136ea"
schema_version: 2
source: "docs/prd/10-cut-agentkit-down-to-the-hands.md"
---
## Intent

Rewrite the Anthropic provider wire on the official `github.com/anthropics/anthropic-sdk-go` SDK with unified Direct, Vertex AI, and AWS Bedrock environment resolution, replace multi-vendor catalog complexity with a streamlined Claude model lookup table, establish adaptive thinking effort controls and strict tool schemas, and verify wire serialization stability through an updated golden request test.

## Goals

1. Rewrite `provider/anthropic` over `github.com/anthropics/anthropic-sdk-go`, replacing hand-rolled HTTP transport, custom SSE parsing, and ad-hoc credential refreshing with official SDK client primitives.
2. Implement `anthropic.Resolve(env Env) (*anthropic.Client, Source, error)` supporting three deployment modes:
   - Direct Anthropic API (`ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`) with optional `ANTHROPIC_BASE_URL`.
   - Google Cloud Vertex AI when `CLAUDE_CODE_USE_VERTEX` is truthy, configuring project, region, and credentials.
   - AWS Bedrock when `CLAUDE_CODE_USE_BEDROCK` is truthy.
3. Verify `anthropic.Resolve` offline via unit tests that inspect client base URLs and auth configurations for Direct, Vertex, and Bedrock without external network requests.
4. Shrink `catalog` to an internal table of Claude models, deleting obsolete OpenAI and Google catalog rows, `catalog/clamp.go`, and `catalog/resolve.go`.
5. Implement `catalog.Lookup(id string) (core.Model, bool)` which returns complete metadata for known Claude models and safe fallback defaults (default context window, default max tokens, adaptive thinking, and zero cost) for uncataloged models without rejecting them.
6. Replace `core.ThinkingLevel` clamping with `Effort` (`low`, `medium`, `high`, `xhigh`, `max`), encoding adaptive thinking as `thinking: {"type": "adaptive"}` and `output_config: {"effort": "<level>"}` without sending `budget_tokens` to modern models.
7. Encode all tool declarations with `strict: true` on the wire while stripping internal SDK metadata (`OutputSchema`, `ReachableTools`, `Terminating`).
8. Enforce byte-identical replay of assistant `tool_use` argument JSON, preserving decoded raw bytes across multi-turn requests.
9. Support prompt caching breakpoints (`cache_control: {"type": "ephemeral"}`) on the last system block, the last tool declaration, the last prefix block, and the last user message block.
10. Forbid forced tool choice (`tool_choice: "any"` or named tool locks) on modern Claude models, defaulting to `auto` or omitting `tool_choice`.
11. Update `testdata/golden/request_anthropic.json` and `golden_requests_test.go` to assert the exact wire serialization of cache controls, strict tools, adaptive thinking effort, and argument byte preservation.
12. Update `go.mod` and the dependency allowlist test in `internal/policy/deps_test.go` to approve `github.com/anthropics/anthropic-sdk-go`.

## Non-goals

- Repository cleanup, deleting dead providers (`openai`, `google`, `ollama`), renaming the root module to `github.com/agent-fox-dev/agentkit-go`, and removing wall-clock performance tests (covered in `09_repository_cut`).
- Refactoring `agentkit.Config`, `agentkit.New`, the batch execution loop, transcript pruning (`Prune`), and turn limits (covered in `minimal_driver`).
- Shrinking the `core` package type vocabulary, folding `jsonx` into `schema.Parse`, and pure-function `guard.Check` (covered in `core_schema_guard`).
- Supporting non-Claude provider wires or building custom provider registries.
- Retaining 0.4.x wire compatibility flags, manual retry ladders, or deprecated audit hooks.

## Background

AgentKit originally implemented its own HTTP transport, SSE stream decoder, retry loop, and credential stores for the Anthropic Messages API in `provider/anthropic/` and `provider/`. This hand-rolled wire stack comprised over 8,000 lines of code across `provider/` and `provider/anthropic/`. Maintenance of streaming edge cases, auth refresh, and cloud vendor variations (Vertex AI, AWS Bedrock) placed undue burden on the repository.

Simultaneously, the model `catalog` supported multiple vendors (Anthropic, OpenAI, Google) with complex vendor-prefix routing (`catalog/resolve.go`), sibling cloning, and thinking level clamping (`catalog/clamp.go`). Because AgentKit's sole consumer (`agent-fox`) operates exclusively on Claude models, this cross-provider machinery is dead weight.

Furthermore, current Claude models (Opus 5.5, Sonnet 5.5, Fable 5.1) introduce changes to wire semantics:
- Specifying `budget_tokens` alongside adaptive thinking causes a 400 Bad Request; models expect `thinking: {"type": "adaptive"}` with `output_config: {"effort": "<level>"}`.
- Modern Claude models reject forced tool choice (`tool_choice: "any"` or a forced tool name) with a 400 Bad Request.
- Claude supports `strict: true` on tool definitions to guarantee structured JSON adherence.

This spec migrates `provider/anthropic` to the official `github.com/anthropics/anthropic-sdk-go`, adds unified credential resolution for Direct, Vertex, and Bedrock, shrinks `catalog` to Claude lookup, introduces `Effort`, and pins request wire bytes with golden tests.

## Requirements

### Dependency and Allowlist Integration

1. `go.mod` shall require `github.com/anthropics/anthropic-sdk-go` pinned to a specific minor version.
2. The dependency allowlist test in `internal/policy` shall be updated to include `github.com/anthropics/anthropic-sdk-go` as an authorized direct dependency.
3. Hand-rolled HTTP transports, SSE parsers (`provider/sse.go`), credential stores (`provider/credentials.go`), and manual retry logic in `provider/anthropic` shall be replaced by the official SDK client.

### Environment Resolution (`anthropic.Resolve`)

4. The `anthropic` package shall provide `Resolve(env Env) (*anthropic.Client, Source, error)` where `Env` allows injecting variable lookups and override maps without mutating the process environment.
5. `Source` shall be a typed string representing the resolved backend: `SourceDirect`, `SourceVertex`, or `SourceBedrock`.
6. When `env` evaluates `CLAUDE_CODE_USE_VERTEX` as truthy (`"1"`, `"true"`, and not `"0"` or `"false"`):
   - `Resolve` shall construct an `*anthropic.Client` configured for Vertex AI using `ANTHROPIC_VERTEX_PROJECT_ID` (or `GOOGLE_CLOUD_PROJECT`), `CLOUD_ML_REGION` (defaulting to `"global"`), and custom proxy URL `ANTHROPIC_VERTEX_BASE_URL` if set.
   - `Resolve` shall return `SourceVertex`.
7. When `env` evaluates `CLAUDE_CODE_USE_BEDROCK` as truthy (`"1"`, `"true"`, and not `"0"` or `"false"`):
   - `Resolve` shall construct an `*anthropic.Client` configured for AWS Bedrock.
   - `Resolve` shall return `SourceBedrock`.
8. When neither Vertex nor Bedrock is selected:
   - `Resolve` shall read `ANTHROPIC_API_KEY` (or `ANTHROPIC_AUTH_TOKEN`) and optional `ANTHROPIC_BASE_URL`.
   - If an API key or auth token is present, `Resolve` shall construct a direct Anthropic client and return `SourceDirect`.
   - If no valid credential is found, `Resolve` shall return an error explaining that credentials are missing.
9. An explicit off flag (`CLAUDE_CODE_USE_VERTEX=0` or `CLAUDE_CODE_USE_BEDROCK=0`) shall explicitly disable that deployment even if secondary coordinates (such as project IDs) exist.
10. Unit tests shall verify `Resolve` offline for Direct, Vertex, and Bedrock deployments by inspecting the resulting client's configuration and base URL without issuing network requests.

### Model Catalog Shrink and Lookup

11. `catalog.json` shall remove all non-Claude vendor sections (OpenAI, Google) and retain only Claude model descriptors.
12. Files `catalog/resolve.go` and `catalog/clamp.go` shall be removed from the `catalog` package.
13. The `catalog` package shall expose `Lookup(id string) (core.Model, bool)`.
14. When `Lookup` is called with a known Claude model ID (with or without an `"anthropic/"` prefix), it shall return the corresponding `core.Model` descriptor and `true`.
15. When `Lookup` is called with an uncataloged model ID, it shall return a default `core.Model` descriptor (with `ID` set to the requested ID, default context window of 1,000,000 tokens, default output cap of 128,000 tokens, `ThinkingKindAdaptive`, and zero cost) and `false`. The uncataloged model shall not be rejected with an error.
16. All model pricing, token limits, and thinking capabilities shall reside solely in `catalog`; no hardcoded model identifiers or prices shall exist in `provider/anthropic` or driver packages.

### Adaptive Thinking and Effort Controls

17. The codebase shall define an `Effort` type with discrete levels: `EffortLow` (`"low"`), `EffortMedium` (`"medium"`), `EffortHigh` (`"high"`), `EffortXHigh` (`"xhigh"`), and `EffortMax` (`"max"`).
18. For models whose catalog row indicates adaptive thinking, the request encoder shall emit:
    - `"thinking": {"type": "adaptive"}`
    - `"output_config": {"effort": "<level>"}` (when an effort level is specified)
    - The encoder shall omit `budget_tokens`.
19. If a model's catalog row explicitly indicates legacy budget-based thinking, the encoder shall emit `budget_tokens` and omit `output_config.effort`.
20. Clamping logic that silently mutates thinking levels or converts thinking requests to off shall be omitted; if effort is unsupported on a model, the thinking parameter shall be omitted.

### Strict Tools and Schema Projection

21. When emitting tool definitions on the Anthropic wire, every tool shall include `"strict": true`.
22. Internal tool fields—specifically `OutputSchema`, `ReachableTools`, and `Terminating`—shall never be serialized into the wire tool definition.
23. Forced tool choice (`tool_choice: "any"` or locking to a specific tool name) shall not be emitted; the wire tool choice shall be set to `"auto"` or omitted entirely.

### History Replay and Prompt Cache Breakpoints

24. Replayed assistant `tool_use` argument inputs shall preserve the exact bytes received from the model (`json.RawMessage`) without re-serializing or reformatting JSON keys.
25. The request encoder shall apply ephemeral cache control headers (`"cache_control": {"type": "ephemeral"}`) at the following positions:
    - The final content block of the system prompt.
    - The final tool definition in the tools list.
    - The final content block of the prefix messages (if prefix messages are provided).
    - The final content block of the user message history.

### Response and Stream Translation

26. Responses and streaming events from the Anthropic SDK shall be translated into `core.AssistantMessage`, `core.ContentBlock` (`TextBlock`, `ThinkingBlock`, `ToolUseBlock`), and `core.StreamEvent`.
27. Streaming translations shall record token consumption in `core.Usage`, including input tokens, output tokens, cache read tokens, cache creation tokens, and increment request counts.
28. Stream event forwarding shall run without blocking the provider loop on slow consumers.

### Golden Request Assertions

29. `golden_requests_test.go` and `testdata/golden/request_anthropic.json` shall be rewritten to assert the complete wire request produced by the SDK encoder against a canonical request fixture.
30. The golden test shall verify that:
    - `cache_control` appears exactly on the last system block, last tool, last prefix block, and last user block.
    - Every tool carries `"strict": true`.
    - Modern models emit `thinking: {"type": "adaptive"}` and `output_config.effort` with no `budget_tokens`.
    - Replayed `tool_use` arguments match the input fixture byte-for-byte.
    - No `outputSchema`, `reachable_tools`, or `terminating` fields appear anywhere in the payload.

## Design Decisions

1. **Use official `github.com/anthropics/anthropic-sdk-go` over hand-rolled transport:** Hand-rolled HTTP transports and SSE decoders duplicate logic that the official SDK actively maintains, reversing PRD 09's single-wire assessment to reduce internal maintenance surface.
2. **Support Vertex AI and Bedrock in `anthropic.Resolve` via SDK options:** Rather than separate provider packages, deployment selection is unified in `anthropic.Resolve` using official SDK option helpers (`vertex.WithGoogleAuth`, custom base URLs, and auth sources), keeping deployment configuration centralized.
3. **Allow uncataloged models in `catalog.Lookup` with `ok == false`:** Returning a usable default `core.Model` with zero cost allows newly released Claude models to function immediately without requiring an emergency SDK release.
4. **Delete `clamp.go` and `resolve.go` completely:** Model resolution across multi-vendor namespaces and upward/downward thinking clamping are unnecessary for a Claude-only SDK and add maintenance overhead.
5. **Emit `strict: true` unconditionally on all tools:** Modern Claude models enforce schema validation when `strict: true` is set, eliminating schema mismatches at the model output boundary.
6. **Reject forced tool choice (`tool_choice: "any"`):** Current Claude models return a 400 Bad Request when presented with forced tool choices; constraining tool choice to `auto` or omitting it avoids wire-level errors.
7. **Preserve `tool_use` argument raw bytes end-to-end:** Re-serializing argument JSON mutates whitespace and key order, which can invalidate prefix cache lookups and alter golden request payloads.
8. **Position ephemeral cache breakpoints across four fixed boundaries:** Placing breakpoints on the last system block, last tool, last prefix block, and last user block maximizes Anthropic prompt cache hit rates across iterative agent turns.

## Dependencies

| Spec | Status | Reason |
|---|---|---|
| `09_repository_cut` | active | Removes non-Anthropic providers, unneeded tools, and examples, and sets up the module rename and dependency allowlist gate. |
| `06_tool_output_schemas` | active | Supplies `OutputSchema` on `core.Tool` which must be filtered out of wire tool definitions. |
| `07_nested_tool_calls` | active | Supplies `ReachableTools` and `Terminating` on `core.Tool` which must be filtered out of wire tool definitions. |

## Verified External API

The external dependency `github.com/anthropics/anthropic-sdk-go` is added in this scope.

### `github.com/anthropics/anthropic-sdk-go` (unverified)
```go
package anthropic

type Client struct {
    Messages *MessageService
}

func NewClient(opts ...option.RequestOption) *Client

type MessageService struct {}
func (s *MessageService) New(ctx context.Context, params MessageNewParams, opts ...option.RequestOption) (*Message, error)
func (s *MessageService) NewStreaming(ctx context.Context, params MessageNewParams, opts ...option.RequestOption) *ssestream.Stream[MessageStreamEvent]

type MessageNewParams struct {
    Model         param.Field[string]
    MaxTokens     param.Field[int64]
    Messages      param.Field[[]MessageParam]
    System        param.Field[[]TextBlockParam]
    Tools         param.Field[[]ToolParam]
    Thinking      ThinkingConfigParamUnion
    OutputConfig  param.Field[OutputConfigParam]
    ToolChoice    ToolChoiceUnionParam
    Temperature   param.Field[float64]
    TopP          param.Field[float64]
    StopSequences param.Field[[]string]
}
```

### `github.com/anthropics/anthropic-sdk-go/option` (unverified)
```go
package option

type RequestOption func(*RequestConfig) error

func WithAPIKey(key string) RequestOption
func WithBaseURL(base string) RequestOption
func WithHeader(key, val string) RequestOption
func WithMaxRetries(retries int) RequestOption
```

### `github.com/anthropics/anthropic-sdk-go/vertex` (unverified)
```go
package vertex

import "github.com/anthropics/anthropic-sdk-go/option"

func WithGoogleAuth(ctx context.Context, project, region string) option.RequestOption
```
