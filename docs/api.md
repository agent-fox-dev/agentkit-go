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
  - [Guard](#guard)
  - [Toolset and runner](#toolset-and-runner)
  - [Agent](#agent)
  - [Serving over MCP](#serving-over-mcp)
  - [Sandboxes](#sandboxes)
  - [Models and providers](#models-and-providers)
  - [Shared vocabulary](#shared-vocabulary)
  - [Test doubles](#test-doubles)
- [Network services](#network-services)

## Packages

### Root module

Import path prefix `github.com/agent-fox-dev/agentkit-go`.

| Package | Purpose |
|---|---|
| `agentkit` (root) | The agent loop — `Config`, `New`, `Agent`, `Resolve` — and the tool side it runs on: `Toolset`, `NewRunner` and `Runner`, which run tool calls with no model. |
| `core` | The shared vocabulary and every interface seam: `Tool`, `ToolResult`, `ToolPolicy`, messages, events, `ProviderClient`, `Model`, `Usage`, stop reasons, errors; `core.Named` picks tools by name. |
| `schema` | JSON Schema values with typed combinators, `Parse`, `Validate` and `Coerce`. |
| `workspace` | The project directory a scenario opens first: containment, ignore rules and the directory walk. |
| `tools` | The eleven built-in file, search, navigation and shell tools (`tools.For`), the subprocess runner, and `tools.References` for reference search without a model. |
| `guard` | The shell guard: `Allow`, `Restricted`, `AllowAll`, and the pure `Check` and `Commands` it is built on. |
| `outline` | Declarations of a source file: Go always, fourteen more languages with cgo. |
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
| `…/sandbox` | The sandbox API (`Provider`, `Sandbox`, `Spec`, `Open`, the transfer sources) and its providers: `sandbox/local`, `sandbox/podman`, `sandbox/landlock` and the gateway client `sandbox/gateway`. Also the `agentkit-sandboxd` command. |
| `…/sandbox/gatewayd` | The gateway server (`gatewayd.New`, `Server`) behind the `agentkit-gateway` command. A program that embeds it can authenticate with `Config.Authenticate`, read the caller with `gatewayd.PrincipalFromContext`, and end a sandbox with `Server.Destroy`. |
| `…/mcpserve` | Serves a `Runner`'s tools over MCP: `mcpserve.New`, `Server.ServeStdio`, `Server.ServeLoopback`. |
| `…/codesearch` | Indexed code search on zoekt: `codesearch.New`, added to the tools with `tools.Options{Index: idx}`. |

## Identifiers, in the order an application uses them

An application opens a **workspace**, builds the **tools** for it and shapes
that set, describes a **toolset** (the tools, their policy, the guard, the
after-call hook and an event sink) and turns it into a **runner**. The runner
is used by the **agent loop** and by **`mcpserve`**. A sandbox takes the place
of the first two steps.

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
| `agentkit.Toolset` | `Tools []core.Tool`, `Policy core.ToolPolicy`, `Guard core.BeforeToolCall`, `After core.AfterToolCall`, `Events func(core.Event)` |
| `agentkit.NewRunner` | `NewRunner(ts Toolset) (*Runner, error)`; refuses what `New` refuses, with the same errors |
| `Runner.Tools`, `Runner.Reachable` | The resolved tools in policy order; those plus everything they reach through wrappers |
| `Runner.Call`, `Runner.Batch` | `Call(ctx, name string, input json.RawMessage) core.ToolResult`, `Batch(ctx, calls ...core.ToolUseBlock) []core.ToolResult`; outside every run, events to `Toolset.Events`; safe for concurrent use |

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
| `mcpserve.New` | `New(r *agentkit.Runner, opts Options) (*Server, error)`: one MCP tool per tool of `r.Tools()`, every call through `r.Call` |
| `mcpserve.Options` | `Name`, `Version`, `Instructions`, `Logger *slog.Logger` |
| `Server.ServeStdio` | `ServeStdio(ctx) error`: one session on stdin and stdout |
| `Server.ServeLoopback` | `ServeLoopback(ctx) (*Endpoint, error)`: Streamable HTTP on `127.0.0.1` with a fresh bearer token |
| `mcpserve.Endpoint` | `URL`, `Token`; `Close`, `ClaudeConfig(name)`, `PiConfig(name)` |

### Sandboxes

| Identifier | Shape |
|---|---|
| `sandbox.Open` | `Open(ctx, p Provider, spec Spec, src Source, opts ToolOptions) (Sandbox, []core.Tool, error)`: `Create`, `Sync` (when `src` is not nil), `Tools`; closes the sandbox if a step fails |
| `sandbox.Provider` | `Capabilities()`, `Create(ctx, Spec) (Sandbox, error)`, `Reap(ctx) ([]string, error)` |
| `sandbox.Sandbox` | `ID`, `Spec`, `Agent`, `Sync(ctx, Source)`, `Tools(ctx, ToolOptions)`, `Export(ctx, path, Destination)`, `Close(ctx)` |
| `sandbox.Spec` | `Image`, `Workdir`, `Env`, `Resources`, `Network`, `TTL`, `Labels`, `Provider`, `RequireLandlock` |
| `sandbox.CheckSpec` | `CheckSpec(caps Capabilities, spec Spec) (Spec, error)` |
| `sandbox.LocalDir`, `Tar`, `TarOut`, `GitRepo` | Sources and destinations: a host directory over rsync, a tar stream in or out, a public repository |
| `local.Unisolated`, `podman.New`, `landlock.New`, `gateway.Dial` | The providers |

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

The root and sandbox modules open no listening socket. Two nested modules
serve: `mcpserve` (MCP on stdio or a loopback port, see
[configuration](configuration.md#mcp-serving-mcpserveoptions)) and
`sandbox/gatewayd`, whose `agentkit-gateway` command speaks gRPC (see
[cli.md](cli.md#agentkit-gateway)). How the modules relate is in
[architecture.md](architecture.md); every option is in
[configuration.md](configuration.md).
