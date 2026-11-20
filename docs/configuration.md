# Configuration

The reference for every option, environment variable, default and limit in
AgentKit. For an introduction with examples, read the [user guide](guide.md)
first; this file is for looking things up.

AgentKit has no configuration file of its own and no global state. It is
configured by:

1. fields on `agentkit.Config`, `agentkit.Toolset` and the option structs of
   each package,
2. environment variables, read only by the provider an agent runs on (and
   nothing else in the library),
3. an optional `[mcp]` TOML section, which an application may load with
   `mcp.ParseConfig`.

## Contents

- [`agentkit.Config`](#agentkitconfig)
  - [`agentkit.Resolve`, `Resolution` and `Agent.Resolution`](#agentkitresolve-resolution-and-agentresolution)
  - [`agentkit.PruneOptions`](#agentkitpruneoptions)
  - [Run behaviour](#run-behaviour)
- [`agentkit.Toolset`](#agentkittoolset)
  - [`Events`](#events)
- [`guard.Options`](#guardoptions)
- [Credentials and deployments](#credentials-and-deployments)
  - [Other variables](#other-variables)
  - [Live provider tests](#live-provider-tests)
- [`anthropic.Options`](#anthropicoptions)
- [`openrouter.Options`](#openrouteroptions)
- [`ollama.Options`](#ollamaoptions)
- [Model catalog](#model-catalog)
  - [Anthropic rows](#anthropic-rows)
  - [OpenRouter and Ollama rows](#openrouter-and-ollama-rows)
  - [Ids the catalog does not list](#ids-the-catalog-does-not-list)
  - [Tiers](#tiers)
  - [`catalog.Select`](#catalogselect)
- [Workspace (`workspace.Options`)](#workspace-workspaceoptions)
- [Built-in tools (`tools.Options`)](#built-in-tools-toolsoptions)
  - [`code_search` (when a `codesearch` index is set)](#code_search-when-a-codesearch-index-is-set)
- [Subprocess runner (`tools.Run`, `tools.RunArgv`)](#subprocess-runner-toolsrun-toolsrunargv)
- [Code mode (`codemode.Options`)](#code-mode-codemodeoptions)
- [MCP (`mcp.Config`, `mcp.ServerConfig`)](#mcp-mcpconfig-mcpserverconfig)
- [MCP serving (`mcpserve.Options`)](#mcp-serving-mcpserveoptions)
- [Limits on untrusted input](#limits-on-untrusted-input)
- [Compatibility](#compatibility)

## Environment variables at a glance

| Variables | Read by | Section |
|---|---|---|
| `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN`, `ANTHROPIC_BASE_URL` | the `anthropic` provider | [Credentials and deployments](#credentials-and-deployments) |
| `CLAUDE_CODE_USE_VERTEX`, `ANTHROPIC_VERTEX_*`, `GOOGLE_CLOUD_*`, `CLOUD_ML_REGION`, `CLOUDSDK_COMPUTE_REGION` | the `anthropic` provider, on Vertex AI | [Credentials and deployments](#credentials-and-deployments) |
| `CLAUDE_CODE_USE_BEDROCK`, `AWS_*` | the `anthropic` provider, on Bedrock | [Credentials and deployments](#credentials-and-deployments) |
| `OPENROUTER_API_KEY`, `OPENROUTER_BASE_URL` | the `openrouter` provider | [`openrouter.Options`](#openrouteroptions) |
| `OLLAMA_HOST` (and `OLLAMA_AUTH`, read by Ollama's own package) | the `ollama` provider | [`ollama.Options`](#ollamaoptions) |
| `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL`, `AGENTKIT_EFFORT`, `AGENTKIT_LIVE` | the examples and the live tests only | [Other variables](#other-variables) |
| `AGENTKIT_INDEX_DIR` | the `agentkit-tools` command only (the library reads no `AGENTKIT_` variable) | [cli.md](cli.md#flags) |
| `AGENTKIT_BASH_DIFF` | opt-in tests only | [development.md](development.md#tests) |

## `agentkit.Config`

`agentkit.New(cfg)` builds an `Agent` from it. It runs
[`agentkit.Resolve`](#agentkitresolve-resolution-and-agentresolution) on the
fields that choose a model, then validates the `Toolset` with
`agentkit.NewRunner` (below), and returns the first error unchanged, in this
order: the provider, the model, the effort, the credentials, the tool set.
Nothing has been sent when it fails, and a missing key is found here, not on
the first request.

| Field | Meaning / default |
|---|---|
| `Client` | A `core.ProviderClient`: a test double such as `provider/faux`, or a provider the embedder built or wrapped. When it is set the agent calls it, builds none and reads no variable; `Provider`, `Model` and `Effort` still choose the catalog row and the effort. Nil means `New` builds the client for `Provider`. |
| `Provider` | `anthropic`, `openrouter` or `ollama`, trimmed and without regard to case; empty means `anthropic`. It names the catalog provider even when `Client` is set. Another name fails with `catalog.ErrUnknownProvider`. |
| `Model` | A tier (`SIMPLE`, `STANDARD` or `ADVANCED`) or the provider's own model id, as [`catalog.Select`](#catalogselect) reads it. It supplies the context window, output cap, prices and thinking kind; an id the catalog does not list gets the provider's default row (below). A tier is the model id it names everywhere the agent names its model: the start event, the messages it stamps and the context-window error. Empty fails with `catalog.ErrNoModel`. |
| `Effort` | `agentkit.Effort` (`core.Effort`): `""` (the tier's effort when `Model` is a tier, else none), normalized, so `"HIGH"` is sent as `high`; every request carries the resolved effort. An effort outside the five fails with `catalog.ErrUnknownEffort`. On Anthropic: `""` (no thinking parameter), `EffortLow`, `EffortMedium`, `EffortHigh`, `EffortXHigh`, `EffortMax` (`low` … `max`). On an adaptive model it is sent as `thinking: {"type":"adaptive"}` with `output_config.effort`; on a budget model as `thinking: {"type":"enabled","budget_tokens":N}`, N from the catalog row; on a model without thinking, or at an effort the row does not list, neither is sent. Never moved to another effort. |
| `Env`, `Getenv` | The variables the chosen provider reads for its credential and endpoint. A non-empty `Env` value is used first; an empty one falls through to `Getenv`, and when `Getenv` is set it is the only other source (a test with `Getenv` set never reads the process environment); with no `Getenv`, the process environment. An empty value counts as unset. Each provider reads only its own variables (below). |
| `System` | The base prompt. Empty means `prompt.BaseInstructions` and `prompt.UniversalGuidelines`. Either way the active tools' own guidelines (`Tool.PromptGuidelines`, and the shell guidelines) follow it (`prompt.Build`). |
| `Prefix` | Messages sent after the system prompt and before the transcript on every request (`core.Request.Prefix`), with a cache breakpoint on their last block. Never recorded in the transcript. |
| `Toolset` | The agent's tools, the policy that selects among them, the guard and the after-interceptor ([`agentkit.Toolset`](#agentkittoolset), below). The zero `Toolset` is an agent with no tools. |
| `MaxTurns` | Ends a run that would go on past this many turns with `core.RunStopMaxTurns` and an error wrapping `core.ErrMaxTurns`, after the last turn's results are recorded. Zero is unbounded. |
| `MaxCostUSD` | Checked before each request against the run's cost so far: at or past it, the request is not sent and the run ends with `core.RunStopBudgetExceeded` and an error wrapping `core.ErrBudgetExceeded`. Usage a provider did not price is priced at the model's catalog row, so an unlisted id costs nothing and never trips it; `Usage.Unpriced` says when that happened ([Run behaviour](#run-behaviour)). Zero is unbounded. |
| `Timeout` | The run's deadline. Past it the run ends with `core.RunStopTimeout` and an error wrapping `context.DeadlineExceeded`. A cancellation from outside ends it with `core.RunStopAborted` and an error wrapping `core.ErrAborted` and the context's error. Zero is unbounded. |
| `Prune` | `agentkit.PruneOptions` (below); the zero value is off. |
| `MaxTokens` | Output cap per response, capped at the model's output cap. Zero means `agentkit.DefaultMaxTokens`. |

`agentkit.DefaultMaxTokens` is `32768`: the `max_tokens` sent when
`Config.MaxTokens` is zero, still capped at the model's own output cap. A
caller who wants the whole cap sets it.

### `agentkit.Resolve`, `Resolution` and `Agent.Resolution`

`agentkit.Resolve(cfg Config) (Resolution, error)` reads `Client`, `Provider`,
`Model`, `Effort`, `Env` and `Getenv` and nothing else, and returns what `New`
would run, so an embedder can see, check or wrap it without building an agent:

| `Resolution` field | Meaning |
|---|---|
| `Provider` | The canonical provider name. |
| `Tier` | `catalog.Tier`; empty when a model id was named. |
| `Model` | `core.Model`: the catalog row, or the provider's fallback row for an id the catalog does not list. `Model.ID` is what the vendor receives. |
| `Effort` | The effort requested, normalized: the explicit one, else the tier's, else empty (none is sent). |
| `Listed` | True when `Model` is a catalog row and not a fallback. |
| `Client` | The client a run calls: `Config.Client` when it was set, else the one built for `Provider`. |

It calls `catalog.Select(cfg.Provider, cfg.Model, cfg.Effort)` and returns its
errors unchanged, `Config.Client` set or not, so `errors.Is` matches
`catalog.ErrUnknownProvider`, `catalog.ErrNoModel` and `catalog.ErrUnknownEffort`
and the message names the value and the valid choices. With `Client` nil it
then builds the client of the chosen provider and returns the constructor's
error unchanged:

- `anthropic`: it runs `anthropic.Resolve` over a lookup that applies the order
  above, so a missing credential (`anthropic.ErrNoCredentials`) or a Vertex
  selection without a project fails now, with that function's error; it then
  builds `anthropic.Provider(model, anthropic.Options{Env: cfg.Env, Getenv:
  cfg.Getenv})`, which resolves the deployment on every request, as an
  embedder's own provider does.
- `openrouter`: `openrouter.New(model, openrouter.Options{Env: cfg.Env, Getenv:
  cfg.Getenv})`: `openrouter.ErrNoAPIKey` when `OPENROUTER_API_KEY` is unset,
  and an error for an `OPENROUTER_BASE_URL` that is not an absolute http or
  https URL.
- `ollama`: `ollama.New(model, ollama.Options{Env: cfg.Env, Getenv:
  cfg.Getenv})`. It needs no credential; `ollama.ErrInvalidHost` for a host it
  refuses.

With `Client` set, `Resolution.Client` is that client and no variable is read.
`Resolve` makes no request and keeps no state: each call builds a new client,
and a host that cannot be reached is found by the first request, not by
`Resolve` or `New`. To wrap the client the agent would build, resolve, wrap and
pass the wrapper back; `New` then reads no variable:

```go
res, err := agentkit.Resolve(cfg)
if err != nil {
	return err
}
cfg.Client = wrap(res.Client)
agent, err := agentkit.New(cfg)
```

`(*Agent).Resolution() Resolution` returns the resolution the agent was built
with. It is a copy: changing its `Model.Efforts` map or `Model.Compat` bytes
does not change the agent, and it is safe to call from any goroutine during a
run. Each run starts with a `core.AgentStartEvent` whose `Provider` is the
resolved name, `Model` the resolved id (never a tier) and `API` the API constant
of the provider package `Resolve` built the client from (`anthropic-messages`,
`openrouter-chat` or `ollama-chat`), empty when `Config.Client` was given.

**Each provider reads only its own variables.** Every lookup goes through the
chosen provider's own code and only that provider's code runs; a value read for
one provider is never passed to another's client. `anthropic` asks for the
variables under [Credentials and deployments](#credentials-and-deployments)
(`ANTHROPIC_*`, `CLAUDE_CODE_*`, `GOOGLE_CLOUD_*`, `CLOUD_ML_REGION`,
`CLOUDSDK_*`, `AWS_*`), `openrouter` for `OPENROUTER_API_KEY` and
`OPENROUTER_BASE_URL`, and `ollama` for `OLLAMA_HOST` (the Ollama package itself
also reads `OLLAMA_AUTH` from the process environment, as
[`ollama.Options`](#ollamaoptions) says). `OLLAMA_API_KEY` is read by none of
them and sent to no server, and the library reads no `AGENTKIT_` variable:
`AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and `AGENTKIT_EFFORT` are the examples' own
(see [Other variables](#other-variables)). An agent's requests go only to
the chosen provider's host, so an Anthropic key is never sent to OpenRouter or
Ollama, an OpenRouter key never to Anthropic or Ollama.

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
- `core.Usage.Unpriced` is true on `RunResult.Usage`, `AgentDoneEvent.Result.Usage`,
  `AgentDoneEvent.Usage` and `Agent.Usage()` when at least one turn's cost counted
  as zero because no one could price it: the usage was reported, the resolved row
  has no price, and the provider is `anthropic` (which reports no cost of its own)
  or `openrouter` and the response carried no cost. The agent never sets it on
  `ollama`, where zero is the price, whichever client is called. The turn is still
  priced at the computed zero with the cost bit set, `MaxCostUSD` still reads
  `CostUSD`, and `TurnEndEvent.Usage` stays the provider's own usage of the turn.
  `Usage.Add` ORs the flag, and every encoded usage in the event JSON carries it as
  `unpriced`.
- A turn that ends in an error ends the run with `core.RunStopError`. The
  run's error (`Agent.Run`'s, `RunResult.Error`, the `AgentDoneEvent`'s) has the
  turn's `ErrorMessage` as its text and wraps the error the provider ended its
  stream with, so `errors.Is` and `errors.As` find the provider's own sentinels
  and types through it (`ollama.ErrModelNotFound`, `*openrouter.StatusError`). A
  provider that marked the turn failed without giving an error leaves a plain
  error with the same text.
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

A sandbox is the boundary the guard never claimed to be, and AgentKit does
not provide one: isolation belongs to the platform that runs the process.
`guard.AllowAll`, the interceptor that never blocks, is a reasonable choice
only for a process that already runs inside an isolation boundary someone
else owns, with its network and the files that leave it treated
accordingly; never on a developer's host, where the guard is the only
boundary. `agentkit.New` and `agentkit.NewRunner` still require a
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
`agentkit.New` also runs `anthropic.Resolve` once, over `Config.Env` and
`Config.Getenv` in that order, so an Anthropic agent with no credential fails at
construction; the provider it then builds is the one above, not the SDK client
that call returned.

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

### Other variables

Variables the library never reads, used by the examples and tests:

| Variable | Read by | Effect |
|---|---|---|
| `AGENTKIT_PROVIDER` | `codingagent`, `customtools` and `mcp` only | The provider: `anthropic`, `openrouter` or `ollama`, passed to `Config.Provider`. Empty is `anthropic`. The library never reads it. |
| `AGENTKIT_MODEL` | `codingagent`, `customtools` and `mcp` only | A tier or a model id, passed to `Config.Model`. Default `STANDARD`. The library never reads it. |
| `AGENTKIT_EFFORT` | `codingagent`, `customtools` and `mcp` only | The effort, passed to `Config.Effort`. Empty is the tier's effort. The library never reads it. |
| `AGENTKIT_LIVE` | the live provider tests only | `1` runs `TestLive` in `internal/providerconformance`; any other value, or none, skips it. The library never reads it. |

### Live provider tests

`make provider-live-test` (`AGENTKIT_LIVE=1`) runs one real, paid tool loop
on each provider whose credentials are present. What it needs and checks is
in [development.md](development.md#live-provider-tests).

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
| `NonStrictTools` | Names of tools never declared strict (`strict: true`), however well they fit. The API totals its strict-mode limits over a request (20 tools, 16 union parameters, and a compiled-grammar size it does not publish) and refuses the whole request past any. Name the simple tools, a file read, to leave the budget to the complex ones; a named tool spends none of it, and its arguments are still validated before it runs. See [ADR 03](adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md). |
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
agent, err := agentkit.New(agentkit.Config{Client: p, Provider: "openrouter", Model: sel.Model.ID, Effort: sel.Effort})
```

`agentkit.New(agentkit.Config{Provider: "openrouter", Model: "STANDARD"})` builds
this provider itself, from `OPENROUTER_API_KEY` and `OPENROUTER_BASE_URL` read
through `Config.Env` and `Config.Getenv` ([`agentkit.Config`](#agentkitconfig)).
Build it by hand, as above, to set `HTTPClient`, `MaxRetries`, `Now` or `Warnf`,
and pass it as `Config.Client`: `Provider` and `Model` then still choose the row
the agent prunes, prices and stamps by, and should name what `p` was built for.

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

## `ollama.Options`

`ollama.New(model, opts)` returns a `*ollama.Client`, a `core.ProviderClient`,
for one model served by an Ollama server, local or self-hosted
(`provider/ollama`). It reaches the server through Ollama's own Go package
(`github.com/ollama/ollama/api`) and its native `/api/chat` endpoint, and no
other: not Ollama's OpenAI-compatible or Anthropic-compatible ones. `model` is a
`core.Model`: the window, the output cap and the efforts come from it, and the
provider holds none of its own but the catalog's default window for a model that
has none. The row `catalog.Select("ollama", "STANDARD", "")` returns is the
usual one:

```go
sel, err := catalog.Select("ollama", "STANDARD", "")
if err != nil {
	return err
}
p, err := ollama.New(sel.Model, ollama.Options{}) // OLLAMA_HOST from the environment
if err != nil {
	return err // wraps ollama.ErrInvalidHost for a host it refuses
}
agent, err := agentkit.New(agentkit.Config{Client: p, Provider: "ollama", Model: sel.Model.ID, Effort: sel.Effort})
```

`agentkit.New(agentkit.Config{Provider: "ollama", Model: "STANDARD"})` builds
this provider itself, from `OLLAMA_HOST` read through `Config.Env` and
`Config.Getenv` ([`agentkit.Config`](#agentkitconfig)); it needs no credential.
Build it by hand, as above, to set `Host`, `HTTPClient`, `Now` or `Warnf`, and
pass it as `Config.Client`. The agent works with the resolved row's window (it
prunes tool results and refuses a request that cannot fit by it), which is the
window the provider sends to Ollama as `num_ctx`.

`Stream(ctx, req)` has the contract of `anthropic.Provider`'s: a context already
done is refused with its error, every later failure is the last item of the
channel, and it is safe for concurrent use. `(*Client).Host()` returns the
resolved base URL.

`New` makes no request. It fails when the model id is empty after trimming
(`ollama: the model id is empty`) and when the host is not one the provider
accepts (an error wrapping `ollama.ErrInvalidHost`, `ollama: invalid host: <why>
(<the value>)`, with any password in the value replaced by `xxxxx`).

| Variable | Effect |
|---|---|
| `OLLAMA_HOST` (`ollama.HostVar`) | The server's address; default `http://127.0.0.1:11434` (`ollama.DefaultHost`). |

The host is `Options.Host` when that is not empty after trimming, and then no
variable is read at all. Otherwise `OLLAMA_HOST` is read from `Options.Env`
first (an empty value falls through), then from `Options.Getenv`, or the process
environment when that is nil. The provider reads this one variable and no
other. The value is trimmed and read by the rules of Ollama's own CLI, so it
means the same server for `ollama` and for AgentKit:

| Value | Base URL |
|---|---|
| `example.local` | `http://example.local:11434` (no scheme: http, port 11434) |
| `http://h` | `http://h:80` (a scheme without a port: its default) |
| `https://h` | `https://h:443` |
| `http://h/proxy` | `http://h:80/proxy` (a path is kept) |
| `::1`, `[::1]:8080` | `http://[::1]:11434`, `http://[::1]:8080` |
| `:8080` | `http://127.0.0.1:8080` |

It is stricter than the CLI where the CLI guesses, and refuses with an error
wrapping `ErrInvalidHost`: a scheme other than exactly `http` or `https`; a port
that is not a number from 1 to 65535; a value that cannot be split into a host
and a port, or has a query or a fragment; user information (`user:pw@h`);
`ollama.com` and any subdomain of it, which is Ollama's hosted service and is not
supported. [ADR 03](adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md) lists each rule.

| Field | Meaning |
|---|---|
| `Host` | The server's address; overrides `OLLAMA_HOST`. |
| `Env` | `map[string]string` holding an explicit value for `OLLAMA_HOST`. |
| `Getenv` | Injectable lookup; nil means the process environment. |
| `HTTPClient` | Sends the requests; a copy of it is used, so its `Timeout`, `CheckRedirect` and `Jar` are kept, and its `Transport` runs beneath the layer that deletes `Authorization`. Nil means a client with no overall timeout, so only the caller's context cuts a long stream. |
| `Now` | Injectable clock for message timestamps. |
| `Warnf` | Receives the report of a transcript repair; nil discards it. |

`ollama.API` is `"ollama-chat"`, the api string of the catalog's `ollama` vendor.

**No credential is sent.** The provider's requests go through a layer that
deletes any `Authorization` header before the embedder's `HTTPClient.Transport`
sees them, so none is sent whatever `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY` or
`OLLAMA_API_KEY` hold; those values appear in no request. The embedder's own
transport may add headers, an `Authorization` for a proxy in front of Ollama
included. The SDK has a signing path of its own, which the provider cannot
turn off: when `OLLAMA_AUTH` is set in the process environment it reads
`~/.ollama/id_ed25519`, signs the request and adds a `ts` query parameter,
and fails the request before it is sent if the key is missing. Only the
`Authorization` header it adds is deleted.

What the provider sends, as one `POST` to `<host>/api/chat`:

- `model` is `Model.ID` as written, `stream` is true and `truncate` is false.
- `options.num_ctx` is `Model.ContextWindow`, or
  `catalog.OllamaDefaultContextWindow` (32,768) when that is not positive. Ollama
  applies it only when the model loads: a model already loaded at another length
  keeps that length until it unloads.
- `options.num_predict` is the request's `MaxTokens` when it is positive and not
  above `Model.MaxOutputTokens`, else the cap, else the request's value, else
  absent. `temperature` and `top_p` are sent when set, zero included, and `stop`
  when there are stop sequences. `keep_alive`, `format` and images are never sent.
- The text blocks of `Request.System` are one leading `system` message, joined
  with a blank line; none when there is no text. The prefix, then the messages,
  in order. An assistant message with nothing to send is skipped. Each tool
  result is a `tool` message with `provider.ToolResultText` as its content (`Error: `
  first for an error), the tool's name and the id of the call it answers.
- A call's `id`, `index` (its position among the message's calls), name and
  arguments are sent with the arguments' keys in the order of `ToolUseBlock.Input`
  and each value as it was written.
- Thinking is replayed as the message's `thinking`, the unredacted blocks
  concatenated, only for a message whose `Model` is `Model.ID`; any other
  thinking is dropped and never sent as content.
- Each tool is `{"type":"function","function":{"name","description","parameters"}}`,
  `parameters` being the part of the schema the SDK's tool type carries:
  `additionalProperties`, `format` and bounds of a property are dropped, what is
  inside an `items` schema and `$defs` is kept, and a tool with no schema, or
  a schema with no properties, is `{"type":"object","properties":{}}`. A tool choice of
  `none` sends no `tools` key. A schema the SDK type cannot carry ends the turn
  before any request (`ollama: the input schema of tool "<name>" is not one Ollama's
  tool type can carry: <error>`).
- `think` is a string level read from `Model.Efforts` as the Anthropic provider
  reads it: none asked, or a model whose `ThinkingKind` is `none`, sends no
  `think`; with no map the effort is sent by its name; with a map, a level that is
  absent or nil sends nothing and a present level sends its value, trimmed and
  lower-cased. No effort is moved to another level, so `xhigh` on the catalog row,
  which does not list it, sends none.
- Before encoding, `provider.RepairToolPairing` runs on the messages (not the
  prefix): errored and aborted assistant turns are dropped, a result is kept only
  in the run after the turn that made the call, and an unanswered call gets an
  error result `No result provided`. `Warnf` is told once when it changed anything.

What it returns: a `MessageStartEvent` with the first chunk; thinking, text and
tool-call events in the order the server sends them, with block indices from 0 in
order of first appearance and a new block when the kind changes (a completed text
or thinking block is appended to the partial message, followed by a
`MessageUpdateEvent`); then, with the done chunk, the block-end events in block
order and the `MessageEndEvent`. Thinking is never reported as text. A tool call
arrives whole: a start event, one delta holding all the arguments and the block.
Its id is the server's; a call with none, or one that repeats an id of the turn,
gets `ollama_<random per Client>_<n>`. The arguments are the JSON the SDK's
ordered map writes, so the top-level keys are in the order the server sent and
a nested object's keys are sorted. `done_reason` is kept in `RawStopReason`; `length`
is `StopReasonLength` (even with tool calls), else a message with tool calls is
`StopReasonToolUse`, else `StopReasonStop`.

The usage is that of the done chunk: `CacheReadTokens` from
`prompt_eval_cached_count` when it is positive, `InputTokens` the
`prompt_eval_count` less the cached count (never below 0), `OutputTokens` from
`eval_count`, `Requests` 1. No reasoning count is set, because Ollama reports
none. The cost is 0 with its presence bit set whenever the chunk reported a count,
whether the model id is listed or not: a run on Ollama is priced at zero, not
unpriced. A done chunk with no count at all reports no usage and no cost.

Errors. There is exactly one HTTP attempt per `Stream` call; nothing is retried.
Every text starts `ollama:`, and none holds the value of an environment variable.

| Failure | Result |
|---|---|
| The caller's context cancelled | Takes precedence over every case below. `StopReasonAborted`, `ErrorMessage` `Request was aborted`, `Err` `core.ErrAborted`, no `ErrorEvent`, no tool call. |
| A body that ends without a done chunk | `ollama.ErrStreamTruncated`, `ollama: stream ended before done`; the text and thinking so far are kept, every tool call is dropped. |
| A failure to connect (refused, DNS, no route) | Wraps `ollama.ErrUnreachable` and the `*net.OpError`: `ollama: cannot reach the Ollama server at <host> (<error>); start it with "ollama serve", or set OLLAMA_HOST to its address`. |
| HTTP 404 | Wraps `ollama.ErrModelNotFound` and is a `*ollama.StatusError` with `Code` 404: `ollama: HTTP 404: the model "<id>" is not on the server at <host> (Ollama said: <message>); run "ollama pull <id>"`. |
| A message that says the input exceeds the context length, in a status error or an error line in a 200 stream | Wraps `ollama.ErrContextWindow` (and is a `*StatusError` when it came as a status): `ollama: <message> (model "<id>", num_ctx <n> sent); shorten the input or ask for a larger context length, but a model already loaded at a smaller length keeps it until it unloads`. |
| HTTP 401 or 403 | A `*ollama.StatusError`: `ollama: HTTP 401 from <host>: AgentKit sends no credential to Ollama; check OLLAMA_HOST (Ollama said: <message>)`. |
| Any other non-2xx | A `*ollama.StatusError`, text `ollama: HTTP <code>: <message>`. |
| An error line inside a 200 stream | `ollama: <message>`. |

The message is Ollama's `error`, else the status text. The server's status is read
from the transport: the SDK's `Chat` returns no error for an error status whose
body is empty. A failed turn has `StopReasonError`, the usage `Requests` 1 with no
token field, and ends with a terminal `ErrorEvent`, then the `MessageEndEvent`.
The typed error is on the `ErrorEvent` the loop forwards and on the error
`Agent.Run` returns (see [Run behaviour](#run-behaviour)).

## Model catalog

The embedded `catalog/catalog.json` (version `2026-10-21`, schema version 1)
lists models for the three providers an agent can run on, which
`catalog.Providers()` returns in this order: `anthropic`, `openrouter` and
`ollama`. The set is fixed in code; a vendor section of a catalog given to
`catalog.Parse` that is not one of the three cannot be selected.

- `catalog.Lookup(id)` resolves a Claude model id against the `anthropic` rows
  only; the `anthropic/` prefix is optional. `catalog.Select` calls it for an
  Anthropic id.
- `catalog.Select(provider, model, effort)` resolves any of the three
  providers, offline ([below](#catalogselect)). `agentkit.Resolve`, and so
  `agentkit.New`, calls it with `Config.Provider`, `Config.Model` and
  `Config.Effort`.

Each of the three providers has a wire in this repository (`provider/anthropic`,
`provider/openrouter` and `provider/ollama`), and `Config.Provider` chooses
among them ([PRD 14](prd/14-run-the-agent-on-anthropic-openrouter-or-ollama.md)).

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

A fallback is a documented guess, not a lookup at the vendor. A run on such an
id costs nothing by the catalog's reckoning, and says so: `Usage.Unpriced` is
set on Anthropic, and on OpenRouter when the response carried no cost
([Run behaviour](#run-behaviour)).

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
tools (`tools.For`) and the code-search index (`codesearch.New`) are both
built for one, so the ignore configuration is given here, once.

| Field | Default |
|---|---|
| `Ignore` | The zero `workspace.IgnoreOptions`: the real gitignore environment, global excludes included. `workspace.NoGlobalExcludes()` pins an empty global layer, for tests and hermetic tool sets. |

An empty `root` is the current directory, and a root that does not exist yet is
kept in its lexical absolute form and is not an error. File tools cannot leave
the root. `execute` can: confining it is the job of a sandbox around the
whole process, which the platform that runs it provides.

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
| `IndexDir` | empty: no stored index | Keeps the index under `IndexDir/agentkit-codesearch-<sha256 of the root>/` for later processes; `Close` keeps it. Must lie outside the workspace, or `New` refuses it. See [the stored index](codesearch.md#the-stored-index). |

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
It returns once the session is closed even when stdin is a blocking pipe, as it
is for a program a harness starts as a child process.
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

## Limits on untrusted input

`wire.Defaults()`: 16 MiB per message, 1,000,000 elements per container,
depth 64, 2,000,000 nodes. The MCP client holds every inbound stdio frame,
JSON response body and SSE event to `ConnectionOptions.Limits` (zero fields
take these defaults) and rejects duplicate keys before the SDK decodes it. The
Anthropic provider checks each stream event and response body the same way,
with the defaults. `mcpserve` holds every inbound stdio
frame and every authorized HTTP body to `wire.Defaults()` too (413 above the
size, 400 for a body `wire.Guard` rejects), with no option.

## Compatibility

Go 1.27 or later (`go.mod`). Builds are checked for linux/amd64,
linux/arm64, darwin/arm64 and windows/amd64 with cgo off; the host is also
built with cgo on. cgo code lives only in `//go:build cgo` files, each with a
pure-Go fallback.
