# Architecture

How the module is laid out, what depends on what, and how a run flows through
it. The code is authoritative; when this file and a package's doc comment
disagree, fix this file.

## Shape

One Go module (`github.com/agentfox/agentkit-go`, `go 1.26.5`), standard
library only. That is enforced by `internal/policy` (see
[`DEPS.md`](DEPS.md)). Two nested modules carry their own dependency budget and
are not in the root build graph: [`difftest/`](../difftest) and
[`examples/flatline/`](../examples/flatline).

The root package holds the `Agent` and nothing else
([ADR 01](adr/01-keep-the-root-package-to-the-agent.md)). Everything else is a
sub-package. [`errata/package_layout.md`](errata/package_layout.md) maps the
PRD's unqualified names to where they live now.

## Package graph

Imports between first-party packages, taken from the source (tests excluded;
`internal/*` omitted). An arrow means "imports".

| Package | Imports |
|---|---|
| `core` | `jsonx`, `schema` |
| `schema` | `jsonx` |
| `outline` | nothing first-party |
| `jsonx`, `wire`, `imagex` | nothing first-party |
| `catalog` | `core` |
| `stop`, `guard`, `compaction` | `core` |
| `middleware` | `core`, `provider` |
| `provider` | `core`, `schema`, `wire` |
| `provider/{anthropic,openai,openairesponses,google,ollama}` | `core`, `catalog`, `provider`, `schema`; a few reuse a sibling wire package's helpers (`openairesponses` → `openai` and `wire`; `google` → `anthropic`, `openai`) |
| `provider/faux` | `core` |
| `session` | `core`, `jsonx` |
| `tools` | `core`, `imagex`, `schema` (exports `Walk`, `CtagsRunner`) |
| `mcp` | `core`, `schema`, `wire` |
| `plugins` | `core`, `provider`, `session` |
| `skills` | `core`, `plugins` |
| `prompt` | `core`, `skills`, `tools` |
| `subagent` | `core`, `schema`, `stop` |
| `.` (root, `agentkit`) | `core`, `compaction`, `guard`, `imagex`, `middleware`, `prompt`, `session`, `skills` |

Rules that follow from it:

- `core` is the vocabulary and owns every interface seam (`Tool`,
  `ProviderClient`, `SessionStore`, `PluginRegistry`, `Middleware`). It imports
  no other part of the SDK except the two leaf encoders.
- Nothing imports the root package except `examples/` and tests. `session`
  says so explicitly: the log format has no opinion about the loop.
- Provider packages are never imported by the root. A caller registers the wire
  APIs it wants, so a program that only wants the loop does not pull in
  `net/http`.

## Packages

| Package | Owns |
|---|---|
| `.` | `Agent`, constructors (`NewAgent`, `NewAgentWithHistory`, `NewAgentFromSession`), the loop (`loop.go`), the tool batch executor (`batch.go`), image normalization at the history boundary (`images.go`), audit emission (`audit.go`), deferred responses (`deferred.go`), `DefaultProviders` / `RegisterDefaults`, skill loading and audit (`Agent.LoadSkills`, `Agent.AuditSkills`; `skillsconfig.go`), the `execute` guard check (`execguard.go`). |
| `core` | Messages, content blocks, events and their discriminated JSON form, `EventStream`, `AgentConfig`, `Tool`, `ToolPolicy`, argument preparation, `Model`, `Usage`, stop reasons, errors, audit events, the plugin and tracer interfaces. |
| `catalog` | Embedded model catalog (`catalog.json`), `ResolveModel`, sibling cloning, `max_tokens` and thinking-level clamping. |
| `provider` | What every wire API shares: send-time transcript repair, HTTP transport and retry, credential resolution (`VendorAuth`, `ModelAuth`, `Credentials`), header precedence, cost arithmetic, SSE/NDJSON decoding, partial-JSON salvage, deferred-tool splitting. |
| `provider/anthropic` | Anthropic Messages, direct and Claude-on-Vertex. |
| `provider/openai` | OpenAI Chat Completions and every compatible host (OpenRouter, DeepSeek, xAI, Groq, Together, Moonshot, anything else by `<VENDOR>_*`). |
| `provider/openairesponses` | OpenAI Responses. |
| `provider/google` | Gemini, AI Studio and Vertex, with opt-in `CachedContent`. |
| `provider/ollama` | Ollama native chat. |
| `provider/faux` | Scripted provider for offline tests and demos. |
| `middleware` | Axis 1 wrappers over the model call: `Retry`, `Budget`, `Caching`, `Tracing`, `RateLimit`, and `CacheMeter`. |
| `compaction` | The context transform, four strategies, summarizers, summary validation, token estimate. |
| `session` | Append-only JSONL log, damage-tolerant loader, branch tree, fold into construction inputs, recorder, `OpenOrCreate`. |
| `outline` | Source-file declaration listing: Go backend (`go/ast`), ctags backend (via an injected `Runner`), anchored-line heuristics for ten languages, and a `none` fallback. Standard-library-only; no first-party imports. |
| `tools` | Built-in tools, workspace containment, output accumulator, process control, glob, layered gitignore, `fetch_url` behind the SSRF guard. `Walk` exposes the single shared directory traversal behind workspace confinement. `CtagsRunner` supplies the ctags process lifecycle for `outline.Options.Runner`. |
| `guard` | The `execute` authorization boundary: `Restricted`, `AllowAll`. |
| `stop` | Stop policies. |
| `subagent` | Delegation as a tool, named definitions, parallel runs. |
| `prompt` | Assembly of the system prompt. |
| `skills` | Skill manifests, three-tier discovery, trust gate, project context files, prompt blocks, activation. |
| `plugins` | Four plugin categories, registry, manifest discovery, import lint, conformance `Validate`. |
| `mcp` | MCP client (stdio and Streamable HTTP), tool pool, and server (stdio and HTTP), on `wire`. |
| `wire` | Bounded strict decoder for untrusted bytes; frame readers. |
| `jsonx` | Order-preserving JSON. |
| `schema` | JSON Schema value and typed combinators. |
| `imagex` | Image normalization to a provider's inline-image limits (resize, re-encode, base64 budget). WebP is forwarded untouched. |
| `internal/toml` | Hand-rolled TOML subset for manifests and config. |
| `internal/diag` | The shared non-fatal `Diagnostic`. |
| `internal/policy` | Tests only: dependency, cgo and cross-target gates. |
| `internal/testkit` | Shared test helpers. |
| `cmd/validate-plugins` | Reference driver for `plugins.Validate` ([CLI](cli.md)). |
| `difftest` | Nested module: wire-level differential harness. |
| `_skills/` | Built-in skills shipped as data (`code-review`). |

