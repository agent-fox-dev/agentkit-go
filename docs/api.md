# API

AgentKit is a library. It serves no REST API and listens on no socket; its API
is the set of public Go packages below. The one network service,
`agentkit-gateway`, speaks gRPC and is described in
[`cli.md`](cli.md) and the `sandbox/gatewayd` package documentation.

The authoritative reference is the package documentation: run `make doc` to
write `go doc -all` output for every public package to `docs/generated/` (committed), or read
it with `go doc -all <import path>`. This file is the map.

## Root module — `github.com/agent-fox-dev/agentkit-go`

| Package | Purpose |
|---|---|
| `agentkit` (root) | The driver: `agentkit.Config`, `agentkit.New`, the agent loop. The tool side it is built on: `agentkit.Toolset`, `agentkit.NewRunner` and `agentkit.Runner`, which run tool calls with no model. |
| `core` | The canonical vocabulary and every interface seam (`Tool`, `ProviderClient`, messages, events), and `core.Named`, which picks tools by name. |
| `provider/anthropic` | The Anthropic Messages wire API, on the official Go SDK. |
| `provider/faux` | A deterministic, scripted provider for offline tests, with `faux.ToolCall` and `faux.Text` for single-block turns. |
| `catalog` | The embedded Claude model catalog; `catalog.Lookup` turns a model id into a `core.Model`. |
| `prompt` | System prompt assembly. |
| `schema` | The structured, transformable tool-schema value type. |
| `workspace` | The workspace every scenario opens first: containment, ignore rules and the directory walk (`workspace.Open`). |
| `tools` | The built-in tools: file, search, navigation and shell, built for a workspace with `tools.For`. |
| `guard` | The shell guard, the `execute` boundary in non-interactive deployments; `guard.Allow` is the allowlist-only form. |
| `outline` | Source outlines. |
| `codemode` | A tool that runs a model-written Starlark script whose functions are other tools. |
| `mcp` | The MCP client pool. |
| `wire` | The bounded, strict decoder for bytes AgentKit did not produce. |

## Nested modules

| Module | Purpose |
|---|---|
| `codesearch` | Indexed code search, backed by zoekt. |
| `sandbox` | The sandbox API (`sandbox.Provider`, and `sandbox.Open`, which creates, fills and equips a sandbox in one call), the agent protocol, and the providers `sandbox/local`, `sandbox/landlock`, `sandbox/podman`, `sandbox/gateway` and `sandbox/images`. |
| `sandbox/gatewayd` | The gateway server (`gatewayd.Server`) behind the `agentkit-gateway` command. |

How the modules relate is in [`architecture.md`](architecture.md); the knobs
each package takes are in [`configuration.md`](configuration.md).
