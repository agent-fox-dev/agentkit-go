# codingagent — built-in tools, a workspace root, and a shell guard

A coding agent is a model plus tools that read, search, edit and run things in
a repository. AgentKit ships those tools (`tools.All`) and two boundaries
around them, and this example wires up both:

- **The workspace root** is the only directory the file tools can reach. Every
  path is resolved against it, symlinks included, before every read and again
  before every write — so `../../etc/passwd` is refused by the tool, not by a
  prompt asking nicely.
- **The execute guard.** `tools.All` includes `execute` and `run_command`.
  Passing either of them in `Config.Tools` with a nil `Config.Guard` makes
  `agentkit.New` return an error wrapping `core.ErrUnguardedExecute`, so the
  agent is never built and no request is sent. You must supply an
  interceptor — here `guard.Restricted` — or pass `guard.AllowAll` to say in
  code that an unrestricted shell is intended. Shell tools are *not*
  contained to the workspace; the guard is what stands in front of them.

## Run it

Needs a credential for the model's vendor (default `anthropic/claude-sonnet-5`):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/codingagent --dir . "Which files define the tool policy?"
go run ./examples/codingagent --dir ./tools "Summarise this package."
```

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Workspace root; the file tools cannot reach outside it. |

Everything after the flags is the task. `AGENTKIT_MODEL` overrides the model.

## What you'll see

The workspace on stderr, then one line per authorised or blocked tool call,
the model's text on stdout, and a usage summary:

```
workspace: /path/to/agentkit-go
  allow   search_files pattern=ToolPolicy …
  run     search_files
  result  {"ok":true,"data":…
  blocked execute: …
...
[claude-sonnet-5 · 6 turns · stop end_turn · in <n> / out <n> tokens · $<cost>]
```

Without a credential it prints the workspace and stops:

```
workspace: /path/to/agentkit-go
error: anthropic: missing credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, ...
```

## Walkthrough

The numbered comments in `run()` are the walkthrough:

1. `tools.NewWorkspace(*dir)` — the containment boundary.
2. `tools.All(tools.Options{Workspace: ws})` — the default set: `read_file`,
   `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`,
   `file_outline`, `find_symbol`, `find_references`, `execute`, `run_command`.
   There is no network tool.
3. `guard.Restricted(guard.Options{AllowedPrograms: allowedPrograms})` — an
   allowlist of program names (`go`, `git`, `ls`, `cat`, `rg`) plus rejection
   of shell operators (pipes, `;`, `&&`, redirection, substitution). A refusal
   goes back to the model as a blocked tool result; `TerminateOnBlock: true`
   ends the run instead.
4. `logged` *wraps* the policy to log each decision, and is passed as
   `Config.Guard`. The wrapper reports; the policy still decides. This is the
   pattern for adding side effects (logging, metrics) to any interceptor.
5. `agentkit.New(agentkit.Config{Client, Model, System, Tools, Guard,
   MaxTurns: 20, MaxCostUSD: 2.00})` — `Client` comes from
   `anthropic.Resolve(anthropic.OSEnv{})`. The run stops at 20 turns
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
- The tools are fixed at construction: they all go in `Config.Tools`, and
  the guard check happens in `agentkit.New`, before any run. There is no way
  to add a tool to an agent afterwards.
- `summarize` sorts argument keys because JSON object order is not stable.

## Related

Packages: `agentkit` (`Config`, `New`), `tools` (`NewWorkspace`, `All`),
`guard` (`Restricted`, `AllowAll`), `core` (`BeforeToolCallContext`,
`BeforeToolCallDecision`, `ErrUnguardedExecute`, `ErrMaxTurns`,
`ErrBudgetExceeded`), `provider/anthropic` (`Resolve`). The optional `code_search` index is the
nested `codesearch` module (`tools.Options.Index`).
