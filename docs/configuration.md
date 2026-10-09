# Configuration

AgentKit is a library. It has no config file of its own and no global state.
Configuration is one of:

1. fields on `agentkit.Config` and the per-provider `Options` structs,
2. environment variables, read at request time,
3. one optional TOML section, `[mcp]`, that an *embedding application* may
   load with the parser the SDK ships.

[`examples/README.md`](../examples/README.md) walks through the three decisions
every application has to make (credential, base URL, model). This file is the
reference.

## Environment variables

### Credentials and deployments

`anthropic.Resolve(env)` chooses the deployment and builds the SDK client
(`provider/anthropic/resolve.go`). The provider calls it on every request
unless `anthropic.Options.Client` is set, reading `RequestOptions.Env` first,
then `Options.Getenv` (or the process environment).

| Variable | Effect |
|---|---|
| `CLAUDE_CODE_USE_VERTEX` | `1` or `true` selects Claude on Vertex AI. Any other value, `0` and `false` included, leaves it off whatever else is set. |
| `ANTHROPIC_VERTEX_PROJECT_ID`, then `GOOGLE_CLOUD_PROJECT` | The Vertex project. Required once Vertex is selected; they never select it. |
| `CLOUD_ML_REGION`, then `GOOGLE_CLOUD_LOCATION`, `CLOUDSDK_COMPUTE_REGION` | The Vertex location; default `global`. |
| `ANTHROPIC_VERTEX_BASE_URL` | A proxy in front of Vertex. |
| `CLAUDE_CODE_USE_BEDROCK` | `1` or `true` selects Claude on Amazon Bedrock (when Vertex is not selected). |
| `AWS_REGION`, then `AWS_DEFAULT_REGION` | The Bedrock region; default `us-east-1`. |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | Static Bedrock credentials. Without them, `AWS_BEARER_TOKEN_BEDROCK`; without that, the AWS SDK's own credential chain. |
| `ANTHROPIC_API_KEY` | The Anthropic API key (`x-api-key`), for the direct deployment. |
| `ANTHROPIC_AUTH_TOKEN` | A bearer token for the direct deployment; on Vertex, a Google access token (anything but `sk-ant-…`). |
| `ANTHROPIC_OAUTH_TOKEN` | An OAuth bearer (`sk-ant-oat…`) for the direct deployment; adds the `oauth-2025-04-20` beta. |
| `ANTHROPIC_BASE_URL` | A proxy or gateway in front of the Anthropic API. |

With no cloud flag and none of the three Anthropic credentials, `Resolve`
fails with `anthropic.ErrNoCredentials` ("anthropic: missing credentials: …"),
and the provider ends the turn with that message rather than sending a
request. A Vertex selection with no project fails the same way, naming the
variables. `anthropic.Options.VertexProject` / `VertexLocation` select Vertex
and set its location in code, over the environment.

### SDK variables

| Variable | Read by | Effect |
|---|---|---|
| `AGENTKIT_MODEL` | `examples/*` only | Model spec (`vendor/id`, or a bare unambiguous id) overriding an example's default. The library never reads it. |

Subprocess tools run with a reduced environment (`tools.ReducedEnv`): `PATH`,
`HOME`, `LANG`, `TMPDIR`, `TERM` and a few non-secret cloud settings are kept;
provider-prefixed variables and anything ending in `_TOKEN`, `_SECRET`,
`_API_KEY`, `_PASSWORD` or `_CREDENTIALS` are stripped.

## `agentkit.Config`

`agentkit.New(cfg)` builds an `Agent` from it, and refuses a config it could
not run: no `Client` and no `Provider`, an empty `Model`, an invalid tool
hierarchy (a tool with both or neither of `Handler` and `Execute`, a
reachability cycle, a wrapper reaching a `Terminating` tool), or a reachable
shell tool with no `Guard`.

