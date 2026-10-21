# codingagent — built-in tools, a workspace root, and a shell guard

A coding agent is a model plus tools that read, search, edit and run things in
a repository. AgentKit ships those tools (`tools.For`) and two boundaries
around them, and this example wires up both:

- **The workspace root** is the only directory the file tools can reach. Every
  path is resolved against it, symlinks included, before every read and again
  before every write — so `../../etc/passwd` is refused by the tool, not by a
  prompt asking nicely.
- **The execute guard.** `tools.For` includes `execute` and `run_command`.
  Passing either of them in `Config.Toolset.Tools` with a nil `Config.Toolset.Guard` makes
  `agentkit.New` return an error wrapping `core.ErrUnguardedExecute`, so the
  agent is never built and no request is sent. You must supply an
  interceptor — here `guard.Restricted` — or pass `guard.AllowAll` to say in
  code that an unrestricted shell is intended. Shell tools are *not*
  contained to the workspace; the guard is what stands in front of them.

## Run it

Three environment variables choose what runs the model, and the program reads
them itself and passes them to `agentkit.Config` unchanged. Each provider needs
only its own credential or server (the variables are in
[`examples/README.md`](../README.md#configuring-an-application)):

```bash
# Anthropic, the default provider
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/codingagent --dir . "Which files define the tool policy?"

# OpenRouter
export OPENROUTER_API_KEY=sk-or-...
AGENTKIT_PROVIDER=openrouter go run ./examples/codingagent --dir . "Which files define the tool policy?"

# Ollama, on a server that has pulled the tier's model (ollama pull <model>)
AGENTKIT_PROVIDER=ollama go run ./examples/codingagent --dir . "Which files define the tool policy?"

# A tier other than STANDARD, and an effort of your own
AGENTKIT_MODEL=ADVANCED AGENTKIT_EFFORT=high go run ./examples/codingagent --dir ./tools "Summarise this package."
```

| Variable | Default | Meaning |
|---|---|---|
| `AGENTKIT_PROVIDER` | empty, which is `anthropic` | `anthropic`, `openrouter` or `ollama` (`Config.Provider`). |
| `AGENTKIT_MODEL` | `STANDARD` | A tier (`SIMPLE`, `STANDARD`, `ADVANCED`) or the provider's own model id (`Config.Model`). |
| `AGENTKIT_EFFORT` | empty, which is the tier's effort | `low`, `medium`, `high`, `xhigh` or `max` (`Config.Effort`). |

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Workspace root; the file tools cannot reach outside it. |

Everything after the flags is the task.

## What you'll see

The workspace on stderr, then one line per authorised or blocked tool call,
the model's text on stdout, and a summary of what ran and what it used:

```
workspace: /path/to/agentkit-go
  allow   search_files pattern=ToolPolicy …
  run     search_files
  result  {"ok":true,"data":…
  blocked execute: …
...
[ollama · qwen3.8:27b · effort high · 6 turns · stop end_turn · in <n> / out <n> tokens · $0.00000]
```

The summary names the provider, the model id and the effort that
`Agent.Resolution()` reports. The cost is `cost unknown` when the run's usage is
flagged `Unpriced`: a model the catalog does not list has no price, and a zero is
then not a cost.

A configuration that cannot run stops before any request, with the reason and
exit status 1: the workspace, then an `error:` line.

```
workspace: /path/to/agentkit-go
error: anthropic: missing credentials: set ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or ANTHROPIC_OAUTH_TOKEN, ...
error: openrouter: OPENROUTER_API_KEY is not set (set it in the environment or in Options.Env)
error: agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama
```

## Walkthrough

The numbered comments in `run()` are the walkthrough:

1. `workspace.Open(*dir, workspace.Options{})` — the containment boundary, and
   where ignore rules are configured, once.
2. `tools.For(ws, tools.Options{})` — the default set: `read_file`,
   `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`,
   `file_outline`, `find_symbol`, `find_references`, `execute`, `run_command`.
   There is no network tool.
3. `guard.Restricted(guard.Options{AllowedPrograms: allowedPrograms})` — an
   allowlist of program names (`go`, `git`, `ls`, `cat`, `rg`) plus rejection
   of shell operators (pipes, `;`, `&&`, redirection, substitution). A refusal
   goes back to the model as a blocked tool result; `TerminateOnBlock: true`
   ends the run instead.
4. `logged` *wraps* the policy to log each decision, and is passed as
   `Config.Toolset.Guard`. The wrapper reports; the policy still decides. This is the
   pattern for adding side effects (logging, metrics) to any interceptor.
5. `agentkit.New(agentkit.Config{Provider, Model, Effort, System, Toolset:
   {Tools, Guard}, MaxTurns: 20, MaxCostUSD: 2.00})` — `selection()` reads
   `AGENTKIT_PROVIDER`, `AGENTKIT_MODEL` and `AGENTKIT_EFFORT` and `New`
   resolves them through the catalog, builds the provider's client from the
   environment and fails here, before any request: an unknown provider or effort
   by name, a missing credential by the variable to set. The run stops at 20 turns
   (`core.RunStopMaxTurns`) or once $2.00 has been spent
   (`core.RunStopBudgetExceeded`): turns catch cheap loops, budget catches
   expensive turns.
6. `agent.Stream` and the event loop: `ToolExecutionStartEvent` fires after
   the guard allowed the call and before the handler runs.
7. `stream.RunResult()` carries the error the loop ended with. A limit stop
   (`core.ErrMaxTurns`, `core.ErrBudgetExceeded`) is reported in the summary
   line rather than as a failure.

## Gotchas

- `guard.Restricted` is a **floor, not a sandbox**: `go` alone can run
  arbitrary code through a test file or a generator. Replace it with a policy
  that knows your workload rather than widening the allowlist.
- The tools are fixed at construction: they all go in `Config.Toolset.Tools`, and
  the guard check happens in `agentkit.New`, before any run. There is no way
  to add a tool to an agent afterwards.
- `summarize` sorts argument keys because JSON object order is not stable.
- The example reads three variables and knows nothing about a provider's own:
  `OLLAMA_HOST` and the credentials are read by the provider `New` builds, and
  only the chosen provider's are ever consulted.

## Related

Packages: `agentkit` (`Config`, `New`), `workspace` (`Open`), `tools` (`For`),
`guard` (`Restricted`, `AllowAll`), `core` (`BeforeToolCallContext`,
`BeforeToolCallDecision`, `ErrUnguardedExecute`, `ErrMaxTurns`,
`ErrBudgetExceeded`). The optional `code_search` index is the
nested `codesearch` module (`tools.Options.Index`).
