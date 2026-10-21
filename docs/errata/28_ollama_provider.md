# Erratum: spec 28 `ollama_provider`

The places where the delivered code and its tests differ from the text of
`.specs/28_ollama_provider`, and why. PRD 14 and the spec are not edited. In
each case the test checks what the code delivers. Sections are added by the
task that delivers the behaviour they describe.

## The Ollama package differs from the spec's External APIs table (all unverified in the spec)

The table in the spec is from the PRD's probe of `api` v0.40.7. The module was
read at that version and probed against an `httptest` server before any code
used it. Where the real package differs, the provider follows the package. All
line references are to `github.com/ollama/ollama@v0.40.7`.

- **An error status with an empty body is not an error to `Chat`.** The spec
  says `Chat` returns `api.StatusError` for a status of 400 or more. `stream`
  (`api/client.go:170`) checks the status only inside its loop over the lines of
  the body (`:239`, `:245`), so a 404, a 500 or a 401 with an empty body
  returns `nil` after no chunk at all.
- **A 401 is `api.AuthorizationError`.** Not `api.StatusError`
  (`api/client.go:239`, `api/types.go:43`); it carries no message.
- **An error line is a plain error.** A line `{"error": …}` inside a 200
  becomes `errors.New(message)` (`:253`), and a line that is not JSON becomes
  `errors.New(line)` (`:236`). Neither has a type or a code.
- **A failure to connect is a `*net.OpError`** with `Op == "dial"`, a DNS failure
  included (it wraps a `*net.DNSError`). A connection reset while the body is
  read is also a `*net.OpError`, with `Op == "read"`.
- **The signing path.** `stream` calls `envconfig.UseAuth()` (`:184`), which
  reads `OLLAMA_AUTH` from the process environment (`envconfig/config.go:232`),
  and also signs when the host name is exactly `ollama.com`. With either it reads
  `~/.ollama/id_ed25519` (`auth/auth.go:53`), adds an `Authorization` header and
  a `ts` query parameter, and returns an error before sending if the key is
  missing. Probed with `OLLAMA_AUTH=1`: no key, the request fails with `open
  …/.ollama/id_ed25519: no such file or directory`; with a key, the server saw
  `POST /api/chat?ts=<unix>` with an `Authorization` header.
- **The line limit is 8,000,000 bytes.** `maxBufferSize = 8 * format.MegaByte`
  (`:168`), a megabyte being 1,000,000 bytes, and `Chat` allocates a buffer of
  that size for every call (`:219`). The spec and the PRD give about 512 KB.
- **Tool parameters.** `ToolFunctionParameters.Properties` has no `omitempty`
  (`api/types.go:483`): a schema without `properties` marshals as
  `"properties":null`. `Items` and `$defs` are `any`, so `minimum` or `format`
  inside an `items` schema survives the cut (probed: `"items":{"minimum":3,…}`);
  the keywords the type models for a property (`additionalProperties`,
  `format`, `minimum`) are dropped.
- **Arguments.** `ToolCallFunctionArguments` keeps the top-level key order
  (`api/types.go:238`, an ordered map), but a nested object comes back with its keys sorted,
  a number passes through `float64` (`12345678901234567890` is written back as
  `12345678901234567000`), and `<`, `>` and `&` are written as `<`, `>`
  and `&`. The spec accepts the number re-rendering (Design Decision 2).
- **Usage.** `Metrics.PromptEvalCachedCount` is a `*int` (`api/types.go:566`),
  so an absent count can be told from zero; `PromptEvalCount` and `EvalCount`
  are plain integers and cannot be. `ThinkValue` is an alias of
  `types/model.ThinkValue`.
- **`envconfig.Host` is more lenient than the spec's refusals.** Run on each
  value (`envconfig/config.go:22`): a port that is not a number from 0 to 65535
  is replaced with the default and only logged (`h:99999` is `http://h:11434`,
  `h:abc` too), port 0 is kept, any scheme is kept (`ftp://h` is
  `ftp://h:11434`, `HTTP://h` is `HTTP://h:11434`), `http://[::1` is
  `http://[::1]:80`, `http://` is `http://127.0.0.1:80`, and `user:pw@h` is the
  host `user` at port 11434.

