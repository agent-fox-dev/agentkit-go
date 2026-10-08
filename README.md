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

[`examples/`](examples/) has fifteen of them — chat, streaming, a coding agent,
durable sessions, delegation, writing your own tools, plugins, MCP in both
directions, a standalone MCP server, mid-run steering, skills and project context,
a test file showing how to test the agent code *you* write, and three finished
applications: [`triage`](examples/triage), which triages a bug report into a
structured GitHub issue, [`cleaner`](examples/cleaner), which takes that issue
and lands the fix, and [`flatline`](examples/flatline), which implements a whole
spec pack task group by task group. Six need no API key. The nested
[`examples/codesearch`](examples/codesearch) module shows the optional
code-search index on its own and wired into an agent. [`examples/README.md`](examples/README.md)
is the configuration reference: which environment variable each vendor reads,
what a base URL does and does not buy you, and the three decisions every
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
prints seven behaviours — five the specification originally got wrong, plus a
kill-and-resume across two "processes" and three concurrent delegations.

## Status

TBD

## Documentation

| Document | Covers |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Package graph, data flow of a run, invariants. |
| [`docs/configuration.md`](docs/configuration.md) | Environment variables, `AgentConfig`, provider and tool options, TOML sections and manifests. |
| [`docs/cli.md`](docs/cli.md) | `validate-plugins`, `difftest`, make targets, example program flags. |
| [`docs/api.md`](docs/api.md) | The MCP server's HTTP and stdio surface. |
| [`examples/README.md`](examples/README.md) | Overview of the examples |
| [`docs/prd/`](docs/prd) | Requirements. |

## Packages

| Package | What it owns |
|---|---|
| `mcp` | Model Context Protocol, client and server, on the standard library: JSON-RPC, stdio transport, tool pool, HTTP serving with API-key auth. |
| `plugins` | Four plugin categories, registry, manifest discovery, import lint, conformance report. |
| `wire` | Bounded, strict decoder for bytes AgentKit did not produce: hand-rolled scanner, reflective binder, framed reader. |
| `jsonx` | Order-preserving JSON. Decodes once, marshals in slice order at every depth. |
| `schema` | Structured JSON Schema value + typed combinators. No reflection, no codegen. |
| `core` | Canonical vocabulary and every interface seam: messages, content blocks, events, `EventStream`, `Tool`, `ProviderClient`. |
| `catalog` | Embedded model catalog, resolution, sibling-cloning, `max_tokens` and thinking-level clamping. |
| `session` | Append-only JSONL log, damage-tolerant loader, branch tree, resume fold. |
| `skills` | Skill manifests (TOML via `go-toml/v2/unstable`, read leniently with line-numbered diagnostics), progressive disclosure, project context files, and the default-off trust gate. |
| `outline` | Source-file declaration listing with three backends (`go/ast`, universal-ctags via an injected runner, anchored-line heuristics) and a `none` fallback. Standard-library-only. |
| `tools` | Built-in tools (`file_outline`, `find_symbol`, `find_references` and the nine others), path containment, bounded accumulator, process control, glob (`doublestar` plus smart-case and basename matching), a layered gitignore engine, `fetch_url` behind an SSRF guard, `Walk` (the single shared directory traversal), `CtagsRunner` (ctags process lifecycle for `outline`), the in-memory symbol table behind `find_symbol`, and the reference engine and cache behind `find_references`. |
| `provider` | Send-time transcript repair, HTTP transport + retry, credential resolution, header precedence, cost arithmetic, SSE decoding — everything shared by every wire API. |
| `provider/{anthropic,openai,google,ollama,faux}` | One wire API each, encode and decode. |
| `codesearch` | Separate module: zoekt-backed `code_search` tool with ranked, file-grouped results, lazy index build, dirty-file overlay and `find_symbol` acceleration. Opt in with `tools.Options{Index: idx}`. |
| `difftest` | Separate module: the NFR-TEST-06/07 differential harness — canonicalizing comparator, key-order side channel, divergence ledger, exit machine. |
| `stop` | The built-in stop policies: `AfterTurns`, `OverBudget`, `AfterDuration`, `WhenToolCalled`, `Any`, `Never`. |
| `middleware` | Axis 1: `Retry`, `Budget`, `Caching`, `Tracing`, `RateLimit` (on `golang.org/x/time/rate`), and the `CacheMeter` behind `Agent.CacheStats`. |
| `compaction` | The context transform, four strategies, two summarizers, the REQ-GO-16 summary taxonomy and the anchored token estimate. |
| `prompt` | The assembled system prompt: base instructions, per-tool guidelines, skills and project-context blocks. |
| `imagex` | Image normalization to a provider's inline-image limits; used at the history boundary. |
| `guard` | The execute boundary: `Restricted` (a program allowlist plus operator rejection) and `AllowAll`. |
| `subagent` | Delegation: `Tool` over an agent factory, named `Definition`s in a `Registry`, `RunParallel`. The one package above the root. |
| `.` (root) | `Agent`, its constructors, the loop, the batch executor, provider registration. |

## License

MIT.
