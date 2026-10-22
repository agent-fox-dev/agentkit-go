---
spec_id: "24"
spec_name: "embedder_contract_docs"
title: "Embedder contract, closing pass: the API golden settled, the contract and data flow documented end to end, and PRD 12's measurement redone"
status: "active"
created_at: "2026-10-16T06:27:40.47366Z"
updated_at: "2026-10-16T06:27:40.47366Z"
intent_hash: "fe35ce7b5eb7a05c0013688c1b60bcd5b9f9df47f688b7f357bb923b703da3f4"
schema_version: 2
source: "docs/prd/12-simplify-the-embedder-contract.md"
---
## Intent

Close PRD 12's embedder-contract work by making the record match the code. The exported-API change detector must cover every package whose public surface the work changed or added. The erratum that retired the API freeze must say what replaced it. The README's consumer contract and the data-flow sections of the architecture document must describe the contract as built: workspace, tools, toolset, runner, agent and MCP serving. PRD 12's before/after measurement of the examples is taken again by a stated rule and recorded. This spec changes no exported identifier and no behaviour.

## Goals

1. `apiPackages` in `internal/policy/layout_test.go` is exactly `".", "core", "tools", "codemode", "workspace", "guard", "provider/faux", "sandbox", "codesearch", "mcpserve"`, in that order. The committed `internal/policy/testdata/exported-api.golden` has one section for each entry, in that order. TS-13-67, TS-14-44, TS-15-42 and TS-17-61 pass with `AGENTKIT_UPDATE_API_GOLDEN` unset. The five sections that already exist are byte-identical to what spec 23 left.
2. The API-freeze entry in `docs/errata/13_sandbox_core.md` covers four things: every package the golden records, the specs that regenerated it, how the four golden tests relate to each other, and the rule for changing it from now on.
3. The README's `## Consumer contract` has a row for every identifier PRD 12 added or reshaped, and the shape in each row matches the declaration in the code. No row names an identifier that PRD 12 removed. The rows follow the order in which an embedder builds a scenario.
4. `docs/architecture.md` covers the embedder's path, one call through a `Runner`, one run and one served call. The package-graph table matches `go list` output for every module that has public packages. No sentence says that code mode fails outside an agent run.
5. No current document or Go doc comment names an identifier PRD 12 removed, except as the old name in a migration note.
6. `docs/errata/24_embedder_contract_docs.md` records two things. The first is PRD 12's §Intent measurement taken again by a stated rule, over the baseline tree and over the current tree. The second is each of PRD 12's twelve acceptance criteria, marked as met or with how the delivery differs.
7. `make check` passes in every module listed in `MODULES`. `make doc` output is committed.

## Non-goals

- Changing any exported identifier, signature or behaviour. The only Go changes in this spec are in `internal/policy` (the package list, a new test, doc comments) and doc comments elsewhere.
- Rewriting any example or its tests, even where the measurement finds that a bound was missed. A missed bound is recorded, not fixed.
- Tests of the presence or content of any document. The steering forbids them. Only the golden coverage gets a test.
- Adding to the golden the packages PRD 12 did not change: `schema`, `catalog`, `mcp`, `prompt`, `outline`, `wire`, `provider`, `provider/anthropic`, `sandbox/local`, `sandbox/podman`, `sandbox/landlock`, `sandbox/gateway`, `sandbox/gatewayd`.
- Changing how `exportedAPI` prints declarations (receiver names, ordering, build tags).
- Removing or merging TS-14-44, TS-15-42 or TS-17-61, or TS-17-61's separate `preSpec17Guard` list.
- Editing `docs/prd/*` (PRD 12 included), `docs/archive/*`, or any erratum entry other than the API-freeze entry in `docs/errata/13_sandbox_core.md`. The new file `docs/errata/24_embedder_contract_docs.md` is written from scratch.
- Restructuring `docs/configuration.md`, `docs/cli.md`, or the Sandboxes section of `docs/architecture.md` beyond the stale-identifier sweep. Rewriting the README's quickstart, code-mode, sandbox or gateway snippets, which earlier specs already moved to the new API.
- Anything the earlier specs of this split deliver:
  - the `workspace` package, `tools.For` and `tools.References` (**`20_workspace_package`**);
  - `Toolset`, `NewRunner`, `Runner` and the new `Config` (**`21_toolset_runner`**);
  - `core.Named`, `guard.Allow`, `faux.ToolCall`, `faux.Text` and `sandbox.Open`, and the example rewrites on them (**`22_embedder_helpers`**);
  - the `mcpserve` module and the rewrite of `examples/harness` (**`23_mcp_serve`**).

