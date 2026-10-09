# Configuration

AgentKit is a library. It has no config file of its own and no global state.
Configuration is one of:

1. fields on `agentkit.Config` and the option structs of the packages below,
2. environment variables, read when an Anthropic client is resolved,
3. one optional TOML section, `[mcp]`, that an embedding application may load
   with `mcp.ParseConfig`.

[`examples/README.md`](../examples/README.md) walks through the decisions every
application has to make (deployment, credential, model). This file is the
reference.

## `agentkit.Config`

`agentkit.New(cfg)` builds an `Agent` from it, and refuses a config it could
not run: no `Client` and no `Provider`, an empty `Model`, an invalid tool
hierarchy (a tool with both or neither of `Handler` and `Execute`, a
reachability cycle, a wrapper reaching a `Terminating` tool), or a reachable
shell tool with no `Guard`.

| Field | Meaning / default |
|---|---|
| `Client` | `*anthropic.Client` from the official SDK, built by `anthropic.Resolve` or by the caller. `New` builds `anthropic.Provider(model, anthropic.Options{Client: cfg.Client})` over it. |
| `Provider` | A `core.ProviderClient` (a test double such as `provider/faux`, or a custom provider). When set it is used instead of `Client`. |
| `Model` | Catalog id. `catalog.Lookup` supplies the context window, output cap, prices and thinking kind; an id the catalog does not list gets the default row (below). |
| `Effort` | `agentkit.Effort` (`core.Effort`): `""` (no thinking parameter), `EffortLow`, `EffortMedium`, `EffortHigh`, `EffortXHigh`, `EffortMax` (`low` … `max`). On an adaptive model it is sent as `thinking: {"type":"adaptive"}` with `output_config.effort`; on a budget model as `thinking: {"type":"enabled","budget_tokens":N}`, N from the catalog row; on a model without thinking, or at an effort the row does not list, neither is sent. Never moved to another effort. |
| `System` | The base prompt. Empty means `prompt.BaseInstructions` and `prompt.UniversalGuidelines`. Either way the active tools' own guidelines (`Tool.PromptGuidelines`, and the shell guidelines) follow it (`prompt.Build`). |
| `Prefix` | Messages sent after the system prompt and before the transcript on every request (`core.Request.Prefix`), with a cache breakpoint on their last block. Never recorded in the transcript. |
| `Tools` | The tools the agent can run. Each sets exactly one of `Handler` and `Execute`. |
| `Policy` | `core.ToolPolicy`: `Tools`, `ToolNames`, `ExcludeTools`, `NoTools` (`builtin` / `all`), `CustomTools`. Resolved over `Config.Tools` once, in `New`, and applied to what wrappers reach too. A non-nil empty `Policy.Tools` means no tools. |
| `Guard` | `core.BeforeToolCall`, the authorization boundary for every call, nested ones included. Required when a shell tool (`execute`, `run_command`) is reachable; otherwise `New` fails with an error wrapping `core.ErrUnguardedExecute`. Use `guard.Restricted(opts)` or `guard.AllowAll`. |
| `After` | `core.AfterToolCall`. Receives the handler's `ToolResult` by value and the mutable `Result *ToolResultMessage`; its `Terminate *bool` can set or clear a call's terminate vote. |
| `MaxTurns` | Ends a run that would go on past this many turns with `core.RunStopMaxTurns` and an error wrapping `core.ErrMaxTurns`, after the last turn's results are recorded. Zero is unbounded. |
| `MaxCostUSD` | Checked before each request against the run's cost so far: at or past it, the request is not sent and the run ends with `core.RunStopBudgetExceeded` and an error wrapping `core.ErrBudgetExceeded`. Usage a provider did not price is priced at the model's catalog row, so an unlisted id costs nothing and never trips it. Zero is unbounded. |
| `Timeout` | The run's deadline. Past it the run ends with `core.RunStopTimeout` and an error wrapping `context.DeadlineExceeded`. A cancellation from outside ends it with `core.RunStopAborted` and an error wrapping `core.ErrAborted` and the context's error. Zero is unbounded. |
| `Prune` | `agentkit.PruneOptions` (below); the zero value is off. |
| `MaxTokens` | Output cap per response, capped at the model's output cap. Zero means `agentkit.DefaultMaxTokens`. |

