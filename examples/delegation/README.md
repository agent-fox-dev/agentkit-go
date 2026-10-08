# delegation — an orchestrator that hands work to named specialists

Delegation lets one agent call other agents as tools. The orchestrator sees
each specialist as a tool name; calling it builds a **fresh child agent** with
its own system prompt, its own tool scope, its own stop policy and a slice of
the parent's budget, runs it on the prompt the orchestrator wrote, and returns
the child's final text.

It matters for two reasons: **least privilege** (the orchestrator here has no
file access at all; only the `researcher` can read, and only two tools) and
**cost** (a child starts with empty history, so the parent's whole transcript
is not re-sent to every specialist). The price is that the orchestrator must
write self-contained prompts.

## Run it

Needs a credential for the model's vendor (default `anthropic/claude-sonnet-5`).
Run it from the directory you want researched — the researcher's workspace is
the current directory:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/delegation "Which Go files in this directory define the agent loop, and what does each do?"
```

No flags. With no argument it uses a default task that needs both
specialists. `AGENTKIT_MODEL` overrides the model, for parent and children.

## What you'll see

Delegations on stderr as they happen, the orchestrator's answer on stdout,
then whole-tree usage. The shape:

```
  → delegating to researcher
  → delegating to researcher
  ← researcher: 4 turns, agent.go defines Agent and its Run/Stream …
  ← researcher: 3 turns, loop.go holds the turn loop …
  → delegating to summarizer
  ← summarizer: 1 turns, The agent loop lives in …
<final answer>
[claude-sonnet-5 · 3 turns · in <n> / out <n> tokens · cached <n> · $<cost>]
```

Without a credential:

```
error: no credential for vendor "anthropic": set one of ANTHROPIC_API_KEY, ...
```

For a keyless demonstration of three parallel delegations, see section 7 of
[`agentdemo`](../agentdemo).

## Walkthrough

The numbered comments in `run()`:

- **One budget for the tree** — `maxBudgetUSD` is both the parent's
  `stop.OverBudget` and the base for each specialist's `BudgetFraction`. A
  child's spend lands in the parent's usage as the tool returns.
- **`cfg.ParallelTools = true`** makes two delegations in one turn actually
  concurrent.
- **The system prompt names the specialists** and forbids answering directly.
  A model that is merely *able* to delegate mostly will not.
- **`subagent.NewRegistry()` + `registry.Register(subagent.Definition{…})`**:
  - `researcher` gets `Tools: builtins` (all of `tools.All`) but
    `ToolPolicy: core.ToolPolicy{ToolNames: []string{"read_file", "search_files"}}`.
    The policy, not the slice, decides what exists — and since `execute` is
    filtered out, this child needs no shell guard.
  - `summarizer` gets `ToolPolicy{NoTools: core.NoToolsAll}`: no tools at all,
    including any added later.
  - `BudgetFraction` is a config field (0.30, 0.20) — a fraction of what the
    parent has left when the call is made.
- **`registry.Tools(parent, maxBudgetUSD)`** returns one delegation tool per
  definition, each backed by a factory. Register them on the parent.
- **`summarize`** decodes a child's `{"result": …, "turns": …}`. A failed
  child comes back as an error *result*, not a failed parent run, so the
  orchestrator can recover.

To fan out from Go code instead of letting the model decide — one child per
item, concurrently, results in input order — use `subagent.RunParallel`.

## Gotchas

- Never share one child agent across calls: the second parallel call would get
  `ErrBusy`. The factory is the point.
- Children inherit the parent's providers, credentials and tracer, and its
  model unless the definition names its own (a cheap child can serve an
  expensive orchestrator).
- Duplicate specialist names are a `Register` error, because the name is the
  tool surface the model sees.

## Related

Packages: `subagent` (`Definition`, `Registry`, `Tool`, `RunParallel`),
`core` (`ToolPolicy`, `NoToolsAll`), `tools`, `stop`.
