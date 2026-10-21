# Erratum: spec 27 `openrouter_provider`

The places where the delivered code and its tests differ from the text of
`.specs/27_openrouter_provider`, and why. PRD 14 and the spec are not edited. In
each case the test checks what the code delivers.

## `provider/repair.go` is back, and TS-09-1 no longer pins its absence (27-REQ-4.1 to 4.3, task 1)

Spec 27's task 1 creates `provider/repair.go` and `provider/repair_test.go` for
the shared tool-pairing repair. Spec 09's `TestDeletedPackagesAbsent_TS09_1`
asserted that both files do not exist: they were the multi-wire repair PRD 9
deleted. The spec does not name that pin, and `make check` failed on it
(`provider/repair.go still exists`) as soon as the repair landed.

The pin is restated, not bypassed: the two names left the absence list in
`repository_cut_test.go` (`TestDeletedPackagesAbsent_TS09_1`), with a comment
that points here, and every other entry stays. The new file is not the old one.
It imports only `core`, repairs tool pairing alone and never touches thinking
(`provider/repair.go`, `RepairToolPairing`), where the deleted repair handled
every vendor's thinking, signatures and ids.

## Rule 2 of the repair is applied by position (27-REQ-4.1, 27-REQ-4.3)

27-REQ-4.1 drops "every tool result whose call id is not in a kept assistant
message". 27-REQ-4.3 requires that, for any transcript, every call of a kept
assistant message is followed by exactly one result with its id before the next
non-result message. Read literally, the two disagree on a result that answers a
call of an earlier kept turn but sits after some other message, and on a second
result for one call: rule 2 keeps both, and the output then has a tool message
the wire cannot pair.

`RepairToolPairing` keeps a result only in the run of results that directly
follows the assistant message that made the call, and only the first result for
each call. A result anywhere else, or a second one, is dropped and counted in
`DroppedResults` (`provider/repair.go:92` for a result that follows no kept
assistant message, `provider/repair.go:113` and `:124` inside a run). For a
transcript the loop builds, where results directly follow their turn, this is
the same as the literal rule. `TestRepairToolPairingProperty_TS27_19`
(`provider/repair_test.go:218`) generates repeated, shuffled, missing and made-up
results and checks the 27-REQ-4.3 property and the fixed point over 500
transcripts.

## The OpenRouter SDK at v0.9.66 differs from the spec's assumptions (Verified External API)

The spec marks every SDK symbol unverified. They were read at `v0.9.66` (module
files below are relative to `github.com/OpenRouterTeam/go-sdk@v0.9.66`) and
probed against a recording server. `New`, `WithSecurity`, `WithServerURL`,
`WithClient` (an `HTTPClient` with `Do(*http.Request) (*http.Response, error)`,
`openrouter.go:31`) and `WithRetryConfig(retry.Config{Strategy: "none"})`
(`openrouter.go:206`; `retry/config.go` says `"none"` or `""` disables retries)
match what the spec assumed, and `WithSecurity` sends `Authorization: Bearer`
(`models/components/security.go:6`). The rest differs:

| What the spec assumed | What the SDK has | What the provider does |
|---|---|---|
| `(*Chat).Send(ctx, ChatRequest, opts...) (*ChatResponse, error)` | `Send(ctx, components.ChatRequest, *components.MetadataLevel, ...operations.Option) (*operations.SendChatCompletionRequestResponse, error)` (`chat.go:37`); the response is a union whose `EventStream` is a `*stream.EventStream[components.ChatStreamingResponse]` with `Next`, `Value`, `Err` and `Close` | calls it with a nil metadata level and reads `EventStream` |
| a typed HTTP error exposing the status, `Retry-After` and the body | one error type per status (`sdkerrors.TooManyRequestsResponseError` and the like) with the vendor's JSON and no status or headers; `*sdkerrors.APIError` (`models/sdkerrors/apierror.go`) only for a response with an unexpected content type | the `HTTPClient` handed to the SDK records each response's status, `Retry-After` and error body, and the retry loop classifies from that |
| request types that carry the tool schemas and `reasoning_details` without loss | a tool's `parameters` is a `map[string]any` (`models/components/chatfunctiontool.go:21`), which loses property order; `reasoning_details` is a typed union (`chatassistantmessage.go:141`) | the provider writes the request body itself; the `HTTPClient` it hands the SDK replaces the body the SDK serializes from a placeholder `ChatRequest{Model, Stream}` |
| an event stream that carries every `reasoning_details` field | the stream's chunks decode into the same typed union, which keeps the documented fields (`type`, `text`, `summary`, `data`, `signature`, `format`, `index`, `id`) and drops any other | the provider keeps the fields the SDK models; a field the SDK does not model is not replayed |
| an `Accept` header for a stream | `Accept: application/json;q=1, text/event-stream;q=0` unless overridden (`chat.go:99`) | passes `operations.WithAcceptHeaderOverride(operations.AcceptHeaderEnumTextEventStream)` |

The SDK decodes a chunk without `created` or `object`, and a chunk with only an
`error` object, so the canned streams of the offline tests need neither.