`agentkit.DefaultMaxTokens` is `32768`: the `max_tokens` sent when
`Config.MaxTokens` is zero, still capped at the model's own output cap. A
caller who wants the whole cap sets it.

### `agentkit.PruneOptions`

| Field | Meaning |
|---|---|
| `Threshold` | The fraction of the model's context window the estimated request must reach before pruning. Zero or less disables it. |
| `KeepTurns` | How many recent turns keep their tool results whole. `0` elides even the results the model has just asked for. |

Before each request the driver estimates its size: the context the latest
assistant message's usage reports, plus what pruning took off the request that
usage measured, plus 4 characters per token for the messages after it. The
decision is on the unpruned size, so pruning stays on once it starts. With no
usage reported, it is 4 characters per token over the system prompt, the
tools, the prefix and the messages. When the estimate reaches `Threshold` of
the window, the content of every tool result older than the last `KeepTurns`
turns is replaced in the request by
`[result of NAME (N bytes) elided; call again if needed]`. The result keeps its
`ToolUseID`; `Agent.Messages()` and `RunResult.Messages` keep it whole.

Whether or not `Prune` is set, a request whose estimate is still larger than
the context window is not sent: the run ends with `core.RunStopError` and the
error `agentkit: the request to model "ID" is about N tokens, more than its
W-token context window`.

### Run behaviour

- A batch's calls run concurrently, one goroutine per call, unless a tool in
  the batch is `core.Sequential`.
- When any call that ran returns `Terminate: true` (or `After` sets it), the
  run ends with `core.RunStopToolTerminate` once every call in the batch has
  finished. A call blocked by `Guard` or refused for its arguments casts no
  vote of its own; a `Guard` decision with both `Block` and `Terminate` ends
  the run (`guard.Options.TerminateOnBlock`).
- A response cut off at `max_tokens` runs none of its tool calls; each gets
  the fixed result `Tool call "NAME" was not executed: the response hit the
  output token limit, ...`.
- A run whose last response is a refusal ends with `core.RunStopRefusal` and
  an error wrapping `core.ErrRefusal`.
- The event stream is unbounded and never drops an event or blocks the run;
  `Config` has no stream options.

## `guard.Options`

`guard.Restricted(opts)` refuses `BlockedTools`, then, for `execute`, shell
operators and environment prefixes, then asks `guard.Check(argv, opts)`
whether the program is on `AllowedPrograms`. `Check` is a pure function
returning a `guard.Decision{Block, Reason, Terminate}`. The guard is a floor
for unattended safety, not a sandbox.

| Field | Meaning |
|---|---|
| `AllowedPrograms` | Programs that may run. A bare name admits only that bare name; a path admits only that path, after cleaning. A program spelled with a path separator is refused unless that path is listed. Empty blocks every shell call. |
| `AllowShellOperators` | Permit pipes, `;`, `&&`, `||`, redirection, subshells, substitution and expansion in `execute` commands (POSIX sh grammar). Off by default. |
| `AllowEnvPrefixes` | Permit `NAME=value` in front of the program in `execute` commands. Off by default. Even when on, names that change which binary runs or what is loaded into it (`PATH`, `LD_*`, `DYLD_*`, `BASH_ENV`, …) are refused. |
| `BlockedTools` | Tools refused by name, shell or not. |
| `TerminateOnBlock` | A block also votes to end the run. |

## Credentials and deployments

