# agentkit-go

AgentKit is two things. **The hands**: workspace mechanics any agent can use —
the file, search, navigation and shell tools (`tools`), source outlines
(`outline`), indexed code search (`codesearch`, a separate module), code mode
(`codemode`), the MCP client pool (`mcp`), the shell guard (`guard`), and
sandboxes that run those tools somewhere else (`sandbox`).
**One driver**: `agentkit.Config` and `agentkit.New`, a minimal agent loop over
the Anthropic Messages API (`provider/anthropic`, on the official Go SDK),
with a scripted provider (`provider/faux`) for offline tests. It serves no
network API: the library listens on no socket; `agentkit-sandboxd` serves the
agent service on its stdio, and `agentkit-gateway`, a separate module, is the
one network service.

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

### Sandboxes

A sandbox is an ephemeral environment that holds one workspace. A
`sandbox.Provider` creates one, `Sync` puts files into it, and `Tools`
returns a drop-in replacement for `tools.All`: one proxy per built-in, with
the same definition, whose every call runs in the AgentKit agent
(`agentkit-sandboxd`) inside the sandbox over the `agentkit.sandbox.v1` gRPC
protocol. The driver does not change and does not know a tool is remote.

```go
sb, err := local.Unisolated().Create(ctx, sandbox.Spec{
	Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted},
})
if err != nil {
	return err // a Spec the provider cannot honour is refused, naming the field
}
defer sb.Close(ctx)
// Only what changed moves; the same ignore rules as the file tools apply.
if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: "./project"}); err != nil {
	return err
}
proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
if err != nil {
	return err
}
agent, err := agentkit.New(agentkit.Config{
	Client: client,
	Model:  "claude-opus-5-5",
	Tools:  proxies,
	Guard:  guard.Restricted(guard.Options{AllowedPrograms: []string{"go"}}),
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

The driver is optional. Another harness — Claude Code, Pi, any MCP client —
can work in a sandbox with code mode and the built-in tools as its only
tools: the embedder serves them over MCP and keeps the sandbox, the guard
and the policy on its side. [`examples/harness`](examples/harness) does that
for one task of a spec package: it clones a repository into a sandbox, runs
`specr` inside it, measures the spec's checks, starts the harness with its
own tools off, and measures again. Offline it runs on `local` with a
scripted stand-in for the harness.

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
| `sandbox.Provider` | `Capabilities()`, `Create(ctx, Spec) (Sandbox, error)`, `Reap(ctx) ([]string, error)` |
| `sandbox.Sandbox` | `ID`, `Spec`, `Agent`, `Sync(ctx, Source)`, `Tools(ctx, ToolOptions) ([]core.Tool, error)`, `Export(ctx, path, Destination)`, `Close` |
| `sandbox.CheckSpec` | `CheckSpec(caps Capabilities, spec Spec) (Spec, error)` |
| `local.Unisolated` | `Unisolated() sandbox.Provider` |

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
| `sandbox` | The host-side sandbox API: `Spec`, `Capabilities`, `Provider`, `Sandbox`, `CheckSpec`, `LocalDir`, `Tar`/`TarOut`, the protocol version and the errors. Its internal packages hold the protocol, the agent service, the attach and proxy code, the tar entry rules and the rsync adapter. |
| `sandbox/local` | `local.Unisolated()`: an in-process provider with no isolation, for tests and development. |
| `sandbox/podman` | `podman.New(Options)`: the first isolating provider, a hardened podman or docker container per sandbox. |
| `sandbox/landlock` | `landlock.New(Options)`: the agent as a child process confined by Landlock, on Linux with ABI 4. |
| `sandbox/gateway` | `gateway.Dial(ctx, url, Options)`: a provider whose sandboxes live on a remote gateway. |
| `sandbox/gatewayd` | Separate module: the gateway server library (`gatewayd.New`, `Serve`, `Shutdown`) and its `agentkit-gateway` command. |
| `sandbox/cmd/agentkit-sandboxd` | The agent command a sandbox image installs at `sandbox.AgentPath`; serves the protocol on its stdio. |
| `codesearch` | Separate module: zoekt-backed `code_search`, opted in with `tools.Options{Index: idx}`. |

## Make targets

| Target | Does |
|---|---|
| `make check` | `fmt`, `vet`, `lint`, `test`; run before committing |
| `make test` | `go test ./...` in every module: the root, `codesearch`, `sandbox`, `sandbox/gatewayd`, `examples/sandbox`, `examples/gateway` and `examples/harness` |
| `make build` | `go build ./...` in every module |
| `make coverage` | The root module's tests with a coverage report |
| `make clean` | Remove `build/` and the coverage profile |
| `make vet` | `go vet` in every module |
| `make lint` | `golangci-lint` in every module; skipped when not installed |
| `make fmt` | `gofmt -l -w .` |
| `make tidy` | `go mod tidy` in every module |
| `make doc` | `go doc -all` for every public package of the root, `codesearch`, `sandbox` and `sandbox/gatewayd` modules, written as text to `docs/generated/` (committed; an orientation aid for agents) |
| `make proto` | Regenerate the sandbox agent and gateway protocols' Go code and their recorded hashes; needs `protoc`, installs the pinned plugins into `build/bin` |
| `make build-containers` | Build the sandbox image (`containers/agentkit-sandbox/Containerfile`) with podman, tagged with the version and `latest`; not part of `check` |
| `make sandbox-podman-test` | Build the sandbox image and run the podman provider's integration tests against them; not part of `check` |
| `make sandbox-landlock-test` | Run the landlock provider's tests verbosely, with the public `GitRepo` clone of `AGENTKIT_SANDBOX_GIT_URL`; not part of `check` |
| `make sandbox-gateway` | Build `build/bin/agentkit-gateway` with `-trimpath`; `AGENTKIT_VERSION=…` stamps its version |
| `make sandbox-gateway-test` | Build the sandbox base image and run the gateway's opt-in Podman tests against it; not part of `check` |

## Documentation

| Document | Covers |
|---|---|
| [`docs/api.md`](docs/api.md) | The public Go packages and where their reference lives (the library has no REST API). |
| [`docs/architecture.md`](docs/architecture.md) | Package graph, the data flow of a run, invariants, nested calls, code mode, the MCP client, `Workspace.References`, sandboxes. |
| [`docs/configuration.md`](docs/configuration.md) | `agentkit.Config`, environment variables, provider, tool, code-mode, MCP and sandbox options, the model catalog. |
| [`docs/cli.md`](docs/cli.md) | The `agentkit-sandboxd` and `agentkit-gateway` commands: flags, transport, the token file and exit codes. |
| [`docs/releasing.md`](docs/releasing.md) | Publishing the sandbox images: the version tags, GHCR visibility, re-runs and local builds. |
| [`docs/DEPS.md`](docs/DEPS.md) | Why each direct dependency is allowed, and the libraries considered and rejected. |
| [`examples/README.md`](examples/README.md) | The seven example programs and how to configure an application. |
| [`docs/prd/`](docs/prd) | Product requirements. |
| [`docs/errata/`](docs/errata) | Where the code diverges from a spec, and why. |
| [`docs/archive/`](docs/archive) | The superseded 0.4.2 PRD and findings from the deleted provider wires. |

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
