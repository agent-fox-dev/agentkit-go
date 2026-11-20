# ADR 10: Keep the runner and the output buffer free of cgo and providers

- **Status:** Accepted
- **Date:** 2026-11-20

## Context

A Go program links a package whole. The runner (`Toolset`, `NewRunner`,
`Runner`, the batch executor and the nested caller) lived in the root package
beside the model loop, and the root package imports the three providers, so
a program that only ran tools linked the vendors' SDKs and the AWS and Google
credential modules with them. `mcpserve` took an `*agentkit.Runner` and linked
them too. `codemode` imported `tools` for its output buffer alone, and `tools`
imports `outline`, whose grammars are C: with cgo on, a program with code
mode linked fifteen tree-sitter packages and needed a C toolchain.

[ADR 01](01-keep-the-agent-loop-small-and-report-through-events.md) and PRD 02
kept the root package to the agent and the tool side it runs on. This goes
further: the tool side leaves the root package.

## Decision

**The runner is package `runner`.** It holds `Toolset`, `New`, `Runner` and
everything behind them, and imports `core` and `guard` and nothing else of the
module. The root package keeps `Toolset` and `Runner` as aliases and
`NewRunner` as a call to `runner.New`, so a value of one is a value of the
other and nothing an embedder wrote breaks. The refusals keep their text,
the `agentkit:` prefix included: the same error with the same text was the
requirement.

**The agent loop reaches the batch executor through one exported method,
`Runner.RunTurn`**, which returns a `runner.Outcome`: the transcript messages,
the results and the batch's terminate vote. A bridge through an `internal`
package would have kept the method unexported; the owner chose the exported
method, which a program that writes its own loop can call as well. `Batch` is
`RunTurn` with the toolset's sink, no assistant message and turn 0.

**The output buffer is package `output`.** `Accumulator`, `TruncateMode` and
`TruncatedBy` live there, on the standard library alone. `tools` keeps its
names as aliases, `codemode` imports `output` and no longer `tools`.
`Accumulator.UseFile` and `Accumulator.FileError` are exported because `tools`
calls them across the boundary; `UseFile` takes an `io.WriteCloser`, and
`tools` hands it a writer whose `Close` still releases the log file.

**`mcpserve` takes a `*runner.Runner`** and does not import the root package.

**One test holds the import graph, and it is the only one.** The steering
forbids tests of an import graph, because they pin structure and not
behaviour. Here the graph is the product: one new import in `runner`,
`codemode`, `core`, `schema`, `guard` or `mcpserve` undoes the work and no
other test would notice. `internal/policy` therefore lists what those packages
import and fails when one reaches:

- a path containing `tree-sitter`;
- the root package, or `provider` and the packages under it;
- a package of `github.com/anthropics/anthropic-sdk-go`,
  `github.com/OpenRouterTeam/go-sdk` or `github.com/ollama/ollama`;
- a package under `github.com/aws/` or `cloud.google.com/`.

`golang.org/x/oauth2` is allowed: the MCP client uses it, and `mcpserve`
reaches it through `mcp`. The list is read with cgo enabled, since with cgo
off the grammar imports sit behind a build constraint and every package would
pass without proof. The gate runs under `-short`, in `make check`, and also
builds the packages with `CGO_ENABLED=0`. It reports the shortest chain of
imports to each forbidden package and does not walk on from one: what a
forbidden package imports came in with it. A second test runs the same code
over `tools` and the root package, which do link what is forbidden, so the
gate is known to fire.

## Consequences

- A program that imports `runner`, `codemode`, `core` and `mcpserve` links no
  provider SDK and no grammar, and builds with `CGO_ENABLED=0`.
- `tools`, `outline` and `codesearch` still parse source with tree-sitter; a
  program that adds the source tools pays for the grammars.
- `Runner.RunTurn` and `runner.Outcome` are public API, on `agentkit.Runner`
  too, and are kept compatible like the rest.
- `cmd/agentkit-tools` and the examples still build their runner with
  `agentkit.NewRunner`. They work unchanged; a program that wants the smaller
  binary calls `runner.New`.
- The module still requires the providers' modules in `go.mod`. What a
  program links is decided by the packages it imports, not by the module
  graph.