`anthropic.Resolve(env) (*sdk.Client, Source, error)` chooses the deployment
and builds the SDK client (`provider/anthropic/resolve.go`). `env` is an
`anthropic.Env`: `anthropic.OSEnv{}` for the process environment, or
`anthropic.MapEnv` for a map. An empty value counts as unset. `Resolve` makes
no network request; Google and AWS credentials are fetched on the first
request. The returned `Source` is `SourceDirect`, `SourceVertex` or
`SourceBedrock`.

A provider built with `anthropic.Options.Client == nil` resolves the same way
on every request, reading `Options.VertexProject` / `VertexLocation` first,
then `Options.Env`, then `Options.Getenv` (or the process environment).

| Variable | Effect |
|---|---|
| `CLAUDE_CODE_USE_VERTEX` | `1` or `true` selects Claude on Vertex AI. Any other value, `0` and `false` included, leaves it off. |
| `ANTHROPIC_VERTEX_PROJECT_ID`, then `GOOGLE_CLOUD_PROJECT` | The Vertex project. Required once Vertex is selected; they never select it. |
| `CLOUD_ML_REGION`, then `GOOGLE_CLOUD_LOCATION`, `CLOUDSDK_COMPUTE_REGION` | The Vertex location; default `global` (`anthropic.DefaultVertexRegion`). |
| `ANTHROPIC_VERTEX_BASE_URL` | A proxy in front of Vertex. |
| `CLAUDE_CODE_USE_BEDROCK` | `1` or `true` selects Claude on Amazon Bedrock, when Vertex is not selected. |
| `AWS_REGION`, then `AWS_DEFAULT_REGION` | The Bedrock region; default `us-east-1` (`anthropic.DefaultBedrockRegion`). |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | Static Bedrock credentials. Without them, `AWS_BEARER_TOKEN_BEDROCK`; without that, the AWS SDK's own credential chain. |
| `ANTHROPIC_API_KEY` | The Anthropic API key, for the direct deployment. Checked first. |
| `ANTHROPIC_AUTH_TOKEN` | A bearer token for the direct deployment. On Vertex, a Google access token (anything not starting `sk-ant-`). |
| `ANTHROPIC_OAUTH_TOKEN` | An OAuth bearer for the direct deployment; adds the `oauth-2025-04-20` beta (`anthropic.BetaOAuth`). |
| `ANTHROPIC_BASE_URL` | A proxy or gateway in front of the Anthropic API. |

The variable names are exported constants: `APIKeyVar`, `AuthTokenVar`,
`OAuthTokenVar`, `BaseURLVar`, `VertexEnableVar`, `VertexProjectVar`,
`GoogleProjectVar`, `VertexRegionVar`, `VertexBaseURLVar`, `BedrockEnableVar`.

Vertex authorizes with, in order: a Google access token in
`ANTHROPIC_AUTH_TOKEN`, `Options.VertexTokenSource`, then Application Default
Credentials found on the first request.

With no cloud flag and none of the three Anthropic credentials, `Resolve`
fails with `anthropic.ErrNoCredentials` (`anthropic: missing credentials: set
ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, or select Vertex AI
(CLAUDE_CODE_USE_VERTEX=1) or Bedrock (CLAUDE_CODE_USE_BEDROCK=1)`). A Vertex
selection with no project fails naming both project variables. A provider
that cannot resolve ends the turn with that message rather than sending a
request.

Other variables:

| Variable | Read by | Effect |
|---|---|---|
| `AGENTKIT_MODEL` | `examples/*` only | Overrides an example's model. The library never reads it. |

## `anthropic.Options`

`anthropic.Provider(model, opts)` is a `core.ProviderClient` for one model.
`Stream(ctx, req)` returns a channel of `core.StreamEvent`, closed when the
turn ends. The last `MessageEndEvent` carries the assistant message, a failed
turn's included, and a failure sets `Err` on the last item; a context already
done is refused with its error. `core.EventStreamOf(ch, err)` reads such a
channel as a `core.EventStream`.

