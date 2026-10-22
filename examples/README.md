# AgentKit examples

Every example is a standalone program with its own `README.md`: what the
feature is, how to run it, and the code an embedder copies. Each is a single
`main.go` you can read top to bottom; they deliberately repeat their setup
rather than sharing a helper package, so nothing you need is in a file you
have not opened.

`agentdemo`, `codemode`, `sandbox`, `gateway` and `harness` need **no API key and no network**:
they run against the scripted provider `provider/faux`. `toolbox` needs none either, and has no model
at all: it calls the built-in tools by hand. The others, `codingagent`,
`customtools` and `mcp`, call a model on a provider you choose, Anthropic, OpenRouter or
Ollama, with three environment variables (below).

| Example | Run it | What it teaches |
|---|---|---|
| [`agentdemo`](agentdemo) | `go run ./examples/agentdemo` | The real loop against a scripted provider: the event stream, why the loop ignores `stop_reason`, why a truncated tool call is never executed, and send-time transcript repair. Start here. |
| [`codingagent`](codingagent) | `go run ./examples/codingagent --dir . "which files define the tool policy?"` | Built-in file/shell tools, a workspace root, and the `execute` authorization boundary. |
| [`customtools`](customtools) | `go run ./examples/customtools` | Writing tools well: the schema combinators, `Handler` vs `Execute`, argument repair, sequential execution, per-tool prompt guidelines, and a tool that ends the run. |
| [`mcp`](mcp) | `go run ./examples/mcp` · `--external CMD` | The MCP client: consuming a server's tools under qualified names, with the pool refusing a server tool that would shadow one of yours. |
| [`toolbox`](toolbox) | `go run ./examples/toolbox` · `list` · `--dir . call find_symbol name=Runner` · `--dir . repl` | The built-in tools with no model: a tour of all eleven through `agentkit.NewRunner`, then `call` and `repl` for your own arguments on your own directory, with the shell tools behind `guard.Allow`. Results as a model reads them, as `Data` for a program, and as typed values from `tools.Search` and `tools.References`. |
| [`codemode`](codemode) | `go run ./examples/codemode` | Code mode: one tool that runs a model-written Starlark script over other tools — chained and parallel calls, errors as values, filtering before anything reaches the conversation — through the agent's own nested-call pipeline. |
| [`sandbox`](sandbox) | `cd examples/sandbox && go run .` (its own module) | Sandboxes: `sandbox.Open` creating one and syncing a project into it, proxy tools that are drop-in replacements for `tools.For`, a guarded code-mode script over them, and exporting files back out — against `local`, which is not isolation. |
| [`gateway`](gateway) | `cd examples/gateway && go run .` (its own module) · `--url URL --token-file FILE` | The sandbox gateway: a `gatewayd.Server` started in process on a loopback port, and an embedder that holds only its URL and token — `gateway.Dial`, a refused token, `sandbox.Open`, `List`, and the same sandbox work through the relay. Or dial an `agentkit-gateway` already running. |
| [`harness`](harness) | `cd examples/harness && go run .` (its own module) · `--podman --image IMG --repo URL --spec N --harness claude` | Another harness in an AgentKit sandbox: a sandbox filled from a repository with one `sandbox.Open`, `specr` run inside it over the proxy `execute`, the spec's checks as a baseline, then `code_mode` and the proxy tools served by `mcpserve` to Claude Code or Pi over MCP on loopback, under `guard.Allow`, with the harness's own tools off — and the checks again as the gate. Offline, a scripted stand-in plays the harness over the same MCP server. |

## Configuring an application

AgentKit reads no configuration file and has no global state. Everything is
either a field on `agentkit.Config` or an environment variable read by the
provider the agent is built on. Three settings choose what runs the agent, and
the smallest real agent sets them:

```go
agent, err := agentkit.New(agentkit.Config{
	Provider:   "openrouter", // "anthropic" (the default when empty), "openrouter" or "ollama"
	Model:      "STANDARD",   // a tier, or the provider's own model id
	Effort:     "",           // empty: the tier's effort
	System:     "You are concise.",
	Toolset:    agentkit.Toolset{Tools: myTools},
	MaxTurns:   12,
	MaxCostUSD: 1.00,
})
if err != nil {
	return err // a missing credential is reported by name, for example OPENROUTER_API_KEY
}
```

`New` validates the whole config before returning an agent, so a bad tool,
an unknown provider or effort, a missing model, a missing credential or an
unguarded shell is an error here rather than on the first run. The agent then has
seven methods: `Run` and `Stream` execute a prompt, `Messages`, `Usage` and
`ReachableTools` read its state, `Runner` returns the runner its tool calls go
through, and `Resolution` reports the provider, model and effort it was built to
run.

There are three things to get right.

### 1. A provider and its credential

