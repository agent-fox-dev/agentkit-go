# Architecture

How the module is laid out, what depends on what, and how a run flows through
it. The code is authoritative; when this file and a package's doc comment
disagree, fix this file.

## Shape

One Go module (`github.com/agentfox/agentkit-go`, `go 1.27`). Maintained
third-party modules are used where they replace hand-rolled infrastructure
([PRD 09](prd/09-replace-hand-rolled-code-with-libraries.md)). cgo is allowed only in `//go:build cgo` files, and every package keeps a
pure-Go fallback: `internal/policy` builds the four supported targets with cgo
off and the host with cgo on. Four nested
modules carry their own `go.mod` and are not in the root build graph:
[`codesearch/`](../codesearch),
[`difftest/`](../difftest),
[`examples/codesearch/`](../examples/codesearch) and
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
| `tools` | `core`, `imagex`, `outline`, `schema` (exports `Walk`, `CtagsRunner`, `file_outline`, `find_symbol`, `find_references`) |
| `codesearch` (nested module) | `tools`, `core`, `schema`, `outline`; plus `github.com/sourcegraph/zoekt` (confined to this module) |
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
| `middleware` | Axis 1 wrappers over the model call: `Retry`, `Budget`, `Caching`, `Tracing`, `RateLimit` (a `golang.org/x/time/rate` limiter), and `CacheMeter`. |
| `compaction` | The context transform, four strategies, summarizers, summary validation, token estimate. |
| `session` | Append-only JSONL log, damage-tolerant loader, branch tree, fold into construction inputs, recorder, `OpenOrCreate`. |
| `outline` | Source-file declaration listing: Go backend (`go/ast`), ctags backend (via an injected `Runner`), anchored-line heuristics for ten languages, and a `none` fallback. The extension table covers the programming languages universal-ctags parses; `LangFor` also reads a `.h` header's content to tell C++ from C. A file ctags gives nothing for falls back to the heuristic. See `docs/errata/01_outline_language_coverage.md`. Standard-library-only; no first-party imports. |
| `tools` | Built-in tools, workspace containment, output accumulator, process control, glob (`github.com/bmatcuk/doublestar/v4` plus smart-case and bare-pattern basename matching), layered gitignore, `fetch_url` behind the SSRF guard (every resolved address and the connect-time address are checked against `code.dny.dev/ssrf`'s IANA special-purpose table; IPv6 outside 2000::/3 is refused). `RunArgv` is the embedder's process runner (no shell, argv-based, with stdin, head/tail truncation, log file, reduced environment and a pinned outcome contract). `Walk` exposes the single shared directory traversal behind workspace confinement. `CtagsRunner` supplies the ctags process lifecycle for `outline.Options.Runner`. `file_outline` returns a file's declarations with line ranges; `find_symbol` searches the workspace by declaration name, backed by a lazily built, bounded in-memory symbol table that is refreshed after `write_file`, `edit_file` and the shell tools run; `find_references` searches for callers and usages of declarations across the workspace with exact Go type resolution and outline attribution, backed by a lazily built, bounded in-memory reference cache (`referenceCache`). |
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
| `imagex` | Image normalization to a provider's inline-image limits (resize with `golang.org/x/image/draw` Catmull-Rom, quality ladder, base64 budget) for JPEG, PNG, GIF and WebP (decoded with `golang.org/x/image/webp`; a WebP that must shrink is re-encoded as JPEG). APNG, CMYK JPEG and non-IHDR PNG are refused. |
| `internal/toml` | Lenient TOML reader for manifests and config: `github.com/pelletier/go-toml/v2/unstable` (pinned) parses; a small adapter folds it into an ordered `Table` with line numbers and "duplicate warns, last wins". |
| `internal/diag` | The shared non-fatal `Diagnostic`. |
| `internal/policy` | Tests only: the cross-target (cgo off) and host (cgo on) build gates. |
| `internal/testkit` | Shared test helpers. |
| `cmd/validate-plugins` | Reference driver for `plugins.Validate` ([CLI](cli.md)). |
| `codesearch` | Nested module: zoekt-backed `code_search` tool, lazy index build, dirty-file overlay, `find_symbol` acceleration. Imports `tools`, `core`, `schema`, `outline` from the root and `github.com/sourcegraph/zoekt` (pinned). |
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
                 sequential) → finalize (AfterToolCall, metadata copy,
                 image normalization, one ToolResultMessage per call)
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

## Symbol and reference navigation

The `tools` package provides in-memory, workspace-confined symbol lookup and reference finding:

- `find_symbol` searches workspace declarations by name, backed by a lazily built, bounded in-memory symbol table (`symbolTable`).
- `find_references` finds usages and callers of declarations across the workspace. It combines exact Go type resolution (`go/parser`, `go/types` with workspace-local imports and synthetic external stubs) with outline-attributed lexical matching for other languages, attributing each site to its enclosing declaration from the outline (or `<file>` at top level).
- **Reference caching**: A thread-safe in-memory cache (`refCache`, of type `referenceCache`) persists parsed Go packages, candidate file sets, and outlines across queries. File edits (`write_file`, `edit_file`) mark touched files and packages dirty; shell executions (`execute`, `run_command`, `powershell`) mark all cached entries for revalidation. Queries lazily re-parse dirty Go packages and re-outline modified files without unbounded memory growth or cross-session persistence.

## Testing layout

- Unit and property tests sit beside their package.
- `testdata/golden/` holds request-body and session-log goldens. They pin
  regression, not vendor truth.
- `internal/policy` holds the cross-target (cgo off) and host (cgo on) build
  gates.
- `difftest/` is the differential harness; it reports DARK until a vendor
  capture exists.
- `examples/testing` shows how an embedder tests its own agent code offline with
  `provider/faux`.

Run everything with `make check` (fmt, vet, lint, test for the root,
`codesearch` and `difftest` modules).
