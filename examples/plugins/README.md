# plugins — extending an agent with compiled-in plugins

A plugin is a Go type you compile into your program and register on a
`plugins.Registry`. There is no runtime loader and no `plugin.Open`: plugins
are ordinary module dependencies resolved at build time. What the package adds
is structure around them — four categories, a load order, a disabled list,
manifest discovery, an import lint, and a conformance report you can run in CI.

The four categories:

| Category | Interface | Supplies |
|---|---|---|
| Tool provider | `ToolProviderPlugin` | `Tools(ctx) ([]core.Tool, error)` |
| Event hook | `EventHookPlugin` | session start/end, and a vote on each tool call |
| Backend | `BackendPlugin` | a whole wire API (`core.APIProvider`) |
| Storage | `StoragePlugin` | a `core.SessionStore` per session id |

The rule that matters most: **a plugin hook can only narrow what the host
already allowed.** `AgentConfig.BeforeToolCall` is the authorization boundary
and runs first; hooks run after it, the first `block` wins and stops the scan,
and a hook's `allow` means only "no objection".

## Run it

Sections 1–6 need **no API key**:

```bash
go run ./examples/plugins
```

Section 7 makes one real streamed run, where a plugin tool is called and a
hook refuses a read of `/etc/passwd`. It needs a credential for the model's
vendor (default `anthropic/claude-sonnet-5`):

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/plugins "Convert 20 celsius to fahrenheit, then read the config file at /etc/passwd."
```

Without a key, section 7 prints `skipped:` with the usual `no credential for
vendor` message and the program exits 0. `AGENTKIT_PLUGINS_NO_RUN=1` skips
section 7 outright.

## What you'll see

```
2. hook ordering: the first block wins and stops the scan
---------------------------------------------------------
  convert_temperature  {"celsius":20}                       -> allow      by -           (audit-hook saw [convert_temperature])
  read_config_file     {"path":"/etc/agentkit/app.toml"}    -> allow      by -           (audit-hook saw [read_config_file])
  read_config_file     {"path":"/etc/passwd"}               -> block      by path-guard  (audit-hook saw [])

4. load order: built-in -> manifest (alphabetical) -> local
-----------------------------------------------------------
  before [units path-guard legacy-metrics snooper echo-backend scratch-store audit-hook]
  after  [path-guard echo-backend scratch-store audit-hook units local-tracer]
  refused [snooper]   disabled [legacy-metrics]
  ...
6. plugins.Validate: a conformance report, without starting anything
  ...
  4 manifest(s), 6 registered plugin(s), 2 error(s), 4 warning(s)
  ok=false exit=1
```

## Walkthrough

1. **Registry** — `plugins.NewRegistry()` and `reg.Register(p)`. A value, not
   a global; registration order is the order hooks vote in.
2. **Hook ordering** — `plugins.ToolDecision(ctx, hooks, tool, args)` is what
   the loop calls. See `pathGuard.OnToolUse`: return `plugins.DecisionNone`
   for calls you have no opinion on, `DecisionBlock` to refuse. Embed
   `plugins.BaseEventHook` to get no-op defaults (`auditHook`).
3. **Discovery** — `plugins.ParseConfig(path, src)` reads a `[plugins]` TOML
   section (`paths`, `disabled`); `plugins.Discover(cfg)` finds
   `plugin.toml` manifests one directory deep. A manifest is a *declaration*
   reconciled against what was registered; it cannot load code.
   `writeManifestTree` shows a realistic tree.
4. **Load** — `plugins.Load(cfg, reg, locals…)` orders built-ins, then
   manifest plugins alphabetically, then locals; name collisions are
   later-wins with a warning; the disabled list applies to the registry too.
5. **Import lint** — `plugins.LintImports(dir)` flags imports under
   `plugins.InternalPrefix` (agentkit's `internal/`). `Load` refuses a
   plugin whose source fails it.
6. **Conformance** — `plugins.Validate(cfg, reg)` checks declared kinds
   against implemented interfaces and runs the lint, without calling any
   plugin. `rep.ExitCode()` is CI-ready; only errors fail it.
7. **Wiring into an agent** — set `cfg.Plugins = reg` (the loop reads the
   event hooks from it). Backends, storage and tool providers are applied by
   you at construction: `cfg.Providers.Register(b.Backend())`,
   `sp.OpenSession(…)` into `cfg.SessionStore`, and
   `agent.RegisterTool` for each tool from `tp.Tools(ctx)`.

## Gotchas

- **The import lint is not a sandbox.** Plugin code runs in your process with
  your privileges; the lint only stops a plugin reaching into agentkit
  internals.
- A hook cannot widen: if `BeforeToolCall` refuses a call, no plugin sees it.
- `scratchStore` reduces the session id to one path element before using it
  as a filename — copy that if your storage plugin maps ids to paths.
- The example writes its manifest tree to a temp directory and removes it;
  `scratch-store` logs to `$TMPDIR/agentkit-plugin-sessions/`.

## Related

Package `plugins` (`Registry`, `Load`, `Discover`, `ParseConfig`,
`LintImports`, `Validate`, `ToolDecision`). The `validate-plugins` command
is described in [`docs/cli.md`](../../docs/cli.md).
