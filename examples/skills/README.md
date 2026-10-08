# skills — prompt material authored by users and repositories

A **skill** is a directory holding a `skill.toml` manifest (name, description,
optional archetypes, tools, a subagent step) and a `prompt.md` with
instructions. A **context file** (`AGENTS.override.md`, `AGENTS.md`,
`CLAUDE.md`) is standing instructions for a directory. Both let people outside
your code shape what the model is told — which is the feature, and also the
risk: a cloned repository's skills and context files are text a stranger
wrote, landing in your system prompt.

AgentKit's `skills` package handles both with three ideas this example makes
visible:

- **Tiers and a trust gate.** Skills come from the user's home
  (`~/.nightshift/skills`), the project (`<repo>/.nightshift/skills`) and the
  SDK's built-in `_skills/`. Project material is read only when
  `TrustProject` is true — the gate applies at *discovery*, so untrusted bytes
  never enter the process. User skills always beat project skills of the same
  name.
- **Progressive disclosure.** The prompt carries only each skill's name,
  description and path (escaped, so a hostile description cannot break out).
  The model reads a skill's body with a file tool only when it decides the
  skill applies.
- **Seams for tools and subagents.** Skills may contribute tools (merged with
  explicit `overrides`, or refused on conflict), be activated mid-session
  without invalidating the provider's prompt cache, and declare a
  pre-analysis subagent step the host runs.

## Run it

Sections 1–10 need **no API key**: the program writes a throwaway home and
checkout into a temp directory, points `HOME` at it, and runs the real
discovery over it.

```bash
go run ./examples/skills
```

Section 11 makes one real run in which the model reads a skill file. It needs a
credential for the model's vendor (default `anthropic/claude-sonnet-5`); without
one it is skipped and the program still exits 0.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/skills "Draft the release-notes entry for the new --dir flag."
```

## What you'll see

```
2. the trust gate: TrustProject decides whether the project directory is READ
  TrustProject=false -> code-review(builtin) commit-message(user) release-notes(user)
  TrustProject=true  -> changelog(project) code-review(builtin) commit-message(user) db-migration(project) ...

3. tiers, precedence, and what discovery skipped
  ! repo/service/.nightshift/skills/broken: error: skill not loaded: skills: manifest has no description
  ! repo/service/.nightshift/skills/linked: error: symlinked skill directory rejected (REQ-SEC-06)
  ! repo/service/.nightshift/skills/release-notes: warning: skill "release-notes" is shadowed by the user skill at ...

5. the block: metadata only, the tool it names, and the escaping
  ...
  2296 bytes of prompt for 7 skills whose bodies are 9597 bytes.

9. activating a skill mid-session without wiping the cache
  after Mark, the provider splits the request's tools:
    immediate read_file execute run_sql
    deferred  plan_migration

11. one real run (needs a credential)
  skipped: no credential for vendor "anthropic": ...
```

## Walkthrough

The wiring an application writes is section 7 of `run()`:

```go
cfg.TrustProject = true                                   // the one place trust is stated
agent, _ := agentkit.NewAgent(cfg)                         // register tools after
skillCfg := skills.ConfigFor(cfg, work, skills.BuiltinDir())
session  := agent.LoadSkills(skills.Discover(skillCfg), "coder", task, skillCfg) // selects + audits
ctxFiles, _ := skills.DiscoverContext(skillCfg)
agent.SetPromptBlocks(prompt.SkillBlocks(session, ctxFiles, agent.Tools()))
```

Pass `agent.Tools()` — the set *after* the tool policy resolved — so the block
names a file-reading tool the model actually has, and is omitted entirely when
there is none. `cfg.Hooks.OnAudit` receives a `core.AuditSkillsLoaded` event
naming every selected skill.

The other sections show the pieces individually: `skills.Discover` and
`reg.Diagnostics()` (2–3), `reg.LoadForSession(archetype, task, cfg)` (4),
`skills.Assemble` and `skills.FileReadTool` (5), `skills.DiscoverContext` (6),
`skill.Contribution` + `skills.MergeTools` and `*skills.SkillConflictError` (8),
`skills.Activate` + `act.Mark` + `provider.SplitDeferredTools` (9), and
`skills.RunSubagents` with a `skills.SubagentRunnerFunc` (10). The fixture tree
is `writeTree`.

## Gotchas

- `TrustProject` defaults to **false**. Say nothing and you get no project
  skills and no project context files.
- Project trust covers the repository, not its parents: an `AGENTS.md` above
  the trust boundary is not loaded (section 6).
- Escaping is containment, not trust. A trusted skill can still say persuasive
  things; the gate is the control.
- The file tools are contained to the workspace. If the workspace is the
  checkout, a user-tier skill under `$HOME` is unreadable — this example uses
  the temp root as the workspace for that reason.
- Manifests are decoded leniently: unknown keys are warnings, only a missing
  description, a symlinked directory or file, an absent `prompt.md` or a
  forbidden import stops a skill loading.
- Setting `HOME` is this example's hermeticity trick only; a real application
  sets no variables (`ConfigFor` uses `os.UserHomeDir`).

## Related

Packages: `skills`, `prompt` (`SkillBlocks`, `Build`), the root `agentkit`
package (`LoadSkills`, `SetPromptBlocks`), `provider` (`SplitDeferredTools`).
`skill.toml` keys are listed in
[`docs/configuration.md`](../../docs/configuration.md).
