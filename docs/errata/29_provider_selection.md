# Erratum: spec 29 `provider_selection`

The places where the delivered code and its tests differ from the text of
`.specs/29_provider_selection`, and why. PRD 14 and the spec are not edited. In
each case the test checks what the code delivers. Sections are added by the
task that delivers the behaviour they describe.

## Three tests that pinned the old `Config` are restated, and the spec lists two of them (29-REQ-1, 29-REQ-2; task 2)

29-REQ-8 names TS-11-1, TS-11-2, TS-11-5, TS-11-9 and TS-21-13. Three more tests
fail on the new `Config` and are restated the same way:

- **TS-21-2** (`TestConfigHasFourteenFieldsAndNoToolFields_TS21_2`,
  `runner_config_test.go:55`) listed the twelve fields of `Config` in order. It
  lists the fourteen of 29-REQ-1.1.
- **TS-26-35** (`TestNewProviderModelsStayInCatalog_TS26_35`,
  `repository_cut_test.go:466`) required `agent.go` to contain
  `catalog.Lookup(cfg.Model)`. The model is resolved by
  `catalog.Select(cfg.Provider, cfg.Model, cfg.Effort)` in `resolve.go:55`, which
  reaches `Lookup` for an Anthropic id, so the test now requires that call in
  `resolve.go` and no `catalog.Lookup` in `agent.go`. `repository_cut_test.go`
  is in no task's `touches`.
- **TS-09-11** (`TestProviderDefaults_TS09_11`, `repository_cut_test.go:57`)
  needs no change but constrains the code: it fails when `agent.go` mentions
  "openai", "google" or "ollama" (`repository_cut_test.go:79`). All provider
  names, constructors and wording therefore live in `resolve.go`, and the doc
  comments of `Config` in `agent.go` point to `catalog.Providers`.

## `ollama.New` returns `*ollama.Client` (29-REQ-3.5; task 2)

The spec's External APIs table, marked unverified, has
`func New(m core.Model, opts Options) (core.ProviderClient, error)`. The package
(`provider/ollama/client.go:78`) returns `(*Client, error)`. `buildClient`
(`resolve.go:77`) assigns the client to the interface only after the error
check, so a failed construction never yields a non-nil interface holding a nil
pointer.

## The exported-API golden and the generated docs are regenerated with the API (29-REQ-9; tasks 1 and 2)

The spec regenerates both in task 8. An exported field or function turns
`make check` red until they are (the golden test and the generated-docs test
both run under `-short`), so the commit that adds the API regenerates them: task
1 for `Usage.Unpriced` and `UsageJSON`, task 2 for `Config`, `Resolve`,
`Resolution` and `Agent.Resolution`. Each golden diff holds only those lines.

## `core` is at its line budget (29-REQ-7; task 1)

`TestLineBudgets_TS12_14` (`core/size_test.go:188`) holds `core` to 1,600 lines
of code. `Unpriced` and its use in `Add` and `usageJSON` add three, one over the
limit, so `Add` combines the presence mask and the flag in one assignment
(`core/usage.go:87`). `core` has no lines left.

## The sandbox module's graph grows by the root's new imports (task 2)

`agentkit.Resolve` builds the OpenRouter and Ollama clients, so every module
that imports the root package compiles both providers. Go refuses that build
(`go: updates to go.mod needed`) unless the module lists the modules providing
the packages, so `go mod tidy` was run, offline, in the five nested modules that
import the root: `sandbox`, `mcpserve` and `examples/gateway`, `examples/harness`
and `examples/sandbox`. Each gained seven `// indirect` lines. In the `sandbox`
module, listing the Ollama module expands its graph by the 57 modules the SDKs'
`go.mod` files name and raises `github.com/stretchr/testify` v1.11.1 to v1.12.1
and `golang.org/x/exp` to `v0.0.0-20250218142911-aa4b98e5adaa`. None of the 57 is
linked: the sandbox tests link the two SDKs and their direct dependencies, and
the sandbox agent links neither SDK.

Four policy tests compare that graph with a snapshot and are restated:
`TestRsyncDependency_TS14_42`, `TestLandlockPromoted_TS15_40`,
`TestPodmanAddsNoDependency_TS17_60` and
`TestGraphTestsPassOnTheRegeneratedSnapshots_TS28_44`. `sandbox-go-list-m-all.txt`
is regenerated, `upgradesBySpec29` (`deps_test.go`) records the two raised
versions and `addedBySpec29` (`landlock_dep_test.go`) the 57 added modules. The
older snapshots (`.13.txt`, `.14.txt`) are kept as their specs committed them.
The root module's `go.mod` and `go-list-m-all.txt` do not change.

## The window caveat of spec 28 is closed (29-REQ-5.3; task 5)

Spec 28 recorded that an Ollama agent worked with the default descriptor's
1,000,000-token window, not the row's 32,768, because `agentkit.New` resolved
the model with `catalog.Lookup` ([`28_ollama_provider.md`](28_ollama_provider.md)).
The loop now prunes and refuses by the resolved row (`loop.go:293`):
`TestAgentUsesTheResolvedClientAndRow_TS29_21` sends a prompt past the Ollama
row's window and gets the 32,768-token error without a request.

## The agent only raises `Usage.Unpriced` (29-REQ-7.3; task 6)

The spec has the agent mark a turn's usage Unpriced when its rule holds. Because
the field is exported, a client may set it on its own usage, so `priced`
(`loop.go:411`) never clears it: it assigns true when `costUnknown`
(`loop.go:429`) says so and otherwise leaves the usage as it came
(`TestAFlagTheClientSetIsKept`). On Ollama the agent itself never marks a turn.

## The examples print the resolution (29-REQ-8.4; task 2)

`examples/codingagent`, `customtools` and `mcp` no longer call `catalog.Lookup`
for their summary line; they print `agent.Resolution().Model.ID`, which is the id
`New` resolved, a tier included. The spec asks only that they drop their
SDK-client resolution.
