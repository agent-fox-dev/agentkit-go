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

### 1. A credential

The one wire API is Anthropic's (`provider/anthropic`). It reads an
**ordered** list of variables, not a single `<VENDOR>_API_KEY` convention. The
first one set wins, and the scheme differs per variable — that is the whole
reason the list is ordered rather than a lookup.

| Vendor | Variables, in order | Sent as |
|---|---|---|
| `anthropic` | `ANTHROPIC_API_KEY` | `x-api-key` |
| | `ANTHROPIC_AUTH_TOKEN` | `Authorization: Bearer` |
| | `ANTHROPIC_OAUTH_TOKEN` | `Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20` |
| | on Vertex: a Google OAuth token in `ANTHROPIC_AUTH_TOKEN`, `ambient` when the transport holds it | `Authorization: Bearer` |

**Credentials have three states, not two.** A deployment using a cloud
instance role, Google ADC or a workload identity has *no key this process can
read* and a transport that will nonetheless authenticate. That is `ambient`,
and it must pass a pre-flight check that `none` fails — otherwise every
service-account deployment fails a check a plain key would have passed. The
examples' `checkCredentials` shows the correct test:

```go
auth := provider.ResolveAuth(anthropic.VendorAuth, provider.Env{})
if auth.State == provider.CredentialNone {
    // genuinely unconfigured
}
```

Setting only a base URL also yields `ambient`: the vendor is *discovered* but
not *authenticated*, which is exactly the state a gateway that authenticates
by URL leaves you in.

The credential is read on every request, so a long-running process whose
token expires updates the variable (or `RequestOptions.Env`); there is no
credential store.

### 2. A base URL, when you are not talking to Anthropic directly

| Variable | Points at |
|---|---|
| `ANTHROPIC_BASE_URL` | a proxy or gateway in front of Anthropic |
| `ANTHROPIC_VERTEX_BASE_URL` | a proxy in front of Vertex; beats `ANTHROPIC_BASE_URL` when the Vertex deployment is on |

**Claude on Vertex** is a config change rather than a provider swap, on the
same wire implementation:

```bash
export CLAUDE_CODE_USE_VERTEX=1
export ANTHROPIC_VERTEX_PROJECT_ID=my-project
export CLOUD_ML_REGION=us-east5            # optional; default `global`
```

| Variable | Does |
|---|---|
| `CLAUDE_CODE_USE_VERTEX` | selects the deployment. Read for *truth*, not presence: `=0` is an explicit **off** that vetoes every other environment signal |
| `ANTHROPIC_VERTEX_PROJECT_ID` | the GCP project. It selects the deployment on its own **only when no `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_OAUTH_TOKEN` is set** |
| `CLOUD_ML_REGION` | the location; `GOOGLE_CLOUD_LOCATION` and `CLOUDSDK_COMPUTE_REGION` also work |
| `ANTHROPIC_VERTEX_BASE_URL` | a proxy in front of Vertex. Like the project variable, a Vertex host here selects the deployment only when no Anthropic-direct credential is set |

`GOOGLE_CLOUD_PROJECT` and `CLOUDSDK_CORE_PROJECT` may *supply* the project
once something else has selected the deployment; they are set on every GCE and
Cloud Run box, so they never select it. `Options.VertexProject` / `Options.VertexLocation` are
the in-code equivalents, and a Vertex base URL selects the deployment too. A
selected deployment with no project anywhere is an error naming the project,
not a request sent to `api.anthropic.com` with a Vertex path.

**Going back to the direct API** is `unset CLAUDE_CODE_USE_VERTEX` *and*
`unset ANTHROPIC_VERTEX_PROJECT_ID` — or, if the project variable is set by
something you do not control, `export CLAUDE_CODE_USE_VERTEX=0`, which turns
the deployment off outright. Exporting an `ANTHROPIC_API_KEY` is enough on its
own when the project variable is the only thing left over: a key that only the
direct deployment can use outranks a project that only names coordinates. If a
request does reach Vertex without a credential, the 401 says so — it names the
project, the setting that selected the deployment, and both ways out, because
Google's own body names none of them.

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

Vertex names Claude models with a dated suffix (`claude-sonnet-5@20260401`).
The catalog is not an allowlist, so such an id resolves by cloning the vendor's
default row and reaches the URL verbatim:
`AGENTKIT_MODEL=anthropic/claude-sonnet-5@20260401`.

### 3. A model, resolved through the catalog

```go
model, err := catalog.ResolveModel("anthropic/claude-sonnet-5")
```

`ResolveModel` is the single entry point, and it is what supplies the wire
API, base URL, context window, pricing, reasoning support and compatibility
profile. The model-ID string carries none of that, which is why a
pass-through design cannot clamp `max_tokens`, cost a turn, or pick the right
request shape.

The catalog is **not an allowlist**. An unknown id under a *known* vendor
clones that vendor's default row with a warning, so a model released after
this build works without an SDK release. An unknown *vendor* is a
configuration error. A bare id that matches two vendors resolves to nothing
and errors rather than guessing.

Every example that calls a model takes `AGENTKIT_MODEL` to override its
default:

```bash
AGENTKIT_MODEL=anthropic/claude-opus-5-5 go run ./examples/codingagent "hello"
```

`catalog.Default().Vendors()` lists what the shipped snapshot knows. **A
vendor with catalog rows is not necessarily a vendor with a provider**: the
snapshot still carries `google` and `openai` rows, but the only wire this
module ships is Anthropic's, so a model under another vendor resolves and then
has no registered provider to send it. An embedder that needs another vendor
writes a `core.APIProvider` of its own; `provider/faux` is the template.

One caution about sibling-cloning, because it costs real money to miss: an
unknown id under a *known* vendor resolves, it does not validate. Ask for
`anthropic/claude-sonnet-4-5` today and you get a working descriptor cloned
from the current default row — and then the request fails at the vendor,
because that model is gone. Resolution succeeding means "AgentKit knows how
to build this request", never "this model exists".

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

**`no credential for vendor "anthropic"`** — none of the variables above is
set, and there is no ambient credential. The message names the variables.

**A 401 despite the pre-flight passing** — you have a base URL set but no key,
which is the `ambient` state: discovered, not authenticated. Set the key too.

**`agent is busy` (`ErrBusy`)** — `Run` or `Stream` was called while a turn was
in flight. Conflicting operations fail rather than queue, because a prompt
queued behind a running turn was written against a transcript that has since
changed. Retry, queue it yourself, or use `Steer`/`FollowUp` to deliver a
message into the *running* turn.

**Nothing streams** — a non-streaming provider emits no delta events at all.
Deltas are an optimization; the authoritative events always arrive.