## Background

PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`) was split into five specs. Specs 20 to 23 each change part of the contract. Each updates the documentation only for the identifiers it touches, and each regenerates the golden only as far as `make check` forces. That leaves four things undone, and this spec does them.

**The golden.**
- `internal/policy/layout_test.go` declares `apiPackages = []string{".", "core", "tools", "codemode"}`. Spec 20 appends `"workspace"`.
- `exportedAPI(t, dir)` parses every non-test `.go` file in a directory, whatever its build tags. It prints functions with their receivers (names included), types with only their exported fields, and constants and variables with their types, sorted and deduplicated by text.
- The golden is a series of sections, each a `== <pkg>` header followed by that package's lines. Today it has sections for `.`, `core`, `tools` and `codemode`, at lines 1, 18, 289 and 394.
- TS-14-44 (`exported_api_14_test.go`), TS-15-42 and TS-17-61 compare the same golden over `apiPackages`. Spec 20 removes their `goldenSHA13` hash pins. TS-17-61 also pins `guard` separately through `preSpec17Guard`, and spec 22 adds `Allow` to that list.
- TS-19-64 (`gateway_unchanged_test.go`) fails when `internal/policy/testdata` differs from HEAD, so a regenerated golden must be committed.
- PRD 12 changed the exported API of five packages the golden does not cover:
  - `guard`: `Allow`;
  - `provider/faux`: `ToolCall` and `Text`;
  - `sandbox`: `Open`, and the type of `LocalDir.Ignore`;
  - `codesearch`: `New` takes a `*workspace.Workspace`, and `Options.Ignore` is gone;
  - `mcpserve`: a new module.
- `codesearch/stub_windows.go` (`//go:build windows`) declares its own `Options`, `New` and `Index` methods, with unnamed receivers.

**The erratum.** `docs/errata/13_sandbox_core.md` has three entries today. Spec 20 adds a fourth, which retires the frozen exported-API invariant and keeps the golden as a change detector. Specs 21 and 22 regenerate the golden again, and they record their own changes in `docs/errata/11_minimal_driver.md` and `docs/errata/17_sandbox_podman.md`. No single place says what the golden covers or how it may change.

**The README.** `## Consumer contract` (README.md around line 202) is a flat table. It still lists, among others:
- `agentkit.Config` with fifteen fields;
- `tools.All`;
- the `Agent` methods without `Runner`.

Specs 20 to 22 replace or add rows one at a time. The README's opening paragraph describes "one driver: `agentkit.Config` and `agentkit.New`". The quickstart comment says `make check` covers the "root and codesearch modules", but it runs every module in `MODULES`. The Documentation index row for `docs/architecture.md` names a `Workspace.References` section.

**The architecture document.** `docs/architecture.md` has these sections: Shape, Package graph, Packages, Data flow of one run, Invariants, Nested tool calls, Code mode, MCP client, Symbol and reference navigation, Sandboxes and Testing layout.
- "Data flow of one run" describes `agentkit.New`, the loop, `batch.go` and `nested.go` as the driver's own.
- "Nested tool calls" says `executeBatch` attaches the nested caller.
- "Code mode" says that calling code mode outside an agent run fails with `ErrNoNestedCaller`, and that `examples/harness` implements `core.NestedCaller` itself.
- The package graph says "Nothing imports the root package except `examples/`". That stops being true once `mcpserve` imports it.
- Specs 21 and 23 patch the sentences that name their identifiers. None of them restructures these sections around the toolset/runner layering, and none describes a call that runs outside a run.

