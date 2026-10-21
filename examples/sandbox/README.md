# sandbox

AgentKit's built-in tools, running in a sandbox. A sandbox is an ephemeral
environment that holds one workspace; `Sandbox.Tools` returns a drop-in
replacement for `tools.For` whose every call runs in the AgentKit agent inside
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

## On a confined child process: `--landlock`

```bash
go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@vX.Y.Z   # or, in a checkout: cd sandbox && go install ./cmd/agentkit-sandboxd
cd examples/sandbox && go run . --landlock --agent "$(go env GOPATH)/bin/agentkit-sandboxd"
```

`--landlock` runs the same program on `sandbox/landlock`:
`landlock.New(landlock.Options{AgentBinary: <--agent>})` and the zero `Spec`
(`NetworkNone`). The agent runs as a child process confined by its own
Landlock ruleset. It needs Linux with Landlock ABI 4 (Linux 6.7); elsewhere
the run fails with `landlock: this host cannot confine a sandbox with Landlock ABI 4`.

## On a real container: `--podman`

```bash
make build-containers    # from the repository root: builds quay.io/agentfox/agentkit-sandbox:latest
cd examples/sandbox && go run . --podman --image quay.io/agentfox/agentkit-sandbox:latest
```

`--podman` runs the same program on `sandbox/podman`: `podman.New(podman.Options{})`
and a `Spec` with the zero `Network` (`NetworkNone`), `MemoryBytes` 1 GiB and
`PIDs` 512, from the image `--image` names (empty means
`sandbox.DefaultImage()`, which a development build does not have). The
agent then runs in a hardened container with no network; the first line of
the output names the provider. The default run, without `--podman`, stays
offline on `local`.

## What it does

1. The project is written to a temporary host directory.
2. `sandbox.Open` does `Create`, `Sync` and `Tools` in one call: it creates a
   sandbox on `local.Unisolated()` with `NetworkUnrestricted` (`local` cannot
   honour the zero network, `none`, and refuses rather than pretend), puts the
   project in from the host directory with `sandbox.LocalDir` over the rsync
   protocol, and returns the proxy tools. If a step fails, `Open` closes the
   sandbox itself. The program closes it with `defer sb.Close(ctx)`.
3. `core.Named` picks the proxy `read_file`, `search_files` and `execute`;
   `read_file` is called directly.
4. `codemode.New` binds the three, and an agent runs a script over them under
   `guard.Allow("ls")`. The script's `ls src` runs in the sandbox; its
   `rm -rf src` is refused by the guard and comes back to the script as
   `blocked_by_policy`. The "model" is `faux.ToolCall` for the `code_mode` call
   and `faux.Text` for the answer.
5. `Export` takes `src` out into a host output directory with
   `sandbox.LocalDir`, which creates it; the program prints the files it
   finds there.

## The code an embedder copies

```go
sb, proxies, err := sandbox.Open(ctx, local.Unisolated(),
	sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}},
	sandbox.LocalDir{Path: projectDir}, sandbox.ToolOptions{})
if err != nil {
	return err // a failed step has already closed the sandbox
}
defer sb.Close(ctx)
picked, err := core.Named(proxies, "read_file", "search_files", "execute")
if err != nil {
	return err
}
agent, err := agentkit.New(agentkit.Config{
	Client: provider,
	Model:  model,
	Toolset: agentkit.Toolset{
		Tools: picked, // execute among them: a Guard is required
		Guard: guard.Allow("ls"),
	},
})
// ... run it, then take the result out.
if err := sb.Export(ctx, "src", sandbox.LocalDir{Path: outDir}); err != nil {
	return err
}
```

`Create`, `Sync` and `Tools` stay on `Provider` and `Sandbox` for a second
`Sync`, for no `Sync` at all (pass a nil source) and for the `SyncResult`,
which `Open` discards. `guard.Allow` is `guard.Restricted` with only
`AllowedPrograms` set.

A cancelled call returns `aborted` and kills the process it started in the
sandbox; a broken connection makes every later call return
`sandbox_unavailable`. See [`docs/configuration.md`](../../docs/configuration.md)
for `sandbox.Spec` and [`docs/architecture.md`](../../docs/architecture.md)
for the guarantees.
