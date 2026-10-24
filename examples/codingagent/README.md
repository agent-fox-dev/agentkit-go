# codingagent: built-in tools, a workspace root and a shell guard

A coding agent is a model plus tools that read, search, edit and run things in
a repository. This example is a minimal one: AgentKit's built-in tools
(`tools.For`) over a directory you choose, a guard in front of the shell, and a
live event stream. It shows the two boundaries every such agent needs: the file
tools cannot reach outside the workspace root, and the shell tools run only
what the guard allows. It calls a real model, so it needs a provider and its
credential.

## Run it

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/codingagent --dir . "Which files define the tool policy?"

AGENTKIT_PROVIDER=ollama AGENTKIT_MODEL=ADVANCED AGENTKIT_EFFORT=high \
  go run ./examples/codingagent --dir ./tools "Summarise this package."
```

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Workspace root; the file tools cannot reach outside it. |

Everything after the flags is the task. With none, the agent lists the Go files
and summarises the package.

`AGENTKIT_PROVIDER` (`anthropic`, `openrouter` or `ollama`), `AGENTKIT_MODEL`
(a tier or a model id, default `STANDARD`) and `AGENTKIT_EFFORT` choose the
model. Only the examples read these variables, and they pass them to
`agentkit.Config` unchanged. Each provider reads its own credential:
`ANTHROPIC_API_KEY` (or another Anthropic credential), `OPENROUTER_API_KEY`, or
`OLLAMA_HOST`. See [choosing a provider](../../docs/guide.md#choosing-a-provider).

The workspace and each tool call go to stderr, the model's text to stdout:

```
workspace: /path/to/agentkit-go
  allow   search_files pattern=ToolPolicy …
  run     search_files
  result  …
  blocked execute: …
...
[anthropic · <model id> · effort <effort> · 6 turns · stop end_turn · in <n> / out <n> tokens · $<cost>]
```

The summary shows `cost unknown` for a model the catalog has no price for. A
configuration that cannot run stops before any request, with exit status 1:

```
error: openrouter: OPENROUTER_API_KEY is not set (set it in the environment or in Options.Env)
error: agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama
```

## What it shows

- `workspace.Open` is the containment boundary: every path is resolved against
  the root, symlinks included, so `../../etc/passwd` is refused by the tool.
- `tools.For` includes `execute` and `run_command`; with no `Toolset.Guard`,
  `agentkit.New` refuses to build the agent (`core.ErrUnguardedExecute`).
- `guard.Restricted` allows a short list of programs (`go`, `git`, `ls`, `cat`,
  `rg`) and rejects shell operators. A refusal goes back to the model as a
  blocked tool result.
- Wrapping a guard adds a side effect (here, a log line) without changing the
  decision.
- `MaxTurns` and `MaxCostUSD` bound the run; hitting one is reported in the
  summary, not as a failure.

## The code that matters

```go
ws, err := workspace.Open(*dir, workspace.Options{})
built, err := tools.For(ws, tools.Options{})
policy := guard.Restricted(guard.Options{AllowedPrograms: []string{"go", "git", "ls", "cat", "rg"}})

agent, err := agentkit.New(agentkit.Config{
	Provider:   provider, // from AGENTKIT_PROVIDER; empty is anthropic
	Model:      model,    // from AGENTKIT_MODEL; a tier or a model id
	Effort:     effort,
	System:     "You are a careful coding assistant. …",
	Toolset:    agentkit.Toolset{Tools: built, Guard: policy},
	MaxTurns:   20,
	MaxCostUSD: 2.00,
})
stream, err := agent.Stream(ctx, task)
```

`guard.Restricted` is a floor, not a sandbox: `go` alone can run arbitrary code
through a test file or a generator. Replace it with a policy that knows your
workload rather than widening the allowlist, or run the tools in a
[sandbox](../sandbox).

## Further reading

- [Examples index](../README.md)
- [User guide](../../docs/guide.md): [the workspace and the built-in tools](../../docs/guide.md#the-workspace-and-the-built-in-tools), [guarding the shell](../../docs/guide.md#guarding-the-shell), [bounding and watching a run](../../docs/guide.md#bounding-and-watching-a-run)
- [Configuration](../../docs/configuration.md): `agentkit.Config`, `guard.Options`, `tools.Options`
