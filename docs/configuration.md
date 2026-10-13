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

A sandbox is the boundary the guard never claimed to be. `guard.AllowAll`,
the interceptor that never blocks, is a reasonable choice only when every
shell tool the agent can reach is a proxy from a sandbox whose provider
reports `sandbox.IsolationContainer` or `sandbox.IsolationMicroVM`
(`sandbox/podman`), under `sandbox.NetworkNone` (or with egress you accept),
with `Spec.Resources` set, and with the files you export from it treated as
untrusted. It is never reasonable with `local` or with any local shell tool
in the set. `agentkit.New` still requires a `Guard` either way.

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
| `Workspace` | `tools.NewWorkspace("")`, the current directory. File tools cannot leave it. On a host `execute` can; inside a sandbox whose agent reports a Landlock ABI (`AgentInfo.Landlock`), the agent's kernel ruleset confines `execute` and every process it starts to the writable roots. Under `local`, or on a kernel without Landlock, it does not. |
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

## Sandboxes (`sandbox.Spec`, `sandbox.ToolOptions`, `local`)

A `sandbox.Provider` creates a sandbox from a `sandbox.Spec`. Every provider
checks it with `sandbox.CheckSpec` first: a field the provider's
`Capabilities` do not cover is refused with a `*sandbox.SpecError` (matching
`sandbox.ErrUnsupported`) that reads
`sandbox: provider "local" cannot honour Spec.Network.Mode = none: …`. No
provider silently widens a policy.

### `sandbox.Spec`

| Field | Meaning | Default |
|---|---|---|
| `Image` | The container or VM image. | the provider's |
| `Workdir` | The workspace path inside the sandbox. | `/workspace`, for a provider that honours it; `Spec()` then reports the root the agent resolved |
| `Env map[string]string` | Extra environment for the tools. A name may not be empty or hold `=` or NUL, a value may not hold NUL. | none |
| `Resources` | `CPUs float64`, `MemoryBytes`, `PIDs`, `DiskBytes int64`; zero means no bound, negative is refused. | none |
| `Network` | `Mode`: `NetworkNone` (the zero value), `NetworkAllowlist` or `NetworkUnrestricted`; `Allow []Endpoint{Host, Port}` only with `NetworkAllowlist`, each with a host and a port in 1–65535. | `NetworkNone` |
| `TTL` | The sandbox's lifetime; at the TTL it becomes unavailable and is destroyed. Negative is refused. | 1 hour |
| `Labels map[string]string` | Tags for the provider. The key `agentkit.sandbox` is reserved. | none |
| `Provider map[string]any` | Provider-specific options. A key the provider does not define is refused, naming it. | none |
| `RequireLandlock` | Make `Create` fail unless the agent confines itself with Landlock enough for the network mode: `CheckSpec` refuses it when `Capabilities.Landlock` is false, and after `Hello` the agent's report must satisfy `LandlockInfo.Satisfies(Network.Mode)`. Either refusal is a `*SpecError` with `Field` `RequireLandlock`; the second destroys the sandbox first. | false |

The context passed to `Create` bounds creation only, not the sandbox's
lifetime.

### Which provider honours which field

