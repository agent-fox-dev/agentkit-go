# Configuration

AgentKit is a library. It has no config file of its own and no global state.
Configuration is one of:

1. fields on `agentkit.Config`, `agentkit.Toolset` and the option structs of the packages below,
2. environment variables, read when an Anthropic client is resolved,
3. one optional TOML section, `[mcp]`, that an embedding application may load
   with `mcp.ParseConfig`.

[`examples/README.md`](../examples/README.md) walks through the decisions every
application has to make (deployment, credential, model). This file is the
reference.

## `agentkit.Config`

`agentkit.New(cfg)` builds an `Agent` from it, and refuses a config it could
not run: no `Client` and no `Provider`, an empty `Model`, or a `Toolset` that
`agentkit.NewRunner` refuses (below). It checks the client and the model
first, and returns `NewRunner`'s error unchanged.

| Field | Meaning / default |
|---|---|
| `Client` | `*anthropic.Client` from the official SDK, built by `anthropic.Resolve` or by the caller. `New` builds `anthropic.Provider(model, anthropic.Options{Client: cfg.Client})` over it. |
| `Provider` | A `core.ProviderClient` (a test double such as `provider/faux`, or a custom provider). When set it is used instead of `Client`. |
| `Model` | Catalog id. `catalog.Lookup` supplies the context window, output cap, prices and thinking kind; an id the catalog does not list gets the default row (below). |
| `Effort` | `agentkit.Effort` (`core.Effort`): `""` (no thinking parameter), `EffortLow`, `EffortMedium`, `EffortHigh`, `EffortXHigh`, `EffortMax` (`low` … `max`). On an adaptive model it is sent as `thinking: {"type":"adaptive"}` with `output_config.effort`; on a budget model as `thinking: {"type":"enabled","budget_tokens":N}`, N from the catalog row; on a model without thinking, or at an effort the row does not list, neither is sent. Never moved to another effort. |
| `System` | The base prompt. Empty means `prompt.BaseInstructions` and `prompt.UniversalGuidelines`. Either way the active tools' own guidelines (`Tool.PromptGuidelines`, and the shell guidelines) follow it (`prompt.Build`). |
| `Prefix` | Messages sent after the system prompt and before the transcript on every request (`core.Request.Prefix`), with a cache breakpoint on their last block. Never recorded in the transcript. |
| `Toolset` | The agent's tools, the policy that selects among them, the guard and the after-interceptor ([`agentkit.Toolset`](#agentkittoolset), below). The zero `Toolset` is an agent with no tools. |
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
- When any call that ran returns `Terminate: true` (or `Toolset.After` sets
  it), the run ends with `core.RunStopToolTerminate` once every call in the
  batch has finished. A call blocked by `Toolset.Guard` or refused for its
  arguments casts no vote of its own; a `Guard` decision with both `Block` and
  `Terminate` ends the run (`guard.Options.TerminateOnBlock`).
- A response cut off at `max_tokens` runs none of its tool calls; each gets
  the fixed result `Tool call "NAME" was not executed: the response hit the
  output token limit, ...`.
- A run whose last response is a refusal ends with `core.RunStopRefusal` and
  an error wrapping `core.ErrRefusal`.
- The event stream is unbounded and never drops an event or blocks the run;
  `Config` has no stream options.

## `agentkit.Toolset`

The tool side of an agent. `Config.Toolset` gives an agent one, and
`agentkit.NewRunner(ts)` builds a `Runner` from one with no model: the agent
holds the `Runner` it runs its calls through, and `Agent.Runner()` returns it.
The zero `Toolset` has no tools, which is valid.

| Field | Meaning / default |
|---|---|
| `Tools` | The tools registered. Each sets exactly one of `Handler` and `Execute`. |
| `Policy` | `core.ToolPolicy`: `Tools`, `ToolNames`, `ExcludeTools`, `NoTools` (`builtin` / `all`), `CustomTools`. Resolved over `Tools` once, in `NewRunner`, and applied to what wrappers reach too. A non-nil empty `Policy.Tools` means no tools. |
| `Guard` | `core.BeforeToolCall`, the authorization boundary for every call, nested ones included. Required when a shell tool (`execute`, `run_command`) is reachable. Use `guard.Allow(programs...)`, `guard.Restricted(opts)` or `guard.AllowAll`. |
| `After` | `core.AfterToolCall`. Receives the handler's `ToolResult` by value and the mutable `Result *ToolResultMessage`; its `Terminate *bool` can set or clear a call's terminate vote. |
| `Events` | `func(core.Event)`, the sink for the execution events of calls a `Runner` runs outside an agent run. Nil discards them. |

`NewRunner` checks every tool in `Tools`, resolves `Policy`, checks every
resolved tool, then checks the shell guard, and returns the first failure with
a nil `Runner`:

- `agentkit: tool "NAME" has neither Handler nor Execute`
- `agentkit: tool "NAME" sets both Handler and Execute; exactly one`
- `agentkit: reachable tools cycle detected: a -> b -> a`
- `agentkit: terminating tool "NAME" cannot be reached through wrapper "NAME"`
- an error wrapping `core.ErrUnguardedExecute` (its text names `Toolset.Guard`)
  with the suffix ` (tool "NAME")`, or ` (wrapper "NAME" reached shell tool "NAME")`
  for a shell tool behind a wrapper

`New` makes the same checks by calling `NewRunner`, so the two refuse exactly
the same toolsets with the same errors. `NewRunner` copies `Tools`, so a
caller who changes its own slice afterwards does not change the runner. A
`Runner` is immutable after construction and safe for concurrent use.

A `Runner` has four methods: `Tools()` is the resolved set in policy order
(what a model, an MCP server or a prompt should see; each call returns a fresh
slice); `Reachable()` is `core.ReachableTools` over it; `Call(ctx, name,
input)` runs one call as a batch of one under a generated `call_` id and
returns its `core.ToolResult`; and `Batch(ctx, calls...)` runs calls with the
rules an agent applies to a turn's. Every outcome is a result, never a Go
error. `Guard` sees `Assistant` nil and `TurnCount` 0 for a call made through
a runner, which is how a guard can tell that no model made it. A result's
`Terminate` is the call's final vote; `Call` and `Batch` act on none.

### `Events`

For calls a runner runs outside an agent run, `Events` receives what a run's
stream receives for them: a `core.ToolExecutionStartEvent` and a
`core.ToolExecutionEndEvent` for every call (nested ones carry
`ParentToolUseID`), a `core.ToolResultEvent` for each top-level call, carrying
the message as `After` left it, and a `core.ErrorEvent` for each panic the
pipeline recovered. A call's start event precedes its end event, and a
top-level call's end event precedes its result event.

`Events` is called from the goroutines that run the calls, several at once, so
it must be safe for concurrent use and should not block. Start events are
emitted while a batch is prepared, and end and result events while a call is
finalized, under a lock scoped to that batch (or to that wrapper's nested
calls), so a sink that waits for another call of the same batch to finish never
returns. No runner-wide lock is held while it runs, so a sink may itself call
`Runner.Call`.
`(*core.EventStream).Push` is a conforming sink. A panic in `Events` is
recovered and the event is dropped; it is not reported again, because the sink
that would receive the report is the one that panicked.

During an agent run the run's stream receives the events of the run's calls and
`Events` is not called for them. Calls made through `Agent.Runner()`, during a
run or not, are outside every run: they do not enter the transcript, are not
counted in `Agent.Usage`, do not claim the run slot, and their events go to
`Events`.

## `guard.Options`

`guard.Restricted(opts)` refuses `BlockedTools`, then, for `execute`, shell
operators (unless `AllowShellOperators`), then reads the line with
`guard.Commands` and, for every command on it, refuses environment
assignments (unless `AllowEnvPrefixes`) and asks `guard.Check(argv, opts)`
whether the program is on `AllowedPrograms`. `Check` is a pure function
returning a `guard.Decision{Block, Reason, Terminate}`. The guard is a floor
for unattended safety, not a sandbox.

