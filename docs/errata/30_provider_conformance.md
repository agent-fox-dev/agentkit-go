# Erratum: spec 30 `provider_conformance`

The places where the delivered code and its tests differ from the text of
`.specs/30_provider_conformance`, and why. PRD 14 and the spec are not edited. In
each case the test checks what the code delivers.

## The spec was written before specs 26 to 29 landed

Its "Verified External API" table marks `catalog.Select`, the OpenRouter and
Ollama error types, `Config.Provider`, `Config.Getenv`, `Agent.Resolution` and
`core.Usage.Unpriced` as not in the tree, and its Background says the root
module is held to 20,000 lines and that the run error is
`errors.New(assistant.ErrorMessage)`. All of them exist with the shapes the table
assumes; the root budget has been 30,000 since spec 28 (the new package is test
files and a `doc.go`, so it counts nothing either way); and since PR #158 the
loop keeps the provider's error (`failedTurn`, `loop.go:458`), so the run's error
wraps `ErrStreamTruncated` and not only the forwarded `ErrorEvent.Err`.
`checkCutOff` (`internal/providerconformance/cases_fail_test.go:101`) asserts
both, which is more than 30-REQ-5.1 asks.

## `-run '…/openrouter/tool_call'` selects two subtests (TS-30-49; task 11)

TS-30-49 says the pattern `TestProviderConformance.*/openrouter/tool_call`
selects "exactly the one subtest". `-run` matches each element as an unanchored
regular expression, so `tool_call` also selects `tool_call_stop`. The smoke test
(`internal/providerconformance/smoke_test.go:22`) anchors the last element with
`$`. The pattern in PRD goal 2, `…/ollama/thinking`, is unambiguous and is used
as written by TS-30-2.

## Anthropic's in-stream error carries a prefix (30-REQ-5.4; task 5)

The Background and design decision 9 say Anthropic reports an `event: error` as
`<type>: <message>`, without the `anthropic:` prefix. With the SDK in use the
event reaches the provider as an `*sdk.Error`, and `statusText`
(`provider/anthropic/stream.go:275`) renders it `anthropic: HTTP 200: api_error:
<message>`, a `*StatusError` with code 200. `checkErrorInStream` requires only
that the vendor's message is contained in the error text, as the spec's own
design decision does, so it holds with either form.

## `ReasoningTokens <= OutputTokens` holds trivially (30-REQ-4.2; task 4)

The logical usage of 30-REQ-2 has prompt, cache-read and output tokens and a
cost, and no reasoning count, so every fake reports none and the check compares
0 with 20. It is kept as the spec states it and would fire for a provider that
reported more reasoning than output tokens.

## TS-30-3 asks which variables are the provider's, not which resolve to the fake (task 6)

TS-30-3 says every variable the provider requests resolves to the subtest's own
fake. The Anthropic provider also asks for the deployment-selection variables
(`CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_BEDROCK`, …), which the map does not
hold and so resolve to nothing. `TestEverySubtestBuildsItsAgentFromItsOwnFake_TS30_3`
(`suite_test.go`) requires the map to hold exactly the provider's variables with
every URL among them pointing at the fake, and every variable asked for to be one
the provider owns.

## The live service is never needed by `make check` (30-REQ-7, TS-30-34; tasks 7 and 11)

TS-30-34 needs a live service, a credential that costs money or a model on the
machine, so it cannot be part of the gate. `TestSmokeMakeTargetAgainstTheLiveServices_TS30_34`
(`smoke_test.go:144`) carries the same opt-in as `TestLive` and is skipped
without `AGENTKIT_LIVE=1`; it was not run for this delivery. What is checked
without a service: `make provider-live-test` end to end in an environment with no
provider ready (`smoke_test.go:117`), and the real `TestLive` subtests run in a
child `go test` against loopback servers that play each vendor's service
(`live_gate_test.go`, `TestSubtestsRunEndToEndWhenTheirPrerequisitesHold_TS30_34`).
The Anthropic skip message is `Resolve`'s error as it is, since the error begins
`anthropic:` already.

## The examples' summary line (30-REQ-8.4, 30-REQ-8.5; tasks 8 to 10)

The spec leaves open which stream the summary goes to and what formats it. It
stays on stderr, where the examples have printed it, and is built by
`summary(agentkit.Resolution, core.RunResult)` (`examples/codingagent/main.go:238`
and the same function in `customtools` and `mcp`), which takes the whole result
for its usage, turn count and stop reason, where the task text says a
`Resolution` and a `core.Usage`. An effort of none is printed `effort none`.

## The example tests never use the developer's keys (30-REQ-9; tasks 8 to 10)

30-REQ-9.1 gives the subprocess "the test's environment plus the case's
variables". The first run of the new tests against the unchanged `codingagent`
did that, defaulted to Anthropic and sent one request with the
`ANTHROPIC_API_KEY` of the shell, which Anthropic rejected. `runExampleAt`
(`examples/codingagent/main_test.go:123`, one copy in each example) therefore
removes the variables that choose a provider, a model, an effort or a deployment
and points every Anthropic and OpenRouter variable at a loopback decoy that must
accept no connection. That runs 30-REQ-9.4 in every test, and a regression to
the wrong provider reaches the decoy and not a vendor.

## Tests that passed on arrival

Most tests of this spec check behaviour that specs 10, 11, 27 and 28 and tasks 2
to 9 had already delivered, so they could not be seen to fail first. Each commit
names them under `Deviation: test-first`. They were proved by injecting defects
into the providers, the loop, the examples and the checks themselves and
requiring the intended test to fail; the commit messages give the counts.