`Config.Provider` names the service that runs the model. `agentkit.New` builds
that provider's client itself, from the variables below, read through
`Config.Env` and `Config.Getenv` or, with neither, the process environment. A
credential that is missing is an error from `New`, before any request. **Each
provider reads only its own variables**, and the library reads no `AGENTKIT_*`
variable: those are these examples'.

| Provider | Runs the model on | Reads | Missing or wrong |
|---|---|---|---|
| `anthropic` (the default) | The Anthropic Messages API, direct, on Vertex AI or on Bedrock | The variables in the table below | `anthropic.ErrNoCredentials`, naming what to set |
| `openrouter` | OpenRouter's Chat Completions API | `OPENROUTER_API_KEY` (required); `OPENROUTER_BASE_URL` (default `https://openrouter.ai/api/v1`) | `openrouter.ErrNoAPIKey`, naming `OPENROUTER_API_KEY` |
| `ollama` | A server you run, through Ollama's `/api/chat` | `OLLAMA_HOST` (default `http://127.0.0.1:11434`); no credential | `ollama.ErrInvalidHost` for a host it cannot use; a server that is down, or a model not pulled, is found by the first request |

```bash
export OPENROUTER_API_KEY=sk-or-...        # OpenRouter
export OLLAMA_HOST=127.0.0.1:11434         # Ollama, if the server is not on this machine's default
```

When you need provider options instead (`VertexProject`, `Betas`, `MaxRetries`,
a `Transport`) build the provider yourself and pass it as `Config.Client`, for
example `anthropic.Provider(model, anthropic.Options{…})` with `model` from
`catalog.Select`; `Client` is used instead of the one `New` would build, and
`Provider` and `Model` still choose the row and the effort.

#### Anthropic's deployments

For `anthropic`, the variables below choose the deployment (`anthropic.Resolve`,
`provider/anthropic/resolve.go`, is what `New` checks them with).

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
which `New` returns before building the agent. A Vertex selection with no
project fails the same way, naming the variables. `anthropic.Options.VertexProject` /
`VertexLocation` select Vertex and set its location in code, over the
environment.

```bash
export ANTHROPIC_API_KEY=sk-ant-...        # the Anthropic API

export CLAUDE_CODE_USE_VERTEX=1            # or Claude on Vertex AI
export ANTHROPIC_VERTEX_PROJECT_ID=my-project
export CLOUD_ML_REGION=us-east5

export CLAUDE_CODE_USE_BEDROCK=1           # or Claude on Amazon Bedrock
export AWS_REGION=us-west-2
```

Vertex authenticates with a Google OAuth access token, through the SDK's
Vertex option. The token comes from, in order:

1. a Google access token in `ANTHROPIC_AUTH_TOKEN` (for example
   `$(gcloud auth print-access-token)`; refresh it yourself, it is
   short-lived);
2. `anthropic.Options.VertexTokenSource`, any `oauth2.TokenSource` the
   application owns;
3. Google Application Default Credentials, looked up on the first request,
   so building the provider needs no credential and no network.

An `ANTHROPIC_API_KEY` left over from a direct deployment is **dropped**, not
forwarded: it is not a Vertex credential, and sending it would hand a
first-party secret to a third party.

### 2. A model, from the catalog

`Config.Model` is a tier or the provider's own model id, and `agentkit.New`
resolves it with `catalog.Select(provider, model, effort)`:

```go
sel, err := catalog.Select("ollama", "STANDARD", "") // sel.Model is the row, sel.Effort the tier's
```

A **tier** (`SIMPLE`, `STANDARD` or `ADVANCED`, in any case) picks a model and an
effort for the provider, the same name on all three:

| Provider | `SIMPLE` | `STANDARD` | `ADVANCED` |
|---|---|---|---|
| `anthropic` | `claude-sonnet-5-5`, medium | `claude-sonnet-5-5`, high | `claude-opus-5-5`, xhigh |
| `openrouter` | `qwen/qwen3.8-flash`, low | `qwen/qwen3.8-flash`, medium | `qwen/qwen3.8-flash`, high |
| `ollama` | `qwen3.8:27b`, low | `qwen3.8:27b`, medium | `qwen3.8:27b`, high |

On Ollama a tier is a model you have to have pulled: `ollama pull qwen3.8:27b`.
The catalog supplies what the model id does not carry: the context window, the
output cap, the prices, and how the model takes extended thinking
(`Model.ThinkingKind`: `adaptive` with an effort, `budget` with `budget_tokens`,
or `none`).

The catalog is **not an allowlist**. An id it does not list for the provider, a
model released after this build, or Vertex's dated ids such as
`claude-sonnet-5@20260401`, still resolves, with `Resolution().Listed` false: the
provider's default window and output cap, adaptive thinking, and on Anthropic and
OpenRouter **no price**. Unless the vendor reports a cost itself (OpenRouter does
for most responses), a run's cost then reads as zero, `RunResult.Usage.Unpriced`
says so (the examples print `cost unknown`), and `Config.MaxCostUSD` never fires
for it. Resolving never means the model exists; the vendor decides that on
the first request.

