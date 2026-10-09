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
| `outline` | nothing first-party; `github.com/tree-sitter/go-tree-sitter` and grammar modules in `//go:build cgo` files |
| `jsonx`, `wire`, `imagex` | nothing first-party |
| `catalog` | `core` |
| `stop`, `guard`, `compaction` | `core` |
| `middleware` | `core`, `provider` |
| `provider` | `core`, `schema`, `wire` |
| `provider/{anthropic,openai,openairesponses,google,ollama}` | `core`, `catalog`, `provider`, `schema`; a few reuse a sibling wire package's helpers (`openairesponses` → `openai` and `wire`; `google` → `anthropic`, `openai`) |
| `provider/faux` | `core` |
| `session` | `core`, `jsonx` |
| `tools` | `core`, `imagex`, `outline`, `schema` (exports `Walk`, `file_outline`, `find_symbol`, `find_references`) |
| `codesearch` (nested module) | `tools`, `core`, `schema`, `outline`; plus `github.com/sourcegraph/zoekt` (confined to this module) |
| `mcp` | `core`, `schema`, `wire`; plus `github.com/modelcontextprotocol/go-sdk` |
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
| `outline` | Source-file declaration listing with real line ranges. Go files use `go/ast` in every build. With cgo, Python, JavaScript, TypeScript/TSX, Java, Kotlin, C#, Scala, Rust, C, C++, PHP, Ruby, Lua and shell are parsed in-process by tree-sitter grammars driven by tags queries (`outline/treesitter*.go`, `//go:build cgo`); without cgo those files are `none`. Anything outside the extension table is `none` and is not read; `LangFor` also reads a `.h` header's content to tell C++ from C. `CommentAndStringSpans` gives `find_references` the comment and string ranges of a file from the same grammars. Languages universal-ctags used to cover with no maintained Go-binding grammar (Swift, Perl, Elixir, Erlang, OCaml, Clojure, Lisp/Scheme, Julia, R, SQL, Terraform, Protobuf, Thrift, Fortran, COBOL, Ada, Pascal, VHDL, SystemVerilog, Raku, Tcl, D, Elm, GDScript, PowerShell, Vim script, Objective-C, CUDA) are not outlined. No first-party imports. |
| `tools` | Built-in tools, workspace containment, output accumulator, process control, glob (`github.com/bmatcuk/doublestar/v4` plus smart-case and bare-pattern basename matching), layered gitignore, `fetch_url` behind the SSRF guard (every resolved address and the connect-time address are checked against `code.dny.dev/ssrf`'s IANA special-purpose table; IPv6 outside 2000::/3 is refused). `RunArgv` is the embedder's process runner (no shell, argv-based, with stdin, head/tail truncation, log file, reduced environment and a pinned outcome contract). `Walk` exposes the single shared directory traversal behind workspace confinement. `file_outline` returns a file's declarations with line ranges; `find_symbol` searches the workspace by declaration name, backed by a lazily built, bounded in-memory symbol table that is refreshed after `write_file`, `edit_file` and the shell tools run; `find_references` searches for callers and usages of declarations across the workspace with exact Go type resolution and outline attribution, backed by a lazily built, bounded in-memory reference cache (`referenceCache`). |
| `guard` | The `execute` authorization boundary: `Restricted`, `AllowAll`. |
| `stop` | Stop policies. |
| `subagent` | Delegation as a tool, named definitions, parallel runs. |
| `prompt` | Assembly of the system prompt. |
| `skills` | Skill manifests, three-tier discovery, trust gate, project context files, prompt blocks, activation. |
| `plugins` | Four plugin categories, registry, manifest discovery, import lint, conformance `Validate`. |
| `mcp` | AgentKit's layer over the official MCP Go SDK, which owns the protocol and version negotiation: the tool pool (qualified names, collision checks, schema conversion, `${VAR}` resolution), subprocess spawning with process-group kill and a reduced environment, respawn, result cap, call limit, audit and sampling gate on the client; the handler adapter, concurrency bound, audit and API-key/Origin HTTP middleware and `Run` on the server; and strict `wire` checks at the stdio, inbound-HTTP and HTTP-response boundaries. |
| `wire` | Bounded strict decoder for untrusted bytes, on `encoding/json/v2` and `encoding/json/jsontext`: a token loop adds the size, depth, container-length and node bounds to jsontext's grammar and duplicate-name rejection; `Bind` is v2 with unknown members rejected, exact-case names, REQ-SEC-12.2 integer rules and the `Validator` hook. Frame readers. |
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
                 image normalization, one ToolResultMessage per call);
                 a tool with ReachableTools runs with a NestedCaller on its
                 context (nested.go, below)
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
  `ErrUnguardedExecute`; `guard.AllowAll` is the explicit opt-out. A shell tool
  reachable through a wrapper counts too:
  `... (wrapper "code_mode" reached shell tool "execute")`.
- **Untrusted bytes go through `wire`**: bounds before allocation, duplicate
  keys rejected, case-sensitive matching. Locally authored config decodes
  leniently and reports diagnostics. Where a library decodes untrusted bytes
  (the MCP SDK), `wire.Guard` checks them first at the transport boundary.
- **`core.Tool.OutputSchema` never reaches a provider.** It documents the shape
  of `ToolResult.Data` for programmatic callers; `core.ToolWire` has no such
  field, so request bodies are the same with or without it. It is not checked
  at runtime. `schema.Validate` does not evaluate a root combinator, so to
  check Data against a schema like `read_file`'s root `oneOf`, validate it as
  a property's value.
