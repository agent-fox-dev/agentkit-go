# Erratum: spec 08 `code_mode`

Where the delivered code mode differs from `.specs/08_code_mode`, and why.

## 08-REQ-3.1: scripts also see Starlark's other pure builtins

**Spec.** Module globals contain only the listed builtins, the bound tools,
`parallel`, `call` and `is_error`.

**Code.** Starlark's builtins live in one process-wide table
(`starlark.Universe`) that cannot be trimmed per thread. Scripts therefore also
see `any`, `all`, `chr`, `ord`, `reversed`, `tuple`, `fail`, `hash`, `bytes`
and `set`. None of them reaches the host, so 08-REQ-3.3 holds. Test: TS-08-14,
`TestSandboxHasNoHostPrimitives_TS08_14`.

## 08-REQ-4.2: code mode needs the agent's nested dispatcher

**Spec.** A bound tool dispatches through `core.CallNested`. The test spec
calls `cm.Execute` directly with real tools.

**Code.** `core.CallNested` works only on a context the agent's batch prepared
for a tool that declares `ReachableTools`. Called anywhere else, a script's
first tool call fails naming `core.ErrNoNestedCaller`, rather than running
the tool around the agent's interceptors. The smoke tests (TS-08-45 to
TS-08-48, `codemode/smoke_test.go`) therefore run a real agent on
`provider/faux`. Unit tests attach a stand-in dispatcher with
`core.WithNestedCaller`. Test: `TestBindingWithoutDispatcherFails`.

## 08-REQ-7.4 and 08-REQ-8.4: output past the limit both stops and truncates

**Spec.** 08-REQ-7.4 stops a script whose output passes `MaxOutputBytes`.
08-REQ-8.4 truncates that output and spills it.

**Code.** Both happen. The script stops with `output_limit_exceeded`. Its
result text is the accumulator's window: the head, the elision marker
naming the spill file, and the tail. The spill file holds everything.
`tools.Accumulator` writes its spill file from the first byte, so a run
whose output fit removes it. Tests: TS-08-31, TS-08-36, TS-08-37, TS-08-38
and TS-08-47.

## 08-REQ-9.2: an undefined name fails before the script runs

**Spec.** TS-08-42 makes two calls and then fails on an undefined name, and
expects both calls in the ledger.

**Code.** Starlark resolves every name before executing anything, so a
script that names something undefined fails as a whole, before its first
call. It is still reported as `runtime_error` with the line. TS-08-42
(`TestLedgerReportedOnFailure_TS08_42`) fails after two calls with a runtime
failure, `fail("now")`, instead.

## Test-spec details that do not hold

- `tools.ListFiles(ws)` and `tools.ReadFile(ws)` do not exist; tests take the
  tools from `tools.All`.
- `hasattr(parallel, "__call__")` is False in Starlark. TS-08-12 checks
  `type(parallel)` and the other helpers' types instead.
- TS-08-28's `while True: pass` would hit the default 100 000-step limit
  long before a 50 ms timeout. The test raises `MaxSteps` so that it
  exercises the timeout.
- Return values are JSON numbers: `Data["return_value"]` holds `int64` for an
  int, not `int`.

## Rules and limits the spec does not state

- **Bindable names.** A bound tool's name must be an ASCII Starlark
  identifier, and must not be a Starlark builtin or one of `parallel`,
  `call`, `is_error`, `main` and `result`. A script could not call it
  otherwise. An MCP tool whose name contains `-` cannot be bound
  (`checkBindable`, `codemode/binding.go`; test:
  `TestNewRefusesUnbindableNames`). A parameter name that is not an
  identifier is still declared as written, so its signature is not valid
  Starlark.
- **Values that contain themselves.** A list or dict that contains itself is
  refused as a tool argument or return value
  (`TestCyclicValuesAreRefused`). Converting one would recurse in Go,
  outside the step budget.
- **A terminate vote drops its batch from the ledger.** When an interceptor
  votes to terminate, `core.CallNested` returns `ErrTerminated` and no
  results. Calls in that batch that ran before the vote are therefore not
  in `calls_completed`. They are in the agent's audit trail.
- **A script that finishes as its deadline passes** can be reported as
  `timeout` or `aborted`: the context watcher may stop the run between the
  script's last step and its result.
