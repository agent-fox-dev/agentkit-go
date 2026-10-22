---
spec_id: "22"
spec_name: "embedder_helpers"
title: "Embedder helpers: core.Named, guard.Allow, faux.ToolCall and faux.Text, and sandbox.Open"
status: "active"
created_at: "2026-10-16T05:44:37.520489Z"
updated_at: "2026-10-16T05:44:37.520489Z"
intent_hash: "cfb324777f92f57720476bfde0b4c65bda1a23f9cde2a57570c5bf0673658417"
schema_version: 2
source: "docs/prd/12-simplify-the-embedder-contract.md"
---
## Intent

Turn the setup blocks every AgentKit scenario repeats into one call each. Those blocks are: picking tools by name, an allowlist guard, a scripted model turn, and opening a sandbox that is filled and has its proxy tools. Each helper is a thin layer over an API that already exists. None adds a concept or changes what the underlying API does. The `codemode`, `sandbox` and `gateway` examples and the README are rewritten on the helpers, so the code a reader copies shows the scenario instead of the wiring.

## Goals

1. `core.Named(ts, names...)` returns the named tools in `names` order. A name that is not in `ts` is an error that names the missing name and lists the names `ts` does hold.
2. For every call, `guard.Allow(programs...)` makes the same decision as `guard.Restricted(guard.Options{AllowedPrograms: programs})`, with the same reason and the same `Terminate` value. Changing the caller's slice after `Allow` returns does not change what it decides.
3. `faux.ToolCall(id, name, args)` and `faux.Text(s)` each return a `faux.Turn` that is `reflect.DeepEqual` to the one-block literal it replaces.
4. `sandbox.Open(ctx, p, spec, src, opts)` on `local` with a `sandbox.LocalDir` returns a live sandbox and the same proxies that `Sandbox.Tools` returns. If any step after `Create` fails, the sandbox is closed before `Open` returns, the provider holds nothing, and the returned error matches the failing step's error under `errors.Is`.
5. In `examples/codemode`, `examples/sandbox` and `examples/gateway`, none of the following remains: a `switch` or map over tool names, a `faux.Turn{…}` literal, a `guard.Restricted(guard.Options{AllowedPrograms: …})` call, or a separate `Create`/`Sync`/`Tools` sequence. The two sandbox examples each call `sandbox.Open` once. All three still pass their offline tests.
6. `make check` passes in every module in `MODULES`. The exported-API golden records `core.Named`, and the guard API pin in TS-17-61 records `guard.Allow`.

## Non-goals

- `agentkit.Toolset`, `agentkit.NewRunner` and `Runner`. Those belong to `toolset_runner`, which this spec builds on.
- The `workspace` package and `tools.For`. Those belong to `workspace_package`, which this spec builds on.
- The nested `mcpserve` module, and rewriting `examples/harness`. Both belong to **`mcp_serve`**, including the harness's own `faux.Turn` literals, its `guard.Restricted` call and its `Create`/`Sync`/`Tools` sequence. `examples/harness` is not touched here.
- The full rewrite of the README consumer-contract table, the data-flow sections of `docs/architecture.md`, settling the exported-API golden across every changed package, and redoing PRD 12's line measurement over the examples. Those belong to **`embedder_contract_docs`**. This spec adds rows and sentences only for the identifiers it introduces.
- Changing `guard.Restricted`, `guard.Options`, `guard.Check`, the `faux` block helpers (`FauxText`, `FauxToolCall`, `FauxThinking`, `FauxAssistantMessage`), `faux.Turn`, or the `sandbox.Provider` and `sandbox.Sandbox` interfaces. `Create`, `Sync` and `Tools` stay, for a second `Sync` or for no `Sync` at all.
- Rewriting `examples/agentdemo`, `examples/codingagent`, `examples/customtools` or `examples/mcp`. `agentdemo` scripts multi-block turns on purpose. `codingagent` needs `Restricted`'s other options.
- Migrating the root module's own tests to the new `faux` helpers.
- A multi-call turn helper (`FauxAssistantMessage` already covers it), a sentinel error for `core.Named`, or a `SyncResult` returned from `sandbox.Open`.

## Background

PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`, decision D5) measured the examples that use a sandbox, code mode or both. Four chores recur in them, and each is sugar waiting to happen:

- **Picking tools by name.** `examples/codemode/main.go` filters `tools.All` with a `switch t.Name` (8 lines). `examples/sandbox/main.go` and `examples/gateway/main.go` build a `byName := map[string]core.Tool{}` over the proxies and index it three times. The README's code-mode section repeats the `switch`.
- **Scripting a turn.** Every offline example writes `faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall(…)}, StopReason: core.StopReasonToolUse}` and `faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(…)}, StopReason: core.StopReasonStop}`. `examples/codemode` defines its own `turn` helper to avoid that. `provider/faux/faux.go` has block-level helpers and `FauxAssistantMessage(reason, blocks...)`, but nothing that returns a whole single-block turn.
- **An allowlist guard.** `guard.Restricted(guard.Options{AllowedPrograms: []string{"ls"}})` appears in the sandbox and gateway examples, their READMEs and the README quickstart. `guard.Check` rebuilds the allowlist from `o.AllowedPrograms` on every call, so `Restricted` keeps a reference to the caller's slice.
- **Opening a sandbox.** Both sandbox examples run `p.Create`, `defer sb.Close`, `sb.Sync(ctx, sandbox.LocalDir{…})` and `sb.Tools(ctx, sandbox.ToolOptions{})`, each with its own error check. If `Sync` or `Tools` fails, the deferred `Close` is the only cleanup. An embedder who writes the sequence without a `defer` leaks the sandbox.

Facts in the code the helpers rely on:

- `Sandbox.Close` is idempotent. Every provider's sandbox forwards it to `internal/attach`, whose `Close` ignores its context.
- A `GitRepo` must be the first `Sync` and come before the first `Tools` call.
- `local`'s `Sync(GitRepo)` fails with `ErrUnsupported`.
- `Tools` with a relative `SpillDir` is refused by the agent. That refusal does not make the sandbox unavailable.

Two existing tests pin what this spec changes:

- `internal/policy/exported_api_17_test.go` (TS-17-61) pins `guard`'s exported declarations to `preSpec17Guard`.
- `internal/policy/layout_test.go` (TS-13-67) pins `core`'s exported API to `testdata/exported-api.golden`.

Two example tests also depend on the current shape of `examples/sandbox`:

- TS-13-68 expects the output line `synced 3 files`.
- TS-14-45 parses `main.go` and requires the `Sync` and `Export` calls each to take one `sandbox.LocalDir` literal.

## Requirements

### R1. `core.Named`

`func Named(ts []Tool, names ...string) ([]Tool, error)` is declared in `core/tool.go`.

- **Selection.** For each entry of `names`, in order, it returns the tool in `ts` whose `Name` equals that entry exactly. It matches only the top-level entries of `ts`; it does not search `ReachableTools`. When `ts` holds two tools with the same name, the first one is returned.
- **Copying.** The result is a new slice of value copies. `ts` is not modified, and the result does not share `ts`'s backing array.
- **No names.** With no names it returns an empty, non-nil slice and a nil error.
- **Missing names.** When one or more names are not in `ts`, it returns nil and an error:
  - the error begins `core: `;
  - it quotes every missing name, in `names` order;
  - it lists every name in `ts`, in `ts` order, or says the set is empty.
  - For example: `core: no tool named "no_such_tool"; the set holds: read_file, write_file`.
- **Repeated names.** When every name is present but one appears more than once in `names`, it returns nil and an error that begins `core: ` and quotes the first repeated name.
- **Concurrency.** `Named` is pure and safe for concurrent use.

### R2. `guard.Allow`

`func Allow(programs ...string) core.BeforeToolCall` is declared in `guard/guard.go`.

- It returns `Restricted(Options{AllowedPrograms: slices.Clone(programs)})`. Every decision it makes (`Block`, `Reason`, `Terminate`) equals the decision that `Restricted` makes for the same context. Reasons therefore keep the `guard.Restricted: ` prefix.
- Mutating the caller's slice after `Allow` returns does not change any decision.
- With no programs, every `execute` and `run_command` call is blocked and every other tool is allowed, as `Restricted` does with an empty allowlist.
- Its doc comment says that it is `Restricted` with only `AllowedPrograms` set, and that `Restricted` is the way to set the other options. It repeats in one sentence that the guard is a floor, not a sandbox.

### R3. `faux.ToolCall` and `faux.Text`

These are declared in `provider/faux/faux.go`:

- `func ToolCall(id, name, args string) Turn` returns `Turn{Blocks: []core.ContentBlock{<the block FauxToolCall(id, name, args) returns>}, StopReason: core.StopReasonToolUse}`.
  - It panics, as `FauxToolCall` does, when `args` is not a JSON object. The panic message names `faux.ToolCall`, the id and the tool name.
  - Empty or whitespace `args` becomes `{}`, as in `core.NewToolUse`.
- `func Text(s string) Turn` returns `Turn{Blocks: []core.ContentBlock{core.TextBlock{Text: s}}, StopReason: core.StopReasonStop}`.
- Every other `Turn` field is zero in both.

A `faux.Provider` scripted with these helpers emits exactly the event sequence it emits for the equivalent literals. `Turn`, `FauxText`, `FauxThinking`, `FauxToolCall` and `FauxAssistantMessage` are unchanged.

### R4. `sandbox.Open`: the happy path

`func Open(ctx context.Context, p Provider, spec Spec, src Source, opts ToolOptions) (Sandbox, []core.Tool, error)` is declared in a new file, `sandbox/open.go`.

- It calls `p.Create(ctx, spec)`. If `src` is non-nil it then calls `Sync(ctx, src)` on the new sandbox, and then `Tools(ctx, opts)`. It returns the sandbox exactly as `Create` returned it (not wrapped, so `Unwrap`-based accessors keep working) together with the proxies.
- The `SyncResult` is discarded. An embedder who needs it calls `Create`, `Sync` and `Tools` directly.
- Because `Sync` runs before `Tools`, a `GitRepo` source satisfies the slot rules on every provider that accepts one.
- `ctx` bounds these three calls only. The sandbox's lifetime belongs to the caller, who closes it.
- After a successful `Open`, `Sync`, `Export` and `Close` behave as they do on any sandbox, and a `Tools` call with equal options returns the same proxies.

### R5. `sandbox.Open`: failures and cleanup

- A nil `p` returns `(nil, nil, err)` with an error beginning `sandbox: `. Nothing is called and nothing panics.
- If `Create` fails, `Open` returns `(nil, nil, err)` where `err` is `Create`'s error unchanged. There is nothing to close.
- If `Sync` or `Tools` fails, `Open` calls `Close` once on the sandbox with `context.WithoutCancel(ctx)`. A cancelled or expired `ctx`, a common cause of the failure, therefore does not stop the cleanup.
  - When `Close` succeeds, `Open` returns `(nil, nil, stepErr)` with the step's error unchanged.
  - When `Close` also fails, it returns `errors.Join(stepErr, closeErr)`.
  - In both cases `errors.Is(err, X)` holds for any `X` the step's error matches (for example `ErrUnsupported`, `ErrToolMismatch` or `ErrUnsafeArchive`).
- After a failed `Open`, the provider holds no sandbox from that call. On `local`, the sandbox's directory under `os.TempDir()/agentkit-sandbox-local/` is gone.
- Two failures are observable on `local` and are tested there:
  - a `sandbox.GitRepo` source fails with `ErrUnsupported`;
  - a relative `ToolOptions.SpillDir` makes `Tools` fail.
- The joined-`Close`-error path and the cancelled-context cleanup are tested with a test double for `Provider` and `Sandbox` in the `sandbox_test` package.

### R6. `examples/codemode` on the helpers

Starting from the names given by `workspace_package` (`tools.For`) and `toolset_runner` (`Config.Toolset`):

- The `switch` over `tools.For`'s result becomes `core.Named(all, "list_files", "find_files", "read_file")`.
- The two scripted `code_mode` calls become `faux.ToolCall(…)`, and the final answer becomes `faux.Text(…)`.
- The local `turn` helper is deleted.

The output is unchanged, and TS-08-49 passes as it is.

### R7. `examples/sandbox` and `examples/gateway` on the helpers

In each example's `runOn`:

- The project files are written to the host directory first.
- `sandbox.Open(ctx, p, spec, sandbox.LocalDir{Path: src}, sandbox.ToolOptions{})` replaces `Create`, `Sync` and `Tools`. `defer sb.Close(ctx)` follows it.
- `core.Named(proxies, "read_file", "search_files", "execute")` replaces the `byName` map. The sandbox example's direct `read_file` call uses the first tool `Named` returns.
- The scripted turns become `faux.ToolCall` and `faux.Text`.
- The guard becomes `guard.Allow("ls")`.

Everything else stays: the `--podman`, `--landlock` and `--url` paths, `Export`, the gateway's `List` and explicit `Close`, and every other output line.

The sandbox example no longer prints `synced N files`. Its tests change to match:

- TS-13-68 drops `synced 3 files` from its expected lines and keeps the rest.
- TS-14-45 requires the `sandbox.Open` call and the `Export` call each to take exactly one `sandbox.LocalDir` literal, and still allows no `Tar` or `TarOut` literal.

The gateway example's `TestExampleRunsOffline` passes unchanged.

Each of the three rewritten examples gains one structural test, which parses its `main.go`. The test fails on any of:

- a `faux.Turn` composite literal;
- a call to `guard.Restricted`;
- a `switch` whose tag is a tool's `.Name`;
- in the two sandbox examples, a `Create`, `Sync` or `Tools` method call, or a number of `sandbox.Open` calls other than one.

### R8. README and documentation

- **`README.md`:**
  - The quickstart's guard becomes `guard.Allow("go", "git")`.
  - The code-mode snippet uses `core.Named`.
  - The sandboxes snippet uses `sandbox.Open` and `guard.Allow("go")`.
  - The sandboxes prose says that `sandbox.Open` does `Create`, `Sync` and `Tools` in one call and closes the sandbox if a step fails.
  - The consumer-contract table gains rows for `core.Named`, `faux.ToolCall`/`faux.Text` and `sandbox.Open`, and the guard row names `guard.Allow`.
- **`docs/configuration.md`:**
  - The `guard.Options` section says that `guard.Allow(programs...)` is `Restricted` with only `AllowedPrograms`.
  - The guard row that names `guard.Restricted(opts)` or `guard.AllowAll` also names `guard.Allow`.
  - The Sandboxes section gains a `sandbox.Open` paragraph covering the order of the calls, `nil` `src`, the discarded `SyncResult`, and the close-on-failure behaviour with its error contract.
- **`docs/architecture.md`:** the Sandboxes section gains one sentence placing `sandbox.Open` over `Create`, `Sync` and `Tools`.
- **`docs/api.md`:** the `guard`, `provider/faux` and `sandbox` rows name the helpers they gain, and the `core` row names `core.Named`.
- **Examples:**
  - `examples/README.md`'s guard advice names `guard.Allow`.
  - The "what it does" and "code an embedder copies" sections of `examples/codemode/README.md`, `examples/sandbox/README.md` and `examples/gateway/README.md` match the rewritten code.
- **Generated docs:** `make doc` is run, and `docs/generated/core.txt`, `guard.txt`, `provider/faux.txt` and `sandbox/sandbox.txt` are committed.

### R9. API pins, budgets and checks

- **Exported-API golden.** `internal/policy/testdata/exported-api.golden` is regenerated with `AGENTKIT_UPDATE_API_GOLDEN=1`, so that it records `func Named(ts []Tool, names ...string) ([]Tool, error)` under `core`, and it is committed. TS-19-64 requires committed testdata.
- **Guard pin.** TS-17-61's guard list gains `func Allow(programs ...string) core.BeforeToolCall`, in sorted position (before `AllowAll`), and keeps checking the rest. `docs/errata/17_sandbox_podman.md` gains an entry. It names PRD 12 and this spec and says the guard pin was extended by `Allow` on purpose.
- **Line budgets.** `core` stays within TS-12-14's budget of 1,600 code lines, and the root module within 20,000.
- **`make check`.** It passes in every module in `MODULES`, including `examples/sandbox` and `examples/gateway`.

## Design Decisions

1. **`Named` lives in `core`, next to `ReachableTools`.** PRD 12 D5 names it `core.Named`. It operates on `[]core.Tool` alone, and every package that builds tools already imports `core`.
2. **A missing name produces one error that quotes every missing name and lists the whole set in `ts` order.** The usual mistake is a typo or a qualified MCP name (`server__tool`), and seeing the set as given fixes it in one step. `ts` order is the order the caller built, which is more useful here than sorted order.
3. **A name repeated in `names` is an error.** `codemode.New` refuses duplicate names, and a model's tool list should not hold one twice. Failing at the pick names the actual mistake.
4. **When `ts` holds a name twice, the first match wins.** It is a deterministic answer to an input none of the repository's tool sources produces. Making it an error would push a check onto every caller for a case that does not occur.
5. **`Named` looks only at top-level tools.** A wrapper's `ReachableTools` are its own, and picking one out of a wrapper would bypass the policy pruning that `ToolPolicy.Resolve` applies to them.
6. **No sentinel error for `Named`.** PRD 12 asks for an error that names the missing name. A new `core` sentinel would add API, and `core` is already over its type budget by decision.
7. **`guard.Allow` is exactly `Restricted` with `AllowedPrograms`, reasons included.** PRD 12 D5 defines it as sugar. A second reason prefix would be a second code path for the same check.
8. **`Allow` clones its arguments.** `Check` rebuilds the allowlist from the slice on every call. Without the clone, a caller who later appended to a reused slice would silently widen a security boundary.
9. **The `faux` turn helpers are named without the `Faux` prefix and return `Turn`, and the block helpers stay.** This follows PRD 12 §4. A turn holding several blocks still needs the block helpers and `FauxAssistantMessage`.
10. **`faux.ToolCall` panics on a non-object `args`, like `FauxToolCall`.** A malformed script is a test bug, and the package already chose to surface that at the call site.
11. **`sandbox.Open` runs `Create`, then `Sync` when `src` is non-nil, then `Tools`, and discards the `SyncResult`.** This is the signature PRD 12 §4 gives. Syncing before `Tools` is what lets a `GitRepo` work. A fourth return value would make the common call longer for a number most callers do not read.
12. **On failure after `Create`, `Open` closes with `context.WithoutCancel(ctx)`, returns nil for both the sandbox and the tools, and returns the step's error unchanged (joined with `Close`'s error only when `Close` fails).** A cancelled context is the most common cause of a failed `Sync` and must not prevent cleanup. Today's providers ignore `Close`'s context, but `Provider` is an interface others implement. Returning the step's own error keeps `errors.Is` and the existing messages, which already name their step (`sandbox: LocalDir …`, `sandbox: agent refused Tools: …`).
13. **A nil `Provider` is an error, not a panic.** `Open` is the entry point a new embedder calls first, and a `sandbox: ` error explains more than a nil-pointer trace.
14. **`Open` returns the sandbox unwrapped.** The gateway and `attach.Live` follow `Unwrap` chains from what `Create` returned, and a wrapper would add a layer for no behaviour.
15. **Only the `codemode`, `sandbox` and `gateway` examples are rewritten. `harness` is left to `mcp_serve`.** The split assigns them this way, and `mcp_serve` rewrites `examples/harness` wholesale. Editing it here would be overwritten.
16. **The sandbox example drops its `synced N files` line, and TS-13-68 and TS-14-45 are updated rather than kept.** With `Open`, the example no longer holds a `SyncResult`. Keeping the line would mean keeping the separate `Sync` call the rewrite removes.
17. **The examples' acceptance is checked structurally, by parsing `main.go`, not by counting lines.** A structural test is stable and follows TS-14-45's precedent. The line measurement is assigned to `embedder_contract_docs`.
18. **The exported-API golden is regenerated in this spec.** `core` is in `apiPackages`, so `make check` cannot pass otherwise. `embedder_contract_docs` settles the golden across all changed packages.
19. **TS-17-61's guard pin is extended rather than retired, and the change is recorded in spec 17's erratum.** The pin still catches an accidental change to `guard`. The erratum for the spec that owns the test is where the deliberate change belongs.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `20_workspace_package` | depends on | The examples and the README are rewritten on top of `workspace.Open` and `tools.For`, and `core.Named` is applied to `tools.For`'s result. Spec 20 also adds `workspace` to `apiPackages` and retires the golden hash pins this spec relies on. |
| `21_toolset_runner` | depends on | The rewritten examples and the README put `guard.Allow` and the picked tools into `agentkit.Config{Toolset: agentkit.Toolset{…}}`. The golden this spec regenerates already contains spec 21's `Toolset`, `NewRunner` and `Runner`. |
| `08_code_mode` | modifies | `examples/codemode` is rewritten, and TS-08-49 must still pass with unchanged output. |
| `12_core_schema_guard` | modifies | `core` gains `Named` and `guard` gains `Allow`. TS-12-14's `core` and root-module line budgets must still hold. |
| `13_sandbox_core` | modifies | `sandbox` gains `Open`. TS-13-68's expected example output drops `synced 3 files`. TS-13-67's golden is regenerated for `core.Named`. |
| `14_sandbox_rsync_sync` | modifies | TS-14-45's check of the sandbox example's AST moves from `Sync` to `sandbox.Open`. |
| `17_sandbox_podman` | modifies | TS-17-61's guard API pin gains `Allow`, and `docs/errata/17_sandbox_podman.md` records why. |
| `19_sandbox_gateway` | modifies | `examples/gateway` is rewritten on `sandbox.Open`. TS-19-64 requires the regenerated golden to be committed. |
