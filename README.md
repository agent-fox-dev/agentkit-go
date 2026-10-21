# agentkit-go

AgentKit is two things. **The hands**: workspace mechanics any agent can use —
the workspace they run in (`workspace`), the file, search, navigation and shell
tools built for it (`tools`), source outlines (`outline`), indexed code search
(`codesearch`, a separate module), code mode (`codemode`), the MCP client pool
(`mcp`), the shell guard (`guard`), and sandboxes that run those tools
somewhere else (`sandbox`).
**One driver**: `agentkit.Config` and `agentkit.New`, a minimal agent loop over
a `Runner` (`agentkit.Toolset` and `agentkit.NewRunner`), which runs the tool
calls, against the Anthropic Messages API (`provider/anthropic`, on the
official Go SDK), OpenRouter (`provider/openrouter`, on OpenRouter's Go SDK) or
Ollama (`provider/ollama`, on Ollama's Go package), chosen by `Config.Provider`,
with a scripted provider (`provider/faux`, passed as `Config.Client`) for
offline tests.
The root and sandbox modules listen on no socket and serve no MCP: they are a
library. `agentkit-sandboxd` serves the agent service on its stdio, and two
separate modules listen: `sandbox/gatewayd` (`agentkit-gateway`, sandboxes over
gRPC) and `mcpserve` (a runner's tools over MCP, to a harness on the same
machine).

## Quickstart

```bash
go test ./...          # everything, offline, no API key
make check             # fast: fmt, vet, lint and the short tests, in every module of MODULES
make check-release     # full: adds the slow gates (cross-target builds, nested make runs)
make provider-live-test # opt-in: one real tool loop on each provider that is ready; costs money
go run ./examples/agentdemo
```

Run a prompt to completion with the built-in tools:

```go
ws, err := workspace.Open(".", workspace.Options{})
if err != nil {
	return err
}
ts, err := tools.For(ws, tools.Options{})
if err != nil {
	return err
}
// Provider names the service that runs the model: "anthropic" (the default when
// it is empty), "openrouter" or "ollama". New builds that provider's client from
// its own environment variables, the table below, and no SDK is imported here.
agent, err := agentkit.New(agentkit.Config{
	Provider: "openrouter",
	Model:    "STANDARD", // a tier: SIMPLE, STANDARD or ADVANCED; or the provider's own model id
	Toolset: agentkit.Toolset{
		Tools: ts,
		Guard: guard.Allow("go", "git"),
	},
})
if err != nil {
	return err // a missing credential is reported by name, for example OPENROUTER_API_KEY
}
res, err := agent.Run(ctx, "Summarise this package.")
fmt.Println(res.FinalText())
```

What each provider reads from the environment, and nothing else:

| `Provider` | Reads |
|---|---|
| `anthropic` (the default) | `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_OAUTH_TOKEN`, with `ANTHROPIC_BASE_URL`; or the Vertex AI or Bedrock variables (`CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_BEDROCK`, …) |
| `openrouter` | `OPENROUTER_API_KEY`, and `OPENROUTER_BASE_URL` |
| `ollama` | `OLLAMA_HOST`; no credential |

`New` refuses a config it could not run safely, with the first of: an unknown
`Provider`, no `Model`, an unknown `Effort`, a credential the provider needs
and cannot find (the error names the variables to set), an invalid tool
hierarchy, or a reachable shell tool (`execute`, `run_command`) with no
`Guard`. `Provider` is `anthropic` (the default), `openrouter` or `ollama`,
`Model` is a tier (`SIMPLE`, `STANDARD`, `ADVANCED`) or the provider's own model
id, and `Env` and `Getenv` supply that provider's variables; each provider reads
only its own. `agentkit.Resolve` shows what a `Config` resolves to without
building an agent. A test sets `Config.Client` (for example `faux.New(...)`)
and builds no provider.

### Code mode

`codemode.New` turns a set of tools into one tool that runs a model-written
Starlark script over them. The bound tools become its `ReachableTools`: the
model sees only `code_mode`, and every call a script makes goes through the
driver's guard and event pipeline as a nested call.

```go
bound, err := core.Named(ts, "list_files", "find_files", "read_file", "search_files")
if err != nil {
	return err // names what is missing and lists what ts holds
}
cm, info, err := codemode.New(bound, codemode.Options{})
if err != nil {
	return err
}
fmt.Printf("code_mode binds %d tools\n", info.BoundToolsCount)
agent, err := agentkit.New(agentkit.Config{
	Model:   "claude-opus-5-5",
	Toolset: agentkit.Toolset{Tools: []core.Tool{cm}},
})
```

No shell tool is bound, so no `Toolset.Guard` is needed. Bind `execute` or
`run_command` and `New` requires one, because a shell reached through a wrapper
is as unguarded as one called directly. [`examples/codemode`](examples/codemode)
runs this offline, with an MCP tool bound beside the built-ins.

### Sandboxes

A sandbox is an ephemeral environment that holds one workspace. A
`sandbox.Provider` creates one, `Sync` puts files into it, and `Tools`
returns a drop-in replacement for `tools.For`: one proxy per built-in, with
the same definition, whose every call runs in the AgentKit agent
(`agentkit-sandboxd`) inside the sandbox over the `agentkit.sandbox.v1` gRPC
protocol. The driver does not change and does not know a tool is remote.
`sandbox.Open` does `Create`, `Sync` and `Tools` in one call and closes the
sandbox if a step fails; a nil source skips the `Sync`, and the three methods
stay for a second `Sync` and for the `SyncResult`, which `Open` discards.

```go
// Only what changed moves; the same ignore rules as the file tools apply.
sb, proxies, err := sandbox.Open(ctx, local.Unisolated(),
	sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}},
	sandbox.LocalDir{Path: "./project"}, sandbox.ToolOptions{})
if err != nil {
	return err // a Spec the provider cannot honour is refused, naming the field
}
defer sb.Close(ctx)
agent, err := agentkit.New(agentkit.Config{
	Model: "claude-opus-5-5",
	Toolset: agentkit.Toolset{
		Tools: proxies,
		Guard: guard.Allow("go"),
	},
})
// ... run the agent, then bring its edits back into a host directory.
if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: "./result"}); err != nil {
	return err
}
```

`sandbox/podman` is the first provider that isolates: `podman.New(podman.Options{})`
runs each sandbox's agent in a hardened, ephemeral podman or docker container
with one workspace volume, no host mount, a read-only root, no capabilities,
an explicit environment (plus the image's own `ENV` on docker) and, by
default, no network, and can fill the
workspace from a public repository with `sandbox.GitRepo`. `local` is **not
isolation**: it runs the agent in the embedder's process with its
privileges, for tests and development, over the same protocol and
transport. A proxy shell tool still needs a `Guard`; `guard.AllowAll` is
reasonable only for proxies from an isolating provider under `NetworkNone`
([`docs/configuration.md`](docs/configuration.md#guardoptions)).
`sandbox/landlock` needs no engine: on Linux with Landlock ABI 4 it runs the
agent as a confined child process, which protects the embedder's files and,
under `NetworkNone`, TCP, but is a confinement, not an isolation; install the
agent with
`go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@vX.Y.Z`.
A cancelled
call returns `aborted` and kills the process it started
in the sandbox; a broken connection makes every later call return
`sandbox_unavailable`. `sandbox.LocalDir` moves a host directory in or out
over the rsync protocol: never an escaping symlink, never `.git` on export,
and never the destination's ignored files, `.git` or escaping symlinks
touched. `sandbox.Tar` and `sandbox.TarOut` move a tar stream instead, every
entry checked before it is written. On Linux the agent also confines itself
with a Landlock ruleset before any other work, reported in
`Sandbox.Agent().Landlock`, and `Spec.RequireLandlock` makes a missing or
too-weak ruleset a `Create` failure (`local` never confines and refuses it).
[`examples/sandbox`](examples/sandbox) runs a
code-mode script over proxy tools offline; [`docs/cli.md`](docs/cli.md)
covers the agent command.

The agent ships in one image, `containers/agentkit-sandbox/Containerfile`,
published as `quay.io/agentfox/agentkit-sandbox:X.Y.Z`
(`sandbox.BaseImageRepo`): UBI 10 with `git`, `bash` and `ca-certificates`,
a uid-1001 user, and the cgo-built agent (tree-sitter outlines) at
`sandbox.AgentPath` (`/usr/local/bin/agentkit-sandboxd`). It runs as is;
`sandbox.DefaultImage()` names it for the build's version. An embedder's own
toolchain image is built `FROM` it.

`make build-containers` builds it locally, and
[`docs/releasing.md`](docs/releasing.md) covers publishing it.

A sandbox can also run on another host, behind a gateway. `sandbox/gateway`
is the client: `gateway.Dial` returns a `sandbox.Provider` whose sandboxes
behave like local ones, with the same proxies, state machine and errors.
The gateway holds the backend provider and its credentials. The embedder
holds only the gateway's URL and bearer token, and the sandbox holds
neither.

```go
p, err := gateway.Dial(ctx, "https://gateway.example.com", gateway.Options{Token: token})
if err != nil {
	return err // a refused token matches gateway.ErrUnauthenticated
}
defer p.Close() // destroys the sandboxes p created, then disconnects
sb, err := p.Create(ctx, sandbox.Spec{}) // NetworkNone; Image defaults to this build's
```

The server is `agentkit-gateway`, built with `make sandbox-gateway` from the
nested `sandbox/gatewayd` module over the Podman or Docker provider. It
authenticates every RPC, gives each token only its own sandboxes, bounds TTL,
count and network modes, and relays the agent protocol to each sandbox. Its
flags and token file are in [`docs/cli.md`](docs/cli.md). `http://` is
accepted only on loopback, so a token never crosses a network in clear text.
[`examples/gateway`](examples/gateway) runs a gateway in process on a
loopback port, over `local`, and drives it as an embedder would, offline;
with `--url` it dials an `agentkit-gateway` already running.

### Another harness

The driver is optional. The tool side of an agent — tools, policy, guard,
after-interceptor — is a `Toolset`, and `agentkit.NewRunner` turns one into a
`Runner` that runs tool calls with no model: `Runner.Call` runs one, through
the same preparation, guard, handler and events a model's calls go through,
and code mode works through it. Another harness — Claude Code, Pi, any MCP
client — can work in a sandbox with code mode and the built-in tools as its
only tools: the embedder serves them over MCP from a runner with `mcpserve`
and keeps the sandbox, the guard and the policy on its side.
[`examples/harness`](examples/harness) does that for one task of a spec
package: it fills a sandbox from a repository with one `sandbox.Open`, runs
`specr` inside it, measures the spec's checks, starts the harness with its own
tools off, and measures again. Offline it runs on `local` with a scripted
stand-in for the harness.

`mcpserve`, a nested module, is the other one that listens. It serves a
`Runner`'s tools to a harness on the same machine, on the process's stdio or
over Streamable HTTP on a loopback port behind a bearer token. Every call runs
through `Runner.Call`, so the guard sees a direct call and a `code_mode`
script's nested call alike, and a blocked or failed call comes back as an
`isError` result, not a protocol error.

```go
srv, err := mcpserve.New(runner, mcpserve.Options{Instructions: "The tools run in a sandbox."})
if err != nil {
	return err // names a tool whose input schema cannot be served
}
ep, err := srv.ServeLoopback(ctx) // 127.0.0.1, a fresh token
if err != nil {
	return err
}
defer ep.Close()
// ep.URL and ep.Token say where a harness connects. ep.ClaudeConfig("agentkit")
// is Claude Code's --mcp-config and ep.PiConfig("agentkit") is Pi's .pi/mcp.json.
// A harness that starts the program as a child process takes srv.ServeStdio(ctx).
```

Every request needs `Authorization: Bearer <ep.Token>` (401 otherwise, before
its body is read), only `/mcp` is served, and a request body is held to
`wire.Defaults()` before the SDK decodes it. The options and limits are in
[`docs/configuration.md`](docs/configuration.md#mcp-serving-mcpserveoptions).

## Consumer contract

The identifiers an embedder such as agent-fox builds on, in the order it builds
a scenario. A scenario opens a **workspace**, builds the **tools** for it and
shapes that set, fixes a **toolset** (the tools, their policy, the guard, the
after-interceptor and an event sink) and turns it into a **runner**, which runs
tool calls with no model. The runner has two consumers: the **agent loop**
(`agentkit.New`, which builds its runner from `Config.Toolset`) and
**`mcpserve`**, which serves its tools over MCP to another harness. A sandbox
takes the place of the first two steps.

### Workspace

| Identifier | Shape |
|---|---|
| `workspace.Open` | `Open(root string, opts Options) (*Workspace, error)` |
| `workspace.Options` | `Ignore IgnoreOptions` (`Getenv func(string) string`, `Home func() (string, error)`, `GitConfig func() string`); the zero value reads the real environment, global excludes included |
| `workspace.NoGlobalExcludes` | `NoGlobalExcludes() IgnoreOptions`: hermetic ignore rules, for tests and for agents |
| `*workspace.Workspace` | `Root() string`, `Resolve(p string) (string, error)`, `Rel(abs string) string`, `CheckWriteTarget(abs string) error`, `Ignore() IgnoreOptions`, `Walk(ctx, sub string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error`, `Ignored(rel string, isDir bool) bool` |

### Tools, and shaping the tool set

| Identifier | Shape |
|---|---|
| `tools.For` | `For(ws *workspace.Workspace, opts Options) ([]core.Tool, error)`: the built-in tools for `ws`, in a fixed order, then `Options.Index`'s; a nil `ws` is an error |
| `tools.Options` | `SpillDir`, `DisableSpill`, `Env`, `Symbols SymbolOptions`, `Index Index` |
| `tools.References` | `References(ctx, ws *workspace.Workspace, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)`: references without a model |
| `core.Named` | `Named(ts []Tool, names ...string) ([]Tool, error)`; value copies in `names` order, an error for a missing or repeated name |
| `codemode.New` | `New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error)` |
| `mcp` | `ParseConfig`, `NewPool`, `Pool.Connect`, `Pool.Tools`, `Pool.Close`, `Connect` |

### Guard

| Identifier | Shape |
|---|---|
| `guard.Allow` | `Allow(programs ...string) core.BeforeToolCall`: `Restricted` with only `AllowedPrograms` |
| `guard.Restricted` | `Restricted(o Options) core.BeforeToolCall`; `Options`: `AllowedPrograms`, `AllowShellOperators`, `AllowEnvPrefixes`, `BlockedTools`, `TerminateOnBlock` |
| `guard.AllowAll` | `AllowAll(ctx, core.BeforeToolCallContext) core.BeforeToolCallDecision`: the explicit opt-out |
| `guard.Check` | `Check(argv []string, o Options) Decision`; `Decision`: `Block`, `Reason`, `Terminate` |
| `guard.Commands` | `Commands(line string) ([]Command, error)`: every simple command a shell line runs, substitutions and heredoc bodies included; `Command`: `Text`, `Assign`, `Argv`, `Redirects`; a `*SyntaxError` for a line it cannot read with certainty |

### Toolset and runner

| Identifier | Shape |
|---|---|
| `agentkit.Toolset` | `Tools []core.Tool`, `Policy core.ToolPolicy`, `Guard core.BeforeToolCall`, `After core.AfterToolCall`, `Events func(core.Event)` |
| `agentkit.NewRunner` | `NewRunner(ts Toolset) (*Runner, error)`; refuses what `New` refuses, with the same errors |
| `Runner.Tools`, `Runner.Reachable` | `Tools() []core.Tool` (the resolved set, in policy order), `Reachable() []core.Tool` (those and everything they reach through wrappers) |
| `Runner.Call`, `Runner.Batch` | `Call(ctx, name string, input json.RawMessage) core.ToolResult`, `Batch(ctx, calls ...core.ToolUseBlock) []core.ToolResult`; outside every run, events to `Toolset.Events`; a `Runner` is safe for concurrent use |

### Agent

| Identifier | Shape |
|---|---|
| `agentkit.Config` | `Client core.ProviderClient`, `Provider string`, `Model`, `Effort`, `Env map[string]string`, `Getenv func(string) string`, `System`, `Prefix`, `Toolset`, `MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune`, `MaxTokens` |
| `agentkit.Resolve` | `Resolve(cfg Config) (Resolution, error)`: what `New` would run, offline; checks the provider, model, effort and credentials |
| `agentkit.Resolution` | `Provider`, `Tier`, `Model core.Model`, `Effort`, `Listed`, `Client core.ProviderClient` |
| `agentkit.New` | `New(cfg Config) (*Agent, error)`; calls `Resolve(cfg)`, then builds its runner with `NewRunner(cfg.Toolset)` |
| `Agent.Run` | `Run(ctx, prompt string) (core.RunResult, error)` |
| `Agent.Stream` | `Stream(ctx, prompt string) (*core.EventStream, error)` |
| `Agent.Messages`, `Agent.Usage`, `Agent.ReachableTools`, `Agent.Runner`, `Agent.Resolution` | `core.Messages`, `core.Usage`, `[]core.Tool`, `*Runner`, `Resolution` (a copy); safe during a run |
| `agentkit.PruneOptions` | `Threshold float64`, `KeepTurns int` |

### Serving

| Identifier | Shape |
|---|---|
| `mcpserve.New` | `New(r *agentkit.Runner, opts Options) (*Server, error)`: one MCP tool for each tool of `r.Tools()`, every call through `r.Call`; refuses, naming the tool, an input schema it cannot serve |
| `mcpserve.Options` | `Name`, `Version`, `Instructions string`, `Logger *slog.Logger` |
| `Server.ServeStdio` | `ServeStdio(ctx) error`: the process's stdin and stdout; nil when the client closes its end, `ctx.Err()` when `ctx` ends |
| `Server.ServeLoopback` | `ServeLoopback(ctx) (*Endpoint, error)`: `127.0.0.1`, a fresh bearer token |
| `mcpserve.Endpoint` | `URL`, `Token`; `Close() error`, `ClaudeConfig(name string) string`, `PiConfig(name string) string` |

### Sandbox API

| Identifier | Shape |
|---|---|
| `sandbox.Open` | `Open(ctx, p Provider, spec Spec, src Source, opts ToolOptions) (Sandbox, []core.Tool, error)`; `Create`, `Sync` when `src` is not nil, then `Tools`; closes the sandbox if a step fails |
| `sandbox.Provider` | `Capabilities()`, `Create(ctx, Spec) (Sandbox, error)`, `Reap(ctx) ([]string, error)` |
| `sandbox.Sandbox` | `ID`, `Spec`, `Agent`, `Sync(ctx, Source)`, `Tools(ctx, ToolOptions) ([]core.Tool, error)`, `Export(ctx, path, Destination)`, `Close(ctx)` |
| `sandbox.CheckSpec` | `CheckSpec(caps Capabilities, spec Spec) (Spec, error)` |
| `sandbox.LocalDir` | `Path`, `Ignore workspace.IgnoreOptions`, `IncludeIgnored`, `ExcludeGit`, `Delete`: a host directory, in or out, over the rsync protocol |
| `local.Unisolated` | `Unisolated() sandbox.Provider` |

### Model and provider

| Identifier | Shape |
|---|---|
| `anthropic.Resolve` | `Resolve(env Env) (*sdk.Client, Source, error)` |
| `openrouter.New` | `New(m core.Model, opts Options) (core.ProviderClient, error)`: reads `OPENROUTER_API_KEY` and `OPENROUTER_BASE_URL`; fails with `ErrNoAPIKey` before any request |
| `ollama.New` | `New(m core.Model, opts Options) (*Client, error)`: reads `OLLAMA_HOST` and sends no credential; fails with `ErrInvalidHost` before any request |
| `catalog.Lookup` | `Lookup(id string) (core.Model, bool)`: a Claude model id; `Select` calls it for an Anthropic id |
| `catalog.Select` | `Select(provider, model string, effort core.Effort) (Selection, error)`: offline, for `anthropic`, `openrouter` and `ollama`; `model` is a tier (`SIMPLE`, `STANDARD`, `ADVANCED`) or a model id; `agentkit.Resolve` calls it |
| `core.ProviderClient` | `Stream(ctx, Request) (<-chan StreamEvent, error)` |

### Vocabulary

| Identifier | Shape |
|---|---|
| `core.Tool` | `Name`, `Description`, `InputSchema`, `OutputSchema`, `ReachableTools`, `Terminating`, `Handler` or `Execute`, `Label`, `Builtin`, `ExecutionMode`, `PrepareArguments`, `PromptGuidelines` |
| `core.ToolResult` | `OK`, `Data`, `Error`, `Detail`, `Terminate`, `Metadata`, `Blocks`, `Text`; `core.OKResult`, `core.ErrResult` |
| `core.BeforeToolCall` | `func(ctx, BeforeToolCallContext) BeforeToolCallDecision` (`Block`, `Terminate`, `Reason`, `Arguments`) |
| `core.AfterToolCall` | `func(ctx, AfterToolCallContext) AfterToolCallDecision` (`Terminate *bool`) |
| `core.RunResult` | `Messages`, `StopReason`, `LastReason`, `Usage`, `TurnCount`, `Error`; `FinalText()`; `Usage.Unpriced` is true when a turn's cost of zero is not a price |
| `core.RunStopReason` | `RunStopEndTurn`, `RunStopMaxTurns`, `RunStopBudgetExceeded`, `RunStopToolTerminate`, `RunStopError`, `RunStopAborted`, `RunStopTimeout`, `RunStopRefusal` (`RunStopPolicy` is declared but the driver never sets it) |
| `schema.Parse`, `schema.Validate` | `Parse(data []byte) (*Schema, error)`, `Validate(s *Schema, v any) error` |

### Test doubles

| Identifier | Shape |
|---|---|
| `faux.New` | `New(turns ...Turn) *Provider`: a scripted `core.ProviderClient` |
| `faux.ToolCall`, `faux.Text` | `ToolCall(id, name, args string) faux.Turn`, `Text(s string) faux.Turn`: one-block scripted turns |

## Packages

| Package | What it owns |
|---|---|
| `.` (root) | The driver: `Config`, `New`, the `Agent`, the loop. The tool side it is built on: `Toolset`, `NewRunner`, `Runner` and its batch engine and nested calls. |
| `core` | The vocabulary and the seams: messages, content blocks, events, `EventStream`, `Tool`, `ToolPolicy`, interceptors, `ProviderClient`, `Model`, `Usage`, stop reasons, errors. |
| `schema` | JSON Schema values, typed combinators, `Parse`, `Validate`, `Coerce`. |
| `workspace` | The workspace every scenario opens first: containment (`Resolve`, `CheckWriteTarget`), the ignore rules (`IgnoreOptions`, `Ignore`, `Ignored`) and the directory walk (`Walk`). It imports nothing of the module. |
| `tools` | The built-in tools (`For`), the subprocess runner, glob matching, the symbol table and the reference engine (`References`). |
| `outline` | Declarations of a source file: `go/ast` for Go, tree-sitter for fourteen more languages with cgo. |
| `codemode` | The code-mode tool, on `go.starlark.net`. |
| `mcp` | MCP client and tool pool on the official MCP Go SDK. |
| `guard` | The shell boundary: `Check`, `Restricted`, `Allow`, `AllowAll`. |
| `prompt` | The assembled system prompt: base prompt plus the active tools' guidelines. |
| `catalog` | The embedded model catalog of the `anthropic`, `openrouter` and `ollama` providers, their tiers, `Lookup` and `Select`. |
| `provider` | Cost arithmetic, the per-session tool-schema cache, deferred-tool splitting, and the tool-pairing repair (`RepairToolPairing`) the chat-style wires share. |
| `provider/anthropic` | The Anthropic wire (direct, Vertex AI, Bedrock) over the official SDK. |
| `provider/openrouter` | The OpenRouter wire over OpenRouter's Go SDK: `openrouter.New`, with its own retry loop, reasoning replay, netted usage and cost, and key redaction. |
| `provider/ollama` | The Ollama wire over Ollama's Go package: `ollama.New`, with `num_ctx` on every request, tool schemas cut to what the SDK carries, thinking and argument replay, zero-cost usage and named failures. |
| `provider/faux` | A scripted provider for offline tests and demos; `ToolCall` and `Text` script a turn. |
| `wire` | Bounded, strict parsing of untrusted JSON, and frame readers. |
| `sandbox` | The host-side sandbox API: `Spec`, `Capabilities`, `Provider`, `Sandbox`, `Open`, `CheckSpec`, `LocalDir`, `Tar`/`TarOut`, the protocol version and the errors. Its internal packages hold the protocol, the agent service, the attach and proxy code, the tar entry rules and the rsync adapter. |
| `sandbox/local` | `local.Unisolated()`: an in-process provider with no isolation, for tests and development. |
| `sandbox/podman` | `podman.New(Options)`: the first isolating provider, a hardened podman or docker container per sandbox. |
| `sandbox/landlock` | `landlock.New(Options)`: the agent as a child process confined by Landlock, on Linux with ABI 4. |
| `sandbox/gateway` | `gateway.Dial(ctx, url, Options)`: a provider whose sandboxes live on a remote gateway. |
| `sandbox/gatewayd` | Separate module: the gateway server library (`gatewayd.New`, `Serve`, `Shutdown`) and its `agentkit-gateway` command. |
| `mcpserve` | Separate module: serves a `Runner`'s tools over MCP to a harness on the same machine, on stdio or on loopback HTTP behind a bearer token (`mcpserve.New`, `Server.ServeStdio`, `Server.ServeLoopback`, `Endpoint`). |
| `sandbox/cmd/agentkit-sandboxd` | The agent command a sandbox image installs at `sandbox.AgentPath`; serves the protocol on its stdio. |
| `codesearch` | Separate module: zoekt-backed `code_search`, built for a workspace (`codesearch.New(ws, opts)`) and opted in with `tools.Options{Index: idx}`. |

## Make targets

| Target | Does |
|---|---|
| `make check` | `fmt`, `vet`, `lint`, `test-short`: the fast check, run while working |
| `make check-release` | `fmt`, `vet`, `lint`, `test`: adds the slow gates (cross-target build and vet, host cgo build, nested `make` runs); run before a release or merge |
| `make test-short` | `go test -short ./...` in every module |
| `make test` | `go test ./...` in every module: the root, `codesearch`, `sandbox`, `sandbox/gatewayd`, `mcpserve`, `examples/sandbox`, `examples/gateway` and `examples/harness` |
| `make build` | `go build ./...` in every module |
| `make coverage` | The root module's tests with a coverage report |
| `make clean` | Remove `build/` and the coverage profile |
| `make vet` | `go vet` in every module |
| `make lint` | `golangci-lint` in every module; skipped when not installed |
| `make fmt` | `gofmt -l -w .` |
| `make tidy` | `go mod tidy` in every module |
| `make doc` | `go doc -all` for every public package of the root, `codesearch`, `sandbox`, `sandbox/gatewayd` and `mcpserve` modules, written as text to `docs/generated/` (committed; an orientation aid for agents) |
| `make proto` | Regenerate the sandbox agent and gateway protocols' Go code and their recorded hashes; needs `protoc`, installs the pinned plugins into `build/bin` |
| `make build-containers` | Build the sandbox image (`containers/agentkit-sandbox/Containerfile`) with podman, tagged with the version and `latest`; not part of `check` |
| `make sandbox-podman-test` | Build the sandbox image and run the podman provider's integration tests against them; not part of `check` |
| `make sandbox-landlock-test` | Run the landlock provider's tests verbosely, with the public `GitRepo` clone of `AGENTKIT_SANDBOX_GIT_URL`; not part of `check` |
| `make sandbox-gateway` | Build `build/bin/agentkit-gateway` with `-trimpath`; `AGENTKIT_VERSION=…` stamps its version |
| `make sandbox-gateway-test` | Build the sandbox base image and run the gateway's opt-in Podman tests against it; not part of `check` |
| `make provider-live-test` | Run the live provider smoke tests (`AGENTKIT_LIVE=1`): one real `read_file` loop on each provider whose prerequisites are present, skipping the rest and naming what is missing; costs money; not part of `check` or `check-release` |

## Documentation

| Document | Covers |
|---|---|
| [`docs/api.md`](docs/api.md) | The public Go packages and where their reference lives (the library has no REST API). |
| [`docs/architecture.md`](docs/architecture.md) | Shape, package graph, packages, the embedder's path, the data flow of one call and of one run, invariants, nested calls, code mode, MCP serving, the MCP client, symbol and reference navigation (`tools.References`), sandboxes, the testing layout. |
| [`docs/configuration.md`](docs/configuration.md) | `agentkit.Config`, environment variables, provider, tool, code-mode, MCP and sandbox options, the model catalog. |
| [`docs/cli.md`](docs/cli.md) | The `agentkit-sandboxd` and `agentkit-gateway` commands: flags, transport, the token file and exit codes. |
| [`docs/releasing.md`](docs/releasing.md) | Publishing the sandbox images: the version tags, GHCR visibility, re-runs and local builds. |
| [`docs/DEPS.md`](docs/DEPS.md) | Why each direct dependency is allowed, and the libraries considered and rejected. |
| [`examples/README.md`](examples/README.md) | The eight example programs and how to configure an application. |
| [`docs/prd/`](docs/prd) | Product requirements. |
| [`docs/errata/`](docs/errata) | Where the code diverges from a spec, and why. |
| [`docs/archive/`](docs/archive) | The superseded 0.4.2 PRD and findings from the deleted provider wires. |

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
