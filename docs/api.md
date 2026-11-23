# API

AgentKit is a Go library; it has no REST API. Its API is the set of public Go
packages below. This file is the map: which package does what, and the
identifiers an application builds on, in the order it uses them. For a
walk-through with examples, read the [user guide](guide.md).

The authoritative reference is each package's documentation: `go doc -all
<import path>`, or the committed text in [`generated/`](generated), which
`make doc` regenerates.

## Contents

- [Packages](#packages)
  - [Root module](#root-module)
  - [Nested modules](#nested-modules)
- [Identifiers, in the order an application uses them](#identifiers-in-the-order-an-application-uses-them)
  - [Workspace](#workspace)
  - [Tools, and shaping the tool set](#tools-and-shaping-the-tool-set)
  - [Outlines](#outlines)
  - [Guard](#guard)
  - [Toolset and runner](#toolset-and-runner)
  - [Agent](#agent)
  - [Serving over MCP](#serving-over-mcp)
  - [Models and providers](#models-and-providers)
  - [Shared vocabulary](#shared-vocabulary)
  - [Test doubles](#test-doubles)
- [Network services](#network-services)

## Packages

### Root module

Import path prefix `github.com/agent-fox-dev/agentkit-go`.

| Package | Purpose |
|---|---|
| `agentkit` (root) | The agent loop: `Config`, `New`, `Agent`, `Resolve`. It runs its tool calls on a runner, and keeps `Toolset`, `Runner` and `NewRunner` as names for package `runner`'s. |
| `runner` | The tool side with no model: `Toolset`, `New` and `Runner`, which run tool calls through the guard and the events. Where a tools-only program starts: it imports no provider and nothing that needs cgo. |
| `core` | The shared vocabulary and every interface seam: `Tool`, `ToolResult`, `ToolPolicy`, messages, events, `ProviderClient`, `Model`, `Usage`, stop reasons, errors; `core.Named` picks tools by name. |
| `schema` | JSON Schema values with typed combinators, `Parse`, `Validate` and `Coerce`. |
| `workspace` | The project directory a scenario opens first: containment, ignore rules and the directory walk. |
| `tools` | The eleven built-in file, search, navigation and shell tools (`tools.For`), the subprocess runner, and `tools.References` for reference search without a model. |
| `output` | The bounded output buffer the tools and code mode share: `Accumulator`, `TruncateMode`, `TruncatedBy`. Standard library only; `tools` keeps its names for them as aliases. |
| `guard` | The shell guard: `Allow`, `Restricted`, `AllowAll`, and the pure `Check` and `Commands` it is built on. |
| `outline` | Declarations of a source file and the verdict of the parse that produced them (parsed or why not, and its syntax errors): Go always, fourteen more languages with cgo. |
| `codemode` | A tool that runs a model-written Starlark script whose functions are other tools. |
| `mcp` | The MCP client and tool pool. |
| `prompt` | System prompt assembly: the base prompt plus the active tools' guidelines. |
| `catalog` | The embedded model catalog of the three providers and their tiers: `Lookup`, `Select`. |
| `provider` | What the providers share: cost arithmetic, the tool-schema cache, tool-pairing repair. |
| `provider/anthropic` | The Anthropic Messages API — direct, Vertex AI, Bedrock — on the official Go SDK. |
| `provider/openrouter` | OpenRouter's Chat Completions API on OpenRouter's Go SDK. |
| `provider/ollama` | An Ollama server's `/api/chat` on Ollama's Go package. |
| `provider/faux` | A scripted provider for offline tests and demos. |
| `wire` | Bounded, strict parsing of JSON that AgentKit did not produce. |

### Nested modules

Each is its own Go module, imported only when needed.

| Module | Purpose |
|---|---|
| `…/mcpserve` | Serves a `runner.Runner`'s tools over MCP: `mcpserve.New`, `Server.ServeStdio`, `Server.ServeLoopback`. |
| `…/codesearch` | Indexed code search on zoekt: `codesearch.New`, added to the tools with `tools.Options{Index: idx}`. |

## Identifiers, in the order an application uses them

An application opens a **workspace**, builds the **tools** for it and shapes
that set, describes a **toolset** (the tools, their policy, the guard, the
after-call hook and an event sink) and turns it into a **runner**. The runner
is used by the **agent loop** and by **`mcpserve`**.

### Workspace

| Identifier | Shape |
|---|---|
| `workspace.Open` | `Open(root string, opts Options) (*Workspace, error)` |
| `workspace.Options` | `Ignore IgnoreOptions` (`Getenv`, `Home`, `GitConfig` hooks); the zero value reads the real environment, global excludes included |
| `workspace.NoGlobalExcludes` | `NoGlobalExcludes() IgnoreOptions`: hermetic ignore rules, for tests and agents |
| `*workspace.Workspace` | `Root`, `Resolve`, `Rel`, `CheckWriteTarget`, `Ignore`, `Walk`, `Ignored` |

### Tools, and shaping the tool set

| Identifier | Shape |
|---|---|
| `tools.For` | `For(ws *workspace.Workspace, opts Options) ([]core.Tool, error)`: the built-in tools, in a fixed order, then the index's; a nil `ws` is an error |
| `tools.Options` | `SpillDir`, `DisableSpill`, `Env`, `Symbols SymbolOptions`, `Index Index` |
| `tools.References` | `References(ctx, ws, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)`: reference search without a model |
| `core.Named` | `Named(ts []Tool, names ...string) ([]Tool, error)`: the named tools, in `names` order; an error for a missing or repeated name |
| `codemode.New` | `New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error)` |
| `mcp` | `ParseConfig`, `NewPool`, `Pool.Connect`, `Pool.Tools`, `Pool.Close`, `Connect` |

### Outlines

The tools outline files for you; a program that works on declarations
itself calls `outline` directly. One parse gives a file's declarations and
its parse verdict, so a caller can refuse an edit that leaves a file
unparseable without a parser of its own.

| Identifier | Shape |
|---|---|
| `outline.Outline` | `Outline(ctx, abs string, src []byte, opts Options) (File, error)`: one file; `src` nil reads it from disk. An unreadable file is an error; a cancelled context returns `ctx.Err()` |
| `outline.OutlineMany` | `OutlineMany(ctx, srcs []Source, opts Options) ([]File, error)`: one `File` per input, in order; an unreadable one is not parsed, `failed` |
| `outline.Options` | `Root`, `MaxFileBytes` (0 means 1 MiB), `MaxSyntaxErrors` (0 or negative means 20) |
| `outline.File` | `Path`, `Lang`, `Backend`, `Decls`, and the verdict: `Parsed`, `NotParsed`, `SyntaxErrors`, `SyntaxErrorsTruncated` |
| `outline.NotParsedReason` | Why a file was not parsed: `NotParsedUnsupported` (`unsupported`: unknown extension, or no backend in this build), `NotParsedTooLarge` (`too_large`), `NotParsedBinary` (`binary`: a NUL in the first 8 KiB), `NotParsedFailed` (`failed`: no syntax tree, or an unreadable file in `OutlineMany`). `Backend` is then `none` and `Decls` empty |
| `outline.SyntaxError` | `Line` (1-based), `Column` (1-based, in bytes), `Kind`: `SyntaxErrorUnexpected` (`unexpected`, text the grammar could not place) or `SyntaxErrorMissing` (`missing`, a token expected at that position) |
| `outline.CommentAndStringSpans` | `CommentAndStringSpans(ctx, path string, src []byte) ([][2]int, bool)`: the byte ranges of comments and string literals; Go in every build, the other languages with cgo |

**The verdict.** `Parsed` is true when a backend parsed the bytes, with or
without errors; then `NotParsed` is empty. `SyntaxErrors` is empty and never
nil when there are none or the file was not parsed. It holds at most
`MaxSyntaxErrors` errors, ordered by line, then column, and
`SyntaxErrorsTruncated` is true when the parse found more. An error is a
position and a kind, never a message, and only what the parser rejects:
semantic errors are not reported.

- **Go.** `go/parser` with all errors requested. Each position it reports is
  one error, `missing` when a message there names a token in quotes
  (`expected ')'`) and `unexpected` otherwise. An empty `.go` file has one
  error, at 1:1. Asking for all errors also means a Go file with errors on
  more than ten lines keeps the declarations the parser recovers; it used to
  have none.
- **Tree-sitter.** Each error node with no error node above it is one
  `unexpected` error at its start, and each missing node one `missing` error
  where the token was expected.
- **A grammar can lag its language.** A file using syntax newer than the
  grammar of this build is reported with errors although the language
  accepts it. A caller that refuses files or edits on the verdict should
  offer a way past it.

**Spans.** For a Go file `CommentAndStringSpans` answers in every build,
from its tokens, with or without syntax errors: every comment, interpreted and
raw string and rune literal, `ok` true. An unterminated raw string or block
comment runs to the end of the file; an unterminated interpreted string or
rune ends with its line. For the other languages the spans come from the
tree-sitter grammar with cgo, and `ok` is false without cgo, without a
grammar, or when the context ended. Reference search labels a hit outside
every span `lexical` and one inside a span `text`.

The verdict is not in the output of `file_outline` or `find_symbol`, nor read
by `codesearch` (whose stored index records it with each outline).

### Guard

| Identifier | Shape |
|---|---|
| `guard.Allow` | `Allow(programs ...string) core.BeforeToolCall`: `Restricted` with only an allowlist |
| `guard.Restricted` | `Restricted(o Options) core.BeforeToolCall`; `Options`: `AllowedPrograms`, `AllowShellOperators`, `AllowEnvPrefixes`, `BlockedTools`, `TerminateOnBlock` |
| `guard.AllowAll` | The explicit opt-out: never blocks |
| `guard.Check` | `Check(argv []string, o Options) Decision`; `Decision`: `Block`, `Reason`, `Terminate` |
| `guard.Commands` | `Commands(line string) ([]Command, error)`: every simple command a shell line runs; a `*SyntaxError` for a line it cannot read with certainty |

### Toolset and runner

| Identifier | Shape |
|---|---|
| `runner.Toolset` | `Tools []core.Tool`, `Policy core.ToolPolicy`, `Guard core.BeforeToolCall`, `After core.AfterToolCall`, `Events func(core.Event)` |
| `runner.New` | `New(ts Toolset) (*Runner, error)`; refuses what `agentkit.New` refuses, with the same errors |
| `Runner.Tools`, `Runner.Reachable` | The resolved tools in policy order; those plus everything they reach through wrappers |
| `Runner.Call`, `Runner.Batch` | `Call(ctx, name string, input json.RawMessage) core.ToolResult`, `Batch(ctx, calls ...core.ToolUseBlock) []core.ToolResult`; outside every run, events to `Toolset.Events`; safe for concurrent use |
| `Runner.RunTurn` | `RunTurn(ctx, emit func(core.Event), assistant *core.AssistantMessage, calls []core.ToolUseBlock, turnCount int) Outcome`: the batch of one model turn, with the caller's event sink and the guard's context. The agent loop calls it; so can a loop of your own |
| `runner.Outcome` | `Messages []core.ToolResultMessage`, `Results []core.ToolResult`, `Terminate bool`: one message and one result per call, and the OR of the calls' terminate votes |
| `agentkit.Toolset`, `agentkit.Runner`, `agentkit.NewRunner` | Aliases of `runner.Toolset` and `runner.Runner`, and a call to `runner.New`: a value of one is a value of the other |

A program that only runs or serves tools imports `runner` and not the root
package, and so links no model provider's SDK.

### Agent

| Identifier | Shape |
|---|---|
| `agentkit.Config` | `Client`, `Provider`, `Model`, `Effort`, `Env`, `Getenv`, `System`, `Prefix`, `Toolset`, `MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune`, `MaxTokens` |
| `agentkit.Resolve` | `Resolve(cfg Config) (Resolution, error)`: what `New` would run, offline |
| `agentkit.Resolution` | `Provider`, `Tier`, `Model core.Model`, `Effort`, `Listed`, `Client` |
| `agentkit.New` | `New(cfg Config) (*Agent, error)`: `Resolve`, then `NewRunner(cfg.Toolset)` |
| `Agent.Run` | `Run(ctx, prompt string) (core.RunResult, error)` |
| `Agent.Stream` | `Stream(ctx, prompt string) (*core.EventStream, error)` |
| `Agent.Messages`, `Usage`, `ReachableTools`, `Runner`, `Resolution` | The transcript, usage, reachable tools, runner and resolution; safe to call during a run |
| `agentkit.PruneOptions` | `Threshold float64`, `KeepTurns int` |

### Serving over MCP

| Identifier | Shape |
|---|---|
| `mcpserve.New` | `New(r *runner.Runner, opts Options) (*Server, error)`: one MCP tool per tool of `r.Tools()`, every call through `r.Call` |
| `mcpserve.Options` | `Name`, `Version`, `Instructions`, `Logger *slog.Logger` |
| `Server.ServeStdio` | `ServeStdio(ctx) error`: one session on stdin and stdout |
| `Server.ServeLoopback` | `ServeLoopback(ctx) (*Endpoint, error)`: Streamable HTTP on `127.0.0.1` with a fresh bearer token |
| `mcpserve.Endpoint` | `URL`, `Token`; `Close`, `ClaudeConfig(name)`, `PiConfig(name)` |

### Models and providers

| Identifier | Shape |
|---|---|
| `catalog.Select` | `Select(provider, model string, effort core.Effort) (Selection, error)`: offline; `model` is a tier or an id |
| `catalog.Lookup` | `Lookup(id string) (core.Model, bool)`: a Claude model id |
| `anthropic.Provider`, `anthropic.Resolve` | `Provider(model core.Model, opts Options) core.ProviderClient`; `Resolve(env Env) (*sdk.Client, Source, error)` chooses the deployment |
| `openrouter.New` | `New(m core.Model, opts Options) (core.ProviderClient, error)`: fails with `ErrNoAPIKey` before any request |
| `ollama.New` | `New(m core.Model, opts Options) (*Client, error)`: fails with `ErrInvalidHost` before any request |
| `core.ProviderClient` | `Stream(ctx, Request) (<-chan StreamEvent, error)`: implement it to add a provider |

### Shared vocabulary

| Identifier | Shape |
|---|---|
| `core.Tool` | `Name`, `Description`, `InputSchema`, `OutputSchema`, `ReachableTools`, `Terminating`, `Handler` or `Execute`, `Label`, `Builtin`, `ExecutionMode`, `PrepareArguments`, `PromptGuidelines` |
| `core.ToolResult` | `OK`, `Data`, `Error`, `Detail`, `Terminate`, `Metadata`, `Blocks`, `Text`; `LLMText()`; built with `core.OKResult` and `core.ErrResult` |
| `core.BeforeToolCall` | `func(ctx, BeforeToolCallContext) BeforeToolCallDecision` (`Block`, `Terminate`, `Reason`, `Arguments`) |
| `core.AfterToolCall` | `func(ctx, AfterToolCallContext) AfterToolCallDecision` (`Terminate *bool`) |
| `core.RunResult` | `Messages`, `StopReason`, `LastReason`, `Usage`, `TurnCount`, `Error`; `FinalText()` |
| `core.RunStopReason` | `RunStopEndTurn`, `RunStopMaxTurns`, `RunStopBudgetExceeded`, `RunStopToolTerminate`, `RunStopError`, `RunStopAborted`, `RunStopTimeout`, `RunStopRefusal` (`RunStopPolicy` is declared but the loop never sets it) |
| `schema.Parse`, `schema.Validate` | `Parse(data []byte) (*Schema, error)`, `Validate(s *Schema, v any) error` |

### Test doubles

| Identifier | Shape |
|---|---|
| `faux.New` | `New(turns ...Turn) *Provider`: a scripted `core.ProviderClient` |
| `faux.ToolCall`, `faux.Text` | `ToolCall(id, name, args string) Turn`, `Text(s string) Turn`: one-block turns |

## Network services

The root module opens no listening socket. One nested module serves:
`mcpserve` (MCP on stdio or a loopback port, see
[configuration](configuration.md#mcp-serving-mcpserveoptions)). How the
modules relate is in
[architecture.md](architecture.md); every option is in
[configuration.md](configuration.md).
