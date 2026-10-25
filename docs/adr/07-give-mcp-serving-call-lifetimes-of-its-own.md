# ADR 07: Give MCP serving call lifetimes of its own

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken in 2026-09)

## Context

`mcpserve` serves a `Runner`'s tools over MCP, on stdio or on loopback HTTP
behind a bearer token, so another harness (Claude Code, Pi) can call them; the
`examples/harness` program is its main user. Its spec assumed the MCP Go SDK
cancels a call when its session closes and left the reporting of read errors
unverified. Measured against the SDK, neither assumption held as written, and
the logging rules left two paths open. The settlements were recorded in an
erratum. The erratum is gone; the rules the code follows are here.

## Decision

- **A session and an endpoint carry a lifetime.** The SDK's
  `ServerSession.Close` waits for the calls in flight and cancels a call only
  when its client cancels it or the reader fails. So the lifetime is passed as
  a value of the context the SDK server is connected with (the SDK keeps a
  connect context's values for every call, not its cancellation), and each
  call's context ends with it: closing a session or force-closing an endpoint
  cancels the calls in flight.
- **A running tool reports its own outcome.** A cancelled call reaches the
  runner as a cancelled context; the result is the runner's `aborted` only when
  the context was done before the handler started (see ADR 01).
- **Exactly one `Authorization` header.** A request with two is refused even
  when one is valid. `net/http` strips trailing whitespace from header values,
  so `Bearer <token> ` is the valid header.
- **Read errors.** The session's `Wait` returns the first read error that is
  not a clean end of stream, and nil for a clean end; `ServeStdio` returns it
  as it is, with no connection wrapper.
- **Logs carry no workspace content.** No record holds the token, the header,
  the arguments, the result text or the `Detail`. An error code that is not a
  short identifier is logged as `other`. `net/http`'s own error reports (a
  recovered handler panic among them) go to a Warn record `http server error`
  with no text, never to the standard logger. A refused body that cannot be
  read is logged with the reason `body unreadable`.
- **The harness's log is the toolset's hooks.** The guard logs a call as `run`
  (with the arguments as the guard received them) or `blocked`, `After` logs a
  call that ran and failed as `error`, and `Events` counts every call from its
  start. A call that never reaches the guard or `After` (an unknown tool,
  arguments that fail validation) is counted and has no line.

## Consequences

- A harness that disconnects, or an embedder that stops serving, does not
  leave tools running on its behalf.
- Upgrading the MCP Go SDK means re-checking the two measured behaviours above:
  how `Close` treats calls in flight, and what `Wait` returns.
