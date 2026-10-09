# AgentKit examples

Every example is a standalone program with its own `README.md`: what the
feature is, how to run it, and the code an embedder copies. Each is a single
`main.go` you can read top to bottom; they deliberately repeat their setup
rather than sharing a helper package, so nothing you need is in a file you
have not opened.

`agentdemo` and `codemode` need **no API key and no network**: they run
against the scripted provider `provider/faux`. The others call a model and
need an Anthropic credential (below).

| Example | Run it | What it teaches |
|---|---|---|
| [`agentdemo`](agentdemo) | `go run ./examples/agentdemo` | The real loop against a scripted provider: the event stream, why the loop ignores `stop_reason`, why a truncated tool call is never executed, and send-time transcript repair. Start here. |
| [`codingagent`](codingagent) | `go run ./examples/codingagent --dir . "which files define the tool policy?"` | Built-in file/shell tools, a workspace root, and the `execute` authorization boundary. |
| [`customtools`](customtools) | `go run ./examples/customtools` | Writing tools well: the schema combinators, `Handler` vs `Execute`, argument repair, sequential execution, per-tool prompt guidelines, and a tool that ends the run. |
| [`mcp`](mcp) | `go run ./examples/mcp` · `--external CMD` | The MCP client: consuming a server's tools under qualified names, with the pool refusing a server tool that would shadow one of yours. |
| [`codemode`](codemode) | `go run ./examples/codemode` | Code mode: one tool that runs a model-written Starlark script over other tools — chained and parallel calls, errors as values, filtering before anything reaches the conversation — through the agent's own nested-call pipeline. |

## Configuring an application

AgentKit reads no configuration file and has no global state. Everything is
either a field on `core.AgentConfig` or an environment variable consulted at
request time. There are exactly three things to get right.

### 1. A deployment and its credential

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

### 2. A model, looked up in the catalog

```go
model, known := catalog.Lookup("claude-opus-5-5") // the "anthropic/" prefix is optional
```

`catalog.Lookup` supplies what the model id does not carry: the context
window, the output cap, the prices, and how the model takes extended thinking
(`Model.Thinking`: `adaptive` with an effort, `budget` with `budget_tokens`,
or `none`). The catalog lists Claude models only.

The catalog is **not an allowlist**. An id it does not list — a model
released after this build, or Vertex's dated ids such as
`claude-sonnet-5@20260401` — still resolves, with `known` false: a
1,000,000-token window, a 128,000-token output cap, adaptive thinking and
**no price**, so a run's cost reads as zero. Resolving never means the model
exists; the vendor decides that on the first request.

Every example that calls a model takes `AGENTKIT_MODEL` to override its
default:

```bash
AGENTKIT_MODEL=claude-opus-5-5 go run ./examples/codingagent "hello"
```

### Other variables

| Variable | Effect |
|---|---|

## Things every application has to decide

These are not defaults you can ignore — the library will stop you.

**A shell tool needs an authorization boundary.** Registering `execute` or
`run_command` with a nil `AgentConfig.BeforeToolCall` fails the
run with `core.ErrUnguardedExecute`, before any request is built. A headless
service would otherwise hand the model an unrestricted shell by omission.
Supply an interceptor — `guard.Restricted` is a replaceable starting
point — or pass `guard.AllowAll` to say in code that you meant it.
See [`codingagent`](codingagent).

**A stop policy is how a run ends.** `AgentConfig.StopPolicy` is a
`func(core.StopContext) bool` you write; the examples bound turns and spend
in one function, calling `sc.SetReason(core.RunStopMaxTurns)` or
`sc.SetReason(core.RunStopBudgetExceeded)` so the run says which limit fired.
Without one, a tool-using agent has no upper bound. The check runs after each
turn, so a run can overshoot a budget by at most one turn plus its tool batch.

**File tools are contained to a workspace root**, resolved through symlinks
before every read and re-checked immediately before every write. `execute` is
deliberately *not* contained — that is what the interceptor above is for.

## Troubleshooting

**`anthropic: missing credentials: …`** — no cloud deployment is selected and
none of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN` is
set. The message names what to set.

**A 401 from Vertex or Bedrock despite the pre-flight passing** — the
pre-flight only resolves the deployment; Google and AWS credentials are
checked on the first request. A Vertex 401 names the project and how the
deployment was selected.

**`agent is busy` (`ErrBusy`)** — `Run` or `Stream` was called while a turn was
in flight. Conflicting operations fail rather than queue, because a prompt
queued behind a running turn was written against a transcript that has since
changed. Retry, queue it yourself, or use `Steer`/`FollowUp` to deliver a
message into the *running* turn.

**Nothing streams** — a non-streaming provider emits no delta events at all.
Deltas are an optimization; the authoritative events always arrive.