| Field | Meaning / default |
|---|---|
| `Client` | `*anthropic.Client` from the official SDK, built by `anthropic.Resolve` or by the caller. Requests go through `provider/anthropic` over it. |
| `Provider` | A `core.ProviderClient` (a test double such as `provider/faux`, or a custom provider). When set it is used instead of `Client`. |
| `Model` | Catalog id. `catalog.Lookup` supplies the context window, output cap and prices; an id the catalog does not list gets the default row (no price). |
| `Effort` | `agentkit.Effort` (`core.Effort`): `""` (no thinking parameter), `low`, `medium`, `high`, `xhigh`, `max`. On an adaptive model it is sent as `thinking: {"type":"adaptive"}` with `output_config.effort`; on a budget model as `thinking: {"type":"enabled","budget_tokens":N}`, N from the catalog row; on a model without thinking, or at a level the row does not list, neither is sent. Never clamped to another level. |
| `System` | The base prompt. Empty means the built-in base instructions and universal guidelines; either way the active tools' own guidelines (`Tool.PromptGuidelines`, and the shell guidelines) follow it. |
| `Prefix` | Messages sent after the system prompt and before the transcript on every request (`core.Request.Prefix`), with a cache breakpoint on their last block. Never recorded in the transcript. |
| `Tools` | The tools the agent can run. Each sets exactly one of `Handler` and `Execute`. |
| `Policy` | `core.ToolPolicy`: `Tools`, `NoTools` (`all` / `builtin`), `ToolNames`, `ExcludeTools`, `CustomTools`, resolved over `Config.Tools` once, in `New`, and applied to what wrappers reach too. Non-nil empty `Policy.Tools` means no tools. |
| `Guard` | `core.BeforeToolCall`, the authorization boundary for every call, nested ones included. Required when a shell tool is reachable (`ErrUnguardedExecute` from `New` otherwise); use `guard.Restricted` or `guard.AllowAll`. |
| `After` | `core.AfterToolCall`. Receives the handler's `ToolResult` by value and the mutable `Result *ToolResultMessage`. `ToolResultMessage.Metadata` carries the tool's structured metadata (in the transcript and events, never sent to the model). |
| `MaxTurns` | Ends a run that would go on past this many turns with `core.RunStopMaxTurns` and an error wrapping `core.ErrMaxTurns`, after the last turn's results are recorded. Zero is unbounded. |
| `MaxCostUSD` | Checked before each request against the run's cost so far: at or past it, the request is not sent and the run ends with `core.RunStopBudgetExceeded` and an error wrapping `core.ErrBudgetExceeded`. Usage a provider did not price is priced at the model's catalog row, so an id the catalog does not list costs nothing and never trips it. Zero is unbounded. |
| `Timeout` | The run's deadline. Past it the run ends with `core.RunStopTimeout` and an error wrapping `context.DeadlineExceeded`. A cancellation from outside — the caller's own cancel or deadline — ends it with `core.RunStopAborted` and an error wrapping `core.ErrAborted` and the context's error. Zero is unbounded. |
| `Prune` | `agentkit.PruneOptions{Threshold, KeepTurns}`; the zero value is off. Before each request the driver estimates its size: the context the latest assistant message's usage reports, plus 4 characters per token for the messages after it, plus what pruning took off the request that usage measured — the decision is on the unpruned size, so pruning stays on once it starts. With no usage reported, it is 4 characters per token over the system prompt, the tools, the prefix and the messages. When the estimate reaches `Threshold` of the model's context window, the content of every tool result older than the last `KeepTurns` turns is replaced in the request by `[result of NAME (N bytes) elided; call again if needed]`. The result keeps its `ToolUseID`; `Agent.Messages()` and `RunResult.Messages` keep it whole. `KeepTurns: 0` elides even the results the model has just asked for. |
| `MaxTokens` | Output cap per response, capped at the model's output cap. Zero → `core.DefaultMaxTokens` (32768), not the model cap. |

Whether or not `Prune` is set, a request whose estimate is still larger than
the model's context window after pruning is not sent: the run ends with
`core.RunStopError` and an error naming the model and its window.

The event stream is unbounded and never drops an event or blocks the run;
`Config` has no stream options.