## Policy tests restated for the new module (28-REQ-9, task 1)

- **`provider/ollama` leaves TS-09-1's absence list.** Spec 09's
  `TestDeletedPackagesAbsent_TS09_1` pinned `provider/ollama` as deleted, and
  `make check` failed on it (`provider/ollama still exists`) the moment the
  package existed. The spec does not name that pin. The entry left the list in
  `repository_cut_test.go` with a comment that points here, and every other
  entry stays. The package is not the NDJSON one PRD 09 deleted: it imports
  `github.com/ollama/ollama/api` and has none of its code.
- **`sandbox-go-list-m-all.14.txt` is not regenerated.** 28-REQ-9.2 lists it with
  the snapshots to regenerate, "as TS-15-40 requires" whole-file equality with
  the current sandbox snapshot. Spec 27 had already restated TS-15-40 as: every
  line 14 committed is still present, and the only lines added are named. The
  `.14` file is spec 14's baseline, and regenerating it would erase what the
  test compares against. It is kept as committed; `TestLandlockPromoted_TS15_40`
  (`internal/policy/landlock_dep_test.go:30`) reads its lines through
  `acceptedLine`, which applies `d1Upgrades` and then `upgradesBySpec28`, and
  accepts as added only the modules in `addedBySpec27` and `addedBySpec28`.
- **Two accepted-upgrade maps, not one.** The spec asks for one map next to
  `d1Upgrades`. A sandbox baseline line has to be moved forward and a root graph
  line cannot: the sandbox graph already holds `x/crypto v0.46.0` and the
  others from spec 14's D-1, so applying the root's `v0.43.0` would move a
  version backwards. `upgradesBySpec28` (`internal/policy/deps_test.go`) holds
  the one version the sandbox graph raised, `gopkg.in/check.v1`, and
  `TestRsyncDependency_TS14_42` and `TestLandlockPromoted_TS15_40` apply it.
  `rootUpgradesBySpec28` records the six versions the root graph raised, which
  `TestSandboxDepsPromoted_TS13_65` compares as a whole with the regenerated
  `go-list-m-all.txt`; `TestGraphTestsPassOnTheRegeneratedSnapshots_TS28_44`
  checks that each entry of both is the version its snapshot holds. TS-13-65's
  `grpc` and `protobuf` pins did not move and are not restated.
- **TS-26-35's pinned require list names eight more modules.**
  `requiredModules26` (`repository_cut_test.go`) gains the Ollama module and
  the seven indirect lines `go mod tidy` wrote for it, as spec 27 did for the
  OpenRouter SDK. The spec lists `repository_cut_test.go` in the task's
  touches but does not name this pin.
- **TS-28-44 passes before the module is in the graph.** It asserts that the four
  graph tests still exist and pass, and they passed on the unchanged tree. It
  went red once the graph changed and before they were restated (the child run
  failed with the `check.v1` and added-module lines of TS-14-42 and TS-15-40),
  and a mutation test shows it fails when a snapshot line is changed.

## The host is parsed by the CLI's rules, and refused where the CLI guesses (28-REQ-1.3 to 1.7, task 2)

28-REQ-1.4 says the value is "parsed by the rules Ollama's own CLI applies",
and 28-REQ-1.5 to 1.7 list what is refused. `envconfig.Host` accepts several
values the refusals cover and cannot be pointed at `Options.Env`, so
`parseHost` (`provider/ollama/host.go:30`) reimplements the rules, as Design
Decision 4 says. For every value the CLI parses without guessing it gives the
same address (`TestHostResolutionProperty_TS28_4`, `TestHostExamples_TS28_5`).
Where the CLI guesses, `parseHost` fails with an error wrapping
`ErrInvalidHost` (`TestInvalidHosts_TS28_6`):