| Field | Meaning |
|---|---|
| `Client` | The SDK client requests go through. Nil resolves one from the environment on every request. When set, `BaseURL`, `HTTPClient`, `Getenv`, `Env`, `VertexProject`, `VertexLocation` and `VertexTokenSource` are not used, and `Betas` sends no header (configure the client instead). |
| `BaseURL` | Overrides the resolved base URL. |
| `HTTPClient` | The HTTP client a resolved SDK client uses. |
| `Getenv` | Injectable environment lookup; nil means the process environment. |
| `MaxRetries` | `*int`; overrides the SDK's retry count (2 by default). |
| `Betas` | Sent as `anthropic-beta`, verbatim. Server-side compaction is not supported: core has no block to carry what it returns. |
| `VertexProject`, `VertexLocation` | Select the Vertex AI deployment, and its location, over the environment. |
| `VertexTokenSource` | `oauth2.TokenSource` for Vertex. |
| `BillingLookup` | Resolves a served model id to its catalog row for pricing. Nil bills at the requested model's rates. |
| `ToolPrefix`, `OnToolPrefixSync` | The per-session tool-schema cache (`*provider.ToolPrefix`; nil means the provider owns one) and its reconciliation reports. |
| `Now` | Injectable clock for timestamps. |
| `CacheRetention` | `core.CacheRetentionNone`, `CacheRetentionShort` (empty means short) or `CacheRetentionLong` (1-hour TTL on the system and tool breakpoints). |
| `Headers` | `map[string]*string` merged into every request; a nil value deletes that header. |
| `Timeout` | Bounds each request, independently of the caller's context; zero is none. |
| `Env` | `map[string]string` consulted before `Getenv` when resolving the deployment. |
| `Transport` | Replaces the HTTP transport. |
| `OnPayload` | `func(payload any, model *core.Model) (any, error)`: sees, and may replace, the encoded request before it is sent; its error ends the turn unmodified. |
| `OnResponse` | `func(resp *http.Response, model *core.Model) error`: sees each HTTP response before it is read; its error ends the turn. |
| `Warnf` | Receives the transcript-repair report; nil discards it. |

What the provider sends:

- Every tool is declared with `"strict": true`. A tool's output schema,
  reachable tools and terminating flag are never sent.
- `tool_choice` is `auto`, `none` or absent. A forced choice is never sent.
- Cache breakpoints (`cache_control: {"type":"ephemeral"}`) go on the last
  system block, the last tool (of those not deferred), the last block of `Config.Prefix`, and the last
  block of the final user message.
- A replayed `tool_use` input reaches the wire with the exact bytes it arrived
  with. `anthropic.BuildRequestJSON(req, model)` returns the body as sent.
- Models whose catalog row sets `compat.supports_sampling: false` get no
  `temperature` or `top_p`.

Each response's `core.Usage` carries input, output, cache-read and
cache-write tokens and `Requests: 1`; `Usage.Add` sums them.
`anthropic.TranslateMessage` and `anthropic.TranslateUsage` translate an SDK
`Message` and an SDK usage pair the way the stream does.

## Model catalog

`catalog.Lookup(id)` reads the embedded `catalog/catalog.json` (version
`2026-09-30`); the `anthropic/` prefix is optional. Prices are USD per million
tokens. Thinking is `adaptive` when the row's `thinking_level_map` holds effort
names, `budget` when it holds token counts.

