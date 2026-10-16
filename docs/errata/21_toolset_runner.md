# Erratum: spec 21 `toolset_runner`

These are the places where the delivered code and its tests differ from the
spec's text, or carry a limit the spec's rules preserved, and why. The spec's
rule for all of them is its Goal 3: running an agent changes in no observable
way.

## The guard's `TurnCount` in a run is the zero-based turn index (21-REQ-8.5, TS-21-45)

TS-21-45's pseudocode asserts `g.TurnCount==1` for a call in the first turn of a
run. The loop has always passed the number of turns completed before the turn
whose calls are running, so a first-turn call sees 0 (`loop.go:155`,
`a.runTools(ctx, s, &assistant, toolCalls, turnCount)`, where `turnCount` is
incremented only after the turn's results are recorded at `loop.go:163`).
21-REQ-8.5 says the loop passes "the turn count" and 21-REQ-8.6 that nothing a
run does changes, so the delivered behaviour is the loop's, not the
pseudocode's.

`TestLoopHandsTurnsToTheRunner_TS21_45` (`agent_runner_test.go:327`) asserts the
behaviour over two turns: the first turn's call sees `TurnCount` 0 and the
second turn's calls see 1, each with a non-nil `Assistant`.

## The root package doc is in `agent.go` (21-REQ-10, task 8's `touches`)

Task 8 lists `doc.go` among the files it touches, for the root package's doc
comment naming `Toolset` and `Runner`. The repository has no `doc.go`: the
package doc is the comment above `package agentkit` in `agent.go`, and that is
where it now names them.

## A reachability cycle that enters only through `Policy.CustomTools` crashes construction (21-REQ-2.1)

21-REQ-2.1 keeps today's order: check every registered tool, resolve the
policy, check every resolved tool. `ToolPolicy.Resolve` always ends in
`pruneReachable`, which recurses without bound on a cyclic tool
(`core/toolpolicy.go:117`). A cycle in `Toolset.Tools` is refused by the first
check, before `Resolve` runs, with `agentkit: reachable tools cycle detected:
…`. A cycle that is present only in `Policy.CustomTools` reaches `Resolve`
unchecked, and the process dies with a stack overflow, which cannot be
recovered. `New` did the same before this spec, and `NewRunner` (`runner.go:76`)
keeps it: the spec rules out new refusals at construction and changes no
behaviour of the policy.

The property test for error parity (`TestNewRunnerAndNewAgreeOnEveryToolset_TS21_8`,
`runner_config_test.go`) therefore generates cycles only in `Tools`
(`genToolset`, `runner_config_test.go:178`). Closing the gap is a change to
`core.ToolPolicy.Resolve` or to the order of the checks, and belongs in its own
change.

## The harness log has a line fewer for a call that never reaches a hook (21-REQ-9.5)

The harness example's log and counts are produced by the toolset's hooks, as
21-REQ-9.5 asks, and 21-REQ-9 allows the log format to change where the hooks
cannot reproduce it exactly. The wrapping `Guard` logs a call as `run` or
`blocked` and counts a block; `After` logs a call that ran and failed, as
`error`; `Events` counts every call from its start event
(`examples/harness/main.go`, `hooks`). Two things differ from the hand-rolled
server's log: a call that never reaches the guard or `After` (an unknown tool,
arguments that fail validation) is counted and has no line, and a blocked call
has its `blocked` line and no `error` line after it. The `blocked` and `run`
lines and the nested `error` line keep their exact format, and the blocked count
is 3 for the sequence TS-21-53 runs (`TestServerGuardsDirectAndNestedCalls`).
