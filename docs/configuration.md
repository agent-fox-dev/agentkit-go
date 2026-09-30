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
| `anthropic` | `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN` | `Authorization: Bearer` |
| | `ANTHROPIC_API_KEY` | `x-api-key` |
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
provider options instead; refresh is serialized per vendor.

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
| `SystemPrompt`, `PromptBlocks` | Base prompt and extra sections appended after the built-in ones. |
| `StopPolicy` | `func(StopContext) bool`; compose with `stop.Any`. |
| `ErrorOnLimit` | A limit stop also returns `ErrMaxTurns` / `ErrBudgetExceeded`. Default false. |
| `ParallelTools` | Run a tool batch's calls concurrently. |
| `ToolChoice` | `""` (auto), or a forced choice. |
| `ThinkingLevel` | `""`, `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; clamped to the catalog row's ladder. |
| `ToolPolicy` | `Tools`, `NoTools` (`all` / `builtin`), `ToolNames`, `ExcludeTools`, `CustomTools`; resolved in that order. Non-nil empty `Tools` means no tools. |
| `BeforeToolCall`, `AfterToolCall` | The authorization boundary and post-processing. A shell tool in the set with a nil `BeforeToolCall` fails the run (`ErrUnguardedExecute`); use `guard.Restricted` or `guard.AllowAll`. |
| `Hooks` | `OnTurnStart`, `OnTurnEnd`, `OnAgentDone`, `OnError`, `OnSessionStart`, `OnSessionEnd`, `OnAudit`. Observation only. |
| `Middleware` | Axis 1; last registered is outermost. |
| `TransformContext` | Bound closure run before every model call; build with `compaction.NewContextTransform`. |
| `SteeringQueueMode`, `FollowUpQueueMode` | `QueueOneAtATime` (default) or `QueueDrainAll`. |
| `SessionID` | Identifier carried on requests and audit events. |
| `TrustProject` | The one place to state project trust; skills discovery derives from it (`skills.ConfigFor`). Default false. |
| `Plugins` | A `PluginRegistry` held on the config, not globally. |
| `Tracer` | Tool-span tracer; nil is a no-op. |
| `Attribution` | `*bool`; nil means on. |
| `CacheRetention` | `none`, `short`, `long`. |
| `RequestOptions` | Per-request `Headers` (nil value deletes a default), `TimeoutMs`, `MaxRetries` (nil → 0), `MaxRetryDelayMs` (nil → 60000), `SessionID`, `CacheRetention`, `Deferred`, `Env`, `Transport`, `StreamFn`, `OnPayload`, `OnResponse`. |
| `StreamOptions` | Streaming behaviour. |
| `Providers` | `core.ProviderRegistry`. Nil means `agentkit.DefaultProviders()`, which is **empty**: register the wire APIs you use (`agentkit.RegisterDefaults(&cfg, anthropic.Provider(anthropic.Options{}), …)`). |
| `SessionStore` | Optional durable log; must be empty at construction (`ErrSessionNotEmpty`) — fold a non-empty one with `NewAgentFromSession`. |
| `OnPersistError` | Mandatory seam for store failures when the store is subscribed internally. |

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

`tools.All` returns `read_file`, `write_file`, `edit_file`, `list_files`,
`find_files`, `search_files`, `execute`, `run_command` and `powershell` on
every platform. `fetch_url` is constructed separately and sits behind the SSRF
guard. Output limits: 50 KB per result, `read_file` 2000 lines, `search_files`
100 matches and 500 characters per line, `find_files` 200 by default (cap 1000),
`list_files` 200 by default (cap 500).

## TOML sections

These are loaded by the embedding application, with `ParseConfig`, from a file
it chooses. Parsing is lenient (locally authored): an unknown key or a value of
the wrong type is a `Diagnostic`, and the rest of the file still loads. The
parser accepts a TOML subset: strings, booleans, integers, floats, string
arrays, tables and arrays of tables.

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

Go 1.26.5 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64; cgo is rejected.
