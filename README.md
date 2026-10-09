# agentkit-go

AgentKit is two things. **The hands**: workspace mechanics any agent can use —
the file, search, navigation and shell tools (`tools`), source outlines
(`outline`), indexed code search (`codesearch`, a separate module), code mode
(`codemode`), the MCP client pool (`mcp`) and the shell guard (`guard`).
**One driver**: `agentkit.Config` and `agentkit.New`, a minimal agent loop over
the Anthropic Messages API (`provider/anthropic`, on the official Go SDK),
with a scripted provider (`provider/faux`) for offline tests. It serves no
network API and listens on no socket.

## Quickstart

```bash
go test ./...          # everything, offline, no API key
make check             # fmt, vet, lint and test, root and codesearch modules
go run ./examples/agentdemo
```

Run a prompt to completion with the built-in tools:

```go
ws, err := tools.NewWorkspace(".")
if err != nil {
	return err
}
ts, err := tools.All(tools.Options{Workspace: ws})
if err != nil {
	return err
}
client, _, err := anthropic.Resolve(anthropic.OSEnv{})
if err != nil {
	return err // names the environment variables to set
}
agent, err := agentkit.New(agentkit.Config{
	Client: client,
	Model:  "claude-opus-5-5",
	Tools:  ts,
	Guard:  guard.Restricted(guard.Options{AllowedPrograms: []string{"go", "git"}}),
})
if err != nil {
	return err
}
res, err := agent.Run(ctx, "Summarise this package.")
fmt.Println(res.FinalText())
```

`New` refuses a config it could not run safely: no `Client` or `Provider`, no
`Model`, an invalid tool hierarchy, or a reachable shell tool (`execute`,
`run_command`) with no `Guard`. A test sets `Config.Provider` (for example
`faux.New(...)`) instead of `Client`.

### Code mode

`codemode.New` turns a set of tools into one tool that runs a model-written
Starlark script over them. The bound tools become its `ReachableTools`: the
model sees only `code_mode`, and every call a script makes goes through the
driver's guard and event pipeline as a nested call.

```go
var bound []core.Tool
for _, t := range ts {
	switch t.Name {
	case "list_files", "find_files", "read_file", "search_files":
		bound = append(bound, t)
	}
}
cm, info, err := codemode.New(bound, codemode.Options{})
if err != nil {
	return err
}
fmt.Printf("code_mode binds %d tools\n", info.BoundToolsCount)
agent, err := agentkit.New(agentkit.Config{
	Client: client,
	Model:  "claude-opus-5-5",
	Tools:  []core.Tool{cm},
})
```

No shell tool is bound, so no `Guard` is needed. Bind `execute` or
`run_command` and `New` requires one, because a shell reached through a wrapper
is as unguarded as one called directly. [`examples/codemode`](examples/codemode)
runs this offline, with an MCP tool bound beside the built-ins.

## Consumer contract

The identifiers an embedder such as agent-fox builds on.

