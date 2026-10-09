# A agentkit-go

A Go agent SDK. The loop, the tool system and the provider abstraction are
ordinary Go you can read and step through — nothing is hidden inside a
subprocess or a graph engine.

**Dependencies.** Maintained third-party modules replace hand-rolled
infrastructure where they carry the same guarantees
([PRD 09](docs/prd/09-replace-hand-rolled-code-with-libraries.md)). cgo is
allowed only behind `//go:build cgo` with a pure-Go fallback;
[`internal/policy`](internal/policy/crosstarget_test.go) builds the four
supported targets with cgo off and the host with cgo on.

```bash
go test ./...          # everything, offline, no API key
go run ./examples/agentdemo
```

[`examples/`](examples/) has five programs, each with its own README:
[`agentdemo`](examples/agentdemo) drives the real loop against a scripted
provider with no key and no network; [`codingagent`](examples/codingagent) is a
coding agent over the built-in file and shell tools;
[`customtools`](examples/customtools) shows how to write your own tools;
[`mcp`](examples/mcp) borrows an MCP server's tools; and
[`codemode`](examples/codemode) shows code mode: one tool that runs a
model-written Starlark script over other tools, so the model can chain and
parallelize calls and filter their results before anything reaches the
conversation (no key). [`examples/README.md`](examples/README.md) is the
configuration reference: which environment variables the Anthropic provider
reads, what a base URL does and does not buy you, and the decisions every
embedding application has to make.

To talk to a real model, register a wire API on the config. Nothing is
registered by import side effect, so the root package never drags `net/http`
into a consumer that only wants the loop:

```go
reg := agentkit.DefaultProviders()
reg.Register(anthropic.Provider(anthropic.Options{}))

cfg := core.AgentConfig{Model: model, Providers: reg}   // credential per REQ-AUTH-03
```

The demo drives the real loop against a scripted provider with no network, and
prints four behaviours the specification originally got wrong.

## Documentation

| Document | Covers |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Package graph, data flow of a run, invariants, the MCP client. |
| [`docs/configuration.md`](docs/configuration.md) | Environment variables, `AgentConfig`, provider and tool options, the `[mcp]` TOML section. |
| [`docs/cli.md`](docs/cli.md) | Make targets and example program flags. |
| [`docs/api.md`](docs/api.md) | Why there is no network API; `Workspace.References`. |
| [`docs/DEPS.md`](docs/DEPS.md) | Why each deliberately adopted dependency is there (`go.starlark.net`). |
| [`examples/README.md`](examples/README.md) | Overview of the examples |
| [`docs/prd/`](docs/prd) | Requirements. |

## Packages

| Package | What it owns |
|---|---|
| `.` (root) | `Agent`, its constructors, the loop, the batch executor, nested tool calls, provider registration. |
| `core` | Canonical vocabulary and every interface seam: messages, content blocks, events, `EventStream`, `Tool`, `ProviderClient`. |
| `tools` | Built-in tools (`read_file`, `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`, `execute`, `run_command`), path containment, bounded accumulator, process control, glob (`doublestar` plus smart-case and basename matching), a layered gitignore engine, `Walk` (the single shared directory traversal), the in-memory symbol table behind `find_symbol`, and the reference engine and cache behind `find_references`. |
| `outline` | Source-file declaration listing: `go/ast` for Go, in-process tree-sitter grammars for fourteen other languages when built with cgo (none without), and a `none` fallback. |
| `codemode` | The code-mode tool: `New(tools, opts)` runs a model-written, sandboxed Starlark script over the bound tools, with every call going through the agent's nested-call pipeline. On `go.starlark.net` ([ruling](docs/DEPS.md)). |
| `mcp` | Model Context Protocol client on the official Go SDK (all revisions, negotiated): tool pool with qualified names, subprocess servers with a reduced environment and respawn, result cap, strict decoding at every trust boundary. |
| `guard` | The execute boundary: `Restricted` (a program allowlist plus operator rejection) and `AllowAll`. |
| `prompt` | The assembled system prompt: base instructions, per-tool guidelines, extra blocks. |
| `catalog` | Embedded model catalog, resolution, sibling-cloning, `max_tokens` and thinking-level clamping. |
| `provider` | Credential resolution, HTTP transport + retry, header precedence, cost arithmetic, SSE decoding, the per-session tool-schema cache. |
| `provider/anthropic` | The Anthropic Messages wire (direct and Vertex), encode and decode, with send-time transcript repair. |
| `provider/faux` | A scripted provider for offline tests and demos. |
| `wire` | Bounded, strict parser for bytes AgentKit did not produce, on the standard library's `encoding/json/jsontext`: size, depth, container and node bounds, duplicate-key rejection; plus framed readers. |
| `jsonx` | Order-preserving JSON. Decodes once, marshals in slice order at every depth. |
| `schema` | Structured JSON Schema value + typed combinators. No reflection, no codegen. |
| `codesearch` | Separate module: zoekt-backed `code_search` tool with ranked, file-grouped results, lazy index build, dirty-file overlay and `find_symbol` acceleration. Opt in with `tools.Options{Index: idx}`. |

## License

MIT.
