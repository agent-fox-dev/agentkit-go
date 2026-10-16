# harness

Another coding harness — Claude Code, Pi, or any MCP client — implementing
one task of a specification package inside an AgentKit sandbox, with
`code_mode` and the sandbox's built-in tools as its only tools.

```bash
cd examples/harness && go run .
```

The example is its own Go module, because it imports the nested `sandbox`
module, which the root module does not require.

AgentKit's driver is one way to run its tools; it is not the only one. Here
the embedder — this program — keeps what it must keep, the sandbox, the
guard and the policy, and hands the model loop to a harness. The harness
holds the model credential and talks to this program over MCP on loopback.
The sandbox holds neither.

## What it does

1. **A sandbox** from `local` (the default; **not isolation**, for the
   offline run) or `sandbox/podman` (`--podman`, a hardened container from
   `--image`, no network unless `--network unrestricted`).
2. **The workspace** from `--repo`, a public https repository the
   provider's init step clones inside the sandbox (`sandbox.GitRepo`,
   podman only); or from `--dir`, a host checkout moved in over rsync with
   `.git` left behind, which is how a private repository goes in without its
   credential; or, with neither, the sample project: a Go module with one
   stub function and a spec package whose one task asks for it.
3. **`specr` inside the sandbox**, over the proxy `execute` tool, under the
   guard like every other call: `specr <spec> --task N --max-tokens 10000`
   renders the package scoped to the task, as af-impl's "one task at a time"
   step reads it. `--task 0` renders the whole package first and takes the
   task its `next` entry names. The budget matters: the shell tool keeps the
   last 50 KB of a command's output, and a cut envelope is no envelope.
4. **The baseline**: the spec's own `test_commands` (`linter`, then
   `all_tests`, read from the package's `tasks.json` through the proxy
   `read_file`), run on the unchanged tree. A red baseline is a fact the
   prompt carries, not a blocker.
5. **The MCP server**: `code_mode` bound to all eleven proxy tools, and the
   eleven tools themselves, on a free loopback port behind a random bearer
   token. Every call — a direct one from the harness, or a nested one from a
   script — goes through argument preparation and `guard.Restricted`
   (`--allow`, default `go,gofmt,git,specr`) before its handler.
6. **The harness** (`--harness`), with the af-impl rules and the served
   tools' guidelines as its appended system prompt and the task — the
   baseline and specr's rendering — as its prompt:
   - `claude`: Claude Code headless. `--tools ""` turns its own tools off,
     `--strict-mcp-config --mcp-config <json>` makes this server its only
     MCP server, `--allowedTools mcp__agentkit` pre-approves the server's
     tools, `--permission-mode dontAsk` denies whatever would still prompt,
     `--output-format json` gives the final text, the cost and the turns.
   - `pi`: Pi in print mode, from a directory holding a `.pi/mcp.json` that
     names this server with `exposure: direct`, so the tools are declared
     to the model rather than reached only through Pi's own code mode;
     `--tools 'mcp__agentkit__*'` keeps this server's tools alone. Pi reads a
     project's MCP file only once the project is trusted. Verified against
     Pi's documentation, not run here.
   - `scripted` (the default): the stand-in, and the proof that the server
     serves any MCP client. AgentKit's own driver imports the server's tools
     through AgentKit's MCP client (`mcp.Pool`), as another embedder's
     would, and `provider/faux` replays one `code_mode` call that implements
     the sample task the af-impl way: the test first, and red; the
     implementation; the test green; the linter; the task's state.
7. **The gate**: the checks again, and af-impl's verdict — `pass`, `pass,
   baseline was failing`, `regressed`, `still failing`, or `gate failed`
   when a check could not run (the guard refused its command, say). Only the
   first two land.
8. **Export** of the workspace to `--out` (a temporary directory by
   default), `.git` left behind. Landing the work — a branch, a commit, a
   pull request — is the embedder's, on the host, where the credential is.

The default run prints the account to stdout and every tool call to stderr:

```
sandbox … on local (no isolation): agent (devel), network unrestricted
synced the sample project: 7 files, 3297 bytes
serving code_mode over 11 proxy tools (its description is 7672 bytes) and the 11 tools themselves
specr: rendered 01_greeting scoped to task 1 of 1 (prd, requirements, test_spec, tasks, ~465 tokens)
baseline:
  linter `gofmt -l .`: ok
  all_tests `go test ./...`: ok
MCP server at http://127.0.0.1:…/mcp
starting harness scripted on task 1 of .specs/01_greeting (Implement Greet): …
    run      write_file content=package greeter … path=greeter_test.go
    run      execute command=go test ./... timeout_s=600
    error    execute: command_exit: exit 1
    run      edit_file edits=… path=greeter.go
    run      execute command=go test ./... timeout_s=600
    …
verdict: pass
exported the workspace to …/out: .specs, README.md, go.mod, greeter.go, greeter_test.go
```

## The real run: `--podman`

The sandbox needs an image with `specr` beside the agent. `Containerfile`
builds `specr` from a clone of the spec repository (`--build-arg
SPEC_VERSION=<branch or tag>`, default `main`) and adds it to the sandbox
image, which holds the agent at `sandbox.AgentPath`:

```bash
# from the repository root: the sandbox image, at this checkout's version
make build-sandbox-container
cd examples/harness
podman build -t agentkit-harness:latest -f Containerfile .
go run . --podman --image agentkit-harness:latest \
	--repo https://github.com/agent-fox-dev/forked-repo \
   --spec 1 --task 1 --harness claude