## Data flow of one run

```
Agent.Run / Stream / Continue
  └─ runLoop (loop.go)
       per turn:
         1. TransformContext (compaction checkpoint applied, then estimate)
         2. build Request from history + tools + system prompt
         3. Middleware chain (last registered is outermost) ── Axis 1
         4. ProviderClient.Stream  ── provider.* repairs the transcript,
            encodes the wire body, resolves auth + headers, sends via the
            retrying transport, decodes SSE/NDJSON into core events
         5. assistant message folded into history, events emitted,
            session store appended
         6. tool_use blocks present? (never the stop reason decides)
              └─ batch.go: prepare (sequential: policy, BeforeToolCall,
                 plugin hooks, argument repair) → execute (parallel or
                 sequential) → finalize (AfterToolCall, image normalization,
                 one ToolResultMessage per call)
         7. StopPolicy evaluated at the turn boundary
  └─ terminal marker, OnSessionEnd hook, RunResult
```

Extension axes:

1. **Middleware** wraps the whole model call on canonical types.
2. **Hooks** observe (`OnTurnStart`, `OnTurnEnd`, `OnAgentDone`, `OnError`,
   session and audit hooks). They never intercept.
3. **Interceptors** — `BeforeToolCall` / `AfterToolCall` — are the
   authorization boundary. Plugin hooks run after `BeforeToolCall` and can only
   narrow.
4. **`RequestOptions.OnPayload`** is the post-serialization seam.

## Invariants worth knowing before editing

- **One `ToolResultMessage` per call**; coalescing into a provider's wire shape
  is the provider's job.
- **The abort decision for a tool batch is made once**, before any handler
  starts.
- **The batch finalize mutex is batch-scoped**, never the agent mutex.
- **Panics in third-party code are contained** (middleware, stop policies,
  transforms, argument preparation, tracers, hooks). A panicking `StopPolicy`
  stops the run.
- **A shell tool with no `BeforeToolCall`** fails the run with
  `ErrUnguardedExecute`; `guard.AllowAll` is the explicit opt-out.
- **Untrusted bytes go through `wire`**: bounds before allocation, duplicate
  keys rejected, case-sensitive matching. Locally authored config decodes
  leniently and reports diagnostics.
- **No global state, no `init()` registration.** Providers, plugins and tracers
  live on the config.
- **Session entries are appended, never rewritten**; a compaction is an entry
  and the checkpoint is applied as a view on each request.

## Testing layout

- Unit and property tests sit beside their package.
- `testdata/golden/` holds request-body and session-log goldens. They pin
  regression, not vendor truth ([`PROVIDERS.md`](PROVIDERS.md)).
- `internal/policy` holds the dependency, cgo, cross-target build and ledger
  gates.
- `difftest/` is the differential harness; it reports DARK until a vendor
  capture exists.
- `examples/testing` shows how an embedder tests its own agent code offline with
  `provider/faux`.

Run everything with `make check` (fmt, vet, lint, test for the root and
`difftest` modules).