A batch's calls run concurrently, one goroutine per call, unless a tool in the
batch is `Sequential`. When any call that ran returns `Terminate: true` (or
`After` sets it), the run ends with `core.RunStopToolTerminate` once every
call in the batch has finished. A call blocked by `Guard` or refused for its
arguments casts no vote of its own; a `Guard` decision with both `Block` and
`Terminate` ends the run (`guard.Options.TerminateOnBlock`). Observation is the event stream `Agent.Stream` returns
(`Run` waits for the result and reads none of them): turn, message, tool execution (`ParentToolUseID` set for a
nested call) and `core.ErrorEvent` for an error the run survives.

Header precedence, lowest to highest: attribution defaults, provider/auth
headers, `Model.Headers`, `RequestOptions.Headers`. A nil value at a higher
layer deletes the name.

## Provider options

Requests go through the official SDK client
(`github.com/anthropics/anthropic-sdk-go`), which owns the transport, retries
and stream framing. Every tool is declared with `"strict": true`, so the API
guarantees arguments that match its input schema; a tool's output schema,
reachable tools and terminating flag are never sent. `tool_choice` is `auto`,
`none` or absent: a forced choice (`any`, or a named tool) is never sent,
because current models reject it.

Prompt-cache breakpoints (`cache_control: {"type":"ephemeral"}`) go on the
last system block, the last tool, the last block of `core.Request.Prefix`
(`Config.Prefix`, sent ahead of the history on every request) and the last block of
the final user message. A replayed `tool_use` input, and any block replayed
verbatim, reaches the wire with the exact bytes it arrived with;
`anthropic.BuildRequestJSON(req, model)` returns the body as sent.

Each response's `core.Usage` carries input, output, cache-read and
cache-write tokens and `Requests: 1`; `Usage.Add` sums them, so a run's usage
counts its model requests. `anthropic.TranslateMessage` and
`anthropic.TranslateUsage` translate an SDK `Message` and an SDK usage pair
the same way the stream does. `anthropic.Options`:

| Field | Meaning |
|---|---|
| `Client` | `*anthropic.Client` (the SDK's). Requests go through it as configured; nil builds one from the environment per request. |
| `BaseURL` | Overrides `ANTHROPIC_BASE_URL` and the catalog row. |
| `HTTPClient` | The HTTP client the built SDK client uses. |
| `Getenv` | Injectable environment; nil means `os.Getenv`. |
| `MaxRetries` | `*int`; overrides the SDK's retry count (default 2). |
| `Betas` | Dated beta headers, opt-in; `compact-2026-01-12` enables server-side compaction; the compaction blocks it returns are dropped, not replayed (core has no block for them). |
| `VertexProject`, `VertexLocation` | Select the Vertex AI deployment. |
| `VertexTokenSource` | `oauth2.TokenSource` for Vertex. Nil uses a Google access token in `ANTHROPIC_AUTH_TOKEN`, then Application Default Credentials, found on the first request. |
| `BillingLookup` | Resolves a served model id to its catalog row for pricing. |
| `ToolPrefix`, `OnToolPrefixSync` | The per-session tool-schema cache and its reconciliation reports. |
| `Now` | Injectable clock for timestamps. |

## Built-in tool options (`tools.Options`)

| Field | Default |
|---|---|
| `Workspace` | `tools.NewWorkspace("")` — the current directory. File tools cannot leave it. |
| `SpillDir` | A per-workspace directory under `os.TempDir()`. Spill files are the embedder's to clean. |
| `DisableSpill` | false. |
| `Env` | `tools.ReducedEnv(nil)`. |
| `Ignore` | The real gitignore environment; `tools.NoGlobalExcludes()` pins an empty global layer for tests. |
| `Symbols` | `tools.SymbolOptions{}`. Configures the symbol table behind `find_symbol`. Fields: `MaxFiles` (file-count bound per build/refresh pass; default 50 000), `MaxDuration` (wall-time bound; default 2 s). |
| `Index` | `nil` (`tools.Index`). When set, `All()` appends the index's tools after the built-ins and `write_file`, `edit_file` and the shell tools call `Index.Invalidate` to keep the index fresh. See the `codesearch` module below. |

`tools.All` returns `read_file`, `write_file`, `edit_file`, `list_files`,
`find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`,
`execute` and `run_command` on every platform. When `Options.Index` is set,
`All()` appends the index's tools (e.g. `code_search`) after the built-in list.
`read_file` reads text; a file whose leading bytes are a PNG, JPEG, GIF or
WebP signature is refused with the error `unsupported_file`. Output
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
| `MaxFiles` | 100 000 | File-count bound for the index build. |
| `MaxBytes` | 1 GiB | Total indexed content bound. |
| `MaxBuildTime` | 60 s | Wall-time bound for the index build. |
| `TempDir` | `os.TempDir()` | Where shard files are written. |

## Code mode (`codemode.Options`)

`codemode.New(tools, opts)` builds the code-mode tool. `codemode.DefaultOptions()`
returns these defaults; a zero field takes its default.

| Field | Default | Meaning |
|---|---|---|
| `Name` | `code_mode` | The tool's name. A bound tool may not have it. |
| `Description` | generated | When set, used verbatim and nothing is generated. |
| `DescriptionTemplate` | `codemode.DefaultDescriptionTemplate` | A `text/template` over `codemode.DescriptionData` for the instructions section. The bound tools' declarations always follow it. |
| `Guidelines` | built-in code-mode guidance | The tool's `PromptGuidelines`. |
| `MaxTimeout` | 30 s | Wall time per script. Past it: `timeout`. |
| `MaxSteps` | 100 000 | Starlark execution steps per script. Past it: `step_limit_exceeded`. |
| `MaxCalls` | 50 | Tool calls per script, direct and in `parallel` together. A call, or a whole `parallel(...)`, that would go past it is not made, and the script stops with `call_limit_exceeded`. |
| `MaxConcurrentCalls` | 8 | Calls one `parallel(...)` has in flight at once. |
| `MaxOutputBytes` | 102 400 (100 KB) | Printed output plus the return value. Past it: `output_limit_exceeded`, with the head and tail kept. |
| `SpillDir` | `agentkit-codemode` under `os.TempDir()` | Where the complete output of a truncated script is written. |
| `DisableSpill` | `false` | When true, truncated output is not written to disk. |

A bound tool's name must be a Starlark identifier, and must not be a Starlark
builtin or one of `parallel`, `call`, `is_error`, `main` and `result`. Names
must be unique, and no code-mode tool may be bound, at any depth.

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
| `name` | Required, unique. Keys the pool and the tool prefix. |
| `command`, `args`, `dir` | Spawn a stdio server. |
| `url` | Connect to a remote server (see `transport`); the protocol version is negotiated. If both `command` and `url` are set, the command wins and a warning is emitted; one of them is required. |
| `env` (table) | Environment for the subprocess; values may use `${VAR}`, resolved from the secrets store at spawn time. |
| `headers` (table) | Headers for every request to a `url` server; `${VAR}` supported. The default HTTP client does not follow redirects. |
| `tool_prefix` | Override the default `<name>__`. |
| `timeout_s` | Integer or float seconds; default 30. |
| `allow_sampling` | Advertise sampling to the server and answer its requests through `ConnectionOptions.Sampling` (refused when that is nil). Default false. |
| `per_session_call_limit` | Default 1000; negative disables. |
| `per_session_reconnect_limit` | Default 3; negative means never reconnect. |
| `transport` | For a `url` server: `"streamable-http"` (default) or `"sse"` (the 2024-11-05 HTTP+SSE transport). Any other value is an error and the server is dropped. |

## Limits on untrusted input

`wire.Defaults()`: 16 MiB per message, 1,000,000 elements per container, depth
64, 2,000,000 nodes. The MCP SDK decodes protocol messages, but every inbound
stdio frame, JSON response body and SSE event the MCP client receives is held
to `ConnectionOptions.Limits` (zero fields take these defaults) and to
duplicate-key rejection first.

## Compatibility

Go 1.27 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64 with cgo off; the host is also
built with cgo on. cgo code lives only in `//go:build cgo` files, each with a
pure-Go fallback.