### 3. An effort

`Config.Effort` is `agentkit.EffortLow` … `agentkit.EffortMax` (`low`, `medium`,
`high`, `xhigh`, `max`), or empty for the tier's effort when `Model` is a tier and
none otherwise. An unknown effort is an error from `New`. Every request carries
the resolved effort in the provider's own form, and `Agent.Resolution()` reports
it.

### Other variables

These are read by the model-calling examples (`codingagent`, `customtools` and
`mcp`) and by nothing in the library. Each example reads them in its own few
lines, and passes them to `agentkit.Config` unchanged.

| Variable | Effect |
|---|---|
| `AGENTKIT_PROVIDER` | The provider: `anthropic`, `openrouter` or `ollama` (`Config.Provider`). Default empty, which is `anthropic`. |
| `AGENTKIT_MODEL` | A tier or the provider's own model id (`Config.Model`). Default `STANDARD`. |
| `AGENTKIT_EFFORT` | The effort (`Config.Effort`). Default empty: the tier's effort. |

```bash
AGENTKIT_PROVIDER=ollama AGENTKIT_MODEL=SIMPLE AGENTKIT_EFFORT=high go run ./examples/codingagent "hello"
```

The summary line each example prints names the provider, model id and effort that
`agent.Resolution()` reports, with the turns, tokens and cost.

## Things every application has to decide

These are not defaults you can ignore — the library will stop you.

**A shell tool needs an authorization boundary.** Passing `execute` or
`run_command` in `Config.Toolset.Tools` — directly or reachable
through a wrapper tool — with a nil `Config.Toolset.Guard` makes `agentkit.New`
return an error wrapping `core.ErrUnguardedExecute`; no agent is built. A
headless service would otherwise hand the model an unrestricted shell by
omission. Supply an interceptor — `guard.Allow("go")` is an allowlist in one
call, and `guard.Restricted` a replaceable starting point with more options —
or pass `guard.AllowAll` to say in code that you meant it.
See [`codingagent`](codingagent).

**Bounds are how a runaway run ends.** `Config.MaxTurns` stops the run once
that many turns have completed (`core.RunStopMaxTurns`, error
`core.ErrMaxTurns`); `Config.MaxCostUSD` stops it before the next request
once the run has spent that much (`core.RunStopBudgetExceeded`, error
`core.ErrBudgetExceeded`); `Config.Timeout` bounds wall-clock time. Zero
means unbounded, so without them a tool-using agent has no upper bound. The
cost check runs before each request, so a run can overshoot a budget by at
most one turn plus its tool batch.

**File tools are contained to a workspace root**, resolved through symlinks
before every read and re-checked immediately before every write. `execute` is
deliberately *not* contained — that is what the interceptor above is for.

## Troubleshooting

**`anthropic: missing credentials: …`** — no cloud deployment is selected and
none of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN` is
set. The message names what to set.

**`openrouter: OPENROUTER_API_KEY is not set …`** — `AGENTKIT_PROVIDER=openrouter`
and no key in the environment. `agentkit.New` reports it before any request.

**`ollama: cannot reach the Ollama server at … start it with "ollama serve"`** —
nothing answers at `OLLAMA_HOST` (default `http://127.0.0.1:11434`). Building the
provider makes no request, so it is the first request that finds this out. Start
the server, or point `OLLAMA_HOST` at the machine that runs it.

**`ollama: HTTP 404: the model "…" is not on the server … run "ollama pull …"`** —
the server is up and does not have the model of the tier. Pull it, or choose a
model it has with `AGENTKIT_MODEL`.

**`agentkit: unknown provider "…"; known providers are anthropic, openrouter and ollama`**
(and `unknown effort`) — a value `AGENTKIT_PROVIDER` or `AGENTKIT_EFFORT` does not
accept. The message lists the choices.

**A 401 from Vertex or Bedrock despite the pre-flight passing** — the
pre-flight only resolves the deployment; Google and AWS credentials are
checked on the first request. A Vertex 401 names the project and how the
deployment was selected.

**`agent is busy` (`ErrBusy`)** — `Run` or `Stream` was called while a run was
in flight. A second run fails rather than queues, because a prompt queued
behind a running one was written against a transcript that has since
changed. Wait for the first run to finish, then retry, or queue it yourself.

**Watching a run** — there are no hooks. `Stream` returns the run's events
(`TurnEndEvent`, `ToolExecutionStartEvent`, `ToolResultEvent`, text deltas,
`ErrorEvent`, …; see `core/event.go`), and a panic in a tool or the guard
arrives as an `ErrorEvent` rather than crashing the process.

**Nothing streams** — a non-streaming provider emits no delta events at all.
Deltas are an optimization; the authoritative events always arrive.
