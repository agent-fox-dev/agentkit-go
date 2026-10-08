# session — a conversation that outlives the process

An agent's memory is its transcript. If the transcript lives only in the
process, a restart, a deploy or a `kill -9` loses it. AgentKit's durable unit
is an **append-only JSONL log**: every message is appended as it happens (not
saved at the end), so a run that dies mid-turn still leaves the turns before
it on disk.

Resuming is not "load the messages and keep going". The log is **folded**
back into the model, API, thinking level and branch it was produced with, and
the agent is *constructed* from them with `NewAgentFromSession`. A resumed run
that quietly reverted to a default model or reasoning level would be a
regression no single-process test catches; the constructor makes it
impossible.

## Run it

Needs a credential for the model's vendor (default `anthropic/claude-sonnet-5`).
Run it twice — the second process answers from the first one's transcript:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/session "Pick a number between 1 and 100 and tell me what it is."
go run ./examples/session "What number did you pick?"
```

| Flag | Default | Meaning |
|---|---|---|
| `--session` | `$TMPDIR/agentkit-example-session.jsonl` | Path to the session log. |
| `--reset` | `false` | Delete the log first and start a new session. |

With no prompt, the first run picks a number and later runs ask for it back.
`cat` the log between runs: one JSON entry per line.

## What you'll see

First run, then a second run (stderr lines shown; the answer goes to stdout):

```
session: /tmp/agentkit-example-session.jsonl (cat it: it is JSONL, one entry per line)
first run: new session
...
session: /tmp/agentkit-example-session.jsonl (cat it: it is JSONL, one entry per line)
resuming: 2 message(s) recovered from the log
  provenance: provider="anthropic" api="anthropic-messages" model="claude-sonnet-5" thinking="…"
```

Without a credential it opens (and creates) the log, then stops:

```
session: /tmp/agentkit-example-session.jsonl (cat it: it is JSONL, one entry per line)
first run: new session
error: no credential for vendor "anthropic": set one of ANTHROPIC_API_KEY, ...
```

For a keyless version of the same kill-and-resume, see section 6 of
[`agentdemo`](../agentdemo).

## Walkthrough

1. **`session.OpenOrCreate(path, session.Options{Durability: session.DurabilityPerEntry})`**
   returns the store to keep writing to *and* the fold (`resume`) of what is
   already there. `DurabilityPerEntry` fsyncs each entry (survives machine
   death); the default `DurabilityBuffered` survives process death only.
2. **Repairs are reported.** A log truncated by a crash still loads — the
   loader drops what it cannot parse — and `resume.LoadRepairs` says what was
   dropped (`rep.LostData()`). A deployment that must not resume from a
   damaged transcript can refuse here.
3. **Branch on `len(resume.Messages)`:**
   - empty → `cfg.SessionStore = store; agentkit.NewAgent(cfg)`;
   - non-empty → `agentkit.NewAgentFromSession(cfg, resume, resolveModel)`.
     `NewAgent` with a non-empty store fails with `core.ErrSessionNotEmpty`,
     on purpose.
4. **`resolveModel(vendor, api, modelID)`** maps the recovered provenance
   *triple* back to a `*core.Model`. All three fields matter: "same model" is
   computed over the triple, and recovering only two makes the first resumed
   request look like a model change, which strips signed thinking blocks. The
   log's API wins over the catalog's. It is a callback so an embedder with its
   own model registry can resume without the catalog.

## Gotchas

- On a resume, `AGENTKIT_MODEL` is only a fallback — the log's model wins.
- The log file is created by `OpenOrCreate` even if the run then fails (for
  example on a missing credential).
- One store per running agent; the log is append-only, so do not edit it by
  hand while a process holds it.

## Related

Packages: `session` (`OpenOrCreate`, `Options`, durability levels, repairs),
the root `agentkit` package (`NewAgentFromSession`), `core`
(`ErrSessionNotEmpty`, `SessionStore`), `catalog`.
