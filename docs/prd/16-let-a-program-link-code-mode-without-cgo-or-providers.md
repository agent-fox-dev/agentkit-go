# Let a program link code mode and the runner without cgo or the providers

## Intent

A Go program that only runs tools (its own, through a runner, with code mode
over them) can import what it needs from AgentKit without a C toolchain and
without the model providers' SDKs in its binary. What a program pays for is
what it uses: the tree-sitter grammars come with the source tools, and the
provider SDKs come with the model loop, and with nothing else.

## Goals

1. A program that imports the runner, `codemode` and `core`, and nothing
   else of AgentKit, builds with `CGO_ENABLED=0` on Linux and macOS.
2. The packages that program links include no tree-sitter module, none of
   `anthropic-sdk-go`, `OpenRouterTeam/go-sdk`, `ollama/ollama`, and no AWS
   or Google module.
3. Every program that builds against AgentKit today builds unchanged:
   `agentkit.Runner`, `agentkit.NewRunner`, `agentkit.Toolset`,
   `tools.Accumulator`, `tools.NewAccumulator`, `tools.TruncateMode` and its
   constants, and `tools.TruncatedBy` and its constants keep their names,
   signatures and behaviour.
4. `make check` passes, and the behaviour of a tool call through a runner,
   of a code-mode script and of truncated output is the same as before.

## Non-goals

- Any change to what the runner, code mode or the accumulator do.
- Making `tools`, `outline` or `codesearch` buildable without cgo. They
  parse source with tree-sitter and keep doing so.
- Removing or renaming anything an embedder imports today.
- Splitting the root module into more modules. The runner stays in the root
  module; a Go program links only the packages it imports.
- Parley's use of this (`parley mcp`). It is Parley's PRD 19.

## Background

Two import chains make a tools-only program expensive today.

- `codemode` imports `tools` for one thing: `tools.Accumulator`, with
  `NewAccumulator`, `TruncateMiddle` and `TruncatedByBytes`, used in
  `codemode/output.go` and `codemode/runtime.go` to bound what a script
  prints. `tools` imports `outline`, and `outline` is tree-sitter and its
  grammar modules, which are C. So `go list -deps ./codemode` lists 15
  tree-sitter packages, and a program with code mode needs cgo.
  `codesearch/tool.go` uses the accumulator too.
- The runner (`Runner`, `NewRunner`, `Toolset`) is in the root package
  `agentkit`: `runner.go`, `batch.go`, `nested.go` and `execguard.go`,
  about 1,000 lines that import only `core` and `guard`. The same package
  holds the model loop (`agent.go`, `loop.go`, `resolve.go`), which imports
  `provider/anthropic`, `provider/openrouter` and `provider/ollama`, and
  through them the vendor SDKs with the AWS and Google credential modules.
  A Go program links a package whole, so importing the runner links all of
  them.

`mcpserve` takes an `*agentkit.Runner`, and `Agent.Runner()` returns one, so
the runner's type is part of both.

The first consumer is Parley's CLI, a pure-Go binary installed on
developers' machines, which is to serve its commands as tools with code
mode over them and calls no model.

## Requirements

### The runner in a package of its own

The runner, its `Toolset`, the batch executor, the nested-call pipeline and
the shell-guard check live in a new package, `runner`
(`github.com/agent-fox-dev/agentkit-go/runner`), which imports no provider
and no package that needs cgo. `runner.New(Toolset)` builds one.

- In the root package, `Runner` and `Toolset` are the same types as the new
  package's (a value of one is a value of the other), and `NewRunner` does
  what `runner.New` does. `Agent.Runner()`, `Config.Toolset` and
  `mcpserve.New` accept and return them as today.
- Every refusal of `NewRunner` is the same error with the same text, for
  example `core.ErrUnguardedExecute` for a reachable shell tool without a
  guard.
- The generated docs list the new package, and `docs/api.md`,
  `docs/architecture.md` and `docs/guide.md` name it as where a
  tools-only program starts.

### The accumulator outside `tools`

The output accumulator and its truncation modes live in a package that
imports only the standard library. `codemode` and `codesearch` use it from
there and no longer import `tools` for it. `tools` keeps its exported names
for it as the same types and constants.

- Truncation markers, spill files and their paths, and the byte counts of
  every tool that uses the accumulator are unchanged.

### Keeping it so

The build has a check, part of `make check`, that fails when the packages
a program needs for the runner and code mode (`runner`, `codemode`, `core`,
`schema`, `guard`) come to depend on a tree-sitter module, a provider
package or a provider's SDK, and names the package that brought it in.

- The check also builds those packages with `CGO_ENABLED=0`.

## Design Decisions

1. **Old names stay, as aliases** (my decision): the change must not break
   `mcpserve`, the examples or agent-fox, and an alias costs one line.
2. **The package is `runner`, its constructor `runner.New`**: the name the
   type already has.
3. **A dependency check in `make check`**, although the steering warns
   against tests of an import graph in general: here the import graph is
   the product, and nothing else would notice a regression.
4. **Profile: lite.** Three requirements, no flow that crosses components.

## Dependencies

- PRD 02 and ADR 01 (keep the root package to the agent): this moves the
  runner out of the root package, which goes further than they did. The
  ADR this work writes says so.
- Archived specs 07 (nested tool calls), 08 (code mode), 21 (toolset
  runner), 23 (MCP serve): behaviour unchanged.
- PRD 15 (`agentkit-tools`): independent. Either order works.
- Parley PRD 19 (`parley mcp`): the consumer; it waits for this.

## Glossary

- **Runner**: what runs tool calls through a tool set's policy, guard and
  interceptors, with no model.
- **Accumulator**: the bounded buffer that keeps the head and tail of a
  long output and may spill the whole to a file.
- **Tools-only program**: a program that serves or calls tools and never
  calls a model.
