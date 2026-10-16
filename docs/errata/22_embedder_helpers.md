# Erratum: spec 22 `embedder_helpers`

These are the places the delivered code, its tests and the order of the work
differ from the spec's text, and why.

## The gateway example no longer prints `synced N files` (22-REQ-7.2)

22-REQ-7.2 says the gateway example prints "the same lines as before the
rewrite". Before the rewrite it also printed `synced 3 files, 187 bytes`, from
the `SyncResult` of its own `Sync` call. 22-REQ-7.3 forbids a `Sync` method
call in the example, and `sandbox.Open` discards the `SyncResult` (R4), so
that line cannot survive. The sandbox example drops the same line on purpose
(22-REQ-7.1, decision 16).

The gateway example calls `sandbox.Open` once (`examples/gateway/main.go:212`)
and prints every other line it printed before. `TestExampleRunsOffline`
(`examples/gateway/main_test.go:15`) passes unmodified, because it never
expected the line, and the smoke test `TestSmokeGatewayOpenListsEmptyAfterClose_TS22_38`
checks the lines 22-REQ-7.2 lists.

## TS-22-14 compares the events with the message timestamps zeroed

TS-22-14 says the events two providers emit are `reflect.DeepEqual`. A faux
provider stamps `Timestamp: time.Now()` on the message in its
`MessageStartEvent`, `MessageUpdateEvent` and `MessageEndEvent`
(`provider/faux/faux.go:184`), so two providers scripted alike never produce
equal events as collected. `collect` in `provider/faux/faux_test.go:93` zeroes
those three timestamps and compares everything else, which is the sequence
22-REQ-3.5 is about. `TestProviderEmitsTheSameEvents_TS22_14` also requires at
least six items per turn, so it cannot pass on two empty streams, and
replacing the stop reason in `faux.ToolCall` turns it red.

## The golden and the guard pin land with the helpers, not in task 6

Tasks 1 and 2 add `core.Named` and `guard.Allow`; task 6 owns the exported-API
golden (TS-22-32) and the TS-17-61 guard pin (TS-22-33). TS-13-67 and TS-17-61
run under `-short`, so `make check` fails from the moment `Named` or `Allow`
exists. The golden is regenerated in task 1 (`internal/policy/testdata/exported-api.golden`)
and the pin gains `Allow` in task 2 (`internal/policy/exported_api_17_test.go:15`),
and task 6 keeps the erratum for spec 17, the budget check and the
verification. TS-22-32, TS-22-33 and TS-22-34 therefore passed when task 6
began; the commit declares it as a test-first deviation. The tests are
`TestSandboxLayoutAndAPI_TS13_67`, `TestPolicyTestdataIsCommitted_TS20_62`,
`TestExportedAPIUnchanged_TS17_61` and `TestLineBudgets_TS12_14`.

## The gate is `make check`, not `make lint` and `make test`

The spec's `test_commands` and every task's `done_when` name `make lint` and
`make test`. `.specs/steering.md` (Quality Gate, rule 3) says a spec's test
command does not override the rule that agents run `make check` while
implementing. Each task was gated with `make check` (fmt, vet, lint and the
short tests in every module) plus the narrow `go test` runs its `done_when`
names, including `go test -race` for `core` and `sandbox`. The full `make test`
and `make check-release` were left to the user.

## Tasks 6 and 11 passed their tests on the first run

Task 6's tests are covered above. Task 11 is the integration task: tasks 1 to
10 had built the paths its four smoke tests exercise. To show the tests detect
a wiring gap, four mutations were applied one at a time, and each turned the
intended test red: `Open` skipping its cleanup after a failed `Sync`
(TS-22-37), the sandbox example passing `guard.AllowAll` in place of
`guard.Allow("ls")` (TS-22-36), `core.Named` dropping `find_files` in the
codemode example (TS-22-35), and the gateway example not closing its sandbox
before listing (TS-22-38).
