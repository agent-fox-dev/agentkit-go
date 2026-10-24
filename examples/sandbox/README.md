# sandbox: AgentKit's tools running inside a sandbox

A sandbox is a short-lived environment that holds one workspace, so an agent's
file and shell tools act on a copy of your project rather than on your machine.
`sandbox.Open` creates one, copies a project into it and returns proxy tools
that are drop-in replacements for `tools.For`: the agent is configured exactly
as before, and every call runs inside the sandbox. This example runs offline
with no API key: the model is `provider/faux` replaying one scripted
`code_mode` call, and the sandbox comes from `local`.

**`local` is not isolation**: it runs the sandbox in this process with its
privileges, for tests and development. Use `--landlock` or `--podman` for a
confined sandbox.

## Run it

The example is its own Go module.

```bash
cd examples/sandbox && go run .
```

```
sandbox 0b419d2debecdb80 on local: agent (devel), protocol 1.2
read_file src/main.go in the sandbox: ok=true, 92 bytes

code_mode result:
  README: Says hello from inside the sandbox.
  TODOs: 2
  src: main.go, util.go
  rm: blocked_by_policy

model: Two TODOs, both in src/.
exported src: main.go, util.go

Sandbox example completed successfully
```

On a confined child process (Linux with Landlock ABI 4, Linux 6.7 or later):

```bash
go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@vX.Y.Z   # or, in a checkout: cd sandbox && go install ./cmd/agentkit-sandboxd
cd examples/sandbox && go run . --landlock --agent "$(go env GOPATH)/bin/agentkit-sandboxd"
```

In a container, from an image you build first:

```bash
make build-containers    # from the repository root: builds quay.io/agentfox/agentkit-sandbox:latest
cd examples/sandbox && go run . --podman --image quay.io/agentfox/agentkit-sandbox:latest
```

| Flag | Default | Meaning |
|---|---|---|
| `--landlock` | off | Use `sandbox/landlock`: the agent runs as a child process confined by Landlock, with no network. |
| `--agent` | beside this program, then on `PATH` | The `agentkit-sandboxd` binary for `--landlock`. |
| `--podman` | off | Use `sandbox/podman`: a hardened container with no network, 1 GiB of memory and 512 PIDs. |
| `--image` | `sandbox.DefaultImage()` | The image for `--podman`; a development build has no default image, so pass one. |

## What it shows

- `sandbox.Open` creates the sandbox, syncs a host directory into it with
  `sandbox.LocalDir`, and returns the proxy tools; if a step fails it closes
  the sandbox itself.
- `core.Named` picks the proxy `read_file`, `search_files` and `execute`, and
  a proxy tool can be called directly like any other.
- A code-mode script runs over them under `guard.Allow("ls")`: `ls src` runs
  in the sandbox, and `rm -rf src` is refused by the guard and comes back as
  `blocked_by_policy`.
- `Export` copies files back out to a host directory.

## The code that matters

```go
sb, proxies, err := sandbox.Open(ctx, local.Unisolated(),
	sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}, // local supports only this mode
	sandbox.LocalDir{Path: projectDir}, sandbox.ToolOptions{})
if err != nil {
	return err // a failed step has already closed the sandbox
}
defer sb.Close(ctx)

picked, err := core.Named(proxies, "read_file", "search_files", "execute")
cm, _, err := codemode.New(picked, codemode.Options{})
agent, err := agentkit.New(agentkit.Config{
	Client:  model,
	Model:   faux.Model().ID,
	Toolset: agentkit.Toolset{Tools: []core.Tool{cm}, Guard: guard.Allow("ls")}, // execute is reachable: a Guard is required
})
// ... run it, then take the result out.
err = sb.Export(ctx, "src", sandbox.LocalDir{Path: outDir})
```

Swap `local.Unisolated()` for `landlock.New(...)` or `podman.New(...)` and
nothing else changes. A cancelled call returns `aborted`, and a lost connection
to the sandbox makes every later call return `sandbox_unavailable`.

## Further reading

- [Examples index](../README.md); [`gateway`](../gateway) runs the same work on sandboxes on another host
- [User guide: sandboxes](../../docs/guide.md#sandboxes)
- [Configuration: sandboxes](../../docs/configuration.md#sandboxes-sandboxspec-sandboxtooloptions-local-the-gateway): `sandbox.Spec` and which provider honours which field
- [`agentkit-sandboxd` command](../../docs/cli.md#agentkit-sandboxd)
- [Architecture: sandboxes](../../docs/architecture.md#sandboxes)
