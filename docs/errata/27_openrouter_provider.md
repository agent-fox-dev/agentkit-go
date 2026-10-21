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

## The SDK reads two variables and times a request out on its own (27-REQ-1.1, 27-REQ-1.2)

Two behaviours of `sdk.New` (`openrouter.go:219`) cut across what the spec says
the provider does:

- Unless they are set, it reads `OPENROUTER_HTTP_REFERER` and
  `OPENROUTER_X_TITLE` from the process environment
  (`internal/config/sdkconfiguration.go:42`). A chat completion never sends
  them (`chat.go` passes no globals to `PopulateHeaders`), but 27-REQ-1.2 says
  the provider looks up no variable other than the two it names. The provider
  sets both to the empty string (`provider/openrouter/client.go:87`), so the SDK
  reads neither.
- Without `WithClient` it builds a client with a 60-second `Timeout`, which
  counts the time spent reading the body and would cut a long stream. The
  provider always passes its own client, and when `Options.HTTPClient` is nil
  that client has no timeout (`provider/openrouter/client.go:59`); only the
  caller's context ends a request.

## The SDK cannot tell an absent token count from a zero (27-REQ-8.1)

27-REQ-8.1 sets a presence bit for exactly the fields the response reported.
The SDK types `prompt_tokens`, `completion_tokens` and `total_tokens` as plain
integers (`models/components/chatusage.go:93`, `:103`, `:109`), so a usage
object that leaves one out decodes the same as one that reports 0. The provider
marks those three reported when they are not zero (`provider/openrouter/usage.go:44`,
`:52`, and the input when the prompt or a cache counter is reported). The cache
read, cache write and reasoning counts and the cost are optional in the SDK
and are marked exactly (`TestUsageIsNetted_TS27_44` checks a response that
reports only the prompt and completion counts). A usage object whose three
counts are all zero is therefore unreported and gets no cost unless the response
reported one.