- **No global state, no `init()` registration.** Providers, plugins and tracers
  live on the config.
- **Session entries are appended, never rewritten**; a compaction is an entry
  and the checkpoint is applied as a view on each request.

## Nested tool calls

A tool can call other tools on the model's behalf, for example a script runner
that turns tools into functions. It declares them up front in
`core.Tool.ReachableTools`, and nothing else is reachable.

- **Registration.** `NewAgent`, `NewAgentWithHistory` and `RegisterTool`
  refuse a hierarchy in which a tool reaches itself:
  `agentkit: reachable tools cycle detected: toolA -> toolB -> toolA`. They
  also refuse one in which a wrapper reaches a `Terminating` tool:
  `agentkit: terminating tool "finish" cannot be reached through wrapper "code_mode"`.
  Cycles are found by name along the path, because tools are values.
- **Policy.** `ToolPolicy.Resolve` applies `NoTools` (`builtin`), `ToolNames`
  and `ExcludeTools` to reachable tools at every depth.
  - A wrapper left reaching nothing is dropped, so an allowlist must name the
    children as well as the wrapper.
  - `ToolPolicy.Tools`, when set, is used verbatim, as before.
- **Queries.** `core.ReachableTools(tools)` is the deduplicated transitive
  closure in depth-first order. `Agent.ReachableTools()` is that closure over
  the resolved set, which is what a check like "this run is read-only" must
  inspect.
- **Dispatch.** `executeBatch` attaches a `nestedCaller` (`nested.go`) to the
  context of every tool that declares `ReachableTools`. The caller is bound to
  that call's id and name and to the wrapper's resolved reachable tools. The
  tool calls `core.CallNested(ctx, calls...)`; with no caller attached, it
  gets `core.ErrNoNestedCaller`.
- **Pipeline.** A nested call goes through the same steps as a direct one:
  1. prepare and validate the arguments;
  2. `BeforeToolCall`, whose context carries `ParentToolUseID` and
     `ParentToolName`;
  3. the plugin veto;
  4. the handler, inside an `agentkit.tool_call` span with
     `parent_tool_use_id`;
  5. an audit record with `ParentToolUseID`;
  6. `AfterToolCall`;
  7. `ToolExecutionStart`/`End` events with `ParentToolUseID`.

  Each nested call gets a fresh id. Preparation runs in call order. Handlers run
  concurrently unless `ParallelTools` is off or a `Sequential` tool is among
  the calls, and results come back in call order.
- **Results, not errors.** A tool the wrapper does not reach is
  `unknown_tool`. A block is `blocked_by_policy` with the reason, and a
  cancelled call is `aborted` with `Operation aborted`. `CallNested` returns
  an error only when an interceptor votes to end the run (`Block` with
  `Terminate`, or `AfterToolCall`'s `Terminate`). In that case the error is
  `core.ErrTerminated`, and the wrapper's result carries `Terminate` to the
  batch.
- **Terminate votes.** A nested handler's own terminate vote is dropped. The
  nested result's detail says `terminate vote ignored`, the wrapper's says
  `nested terminate vote ignored`, and the audit record sets
  `TerminateIgnored`.
- **Out of the transcript.** Nested calls emit no `ToolResultEvent`, never
  enter the history or the session log, and are not seen by stop policies.
  Usage a nested handler reports through `core.ReportUsage` still reaches the
  agent at once.

## Symbol and reference navigation

The `tools` package provides in-memory, workspace-confined symbol lookup and reference finding:

- `find_symbol` searches workspace declarations by name, backed by a lazily built, bounded in-memory symbol table (`symbolTable`).
- `find_references` finds usages and callers of declarations across the workspace. It combines exact Go type resolution (`go/parser`, `go/types` with workspace-local imports and synthetic external stubs; a use is `resolved` only when it resolves to the object declared at the target's position, and a use go/types cannot resolve because its type comes from a stubbed import is `lexical`) with outline-attributed lexical matching for other languages (a hit is `lexical` outside the comments and strings tree-sitter finds, and `text` in a build without cgo), attributing each site to its enclosing declaration from the outline (or `<file>` at top level).
- **Reference caching**: A thread-safe in-memory cache (`refCache`, of type `referenceCache`), created on the first `find_references` call and kept for the life of the tool set, holds the last complete type-check of the workspace's Go packages, their parsed files, file outlines and per-name candidate file lists. `write_file` and `edit_file` mark the written file and its Go package dirty; the shell tools (`execute`, `run_command`, `powershell`) mark everything for revalidation; a changed `.gitignore` or `.ignore` drops the Go check and the candidate lists. Before each query, deleted files are purged, the Go check is rebuilt if any Go package is dirty (re-parsing only the dirty packages' files), dirty files already outlined are outlined again, and candidate lists are dropped once anything changed. Nothing is written to disk.
- **Bounds**: each reference pass takes one file per Go file walked and per file read while looking for candidates from a budget of `SymbolOptions.MaxFiles` files and `SymbolOptions.MaxDuration` (50 000 and 2 s by default; `Workspace.References` always uses the defaults). The clock is also checked between Go package checks and between scanned candidate files; resolving uses in packages already checked is not interrupted. When the budget runs out the pass stops, keeps the sites found so far and sets `Partial`, and `find_references` appends `SymbolPartialMarker`. A Go check or candidate list cut short is not cached.

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