`guard.Commands(line)` is the reading the guard judges, exported so a
stricter policy layered over `Restricted` judges the same commands: every
simple command the line runs, the ones in substitutions, subshells, process
substitutions and unquoted heredoc bodies included, each as a
`guard.Command{Text, Assign, Argv, Redirects}`. Expansions are left as
written, so a program the shell would compute (`$CMD`, `$(echo rm)`) never
matches a listed name. A line it cannot read with certainty — an unterminated
quote, a `case` statement, a heredoc with no terminating line — is a
`*guard.SyntaxError`, and `Restricted` refuses it. `TestCommandsAgainstBash`
checks the reading against bash; it runs only with `AGENTKIT_BASH_DIFF=1`.

`guard.Allow(programs...)` is `guard.Restricted` with only `AllowedPrograms`
set: its decisions, reasons (the `guard.Restricted: ` prefix) and `Terminate`
values are `Restricted`'s. It copies `programs`, so changing or appending to
the caller's slice afterwards does not widen the allowlist. With no programs
every `execute` and `run_command` call is blocked. `Restricted` is the way to
set the other options.

A sandbox is the boundary the guard never claimed to be. `guard.AllowAll`,
the interceptor that never blocks, is a reasonable choice only when every
shell tool the agent can reach is a proxy from a sandbox whose provider
reports `sandbox.IsolationContainer` or `sandbox.IsolationMicroVM`
(`sandbox/podman`), under `sandbox.NetworkNone` (or with egress you accept),
with `Spec.Resources` set, and with the files you export from it treated as
untrusted. It is never reasonable with `local` or with any local shell tool
in the set. `agentkit.New` and `agentkit.NewRunner` still require a
`Toolset.Guard` either way.

| Field | Meaning |
|---|---|
| `AllowedPrograms` | Programs that may run. A bare name admits only that bare name; a path admits only that path, after cleaning. A program spelled with a path separator is refused unless that path is listed. Empty blocks every shell call. |
| `AllowShellOperators` | Permit pipes, `;`, `&&`, `||`, redirection, subshells, substitution and expansion in `execute` commands (POSIX sh grammar). Off by default. When on, every command on the line is judged, not only the first, and a line `guard.Commands` cannot read is refused. |
| `AllowEnvPrefixes` | Permit `NAME=value` in `execute` commands, in front of a program or, under `AllowShellOperators`, on its own. Off by default. Even when on, names that change which binary runs or what is loaded into it (`PATH`, `LD_*`, `DYLD_*`, `BASH_ENV`, …) are refused. |
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
ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or ANTHROPIC_OAUTH_TOKEN, or select
Vertex AI (CLAUDE_CODE_USE_VERTEX=1) or Bedrock (CLAUDE_CODE_USE_BEDROCK=1)`). A Vertex
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
| `NonStrictTools` | Names of tools never declared strict (`strict: true`), however well they fit. The API totals its strict-mode limits over a request (20 tools, 16 union parameters, and a compiled-grammar size it does not publish) and refuses the whole request past any. Name the simple tools, a file read, to leave the budget to the complex ones; a named tool spends none of it, and its arguments are still validated before it runs. See errata 10. |
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

## `openrouter.Options`

`openrouter.New(model, opts)` returns a `core.ProviderClient` for one model
served by OpenRouter (`provider/openrouter`). It reaches OpenRouter through
OpenRouter's own Go SDK and its native Chat Completions API, and no other
endpoint. `model` is a `core.Model`: the window, the output cap, the prices and
the efforts come from it, and the provider holds none of its own. The row
`catalog.Select("openrouter", "STANDARD", "")` returns is the usual one:

```go
sel, err := catalog.Select("openrouter", "STANDARD", "")
if err != nil {
	return err
}
p, err := openrouter.New(sel.Model, openrouter.Options{}) // OPENROUTER_API_KEY from the environment
if err != nil {
	return err // wraps openrouter.ErrNoAPIKey, naming the variable
}
agent, err := agentkit.New(agentkit.Config{Provider: p, Model: sel.Model.ID, Effort: sel.Effort})
```

`agentkit.New` does not build this provider from a name: pass it as
`Config.Provider`. `Config.Model` is still resolved by `catalog.Lookup`, which
lists Claude rows only, and the provider uses the `core.Model` given to `New`.

`Stream(ctx, req)` has the contract of `anthropic.Provider`'s: a context already
done is refused with its error, every later failure is the last item of the
channel, and it is safe for concurrent use.

`New` makes no request. It fails, naming what is wrong, when the model id is
empty (`openrouter: the model id is empty`), when the key is unset (an error
wrapping `openrouter.ErrNoAPIKey`: `openrouter: OPENROUTER_API_KEY is not set
(set it in the environment or in Options.Env)`) and when `OPENROUTER_BASE_URL`
is set but is not an absolute http or https URL (`openrouter: OPENROUTER_BASE_URL
is "...", not an absolute http or https URL`, shown without user-info).

| Variable | Effect |
|---|---|
| `OPENROUTER_API_KEY` (`openrouter.APIKeyVar`) | The API key; sent only as `Authorization: Bearer <key>`. |
| `OPENROUTER_BASE_URL` (`openrouter.BaseURLVar`) | The base URL; default `https://openrouter.ai/api/v1` (`openrouter.DefaultBaseURL`). Trailing slashes are dropped. |

A variable is read from `Options.Env` first (an empty value falls through), then
from `Options.Getenv`, or the process environment when that is nil. An empty
value is unset. The provider reads these two variables and no other: no
`ANTHROPIC_*` or `OLLAMA_*` value reaches a request, and the SDK's own reads of
`OPENROUTER_HTTP_REFERER` and `OPENROUTER_X_TITLE` are switched off. Every
request is a `POST` to `<base>/chat/completions`, to that host only.

| Field | Meaning |
|---|---|
| `Env` | `map[string]string` holding explicit values for the two variables. |
| `Getenv` | Injectable lookup; nil means the process environment. |
| `HTTPClient` | Sends the requests. Nil means a client with no overall timeout, so only the caller's context cuts a long stream. |
| `MaxRetries` | `*int`; nil means `openrouter.DefaultMaxRetries` (2); a negative value means none. |
| `Now` | Injectable clock for message timestamps and for a `Retry-After` given as a date. |
| `Warnf` | Receives warnings; nil discards them. |

`openrouter.API` is `"openrouter-chat"`, the api string of the catalog's
`openrouter` vendor.

What the provider sends:

- `model` is `Model.ID` as written, `stream` is true and
  `stream_options.include_usage` is true, so the response reports its usage.
- The text blocks of `Request.System` are one leading `system` message, joined
  with a blank line; none when there is no text.
- The prefix, then the messages, in order. A user message without text and an
  assistant message with no text, no tool calls and no reasoning to replay are
  skipped. Each tool result is its own `tool` message, with
  `provider.ToolResultText` as its content (`Error: ` first for an error). No
  `cache_control` is ever sent.
- A tool call's `id` and `function.arguments` are sent as the model produced
  them, whichever model that was.
- Each tool is `{"type":"function","function":{"name","description","parameters"}}`,
  `parameters` being the tool's schema exactly as `schema.Schema` marshals it; a
  tool with no schema gets `{"type":"object","properties":{}}`. No `strict` flag.
- `tool_choice` is `auto`, `none` or absent. `max_completion_tokens` is the
  request's `MaxTokens` capped at `Model.MaxOutputTokens`, else the cap, else
  absent. `temperature` and `top_p` are sent when set, zero included, and `stop`
  when there are stop sequences.
- Effort follows `Model.Efforts` as the Anthropic provider reads it: none asked
  sends no `reasoning`; with no map the effort is sent by its name; with a map, a
  level that is absent or nil sends nothing and a present level sends its value.
  A value that is a positive integer is `reasoning.max_tokens`; any other is
  `reasoning.effort`. No effort is moved to another level.
