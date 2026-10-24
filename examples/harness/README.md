# harness: another coding harness working inside an AgentKit sandbox

AgentKit's own agent loop is one way to drive its tools; this example shows
another. Here a separate coding harness (Claude Code, Pi, or any MCP client)
implements one task of a specification package, and its only tools are
`code_mode` and the built-in tools of an AgentKit sandbox, served to it over
MCP on loopback. Your program keeps the sandbox, the shell guard and the
verdict; the harness keeps the model and its credential. The default run is
offline with no API key, no container engine and no harness installed: it uses
`local` (not isolation), a sample project, a stand-in `specr`, and a scripted
stand-in harness.

Contents: [Run it](#run-it) · [What it does](#what-it-does) ·
[The real run](#the-real-run---podman) · [The code that matters](#the-code-that-matters) ·
[Flags](#flags) · [Further reading](#further-reading)

## Run it

The example is its own Go module.

```bash
cd examples/harness && go run .
```

The account of the run goes to stdout and every tool call to stderr:

```
sandbox … on local (no isolation): agent (devel), network unrestricted
filled the workspace from the sample project
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
    …
verdict: pass
exported the workspace to …/out: .specs, README.md, go.mod, greeter.go, greeter_test.go
```

## What it does

1. Creates a sandbox, on `local` or, with `--podman`, in a hardened container.
2. Fills the workspace from `--repo` (a public https repository cloned inside
   the sandbox; podman only), from `--dir` (a host checkout synced in without
   its `.git`, which is how a private repository goes in without its
   credential), or from the sample project.
3. Runs `specr` inside the sandbox, through the guarded `execute` tool, to
   render the spec package scoped to the task.
4. Runs the spec's own checks (`linter`, then `all_tests`, from its
   `tasks.json`) on the unchanged tree as the baseline.
5. Serves `code_mode` and the eleven sandbox tools over MCP on a free loopback
   port behind a random bearer token. Every call, direct or from a script,
   passes `guard.Allow` (`--allow`).
6. Starts the harness with its own tools turned off, this server as its only
   tool source, implementation rules appended to its system prompt
   (`prompt.go`) and the task as its prompt.
7. Runs the checks again and prints a verdict: `pass`,
   `pass, baseline was failing`, `regressed`, `still failing` or `gate failed`.
8. Exports the workspace to `--out`. Committing and opening a pull request is
   left to your program, on the host, where the credential is.

## The real run: `--podman`

The sandbox needs an image with `specr` beside the AgentKit agent;
`Containerfile` builds one on top of the sandbox image:

```bash
# from the repository root: the sandbox image, at this checkout's version
make build-sandbox-container
cd examples/harness
podman build -t agentkit-harness:latest -f Containerfile .
go run . --podman --image agentkit-harness:latest \
	--repo https://github.com/you/project --spec 1 --task 1 --harness claude
```

The host and the sandbox agent must be the same AgentKit version. The
Containerfile's base is `quay.io/agentfox/agentkit-sandbox:latest`, which
`make build-sandbox-container` tags; for a release image, change its `FROM`
line. Under the default `--network none`, `go` cannot download modules: vendor
them, or pass `--network unrestricted`.

With `--harness claude` the program runs Claude Code headless with
`--tools ""`, `--strict-mcp-config --mcp-config <json>` and
`--permission-mode dontAsk`; with `--harness pi` it runs Pi in print mode from
an empty directory holding a `.pi/mcp.json`. Either reads its model credential
from this program's environment.

## The code that matters

```go
sb, proxies, err := sandbox.Open(ctx, provider, spec, src, sandbox.ToolOptions{}) // create, fill, equip
defer sb.Close(ctx)
cm, _, err := codemode.New(proxies, codemode.Options{})
runner, err := agentkit.NewRunner(agentkit.Toolset{
	Tools: append([]core.Tool{cm}, proxies...),
	Guard: guard.Allow("go", "gofmt", "git", "specr"), // sees every call, direct or from a script
})
srv, err := mcpserve.New(runner, mcpserve.Options{Instructions: "…"})
ep, err := srv.ServeLoopback(ctx)
defer ep.Close()
// ep.URL and ep.Token say where a harness connects; ep.ClaudeConfig("agentkit")
// is Claude Code's --mcp-config, and ep.PiConfig("agentkit") is Pi's .pi/mcp.json.
```

`agentkit.NewRunner` runs one tool call at a time with no model, through the
same argument checks and guard as an agent, and refuses a toolset that reaches
a shell tool without a `Guard`. `mcpserve` serves each runner tool under its
own name, description and schema. The example also logs and counts calls with
the toolset's `Guard`, `After` and `Events` hooks (`calls.go`).

## Flags

| Flag | Default | Effect |
|---|---|---|
| `--podman` | off | Use `sandbox/podman` instead of `local`. |
| `--image` | `sandbox.DefaultImage()` | The sandbox image for `--podman`; the default has no Go and no `specr`. |
| `--network` | `none` | The sandbox's network for `--podman`: `none` or `unrestricted`. |
| `--repo`, `--ref` | | A public https repository the sandbox clones (needs `--podman`), and its branch or tag. |
| `--dir` | | A host checkout to sync in instead of `--repo`. |
| `--spec` | the sample's | The spec package, as `specr` takes it: a path, a number or a name. |
| `--task` | `0` | The task; `0` means the next task not done. |
| `--harness` | `scripted` | `claude`, `pi` or `scripted`. |
| `--model` | the harness's | The model for `claude` or `pi`. |
| `--allow` | `go,gofmt,git,specr` | The programs the sandbox's shell may run. |
| `--out` | a temporary directory | Where the workspace is exported. |
| `--max-turns` | `40` | The harness's turn bound. |
| `--spec-tokens` | `10000` | `specr --max-tokens`. |

## Further reading

- [Examples index](../README.md)
- [User guide](../../docs/guide.md): [running tools without a model](../../docs/guide.md#running-tools-without-a-model), [serving tools to another harness over MCP](../../docs/guide.md#serving-tools-to-another-harness-over-mcp), [sandboxes](../../docs/guide.md#sandboxes)
- [Configuration](../../docs/configuration.md): `sandbox.Spec`, the providers, `mcpserve.Options`
- [Architecture](../../docs/architecture.md): code mode, nested calls and MCP serving