| Model id | Context window | Max output | Input $/M | Output $/M | Thinking |
|---|---|---|---|---|---|
| `claude-fable-5-1` | 1,000,000 | 128,000 | 10 | 50 | adaptive |
| `claude-fable-5` | 1,000,000 | 128,000 | 10 | 50 | adaptive |
| `claude-opus-5-5` | 1,000,000 | 128,000 | 4 | 20 | adaptive |
| `claude-opus-5` | 1,000,000 | 128,000 | 5 | 25 | adaptive |
| `claude-opus-4-8` | 1,000,000 | 128,000 | 5 | 25 | adaptive |
| `claude-opus-4-7` | 1,000,000 | 128,000 | 5 | 25 | adaptive |
| `claude-opus-4-6` | 1,000,000 | 128,000 | 5 | 25 | adaptive |
| `claude-sonnet-5-5` | 1,000,000 | 128,000 | 2 | 10 | adaptive |
| `claude-sonnet-5` | 1,000,000 | 128,000 | 2 | 10 | adaptive |
| `claude-sonnet-4-6` | 1,000,000 | 128,000 | 3 | 15 | adaptive |
| `claude-opus-4-5` | 200,000 | 64,000 | 5 | 25 | budget |
| `claude-sonnet-4-5` | 200,000 | 64,000 | 3 | 15 | budget |
| `claude-haiku-4-5` | 200,000 | 64,000 | 1 | 5 | budget |

The budget models take `low`, `medium` and `high` (4096, 16384 and 32768
tokens); `xhigh` and `max` are not sent to them. `claude-opus-4-6` and
`claude-sonnet-4-6` do not list `xhigh`.

An id the catalog does not list still resolves, with `false`: a
1,000,000-token window (`catalog.DefaultContextWindow`), a 128,000-token
output cap (`catalog.DefaultMaxTokens`), adaptive thinking and no price.

## Built-in tools (`tools.Options`)

| Field | Default |
|---|---|
| `Workspace` | `tools.NewWorkspace("")`, the current directory. File tools cannot leave it. |
| `SpillDir` | A per-workspace directory under `os.TempDir()`. Spill files are the embedder's to clean. |
| `DisableSpill` | false. |
| `Env` | `tools.ReducedEnv(nil)`. |
| `Ignore` | The real gitignore environment; `tools.NoGlobalExcludes()` pins an empty global layer for tests. |
| `Symbols` | `tools.SymbolOptions{}`: `MaxFiles` (default 50 000) and `MaxDuration` (default 2 s) bound each symbol-table build or refresh. |
| `Index` | nil. When set, `All` appends the index's tools after the built-ins, and `write_file`, `edit_file` and the shell tools call `Index.Invalidate`. |

`tools.All` returns `read_file`, `write_file`, `edit_file`, `list_files`,
`find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`,
`execute` and `run_command`. `read_file` reads text; a file whose leading
bytes are a PNG, JPEG, GIF or WebP signature is refused with
`unsupported_file`. Output limits: 50 KB per result (`DefaultByteLimit`),
`read_file` 2000 lines, `search_files` 100 matches and 500 characters per line,
`find_files` 200 by default (cap 1000), `list_files` 200 by default (cap 500),
`find_symbol` 20 by default (cap 50), `find_references` 30 by default (cap
100).

Subprocess tools run with `tools.ReducedEnv`: `PATH`, `HOME`, `LANG`,
`TMPDIR`, `TERM`, `AWS_PROFILE`, `AWS_REGION`, `AWS_DEFAULT_REGION` and
`GOOGLE_CLOUD_PROJECT` are kept; provider-prefixed variables (`ANTHROPIC_`,
`AWS_`, `GOOGLE_`, `AGENTKIT_`, …), names ending in `_TOKEN`, `_SECRET`,
`_KEY`, `_PASSWORD`, `_CREDENTIALS` and similar, and the bare `TOKEN`,
`API_KEY`, `SECRET`, `PASSWORD` are stripped.

### `code_search` (when a `codesearch` index is set)

| Parameter | Default | Cap |
|---|---|---|
| `max_files` | 10 | 25 |
| `context_lines` | 2 when absent; an explicit 0 means none | 20 (`tools.MaxSearchContextLines`) |
| `query` length | | 1 024 bytes |
| query timeout | | 10 s |
| result size | | 50 KB (`tools.DefaultByteLimit`) |

