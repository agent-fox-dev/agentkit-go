# API

AgentKit is a library. It serves no REST API, and its root and sandbox modules
listen on no socket; its API is the set of public Go packages below. Two nested
modules serve: `agentkit-gateway` (`sandbox/gatewayd`) speaks gRPC and is
described in [`cli.md`](cli.md) and the `sandbox/gatewayd` package
documentation, and `mcpserve` serves a runner's tools over MCP to a harness on
the same machine.

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
| `catalog` | The embedded model catalog of the three providers (`anthropic`, `openrouter`, `ollama`), with their tiers. `catalog.Lookup` turns a Claude model id into a `core.Model`; `catalog.Select` turns a provider, a model (a tier or an id) and an effort into the `Selection` a run will use. |
| `prompt` | System prompt assembly. |
| `schema` | The structured, transformable tool-schema value type. |
| `workspace` | The workspace every scenario opens first: containment, ignore rules and the directory walk (`workspace.Open`). |
| `tools` | The built-in tools: file, search, navigation and shell, built for a workspace with `tools.For`. |
| `guard` | The shell guard, the `execute` boundary in non-interactive deployments; `guard.Allow` is the allowlist-only form, and `guard.Commands` is the reading of a shell line it judges. |
| `outline` | Source outlines, the extension-to-language table (`outline.LangForExt`) and the test-file convention the tools rank by (`outline.IsTestFile`). |
| `codemode` | A tool that runs a model-written Starlark script whose functions are other tools. |
| `mcp` | The MCP client pool. |
| `wire` | The bounded, strict decoder for bytes AgentKit did not produce. |

## Nested modules

| Module | Purpose |
|---|---|
| `codesearch` | Indexed code search, backed by zoekt. |
| `sandbox` | The sandbox API (`sandbox.Provider`, and `sandbox.Open`, which creates, fills and equips a sandbox in one call), the agent protocol, and the providers `sandbox/local`, `sandbox/landlock`, `sandbox/podman` and `sandbox/gateway`. The image tests are in `sandbox/images`. |
| `sandbox/gatewayd` | The gateway server (`gatewayd.Server`) behind the `agentkit-gateway` command. A program that embeds it can authenticate with `Config.Authenticate` (an `Authenticator`), read the caller in its backend with `gatewayd.PrincipalFromContext` and end any sandbox with `Server.Destroy` (`gatewayd.ErrNotHeld`). |
| `mcpserve` | Serves an `agentkit.Runner`'s tools over MCP: `mcpserve.New`, `Server.ServeStdio`, `Server.ServeLoopback`, and the `Endpoint` it returns with the harness configuration strings `ClaudeConfig` and `PiConfig`. |

How the modules relate is in [`architecture.md`](architecture.md); the knobs
each package takes are in [`configuration.md`](configuration.md).