```

The host and the agent must be the same AgentKit version — `Sandbox.Tools`
checks the definitions' digest — so a checkout builds its own sandbox image
and a release passes
`--build-arg SANDBOX_IMAGE=quay.io/agentfox/agentkit-sandbox:X.Y.Z`.

The container has a read-only root and a tmpfs `/tmp`; the program points
`GOPATH`, `GOCACHE` and `GOMODCACHE` under `/tmp` through `Spec.Env` (podman
drops the image's own `ENV`) and gives the sandbox 4 GiB, which the build
cache counts against. Under the default `--network none`, `go` cannot
download modules or a toolchain: vendor them, or pass
`--network unrestricted`. Under `none`, `guard.AllowAll` would be a
reasonable guard too, because the container is then the boundary; the
example keeps `guard.Restricted` for both providers.

`--repo` takes a public https repository; a private one is cloned on the host
and passed as `--dir`. `local` has no init step and refuses `--repo`.

## What the harness is told

The appended system prompt is `afImplRules` in `prompt.go` — agent-fox's
`af-impl` skill condensed to one task: scope, orient, test first, implement,
check narrowly then run the spec's checks as the gate, update the
documentation, never edit the spec except the task's state, never commit or
push — followed by the allowed programs and the served tools' own
`PromptGuidelines`, assembled with `prompt.Build` so they follow the tools to
another driver exactly as they would in AgentKit's own.

The prompt names the task, the package's path in the workspace, the baseline
as measured, and specr's rendering. The host does what the skill's other
steps do around the task: it measures before and after, and it lands.

## The code an embedder copies

A harness is its own driver, so what it needs from AgentKit is the pipeline
that runs one tool call, with no model: `agentkit.NewRunner`. A `Runner` runs a
call through preparation, the guard, the handler and its events, and attaches
the nested caller a `code_mode` script's calls go through — the same steps a
call from AgentKit's own model goes through, in the same place. `buildRunner`
in `main.go` is the whole of it:

```go
cm, _, err := codemode.New(proxies, codemode.Options{}) // one tool over the proxies
runner, err := agentkit.NewRunner(agentkit.Toolset{
	Tools:  append([]core.Tool{cm}, proxies...),
	Guard:  guard, // sees every call, direct or from a script
	After:  after, // sees every call that ran
	Events: events,
})
res := runner.Call(ctx, name, input) // a core.ToolResult; never a Go error
```

`NewRunner` refuses what `agentkit.New` refuses — a reachable shell tool with no
`Guard` among it — so the program cannot serve an unguarded shell by omission.
`Runner.Call` takes the tool's name and the input bytes and returns a result
for every outcome: `unknown_tool`, `invalid_arguments`, `blocked_by_policy`,
`panic`, `handler_error`, `aborted`. `runner.Tools()` is the served set, which
the system prompt and the MCP server both read.

`server.go` is the MCP server around it (`sdk.NewServer`,
`sdk.NewStreamableHTTPHandler` from the MCP Go SDK the `mcp` package already
depends on) with one MCP tool per served tool — the tool's own name,
description and input schema, so the harness's model reads the definition
AgentKit's driver would send, and `core.ToolResult.LLMText()` as the result
text, so it reads the result the same way too. Its handler is one line:
`runner.Call`.

The log and the counts come from the toolset's hooks, not from the pipeline: a
`Guard` that wraps the configured one logs each call as `run` or `blocked` and
counts the blocks, `After` logs a call that ran and failed, and `Events` counts
every call from its start event. A call that never reaches the guard or `After`
— an unknown tool, arguments that fail validation — is counted and has no line.

What the runner leaves to the harness: a transcript, a stop condition and the
terminate vote. A harness has its own loop to end, and its own event stream to
watch.

## Flags

| Flag | Default | Effect |
|---|---|---|
| `--podman` | off | `sandbox/podman` instead of `local` |
| `--image` | `sandbox.DefaultImage()` | the sandbox image for `--podman`; the default has no Go and no `specr` |
| `--network` | `none` | the sandbox's network for `--podman`: `none` or `unrestricted` |
| `--repo`, `--ref` | | a public https repository the sandbox clones, and its branch or tag |
| `--dir` | | a host checkout to sync instead of `--repo` |
| `--spec` | the sample's | the package, as `specr` takes it: a path, a number or a name |
| `--task` | `0` | the task; `0` means the next task not done |
| `--harness` | `scripted` | `claude`, `pi` or `scripted` |
| `--model` | the harness's | `--model` for `claude` or `pi` |
| `--allow` | `go,gofmt,git,specr` | the programs the sandbox's shell may run |
| `--out` | a temporary directory | where the workspace is exported |
| `--max-turns` | `40` | the harness's turn bound |
| `--spec-tokens` | `10000` | `specr --max-tokens` |

See [`docs/configuration.md`](../../docs/configuration.md) for `sandbox.Spec`
and the providers, [`docs/architecture.md`](../../docs/architecture.md) for
code mode and nested calls, and the spec repository's `docs/specr.md` for
`specr`'s envelope.