- **The scheme is exactly `http` or `https`.** The CLI keeps any other scheme and
  gives it port 11434 (`ftp://h` is `ftp://h:11434`, `HTTP://h` is
  `HTTP://h:11434`). `HTTP://h` is refused rather than read as port 80, which
  would be a different server from the CLI's.
- **The port is digits, from 1 to 65535.** The CLI replaces any other port with
  its default and logs a warning, and accepts port 0. A port with nothing after
  the colon (`h:`) is no port, as in the CLI.
- **A value is an address and a path.** `?` and `#` are refused anywhere; the
  CLI reads `http://h:80?x=1` as `http://h:80` and drops the rest. A value that
  `net.SplitHostPort` cannot split and that is not an IP address, such as
  `http://[::1` (the CLI reads it as `http://[::1]:80`) or `h:80:90`, is refused.
- **User information is refused** with the value shown and its password replaced
  by `xxxxx` (`redactUserInfo`, `host.go:79`); the CLI reads `user:pw@h` as the
  host `user`. Every refusal shows the value this way, so a password in a value
  that fails for another reason is not echoed either.
- **The hosted service is refused** for `ollama.com`, any subdomain, any letter
  case, and a trailing dot. The SDK compares `Hostname() == "ollama.com"`
  exactly (`api/client.go:184`), so `OLLAMA.COM` would not have taken the
  signing path there.
- **Bare IPv6 and an empty host are kept as the CLI keeps them:** `::1` and
  `[::1]` are `http://[::1]:11434`, and `:8080` and `http://` use
  `127.0.0.1`.

`Options.Host` is "set" when it is not empty after trimming, so a host of spaces
falls through to the variable, as an empty `Env` value does
(`TestHostLookupOrder_TS28_3`).

## `New` starts with a `Stream` that only fails (task 2)

`TS-28-1` assigns the client to `core.ProviderClient`, so task 2 needs a `Stream`
method to compile. It returns an error and no channel (`provider/ollama/client.go`,
`(*Client).Stream`) and is replaced in task 3; it is not a `panic`.

## TS-28-9: an Authorization header the embedder's transport adds reaches the server (28-REQ-1.8, task 3)

TS-28-9 gives an embedder `RoundTripper` that adds `X-Embedder: yes` and also
`Authorization: Bearer x`, and expects that "no recorded request has an
Authorization header" and that `X-Embedder: yes` reaches the server. 28-REQ-1.8
and Design Decision 6 put the embedder's transport *beneath* the layer that
deletes `Authorization`, which "still lets an embedder that fronts Ollama with an
auth proxy add a header deliberately", and a layer above a transport cannot
remove what that transport adds. For an `Authorization` the embedder's own transport adds, the test
and the requirement cannot both hold.

The requirement and the decision are followed: `credentialFree.RoundTrip`
(`provider/ollama/transport.go:24`) deletes the header before the embedder's
transport sees the request, so a header it adds is the embedder's own.
`TestNoCredentialIsSent_TS28_9` (`provider/ollama/stream_test.go:74`) checks the
rest in three subtests: with `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY` and
`OLLAMA_API_KEY` set, no request carries `Authorization` or any of the three
values in its URL, headers or body; with `OLLAMA_AUTH=1` and a generated Ed25519
key under `$HOME/.ollama`, the SDK signs the request (the test requires the `ts`
query the SDK adds, or it would check nothing) and still no `Authorization`
reaches the server; and an embedder transport that adds `X-Embedder: yes` and
`Authorization: Bearer proxy-token` has both arrive. The signing subtest skips on
Windows, where the SDK does not find its key through `$HOME`.

## The provider sends through a copy of the embedder's `http.Client` (task 3)