| Field | `local` | `podman` / `docker` |
|---|---|---|
| `Image` | refused | yes; empty means `sandbox.DefaultImage()`; refused when it starts with `-` or holds whitespace or a control character |
| `Workdir` | refused (the workspace is `<dir>/workspace`) | yes (default `/workspace`); must be absolute and clean, not `/`, `/tmp` or beneath it, not beneath `/proc`, `/sys` or `/dev`, and hold no `,` or control character |
| `Env` | yes | yes, plus the defaults below; `HOME` and `TMPDIR` must be `/tmp`, the workspace, or beneath either |
| `Resources.*` | refused | `CPUs`, `MemoryBytes`, `PIDs`; `DiskBytes` is refused; a zero field takes the engine's default |
| `Network.Mode` | `NetworkUnrestricted` only; the zero `Spec` is refused | `NetworkNone` (`--network none`) or `NetworkUnrestricted` (the engine's default network); `NetworkAllowlist` is refused |
| `TTL` | yes | yes, twice: attach's host timer and the agent's `--ttl`; the volume carries the expiry |
| `Labels` | yes (recorded in the marker) | yes, on the container, the init container and the volume; the key prefix `agentkit.sandbox.` is reserved, and a key may not be empty or hold `=`, whitespace or a control character, nor a value a control character |
| `Provider` | no keys defined; any key is refused | no keys defined; any key is refused with a `*SpecError` |
| `RequireLandlock` | refused (`Capabilities.Landlock` is false: its agent runs in the embedder's process, which nothing may confine) | yes; the engine's default seccomp profile may withhold Landlock, which the agent then reports |

### Landlock: `RequireLandlock`, `Capabilities.Landlock`, `AgentInfo.Landlock`

`Capabilities.Landlock` says the provider starts `agentkit-sandboxd` in a
way that lets it apply its Landlock ruleset; `local` reports false.
`Sandbox.Agent().Landlock` is the `sandbox.LandlockInfo` the agent reported
in `Hello` (protocol 1.2 and later):

| Field | Meaning |
|---|---|
| `ABI int` | The ABI of the ruleset applied; 0 means none. |
| `KernelABI int` | The ABI the kernel offered, uncapped; 0 means none or unknown. |
| `TCP bool` | TCP bind and connect are denied. |
| `Note string` | Why nothing, or only part, was applied. |
| `Writable []string` | The resolved writable roots, then the device files, in rule order. |
| `ReadOnly []string` | The resolved read roots, ending with `/proc`, in rule order. |

`LandlockInfo.Satisfies(mode)` is true when `ABI >= sandbox.LandlockMinABI`
(2) and, under `NetworkNone`, `TCP` too; `sandbox.LandlockTCPABI` (4) is the
ABI that restricts TCP. An agent older than protocol 1.2 reports ABI 0 with
the note `agent protocol 1.N does not report Landlock`, and `local`'s
in-process agent reports ABI 0 with the note
`in-process agent (local provider): no confinement`.

`sandbox.AgentArgs(spec)` returns the agent's flags for an effective spec:
`--ttl`, `--network` and, when `Workdir` is set, `--workdir`. Providers start
the agent with it.

### The agent's Landlock ruleset

On Linux, `agentkit-sandboxd` probes the kernel's Landlock ABI once and
applies a ruleset of exactly the version it chose, never a best-effort one,
to its whole process before any other work; `no_new_privs` is set on every
thread as part of it. The ruleset grants exactly:

| Access | Paths |
|---|---|
| Every right the applied ABI defines (execute, refer from ABI 2, truncate from ABI 3) | the workspace (`--workdir`), `HOME`, the temporary directory (`TMPDIR`, else `TEMP`, `TMP`, `/tmp`, from the agent's environment), each `--landlock-rw` |
| Read and execute | `/usr`, `/bin`, `/sbin`, `/lib`, `/lib32`, `/lib64`, `/libx32`, `/etc`, `/opt`, each `--landlock-ro` |
| Read | `/proc` |
| Read and write (and truncate from ABI 3) | `/dev/null`, `/dev/zero`, `/dev/full`, `/dev/random`, `/dev/urandom` |
| TCP bind and connect | denied, loopback included, under `--network none` at ABI 4; not handled under `allowlist` or `unrestricted` |

Every root is resolved through symlinks and duplicates collapse. A missing
system or device path is skipped silently; a missing `--landlock-ro` or
`--landlock-rw` is skipped with a note. `HOME` or the temporary directory is
skipped, with a note naming the variable and why, when it is unset,
relative, missing, `/`, or equal to or an ancestor of a system directory. A
`--workdir` or `--landlock-rw` that is `/` or contains a system directory
(a relative `--workdir` is judged by the absolute directory it names), and
a relative `--landlock-ro` or `--landlock-rw`, are usage errors (exit 2).
With a ruleset applied, a `ToolOptions.SpillDir` outside the writable
directory roots is refused at `Tools` with `InvalidArgument`; the default
spill directory always qualifies.

What the agent applies for each kernel (`Plan`):

| Kernel ABI | Applied ABI | `TCP` | `Note` |
|---|---|---|---|
| probe error | 0 | false | `Landlock unavailable: <error>` |
| 0 | 0 | false | `kernel reports no Landlock support` |
| 1 | 0 | false | `kernel Landlock ABI 1 cannot permit cross-directory renames (needs ABI 2, Linux 5.19); not applied` |
| 2 or 3 | the kernel's | false | under `none`: `kernel Landlock ABI k cannot restrict TCP (needs ABI 4, Linux 6.7)` |
| 4 or later | 4 | true only under `none` | empty |

Under `allowlist`, at any applied ABI above 0, the note says that Landlock
cannot express a host allowlist and that TCP is left to the provider. A
probe failure is not fatal; a ruleset that cannot be built or applied after
a good probe ends the agent with exit status 1.

### `sandbox.ToolOptions`

`Sandbox.Tools(ctx, opts)` configures the agent's tool set. The options are
fixed by the first successful call; a later call with different options
fails with `sandbox.ErrToolsConfigured`.

| Field | Default |
|---|---|
| `Symbols` | `tools.SymbolOptions{}`, as for `tools.Options`. |
| `SpillDir` | `agentkit-spill` under the `TMPDIR` of the tools' environment inside the sandbox. A non-empty value must be absolute. |
| `DisableSpill` | false. |

The agent always uses `tools.NoGlobalExcludes()` (the workspace's own ignore
files still apply), its explicit environment, and no index.

### Transfer: `sandbox.Tar` and `sandbox.TarOut`

`Sync(ctx, sandbox.Tar{R, Path, Replace})` extracts the tar stream `R` into
`Path`, a workspace-relative slash-separated path (`""` and `.` are the
root; an absolute or escaping path is refused). With `Replace`, the existing
contents of `Path` are removed first and `Path` is kept, or created. It
returns `SyncResult{Files, Bytes, Deleted, Sent}`: the regular files written
and their content bytes, `Deleted` 0 even when `Replace` removed entries,
and `Sent` the tar bytes read from `R` and sent. `Export(ctx, path, sandbox.TarOut{W})`
writes a tar of the subtree to `W`, names relative to `path`, verbatim
(hidden and ignored files included, symlinks as symlinks, other types
skipped). Both directions refuse absolute names, `..`, hard links, devices,
FIFOs and escaping symlinks with `sandbox.ErrUnsafeArchive`; a refused entry
ends a sync, and entries extracted before it stay.

### Transfer: `sandbox.LocalDir`

`sandbox.LocalDir` is a host directory, a `Source` for `Sync` and a
`Destination` for `Export`. The bytes travel over the rsync protocol, and
only what changed moves. It needs an agent speaking protocol 1.1; an older
agent is refused with `ErrUnsupported` (`LocalDir needs agent protocol 1.1,
agent speaks 1.0`) before any walk or stream.

| Field | On `Sync` | On `Export` |
|---|---|---|
| `Path` | An existing host directory whose contents map onto the workspace root. | A host directory, or a missing one whose parent exists, created once the agent accepts the transfer. A workspace directory's contents map onto it; a regular file lands as `Path/<base name>`. |
| `Ignore tools.IgnoreOptions` | The host's ignore environment. The zero value reads the real environment, global excludes included, as `tools.Options.Ignore` does; `tools.NoGlobalExcludes()` is hermetic. The agent always uses `tools.NoGlobalExcludes()`. | The same, for the host's rules over the destination. |
| `IncludeIgnored` | Turns ignore rules off on both sides. | The same. |
| `ExcludeGit` | Leaves `.git` behind. Without it `.git` content transfers whole. | No effect: an export always leaves `.git` behind. |
| `Delete` | Removes workspace entries the host directory lacks, except what either side's rules exclude. | Removes destination entries the exported directory lacks, except what either side's rules exclude. Refused for a regular-file export path. |

A relative `Path` resolves against the working directory at call time, and
symlinks in it are resolved once; the working directory is never changed.
An empty `Path`, a missing `Sync` directory, a destination that is not a
directory, or one whose parent is missing is refused before any stream,
naming the path. The `Export` path is checked like a `Tar` path; a symlink
there is refused (`InvalidArgument`) and a missing one is `NotFound`.

For a `LocalDir` sync, `SyncResult.Files` counts the regular files the
transfer created or changed in the workspace and `Bytes` their content bytes
(from the agent comparing size and modification time before and after, so a
tool writing in the same window may be counted), `Deleted` the entries
removed under `Delete`, and `Sent` the rsync data bytes the host wrote to the
stream. `Export` returns only an error.

**What moves.** Each side computes rules over its own tree, and a path
either side's rules exclude is not sent, not overwritten, not written
through and not deleted:

- ignored entries: what `tools.Walk` with hidden files does not visit, at
  the highest ignored level, never under `.git`. An export of a
  subdirectory honours the workspace's ignore files above it; `IncludeIgnored`
  and a single-file export apply none;
- every symlink whose target leaves the transfer root (absolute, or climbing
  above it), on both sides, `IncludeIgnored` or not;
- every entry the side cannot read, with its subtree;
- `.git`, at any depth, on a `Sync` with `ExcludeGit` and on every `Export`.

A rule is a literal path; `*`, `?` and `[` in a name are plain characters.
The combined rules may measure at most 8 MiB; past that the transfer is
refused before any file moves, with `rsync rules measure N bytes, over the
8 MiB limit (8388608 bytes)`.

**Warning: `IncludeIgnored` with `Delete` on `Export`** turns off the rules
that protect the destination's ignored files, so a host `.env` (or any other
ignored file) the workspace lacks is deleted. **A `.git/config` holding a
token in a remote URL travels into the sandbox** on a `Sync` unless
`ExcludeGit` is set.

Failures: a closed or unavailable sandbox returns `ErrClosed` or
`ErrUnavailable` before anything else. On Windows the host refuses with
`ErrUnsupported` naming the platform, and a Windows-built agent answers
`Rsync` with `FailedPrecondition`. The agent's deliberate refusals, a failure
of the rsync library on either side, and a cancelled context (the error
wraps `ctx.Err()`; files already written stay) keep the sandbox live; a
transport failure or a malformed stream makes it unavailable. No transfer is
retried.

### `local.Unisolated()`

For tests and development only: no isolation. The agent runs in the
embedder's process with its privileges. Each sandbox is
`<os.TempDir()>/agentkit-sandbox-local/<16 hex>/` holding `workspace/`,
`home/` and `tmp/` (each 0700) and a marker, `agentkit-sandbox.json`, with
the creating pid, the labels, the creation time and the TTL. The tools'
environment is `Spec.Env`, plus the host's `PATH`, `LANG` and `TERM` where
`Spec.Env` lacks them, plus `HOME` and `TMPDIR` pointing at the private
directories. Nothing is confined: `Capabilities().Landlock` is false, the
agent reports ABI 0 with the note
`in-process agent (local provider): no confinement`, and `RequireLandlock`
is refused before any directory is made. `Close` removes the directory; `Reap` removes directories whose
marker names a process that is no longer running (on Unix; elsewhere it
removes nothing).

### The Podman and Docker provider (`sandbox/podman`)

The first provider that isolates. Each sandbox's agent runs as the only
process of a hardened, ephemeral container that holds one workspace volume.
The provider drives the `podman` or `docker` command line, never their Go
API clients.

```go
p, err := podman.New(podman.Options{})
sb, err := p.Create(ctx, sandbox.Spec{Image: image, Resources: sandbox.Resources{MemoryBytes: 1 << 30, PIDs: 512}})
```

| `podman.Options` field | Meaning |
|---|---|
| `Binary` | The engine command; empty means `podman`. A bare name is resolved through `EngineEnv`'s `PATH`. |
| `Engine` | `podman.EnginePodman` or `podman.EngineDocker`; empty means detected once from `<Binary> --version` (`podman version …` is podman, which includes a `docker` shim that runs podman; `Docker version …` is docker). |
| `Runtime` | `""` (the engine's default runtime) or `krun` (a libkrun micro-VM, podman only). `crun-vm` is refused: it boots VM disk images, not OCI images with an entrypoint. |
| `UserNS` | Passed as `--userns=<UserNS>` when set. |
| `EngineEnv` | The environment of every engine command; nil means `os.Environ()`. Nothing in it reaches a container. |

`podman.New(o)` resolves the binary (an error says
`engine binary "podman" not found on PATH; install podman or docker, or set Options.Binary`),
refuses an unknown `Engine`, an unsupported `Runtime` and `krun` with
docker, and runs no command but one `--version`, with a 30-second timeout,
and only when `Engine` is empty. `Capabilities()` reports the engine as
`Provider`, `IsolationMicroVM` with `krun` and `IsolationContainer`
otherwise, `NetworkNone` and `NetworkUnrestricted`, `CPUs`, `MemoryBytes`
and `PIDs`, and `Image`, `Workdir` and `Landlock`.

**The container.** `podman.Args(o, id, created, spec)` and
`podman.InitArgs(o, id, created, spec, repo)` are the exact, pure argument
lists the provider runs after the binary, for the effective spec with
`Image` set. Every sandbox container carries:

| Flag | Why |
|---|---|
| `run --rm -i --init` | The agent is the container's own process on stdio; `--rm` removes the container when it ends, and `--init` reaps zombies. |
| `--name agentkit-<id>` | The container's name; the init step's is `agentkit-<id>-init`. |
| `--label agentkit.sandbox=<id>`, `--label agentkit.sandbox.expires=<created+TTL>` | The reserved labels, RFC 3339 UTC to the second, then the embedder's labels sorted by key. |
| `--log-driver none` | The agent's stdout is the protocol stream; no log driver copies it. |
| `--network none` | Unless the spec is `NetworkUnrestricted`. |
| `--cap-drop ALL`, `--security-opt no-new-privileges` | No capabilities, and none to gain. |
| `--read-only` | A read-only root. |
| `--tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777` | The tools' writable directory besides the workspace (`HOME` and `TMPDIR`); memory-backed, so it counts against `MemoryBytes`. Podman's `--read-only` also mounts tmpfs at `/run`, `/var/tmp` and `/dev/shm`, which the agent's Landlock ruleset keeps read-only for the tools. |
| `--mount type=volume,source=agentkit-<id>,target=<Workdir>` | The workspace: a named volume, never a host path. |
| `--cpus`, `--memory` and `--memory-swap` (equal), `--pids-limit` | One flag per non-zero `Resources` field. |
| `--runtime krun`, `--userns=<v>` | When set in `Options`. |
| `--unsetenv-all` | Podman only: drop the image's environment. |
| `--env NAME=value`, sorted | The environment, below. |
| `--entrypoint /usr/local/libexec/agentkit-sandboxd <image> <sandbox.AgentArgs…>` | Always the agent at `sandbox.AgentPath`, whatever the image's own entrypoint. |

No other flag is emitted: no `-v`/`--volume`, bind mount, `--privileged`,
`--cap-add` or seccomp override; the engine's default seccomp profile
applies. The image must have the workspace directory, owned by its user (or
run as root); the base image does.

**The environment** is `Spec.Env`, plus each default it does not set:
`HOME=/tmp`, `LANG=C.UTF-8`,
`PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`,
`TERM=dumb`, `TMPDIR=/tmp`. Both engines also add `HOSTNAME`, the
container's own. On podman `--unsetenv-all` drops the image's own `ENV`;
docker has no such flag, so there an image's `ENV` also reaches the tools
(the base image declares none). `HOME` and `TMPDIR` in `Spec.Env` must be
`/tmp`, the workspace, or beneath either: nothing else is writable under
`--read-only` and the agent's Landlock ruleset. The values
appear on the engine's command line on the host, so credentials never belong
in `Spec.Env`.

**`Create`** runs `sandbox.CheckSpec` and then the provider's own checks
(the table above), all before any engine command; with an empty `Image` it
uses `sandbox.DefaultImage()`, whose `ErrNoPublishedImage` it returns as is.
It then runs `volume create` with the labels, starts the engine with `Args`
and attaches over its stdio. Whatever fails after the volume exists, the
container and the volume are removed before `Create` returns. A protocol
mismatch (`ErrProtocol`) and the `RequireLandlock` `*SpecError` pass
through; a done context returns its error; an engine exit of 125 returns an
engine error with the last 8 KiB of its stderr. A failed handshake with any
other non-zero exit
runs `run --rm … --entrypoint <AgentPath> <image> --version` without
`--init` or a volume: exit 126 or 127 means the image has no agent, and the
error wraps `sandbox.ErrNoAgent`
(`sandbox: the image has no executable agent at sandbox.AgentPath`), names
the image and gives the fix, the line `sandbox.OverlayCopyLine` returns.

**`Close`** (also at the TTL) waits up to 5 seconds for the engine process
and then kills its group, runs `rm -f agentkit-<id>` and
`rm -f agentkit-<id>-init`, and `volume rm agentkit-<id>`, retried for up to
10 seconds while it is in use. A missing container or volume is not an
error. The agent's own `--ttl` ends the container, which `--rm` removes,
even when the host is gone.

**`Reap`** lists the containers and volumes with the label key
`agentkit.sandbox`, reads their labels from the engine's inspect JSON, and
removes those whose `agentkit.sandbox.expires` parses and is not after now,
containers first. Everything else, including an entry without a readable
expiry, is left alone. The host-side TTL counts from the same creation time
as the label, so an expired sandbox is already being destroyed by its owner,
and one another process or host still uses is never touched (as long as the
hosts' clocks agree); a crashed host's volume lingers until its TTL.

### `sandbox.GitRepo`

`sandbox.GitRepo{URL, Ref, Depth}` is a `Source`: a public repository the
provider's init step clones into the workspace. `podman` honours it;
`local` (and any provider without an init step) refuses it with an error
matching `ErrUnsupported`.

| Field | Rule |
|---|---|
| `URL` | `https` only, with a host, no user information, no fragment, no whitespace or control character. |
| `Ref` | A branch or tag; empty means the remote's default branch. Letters, digits, `.`, `_`, `-` and `/` only, not starting with `-` or `/`, and no `..`. |
| `Depth` | `> 0`: a shallow clone of that depth; `0`: full history; negative is refused. |

The clone runs `git -c protocol.allow=never -c protocol.https.allow=always clone [--depth N] [--branch REF] -- URL <Workdir>`
in a separate `agentkit-<id>-init` container with the same labels,
hardening, volume and bounds, on the engine's default network, while the
agent's container sits idle beside it with its own network policy. Its
environment is `GIT_TERMINAL_PROMPT=0` and the `HOME`, `LANG` and `PATH`
defaults, plus the engine's `HOSTNAME` (and, on docker, the image's own
`ENV`), never `Spec.Env`. A `GitRepo` must be the first `Sync` and come before the
first `Tools` call: any successful `Sync` and the first `Tools` call take
the slot, and a later `GitRepo` is refused with
`GitRepo must be the first Sync and come before Tools`. A failed clone takes
nothing. `Tools` and `Sync` wait for a clone in progress. The image needs
`git` on its `PATH`. A successful clone reports a `SyncResult` of zeros;
its files are then visible to the tools. A private repository is cloned on
the host and synced as a `LocalDir`.

**Testing it.** `make sandbox-podman-test` builds the images (`make
sandbox-images`) and runs the provider's integration tests in
`sandbox/podman` with `AGENTKIT_SANDBOX_BASE_IMAGE`,
`AGENTKIT_SANDBOX_OVERLAY_IMAGE`, `AGENTKIT_SANDBOX_IMAGE_VERSION` and
`AGENTKIT_CONTAINER` set; it is not part of `make check`. Without an engine,
without `AGENTKIT_SANDBOX_BASE_IMAGE` or under `-short`, those tests skip.
The `GitRepo` test clones `AGENTKIT_SANDBOX_GIT_URL` (default
`https://github.com/agent-fox-dev/agentkit-go`) at depth 1, and the
micro-VM test runs only with `AGENTKIT_SANDBOX_RUNTIME=krun`.

### Sandbox images

The agent is published in two images, both with the agent at
`sandbox.AgentPath` (`/usr/local/libexec/agentkit-sandboxd`), owned by root
with mode 0755, and `ENTRYPOINT` set to it. A provider must not rely on that
entrypoint: an embedder's image built with the overlay keeps its own, so
providers start the agent with `--entrypoint sandbox.AgentPath` and
`sandbox.AgentArgs(spec)`.

| Identifier | Value or behaviour |
|---|---|
| `sandbox.BaseImageRepo` | `ghcr.io/agent-fox-dev/agentkit-sandbox` |
| `sandbox.OverlayImageRepo` | `ghcr.io/agent-fox-dev/agentkit-sandboxd` |
| `sandbox.ImageTag(version)` | The tag of a release version: `version` without its `v`. Only `vMAJOR.MINOR.PATCH` with no leading zeros and an optional `-prerelease` is accepted; `(devel)`, build metadata (`+dirty`, `+incompatible`) and Go pseudo-versions give `("", false)`. |
| `sandbox.DefaultImage()` | `BaseImageRepo:<tag>` for the build's version (`buildver`: the link-time stamp, else the sandbox module's version, else `(devel)`). |
| `sandbox.OverlayImage()` | `OverlayImageRepo:<tag>` for the build's version. |
| `sandbox.ErrNoPublishedImage` | `sandbox: no published image for this agentkit-go version`. `DefaultImage` and `OverlayImage` wrap it when the version has no published image; the message names the version and says to set `Spec.Image` or build an image with `make sandbox-images`. |
| `sandbox.OverlayCopyLine(ref)` | `COPY --from=<ref> /usr/local/libexec/agentkit-sandboxd /usr/local/libexec/`: the one line an embedder adds to its own Containerfile. |

The default is the exact `X.Y.Z` tag, never `X.Y`: the `Tools` digest check
fails when the host and the agent are at different versions.

**The base image** (`--target base`):

| Content | Value |
|---|---|
| Base | `debian:trixie-slim` |
| Packages | `git`, `bash`, `ca-certificates`; nothing else (the agent carries rsync itself; zombie reaping is the provider's `--init`) |
| User | `agentkit`, uid 1000, gid 1000, home `/home/agentkit`, shell `/bin/bash`; `USER 1000:1000` |
| Workspace | `/workspace`, owned 1000:1000, mode 0755, `WORKDIR`; not a `VOLUME` |
| git | `safe.directory = *` in the system config |
| Agent | cgo-built (tree-sitter outlines), at `sandbox.AgentPath` |
| Environment | none beyond the Debian image's `PATH` |
| Labels | `org.opencontainers.image.source` (`https://github.com/agent-fox-dev/agentkit-go`), `licenses` (`Apache-2.0`), `title`, `description`, `version`, `revision` |

**The overlay image** (`--target overlay`) is `FROM scratch` with one layer:
the static agent (`OutlineBackend` `none`) and the same labels.

**Tags.** A release `vX.Y.Z` publishes `X.Y.Z`, plus a moving `X.Y` when there
is no prerelease part, for linux/amd64 and linux/arm64; there is no
`latest`. A re-run rebuilds a tag, so an embedder who needs an immutable
image pins its digest. [`releasing.md`](releasing.md) has the details.

**Make variables** for `make sandbox-images` and `make sandbox-images-test`,
each overridable on the command line:

| Variable | Default |
|---|---|
| `CONTAINER` | `podman` when on `PATH`, else `docker` |
| `SANDBOX_BASE_IMAGE` | `ghcr.io/agent-fox-dev/agentkit-sandbox` |
| `SANDBOX_OVERLAY_IMAGE` | `ghcr.io/agent-fox-dev/agentkit-sandboxd` |
| `SANDBOX_IMAGE_TAG` | `dev` |
| `SANDBOX_PLATFORM` | empty: the host platform; when set, passed as `--platform` |
| `AGENTKIT_VERSION` | empty: the agent reports `(devel)` |
| `AGENTKIT_REVISION` | `git rev-parse HEAD`, or empty outside a checkout |

`make sandbox-images-test` runs the image tests in `sandbox/images` with
`AGENTKIT_SANDBOX_BASE_IMAGE`, `AGENTKIT_SANDBOX_OVERLAY_IMAGE`,
`AGENTKIT_SANDBOX_IMAGE_VERSION` and `AGENTKIT_CONTAINER` set; without those
variables, a container engine, or under `-short`, the image tests skip.

## Limits on untrusted input

`wire.Defaults()`: 16 MiB per message, 1,000,000 elements per container,
depth 64, 2,000,000 nodes. The MCP client holds every inbound stdio frame,
JSON response body and SSE event to `ConnectionOptions.Limits` (zero fields
take these defaults) and rejects duplicate keys before the SDK decodes it. The
Anthropic provider checks each stream event and response body the same way,
with the defaults. A sandbox proxy guards the `Data` and `Metadata` of every agent answer the
same way, and every sandbox protocol message is bounded by
`sandbox.MaxMessageBytes`, also 16 MiB.

## Compatibility

Go 1.27 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64 with cgo off; the host is also
built with cgo on. cgo code lives only in `//go:build cgo` files, each with a
pure-Go fallback.