- Reasoning is replayed to the model that produced it, as `reasoning_details`
  (each thinking block's `Signature`, a JSON array of the fragments received, in
  block order), and never as text. A message from another model sends none.
- Before encoding, `provider.RepairToolPairing` runs on the prefix and on the
  history: errored and aborted assistant turns are dropped, a result is kept only
  in the run after the turn that made the call, and an unanswered call gets an
  error result `No result provided`. `Warnf` is told once for each part changed.

What it returns: a `MessageStartEvent` before the first content event; text,
thinking and tool-call events in the order the vendor sends them, with block
indices from 0 in order of first appearance and a new block when the kind of
delta changes; then the block-end events in block order and the
`MessageEndEvent`, after the body has been read to its end. Keep-alive comments,
empty deltas and other choices are ignored. `finish_reason` is kept in
`RawStopReason`; `stop` is `StopReasonStop`, `length` `StopReasonLength`,
`tool_calls` `StopReasonToolUse` and `content_filter` `StopReasonRefusal`,
anything else `StopReasonStop`. Tool calls stay in the message whatever the
reason. A call's arguments are the fragments concatenated byte for byte; empty
arguments, and arguments that are not a JSON object (with a warning naming the
call), become `{}`; a call without an id is given `call_` and random hex.

The usage is netted: `CacheReadTokens` from `cached_tokens`, `CacheWriteTokens`
from `cache_write_tokens`, `InputTokens` the prompt tokens less both (never
below 0), `OutputTokens` from `completion_tokens`, `ReasoningTokens` from
`reasoning_tokens` (never above the output) and `TotalTokens`. `Requests` is 1
however many attempts a turn took, and `BilledModel` the served model when it
differs from `Model.ID`. The cost is the response's `usage.cost`; else, when the
model has any price, `provider.ComputeCost` over the usage; else `CostUSD` stays
0 with its presence bit clear, so an unpriced response is told from a free one.
A response with no usage leaves it unreported. A count of 0 for the prompt,
completion or total is not marked reported, because the SDK cannot tell it from
a missing one.

Errors and retries:

| Failure | Result |
|---|---|
| HTTP 429 or 5xx, or an error object or `finish_reason: "error"` inside the stream with such a code before any content | Retried up to `MaxRetries` times. |
| HTTP 401 or 403 | Not retried. A `*openrouter.StatusError`, text `openrouter: HTTP 401: <message> (check OPENROUTER_API_KEY)`. |
| Any other non-2xx | Not retried. `*openrouter.StatusError`, text `openrouter: HTTP <code>: <message>`. |
| An error inside the stream after content, or with another code | Not retried. `*openrouter.StatusError`, text `openrouter: error <code> in stream: <message>`. |
| A body that ends without a `finish_reason` | `openrouter.ErrStreamTruncated`, `openrouter: stream ended before finish_reason`. |
| No HTTP response | Not retried. `openrouter: <error>`. |
| The caller's context cancelled, in the request, in the stream or in a wait | `StopReasonAborted`, `ErrorMessage` `Request was aborted`, `Err` wrapping `core.ErrAborted`. |

The message is the vendor's `error.message`, else the first line of a body that
is not JSON, else the status text. A failed turn's message has `StopReasonError`,
the text and thinking streamed so far and never a tool call (a Chat Completions
tool call is not complete before its `finish_reason`), and the usage that
arrived. It ends with a terminal `ErrorEvent`, then the `MessageEndEvent`.

Between attempts the provider waits for the response's `Retry-After` when it is
0 to 60 seconds, as seconds or an HTTP date, and otherwise 0.5 s doubling for
each retry to 8 s with up to 25% added. A discarded attempt emits no event. The
SDK's own retries are off. No error text, event, `ErrorMessage` or warning
holds the API key: every occurrence, and its URL-escaped form, is replaced by
`[redacted]`.

## Model catalog

The embedded `catalog/catalog.json` (version `2026-10-21`, schema version 1)
lists models for the three providers an agent can run on, which
`catalog.Providers()` returns in this order: `anthropic`, `openrouter` and
`ollama`. The set is fixed in code; a vendor section of a catalog given to
`catalog.Parse` that is not one of the three cannot be selected.

- `catalog.Lookup(id)` resolves a Claude model id against the `anthropic` rows
  only; the `anthropic/` prefix is optional. `agentkit.New` calls it with
  `Config.Model`.
