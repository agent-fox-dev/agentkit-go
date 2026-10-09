# Dependency rulings

Why a third-party module is in AgentKit, and on what terms. The rule since
[PRD 09](prd/09-replace-hand-rolled-code-with-libraries.md) is that a
maintained library replaces hand-rolled infrastructure when it carries the
same guarantees. Each module here is one where that was decided on purpose.

## `go.starlark.net`

**Used by.** `codemode`, the tool that runs model-written scripts. Nothing
else imports it.

**Version.** `v0.0.0-20261005163335-bcb1a1a55bf9`. The module publishes
pseudo-versions only; there are no tagged releases to pin to.

**Why this module.** A code-mode script is written by a model and runs inside
the host process, so the interpreter has to be safe to run without trusting
the script:

- **No host I/O of its own.** Starlark has no file, network, environment,
  process or clock primitives. A script can reach only the builtins the host
  passes in, and `codemode` passes in the bound tools and three helpers.
  Module loading goes through a host hook, which `codemode` sets to refuse
  every module.
- **A step budget.** `Thread.SetMaxExecutionSteps` counts execution steps and
  cancels the thread when the budget is spent. A runaway loop therefore ends
  as `step_limit_exceeded` rather than by burning a CPU until the wall-time
  limit fires.
- **Cancellable.** `Thread.Cancel` stops a running script from another
  goroutine, which is how the wall-time limit and a cancelled run stop one.
- **Deterministic and pure Go.** There is no cgo and no background
  goroutines, and it builds for every target `internal/policy` checks.
- **A language models write.** It is a Python dialect. The tool's description
  spells out what differs (no exceptions, no imports, no classes).

**What is pulled in.** `codemode` imports `go.starlark.net/starlark`, and that
brings in `syntax`, `resolve` and two internal packages. The module's REPL
dependencies (`chzyer/readline`, `x/term`) and its protobuf support are not
imported. `codemode`'s `TestDepsDefaultsAndIsolation_TS08_44` checks this.

**Alternatives considered.** An embedded JavaScript engine is larger, has
ambient capabilities to remove rather than none to add, and has no step
counter. A WebAssembly sandbox would need a compiler for the model's language
as well.
