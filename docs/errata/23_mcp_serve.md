# Erratum: spec 23 `mcp_serve`

These are the places the delivered code, its tests and the order of the work
differ from the spec's text, and why.

## The SDK does not cancel a call when a session is closed, so mcpserve does

23-REQ-3.4 and 23-REQ-6.2 say a call's context, "derived from the request's",
is cancelled when the session ends or the endpoint is force-closed. The SDK's
`ServerSession.Close` is graceful: it waits for the calls in flight, and the
SDK cancels a call only when its client cancels the request or the reader fails.
`ServeStdio` closing a session whose tool was running would wait for the tool, as
would closing the HTTP server by force. A session and an endpoint therefore
carry a lifetime, passed as a value of the context the SDK server is connected
with (the SDK keeps a connect context's values for every call, and not its
cancellation), and the call handler's context ends with it
(`mcpserve/call.go:75` `withLifetime`, `:81` `endWithLifetime`;
`mcpserve/server.go:112` `open`, `:128` `close`; `mcpserve/loopback.go`).
`TestCancellationReachesTheRunner_TS23_16` and
`TestForceCloseCancelsInFlightCalls_TS23_36` fail without it (the first by
hanging in `Close`).

## A running tool reports its own outcome; the runner reports `aborted` only before the handler starts

23-REQ-3.4 and TS-23-16 and TS-23-36 expect the runner's result for a cancelled
call to have the error code `aborted`. `Runner.Call` returns `aborted` for a
context that is already done before any handler starts (`batch.go`,
`abortedResult`); a handler that is running gets the cancelled context and
reports its own outcome, as the built-in tools do when cancelled. The tests'
tool answers `aborted` when it sees `ctx.Done()`, and they assert that the
context passed to `Runner.Call` was cancelled and that the runner's result is
that `aborted`.

## TS-23-30's trailing-space case cannot be expressed over HTTP

TS-23-30 lists `Bearer <token> ` with a trailing space among the requests that
must get 401. `net/http` strips trailing whitespace from a header value when it
parses the request, so the handler receives exactly `Bearer <token>`, which is
the valid header, and the request is authorized. The test
(`TestUnauthorizedRequestsAreRefusedFirst_TS23_30`) uses an extra character, an
inner blank, a lower-case scheme, a wrong token, no header and two
`Authorization` headers instead. Two headers are refused even when one is valid:
`mcpserve/loopback.go:148` requires exactly one.

## No connection wrapper records the first read error

23-REQ-4 suggests wrapping the SDK connection to record the first read error,
because what `Server.Run` returns was unverified. Measured against v1.8.0, the
session's `Wait` returns the first read error that is not a clean end of stream
(here the `*wire.Error`) and nil for a clean end, so `ServeStdio` uses it
directly (`mcpserve/stdio.go`, `mcpserve/server.go:125`).
`TestOverLimitFrameEndsTheSession_TS23_25` asserts the exact wire rule for each
limit.

## Logging closes two paths the spec did not name

23-REQ-8.5 forbids the token, the header, the arguments, the result text and the
`Detail` in a log record. Two paths could still carry workspace content: the
runner's error code, which a tool chooses (`mcpserve/call.go:57` `errorCode`
logs a code that is not a short identifier as `other`), and `net/http`'s own
error reports, such as a recovered handler panic with its text, which go to the
standard logger and so to stderr (`mcpserve/loopback.go:87` sets `ErrorLog` to a
Warn record `http server error` with no text). A refused body that cannot be
read logs the reason `body unreadable`, a fourth beside the spec's three.
`TestNoSecretReachesTheLog_TS23_48` and `TestZeroOptionsServesSilently_TS23_44`
fail without them.

## The header timeout is a test seam

23-REQ-5.8 gives the HTTP server a `ReadHeaderTimeout` of 10 s and handler
contexts derived from ctx. `headerTimeout` is a package variable
(`mcpserve/loopback.go`) so `TestServerTimeoutAndContext_TS23_34` can shorten
it; production code never assigns it. The same test requires the context of a
request in flight to end within a second of ctx ending, which only the HTTP
server's `BaseContext` does (the endpoint's own stop waits two seconds first).

## The hooks moved to `calls.go` from `main.go`, not `server.go`

23-REQ-10.1 says `calls.go` keeps what `server.go` still held after spec 21: the
per-call log and the call and blocked counts. Those hooks (`hooks`, `indentFor`
and their methods) were in `main.go`; `summarize`, `firstLine`, `truncate` and
`errShellBlocked` were in `server.go`. All of them are in
`examples/harness/calls.go` with unchanged behaviour.

## Three spec-21 harness tests were changed, not dropped

TS-21-51 parsed `server.go`, which is gone; it now parses every non-test file of
the example with the same bans. TS-21-54 drove the example's `toolServer` with
the MCP SDK's client; it now drives an `mcpserve` endpoint with AgentKit's own
`mcp` client, which also keeps the SDK out of the example's test imports so it
can be an indirect requirement (23-REQ-10.6). `TestServeRequiresTheToken` is
removed, as the spec says.

## `docs/architecture.md` counts seven nested modules, not six

The spec says to update "six nested modules". The file said five and listed six
(`codesearch`, `sandbox`, `sandbox/gatewayd`, `examples/sandbox`,
`examples/gateway`, `examples/harness`); with `mcpserve` the correct count is
seven, and the file says seven.

## The slow policy gates and the offline full gate are left to the user

`.specs/steering.md` (Quality Gate) has agents run `make check`, not
`make check-release`. TS-19-66 runs `make check-release` offline, and the
cross-target and host-cgo gates of TS-23-50 are slow `internal/policy` tests that
`-short` skips. This spec's `GOPROXY=off make check` (TS-23-53) ran in full, with
`mcpserve` in `MODULES`. For `mcpserve` the cross-target gate's own loop (`go
build` and `go vet` with cgo off for linux/amd64, linux/arm64, darwin/arm64 and
windows/amd64, and with cgo on for the host) was run directly. The slow gates
themselves, and `make check-release`, are left to the user.

## Tests that passed on their first run

Declared in the commits: TS-23-3 and TS-23-11 (task 1), TS-23-13, 15, 17 and 18
(task 2), TS-23-12, 19, 37, 38 and 39 (task 5), TS-23-1 and TS-23-2 (task 8),
TS-21-54 (rewritten) and `TestHarnessArgs` (task 9), TS-23-52 and 53 (task 10)
and the four smoke tests TS-23-61 to 64 (task 11), each because an earlier task
had built what it exercises. Task 11's were shown to detect a wiring gap by four
one-at-a-time mutations that each turned the intended test red.