**The measurement.** PRD 12's §Intent table counts the lines of five recurring chores in four examples (`codemode`, `sandbox`, `gateway`, `harness`), but it states no counting rule. Acceptance criterion 10 asks for "at most one line for each of the first four chores and zero for the fifth". Under any consistent rule that bound cannot be met literally: gofmt splits composite literals across lines, and a call that can fail carries an error check. PRD 12's Goal 4 states the intent as "one call each". Specs 22 and 23 already enforce the code-level patterns with structural tests: no `faux.Turn` literal, no `switch` over `.Name`, no `Create`/`Sync`/`Tools` sequence, no `server.go`. What is missing is the measurement itself and a record of it.

**Where records go.** The project does not edit a PRD after it is implemented. PRD 11 still reads "draft … Nothing here is implemented" after specs 13 to 19 implemented it, and spec 20 (design decision 19) leaves `docs/prd/*` untouched. Divergences from a spec are recorded in `docs/errata/NN_<spec_name>.md` (AGENTS.md, "Documentation").

The code in the tree today predates spec 20. This spec is written against the names specs 20 to 23 leave.

## Requirements

### R1. The golden covers every package PRD 12 changed or added

`apiPackages` becomes `".", "core", "tools", "codemode", "workspace", "guard", "provider/faux", "sandbox", "codesearch", "mcpserve"`, in that order. The new entries come after the existing ones, so the existing sections do not move.

- **Regeneration.** The golden is regenerated with `AGENTKIT_UPDATE_API_GOLDEN=1 go test ./internal/policy -run TS13_67` and committed. The diff contains only the five new sections. If an existing section changes, the delivered code differs from what spec 23 left. That is reported as a defect in the earlier spec, not absorbed into this one.
- **What is recorded.** `exportedAPI` is unchanged. `codesearch`'s Windows-stub declarations therefore appear in its section beside the real ones, exactly as they print.
- **Missing directories.** A directory listed in `apiPackages` that does not exist (for example `mcpserve`, if spec 23 had not landed) fails TS-13-67 with an error that names the path. The test does not skip.
- **New test TS-24-1.** It asserts that `apiPackages` equals the list above, element by element.
- **New test TS-24-2.** It asserts that `exportedAPI` returns a non-empty list for each new entry, containing at least:
  - for `guard`, a line beginning `func Allow(`;
  - for `provider/faux`, lines beginning `func ToolCall(` and `func Text(`;
  - for `sandbox`, a line beginning `func Open(`;
  - for `codesearch`, a `func New(` line whose parameters include `*workspace.Workspace`;
  - for `mcpserve`, a line beginning `func New(` and a line beginning `type Endpoint`.
- **Doc comments.** Several doc comments still describe the golden as frozen at spec 13's hash. They must say instead that it is the change detector over `apiPackages`:
  - the comment on `apiPackages`, which today reads "whose exported surface spec 13 must not change";
  - TS-13-67's comment;
  - the comments of TS-14-44, TS-15-42 and TS-17-61.

### R2. The API-freeze erratum is completed

The entry spec 20 added to `docs/errata/13_sandbox_core.md` is extended in place, so the retirement reads as one record. The extended entry states:

- the packages the golden covers, and why each was added: it is in the original contract, or PRD 12 changed or added its exported API. It also says which spec added each one (`workspace` by spec 20; `guard`, `provider/faux`, `sandbox`, `codesearch` and `mcpserve` by spec 24);
- the specs that regenerated the golden (20, 21, 22 and 24) and what each one added;
- that TS-14-44, TS-15-42 and TS-17-61 compare the same golden over the same list, that spec 20 retired their hash pins, and that TS-17-61 keeps its separate `guard` list;
- the rule from now on: a change to the exported API of any package in `apiPackages` regenerates the golden with `AGENTKIT_UPDATE_API_GOLDEN=1` in the same change and commits it (TS-19-64 requires this), and the spec or change that makes it says why in its own record;
- that `codesearch`'s section lists the Windows stub's declarations beside the real ones, and that this is expected.