`codesearch.Options`:

| Field | Default | Meaning |
|---|---|---|
| `Ignore` | | `tools.IgnoreOptions`; pass the same value as `tools.Options.Ignore`. |
| `MaxFiles` | 100 000 | File-count bound for the index build. |
| `MaxBytes` | 1 GiB | Total indexed content bound. |
| `MaxBuildTime` | 60 s | Wall-time bound for the build; files already walked get a grace window of a quarter of it. |
| `TempDir` | `os.TempDir()` | Where shard files are written. |

## Subprocess runner (`tools.Run`, `tools.RunArgv`)

`tools.Run` executes a shell command; `tools.RunArgv` runs a program from an
argv without a shell. Both return `ExecResult` and take `ExecOptions`.

| `ExecOptions` field | Zero-value behaviour |
|---|---|
| `Dir` | The process working directory. |
| `Timeout` | No timeout. |
| `MaxBytes` | `DefaultByteLimit` (50 KB). |
| `KeepHead` | false: keep the tail of the output. When true, keep the first `MaxBytes` bytes. |
| `SpillDir` | Disabled. When set, the complete output is written to a temporary file there. |
| `LogPath` | Disabled. When set, the complete output is written to this file as it arrives (relative to `Dir`; parents created `0o700`, file `0o600`), replacing `SpillDir`; `ExecResult.SpillPath` reports it. |
| `Stdin` | nil: the null device. Otherwise copied into the child's stdin, closed at `io.EOF`. |
| `Env` | nil means `ReducedEnv(nil)`. A non-nil slice, even empty, is used verbatim. |
| `DrainIdle` | 2 s: how long output must stay quiet after the child exits before draining stops. |
| `DrainCeiling` | 10 s: the absolute bound on the post-exit drain. |

| `ExecResult` field | Meaning |
|---|---|
| `Output` | The truncated output. |
| `Outcome` | `ok`, `exit`, `signal`, `timeout` or `abort`. |
| `ExitCode` | The exit code; 128+signum on unix for a signal-killed child. |
| `Truncated`, `TotalBytes` | Whether output was cut, and how many bytes the child wrote. |
| `SpillPath` | The spill or log file, when one exists. |
| `Duration` | Wall time to the child's exit. |
| `IOErr` | A byte-moving failure after the process started (stdin, log or spill write); `Outcome` is still classified from the exit status. |

`abort` means the caller's context was done at exit; `timeout` means the
`ExecOptions.Timeout` deadline expired and the caller's context had not. A
non-nil `error` return means nothing started.

## Code mode (`codemode.Options`)

`codemode.New(tools, opts)` builds the code-mode tool and returns it with a
`codemode.BuildInfo` (`DescriptionBytes`, `DescriptionChars`,
`BoundToolsCount`). A zero field takes its default; `codemode.DefaultOptions()`
returns them all.

| Field | Default | Meaning |
|---|---|---|
| `Name` | `code_mode` | The tool's name. |
| `Description` | generated | When set, used verbatim and nothing is generated. |
| `DescriptionTemplate` | `codemode.DefaultDescriptionTemplate` | A `text/template` over `codemode.DescriptionData` for the instructions section. The bound tools' declarations always follow it. |
| `Guidelines` | built-in guidance | The tool's `PromptGuidelines`. |
| `MaxTimeout` | 30 s | Wall time per script; past it, `timeout`. |
| `MaxSteps` | 100 000 | Starlark execution steps per script; past it, `step_limit_exceeded`. |
| `MaxCalls` | 50 | Tool calls per script, direct and in `parallel` together; past it, `call_limit_exceeded`. |
| `MaxConcurrentCalls` | 8 | Calls one `parallel(...)` has in flight at once. |
| `MaxOutputBytes` | 102 400 (100 KB) | Printed output plus the return value; past it, `output_limit_exceeded`, head and tail kept. |
| `SpillDir` | `agentkit-codemode` under `os.TempDir()` | Where the complete output of a truncated script is written. |
| `DisableSpill` | false | When true, truncated output is not written to disk. |

