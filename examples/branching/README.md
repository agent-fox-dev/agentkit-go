# branching

A session log is append-only. Nothing in it is ever rewritten or deleted. So
how does a user "go back three messages and try something else"? **By
branching.** Every entry records its parent, so the file is a tree. The
conversation the model sees is one root-to-leaf path through it, called a
branch. Rewinding moves the head to an earlier entry, and the next append
starts a second child there. Both branches stay in the same file, and either
one can be resumed later.

This is what rewind, edit-and-retry and "try another approach" are built on.
The [`session`](../session) example shows the straight-line case of resuming
a log. This one shows the tree.

The example runs with **no API key and no network**. The real loop and the
real `session.Store` run against a scripted
[`provider/faux`](../../provider/faux) provider. At each step the program
prints what the model was actually sent, so you can see which branch it saw.

```bash
go run ./examples/branching
go test ./examples/branching/
```

## What it shows

```
── 2. rewind to the first answer and take another path ───────────────────
  what the model was sent on the new branch:
    user      Which database for an append-only event log?
    assistant Postgres, with the events table partitioned by month.
    user      [The conversation was rewound. A summary of the abandoned branch follows.]  We c…
    user      Actually, what would this look like in ClickHouse?

  e02 user      Which database for an append-only event log?
  e03 assistant Postgres, with the events table partitioned by month.
  ├─ e04 user      Show me the schema.
     e05 assistant CREATE TABLE events (id bigint, at timestamptz, body j…
     e06 user      How do we expire old events?
     e07 assistant Use pg_partman to create partitions and drop ones olde…
  └─ e08 summary   We chose Postgres, drafted a partitioned events table,…
     e09 user      Actually, what would this look like in ClickHouse?
     e10 assistant ClickHouse: a MergeTree table ordered by time, with a …   ← head

── 3. both branches are in one file ──────────────────────────────────────
  session.Load: 9 entries, leaves [e07 e10], head e10 (the last entry written)
  branch to e07: e02 → e03 → e04 → e05 → e06 → e07
  branch to e10: e02 → e03 → e08 → e09 → e10

── 4. a later process resumes the ORIGINAL branch ────────────────────────
  OpenOrCreate folded the head branch, ending at e10
  refused, as it should be: agentkit: the session store's head is "e07" but this Resume was folded from "e10"
  ...
     e11 user      How do we schedule that?
     e12 assistant Run pg_partman's maintenance from pg_cron every night.   ← head
```

1. A three-turn conversation is recorded to `session.jsonl`.
2. The user rewinds to the first answer (`e03`) and asks about ClickHouse
   instead. The new branch opens with a **branch summary** of the abandoned
   path. The model is told what was tried without being sent those turns.
3. `session.Load` reads the file: two leaves, one per branch.
4. A later process resumes the *original* branch and continues it. The
   ClickHouse branch is still in the file, untouched.

## The code an application copies

**Rewind and branch.** You choose the fork point, for example the message the
user clicked "edit" or "retry" on:

```go
store.ForkFrom(forkPoint)  // move the head; nothing is rewritten

// Optional, but usually right: tell the model what the abandoned branch did.
store.Append(session.NewBranchSummaryEntry(summary, oldLeaf, forkPoint))

// Fold the new branch and build a NEW agent from it.
resume, err := session.FoldLeaf(store, store.Head())
agent, err := agentkit.NewAgentFromSession(cfg /* SessionStore: store */, resume, resolve)
```

The branch summary is folded into the model's context as a user message,
wrapped by `session.RenderBranchSummary`:

```
[The conversation was rewound. A summary of the abandoned branch follows.]

<your summary>
```

That wrapper is part of the model-visible format and is pinned by a test.
**You write the summary text.** The SDK does not generate one, because that
would be a second model call with its own failure modes. Write it yourself,
ask the model for one, or skip the entry if the abandoned path does not
matter.

**Resume a particular branch.** `session.OpenOrCreate` and `session.Load`
follow the **head**, which is the last entry *written to the file*, whichever
branch it is on. To resume a different branch, call `FoldLeaf`:

```go
store, _, err := session.OpenOrCreate(path, session.Options{})
resume, err := session.FoldLeaf(store, leafID) // moves the head AND folds that branch
agent, err := agentkit.NewAgentFromSession(cfg, resume, resolve)
```

**Inspect the tree** without opening it for writing:

```go
loaded, _ := session.Load(path)
loaded.Leaves()          // every branch tip, in file order
loaded.Branch(leafID)    // the root→leaf path
loaded.Entries()         // every entry; ParentID gives the tree
loaded.Repairs           // what the loader fixed in a damaged file, never silent
```

## Gotchas

- **Build a new agent per branch.** An agent holds the conversation it was
  built from. After `ForkFrom`, the old agent still holds the abandoned
  branch, and its next `Run` would append that context under the new head.
  Fold and construct again.
- **`ForkFrom` alone is not enough.** A `Resume` folded before the fork
  describes the old branch. `NewAgentFromSession` refuses a `Resume` whose
  leaf is not the store's head, and section 4 shows that error. Use
  `FoldLeaf`, which moves the head and folds the branch together.
- **The head after a reload is the last entry in file order.** It is not
  stored anywhere. If your UI lets users switch branches, keep the leaf id
  they were on (in your database, or as a `custom_message` entry) and
  `FoldLeaf` to it on open.
- **`ForkFrom(core.NullLeaf)` starts a new root.** The next entry has no
  parent, and the branch begins empty.
- **Entry ids are random by default.** `session.Options.NewID` and
  `Options.Now` exist so tests (and this example) can produce stable output.
  Production code leaves them nil.
- **Branching is not compaction.** A branch summary stands in for turns the
  model will *not* continue. A compaction summary stands in for turns it
  *has* had and that no longer fit. See [`compaction`](../compaction).

## See also

- [`session`](../../session): `Store.ForkFrom`, `FoldLeaf`, `Load`,
  `NewBranchSummaryEntry`, `RenderBranchSummary`, `Fold`, `Resume`
- [`core.SessionStore`](../../core/history.go): the interface, if you store
  sessions somewhere other than a JSONL file
- [`session`](../session): the straight-line resume this builds on
