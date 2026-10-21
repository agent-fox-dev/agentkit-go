# Erratum: spec 04 `runner_and_tool_metadata`

These are the places the delivered code and its tests differ from the spec's
text, and why.

## A timed-out call's Duration now covers its Timeout (04-REQ-5.3)

04-REQ-5.3 says a timed-out call has `Duration >= Timeout`, and TS-04-22
asserts it. `runArgv` created the timeout context first and took the
`Duration` clock (`start`) after the log file and the pipes were set up, so
the deadline started earlier than the clock that measures it. Whatever the
setup took came off the `Duration`: a call whose setup took 100ms, with a
`Timeout` of 200ms, reported about 100ms. The setup is microseconds in the
ordinary case, so the shortfall shows only when something stretches it.

`runArgv` now creates the timeout context right after `start`
(`tools/exec.go:428`), so the deadline can never begin before the clock.
`TestTS_04_22_DurationCoversTimeoutAfterSlowSetup`
(`tools/runner_timeout_clock_test.go:19`) holds the setup for 100ms with a
named pipe as `LogPath` and fails without the change (`Duration = 100.2ms`).

## TS-04-21 times its cancel from the child, not from before the call

TS-04-21 cancels "after 150ms" and asserts `150ms <= Duration`. Its pseudocode
arms the timer before `RunArgv` is called, but `Duration` starts inside the
call, after `LookPath`, the pipes and the process-group setup. When the timer
wins that race by a millisecond, `Duration` is under 150ms and the assertion
fails (`Duration = 148.837458ms, want >= 150ms` in a full `make test`). No
change to `RunArgv` can make that bound hold, because the caller's timer is
outside it.

`TestTS_04_21_CancelKillsTreeAndClassifiesAbort`
(`tools/runner_outcome_test.go:161`) now starts the 150ms when the child
reports it has started, by creating a file as its first act. The order is
`start`, then the child's file, then the timer, so the cancel always comes more
than 150ms after `start`, and the assertions of the spec (`Outcome`,
`150ms <= Duration < 1s`, no surviving grandchild) are unchanged. With a 10ms
gap injected between arming the timer and the call, the old form of the test
failed at `Duration = 142ms`; with a 40ms gap injected before the call, the new
form passes.
