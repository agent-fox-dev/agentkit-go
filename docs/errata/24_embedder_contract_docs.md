# Erratum: spec 24 `embedder_contract_docs`

The record PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`) asked for at its close, and the places where the delivered change differs from `.specs/24_embedder_contract_docs`. PRD 12 itself is not edited (spec 20, design decision 19).

## PRD 12 §Intent, measured again

PRD 12's §Intent table counts the lines of five recurring chores in four examples, but states no counting rule. The numbers below are taken by a stated rule, over the tree that introduced PRD 12 and over the current tree, by reading the code. No example was changed.

**The rule.**

- The files are each example's non-test Go files: `examples/codemode/main.go`, `examples/sandbox/main.go`, `examples/gateway/main.go` and every non-test `.go` file in `examples/harness`.
- A counted line is a non-blank, non-comment line as gofmt formats it.
- A line counts toward a chore when it belongs to a statement or declaration that does that chore (a local helper function included), or to the `if err != nil { … }` that immediately checks that statement's error. A line inside a statement counts toward the innermost chore it serves: `faux.ToolCall` and `faux.Text` lines inside an `agentkit.New` call count as chore 2, and the rest of that call as chore 4.
- A statement is a top-level statement or declaration of a function body or the file that has at least one counted line of the chore. A helper function is one declaration. Error checks are excluded from the statement count.
- The chores are PRD 12's five: (1) picking tools by name; (2) building the scripted model turns and their arguments, the marshalling of the script into the call's arguments included; (3) the sandbox lifecycle: `Create`, `Sync`, `Tools`, `sandbox.Open`, `Export`, `Close`; (4) the guard together with the `agentkit.New` or `agentkit.NewRunner` statement and its `Config` or `Toolset` literal; (5) running a tool call outside the loop: any implementation of a pipeline step.

**The baseline** is the tree at `95af32d8661dc9bc5f00a6f8aac42b1280926a2e`, the commit that added PRD 12 (`git log --diff-filter=A --format=%H -- docs/prd/12-simplify-the-embedder-contract.md`). Git history was available.

**Decisions the rule needed.**

- The baseline harness's `toolServer`, `newToolServer`, `call`, `nested`, `nested.Call` and `direct` are chore 5: they are the rebuilt pipeline, with its name-keyed tool table (the lookup step) and its logging inside `call`. The rest of `server.go` (the MCP server, the token check, the config strings, the helpers) is counted in no chore.
- On the current tree the harness's logging hooks (`hooks`, `printf`, `indentFor`, `summarize`, `firstLine`, `truncate`) are example instrumentation and are counted in no chore. In the baseline the same logging sat inside `call` and counts toward chore 5.
- The scripted stand-in's `agentkit.New` in `examples/harness/harness.go` counts toward chore 4 on both trees. PRD 12 published 0 for the harness's chore 4.
- PRD 12's published numbers came from no stated rule, and several are not reproducible by one (for example, 30 lines of sandbox lifecycle in `sandbox`, where the baseline code has 16). They are shown for reference, not as the baseline.

**Counts**, as lines / statements. The published column is lines only.

| Chore | Example | PRD 12 | Baseline | Current |
|---|---|---|---|---|
| 1 Pick tools by name | `codemode` | 8 | 7 / 2 | 4 / 1 |
| | `sandbox` | 5 | 4 / 2 | 4 / 1 |
| | `gateway` | 5 | 4 / 2 | 4 / 1 |
| | `harness` | 0 | 0 / 0 | 0 / 0 |
| 2 Scripted turns and arguments | `codemode` | 9 | 14 / 3 | 9 / 2 |
| | `sandbox` | 5 | 3 / 2 | 3 / 2 |
| | `gateway` | 5 | 3 / 2 | 3 / 2 |
| | `harness` | 6 | 6 / 2 | 5 / 2 |
| 3 Sandbox lifecycle | `codemode` | 0 | 0 / 0 | 0 / 0 |
| | `sandbox` | 30 | 16 / 5 | 8 / 3 |
| | `gateway` | 30 | 19 / 6 | 11 / 4 |
| | `harness` | 35 | 23 / 7 | 8 / 3 |
| 4 Guard and `agentkit.New` | `codemode` | 10 | 9 / 1 | 9 / 1 |
| | `sandbox` | 12 | 11 / 1 | 10 / 1 |
| | `gateway` | 12 | 11 / 1 | 10 / 1 |
| | `harness` | 0 | 12 / 2 | 24 / 4 |
| 5 Tool call outside the loop | `codemode` | 0 | 0 / 0 | 0 / 0 |
| | `sandbox` | 0 | 0 / 0 | 0 / 0 |
| | `gateway` | 0 | 0 / 0 | 0 / 0 |
| | `harness` | 95 | 135 / 6 | 0 / 0 |

**Totals**, the counted lines of each example's non-test Go files:

| Example | Baseline | Current |
|---|---|---|
| `codemode` | 192 | 185 |
| `sandbox` | 150 | 140 |
| `gateway` | 220 | 210 |
| `harness` | 1122 (`server.go` 265) | 970 (`calls.go` 86) |

What the numbers show. The pipeline rebuild is gone: chore 5 in `harness` goes from 135 lines in six declarations to none, and `server.go` no longer exists. The sandbox lifecycle in `sandbox`, `gateway` and `harness` shrinks to the `sandbox.Open` call, the deferred `Close` and the `Export`. Picking tools by name is one `core.Named` call where there was a loop and a `switch` or a map. Chore 4 in `harness` is larger than the baseline because the baseline's guard sat in `newToolServer`'s argument and its wiring was the rebuilt server itself; the current figure counts the guard argument, the nil-guard check, `NewRunner` with its `Toolset` literal and the scripted stand-in's `agentkit.New`.

## PRD 12's acceptance criteria as delivered

1. **Met.** `Runner.Call` runs `code_mode`, and the nested `read_file`, with no `Agent`; the nested call is reported to `Toolset.Events` with the `code_mode` call as its parent (spec 21: TS-21-34, TS-21-29, TS-21-57).
2. **Met.** `NewRunner` refuses what `New` refused, with the same errors, and `New` returns `NewRunner`'s error unchanged before any request (spec 21: TS-21-5, 6, 7, 42, 59).
3. **Met.** A run's transcript, events, stop reasons and `max_tokens` handling are unchanged by the move under the runner (spec 21: TS-21-45, TS-21-46).
4. **Met.** `workspace.Open` refuses an escaping path in `Resolve`, its walk visits what `Ignored` allows, and `tools.For` returns the eleven tools with byte-identical definitions and an unchanged sandbox definition digest (spec 20: TS-20-9, 32, 33, 34).
5. **Met.** `tools` declares none of the removed identifiers, `tools.Options` has five fields and none is `Workspace` or `Ignore`, and `codesearch.Options` has no `Ignore` (spec 20: TS-20-37, 47, 48, 60).
6. **Met.** `core.Named` names the missing tool and lists the names the set holds (spec 22: TS-22-4, with TS-22-5).
7. **Met.** `sandbox.Open` on `local` with a `LocalDir` returns a live sandbox and the proxies; a failing step returns its error with the sandbox closed (spec 22: TS-22-15 to 19, 23, 37).
8. **Met.** `ServeLoopback` answers `tools/list` with the runner's tools, refuses a request without the token with 401, returns a blocked call as an `isError` result with the guard's reason, and `mcp.Pool` runs `code_mode` through it (spec 23: TS-23-6, 15, 19, 30, 63).
9. **Met.** `ServeStdio` serves the same tools over pipes and to a child process (spec 23: TS-23-20, 21, 62).
10. **Partly met, assessed per statement (design decision 9).** `examples/harness` has no `server.go` (spec 23: TS-23-54), and chore 5 is zero in all four examples. A line count cannot meet "at most one line" for any call that can fail, because the call and its error check are four lines at least. Per statement, chores 1 and 4 are at their bound of one in `codemode`, `sandbox` and `gateway`, and chore 1 is zero in `harness`. Above the bound are:
    - chore 2 in all four examples: two statements each, the scripted turns (in `faux.New` or inside `agentkit.New`) and the one that marshals the script into the call's arguments (`scriptArgs` in `codemode`, `json.Marshal` in the others);
    - chore 3 in `sandbox` (three statements), `gateway` (four) and `harness` (three): `sandbox.Open`, the deferred `Close` and the `Export`, plus the explicit `Close` in `gateway`;
    - chore 4 in `harness` (four): the guard argument, the nil-guard check, `NewRunner` with its `Toolset` and the stand-in's `agentkit.New`.
    
    These are recorded, not fixed: this spec changes no example.
11. **Met.** `make check` passes in every module of `MODULES`, `mcpserve` included. The no-listen check keeps its scope for the root and sandbox modules and exempts the nested modules that listen, and the cross-target gate covers `mcpserve` (spec 23: TS-23-49, 50, 51; spec 24: TS-24-13).
12. **Not met as written.** The golden was regenerated in four specs, not once in spec 24: spec 20 (`tools.For`, `tools.References`, the removed `tools` identifiers and the `workspace` section, in three commits), spec 21 (`Toolset`, `NewRunner`, `Runner`, the new `Config`, in two), spec 22 (`core.Named`) and spec 24 (the five sections for `guard`, `provider/faux`, `sandbox`, `codesearch` and `mcpserve`). Each change broke the four golden comparison tests under `make check` until the golden was regenerated, so it could not wait for the last spec. The erratum PRD 12 asked for is in `docs/errata/13_sandbox_core.md` (the API-freeze entry, completed by this spec), and names PRD 12.

## Divergences from this PRD

- **The documentation was already partly done.** The PRD's Background describes the README, the architecture data flow and the stale-identifier sweep as untouched by specs 20 to 23. They had patched the sentences naming their identifiers: the runner attaches the nested caller in "Nested tool calls", "Code mode" runs outside an agent through `Runner.Call`, an "MCP serving" section existed, and a search found no removed identifier outside a migration note. This spec restructured and completed the README contract and the architecture sections, and left "Nested tool calls" and "Code mode" as they were.
- **The golden was regenerated in task 1, not task 2.** Widening `apiPackages` turns TS-13-67, TS-14-44, TS-15-42 and TS-17-61 red under `make check`. Task 1 regenerated and committed the golden to stay green, so task 2's tests passed on first run; each was shown to fail by mutation.
- **`make check` is the gate, not the spec's `make lint` and `make test`** (`.specs/steering.md`). `make check-release`, the full `make test` and the slow `internal/policy` gates were left to the user.
- **TS-24-9 compares with HEAD, not a pinned commit.** A test pinned to the golden spec 23 left would fail at the next legitimate API change. It checks that the five sections equal their packages' exported API and that the golden's change against HEAD has no removed line and none before the first appended section. Requirement 24-REQ-3.1 against spec 23's merge (`aa0da90`) was measured once by hand: 118 added lines, none removed.
- **The child runs of TS-24-5, 11, 12 and 16 name their tests with anchored patterns.** Running `go test ./internal/policy` in full would start two nested `make check-release` runs (TS-09-20, TS-19-66).
- **`make check` depends on `test-short`.** TS-24-13 asserts `fmt vet lint test-short`; the spec says `test`, which is `make check-release`'s.
- **Two stale statements outside the task's `touches` were fixed with the doc comment they repeated:** the `sandbox/gatewayd/go.mod` comment ("the one network service") and `docs/api.md`, which listed `sandbox/images`, a package of tests only, as a provider.

## After this spec: 24-REQ-1.5's guard list changes, on purpose

24-REQ-1.5 keeps `preSpec17Guard` "unchanged from spec 22". Exporting
`guard.Commands`, the reading of a shell line `Restricted` judges, adds five
declarations to `guard` on purpose (`docs/errata/17_sandbox_podman.md`, "gains
`Commands`"). The list gains them and keeps every spec 22 entry, so TS-17-61
still fails on any other change to `guard`. The `SyntaxError.Error` method
sorts ahead of `Allow`, so TS-24-7 now checks that `Allow` is in the list
rather than that it leads it.
