# Configuration

AgentKit is a library. It has no config file of its own and no global state.
Configuration is one of:

1. fields on `core.AgentConfig` and the per-provider `Options` structs,
2. environment variables, read at request time,
3. three optional TOML sections that an *embedding application* may load with
   the parsers the SDK ships (`[mcp]`, `[mcp_server]`, `[plugins]`), plus
   per-skill `skill.toml` manifests and per-plugin `plugin.toml` manifests.

[`examples/README.md`](../examples/README.md) walks through the three decisions
every application has to make (credential, base URL, model). This file is the
reference.

## Environment variables

### Credentials

The first non-empty variable in a vendor's list wins. `RequestOptions.Env` is
consulted before the process environment, and an empty override value falls
through rather than masking.

| Vendor | Variables, in order | Sent as |
|---|---|---|
| `anthropic` | `ANTHROPIC_API_KEY` | `x-api-key` |
| | `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN` | `Authorization: Bearer` (the OAuth token also adds `anthropic-beta: oauth-2025-04-20`) |
| | on Vertex: `ANTHROPIC_AUTH_TOKEN` only — the API key and the OAuth token are Anthropic-issued and never sent to Google | `Authorization: Bearer` |
| `openai` | `OPENAI_API_KEY` | `Authorization: Bearer` |
| `google` | `GOOGLE_GENERATIVE_AI_API_KEY`, `GEMINI_API_KEY`, `GOOGLE_API_KEY` | `x-goog-api-key` |
| | ambient when `GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_CLOUD_PROJECT` or `CLOUDSDK_CORE_PROJECT` is set | transport-supplied |
| `ollama` | `OLLAMA_API_KEY` (usually unset) | `Authorization: Bearer` |
| `openrouter`, `deepseek`, `xai`, `groq`, `together`, `moonshot` | `<VENDOR>_API_KEY` | `Authorization: Bearer` |
| any other vendor on the OpenAI-compatible wire | `<VENDOR>_API_KEY` | `Authorization: Bearer` |

Resolution yields a three-valued state: `resolved`, `ambient` (a base URL, or
a credential chain the transport holds) and `none`. Pre-flight checks must
treat `ambient` as configured. Stringifying a `ModelAuth` or `Credential`
redacts it (first 4 and last 4 characters; shorter secrets entirely).