28-REQ-1 says `HTTPClient` is "the `*http.Client` requests go through" and that a
nil one means a fresh client with the default transport. `sdkHTTPClient`
(`provider/ollama/transport.go:38`) copies the struct and replaces only its
`Transport`, so the embedder's `Timeout`, `CheckRedirect` and `Jar` are kept
(`TestEmbedderClientSettingsAreKept`). A nil client has no overall timeout, so a
long stream is cut only by the caller's context.

## The root module's line budget is 30,000, not 20,000 (TS-12-14, TS-13-66, TS-20-63; task 3)

The spec does not mention the budget. `core/size_test.go` (TS-12-14) holds the
root module to 20,000 lines of hand-written library code: non-blank,
non-comment lines of non-test Go files, with generated files and test-only
internal packages left out (`budgetLines`). Spec 27 left the root module at
19,773, which is 227 below it. `provider/ollama` was expected to be about 1,000
lines (`provider/openrouter` is 1,300), and with 430 of them in task 3 `make check`
failed in `TestLineBudgets_TS12_14`, `TestSandboxCountedInBudget_TS13_66` and
`TestRootModuleStaysWithinItsLineBudget_TS20_63` (`the root module has 20203
lines of hand-written library code, over 20,000`).

Nothing else in the repository fits the code under the old cap. PRD 14's
decision 2 puts both new providers in the root module "so choosing one is a
string in `Config`"; a nested module, which is how spec 14 kept the sandbox out
of the count (`docs/errata/14_sandbox_rsync_sync.md`), would keep the root from
importing the provider. The decision was the project owner's, taken when task 3
first failed on it: the cap rises to **30,000**.

- `core/size_test.go`: `TestLineBudgets_TS12_14` and
  `TestSandboxCountedInBudget_TS13_66` hold the root module to `n > 30000`, and
  the comments and messages say 30,000.
- The two tests that read the size test's source for the figure now require the
  new one: `TestRsyncBuildTags_TS14_43` (`internal/policy/buildtags_test.go`,
  `"n > 30000"`) and `TestGatesUnchanged_TS15_41`
  (`internal/policy/gates_15_test.go`, `"budgetLines(t, moduleRoot(t)); n >
  30000"`). They are not weakened: each still fails if the limit changes again.
- `TestRootModuleStaysWithinItsLineBudget_TS20_63` runs TS-12-14, so only its
  comment changed.
- The finished provider is 769 lines, so the root module is at 20,542 of the new
  30,000.
- What is counted is unchanged, and so is core's own 1,600-line budget. The
  `.specs` of specs 12 to 22 and 30 still say 20,000; they are history.

## Tool-call ids, usage and the concurrency test (28-REQ-6.3, 6.4, 6.7, 8.1, 8.2; task 4)

- **A synthesized id carries a per-Client prefix.** 28-REQ-6.4 asks for ids unique
  "within each response and across responses" of one Client, and step 4.4 suggests
  a counter. A counter alone gives every Client the same first id, and a
  transcript resumed in another process holds ids written by two Clients, which
  `provider.RepairToolPairing` would pair as one. The id is `ollama_<8 hex digits>_<n>`
  (`(*callIDs).next`, `provider/ollama/decode.go:62`), the digits random per Client and
  `n` atomic. `TestSynthesizedIDsDifferBetweenClients_TS28_29` pins it.
- **A vendor id that repeats within a response is replaced too.** The spec
  synthesizes an id only for a call without one; two calls with the same id would
  be paired as one by the repair and by the loop, so the second gets a made-up id.
  `TestSynthesizedIDsAreUnique_TS28_29` repeats vendor ids on purpose.
- **A done chunk with no count at all reports no usage.** `prompt_eval_count` and
  `eval_count` are plain integers in the SDK, so zero cannot be told from absent
  (`prompt_eval_cached_count` is a `*int` and can). `nettedUsage`
  (`provider/ollama/usage.go:24`) reads a chunk with neither, and no cached count,
  as one that reported nothing: `Requests` 1, no token field, no cost. 28-REQ-8.1
  sets the cost "whenever the done chunk reports usage", so this is where the
  rule ends; the agent prices such a turn from the catalog row, which is zero.
  Otherwise the input is the prompt count less the cached one, floored at 0, the
  cache read is set only when positive, and the cost is 0 with its bit
  (`TestUsageIsNettedAndPricedAtZero_TS28_40`, `TestUsageNettingProperty_TS28_41`).
