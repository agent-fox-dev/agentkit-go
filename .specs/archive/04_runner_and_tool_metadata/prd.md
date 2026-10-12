---
spec_id: "04"
spec_name: "runner_and_tool_metadata"
title: "An embedder's process runner and tool metadata on the result message"
status: "active"
created_at: "2026-10-07T12:24:48.250973Z"
updated_at: "2026-10-07T12:24:48.250973Z"
intent_hash: "bb3e648353b730648e5e6de9c418d2b130ed48bbd0bd4c2f636b7bcd03332029"
schema_version: 2
source: "docs/prd/06-support-multi-phase-coding-pipelines.md"
---
## Intent

Give an application that sequences agents the SDK's own subprocess control, and the structured metadata every tool already computes, as public surfaces. Then it never has to re-implement a less safe process runner or parse exit codes and truncation out of the text the model reads.

## Goals

1. An embedder can run a program through `tools.RunArgv` (or a shell command through `tools.Run`) with:
   - standard input;
   - a log file it names, receiving the full output as it arrives;
   - a choice of keeping the head or the tail of the output;
   - the credential-stripped environment by default.

   Every process the command starts, grandchildren included, is killed on timeout and on cancellation. Tests on real processes pin this.
2. `ExecResult.Outcome` tells a timeout (`ExecOptions.Timeout` expired) from a cancellation (the caller's context ended, including the caller's own deadline) from an exit status, a signal or success. Never two at once. Tests through `RunArgv` pin this.
3. Every `core.ToolResultMessage` produced by a tool handler carries that handler's `ToolMetadata` (exit code, outcome, truncation, spill path, totals, duration, line ending). An embedder reads it from the message, from `AfterToolCall`, from `TurnEndEvent`, from `StopContext` and from `RunResult` without parsing `[exit N]`.
4. Metadata survives a session-log round trip and `NewAgentFromSession`, and appears in the event JSON union.
5. The request bodies of all five wire APIs are byte-identical whether or not tool results carry metadata: the five request goldens are unchanged.
6. The built-in `execute`, `run_command` and `powershell` tools produce byte-identical model-visible output, `Data` and `Metadata` as before.
7. The root module stays standard-library-only and cgo-free; `internal/policy` stays green.

## Non-goals

- **No new `tools.Exec` function or second result type.** `RunArgv` is extended instead (Design Decision 2).
- **No new text for the model.** Metadata never reaches the model. `ToolResult.ToLLMMap` and `LLMText` are unchanged, and status lines stay as they are.
- **No metadata on results that no handler produced.** This covers unknown tool, invalid arguments, blocked, aborted, `max_tokens`-synthesized and repair-synthesized results.
- **No stdin for the built-in shell tools.** REQ-TOOL-06's null-device stdin stands for the model's tools.
- **No migration of the examples.** The example runners (`examples/cleaner/git.go`, `examples/flatline/git.go`) are not migrated onto `RunArgv` in this spec.
- **No redo of work already shipped.** §7 of the input (a custom prompt keeps the tool guidelines) and §5's usage accounting and aborted-summary rejection already shipped (`docs/errata/custom_prompt_keeps_tool_guidelines.md`, `docs/errata/aborted_summary_is_a_failure.md`). No `KeepGuidelines` flag is added.
- **No pipeline, workflow or phase abstraction.**
- **The rest of the input lands in later specs of this split:**
  - `prompt_text_documents`: prompt text as documents.
  - `stable_cache_prefix`: the cross-agent cache prefix.
  - `transcript_pruning`: pruning, transform composition and summarizer retry.
  - `workspace_change_journal`: the change journal.
  - `read_deduplication`: read dedupe.
  - `symbol_navigation_parity`: per-language symbol navigation.
  - `required_tool_choice`: forced tool choice.

## Background

The input (`docs/prd/06-support-multi-phase-coding-pipelines.md`) asks for nine SDK changes on behalf of agent-fox's `fix`/`impl` rebuild. Its own ordering puts §6 (a process runner) and §9 (tool metadata on the result) first, because the agent-fox engine's verifier and phase runner need them before anything else. Reading the code changes the size of both items.

**What already exists for §6.** `tools/exec.go` already exports the runner the input describes:

