# gateway

AgentKit's sandboxes, behind a sandbox gateway. A gateway is a gRPC service
that holds the backend provider and its credentials; the embedder holds only
the gateway's URL and a bearer token. `gateway.Dial` returns a
`sandbox.Provider` whose sandboxes behave like local ones: the same proxy
tools, the same state machine and the same errors.

```bash
cd examples/gateway && go run .
```

The example is its own Go module, because it imports the nested `sandbox`
and `sandbox/gatewayd` modules, which the root module does not require.

By default the program plays both parts on this machine. It starts a
`gatewayd.Server` in process, listening on a free loopback port, over
`local`, with a random token. Then it dials that port over TCP, exactly as an
embedder on another host would. It runs offline, with no API key: the
"model" is `provider/faux` replaying one scripted `code_mode` call.
**`local` is not isolation**: the gateway's sandboxes run in this process
with its privileges. The gateway logs one structured line per create,
destroy and refused token to stderr.

## Against a running `agentkit-gateway`: `--url`

```bash
# from the repository root
make sandbox-gateway build-containers
openssl rand -hex 24 > tokens         # at least 32 printable characters
build/bin/agentkit-gateway --token-file tokens --plaintext --listen 127.0.0.1:8443

# in another terminal
cd examples/gateway && go run . --url http://127.0.0.1:8443 --token-file ../../tokens \
	--image quay.io/agentfox/agentkit-sandbox:latest
```

`--url` starts no gateway: the program dials the one at the URL with the
first token in `--token-file` (the gateway's own format: one per line, blank
lines and `#` comments skipped). It creates its sandbox from `--image` (empty
means `sandbox.DefaultImage()`, which a development build does not have)
under `NetworkNone`, the gateway's default ceiling, with 1 GiB of memory and
512 PIDs. `agentkit-gateway` runs every sandbox in a hardened Podman or
Docker container ([`docs/cli.md`](../../docs/cli.md#agentkit-gateway)).

`http://` is accepted only for `localhost` or a loopback address, so a token
never crosses a network in clear text. A gateway on another host is dialled
over `https://`, with `agentkit-gateway --tls-cert … --tls-key …`.

## What it does

1. **The gateway's side** (default run only): `gatewayd.New` with the
   `local` backend, one token, a network ceiling of `unrestricted`, at most
   four sandboxes and a TTL of at most an hour. `local` offers only
   `unrestricted`; the default ceiling, `none`, would leave no mode in common,
   and `New` refuses that. `Serve` runs on `127.0.0.1:0`, and `Shutdown`
   destroys every sandbox still held.
2. `gateway.Dial` with the URL and token, which fetches the gateway's
   capabilities (`Provider.Gateway()`: version, backend, limits). A second
   `Dial` with a token the gateway does not hold fails with
   `gateway.ErrUnauthenticated`.
3. `Create` makes a sandbox on the gateway's host with a 10-minute TTL and a
   label; `List` shows it, owned by this token only.
4. `Sync` puts a small project in from a host directory with
   `sandbox.LocalDir`; the gateway relays the rsync stream.
5. `Tools` returns the proxy tools, and an agent runs a code-mode script over
   `read_file`, `search_files` and `execute` under
   `guard.Restricted(guard.Options{AllowedPrograms: []string{"ls"}})`. The
   guard runs in the embedder: the script's `ls src` is relayed to the
   sandbox, and its `rm -rf src` is refused before anything reaches the
   gateway, coming back to the script as `blocked_by_policy`.
6. `Export` takes `src` back out into a host directory, and `Close` destroys
   the sandbox on the gateway; `List` is empty again.

## The code an embedder copies

The server, when you embed the gateway rather than run `agentkit-gateway`:

```go
srv, err := gatewayd.New(gatewayd.Config{
	Backend:      backend,          // podman.New(...) in production
	Tokens:       tokens,           // each at least 32 printable characters
	TLS:          tlsConfig,        // nil serves plaintext: loopback or behind a TLS proxy only
	NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone},
})
if err != nil {
	return err
}
l, err := net.Listen("tcp", "127.0.0.1:8443")
if err != nil {
	return err
}
go srv.Serve(l)
defer srv.Shutdown(ctx) // destroys every sandbox it holds
```

The client:

```go
p, err := gateway.Dial(ctx, "http://127.0.0.1:8443", gateway.Options{Token: token})
if err != nil {
	return err // a refused token matches gateway.ErrUnauthenticated
}
defer p.Close() // destroys the sandboxes p created, then disconnects
sb, err := p.Create(ctx, sandbox.Spec{TTL: 10 * time.Minute})
if err != nil {
	return err
}
defer sb.Close(ctx)
// From here on, sb is a sandbox.Sandbox like any other: Sync, Tools, Export.
```

See [`examples/sandbox`](../sandbox) for the same sandbox work against a
provider in the embedder's own process, and
[`docs/architecture.md`](../../docs/architecture.md) for the guarantees.