- **A tool call's arguments are the SDK's rendering.** `Input` is
  `json.Marshal(call.Function.Arguments)`: the top-level keys are in the order
  the server wrote them (`TestToolCallKeepsIDAndKeyOrder_TS28_28`), but a nested
  object's keys are sorted and a number is re-rendered through `float64`, as the
  first section of this erratum records. 28-REQ-6.3 and Design Decision 2 ask for
  top-level order only.
- **TS-28-31's markers are handed out by the server in arrival order.** The
  spec has the server echo a marker taken from the request. Until task 5 the
  request carries nothing that differs from one call to the next, so
  `TestConcurrentStreamsAreIndependent_TS28_31` has the server number each
  response and send it as thinking, text in three chunks with a pause between
  them, and a tool call with no id, all of the same number. A stream that shared
  state with another comes back with parts of two numbers or an id another stream
  also got; a map of used ids shared by every stream, and an unsynchronised id
  counter, both fail it.

## Replayed arguments keep their bytes, and three small choices of the replay (28-REQ-2.3, 4.1 to 4.5; task 5)

- **A call's arguments are replayed as written, not as the SDK renders them.**
  Step 5.5 says to decode `ToolUseBlock.Input` into the SDK's
  `ToolCallFunctionArguments` with `json.Unmarshal`, and Design Decision 2 accepts
  that a number is re-rendered by it. That also sorts the keys of a nested object
  and writes `12345678901234567890` as `12345678901234567000`, while
  `ToolUseBlock.Input` is "the provider's own argument bytes, verbatim and
  AUTHORITATIVE for every replay" (`core/message.go:193`). `(*Client).arguments`
  (`provider/ollama/messages.go:108`) reads the members of `Input` in order and sets
  each value on the SDK's map as a `json.RawMessage` (`eachMember`, `:120`), which
  `json.Marshal` writes as it is: the top-level keys are in the order of `Input`
  (28-REQ-4.3, `TestReplayedArgumentsKeepKeyOrder_TS28_21`), and a number literal and a
  nested object are untouched (`TestReplayedArgumentsKeepTheirBytes_TS28_21`, which
  fails if `Input` goes through `json.Unmarshal` instead). Whitespace is compacted and
  `<`, `>` and `&` in a string are written as `<`, `>` and `&`, which mean
  the same. Input that is not a JSON object cannot occur for a block made by
  `core.NewToolUse`; if one does, no arguments are sent and `Warnf` says so.
- **Only the transcript is repaired.** 28-REQ-4.1 passes `Request.Messages`
  through `provider.RepairToolPairing`; `Request.Prefix` is encoded as given.
  `provider/openrouter` repairs both, the prefix and the history, each reported on its own.
- **An assistant message with nothing to send is left out.** One with no text, no
  tool call, and thinking that is redacted or another model's would be sent as an
  empty assistant turn; `encodeAssistant` (`provider/ollama/messages.go:74`) leaves it
  out (`TestAnAssistantMessageWithNothingToSendIsLeftOut_TS28_22`). A user message is
  always sent, with whatever text it has, as 28-REQ-4.2 says.

## Tools, options and think (28-REQ-2.4, 2.5, 3, 5; task 6)

- **The cut of a tool schema is the SDK type's, and its edges are the type's.**
  `toolParameters` (`provider/ollama/tools.go:40`) encodes the schema and decodes it
  into `api.ToolFunctionParameters`, as 28-REQ-3.1 says, so `additionalProperties`,
  `format` and `minimum` of a property are dropped
  (`TestToolsAreDeclaredWithTheSchemaSubset_TS28_15`). The type models an `items`
  schema and `$defs` as `any`, so a keyword inside either stays: the "absent
  anywhere" of TS-28-15 holds for the properties and for an `items` that holds only a
  type, which is what the test uses. The type's `properties` has no `omitempty`, so
  a schema without properties would be sent as `"properties":null`; it is made an
  empty map, and a missing type is `object`, which also makes the nil schema of
  28-REQ-3.2 `{"type":"object","properties":{}}`
  (`TestToolWithoutASchemaIsAnEmptyObject_TS28_16`).