- `Run` (through the fixed bash ladder) and `RunArgv` (no shell; argv resolved against the child's own `PATH`, with `./x` resolved against `Dir`). Both share one `runArgv` body.
- That body runs the child in its own process group (`proc_unix.go` `Setpgid`; `proc_windows.go` `taskkill /T`), and kills the group on caller cancellation or on `ExecOptions.Timeout`.
- Stdout and stderr share one pipe the runner owns. A re-arming post-exit drain (`DrainIdle`, `DrainCeiling`) follows.
- Output is truncated from the tail through `Accumulator`, with an optional spill of the full output into `SpillDir`.

`ExecResult` already has `Output`, `Outcome`, `ExitCode`, `Truncated`, `TotalBytes`, `SpillPath` and `Duration`. `ClassifyOutcome` applies abort > timeout > signal > exit, with `aborted` taken from the caller's `ctx` and `timedOut` from the derived deadline. That is exactly the timeout-versus-cancellation distinction agent-fox #216 needed. `TestGrandchildDoesNotSurviveTreeKill` pins group kill on cancellation through `Run`.

What `RunArgv` lacks for an embedder:

- **No stdin.** `cmd.Stdin = nil` is hard-coded.
- **Tail-only truncation.** `NewAccumulator(opts.MaxBytes, TruncateTail)` is fixed.
- **No caller-named log.** The spill is a `CreateTemp` file in `SpillDir`.
- **Unsafe environment default.** `ExecOptions.Env == nil` inherits the full process environment, credentials included. By contrast, `tools.Options.Env == nil` means `ReducedEnv(nil)` (`Options.withDefaults`), and every built-in tool passes it explicitly.
- **A silently dropped Wait error.** A `Wait` error that is not an `*exec.ExitError` is discarded, so the call reports exit 0 / `ok`. Nothing reaches that path today. Adding stdin makes it reachable (a failing reader, `exec.ErrWaitDelay`).

The in-repo embedders show the cost of the gap. `examples/cleaner/git.go` and `examples/flatline/git.go` each re-implement `runCommand` over `exec.CommandContext`, with stdin and without process-group kill.

**What exists for §9.** `core.ToolResult.Metadata *ToolMetadata` (`core/tool.go`) is filled by `read_file`, `edit_file`, `search_files`, `find_files`, `list_files`, `file_outline`, `find_symbol`, `fetch_url`, `code_search`, the subagent tool and, through `execResultToTool`, the three shell tools (`ExitCode`, `Outcome`, `SpillPath`, `Truncated`, `TotalBytes`, `DurationMS`). It is then lost:

- `batch.go`'s `toolResultMessage` copies only the text and blocks into `core.ToolResultMessage`, which has no metadata field (`core/message.go`).
- `AfterToolCallContext` carries only `Result *ToolResultMessage`, not the `ToolResult`.

An embedder therefore parses the status line (`[exit 2]`, `[timeout after 30s]`) out of model-facing text.

**Persistence and encoding paths.**

- The session codec (`session/codec.go`, `encodeMessage`/`decodeMessage`, `toolResultKnown`) persists tool-result fields by name and keeps unknown keys in `Unknown`.
- `session.EncodeMessage` is also the `core.MessageEncoder` the event JSON union uses (`core/eventjson.go`), so one codec change reaches both.
- Provider encoders build their wire bodies from `Content` field by field.
- `compaction/estimate.go` counts a tool result as its content plus its tool name.
- The request goldens (`golden_requests_test.go`, `testdata/golden/request_*.json`) and the session-log golden (`golden_test.go`, `testdata/golden/session_log.jsonl`) pin the bytes.

## Requirements

**R1. Standard input for the runner.**
- `tools.ExecOptions` gains `Stdin io.Reader`, honoured by both `Run` and `RunArgv`.
- Nil keeps today's behaviour: the child's stdin is the null device.
- When non-nil, the runner copies the reader into the child's stdin and closes the child's stdin when the reader returns `io.EOF`.
- A child that exits or closes its stdin without reading everything is not an error.
- A read error other than `io.EOF` closes the child's stdin and is reported in `ExecResult.IOErr` (R5), whatever the exit status.
- The call does not wait for a reader that is still blocked after the child has exited and the post-exit drain has finished. The copier ends when the reader returns.
- The built-in shell tools never set `Stdin`.

**R2. Which end of the output is kept.**
- `ExecOptions` gains `KeepHead bool`.
- False, the zero value, keeps the tail exactly as today.
- True keeps the first `MaxBytes` bytes, followed by the `Accumulator`'s existing head-mode marker, which names the elided byte count and the spill or log path when there is one.
- `MaxBytes <= 0` still means `DefaultByteLimit` (50 KB).
- `Truncated` and `TotalBytes` mean the same thing in both modes.

**R3. A caller-named log of the full output.**
- `ExecOptions` gains `LogPath string`. When set, the complete interleaved output is written to that file as it arrives, independent of truncation.
- A relative `LogPath` is resolved against `Dir`, or against the process working directory when `Dir` is empty.
- Missing parent directories are created with mode `0o700`. The file is created or truncated with mode `0o600` before the process starts.
- If it cannot be opened, the call returns an error and starts nothing.
- For that call `LogPath` replaces `SpillDir`: no temporary spill file is created. `ExecResult.SpillPath` is the absolute log path, even when the command printed nothing, and the truncation marker names it.
- A write failure after the start stops further logging, does not affect the process, and is reported in `IOErr`.
- The SDK never deletes the file.

**R4. The reduced environment by default.**
- `ExecOptions.Env == nil` now means `ReducedEnv(nil)` for both `Run` and `RunArgv`, the same rule `tools.Options.Env` already follows (REQ-SEC-08).
- A non-nil `Env`, including an empty one, is used verbatim.
- A caller that wants the full inherited environment passes `os.Environ()`.
- `RunArgv`'s program lookup keeps using the `PATH` of the environment the child runs with. `ReducedEnv` keeps `PATH` verbatim, so lookups are unchanged.

**R5. The outcome contract.**

For every call that starts a process, `ExecResult.Outcome` is exactly one of `ok`, `exit`, `signal`, `timeout`, `abort`, classified from the state at the child's exit:
- `abort` exactly when the caller's `ctx` was done at that moment, including when the caller's own deadline expired.
- `timeout` exactly when the deadline derived from `ExecOptions.Timeout` had expired and the caller's `ctx` had not.
- Otherwise `signal`, `exit` or `ok` from the wait status. `ExitCode` keeps the 128+signum convention for signal-killed children on unix.

On both timeout and abort:
- The whole process tree is killed.
- `Duration` is measured to the child's exit.
- A cancellation that arrives during the post-exit drain does not reclassify a finished command.

Errors and I/O failures:
- A failure to start returns a non-nil error and a zero `ExecResult`, and nothing else does. Start failures are: empty argv, program not found, `LogPath` unopenable, pipe or fork failure.
- `ExecResult` gains `IOErr error`. It is non-nil when moving bytes between the caller and the process failed without stopping the process: a `Stdin` read error, a log or spill write error, or `Wait` reporting an I/O completion failure such as `exec.ErrWaitDelay`. `Outcome` is still classified from the exit status.
- A `Wait` error that is neither an exit status nor a signal is never silently discarded again.

Tests through `RunArgv` on real processes pin:
- caller cancel → `abort`;
- `Timeout` expiry → `timeout`;
- a caller deadline shorter than `Timeout` → `abort`;
- no grandchild survives a timeout (today's test covers cancellation only);
- stdin round-trip;
- head mode;
- `LogPath` content equal to the full output when `Output` is truncated;
- `LogPath` open failure → error and nothing run;
- nil `Env` strips a credential variable.

**R6. The built-in shell tools are unchanged.**
- `execute`, `run_command` and `powershell` keep calling `Run`/`RunArgv` with:
  - their explicit `Options.Env`;
  - tail truncation at `DefaultByteLimit`;
  - `SpillDir`;
  - no `Stdin` and no `LogPath`.
- Their `ToolResult.Text`, status lines, `Data`, `Error` and `Metadata` stay byte-identical.
- `IOErr` is not rendered into a tool result.
- The existing tests in `tools/tools_test.go` and `tools/reqtool06_test.go` pass unmodified.

**R7. Tool metadata on the result message.**
- `core.ToolResultMessage` gains `Metadata *core.ToolMetadata`.
- The batch executor sets it from the `ToolResult` the handler returned when that result's `Metadata` is non-nil and has at least one field set. The message holds its own copy, `ExitCode` pointer included, so a tool reusing a metadata value cannot alias a message.
- A `Handler`-style tool, a handler panic, and every result finalized without running a handler leave it nil. Handler-less results are:
  - unknown tool;
  - invalid arguments;
  - `BeforeToolCall` block;
  - plugin block;
  - batch abort;
  - `max_tokens` synthesis (`loop.go` `synthesizeTruncated`);
  - the `no_result` backstop;
  - `provider.RepairTranscript`'s synthetic results.
- `ToolResultMessage.Clone` deep-copies `Metadata`.
- Because history, `ToolResultEvent`, `TurnEndEvent.ToolResults`, `StopContext.ToolResults` and `RunResult.Messages` all carry the message, every observer and stop policy sees the metadata with no further plumbing.

**R8. The handler's result reaches `AfterToolCall`.**
- `core.AfterToolCallContext` gains `ToolResult core.ToolResult`: the handler's result as returned (after panic and `Handler`-error conversion), passed by value.
- `Result *ToolResultMessage` is built before the hook runs, already carries `Metadata`, and remains the one mutable record.
- A hook that edits `Result.Metadata` changes what is emitted, persisted and seen by the stop policy. A change to the `ToolResult` copy has no effect.
- The termination-vote rules (REQ-TOOL-13.3) and the image normalization after the hook are unchanged.

**R9. Persisted, never sent.**

Session codec:
- It writes a non-nil `Metadata` with at least one field set as a `metadata` object, after `usage` and before `timestamp`.
- Keys are `ToolMetadata`'s JSON tag names in declaration order, with zero fields omitted, except that `exit_code` is written whenever the pointer is non-nil (exit 0 is information).
- Decoding restores it. An absent key, or an object with no known fields, decodes as nil, and unknown keys inside it are dropped, as they are inside `usage`.
- `metadata` joins `toolResultKnown`.
- Compatibility: a log written before this change reads back with nil metadata. A log with metadata, read by an older build, keeps the key in `Unknown` and re-encodes losslessly, as today.

Event JSON and resume:
- The event JSON union carries the metadata through `session.EncodeMessage`.
- `NewAgentFromSession` restores it into history.

Providers:
- The Anthropic, OpenAI chat, OpenAI responses, Google and Ollama encoders send nothing new. `golden_requests_test.go`'s canonical tool result gains a populated `Metadata` and the five request goldens stay byte-identical.
- The compaction token estimate is unchanged.

Session-log golden:
- `golden_test.go`'s tool result gains a metadata value, and `testdata/golden/session_log.jsonl` changes by that one object.

**R10. Documentation.**
- `docs/configuration.md`:
  - gains a "Subprocess runner (`tools.Run`, `tools.RunArgv`)" subsection with:
    - an `ExecOptions` table: `Dir`, `Timeout`, `MaxBytes`, `KeepHead`, `SpillDir`, `LogPath`, `Stdin`, `Env`, `DrainIdle`, `DrainCeiling`;
    - the `ExecResult` fields, with the `Outcome` rules and `IOErr`;
  - in its `BeforeToolCall`, `AfterToolCall` row, says that `AfterToolCall` receives the handler's `ToolResult` and that `ToolResultMessage.Metadata` carries the tool's metadata.
- `docs/architecture.md`:
  - the finalize step of the data flow mentions the metadata;
  - the `tools` row names `RunArgv` as the embedder's process runner.
- A new erratum in `docs/errata/`, prefixed with this spec's number like the `01_`–`03_` files, records where this spec departs from `docs/prd/06` §6 and §9: `RunArgv` instead of `Exec`, `Outcome` instead of `TimedOut`/`Aborted`, no `ExitCode -1`, `KeepHead` instead of `KeepTail`, no `WaitDelay`. It also records that §7 and §5's usage and abort items were already implemented.
- `make check` passes.

## Design Decisions

1. **Split, with this as the first scope.** The input spans nine independently buildable areas across `tools`, `core`, `compaction`, `prompt`, `outline`, `codesearch` and every provider. This PRD covers §6 and §9, the two the input orders first and which agent-fox's engine needs before the rest. The other scopes are listed in Non-goals and in the recommended split.
2. **Extend `RunArgv`; add no `tools.Exec`.** `RunArgv` already is "the control without the tool semantics": no status line, spill only when `SpillDir` is set. `tools.ExecResult` is already an exported name, so a second result type would collide. ADR 01 also rules out duplicate aliases.
3. **Keep `Outcome`; add no `TimedOut`/`Aborted` booleans.** `Outcome` already distinguishes timeout from abort, with abort taken from the caller's context (agent-fox #216's need). Two booleans beside it would be a second, redundant encoding. This spec pins the contract with tests instead.
4. **No `ExitCode -1`.** A signal-killed child keeps 128+signum (NFR-COMPAT-06). A process that never started is an error, not a result. `Outcome` already says when an exit code is not a normal exit.
5. **`KeepHead bool` rather than `KeepTail`.** The zero value must keep today's tail truncation for every existing caller. `TruncateHead` is `iota` 0, so a `TruncateMode` field would silently flip the default.
6. **No `WaitDelay` option.** `DrainIdle` and `DrainCeiling` already bound the post-exit wait (REQ-TOOL-17.5). `cmd.WaitDelay` stays an internal backstop.
7. **`LogPath` reuses the spill mechanism.** It is the spill with a caller-chosen path, opened eagerly so a caller who asked for a log learns at once that it cannot have one. It wins over `SpillDir` so there is one full-output file per call. A relative path resolves against `Dir`, matching how `RunArgv` already resolves `./prog`.
8. **`ExecOptions.Env == nil` means `ReducedEnv(nil)`.** This aligns the runner with `tools.Options.Env` and REQ-SEC-08, so the safe environment is the default for embedders too. Every in-repo caller already passes an explicit `Env`, and the module is untagged with all consumers in this repository (ADR 01).
9. **The runner owns the stdin pipe**, as it already owns the output pipe. `os/exec` reports a stdin copy error only when the child exited 0, so owning the copier is the only way to observe a reader failure whatever the exit status.
10. **One `IOErr` field for byte-moving failures that did not stop the process.** Returning a non-nil `error` alongside a result would break the existing rule that an error means nothing ran. Swallowing the failure is the bug this spec removes.
11. **Metadata is copied onto the message only when non-nil and non-empty.** An empty metadata (the subagent tool's `DurationMS: 0`) carries no information. Treating it as nil makes the session round trip exact.
12. **`AfterToolCallContext.ToolResult` is a read-only value.** The message is already the hook's mutable record. Two mutable records would leave it ambiguous which one is persisted.
13. **The session codec uses `ToolMetadata`'s own JSON tag names and drops unknown nested keys.** This keeps the on-disk shape identical to the documented `ToolResult` JSON, and matches how the codec treats `usage`.
14. **Provider non-transmission is proven by the existing request goldens.** No encoder change is needed, because every encoder builds its wire body from `Content`. Adding metadata to the canonical request and keeping the goldens byte-identical is the regression guard.
15. **Go API docs go in `docs/configuration.md`, not `docs/api.md`.** `api.md` is the network (MCP server) API by its own title and by the steering table. `configuration.md` already documents `tools.Options` and `codesearch.Options`.
16. **§7 and most of §5 are already implemented.**
    - `prompt.Build` keeps the tool guidelines under a custom prompt and keys the shell guidelines on the active shell tool (`guard.ShellToolNames`). `configuration.md`'s `SystemPrompt` row already says so.
    - Summarization usage reaches `Agent.Usage` through `core.ReportUsage`, and `ValidateSummary` rejects `StopReasonAborted`.
    - The one remaining §5 item, retrying the summarizer request, moves to `transcript_pruning`, because it reverses REQ-GO-12.3's routing and belongs with the other transcript-transform changes.
17. **The examples are not migrated here.** The cleaner's `git`/`gh` calls deliberately inherit the full environment for `gh`'s token, and `examples/flatline` is a separate module. Migrating them changes example behaviour, and none of this spec's goals need it.
18. **In the split, the change journal comes before read dedupe and symbol parity.** The input orders §10 and §11 after §4. But §4's read dedupe invalidates through the journal, so the journal goes first and dedupe becomes its own scope after it.