No other entry in that file changes.

### R3. The README's consumer contract, end to end

The section keeps its heading, `## Consumer contract`.

**Lead.** It opens with one short paragraph naming the layers: a workspace, the tools built for it, a toolset, the runner over it, and its two consumers (the agent loop and `mcpserve`).

**Row order.** The rows follow the path an embedder takes:
1. workspace;
2. tools and shaping the tool set;
3. guard;
4. toolset and runner;
5. agent;
6. serving;
7. sandboxes;
8. model and provider;
9. vocabulary;
10. test doubles.

Sub-tables under `###` headings are allowed.

**Required rows.** A row may group closely related identifiers. Rows are required for:
- `workspace.Open`, `workspace.Options`, `workspace.NoGlobalExcludes`, and the `*workspace.Workspace` methods `Root`, `Resolve`, `Rel`, `CheckWriteTarget`, `Ignore`, `Walk` and `Ignored`;
- `tools.For`, `tools.Options` and `tools.References`;
- `core.Named`, `codemode.New` and `mcp`'s pool functions;
- `guard.Allow`, `guard.Restricted`, `guard.AllowAll` and `guard.Check`;
- `agentkit.Toolset` (its five fields), `agentkit.NewRunner`, and `Runner.Tools`, `Runner.Reachable`, `Runner.Call` and `Runner.Batch`;
- `agentkit.Config` (its twelve fields), `agentkit.New`, `Agent.Run`, `Agent.Stream`, `Agent.Messages`/`Usage`/`ReachableTools`/`Runner`, and `agentkit.PruneOptions`;
- `mcpserve.New`, `mcpserve.Options`, `Server.ServeStdio`, `Server.ServeLoopback`, and `mcpserve.Endpoint` (`URL`, `Token`, `Close`, `ClaudeConfig`, `PiConfig`);
- `sandbox.Open`, `sandbox.Provider`, `sandbox.Sandbox`, `sandbox.CheckSpec`, `sandbox.LocalDir` and `local.Unisolated`;
- `anthropic.Resolve`, `catalog.Lookup` and `core.ProviderClient`;
- `core.Tool`, `core.ToolResult`, `core.BeforeToolCall`, `core.AfterToolCall`, `core.RunResult`, `core.RunStopReason`, `schema.Parse` and `schema.Validate`;
- `faux.New`, `faux.ToolCall` and `faux.Text`.

**Accuracy.** Every shape matches the declaration in the code as delivered. Check it against `docs/generated/` after `make doc`. No row names an identifier PRD 12 removed.

**Elsewhere in the README.** In the same pass:
- the opening paragraph describes the driver as the loop over a runner, and says which modules listen on a socket (`sandbox/gatewayd` and `mcpserve`; the root and sandbox modules do not);
- the quickstart's `make check` comment says it covers every module in `MODULES`;
- the Documentation index row for `docs/architecture.md` lists that document's sections as they are after R4.

### R4. The data-flow sections of `docs/architecture.md`, end to end

Rewrite these sections from the code as delivered, keeping the existing headings so that existing links still resolve:

- **"The embedder's path"** (new, placed before the data-flow sections). A block that shows what is built from what:
  - `workspace.Open`;
  - `tools.For`;
  - shaping the set: `core.Named`, `codemode.New`, `mcp` `Pool.Tools`, `sandbox.Open`'s proxies;
  - `agentkit.Toolset`;
  - `agentkit.NewRunner`;
  - the three uses of a runner: `Runner.Call`/`Batch` with no model; `agentkit.New`, whose loop runs over the runner, with `Agent.Runner`; and `mcpserve.New`.

  Each line has a few words on what that layer owns.