A bound tool's name must be a Starlark identifier, must not be a Starlark
builtin or one of `parallel`, `call`, `is_error`, `main` and `result`, and must
be unique. No code-mode tool may be bound, at any depth.

## MCP (`mcp.Config`, `mcp.ServerConfig`)

`mcp.ParseConfig(path, src) (Config, []Diagnostic, error)` reads the `[mcp]`
section into `mcp.Config{Servers []ServerConfig}`. `mcp.NewPool(opts)` makes a
pool, `Pool.Connect(ctx, cfg, env, secrets)` connects one server, and
`Pool.Tools(ctx, existing)` returns its tools as `core.Tool`s.

| `ServerConfig` field | TOML key | Meaning |
|---|---|---|
| `Name` | `name` | Required, unique. Keys the pool and the tool prefix. |
| `Command`, `Args`, `Dir` | `command`, `args`, `dir` | Spawn a stdio server. |
| `URL` | `url` | Connect to a remote server. When both `command` and `url` are set, the command wins with a warning; one is required. |
| `Transport` | `transport` | For a `url` server: `"streamable-http"` (default) or `"sse"` (the 2024-11-05 HTTP+SSE transport). Any other value is an error and the server is dropped. |
| `Env` | `env` (table) | The subprocess environment; values may use `${VAR}`, resolved through `secrets` at connect time. |
| `Headers` | `headers` (table) | Sent on every request to a `url` server; `${VAR}` supported. Redirects are not followed. |
| `ToolPrefix` | `tool_prefix` | Overrides the default `<name>__`. |
| `DisablePrefix` | (none) | Set in code for no prefix. |
| `AllowSampling` | `allow_sampling` | Advertise sampling and answer it through `ConnectionOptions.Sampling` (refused when nil). Default false. |
| `PerSessionCallLimit` | `per_session_call_limit` | Default 1000 (`mcp.DefaultCallLimit`); negative disables it. |
| `PerSessionReconnectLimit` | `per_session_reconnect_limit` | Default 3 (`mcp.DefaultReconnectLimit`); negative means never reconnect. |
| `Timeout` | `timeout_s` | Integer or float seconds; default 30 (`mcp.DefaultTimeout`). |

`mcp.ConnectionOptions` (given to `NewPool` or `Connect`): `Sampling`,
`Warnf` (reconnects and a child's stderr; nil discards), `Limits`
(`wire.Limits`) and `ClientInfo`. `Pool.NativeTools` names the host's own
tools, which no server tool may shadow.

The file is parsed as TOML 1.0 (`go-toml/v2`); a grammar error rejects it with
its line. Strings, booleans, decimal integers, floats, string arrays, tables
and arrays of tables are read. Any other well-formed value (dates, multi-line
strings, inline tables, hex/octal/binary integers, non-string arrays) skips its
key with a warning, and a duplicate key warns and the last value wins. A key
`ParseConfig` does not know, or a value of the wrong kind for its key, is
ignored. A server with no `name`, with neither `command` nor `url`, with an
unknown `transport`, or with a duplicate name is dropped with an error
diagnostic.

## Limits on untrusted input

`wire.Defaults()`: 16 MiB per message, 1,000,000 elements per container,
depth 64, 2,000,000 nodes. The MCP client holds every inbound stdio frame,
JSON response body and SSE event to `ConnectionOptions.Limits` (zero fields
take these defaults) and rejects duplicate keys before the SDK decodes it. The
Anthropic provider checks each stream event and response body the same way,
with the defaults.

## Compatibility

Go 1.27 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64 with cgo off; the host is also
built with cgo on. cgo code lives only in `//go:build cgo` files, each with a
pure-Go fallback.
