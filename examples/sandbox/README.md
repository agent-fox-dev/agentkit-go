# sandbox

AgentKit's built-in tools, running in a sandbox. A sandbox is an ephemeral
environment that holds one workspace; `Sandbox.Tools` returns a drop-in
replacement for `tools.All` whose every call runs in the AgentKit agent inside
it, over the `agentkit.sandbox.v1` protocol. The driver does not change.

```bash
cd examples/sandbox && go run .
```

The example is its own Go module, because it imports the nested `sandbox`
module, which the root module does not require.

It runs offline, with no API key: the "model" is `provider/faux` replaying one
scripted `code_mode` call, and the sandbox comes from `local`, the in-process
provider. **`local` is not isolation**: it runs the agent in this process with
its privileges, and exists for tests and development. It speaks the same
protocol, over the same transport, as the providers that isolate.

## What it does

1. `local.Unisolated().Create` with `NetworkUnrestricted`. `local` cannot
   honour the zero network (`none`), and refuses rather than pretend.
2. The project is written to a temporary host directory, and `Sync` puts it
   in with `sandbox.LocalDir`, over the rsync protocol.
3. `Tools` returns the proxy tools; one `read_file` is called directly.
4. `codemode.New` binds the proxy `read_file`, `search_files` and `execute`,
   and an agent runs a script over them under
   `guard.Restricted(guard.Options{AllowedPrograms: []string{"ls"}})`. The
   script's `ls src` runs in the sandbox; its `rm -rf src` is refused by the
   guard and comes back to the script as `blocked_by_policy`.
5. `Export` takes `src` out into a host output directory with
   `sandbox.LocalDir`, which creates it; the program prints the files it
   finds there.

## The code an embedder copies

```go
sb, err := local.Unisolated().Create(ctx, sandbox.Spec{
	Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted},
})
if err != nil {
	return err
}
defer sb.Close(ctx)
if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: projectDir}); err != nil {
	return err
}
proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
if err != nil {
	return err
}
agent, err := agentkit.New(agentkit.Config{
	Provider: provider,
	Model:    model,
	Tools:    proxies, // execute and run_command among them: a Guard is required
	Guard:    guard.Restricted(guard.Options{AllowedPrograms: []string{"ls"}}),
})
// ... run it, then take the result out.
if err := sb.Export(ctx, "src", sandbox.LocalDir{Path: outDir}); err != nil {
	return err
}
```

A cancelled call returns `aborted` and kills the process it started in the
sandbox; a broken connection makes every later call return
`sandbox_unavailable`. See [`docs/configuration.md`](../../docs/configuration.md)
for `sandbox.Spec` and [`docs/architecture.md`](../../docs/architecture.md)
for the guarantees.
