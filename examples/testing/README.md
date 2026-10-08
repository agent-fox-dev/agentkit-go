# testing — how to test the agent code you write

Once an agent works, the next question is how to test it. The code under test
is not AgentKit — it is your tool handlers, your `BeforeToolCall` interceptor,
your middleware, your stream renderer — and all of it only really runs inside
the loop. A real model is slow, costs money and is not deterministic; a
hand-rolled fake means guessing at the loop's contract.

AgentKit ships the fake: **`provider/faux`**, a supported provider that
replays a scripted sequence of assistant turns and emits events in the
normative order. Scripting a turn is the whole setup. This example is a test
file, not a program — seven tests, one technique each, meant to be copied into
your repository.

## Run it

No key, no network, no environment variables:

```bash
go test ./examples/testing/ -v
```

## What you'll see

```
=== RUN   TestAScriptedProviderDrivesAToolCallAndAFinalAnswer
--- PASS: TestAScriptedProviderDrivesAToolCallAndAFinalAnswer (0.00s)
=== RUN   TestTheRequestCarriesTheSystemPromptTheToolAndTheToolResult
--- PASS: TestTheRequestCarriesTheSystemPromptTheToolAndTheToolResult (0.00s)
=== RUN   TestAToolHandlerIsTestedByCallingIt
    --- PASS: TestAToolHandlerIsTestedByCallingIt/known_city (0.00s)
    ...
--- PASS: TestAFailedOrAbortedTurnStillLeavesATerminalMessage (0.00s)
PASS
ok  	github.com/agentfox/agentkit-go/examples/testing	0.329s
```

## The setup to copy

From the fixtures at the top of `agent_test.go`:

```go
p := faux.New(
    faux.Turn{Blocks: []core.ContentBlock{
        faux.FauxText("Let me look that up."),
        faux.FauxToolCall("call_1", "get_weather", `{"city":"Oslo"}`),
    }, StopReason: core.StopReasonToolUse},
    faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("It is 7C in Oslo.")},
        StopReason: core.StopReasonStop},
)
cfg := core.AgentConfig{
    Model:      faux.Model(),
    Providers:  core.ProviderRegistry{faux.API: p.APIProvider()},
    StopPolicy: stop.AfterTurns(5), // an under-scripted test fails instead of spinning
}
```

`newAgent` wraps that with a `mutate func(*core.AgentConfig)` hook so each test
can add an interceptor or middleware. `weatherTool` is the stand-in for your
code under test.

## The seven techniques

| # | Test | Technique |
|---|---|---|
| 1 | `TestAScriptedProviderDrivesAToolCallAndAFinalAnswer` | Script a run, assert the handler ran once (`atomic.Int32`), the provider was called twice (`p.Calls()`), and `res.FinalText()`. |
| 2 | `TestTheRequestCarriesTheSystemPromptTheToolAndTheToolResult` | Assert what was **sent** with `p.Requests()`: your text is in the assembled system prompt, the tool is declared with its schema, and the second request carries the tool result. |
| 3 | `TestAToolHandlerIsTestedByCallingIt` | Call `tool.Handler` directly, table-driven. No agent at all; write most of your tests like this. |
| 4 | `TestABlockedToolCallBecomesAnErrorResultAndTheRunContinues` | A blocked call never runs the handler, yields an error result carrying `core.BlockErrorCode` and the reason, and the run continues. |
| 5 | `TestMiddlewareWrapsEveryModelCallAndTheLastRegisteredIsOutermost` | Middleware wraps every model call; the **last** registered is **outermost**; a header set by middleware reaches the provider. |
| 6 | `TestTheEventSequenceIsTheOneAStreamingUIExpects` | `p.ChunkSize` forces deltas; assert an ordered *subsequence* of event types (`faux.EventNames` makes failures readable); `TextEndEvent` carries the whole text. |
| 7 | `TestAFailedOrAbortedTurnStillLeavesATerminalMessage` | `faux.Turn{Err: …}` fails a turn mid-stream; cancelling from inside a handler aborts deterministically. Both leave a terminal message and a resumable transcript. |

## Gotchas

- Assert that *your* system prompt is **contained** in what was sent — the SDK
  assembles its own sections around it.
- Assert event **subsequences**, not exact lists, or a harmless provider change
  breaks your test.
- Test aborts by cancelling from inside a handler, not with sleeps; flaky
  failure-path tests get deleted.
- Middleware runs under `-race` in tests; guard shared counters (see
  `countingMiddleware`), and copy maps on the `Request` rather than writing
  through them.

## Related

Packages: `provider/faux` (`New`, `Turn`, `FauxText`, `FauxToolCall`,
`Model`, `Requests`, `Calls`, `ChunkSize`, `EventNames`), `core` (event types,
`BeforeToolCallDecision`, `Middleware`). [`agentdemo`](../agentdemo) uses the
same provider as a tour; [`triage`](../triage) and [`cleaner`](../cleaner)
have full application test suites built the same way.