- **A tool choice of none does not look at the schemas.** `chatTools`
  (`provider/ollama/tools.go:15`) returns before decoding, because no tool is sent:
  a schema the SDK cannot carry fails the turn only when its tools are sent.
- **A schema that cannot be decoded ends the turn before the request, in
  `run`.** `chatRequest` (`provider/ollama/request.go:16`) returns the error and
  `run` ends the turn with it (`provider/ollama/stream.go:27`) before any HTTP attempt.
  `stream.go` is not in the task's touches in `tasks.json`; the error has to end
  the turn there. The text is `ollama: the input schema of tool "bad_tool" is not
  one Ollama's tool type can carry: <the SDK's error>`
  (`TestUndecodableSchemaFailsBeforeTheRequest_TS28_17`). Its schema has a top-level
  type of `["object","null"]` (a nullable object), which the type's one-string `type`
  cannot read.
- **`think`.** `thinkFor` (`provider/ollama/think.go:17`) reads the model as
  `provider/anthropic` does, and sends a string level (`api.ThinkValue{Value: "medium"}`).
  An Efforts value that is empty after trimming sends nothing, where the spec says
  only "its value, trimmed and lower-cased"; an empty string is not a level. With no
  Efforts map the effort is sent as it is named.
- **`num_predict` and the sampling options.** `numPredict`
  (`provider/ollama/request.go:66`) follows 28-REQ-2.4 row by row
  (`TestNumPredict_TS28_13`). `options.stop` is sent when the request has at least one
  stop sequence; temperature and top_p are sent whenever the pointer is set, 0 included.

## How a failed turn is classified (28-REQ-7, 28-REQ-8.3; task 7)

