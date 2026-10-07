# Erratum: runner and tool metadata — departures from PRD §6 and §9

**Relates to:** `docs/prd/06-support-multi-phase-coding-pipelines.md`, §6
(process runner) and §9 (tool metadata on the result).

## RunArgv instead of Exec

**What the PRD says.** §6 proposes a new `tools.Exec` function returning a new
result type.

**What is implemented.** `tools.RunArgv` is extended instead. It already is
"the control without the tool semantics": no status line, spill only when
`SpillDir` is set. `tools.ExecResult` is already an exported name, so a second
result type would collide. ADR 01 also rules out duplicate aliases.

## Outcome instead of TimedOut/Aborted

**What the PRD says.** §6 proposes `TimedOut bool` and `Aborted bool` on the
result.

**What is implemented.** `tools.Outcome` already distinguishes timeout from
abort, with abort taken from the caller's context. Two booleans beside it would
be a second, redundant encoding. The contract is pinned with tests instead.

## No ExitCode -1

**What the PRD says.** §6 proposes `ExitCode -1` for a process that never
started.

**What is implemented.** A signal-killed child keeps 128+signum
(NFR-COMPAT-06). A process that never started is an error, not a result.
`Outcome` already says when an exit code is not a normal exit.

## KeepHead instead of KeepTail

**What the PRD says.** §6 proposes `KeepTail bool` (default true, keeping the
tail).

**What is implemented.** `KeepHead bool` (default false, keeping the tail). The
zero value must keep today's tail truncation for every existing caller.
`TruncateHead` is `iota` 0, so a `TruncateMode` field would silently flip the
default.

## No WaitDelay option

**What the PRD says.** §6 proposes a `WaitDelay` option.

**What is implemented.** `DrainIdle` and `DrainCeiling` already bound the
post-exit wait (REQ-TOOL-17.5). `cmd.WaitDelay` stays an internal backstop.

## §7 and §5's usage and abort items were already implemented

**§7 (custom prompt keeps tool guidelines)** was already shipped. `prompt.Build`
keeps the tool guidelines under a custom prompt and keys the shell guidelines on
the active shell tool (`guard.ShellToolNames` order). See
`docs/errata/custom_prompt_keeps_tool_guidelines.md`.

**§5 (usage accounting and aborted-summary rejection)** was already shipped.
Summarization usage reaches `Agent.Usage` through `core.ReportUsage`, and
`ValidateSummary` rejects `StopReasonAborted`. See
`docs/errata/aborted_summary_is_a_failure.md`.

The one remaining §5 item — retrying the summarizer request — moves to the
`transcript_pruning` scope, because it reverses REQ-GO-12.3's routing and
belongs with the other transcript-transform changes.