- **"Data flow of one call"** (new). Covers:
  - `Runner.Call` as a batch of one;
  - the prepare phase in call order: start event, lookup, argument preparation, `Guard`;
  - the abort decision, made once;
  - handlers running concurrently unless a `Sequential` tool is among the calls, with a nested caller for any tool that declares `ReachableTools`;
  - finalization under a batch-scoped mutex: `After`, then the end and result events;
  - one `core.ToolResult` per call, in call order, with `Terminate` carrying the final vote;
  - where events go: `Toolset.Events` outside a run, the run's stream inside one;
  - what the runner does not own: transcript, usage, turn count, run slot, stop reason.
- **"Data flow of one run"** (rewritten, heading kept):
  - `agentkit.New` checks the model side and then calls `NewRunner(cfg.Toolset)`;
  - step 4 hands the turn's calls to the runner's batch implementation, with the run's stream as the event sink;
  - the `max_tokens` path stays in the loop;
  - the observation paragraph names `Toolset.Guard` and `Toolset.After` as the only interception.
- **The served call.** Its data flow appears once: transport, the bearer check for HTTP, `tools/list` from `Runner.Tools`, `tools/call` to `Runner.Call`, and the result mapping. If spec 23 already describes it in an MCP serving section, that section is brought in line with this rule and linked from the data-flow sections, not copied.
- **"Invariants".** States that the pipeline exists once (the runner's), that calls through a runner are outside every run, and that `Guard` and `After` belong to the toolset.
- **"Nested tool calls".** States that the runner attaches the nested caller, whether the call comes from a run, `Runner.Call` or `Runner.Batch`. `core.ErrNoNestedCaller` is the answer only when neither a runner nor an agent invoked the tool.
- **"Code mode".** States that code mode runs outside an agent through `Runner.Call`. It no longer tells an embedder to implement `core.NestedCaller`.
- **"Package graph".** The table is regenerated from `go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./...` (tests excluded, first-party imports only) in the root, `codesearch`, `sandbox`, `sandbox/gatewayd` and `mcpserve` modules. The "What follows from it" bullets are re-derived from the new table. For example, the root package is now imported by `mcpserve` as well as by `examples/`, and the root still imports neither `tools` nor `workspace` in non-test code.
- **"Testing layout".** Says that `internal/policy` also holds the exported-API golden over `apiPackages`.

### R5. No current document names a removed identifier

**Where to look.**
- `README.md`, `AGENTS.md` (its Repository Layout, which also gains `workspace/` and `mcpserve/`), and `docs/api.md`, `docs/architecture.md`, `docs/configuration.md`, `docs/cli.md`, `docs/DEPS.md` and `docs/releasing.md`;
- every `README.md` under `examples/`, and `codesearch/README.md`;
- the doc comments of non-test Go files in every module.

**What must not appear.**
- The `tools` identifiers: `tools.All`, `tools.NewWorkspace`, `tools.Workspace`, `tools.Walk`, `tools.WalkOptions`, `tools.IgnoreOptions`, `tools.NoGlobalExcludes`, `tools.Normalize`, `tools.ErrPathNotAllowed`, `tools.ErrPathMalformed`, `tools.ErrNoHome`, `tools.Options.Workspace`, `tools.Options.Ignore`, and `Workspace.References` as a method.
- `ws.Root` used as a field.
- `codesearch.Options.Ignore`.
- `Config.Tools`, `Config.Policy`, `Config.Guard`, `Config.After` and `executeBatch`.
- `examples/harness/server.go`.
- Any claim that the repository as a whole has no MCP server or listens on no socket.

**Exceptions.** A hit is allowed only when it names the old identifier as the old name in a migration note. `docs/prd/`, `docs/archive/`, `docs/errata/` entries other than R2's, `.specs/` and `docs/generated/` are out of scope. `docs/generated/` is regenerated by R8.

This requirement is checked by search during review, not by a test.

### R6. How the measurement is taken

**Files.** The measurement covers each example's non-test Go files:
- `examples/codemode/main.go`;
- `examples/sandbox/main.go`;
- `examples/gateway/main.go`;
- every non-test `.go` file in `examples/harness`.

**Counting rule.**
- A counted line is a non-blank, non-comment line as gofmt formats it.
- A line counts toward a chore when it belongs to a statement or declaration that does that chore (a local helper function included), or to the `if err != nil { … }` that immediately checks that statement's error.
- A line inside a statement counts toward the innermost chore it serves. For example, `faux.ToolCall` lines inside an `agentkit.New` call count as chore 2, and the remaining lines of that call count as chore 4.

**Chores.** These are PRD 12's five:
1. Picking tools by name: a `switch` over `.Name`, a map keyed by tool name and its fill loop, or `core.Named`.
2. Building the scripted model turns and their arguments: `faux.Turn` literals, local turn helpers, `faux.ToolCall`/`faux.Text`, and the marshalling of the script into the call's arguments.
3. The sandbox lifecycle: `Create`, `Sync`, `Tools`, `sandbox.Open`, `Export` and `Close`.
4. The guard together with the `agentkit.New` or `agentkit.NewRunner` statement and its `Config`/`Toolset` literal.
5. Running a tool call outside the loop: any implementation of a pipeline step (`core.NewToolUse`, `core.PrepareArguments`, invoking the guard or a handler, recovering a handler panic, `core.WithNestedCaller`, or a `core.NestedCaller` implementation).

**What is reported.** For each chore and example, two numbers: the counted lines, and the number of statements that do the chore, error checks excluded.

**Baseline.** The baseline is the tree at the commit that added PRD 12 (`git log --diff-filter=A --format=%H -- docs/prd/12-simplify-the-embedder-contract.md`), measured by the same rule. PRD 12's published numbers are kept beside the remeasured ones. If git history is not available, only the published numbers are used, and the record says so.

**Totals.** The total counted lines of each example's non-test Go files are reported for both trees.

The measurement is done by reading the code. It is not automated as a test.

### R7. The record: `docs/errata/24_embedder_contract_docs.md`

A new erratum, in the format of the existing ones (title `# Erratum: spec 24 \`embedder_contract_docs\``, one intro line, then `##` entries), containing:

- **"PRD 12 §Intent, measured again".** The counting rule from R6 and the baseline commit hash. Then a table with one row per chore and, for each example, the published number, the remeasured baseline, the current lines and the current statement count. Then the totals per example, before and after.
- **"PRD 12's acceptance criteria as delivered".** One line for each of criteria 1 to 12, marked met or naming how the delivery differs and which spec and erratum explain it. In particular:
  - Criterion 10 is assessed per statement (Design Decision 9), and any chore above its bound is named with its count.
  - Criterion 12 was not met as written: the golden was regenerated in specs 20, 21, 22 and 24, not once in 24.
- **Divergences from this PRD.** Any place where this spec's own delivery differs from this PRD.

### R8. Generated docs and checks

- `make doc` is run and `docs/generated/` is committed. The doc comments touched by R1 and R5 change its output.
- `make check` passes in every module in `MODULES`.
- The golden, the erratum and the new record are committed.
- No exported identifier or behaviour changes. The golden's existing sections being unchanged is the evidence for this.

## Design Decisions

1. **The golden gains `guard`, `provider/faux`, `sandbox`, `codesearch` and `mcpserve`, appended after the existing sections.** These are the packages whose exported API PRD 12 changed or added and that the golden did not cover. Appending keeps the existing sections where they are, so the diff contains only additions.
2. **Nested-module packages are read by the root module's policy test straight from their directories.** `exportedAPI` parses files and needs no build. `internal/policy` already reads nested modules (`nolisten_test.go`, `gatewayd_deps_test.go`, `crosstarget_test.go`).
3. **`exportedAPI` is not changed, so `codesearch`'s Windows-stub declarations are recorded beside the real ones.** Normalizing receiver names would rewrite every existing line of the golden. The doubled lines also make drift between the stub and the real index visible.
4. **Packages PRD 12 did not change stay out of the golden.** The scope is the packages PRD 12 changed. Widening the change detector further is a separate decision with its own cost to every later change.
5. **TS-14-44, TS-15-42 and TS-17-61 stay, as does TS-17-61's guard list. Only their doc comments change.** They loop over `apiPackages` and widen automatically. Removing other specs' tests would need errata of its own and gain nothing.
6. **The API-freeze erratum entry that spec 20 wrote is completed in place, not followed by a second entry.** The retirement and what replaced it read as one record.
7. **The measurement and the acceptance record go in a new erratum, `docs/errata/24_embedder_contract_docs.md`, and PRD 12 is not edited.** This project does not edit PRDs after implementation (PRD 11, and spec 20's decision 19). The criterion-10 reading and the repeated golden regeneration are divergences from PRD 12, which is what errata record.
8. **The measurement uses an explicit counting rule, and the baseline is remeasured by the same rule on the tree that introduced PRD 12.** PRD 12's numbers were taken with no stated rule, so only a like-for-like count shows whether an example got shorter. The published numbers stay beside the remeasured ones for reference.
9. **Acceptance criterion 10 is assessed per statement, while line counts are reported as measured.** gofmt and error checks make a literal "one line" impossible for any call that can fail, and PRD 12's Goal 4 states the intent as "one call each".
10. **This spec changes no example. A missed bound is recorded, not fixed.** The examples belong to specs 21 to 23, and a closing pass that rewrote code would itself need measuring again.
11. **Only the golden coverage is tested (TS-24-1, TS-24-2). The documentation requirements are checked in review.** The steering forbids tests of document content. The code-level example patterns are already enforced by the structural tests of specs 22 and 23.
12. **The `## Consumer contract` and `## Data flow of one run` headings are kept, and the new sections are added beside them.** Existing links, and readers who know the document, keep working.
13. **The package-graph table is regenerated from `go list` in every module that has public packages, `mcpserve` included.** `go list` is the source the table already names. Patching the table row by row is how "Nothing imports the root package except `examples/`" became stale.
14. **`docs/configuration.md` gets the stale-identifier sweep only.** Specs 20 to 23 each wrote their own sections of it, and the split assigns this spec the README contract and the architecture data flow, not a configuration rewrite.
15. **The served-call data flow is described once.** If spec 23 wrote an MCP serving section, it is aligned and linked, not duplicated. Two descriptions of one path drift apart.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `20_workspace_package` | depends on | Adds `workspace` to `apiPackages`, retires the hash pins and writes the erratum entry that R2 completes. The contract table and the embedder's path use `workspace.Open` and `tools.For`. |
| `21_toolset_runner` | depends on | `Toolset`, `NewRunner`, `Runner`, the new `Config` and `Agent.Runner` are what the data-flow sections and the contract table describe. Its golden regeneration and its spec 11 erratum are recorded in R2 and R7. |
| `22_embedder_helpers` | depends on | `guard.Allow`, `faux.ToolCall`/`Text` and `sandbox.Open` are the reason three of the new golden sections exist. The `codemode`, `sandbox` and `gateway` examples it rewrote are measured in R6. |
| `23_mcp_serve` | depends on | The `mcpserve` package must exist for its golden section and for its rows and served-call flow in the docs. The `harness` example it rewrote is measured in R6. |
| `13_sandbox_core` | modifies | TS-13-67's `apiPackages` and `exported-api.golden` grow by five sections. Its erratum's API-freeze entry is completed. TS-19-64 requires the golden to be committed. |
| `14_sandbox_rsync_sync` | modifies | TS-14-44 compares against the widened golden, and its doc comment changes. |
| `15_sandbox_landlock_agent` | modifies | TS-15-42 compares against the widened golden, and its doc comment changes. |
| `17_sandbox_podman` | modifies | TS-17-61 compares against the widened golden, and its doc comment changes. Its `preSpec17Guard` list is unchanged. |
