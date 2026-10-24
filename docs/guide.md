# User guide

This guide walks through AgentKit one part at a time, with a short example for
each. It explains what each part is for and how to use it; the exact options,
defaults and error messages are in the
[configuration reference](configuration.md), and how the code works inside is
in the [architecture](architecture.md) document.

## Contents

- [How the parts fit together](#how-the-parts-fit-together)
- [Your first agent](#your-first-agent)
- [Choosing a provider](#choosing-a-provider)
  - [Credentials](#credentials)
  - [Models and tiers](#models-and-tiers)
  - [Effort](#effort)
  - [Building the client yourself](#building-the-client-yourself)
- [The workspace and the built-in tools](#the-workspace-and-the-built-in-tools)
- [Guarding the shell](#guarding-the-shell)
- [Bounding and watching a run](#bounding-and-watching-a-run)
- [Writing your own tools](#writing-your-own-tools)
- [Code mode](#code-mode)
- [Using tools from MCP servers](#using-tools-from-mcp-servers)
- [Running tools without a model](#running-tools-without-a-model)
- [Serving tools to another harness over MCP](#serving-tools-to-another-harness-over-mcp)
- [Sandboxes](#sandboxes)
  - [Choosing a sandbox provider](#choosing-a-sandbox-provider)
  - [Moving files in and out](#moving-files-in-and-out)
- [Sandboxes on another host: the gateway](#sandboxes-on-another-host-the-gateway)
- [Indexed code search](#indexed-code-search)
- [Testing without a model](#testing-without-a-model)
- [Troubleshooting](#troubleshooting)

## How the parts fit together

An agent is built in layers. Each layer takes the one before it as a value, so
you can stop at any layer and use it on its own:

| Step | Package | What you get |
|---|---|---|
| 1. Open a workspace | `workspace` | A project directory with its containment boundary and ignore rules. |
| 2. Build tools for it | `tools` | The built-in file, search, navigation and shell tools. Add your own, MCP tools, code mode, or swap in sandbox proxies. |
| 3. Describe the tool side | `agentkit.Toolset` | The tools, a policy that selects among them, the shell guard and an optional after-call hook. |
| 4. Run tool calls | `agentkit.Runner` | Runs calls through validation, the guard and events — with or without a model. |
| 5. Drive a model | `agentkit.New` | The agent loop: sends the conversation to a provider, runs the tool calls it asks for, repeats until done. |

The runner has two consumers besides your own code: the agent loop, and
`mcpserve`, which serves the same tools to another harness over MCP.

## Your first agent

```go
ws, err := workspace.Open(".", workspace.Options{})
if err != nil {
	return err
}
ts, err := tools.For(ws, tools.Options{})
if err != nil {
	return err
}
agent, err := agentkit.New(agentkit.Config{
	Provider: "anthropic",
	Model:    "STANDARD",
	Toolset:  agentkit.Toolset{Tools: ts, Guard: guard.Allow("go", "git")},
	MaxTurns: 20,
})
if err != nil {
	return err
}
res, err := agent.Run(ctx, "Summarise this package.")
if err != nil {
	return err
}
fmt.Println(res.FinalText())
```

`agentkit.New` checks the whole configuration before it returns: an unknown
provider or effort, a missing model, a missing credential, a malformed tool
set, or a shell tool without a guard is an error here, never on the first
request. An `Agent` runs one prompt at a time and keeps its conversation
between runs; `Agent.Messages()` returns it.

[`examples/agentdemo`](../examples/agentdemo) runs the loop offline against a
scripted model, and [`examples/codingagent`](../examples/codingagent) is the
smallest real coding agent.

## Choosing a provider

`Config.Provider` names the service that runs the model:

| `Provider` | Runs the model on | Needs |
|---|---|---|
| `anthropic` (the default) | The Anthropic Messages API — direct, on Google Vertex AI, or on Amazon Bedrock | An Anthropic credential, or Vertex AI / Bedrock settings |
| `openrouter` | OpenRouter's Chat Completions API | `OPENROUTER_API_KEY` |
| `ollama` | An Ollama server you run, local or self-hosted | `OLLAMA_HOST` if the server is not at `127.0.0.1:11434`; no credential |

### Credentials

`agentkit.New` builds the provider's client from environment variables. It
reads them from `Config.Env` first, then `Config.Getenv`, and otherwise from
the process environment. **Each provider reads only its own variables**, so an
Anthropic key is never sent to OpenRouter or Ollama.

```bash
export ANTHROPIC_API_KEY=sk-ant-...        # Anthropic, direct

export CLAUDE_CODE_USE_VERTEX=1            # or Claude on Vertex AI
export ANTHROPIC_VERTEX_PROJECT_ID=my-project
export CLOUD_ML_REGION=us-east5

export CLAUDE_CODE_USE_BEDROCK=1           # or Claude on Amazon Bedrock
export AWS_REGION=us-west-2

export OPENROUTER_API_KEY=sk-or-...        # OpenRouter
export OLLAMA_HOST=gpu-box.local:11434     # Ollama on another machine
```

A missing credential is reported by name, for example
`anthropic: missing credentials: set ANTHROPIC_API_KEY, ...`. Vertex AI and
Bedrock credentials (Google Application Default Credentials, the AWS
credential chain) are looked up on the first request. Every variable each
provider reads is listed under
[Credentials and deployments](configuration.md#credentials-and-deployments).

### Models and tiers

`Config.Model` is either a **tier** or the provider's own model id. A tier
names a model and an effort that suit the provider, so the same configuration
works on all three:

| Provider | `SIMPLE` | `STANDARD` | `ADVANCED` |
|---|---|---|---|
| `anthropic` | `claude-sonnet-5-5`, medium | `claude-sonnet-5-5`, high | `claude-opus-5-5`, xhigh |
| `openrouter` | `qwen/qwen3.8-flash`, low | `qwen/qwen3.8-flash`, medium | `qwen/qwen3.8-flash`, high |
| `ollama` | `qwen3.8:27b`, low | `qwen3.8:27b`, medium | `qwen3.8:27b`, high |

On Ollama, pull the model first: `ollama pull qwen3.8:27b`.

AgentKit carries a small model catalog with each model's context window,
output limit and prices. It is **not an allowlist**: a model id it does not
know, such as one released after this version, still works, with default
limits and no price. Such a run reports its cost as zero and sets
`Usage.Unpriced`, and `MaxCostUSD` cannot stop it. `agentkit.Resolve(cfg)`
shows what a configuration resolves to without building an agent, and
`Agent.Resolution()` reports what an agent runs.

### Effort

`Config.Effort` asks the model to think more or less: `low`, `medium`, `high`,
`xhigh` or `max`. Empty means the tier's effort when `Model` is a tier, and no
effort otherwise. Each provider sends it in its own form; a model that does
not support a level is sent none rather than a different one.

### Building the client yourself

To set provider options that `Config` does not carry — retries, an HTTP
transport, Vertex AI settings in code, beta headers — build the provider
yourself and pass it as `Config.Client`:

```go
sel, err := catalog.Select("openrouter", "STANDARD", "")
if err != nil {
	return err
}
client, err := openrouter.New(sel.Model, openrouter.Options{MaxRetries: &retries})
if err != nil {
	return err
}
agent, err := agentkit.New(agentkit.Config{
	Client: client, Provider: "openrouter", Model: sel.Model.ID, Effort: sel.Effort,
	Toolset: toolset,
})
```

The options of each provider are in the configuration reference:
[`anthropic.Options`](configuration.md#anthropicoptions),
[`openrouter.Options`](configuration.md#openrouteroptions) and
[`ollama.Options`](configuration.md#ollamaoptions).

## The workspace and the built-in tools

`workspace.Open(root, opts)` opens the project directory the tools work in.
The file tools cannot leave it — paths are resolved through symlinks and
checked again before every write — and they skip what `.gitignore` excludes.

`tools.For(ws, opts)` returns eleven tools:

| Tool | Does |
|---|---|
| `read_file` | Read a text file, or a range of its lines. |
| `write_file` | Create or overwrite a file. |
| `edit_file` | Replace exact text in a file. |
| `list_files` | List a directory. |
| `find_files` | Find files by glob pattern. |
| `search_files` | Search file contents with a regular expression. |
| `file_outline` | List the declarations in a source file (Go always; 14 more languages in a cgo build). |
| `find_symbol` | Find declarations by name across the workspace. |
| `find_references` | Find the uses and callers of a declaration (exact for Go). |
| `execute` | Run a shell command line. |
| `run_command` | Run a program from an argument list, without a shell. |

Every result is bounded (50 KB by default); a longer output keeps its head or
tail and the full text is written to a spill file the model can read.
Subprocesses get a reduced environment with credentials stripped.

To give an agent only some tools, pick them by name:

```go
readOnly, err := core.Named(ts, "read_file", "list_files", "find_files", "search_files")
```

or set `Toolset.Policy` to select by name or exclude tools. Limits and options
are under [Built-in tools](configuration.md#built-in-tools-toolsoptions).
[`examples/toolbox`](../examples/toolbox) calls each tool by hand so you can
see what it takes and returns.

## Guarding the shell

`execute` and `run_command` can do anything the process can. AgentKit
therefore refuses to build an agent or runner that can reach either of them —
directly or through a wrapper such as code mode — without a
`Toolset.Guard`.

```go
Guard: guard.Allow("go", "git", "make") // only these programs; no pipes, no env assignments
```

`guard.Allow` admits the listed programs and nothing else. It reads the
whole command line the way a shell would, so `go test && rm -rf /` is
refused. `guard.Restricted(guard.Options{...})` adds switches for shell
operators, `NAME=value` prefixes, tools blocked by name and ending the run on a
block. `guard.AllowAll` allows everything; use it only for tools that run in
an isolating sandbox with no network (see [Sandboxes](#sandboxes)).

The guard is a floor for unattended safety, not a sandbox: a listed program
such as `go` can still run arbitrary code. Details and every option are under
[`guard.Options`](configuration.md#guardoptions).

## Bounding and watching a run

Without limits, a tool-using agent has no upper bound. Set them on `Config`:

| Field | Ends the run |
|---|---|
| `MaxTurns` | after this many model turns |
| `MaxCostUSD` | before a request once the run has spent this much |
| `Timeout` | at this wall-clock deadline |
| `Prune` | (not a stop) drops old tool results from requests as the context window fills |

`Agent.Run` waits for the result. `Agent.Stream` returns the run's events as
they happen — text deltas, tool calls, tool results, usage per turn, errors —
for a live display or a log:

```go
stream, err := agent.Stream(ctx, prompt)
if err != nil {
	return err
}
for ev := range stream.Events() {
	switch e := ev.(type) {
	case core.TextDeltaEvent:
		fmt.Print(e.Delta)
	case core.ToolExecutionEndEvent:
		fmt.Printf("\n[%s done in %dms]\n", e.Name, e.ElapsedMS)
	}
}
```

The run's `RunResult` tells you why it stopped (`StopReason`), what it cost
(`Usage`) and the final text (`FinalText()`). A panicking tool or guard does
not crash the process; it becomes an error event and an error result.
[Run behaviour](configuration.md#run-behaviour) lists every stop reason.

## Writing your own tools

A tool is a `core.Tool` value: a name, a description, an input schema built
with the `schema` package, and a handler.

```go
wordCount := core.Tool{
	Name:        "word_count",
	Description: "Count the words in a text",
	InputSchema: schema.Object(
		schema.Prop("text", schema.String("The text to count")),
	),
	Handler: func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		var args struct{ Text string }
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]int{"words": len(strings.Fields(args.Text))})
	},
}
```

Arguments are coerced and validated against the schema before the handler
runs, so a handler sees well-formed input; a model that sends the wrong shape
gets an error result describing the expected one. Use `Execute` instead of
`Handler` to return a full `core.ToolResult` with an error code. A tool can
also carry prompt guidelines, repair common argument mistakes, run
sequentially, or end the run. [`examples/customtools`](../examples/customtools)
shows each of these.

## Code mode

Each tool call normally costs a model turn. Code mode turns a set of tools into
one tool, `code_mode`, that runs a short Starlark (Python-like) script the
model writes. The script can call tools in sequence or in parallel, filter
their results, and return only what matters — in one turn.

```go
bound, err := core.Named(ts, "list_files", "find_files", "read_file", "search_files")
if err != nil {
	return err
}
cm, _, err := codemode.New(bound, codemode.Options{})
if err != nil {
	return err
}
agent, err := agentkit.New(agentkit.Config{
	Provider: "anthropic",
	Model:    "STANDARD",
	Toolset:  agentkit.Toolset{Tools: []core.Tool{cm}},
})
```

Every call a script makes goes through the same validation and guard as a
direct call, so binding a shell tool still requires a `Guard`. Scripts are
bounded by time, steps, calls and output size
([`codemode.Options`](configuration.md#code-mode-codemodeoptions)).
[`examples/codemode`](../examples/codemode) runs one offline.

## Using tools from MCP servers

The `mcp` package connects to [Model Context Protocol](https://modelcontextprotocol.io)
servers — over stdio, Streamable HTTP or SSE — and turns their tools into
`core.Tool`s, named `<server>__<tool>`:

```go
pool := mcp.NewPool(mcp.ConnectionOptions{})
defer pool.Close()
if _, err := pool.Connect(ctx, mcp.ServerConfig{
	Name:    "docs",
	Command: "docs-mcp-server",
}, nil, os.Getenv); err != nil {
	return err
}
mcpTools, err := pool.Tools(ctx, ts) // refuses a tool that would shadow one of ts
if err != nil {
	return err
}
toolset := agentkit.Toolset{Tools: append(ts, mcpTools...), Guard: guard.Allow("go")}
```

Server definitions can also be read from an `[mcp]` TOML section with
`mcp.ParseConfig`. A crashed server is restarted on the next call, results are
size-capped, and calls are counted against a per-session limit
([MCP configuration](configuration.md#mcp-mcpconfig-mcpserverconfig)).
[`examples/mcp`](../examples/mcp) runs an in-process server.

## Running tools without a model

The agent loop is optional. `agentkit.NewRunner` turns a `Toolset` into a
`Runner` that runs tool calls directly, through the same validation, guard
and events a model's calls go through:

```go
runner, err := agentkit.NewRunner(agentkit.Toolset{Tools: ts, Guard: guard.Allow("go")})
if err != nil {
	return err
}
res := runner.Call(ctx, "find_symbol", json.RawMessage(`{"name":"Runner"}`))
fmt.Println(res.LLMText()) // what a model would read; res.Data holds the structured result
```

Use it in tests, scripts, your own agent loop, or another harness. A failed
call — unknown tool, bad arguments, a guard block — is a result with `OK`
false, never a Go error. A `Runner` is safe for concurrent use; `Batch` runs
several calls the way a model's turn would. Code mode works through a runner
too.

## Serving tools to another harness over MCP

The nested `mcpserve` module serves a runner's tools over MCP, so another
agent harness — Claude Code, Pi, any MCP client on the same machine — can use
AgentKit's tools, code mode and sandboxes while you keep the guard and policy
on your side:

```go
srv, err := mcpserve.New(runner, mcpserve.Options{Instructions: "The tools run in a sandbox."})
if err != nil {
	return err
}
ep, err := srv.ServeLoopback(ctx) // http://127.0.0.1:<port>/mcp with a fresh bearer token
if err != nil {
	return err
}
defer ep.Close()
fmt.Println(ep.ClaudeConfig("agentkit")) // pass to `claude --mcp-config`
```

`srv.ServeStdio(ctx)` serves on the process's stdin and stdout instead, for a
harness that starts your program as a child. Every call goes through
`Runner.Call`, so a guard block reaches the harness as an error result.
[`examples/harness`](../examples/harness) runs Claude Code or Pi against a
sandbox this way. Options and limits:
[MCP serving](configuration.md#mcp-serving-mcpserveoptions).

## Sandboxes

The guard limits what the model may run; a sandbox limits what that can
affect. A sandbox is an ephemeral environment holding one workspace and the
AgentKit agent, `agentkit-sandboxd`. `Sandbox.Tools` returns proxy tools with
exactly the same definitions as `tools.For`, whose calls run inside the
sandbox. The agent loop does not change and does not know a tool is remote.

```go
sb, proxies, err := sandbox.Open(ctx, provider,
	sandbox.Spec{}, // no network, one-hour lifetime, the default image
	sandbox.LocalDir{Path: "./project"}, sandbox.ToolOptions{})
if err != nil {
	return err
}
defer sb.Close(ctx)

agent, err := agentkit.New(agentkit.Config{
	Provider: "anthropic",
	Model:    "STANDARD",
	Toolset:  agentkit.Toolset{Tools: proxies, Guard: guard.Allow("go", "git")},
})
// ... run the agent, then copy its changes back out:
err = sb.Export(ctx, ".", sandbox.LocalDir{Path: "./result"})
```

`sandbox.Open` creates the sandbox, fills it from a source and returns the
proxies, closing the sandbox if a step fails. A `Spec` sets the image,
environment, resources, network mode and lifetime; a provider refuses any
field it cannot honour rather than quietly ignoring it. On Linux the agent
also confines itself with Landlock, and `Spec.RequireLandlock` makes that
mandatory.

### Choosing a sandbox provider

| Provider | Isolation | Needs | Use it for |
|---|---|---|---|
| `sandbox/podman` — `podman.New(podman.Options{})` | A hardened container per sandbox (or a micro-VM with `krun`): read-only root, no capabilities, no host mounts, no network by default | Podman or Docker, and the sandbox image | Untrusted, model-written code |
| `sandbox/landlock` — `landlock.New(landlock.Options{})` | Confinement: the agent runs as a child process under Linux Landlock; no resource limits | Linux 6.7 or later and `agentkit-sandboxd` on `PATH` | Hosts without a container engine |
| `sandbox/gateway` — `gateway.Dial(...)` | Whatever the remote gateway's backend gives | A running `agentkit-gateway` | Sandboxes on another machine |
| `sandbox/local` — `local.Unisolated()` | **None**: runs in your own process | Nothing | Tests and development only |

The Podman provider uses the published image
`quay.io/agentfox/agentkit-sandbox:<version>` by default; build your own
toolchain image `FROM` it. Install the agent for the Landlock provider with:

```bash
go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@vX.Y.Z
```

A proxy shell tool still needs a `Guard`. `guard.AllowAll` is reasonable only
for proxies from an isolating provider (`podman`) with `NetworkNone`, bounded
resources, and exported files treated as untrusted.

### Moving files in and out

| Source or destination | Moves |
|---|---|
| `sandbox.LocalDir{Path: dir}` | A host directory, over the rsync protocol: only what changed, honouring ignore rules, never `.git` on export, never through an escaping symlink. |
| `sandbox.Tar{R: r}` / `sandbox.TarOut{W: w}` | A tar stream, every entry checked before it is written. |
| `sandbox.GitRepo{URL: u}` | A public HTTPS repository, cloned by the provider (Podman, Landlock and the gateway). |

[`examples/sandbox`](../examples/sandbox) runs a sandbox end to end offline.
Every `Spec` field, each provider's rules and the transfer options are in the
[sandbox reference](configuration.md#sandboxes-sandboxspec-sandboxtooloptions-local-the-gateway).

## Sandboxes on another host: the gateway

A gateway runs sandboxes on another machine. The embedder holds only the
gateway's URL and a bearer token; the gateway holds the container engine and
its credentials; the sandbox holds neither.

```go
p, err := gateway.Dial(ctx, "https://gateway.example.com", gateway.Options{Token: token})
if err != nil {
	return err // a refused token matches gateway.ErrUnauthenticated
}
defer p.Close() // destroys the sandboxes p created
sb, proxies, err := sandbox.Open(ctx, p, sandbox.Spec{}, sandbox.LocalDir{Path: "."}, sandbox.ToolOptions{})
```

A gateway sandbox behaves exactly like a local one. Each token sees only its
own sandboxes, and the gateway bounds their number, lifetime and network.
Run the server with the [`agentkit-gateway`](cli.md#agentkit-gateway) command,
or embed `gatewayd.Server` in your own program to authenticate tokens your
way. [`examples/gateway`](../examples/gateway) runs both ends in one process.

## Indexed code search

On large repositories, `search_files` scans every file for every query. The
nested `codesearch` module adds a `code_search` tool backed by an in-process
[zoekt](https://github.com/sourcegraph/zoekt) trigram index, with ranked
results and a query language (`sym:`, `file:`, `lang:`, `case:`):

```go
idx, err := codesearch.New(ws, codesearch.Options{})
switch {
case errors.Is(err, codesearch.ErrUnsupported):
	// Windows: carry on without the index
case err != nil:
	return err
default:
	defer idx.Close()
	opts.Index = idx // tools.For then adds code_search
}
ts, err := tools.For(ws, opts)
```

The index is built on the first query and kept up to date as the tools write
files. See [`codesearch/README.md`](../codesearch/README.md) and
[`examples/indexedsearch`](../examples/indexedsearch).

## Testing without a model

`provider/faux` is a scripted model for tests and demos. Pass it as
`Config.Client`, and the agent runs the turns you script, offline and
deterministically:

```go
agent, err := agentkit.New(agentkit.Config{
	Client: faux.New(
		faux.ToolCall("call_1", "read_file", `{"path":"go.mod"}`),
		faux.Text("It is a Go module."),
	),
	Model:   "STANDARD",
	Toolset: agentkit.Toolset{Tools: ts, Guard: guard.Allow("go")},
})
```

## Troubleshooting

**`anthropic: missing credentials: …`** — no Vertex AI or Bedrock deployment is
selected and none of `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` or
`ANTHROPIC_OAUTH_TOKEN` is set.

**`openrouter: OPENROUTER_API_KEY is not set …`** — the provider is
`openrouter` and there is no key.

**`ollama: cannot reach the Ollama server at …`** — nothing answers at
`OLLAMA_HOST`. Start it with `ollama serve`, or point `OLLAMA_HOST` at the
machine that runs it. AgentKit makes no request until the first turn, so this
appears then, not from `New`.

**`ollama: HTTP 404: the model "…" is not on the server …`** — pull the model
with `ollama pull`, or choose one the server has.

**`agentkit: unknown provider …` / `unknown effort …`** — the message lists
the valid choices.

**An error wrapping `core.ErrUnguardedExecute`** — a shell tool is reachable
and `Toolset.Guard` is nil. Set a guard (see
[Guarding the shell](#guarding-the-shell)).

**A 401 from Vertex AI or Bedrock** — AgentKit checks which deployment is
selected at construction, but Google and AWS credentials are checked by the
first request.

**`agent is busy` (`core.ErrBusy`)** — `Run` or `Stream` was called while
another run was in flight. An agent runs one prompt at a time; wait for the
first run to finish.

**The cost reads as zero** — the model id is not in the catalog and the vendor
reported no cost; `Usage.Unpriced` is set. See [Models and tiers](#models-and-tiers).

**`sandbox: provider "local" cannot honour Spec.Network.Mode = none`** — the
`local` provider has no isolation and only accepts
`sandbox.NetworkUnrestricted`. Ask for it explicitly, or use an isolating
provider.