Long-running processes should supply a `provider.Credentials` store on the
provider options instead. To have expiring OAuth tokens refreshed, register the
vendor's refresh flow with `Credentials.SetRefresher(vendorID, refresher,
provider.RefreshOptions{})`: every request for that vendor then resolves its
credential through `EnsureFresh`, which refreshes a token inside the validity
floor once — double-checked inside the per-vendor lock — however many turns
race. Without a registered refresher the stored credential is used as it is.
A stored `Credential` whose `Scheme` is left at its zero value,
`provider.SchemeVendor`, is sent the way the vendor sends it: an access token
as `Authorization: Bearer`, an API key with the scheme of the vendor's first
credential variable above (a bearer on OpenAI, `x-api-key` on Anthropic).
Set `SchemeAPIKey` or `SchemeBearer` to override.

### Base URLs and deployments

| Variable | Effect |
|---|---|
| `ANTHROPIC_BASE_URL` | Proxy or gateway in front of Anthropic (both deployments). |
| `ANTHROPIC_VERTEX_BASE_URL` | Proxy in front of Vertex; beats `ANTHROPIC_BASE_URL` when the Vertex deployment is on. |
| `OPENAI_BASE_URL` | Azure OpenAI, a gateway, or any OpenAI-compatible server. |
| `GOOGLE_GEMINI_BASE_URL` | A proxy, or a Vertex host (which selects the Vertex deployment). |
| `OLLAMA_HOST` | The Ollama server. |
| `<VENDOR>_BASE_URL` | Same, for `openrouter`, `deepseek`, `xai`, `groq`, `together`, `moonshot` and any other compatible vendor. |
| `CLAUDE_CODE_USE_VERTEX` | Selects Claude on Vertex. Read for truth: `0`/false is an explicit off that vetoes the other signals. |
| `ANTHROPIC_VERTEX_PROJECT_ID` | GCP project for Claude on Vertex. Selects the deployment alone only when no Anthropic-direct credential is set. |
| `CLOUD_ML_REGION` | Vertex location for Claude (`GOOGLE_CLOUD_LOCATION`, `CLOUDSDK_COMPUTE_REGION` also work; default `global`). |
| `GOOGLE_CLOUD_PROJECT`, `CLOUDSDK_CORE_PROJECT` | May *supply* a Vertex project once something else selected the deployment; they never select it. |
| `GOOGLE_CLOUD_LOCATION`, `CLOUDSDK_COMPUTE_REGION` | Vertex location (Gemini); default `global`. |

Why the deployment switches are ranked rather than OR-ed:
[`errata/01_vertex_deployment_selection.md`](errata/01_vertex_deployment_selection.md)
and rulings L-7 / L-12 in [`PROVIDERS.md`](PROVIDERS.md).

### SDK variables

| Variable | Read by | Effect |
|---|---|---|
| `AGENTKIT_TELEMETRY` | `provider` | `0` or `false` disables every attribution header (`x-agentkit-version`, `user-agent`). `AgentConfig.Attribution = false` does the same in code. |
| `AGENTKIT_MODEL` | `examples/*` only | Model spec (`vendor/id`, or a bare unambiguous id) overriding an example's default. The library never reads it. |
| `AGENTKIT_PLUGINS_NO_RUN` | `examples/plugins` only | Non-empty skips the live-model part of the example. |

An MCP HTTP server's API key is read from the variable *named* by
`[mcp_server] api_key_env`; there is no fixed name.

Subprocess tools run with a reduced environment (`tools.ReducedEnv`): `PATH`,
`HOME`, `LANG`, `TMPDIR`, `TERM` and a few non-secret cloud settings are kept;
provider-prefixed variables and anything ending in `_TOKEN`, `_SECRET`,
`_API_KEY`, `_PASSWORD` or `_CREDENTIALS` are stripped.

## `core.AgentConfig`

| Field | Meaning / default |
|---|---|
| `Model` | `*core.Model`; obtain with `catalog.ResolveModel("vendor/id")`. |
| `Provider` | Vendor id, used only for credential resolution and catalog lookup. |
| `MaxTokens` | Upper bound, clamped to the model. Nil → `core.DefaultMaxTokens` (32768), not the model cap. |
| `Temperature`, `TopP` | Optional sampling parameters; dropped where the catalog row says the model does not accept them. |
| `SystemPrompt`, `PromptBlocks` | Base prompt and extra sections appended after the built-in ones. A `SystemPrompt` replaces the built-in base instructions and universal guidelines; the active tools' own guidelines (`Tool.PromptGuidelines`, and the shell guidelines) still follow it. |
| `StopPolicy` | `func(StopContext) bool`; compose with `stop.Any`. `StopContext.Usage`, like `RunResult.Usage`, is the current run's usage — a budget policy on a reused agent is a per-run budget; `Agent.Usage()` is the lifetime total. |
| `ErrorOnLimit` | A limit stop also returns `ErrMaxTurns` / `ErrBudgetExceeded`. Default false. |
| `ParallelTools` | Run a tool batch's calls concurrently. |
| `ToolChoice` | `""` (auto), or a forced choice. |
| `ThinkingLevel` | `""`, `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; clamped to the catalog row's ladder. |
| `ToolPolicy` | `Tools`, `NoTools` (`all` / `builtin`), `ToolNames`, `ExcludeTools`, `CustomTools`; resolved in that order. Non-nil empty `Tools` means no tools. A `CustomTools` entry must set exactly one of `Handler` and `Execute`; construction fails otherwise, as `RegisterTool` does. |
| `BeforeToolCall`, `AfterToolCall` | The authorization boundary and post-processing. A shell tool in the set with a nil `BeforeToolCall` fails the run (`ErrUnguardedExecute`); use `guard.Restricted` or `guard.AllowAll`. `AfterToolCall` receives the handler's `ToolResult` by value and the mutable `Result *ToolResultMessage`. `ToolResultMessage.Metadata` carries the tool's structured metadata (persisted, in events, never sent to the model). |
| `Hooks` | `OnTurnStart`, `OnTurnEnd`, `OnAgentDone`, `OnError`, `OnSessionStart`, `OnSessionEnd`, `OnAudit`. Observation only. `OnAudit` receives a `tool_call` event for every call, including one blocked, refused, aborted or cut off by `max_tokens`; `ErrorCode` says why. |
| `Middleware` | Axis 1; last registered is outermost. |
| `TransformContext` | Bound closure run before every model call; build with `compaction.NewContextTransform`. Its context carries a usage reporter: a model call made inside it (a summary) reports its usage with `core.ReportUsage`, and the agent adds it to `Agent.Usage`. |
| `SteeringQueueMode`, `FollowUpQueueMode` | `QueueOneAtATime` (default) or `QueueDrainAll`. |
| `SessionID` | Identifier carried on requests and audit events. |
| `TrustProject` | The one place to state project trust; skills discovery derives from it (`skills.ConfigFor`). Default false. |
| `Plugins` | A `PluginRegistry` held on the config, not globally. |
| `Tracer` | Tool-span tracer; nil is a no-op. A span lives until `Span.End`, which may come after `StartSpan`'s callback has returned (`middleware.Tracing` ends a model-call span when the response completes), so an adapter must not end the span when the callback returns. |
| `Attribution` | `*bool`; nil means on. |
| `CacheRetention` | `none`, `short`, `long`. |
| `RequestOptions` | Per-request `Headers` (nil value deletes a default), `TimeoutMs`, `MaxRetries` (nil → 0), `MaxRetryDelayMs` (nil → 60000), `SessionID`, `CacheRetention`, `Deferred`, `Env`, `Transport`, `StreamFn`, `OnPayload`, `OnResponse`. |
| `StreamOptions` | Streaming behaviour. |
| `Providers` | `core.ProviderRegistry`. Nil means `agentkit.DefaultProviders()`, which is **empty**: register the wire APIs you use (`agentkit.RegisterDefaults(&cfg, anthropic.Provider(anthropic.Options{}), …)`). |
| `SessionStore` | Optional durable log; must be empty at construction (`ErrSessionNotEmpty`) — fold a non-empty one with `NewAgentFromSession`, which refuses a `Resume` not folded from the store's current head (resume another branch with `session.FoldLeaf`) and, with no resolver, a `cfg.Model` whose provider, API or id differs from the log's. |
| `OnPersistError` | Mandatory seam for store failures when the store is subscribed internally. A failed (or panicking) `Append` is reported here and through `OnError`, and the run continues with the message still in the model's history: the log loses the entry, the model does not lose the turn. A panic in this hook or in the store is contained. |

Header precedence, lowest to highest: attribution defaults, provider/auth
headers, `Model.Headers`, `RequestOptions.Headers`. A nil value at a higher
layer deletes the name.

## Provider options

Every `Options` struct has `BaseURL`, `HTTPClient`, `Getenv` (injectable
environment), `Retry` (`provider.RetryPolicy`: `MaxRetries`, base / max delay,
`MaxRetryDelay` default 60 s, base delay default 500 ms, max delay default 8 s),
`Attribution` and `Credentials`. Those that price turns take `BillingLookup`.

| Package | Extra options |
|---|---|
| `anthropic` | `Betas` (dated beta headers, opt-in; `compact-2026-01-12` enables server-side compaction), `VertexProject`, `VertexLocation`. |
| `openai` | `Auth` (override the vendor table), `ToolPrefix`, `OnToolPrefixSync`, `MaxSSEEventBytes`. |
| `openairesponses` | `Auth`, `ServiceTier`, `ToolPrefix`, `OnToolPrefixSync`, `MaxSSEEventBytes`. |
| `google` | `VertexProject`, `VertexLocation`, `ToolPrefix`, `OnToolPrefixSync`, `MaxSSEEventBytes`. |
| `ollama` | `ToolPrefix`, `OnToolPrefixSync`, `MaxLineBytes`. |

## Built-in tool options (`tools.Options`)

| Field | Default |
|---|---|
| `Workspace` | `tools.NewWorkspace("")` — the current directory. File tools cannot leave it. |
| `SpillDir` | A per-workspace directory under `os.TempDir()`. Spill files are the embedder's to clean. |
| `DisableSpill` | false. |
| `Env` | `tools.ReducedEnv(nil)`. |
| `Ignore` | The real gitignore environment; `tools.NoGlobalExcludes()` pins an empty global layer for tests. |
| `Symbols` | `tools.SymbolOptions{}`. Configures the symbol table behind `find_symbol` and the outline runner shared by `file_outline` and `find_symbol`. Fields: `MaxFiles` (file-count bound per build/refresh pass; default 50 000), `MaxDuration` (wall-time bound; default 2 s), `DisableCtags` (force heuristic/go-ast backends only), `Runner` (override the default `CtagsRunner`; the seam tests use). |
| `Index` | `nil` (`tools.Index`). When set, `All()` appends the index's tools after the built-ins and `write_file`, `edit_file` and the shell tools call `Index.Invalidate` to keep the index fresh. See the `codesearch` module below. |

`tools.All` returns `read_file`, `write_file`, `edit_file`, `list_files`,
`find_files`, `search_files`, `file_outline`, `find_symbol`, `execute`,
`run_command` and `powershell` on every platform. When `Options.Index` is set,
`All()` appends the index's tools (e.g. `code_search`) after the built-in list.
`fetch_url` is constructed separately and sits behind the SSRF guard. Output
limits: 50 KB per result, `read_file` 2000 lines, `search_files` 100 matches
and 500 characters per line, `find_files` 200 by default (cap 1000),
`list_files` 200 by default (cap 500), `find_symbol` 20 results by default
(cap 50), symbol-table build bounded at 50 000 files and 2 s.

### `code_search` limits (when `codesearch` index is active)

| Parameter | Default | Cap |
|---|---|---|
| `max_files` | 10 | 25 |
| `context_lines` | 2 when absent; an explicit 0 means none | 20 (`tools.MaxSearchContextLines`) |
| `query` max length | — | 1 024 bytes |
| query timeout | — | 10 s |
| result byte limit | — | 50 KB (`tools.DefaultByteLimit`) |

### `codesearch.Options`

| Field | Default | Meaning |
|---|---|---|
| `Ignore` | — | `tools.IgnoreOptions`; pass the same value as `tools.Options.Ignore`. |
| `Env` | `tools.ReducedEnv(nil)` | Environment for the ctags runner. |
| `DisableCtags` | `false` | Force heuristic/go-ast backends only; no ctags process. |
| `Runner` | `nil` | Override the default ctags runner (test seam). |
| `MaxFiles` | 100 000 | File-count bound for the index build. |
| `MaxBytes` | 1 GiB | Total indexed content bound. |
| `MaxBuildTime` | 60 s | Wall-time bound for the index build. |
| `TempDir` | `os.TempDir()` | Where shard files are written. |

## Subprocess runner (`tools.Run`, `tools.RunArgv`)

`tools.Run` executes a shell command through the platform's shell ladder;
`tools.RunArgv` runs a program directly from an argv without a shell. Both
share one implementation, return `ExecResult` and accept `ExecOptions`.

### `ExecOptions`

| Field | Zero-value behaviour |
|---|---|
| `Dir` | The process working directory. |
| `Timeout` | No timeout. |
| `MaxBytes` | `DefaultByteLimit` (50 KB). |
| `KeepHead` | `false`: keep the tail of the output (tail truncation). When `true`, keep the first `MaxBytes` bytes (head truncation). |
| `SpillDir` | Disabled. When set, the complete output is written to a temporary file in this directory. |
| `LogPath` | Disabled. When set, the complete interleaved output is written to the named file as it arrives. A relative path is resolved against `Dir` (or the process working directory when `Dir` is empty). Missing parent directories are created with mode `0o700`; the file is created or truncated with mode `0o600`. When set, `LogPath` replaces `SpillDir` for that call: no temporary spill file is created, and `ExecResult.SpillPath` reports the absolute log path. The SDK never deletes the file. |
| `Stdin` | `nil`: the child's stdin is the null device. When non-nil, the reader is copied into the child's stdin pipe and the pipe is closed on `io.EOF`. |
| `Env` | `nil` means `ReducedEnv(nil)` (same rule as `tools.Options.Env`): credentials are stripped and `PATH`, `HOME`, `LANG`, `TMPDIR` and `TERM` are kept. A non-nil slice, including an empty one, is used verbatim. Pass `os.Environ()` for the full inherited environment. |
| `DrainIdle` | `defaultDrainIdle` (2 s). How long the output pipe must stay quiet after the child exits before draining stops. Every read re-arms it. |
| `DrainCeiling` | `defaultDrainCeiling` (10 s). Absolute bound on the post-exit drain. |

### `ExecResult`

| Field | Meaning |
|---|---|
| `Output` | The truncated output (head or tail, per `KeepHead`). |
| `Outcome` | One of `ok`, `exit`, `signal`, `timeout`, `abort`. |
| `ExitCode` | The child's exit code. 128+signum on unix for signal-killed children. |
| `Truncated` | Whether the output was truncated. |
| `TotalBytes` | Total bytes the child wrote, before truncation. |
| `SpillPath` | Path to the spill or log file, when one exists. |
| `Duration` | Wall-clock duration to the child's exit. |
| `IOErr` | A byte-moving failure that did not stop the process: a `Stdin` read error, a log or spill write error, or `Wait` reporting an I/O completion failure. `Outcome` is still classified from the exit status. |

**Outcome rules.** For every call that starts a process, `Outcome` is exactly
one of the five values, classified from the state at the child's exit:

- `abort` — the caller's `ctx` was done at that moment, including when the
  caller's own deadline expired.
- `timeout` — the deadline derived from `ExecOptions.Timeout` had expired and
  the caller's `ctx` had not.
- `signal` — the child was killed by a signal (128+signum on unix).
- `exit` — the child exited with a non-zero status.
- `ok` — the child exited with status 0.

A non-nil `error` return means nothing started (empty argv, program not found,
`LogPath` unopenable, pipe or fork failure). `IOErr` reports failures that
happened after the process started.

## TOML sections

These are loaded by the embedding application, with `ParseConfig`, from a file
it chooses. Parsing is lenient (locally authored): an unknown key or a value of
the wrong type is a `Diagnostic`, and the rest of the file still loads. The
file is parsed as TOML 1.0 (`go-toml/v2`); a grammar error rejects the file
with its line. The values read are strings, booleans, decimal integers,
floats, string arrays, tables and arrays of tables. Any other well-formed
value (dates, multi-line strings, inline tables, hex/octal/binary integers,
non-string arrays) skips its key with a warning. A duplicate key warns and
the last value wins.

### `[[mcp.servers]]` — `mcp.ParseConfig`

| Key | Meaning |
|---|---|
| `name` | Required, unique. Keys the pool, the tool prefix and audit events. |
| `command`, `args`, `dir` | Spawn a stdio server. |
| `url` | Connect over Streamable HTTP. If both `command` and `url` are set, the command wins and a warning is emitted; one of them is required. |
| `env` (table) | Environment for the subprocess; values may use `${VAR}`, resolved from the secrets store at spawn time. |
| `headers` (table) | Headers for every request to a `url` server; `${VAR}` supported. The default HTTP client does not follow redirects. |
| `tool_prefix` | Override the default `<name>__`. |
| `timeout_s` | Integer or float seconds; default 30. |
| `allow_sampling` | Permit the server to request sampling. Default false. |
| `per_session_call_limit` | Default 1000; negative disables. |
| `per_session_reconnect_limit` | Default 3; negative means never reconnect. |
| `transport` | Obsolete. `"streamable-http"` is accepted with a warning; any other value (e.g. `"sse"`) is an error and the server is dropped. |

### `[mcp_server]`

| Key | Meaning |
|---|---|
| `enabled` | Default **false**; a missing key cannot turn it on. |
| `transport` | `"stdio"` (default) or `"http"`. |
| `port` | TCP port for http mode. Required when `transport = "http"`. |
| `api_key_env` | Name of the environment variable holding the API key. Required for http; the server refuses to start when it is missing, empty or unset. |

HTTP mode binds `127.0.0.1` only. See [`api.md`](api.md).

### `[plugins]` — `plugins.ParseConfig`

| Key | Meaning |
|---|---|
| `paths` | Directories searched for `plugin.toml`. There is no implicit search path. |
| `disabled` | Plugin names to skip. |

### `plugin.toml`

`[plugin]` with `name` and `module` (both required), `description`, `source`
and `kinds` (string array of plugin categories).

### `skill.toml`

Top-level keys (an optional `[skill]` table is an accepted alias): `name`,
`version`, `description`, `author`, `sdk_min_version`, `archetypes`,
`overrides`, `disable_model_invocation`. Tables: `[skill.tools]`
(`module`, `factory`), `[skill.security]` (`allowlist_extend`),
`[skill.session]` (`max_turns_add`), `[skill.subagent]` (`archetype`, `mode`,
`prompt_template`, `result_key`, `on_failure` = `abort` | `warn` | `skip`,
default `warn`). The prompt body is always `prompt.md` beside the manifest.
`injection`, `keywords`, `prompt_file` and `prompt_position` are removed keys
and produce a diagnostic.

Skill discovery roots: built-in (`_skills/`, opt-in via `builtinDir`), user
(`~/.nightshift/skills`), project (`<workdir>/.nightshift/skills`, admitted
only when `TrustProject` is true). Name collisions resolve user > project >
built-in. The user tier is skipped when the home directory cannot be resolved.

## Limits on untrusted input

`wire.Defaults()`: 16 MiB per message, 1,000,000 elements per container, depth
64, 2,000,000 nodes. MCP HTTP bodies are capped at the smaller of
`HTTPOptions.MaxBodyBytes` and the decoder's bound.

## Compatibility

Go 1.27 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64 with cgo off; the host is also
built with cgo on. cgo code lives only in `//go:build cgo` files, each with a
pure-Go fallback.