`(*Client).run` (`provider/ollama/stream.go:25`) sends one request, with no retry, and
decides the turn in this order: a done chunk means the turn finished, whatever
`Chat` returned after it; otherwise a cancelled caller's context means aborted,
which takes precedence over every classification below
(`TestCancelAbortsTheTurn_TS28_34`: within a second, `core.ErrAborted`, "Request was
aborted", no `ErrorEvent`, no tool call, the text kept); otherwise
`(*Client).failureOf` (`provider/ollama/errors.go:37`) says why, from what the SDK
returned, which is not what the spec's table assumed:

- **An error status with an empty body is read from the transport's record.**
  `Chat` returns `nil` for it, so the status the transport recorded in the call's
  `attempt` is used: a 404 is `ErrModelNotFound`, a 401 or 403 is a `*StatusError`,
  any other is `ollama: HTTP <code>: <status text>`. Only a 2xx, or no status, with no
  done chunk is `ErrStreamTruncated`
  (`TestA404IsModelNotFound_TS28_36`, `TestStatusErrors_TS28_38`, each with and
  without a body).
- **A 401 arrives as `api.AuthorizationError`,** which carries no message; it gets
  the same text as a 403.
- **"Unreachable" is a failed dial.** The error is a `*net.OpError` whose `Op` is
  `dial`, which a DNS failure also is. A connection reset while the body is read is
  an `OpError` with `Op` `read`: the server was reached, so it is a plain
  `ollama: <error>` that keeps the text so far
  (`TestAResetMidStreamIsNotUnreachable_TS28_35`). The error wraps `ErrUnreachable`
  and the `*net.OpError`, and its text names the host, `ollama serve` and `OLLAMA_HOST`.
- **A 404 is both `ErrModelNotFound` and a `*StatusError{Code: 404}`,** and a
  context-window error that came as a status is both `ErrContextWindow` and a
  `*StatusError` with its code. The unexported `failure` type
  (`provider/ollama/errors.go:16`) carries a text of its own and wraps the errors it
  stands for (`Unwrap() []error`), so `errors.Is` and `errors.As` find all of them.
- **The context-window rule is the message.** `isContextWindow`
  (`provider/ollama/errors.go:83`) accepts a message that mentions both "context
  length" and "exceed", which the three messages in Ollama's `server/routes.go`
  all do (`the input length exceeds the context length`, `input after truncation
  exceeds maximum context length`, `input exceeds maximum context length and cannot be
  truncated further`). It applies to a status error and to an error line inside a 200
  stream (`TestContextWindowErrors_TS28_37`). The text names the model and the
  `num_ctx` sent, includes Ollama's message and says that a model already loaded at a
  smaller length keeps it until it unloads.
- **Every error text starts "ollama:"** and holds only the host among the values the
  provider reads, and never an environment variable
  (`TestOneAttemptAndNoSecrets_TS28_39`, which also counts one request per call for a
  404, 401, 429, 500 and 503, and for a closed port). A failed turn reports one request
  and no token field (`TestFailedTurnsReportOnlyTheRequest_TS28_42`).

## The loop flattens a failed turn's error, so TS-28-48 and TS-28-49 are checked on the event (28-PATH-2, 28-PATH-3; task 8)

TS-28-48 and TS-28-49 say `agent.Run` returns an error that `errors.Is` finds
`ollama.ErrModelNotFound`, `ollama.ErrUnreachable` or `ollama.ErrStreamTruncated`
in. It cannot: when a turn ends in an error the loop sets the run's error to
`errors.New(assistant.ErrorMessage)` (`loop.go:146`), a new error made from the
message, so the chain the provider built is gone by the time `Run` returns. The
provider's `StreamResult.Err` does wrap the sentinel
(`TestATruncatedStreamFails_TS28_33`, `TestA404IsModelNotFound_TS28_36`,
`TestARefusedConnectionIsUnreachable_TS28_35`).

The loop is not changed here: it is outside the spec, and what `Run` returns is
every provider's contract. The smoke tests (`provider/ollama/smoke_test.go`) run the
real loop through `Agent.Stream`. The loop forwards the provider's events onto
its own stream, so the terminal `ErrorEvent` it forwards still has the typed error:
each test finds exactly one, checks `errors.Is` on its `Err`, checks that the run's
own error says what to do (`ollama pull <model>`, `ollama serve`) or that the stream
was cut, that the run's last assistant message is `StopReasonError` (the text so far
kept and no tool call, for the cut stream), and that the tool's handler never ran. The
404 is run twice, with Ollama's message and with an empty body.

## What the smoke tests cannot see, and what covers it (task 8)

- **The run's cost.** `Agent` sets the cost bit on the run's total itself, so TS-28-47's
  "total cost 0" cannot show that the provider reported its cost. The smoke test checks
  `Usage.Has(UsageCostUSD)` and a cost of 0 on each assistant message, which is what the
  provider reported, and the run's sums (198 input, 556 cache read, 170 output, two
  requests).
- **The window the agent works with.** `agentkit.New` resolves `Config.Model` with
  `catalog.Lookup` (`agent.go:152`), which lists Claude rows only and gives any other id
  a default descriptor with a 1,000,000-token window. The loop prunes tool results and
  refuses a request that cannot fit by that window (`loop.go:292`), so on Ollama it is
  not the row's 32,768 that the agent works with, while `num_ctx` is. The spec leaves
  choosing the provider from `Config` to `provider_selection`; `docs/configuration.md`
  says so.
- **TS-28-46** (the existing `provider/anthropic` and `provider/faux` tests pass
  unmodified, and `make check` passes) is checked with commands, not a Go test: `git diff
  main -- provider/anthropic provider/faux agent.go loop.go` is empty, and `make check`
  exits 0. A test that reads git history cannot run in a checkout without it.