- `catalog.Select(provider, model, effort)` resolves any of the three
  providers, offline ([below](#catalogselect)).

Only `anthropic` has a vendor wire in this repository (`provider/anthropic`). The
`openrouter` and `ollama` sections, the tiers and `Select` are the catalog side
of [PRD 14](prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md): nothing
in `agentkit.New` reads them yet.

Prices are USD per million tokens. Thinking is `adaptive` when the row's
`thinking_level_map` holds effort names, `budget` when it holds token counts.

### Anthropic rows

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

### OpenRouter and Ollama rows

Each provider has one row. Both list `low`, `medium`, `high` and `max`, each
mapped to its own name, and not `xhigh` (it was not probed); they take text
input only.

| Provider | Model id | Context window | Max output | Input $/M | Output $/M | Cache read $/M |
|---|---|---|---|---|---|---|
| `openrouter` | `qwen/qwen3.8-flash` | 1,000,000 | 131,072 | 0.15 | 0.47 | 0.016 |
| `ollama` | `qwen3.8:27b` | 32,768 | 32,768 | 0 | 0 | 0 |

The OpenRouter prices are OpenRouter's list of 2026-10-21. The Ollama window is
the length the model ran at on the reference machine (32 GB Apple M4), not the
262,144 that `/api/show` reports; the row's note says an Ollama provider is to
send it as `num_ctx`. The Ollama price is zero because the model runs on the
embedder's own hardware.

### Ids the catalog does not list

An id the catalog does not list is served, not refused, so a model released
after this snapshot works the day it ships. It gets its provider's fallback
limits, no price, adaptive thinking and no effort map (an effort is sent by its
own name). `Lookup` reports it with `false`, and `Select` with
`Selection.Listed` false.

| Provider | Context window | Max output | Constants |
|---|---|---|---|
| `anthropic` | 1,000,000 | 128,000 | `catalog.DefaultContextWindow`, `catalog.DefaultMaxTokens` |
| `openrouter` | 131,072 | 32,768 | `catalog.OpenRouterDefaultContextWindow`, `catalog.OpenRouterDefaultMaxTokens` |
| `ollama` | 32,768 | 32,768 | `catalog.OllamaDefaultContextWindow`, `catalog.OllamaDefaultMaxTokens` |

A fallback is a documented guess, not a lookup at the vendor.

### Tiers

A tier name picks a model and an effort for a provider. The table is the
optional `tiers` object of each vendor in `catalog.json`; the embedded table is
fixed per release. `catalog.Parse` rejects a `tiers`
object with a key other than `SIMPLE`, `STANDARD` or `ADVANCED` (lower case
included), a missing tier, a model that is not a row of the same vendor, and an
effort that is not one of the five or that the row does not take, with a
`*catalog.ParseError` that names the path (for example
`vendors.ollama.tiers.STANDARD.effort`).

| Provider | `SIMPLE` | `STANDARD` | `ADVANCED` |
|---|---|---|---|
| `anthropic` | `claude-sonnet-5-5`, medium | `claude-sonnet-5-5`, high | `claude-opus-5-5`, xhigh |
| `openrouter` | `qwen/qwen3.8-flash`, low | `qwen/qwen3.8-flash`, medium | `qwen/qwen3.8-flash`, high |
| `ollama` | `qwen3.8:27b`, low | `qwen3.8:27b`, medium | `qwen3.8:27b`, high |

### `catalog.Select`

`Select(provider, model string, effort core.Effort) (Selection, error)`
resolves against the embedded catalog; `(*Catalog).Select` does the same against
a catalog from `Parse`. It reads no environment variable, does no I/O, returns a
deep copy of the row and is safe for concurrent use. The result is a
`Selection`:

| Field | Meaning |
|---|---|
| `Provider` | The canonical provider name. |
| `Tier` | The canonical tier when `model` named one, else empty. |
| `Model` | A `core.Model`; `Model.ID` is the id the vendor will receive. |
| `Effort` | The effort to request, reported as asked and never filtered or clamped against `Model.Efforts`: `xhigh` on the `ollama` row, which does not list it, is still reported. Whether it is sent is the provider's reading of `Model.Efforts`; `provider/anthropic` does not send an effort that is absent or null in the map. |
| `Listed` | Whether `Model` is a row of the provider's vendor. Always true for a tier. |

How the inputs are read:

- **Provider.** Trimmed and matched without regard to case; empty means
  `anthropic`.
- **Model.** The whole string, trimmed and matched without regard to case, is
  a tier when it is `SIMPLE`, `STANDARD` or `ADVANCED`, and is then never a model
  id (`STANDARD-1` is an id). Any other string is a model id: trimmed, its case
  kept. On `anthropic` it goes through `Lookup`, so `anthropic/` is removed. On
  `openrouter` and `ollama` it is looked up among that provider's rows only, and
  `anthropic/claude-sonnet-5-5` on `openrouter` is an OpenRouter id, kept with
  its prefix. A row of one provider is never returned for another.
- **Effort.** Trimmed and matched without regard to case against `low`,
  `medium`, `high`, `xhigh` and `max`, and normalized to the lower-case constant;
  empty means none asked.

The effort of the result is the explicit effort when one is given; otherwise the
tier's effort when `model` is a tier; otherwise none. An explicit model id
bypasses the tier table.

The inputs are checked in the order provider, model, effort; the first that
fails is returned, with the zero `Selection`. Each error wraps a sentinel that
`errors.Is` matches:

| Sentinel | Message |
|---|---|
| `catalog.ErrUnknownProvider` | `agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama` |
| `catalog.ErrNoModel` | `agentkit: no model given; name a tier (SIMPLE, STANDARD or ADVANCED); a model id is also accepted` |
| `catalog.ErrUnknownEffort` | `agentkit: unknown effort "turbo"; known efforts are low, medium, high, xhigh and max` |

Each message names the value as given. Naming a tier on a provider whose section
in a catalog from `Parse` has no `tiers` object fails with an error such as
`agentkit: provider "ollama" has no ADVANCED tier in this catalog`; the embedded
catalog has a table for each provider.

## Workspace (`workspace.Options`)

`workspace.Open(root, opts)` opens the workspace every scenario starts from: the
tools (`tools.For`), the code-search index (`codesearch.New`) and a sandbox's
`LocalDir.Ignore` (`ws.Ignore()`) are all built for one, so the ignore
configuration is given here, once.

| Field | Default |
|---|---|
| `Ignore` | The zero `workspace.IgnoreOptions`: the real gitignore environment, global excludes included. `workspace.NoGlobalExcludes()` pins an empty global layer, for tests and hermetic tool sets. |

An empty `root` is the current directory, and a root that does not exist yet is
kept in its lexical absolute form and is not an error. File tools cannot leave
the root. On a host `execute` can; inside a sandbox whose agent reports a
Landlock ABI (`AgentInfo.Landlock`), the agent's kernel ruleset confines
`execute` and every process it starts to the writable roots. Under `local`, or
on a kernel without Landlock, it does not.

`workspace.IgnoreOptions` has the hooks `Getenv func(string) string`,
`Home func() (string, error)` and `GitConfig func() string` (nil runs
`git config --path --get core.excludesFile`). A workspace runs at most one
`GitConfig` lookup, when its rules are first used; `ws.Ignore()` returns the
options with that lookup memoized, to pass on. The sources are, lowest
precedence first, the global excludes file, `.git/info/exclude` and the
`.gitignore` files from the scan root down; the last matching pattern wins; a
directory containing `.git` starts a new ignore root; a `.git` path is always
excluded; `.ignore` files are not read. `ws.Walk(ctx, sub, WalkOptions{IncludeHidden}, fn)`
visits what those rules leave under `sub`, with the engine rooted at `sub`, and
`ws.Ignored(rel, isDir)` answers the question for one path as a walk from the
workspace root would.

## Built-in tools (`tools.Options`)

`tools.For(ws, opts)` takes the workspace as its argument, and a nil workspace
is an error: there is no fallback to the working directory.

| Field | Default |
|---|---|
| `SpillDir` | A per-workspace directory under `os.TempDir()`. Spill files are the embedder's to clean. |
| `DisableSpill` | false. |
| `Env` | `tools.ReducedEnv(nil)`. |
| `Symbols` | `tools.SymbolOptions{}`: `MaxFiles` (default 50 000) and `MaxDuration` (default 2 s) bound each symbol-table build or refresh. |
| `Index` | nil. When set, `For` appends the index's tools after the built-ins, and `write_file`, `edit_file` and the shell tools call `Index.Invalidate`. |

`tools.For` returns `read_file`, `write_file`, `edit_file`, `list_files`,
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

`codesearch.New(ws, opts)` indexes the files `ws.Walk` visits, so the index uses the
workspace's ignore rules; `codesearch.Options` has no ignore setting:

| Field | Default | Meaning |
|---|---|---|
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
| `Duration` | Wall time from just before the child starts to its exit. A `timeout` reports at least `ExecOptions.Timeout`. |
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

## MCP serving (`mcpserve.Options`)

`mcpserve` is a nested module, `github.com/agent-fox-dev/agentkit-go/mcpserve`.
`mcpserve.New(r *agentkit.Runner, opts mcpserve.Options) (*mcpserve.Server, error)`
serves the tools of a runner to a harness on the same machine. A `Server` is
immutable and safe for concurrent use; it may serve any number of loopback
endpoints and one stdio session at once, over the same tools.

`New` refuses a nil runner (`mcpserve: Runner is nil`) and a runner holding a
tool whose input schema does not marshal, or whose top level is not an object
with `"type":"object"` (`mcpserve: tool "<name>": input schema …`). It reads
`r.Tools()` once, so the served set never changes afterwards. Each tool is
served under its own name and description with `json.Marshal` of its input
schema, or `{"type":"object"}` when it has none, and no output schema,
annotations or `_meta`. A `tools/call` runs `r.Call` with the request's
argument bytes and nothing else. Its result is one text item holding
`core.ToolResult.LLMText()`, then one for each `core.TextBlock` in `Blocks`;
`isError` is `!OK`. `Data`, `Metadata` and the `Terminate` vote are not served,
and a failed call (`invalid_arguments`, `blocked_by_policy`, `panic`,
`handler_error`, `aborted`) is an error result, never a protocol error.

| `Options` field | Meaning |
|---|---|
| `Name` | The server name in the initialize result. Empty means `agentkit`. |
| `Version` | The server version. Empty means this module's version from the binary's build information (the replacement's when it is replaced), or `(devel)`. |
| `Instructions` | Sent to the client in the initialize result. |
| `Logger` | A `*slog.Logger`; nil discards. See below. |

**`ServeLoopback(ctx)`** listens on TCP `127.0.0.1` on a port the system
chooses and returns an `Endpoint{URL, Token}`: the URL is
`http://127.0.0.1:<port>/mcp`, and the token is 24 bytes from `crypto/rand` as 48
lowercase hex characters, generated for each call. Neither is configurable.
Every request needs exactly one `Authorization: Bearer <token>` header,
compared in constant time, or it gets 401 with `WWW-Authenticate: Bearer` and
the body `unauthorized` before its body is read, whatever its path or method.
An authorized request for a path other than `/mcp` gets 404. An authorized body
is read up to `wire.Defaults().MaxMessageBytes` (16 MiB; 413 above it) and must
pass `wire.Guard` (400 if it does not) before it is replayed, byte for byte, to
the SDK's Streamable HTTP handler with its default stateful sessions. The
endpoint serves until `Endpoint.Close` is called or `ctx` ends: it stops
accepting, lets requests in flight finish for up to two seconds, then closes
the connections that remain and cancels the calls still running. `Close` blocks
until then, is idempotent and safe from several goroutines, and does nothing on
an `Endpoint` that `ServeLoopback` did not return. A `ctx` that is already done
returns `nil, ctx.Err()` and listens on nothing. Two calls on one `Server` give
independent endpoints.

**`ServeStdio(ctx)`** serves one session on `os.Stdin` and `os.Stdout`, as
newline-delimited JSON through `mcp.NewPipeTransport` under `wire.Defaults()`;
a frame that breaks those limits ends the session and never reaches the runner.
It returns `nil` when the client closes its end (after every reply owed is
written), `ctx.Err()` when `ctx` ends (the session is closed first and calls in
flight see a cancelled context), and otherwise an error starting `mcpserve: `.
A served session closes `os.Stdin` and leaves `os.Stdout` open; nothing but
protocol frames is written to stdout, so the program's own output belongs on
stderr. It is meant to be called once per process, which is not enforced.

**`Endpoint.ClaudeConfig(name)`** returns the JSON Claude Code's
`--mcp-config` takes, `{"mcpServers":{"<name>":{"type":"http","url":"<URL>",
"headers":{"Authorization":"Bearer <Token>"}}}}`, and **`PiConfig(name)`** the
same object with `"exposure":"direct"` in the server entry, which Pi's
`.pi/mcp.json` takes. Pi reads a project's `.pi/mcp.json` only once the project
is trusted, and its print mode (`-p`) cannot ask: run it with `--approve`, or it
skips the file, starts with no server and offers the model no tools. An empty
`name` means `agentkit`; the output is built with `encoding/json` from the
endpoint's fields as they are when it is called.

**Logging.** The `Logger` gets an Info record `tool call` for each served call
(`tool`, `ok`, `duration`, and `error`, the runner's error code, when the call
failed; a code that is not a short identifier is logged as `other`); a Warn
record `request refused` for each refused HTTP request (`peer` and a `reason`:
`missing or wrong token`, `body over the size limit`, `body rejected: <wire
rule>` or `body unreadable`); a Warn record `stdio session ended with an error`
(`reason`: `frame rejected: <wire rule>` or `session error`); and a Warn record
`http server error` for what `net/http` reports about its own failures. No
record carries the token, the `Authorization` header, a call's arguments, a
result's text or its `Detail`.

## Sandboxes (`sandbox.Spec`, `sandbox.ToolOptions`, `local`, the gateway)

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

| Field | `local` | `podman` / `docker` | `landlock` | `gateway` |
|---|---|---|---|---|
| `Image` | refused | yes; empty means `sandbox.DefaultImage()`; refused when it starts with `-` or holds whitespace or a control character | refused | the backend's; an empty `Image` becomes the client's own `sandbox.DefaultImage()` when the backend honours `Image` |
| `Workdir` | refused (the workspace is `<dir>/workspace`) | yes (default `/workspace`); must be absolute and clean, not `/`, `/tmp` or beneath it, not beneath `/proc`, `/sys` or `/dev`, and hold no `,` or control character | refused (the workspace is `<Root>/<id>/workspace`) | the backend's |
| `Env` | yes | yes, plus the defaults below; `HOME` and `TMPDIR` must be `/tmp`, the workspace, or beneath either | yes, plus `HOME`, `TMPDIR` and a default `PATH`; `HOME` and `TMPDIR` in `Spec.Env` are refused | the backend's; never logged by the gateway |
| `Resources.*` | refused | `CPUs`, `MemoryBytes`, `PIDs`; `DiskBytes` is refused; a zero field takes the engine's default | refused (no resource is bounded) | the backend's |
| `Network.Mode` | `NetworkUnrestricted` only; the zero `Spec` is refused | `NetworkNone` (`--network none`) or `NetworkUnrestricted` (the engine's default network); `NetworkAllowlist` is refused | `NetworkNone` (TCP denied by the agent's Landlock ruleset) or `NetworkUnrestricted`; `NetworkAllowlist` is refused | the backend's modes within the gateway's ceiling (`gatewayd.Config.NetworkModes`, default `NetworkNone`); others are refused, naming `Network.Mode` |
| `TTL` | yes | yes, twice: attach's host timer and the agent's `--ttl`; the volume carries the expiry | yes, twice: attach's host timer and the agent's `--ttl` | the backend's, at most the gateway's `MaxTTL` (default 24h); a longer one is refused, naming `TTL`; the gateway also removes the sandbox at `created + TTL` |
| `Labels` | yes (recorded in the marker) | yes, on the container, the init container and the volume; the key prefix `agentkit.sandbox.` is reserved, and a key may not be empty or hold `=`, whitespace or a control character, nor a value a control character | yes (recorded in the marker only) | the backend's; `Provider.List` reports them |
| `Provider` | no keys defined; any key is refused | no keys defined; any key is refused with a `*SpecError` | no keys defined; any key is refused with a `*SpecError` | sent as JSON: it must encode as JSON and pass `wire.Guard`; the backend decides |
| `RequireLandlock` | refused (`Capabilities.Landlock` is false: its agent runs in the embedder's process, which nothing may confine) | yes; the engine's default seccomp profile may withhold Landlock, which the agent then reports | yes; the provider's ABI 4 floor applies whether it is set or not | the backend's |

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

The agent always uses `workspace.NoGlobalExcludes()` (the workspace's own ignore
files still apply), its explicit environment, and no index.

### `sandbox.Open`

`sandbox.Open(ctx, p, spec, src, opts)` does `Create`, `Sync` and `Tools` in
one call and returns the sandbox with its proxy tools. It calls
`p.Create(ctx, spec)`, then `Sync(ctx, src)` when `src` is not nil, then
`Tools(ctx, opts)`, each once and in that order, so a `GitRepo` source, which
must be the first `Sync` and come before the first `Tools` call, works on every
provider that accepts one. A nil `src` skips the `Sync`. The `SyncResult` is
discarded: call `Create`, `Sync` and `Tools` yourself to read it. The sandbox
is returned exactly as `Create` returned it and is live; `ctx` bounds the three
calls only, and the caller closes the sandbox.

`Open` leaves nothing behind when it fails. A nil `p` is an error beginning
`sandbox: `, and nothing is called. A `Create` error is returned unchanged. If
`Sync` or `Tools` fails, `Open` calls `Close` once on the sandbox with
`context.WithoutCancel(ctx)`, so an expired or cancelled `ctx`, the commonest
cause of the failure, does not stop the cleanup, and returns no sandbox, no
tools and the step's error unchanged: `errors.Is` still matches
`sandbox.ErrUnsupported`, `sandbox.ErrToolMismatch`, `sandbox.ErrUnsafeArchive`
and whatever else the step's error matches. When `Close` fails too, the error
is `errors.Join(stepErr, closeErr)`, which matches both. On `local`, a failed
`Open` leaves no directory under `agentkit-sandbox-local`: a `GitRepo` source
fails with `ErrUnsupported`, and a relative `ToolOptions.SpillDir` with
`sandbox: agent refused Tools: …`.

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
| `Ignore workspace.IgnoreOptions` | The host's ignore environment. The zero value reads the real environment, global excludes included, as `workspace.Options.Ignore` does; `workspace.NoGlobalExcludes()` is hermetic. The agent always uses `workspace.NoGlobalExcludes()`. | The same, for the host's rules over the destination. |
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

- ignored entries: what `workspace.Walk` with hidden files does not visit, at
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
| `--entrypoint /usr/local/bin/agentkit-sandboxd <image> <sandbox.AgentArgs…>` | Always the agent at `sandbox.AgentPath`, whatever the image's own entrypoint. |

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
the image and gives the fix: build the image `FROM` the base image of this
build's version (`sandbox.DefaultImage()`), which has the agent there.

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

**Testing it.** `make sandbox-podman-test` builds the image (`make
build-containers`) and runs the provider's integration tests in
`sandbox/podman` with `AGENTKIT_SANDBOX_BASE_IMAGE`,
`AGENTKIT_SANDBOX_IMAGE_VERSION` and `AGENTKIT_CONTAINER` set; it is not part of `make check`. Without an engine,
without `AGENTKIT_SANDBOX_BASE_IMAGE` or under `-short`, those tests skip.
The `GitRepo` test clones `AGENTKIT_SANDBOX_GIT_URL` (default
`https://github.com/agent-fox-dev/agentkit-go`) at depth 1, and the
micro-VM test runs only with `AGENTKIT_SANDBOX_RUNTIME=krun`.

### The Landlock provider (`sandbox/landlock`)

A confined child process on Linux, for hosts whose kernel offers Landlock
ABI 4 (Linux 6.7) or later. The agent runs as a child of the embedder, over
pipes, and its own Landlock ruleset is the only boundary: it can write only
its workspace, `home/` and `tmp/`, read and execute the system directories,
and, under `NetworkNone`, open no TCP connection. It needs no container
engine, no image and no privileges. It is a confinement, not an isolation
(`IsolationConfinement`): the host kernel, memory, CPU and process table are
shared, there are no resource limits, and UDP, Multipath TCP, Unix sockets
(a D-Bus session bus, an ssh-agent, a podman or docker socket) and signals
to the user's processes stay open. Use `sandbox/podman` for untrusted code.

```go
p, err := landlock.New(landlock.Options{})
if errors.Is(err, landlock.ErrNoLandlock) {
	// this host cannot confine: fall back to sandbox/podman
}
sb, err := p.Create(ctx, sandbox.Spec{}) // NetworkNone
```

| `landlock.Options` field | Meaning |
|---|---|
| `Root` | Holds one directory per sandbox. Empty means `<os.UserCacheDir()>/agentkit-sandbox-landlock`. It is made absolute, created with mode 0700 and resolved through symlinks once. Choose it outside the system directories. |
| `AgentBinary` | The `agentkit-sandboxd` to run. Empty means the file beside `os.Executable()` (symlinks resolved), then the one on `PATH`. |
| `ExtraRO` | Absolute directories the agent and its tools may read and execute beneath (`--landlock-ro`). |
| `ExtraRW` | Absolute directories they may also write beneath (`--landlock-rw`). |

**`landlock.New(o)`** checks, in order, stopping at the first failure:

1. The platform: any `GOOS` but `linux` fails with
   `landlock: this host cannot confine a sandbox with Landlock ABI 4: GOOS darwin has no Landlock; use sandbox/podman`.
2. The kernel, probed once with the Landlock version query (syscall 444).
   A probe errno is named with a hint (`ENOSYS`: the kernel has no Landlock;
   `EOPNOTSUPP`: Landlock is not enabled in `lsm=`; `EPERM`: the call is
   blocked, typically by a container's seccomp filter); an ABI below 4 reads
   `kernel Landlock ABI N; this provider needs ABI 4 (Linux 6.7) or later; use sandbox/podman`.
3. `Root`, as above; a failure names `Options.Root`.
4. Each `ExtraRO` and `ExtraRW` entry must be absolute, not `/`, and must
   neither equal, contain nor lie beneath `Root` (one sandbox could reach
   another); a refusal names `Options.ExtraRO[i]` or `Options.ExtraRW[i]`.
5. The agent binary, resolved as above, must be a regular executable file,
   and `<binary> --version`, run once with an empty environment and a
   10-second timeout, must report protocol 1.2 or later (the first to report
   Landlock). A failure lists the paths tried or quotes the output, with the
   install command.

Only steps 1 and 2 wrap `landlock.ErrNoLandlock`
(`landlock: this host cannot confine a sandbox with Landlock ABI 4`), as does
an agent whose report falls short at `Create`; a misconfiguration does not.
The version check is the only process `New` starts.

**Installing the agent.** There are no prebuilt host binaries:

```bash
go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@vX.Y.Z   # the release your module uses
cd sandbox && go install ./cmd/agentkit-sandboxd                                       # from a checkout, for a (devel) build
```

**The environment** is exactly `Spec.Env`, plus `HOME=<Root>/<id>/home` and
`TMPDIR=<Root>/<id>/tmp`, plus
`PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin` when
`Spec.Env` sets no `PATH`; nothing comes from the embedder's environment.
`HOME` or `TMPDIR` in `Spec.Env` is refused with a `*sandbox.SpecError` on
`Env`, because those two variables are the ruleset's writable roots.

**The sandbox directory** is `<Root>/<id>/` (mode 0700, `id` 16 lowercase
hex characters) holding `workspace/`, `home/` and `tmp/` (each 0700) and the
marker `agentkit-sandbox.json` (0600). The marker is JSON with `pid`,
`start_time` (`/proc/<pid>/stat` field 22), `boot_id`, `role` (`creator`,
then `agent` once the agent has started, replaced atomically), `created`,
`ttl`, `network` and `labels`; it never holds `Spec.Env`. Neither the marker
nor `<Root>/<id>/` itself is inside the ruleset, so nothing in the sandbox
can read it. `Spec().Workdir` is the host path of the workspace, and tool
results name host paths.

**The agent** gets `--ttl`, `--network`, `--workdir <Root>/<id>/workspace`,
then `--landlock-ro` for each `ExtraRO`, then, under `NetworkUnrestricted`
only, the resolver directory, then `--landlock-rw` for each `ExtraRW`. The
resolver directory is the directory of the file `/etc/resolv.conf` resolves
to when it lies beneath `/run` or `/var/run` but is neither of them, such as
systemd-resolved's `/run/systemd/resolve`; without it DNS does not work on
such hosts. The agent runs as the leader of its own session, in the
workspace. After `Hello`, whatever `RequireLandlock` says, its report must
show ABI 4 or later, TCP under `NetworkNone`, and the sandbox's `workspace/`,
`home/` and `tmp/` among `Writable`, and no other reported root may contain
`Root`; otherwise `Create` closes the sandbox and fails. An agent that exits
before `Hello` is reported as
`landlock: agentkit-sandboxd exited with status N: <stderr tail>` (status 2
is a refused flag value, such as an `ExtraRW` containing a system directory).

**`Close`** (also at the TTL, and after any failure once the agent has
started) waits up to 5 seconds for the agent to exit, then kills its process
group; kills every process left in the agent's session, rescanning for up to
5 seconds; and removes `<Root>/<id>/` without following symlinks, restoring
owner permissions on read-only directories first. A process a tool started
with `setsid` escapes the session kill.

**`GitRepo`** is cloned by a second, confined agent with
`--network unrestricted`, the same workspace and roots plus the resolver
directory, and only `GIT_CONFIG_NOSYSTEM=1`, `GIT_TERMINAL_PROMPT=0`, the
sandbox's `HOME` and `TMPDIR` and its agent's `PATH` in its environment; it
runs `git` through its `run_command` tool, bounded by the sandbox's expiry,
while the sandbox's own agent waits under its stricter ruleset. The slot
rules of the podman provider apply, with the messages
`landlock: GitRepo must be the first Sync and come before Tools` and
`landlock: git is not on the sandbox's PATH (<PATH>); a GitRepo needs it`.
git must lie beneath a system directory or an `ExtraRO` directory. A failed
clone empties the workspace and leaves the slot free.

**`Reap`** considers only directories under `Root` named by 16 lowercase hex
characters with a readable marker. Their owner is alive when its pid exists
with the recorded start time on the recorded boot; such a sandbox is kept,
whoever created it. Otherwise `Reap` kills what is left of a dead agent's
session (same boot, pid gone) and removes the directory, and returns the
removed ids, sorted.

**Testing it.** The offline tests run everywhere in `make check`; the Linux
tests run there too on a kernel with Landlock ABI 4 and skip, naming the ABI,
below it. `make sandbox-landlock-test` runs `go test -count=1 -v ./landlock/`
in `sandbox/` with `AGENTKIT_SANDBOX_GIT_URL` (default
`https://github.com/agent-fox-dev/agentkit-go`), which turns on the real
public clone; it is not part of `make check`.

### The gateway (`sandbox/gateway`, `sandbox/gatewayd`)

A gateway runs sandboxes on another host. The client, `sandbox/gateway`,
returns a `sandbox.Provider` whose sandboxes behave like local ones. The
server, `sandbox/gatewayd` (a separate module), holds one backend provider;
[`cli.md`](cli.md#agentkit-gateway) covers its command.

**`gateway.Dial(ctx, rawURL, gateway.Options)`** checks the URL and the token,
connects, and calls the gateway's `Capabilities` under `ctx`.

| URL | Transport |
|---|---|
| `https://host[:port]` | TLS; port 443 by default |
| `http://host[:port]` | plaintext; port 80 by default; only when the host is `localhost` or a loopback IP literal |

A URL with user information, a query, a fragment, a path other than empty or
`/`, or any other scheme is refused, naming the problem, before any
connection. A refused token returns an error matching
`gateway.ErrUnauthenticated`; a gateway of another protocol major one
wrapping `sandbox.ErrProtocol` that names both versions; any other failure
an error naming the URL.

| `gateway.Options` field | Meaning | Default |
|---|---|---|
| `Token` | The bearer token: non-empty printable ASCII without whitespace, sent on every RPC as `authorization: Bearer <token>`; for `https`, only over TLS. | required |
| `TLSConfig` | Cloned for `https`; an empty `ServerName` becomes the URL's host. The caller's value is never changed. | the system roots, TLS 1.2 at least |
| `Dialer` | Replaces TCP; it is given `host:port`. The URL rules still apply. | `net.Dialer` |

Every message is bounded by `sandbox.MaxMessageBytes` (16 MiB).

| `*gateway.Provider` | Does |
|---|---|
| `Capabilities()` | The answer cached at `Dial`: `Provider` is `gateway/<backend>`, `Network` the backend's modes within the ceiling, `Isolation`, `Resources`, `Image`, `Workdir` and `Landlock` the backend's, and `Notes` the backend's notes followed by a sentence naming the gateway's host, `MaxTTL` and `MaxSandboxes`. |
| `Gateway()` | `GatewayInfo{URL, Version, Backend, ProtocolMajor, ProtocolMinor, MaxTTL, MaxSandboxes}` as reported at `Dial`. |
| `Create(ctx, spec)` | Checks the spec first, with no RPC: `CheckSpec`, the gateway's `MaxTTL` (a `*SpecError` naming `TTL`) and that `Spec.Provider` encodes as JSON (naming `Provider`). An empty `Image` becomes the client's `sandbox.DefaultImage()` when the backend honours `Image`, whose `ErrNoPublishedImage` is returned unchanged. A gateway refusal keeps its type: a `*SpecError` naming `gateway/<backend>` and the field, or an error matching `ErrUnsupported`, `ErrNoAgent`, `ErrNoPublishedImage`, `ErrProtocol` or `gateway.ErrCapacity`. |
| `Reap(ctx)` | Runs the backend's `Reap` on the gateway; the removed ids, sorted, and an error carrying the gateway's text when a removal failed. For `podman` the rule is "expired". |
| `List(ctx)` | The caller's sandboxes as `Info{ID, Labels, Created, Expires}`, by creation time and then id. |
| `Destroy(ctx, id)` | Destroys the caller's sandbox `id`; an unknown id returns nil. |
| `Close()` | Closes every sandbox it created that is still open, which destroys each on the gateway, then the connection; the joined errors. Later `Create`, `Reap`, `List` and `Destroy` return `gateway.ErrClosed`. |

A gateway sandbox's `Sync(GitRepo)` checks that the sandbox is live and the
repository valid before any RPC, then runs the backend's init step with
`SyncGit`. The slot rules are those of `sandbox.GitRepo`, kept by the
gateway. Its `Close` destroys it on the gateway with a fresh 30-second
context; a failed destroy is returned, and the sandbox then lives on the
gateway until its expiry. Calls fail with `sandbox_unavailable` when the
connection to the gateway, or the gateway's connection to the agent, breaks
mid-call, and when the gateway destroys the sandbox (its expiry, another
client of the same principal, the embedder's `Server.Destroy`, a shutdown).
Nothing reconnects.

**`gatewayd.New(gatewayd.Config)`** checks the configuration and returns a
server; `Serve(net.Listener)` serves it once, and `Shutdown(ctx)` stops
accepting RPCs, destroys every sandbox it holds and waits for the handlers
until `ctx` is done.

| `gatewayd.Config` field | Meaning | Default |
|---|---|---|
| `Backend` | The one provider that creates the sandboxes. | required |
| `Tokens` | The accepted tokens (static mode): at least one, each at least 32 characters of printable ASCII without whitespace, none repeated. Only their SHA-256 sums are kept. | exactly one of `Tokens` and `Authenticate` |
| `Authenticate` | An `Authenticator`, `func(ctx context.Context, token string) (principal string, ok bool)` (callback mode): it decides which tokens are accepted and names the principal that owns each one's sandboxes. | exactly one of `Tokens` and `Authenticate` |
| `TLS` | Serve TLS with this config; nil serves plaintext. | nil |
| `NetworkModes` | The network ceiling: the modes clients may request, intersected with the backend's; an empty intersection is refused, naming both lists. | `[NetworkNone]` |
| `MaxSandboxes` | The most sandboxes held at once, counting creates in progress; beyond it `Create` is refused with `CAPACITY`. | 16 |
| `MaxTTL` | The longest TTL accepted; a zero TTL counts as 1h. | 24h |
| `ReapInterval` | How often `Backend.Reap` runs; it also runs once when `Serve` starts. A failure is logged, never fatal. | 10m |
| `Logger` | Receives one line per create, destroy, refused authentication (with its peer address and a `reason`) and backend reap; never a token, an `Env` value, a tool input or a tool result. | discards |

Negative limits are refused. The ceiling defaults to `none` because a token
holder on a shared host should not get unrestricted egress unless the
operator chose it; the command's `--network` sets it. The server checks every
request again, whatever the client checked. It is the trust boundary, and
a client may be older, newer or not AgentKit at all.

**Authentication modes.** `New` refuses a `Config` that sets both `Tokens` and
`Authenticate` and one that sets neither, with an error that names both fields
and holds no token (`gatewayd: Config.Tokens and Config.Authenticate are both
set: set exactly one`, `gatewayd: neither Config.Tokens nor
Config.Authenticate is set: set exactly one`). Everything else is validated as
before; `Tokens` is validated only in static mode.

- *Static mode* (`Tokens`): each RPC's token is hashed and compared with every
  configured hash in constant time, in gRPC's tap handle and again in the
  interceptors.
- *Callback mode* (`Authenticate`): the tap handle calls the function once for
  each unary RPC and each stream, with a context that carries the peer address
  and the token exactly as presented; the server applies no length or
  character rule to it. A missing, repeated, non-`Bearer` or empty
  `authorization` header is refused without calling it. `ok == false`, an empty
  principal, a principal over 256 bytes and a panic (recovered; its value is
  never logged) all refuse the RPC with `Unauthenticated` before gRPC reads a
  message, also for a method no handler serves. The interceptors do not call it
  again; they check that the context carries a principal. The function must be
  safe for concurrent use and return quickly without blocking: a slow one
  stalls every stream on its connection, and the server enforces no timeout.
  The server trusts it to compare tokens in constant time.

The refused-authentication line keeps its message and `peer` and has a `reason`:
`no token` (a missing, repeated, non-`Bearer` or empty header), `refused` (no
hash matches, the function said no, or an interceptor found no principal),
`invalid principal` or `panic`.

**Principals.** An authenticated RPC has a *principal*: what the
`Authenticator` returned in callback mode, the 64-character lowercase hex
SHA-256 of the token in static mode. A sandbox belongs to the principal of the
`Create` that made it. Another token of the same principal lists, relays to,
syncs and destroys it; a token of another principal gets the answers for an id
that does not exist (`List` omits it, `Destroy` answers OK and does nothing,
the relay and `SyncGit` answer `Unavailable` naming the id). Revoking a token
refuses its later RPCs; RPCs in flight run to their end, and the principal's
sandboxes stay until their TTL, a `Destroy`, `Server.Destroy` or `Shutdown`.
The principal must not be a secret: a callback-mode `sandbox created` line
carries it whole as `owner`, a static-mode line only its first 8 characters,
and the backend receives it.

**`gatewayd.PrincipalFromContext(ctx)`** returns the principal to a `Backend`
that is serving an authenticated RPC: in `Create`, in the `Sync` of a sandbox
that a `SyncGit` RPC syncs, and in `Reap` when the `Gateway.Reap` RPC calls it.
It reports `ok == false` on the periodic reap, on every `Close` the server
makes on its own (a client `Destroy`, expiry, unavailability, `Shutdown`, the
error paths of `Create`) and on any context the server did not make. Nothing
exported puts a principal on a context.

**`(*Server).Destroy(ctx, id)`** ends a sandbox the server holds, whoever
created it, without a client token. It removes the entry, stops its timer,
calls the backend sandbox's `Close` with `ctx` (which ends the relayed calls in
flight) and logs `sandbox destroyed` with the reason `embedder`; the client
then sees what it sees at expiry (`sandbox_unavailable` on its next proxy
call, the id gone from `List`). It returns the `Close` error wrapped with the
id, and the entry stays removed if `Close` fails or `ctx` is cancelled. For an
id the server does not hold (unknown, already removed by any path, a `Create`
still in progress, before `Serve`, after `Shutdown`) it returns an error that
matches `gatewayd.ErrNotHeld`, which an embedder wanting idempotence treats as
"already gone". The `agentkit-gateway` command always runs in static mode.

**Testing it.** The gateway's offline tests run in `make check`: an
in-process server over `local` with a client in the same process. `make
sandbox-gateway-test` builds the image and runs the opt-in Podman tests in
`sandbox/gatewayd` with `AGENTKIT_SANDBOX_BASE_IMAGE` and
`AGENTKIT_CONTAINER` set. They skip without an engine, without
`AGENTKIT_SANDBOX_BASE_IMAGE` or under `-short`. The `GitRepo` test clones
`AGENTKIT_SANDBOX_GIT_URL` (default
`https://github.com/agent-fox-dev/agentkit-go`).

### Sandbox images

The agent is published in one image, with the agent at `sandbox.AgentPath`
(`/usr/local/bin/agentkit-sandboxd`), owned by root with mode 0755, and
`ENTRYPOINT` set to it. A provider must not rely on that entrypoint: an
embedder's image built `FROM` it may set its own, so providers start the
agent with `--entrypoint sandbox.AgentPath` and `sandbox.AgentArgs(spec)`.

| Identifier | Value or behaviour |
|---|---|
| `sandbox.BaseImageRepo` | `quay.io/agentfox/agentkit-sandbox` |
| `sandbox.ImageTag(version)` | The tag of a release version: `version` without its `v`. Only `vMAJOR.MINOR.PATCH` with no leading zeros and an optional `-prerelease` is accepted; `(devel)`, build metadata (`+dirty`, `+incompatible`) and Go pseudo-versions give `("", false)`. |
| `sandbox.DefaultImage()` | `BaseImageRepo:<tag>` for the build's version (`buildver`: the link-time stamp, else the sandbox module's version, else `(devel)`). |
| `sandbox.ErrNoPublishedImage` | `sandbox: no published image for this agentkit-go version`. `DefaultImage` wraps it when the version has no published image; the message names the version and says to set `Spec.Image` or build an image with `make build-containers`. |

The default is the exact `X.Y.Z` tag, never `X.Y`: the `Tools` digest check
fails when the host and the agent are at different versions.

**The image** (`containers/agentkit-sandbox/Containerfile`):

| Content | Value |
|---|---|
| Base | `registry.redhat.io/ubi10/s2i-core` |
| Packages | `git-core`, `bash`, `ca-certificates`; nothing else (the agent carries rsync itself; zombie reaping is the provider's `--init`) |
| User | uid 1001, gid 0, home `/home/agentkit` (`ENV HOME`); `USER 1001:0` |
| Workspace | `/workspace`, owned 1001:0, mode 0775, `WORKDIR`; not a `VOLUME` |
| git | `safe.directory = *` in the system config |
| Agent | cgo-built (tree-sitter outlines), at `sandbox.AgentPath` |
| Environment | `HOME`, beyond the base image's `PATH` |
| Labels | `org.opencontainers.image.source` (`https://github.com/agent-fox-dev/agentkit-go`), `licenses` (`Apache-2.0`), `title`, `description`, `version`, `revision` |

**Tags.** A release `vX.Y.Z` publishes `X.Y.Z`, plus a moving `X.Y` when there
is no prerelease part, for linux/amd64 and linux/arm64; there is no
`latest`. A re-run rebuilds a tag, so an embedder who needs an immutable
image pins its digest. [`releasing.md`](releasing.md) has the details.

**Make variables** for `make build-containers`, each overridable on the
command line:

| Variable | Default |
|---|---|
| `CONTAINER_REGISTRY` | `quay.io/agentfox` |
| `SANDBOX_IMAGE` | `agentkit-sandbox` |
| `SANDBOX_IMAGE_TAG` | `VERSION`; the image is also tagged `latest` |
| `VERSION` | `git describe --tags`, or `0.1.0` outside a tagged checkout |

The build stamps `VERSION`, the short commit and the UTC build time into
the agent (`-ldflags -X …/sandbox/internal/buildver.stamp`, `.commit` and
`.buildTime`); `agentkit-sandboxd --version` prints
`agentkit-sandboxd <version> (protocol M.N) commit <commit> built <time>`,
leaving out the commit and build time when a build stamps none.

The image tests in `sandbox/images` run against the image named by
`AGENTKIT_SANDBOX_BASE_IMAGE`, with `AGENTKIT_SANDBOX_IMAGE_VERSION` and
`AGENTKIT_CONTAINER` optional; without that variable, a container engine, or
under `-short`, they skip.

## Limits on untrusted input

`wire.Defaults()`: 16 MiB per message, 1,000,000 elements per container,
depth 64, 2,000,000 nodes. The MCP client holds every inbound stdio frame,
JSON response body and SSE event to `ConnectionOptions.Limits` (zero fields
take these defaults) and rejects duplicate keys before the SDK decodes it. The
Anthropic provider checks each stream event and response body the same way,
with the defaults. A sandbox proxy guards the `Data` and `Metadata` of every agent answer the
same way, and every sandbox protocol message is bounded by
`sandbox.MaxMessageBytes`, also 16 MiB. `mcpserve` holds every inbound stdio
frame and every authorized HTTP body to `wire.Defaults()` too (413 above the
size, 400 for a body `wire.Guard` rejects), with no option.

## Compatibility

Go 1.27 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64 with cgo off; the host is also
built with cgo on. cgo code lives only in `//go:build cgo` files, each with a
pure-Go fallback.