| Identifier | Shape |
|---|---|
| `agentkit.Config` | `Client`, `Provider`, `Model`, `Effort`, `System`, `Prefix`, `Tools`, `Policy`, `Guard`, `After`, `MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune`, `MaxTokens` |
| `agentkit.New` | `New(cfg Config) (*Agent, error)` |
| `Agent.Run` | `Run(ctx, prompt string) (core.RunResult, error)` |
| `Agent.Stream` | `Stream(ctx, prompt string) (*core.EventStream, error)` |
| `Agent.Messages`, `Agent.Usage`, `Agent.ReachableTools` | `core.Messages`, `core.Usage`, `[]core.Tool`; safe during a run |
| `agentkit.PruneOptions` | `Threshold float64`, `KeepTurns int` |
| `core.RunResult` | `Messages`, `StopReason`, `LastReason`, `Usage`, `TurnCount`, `Error`; `FinalText()` |
| `core.RunStopReason` | `RunStopEndTurn`, `RunStopMaxTurns`, `RunStopBudgetExceeded`, `RunStopToolTerminate`, `RunStopError`, `RunStopAborted`, `RunStopTimeout`, `RunStopRefusal` (`RunStopPolicy` is declared but the driver never sets it) |
| `core.Tool` | `Name`, `Description`, `InputSchema`, `OutputSchema`, `ReachableTools`, `Terminating`, `Handler` or `Execute`, `Builtin`, `ExecutionMode`, `PrepareArguments`, `PromptGuidelines` |
| `core.ToolResult` | `OK`, `Data`, `Error`, `Detail`, `Terminate`, `Metadata`, `Blocks`, `Text`; `core.OKResult`, `core.ErrResult` |
| `core.ProviderClient` | `Stream(ctx, Request) (<-chan StreamEvent, error)` |
| `core.BeforeToolCall` | `func(ctx, BeforeToolCallContext) BeforeToolCallDecision` |
| `schema.Parse`, `schema.Validate` | `Parse(data []byte) (*Schema, error)`, `Validate(s *Schema, v any) error` |
| `guard.Check`, `guard.Restricted` | `Check(argv []string, o Options) Decision`, `Restricted(o Options) core.BeforeToolCall` |
| `catalog.Lookup` | `Lookup(id string) (core.Model, bool)` |
| `anthropic.Resolve` | `Resolve(env Env) (*sdk.Client, Source, error)` |
| `codemode.New` | `New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error)` |
| `mcp` | `ParseConfig`, `NewPool`, `Pool.Connect`, `Pool.Tools`, `Pool.Close`, `Connect` |
| `tools.All` | `All(opts Options) ([]core.Tool, error)` |

## Packages

| Package | What it owns |
|---|---|
| `.` (root) | The driver: `Config`, `New`, the `Agent`, the loop, the tool batch executor, nested tool calls. |
| `core` | The vocabulary and the seams: messages, content blocks, events, `EventStream`, `Tool`, `ToolPolicy`, interceptors, `ProviderClient`, `Model`, `Usage`, stop reasons, errors. |
| `schema` | JSON Schema values, typed combinators, `Parse`, `Validate`, `Coerce`. |
| `tools` | The built-in tools, workspace containment, `Walk`, the ignore engine, the subprocess runner, the symbol table and the reference engine. |
| `outline` | Declarations of a source file: `go/ast` for Go, tree-sitter for fourteen more languages with cgo. |
| `codemode` | The code-mode tool, on `go.starlark.net`. |
| `mcp` | MCP client and tool pool on the official MCP Go SDK. |
| `guard` | The shell boundary: `Check`, `Restricted`, `AllowAll`. |
| `prompt` | The assembled system prompt: base prompt plus the active tools' guidelines. |
| `catalog` | The embedded Claude model catalog and `Lookup`. |
| `provider` | Cost arithmetic, the per-session tool-schema cache, deferred-tool splitting. |
| `provider/anthropic` | The Anthropic wire (direct, Vertex AI, Bedrock) over the official SDK. |
| `provider/faux` | A scripted provider for offline tests and demos. |
| `wire` | Bounded, strict parsing of untrusted JSON, and frame readers. |
| `codesearch` | Separate module: zoekt-backed `code_search`, opted in with `tools.Options{Index: idx}`. |

## Make targets

| Target | Does |
|---|---|
| `make check` | `fmt`, `vet`, `lint`, `test`; run before committing |
| `make test` | `go test ./...` in the root and `codesearch` modules |
| `make vet` | `go vet` in the root and `codesearch` modules |
| `make lint` | `golangci-lint` in both modules; skipped when not installed |
| `make fmt` | `gofmt -l -w .` |
| `make tidy` | `go mod tidy` in both modules |

## Documentation

| Document | Covers |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Package graph, the data flow of a run, invariants, nested calls, code mode, the MCP client, `Workspace.References`. |
| [`docs/configuration.md`](docs/configuration.md) | `agentkit.Config`, environment variables, provider, tool, code-mode and MCP options, the model catalog. |
| [`docs/DEPS.md`](docs/DEPS.md) | Why each direct dependency is allowed, and the libraries considered and rejected. |
| [`examples/README.md`](examples/README.md) | The five example programs and how to configure an application. |
| [`docs/prd/`](docs/prd) | Product requirements. |
| [`docs/errata/`](docs/errata) | Where the code diverges from a spec, and why. |
| [`docs/archive/`](docs/archive) | The superseded 0.4.2 PRD and findings from the deleted provider wires. |

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
