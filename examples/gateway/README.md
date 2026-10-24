# gateway: sandboxes on another host, behind a URL and a token

A sandbox gateway is a gRPC service that holds the sandbox backend (Podman or
Docker) and its configuration, so an application can create, use and destroy
sandboxes with nothing but a URL and a bearer token. `gateway.Dial` returns a
`sandbox.Provider` whose sandboxes behave like local ones: the same proxy
tools, the same lifecycle and the same errors. By default this example plays
both parts on one machine and runs offline with no API key: it starts a gateway
in process over `local` and dials it over TCP, and the model is `provider/faux`
replaying one scripted `code_mode` call.

**`local` is not isolation**: in the default run the sandboxes run in this
process with its privileges. Use `--url` against a real `agentkit-gateway` for
containers.

## Run it

The example is its own Go module.

```bash
cd examples/gateway && go run .
```

```
gateway serving on 127.0.0.1:46559 (plaintext, loopback), backend local
dialled http://127.0.0.1:46559: gateway (devel), protocol 1.0, backend local, max TTL 1h0m0s, max 4 sandboxes
a wrong token: refused (gateway.ErrUnauthenticated)
sandbox 7097d002e377c2e7 on gateway/local: agent (devel), protocol 1.2
...
code_mode result:
  README: Says hello from behind the gateway.
  TODOs: 2
  src: main.go, util.go
  rm: blocked_by_policy
...
closed: the gateway holds 0 sandboxes for this token

Gateway example completed successfully
```

The gateway also logs one structured line per sandbox created or destroyed and
per refused token to stderr.

Against a running `agentkit-gateway`:

```bash
# from the repository root
make sandbox-gateway build-containers
openssl rand -hex 24 > tokens         # at least 32 printable characters
build/bin/agentkit-gateway --token-file tokens --plaintext --listen 127.0.0.1:8443

# in another terminal
cd examples/gateway && go run . --url http://127.0.0.1:8443 --token-file ../../tokens \
	--image quay.io/agentfox/agentkit-sandbox:latest
```

| Flag | Default | Meaning |
|---|---|---|
| `--url` | empty | Dial the gateway at this URL instead of starting one in process. |
| `--token-file` | empty | The token file for `--url` (one token per line, `#` comments); its first token is used. |
| `--image` | empty | The sandbox image for `--url`; empty means `sandbox.DefaultImage()`, which a development build does not have. |

With `--url` the sandbox has no network, 1 GiB of memory and 512 PIDs. `http://`
is accepted only for `localhost` or a loopback address; a gateway on another
host is dialled over `https://`.

## What it shows

- Embedding the server: `gatewayd.New` with a backend, the accepted tokens and
  limits (network ceiling, sandbox count, longest TTL).
- `gateway.Dial` fetches the gateway's capabilities; a token the gateway does
  not hold fails with `gateway.ErrUnauthenticated`.
- `sandbox.Open` creates a sandbox on the gateway's host, syncs a host
  directory into it and returns the proxy tools; `List` shows only this
  token's sandboxes.
- A code-mode script runs over the proxy tools under `guard.Allow("ls")`. The
  guard runs in your process: `rm -rf src` is refused before anything reaches
  the gateway.
- `Export` copies files back out; `Close` destroys the sandbox on the gateway.

## The code that matters

The client:

```go
p, err := gateway.Dial(ctx, "http://127.0.0.1:8443", gateway.Options{Token: token})
if err != nil {
	return err // a refused token matches gateway.ErrUnauthenticated
}
defer p.Close() // destroys the sandboxes p created, then disconnects
sb, proxies, err := sandbox.Open(ctx, p, sandbox.Spec{TTL: 10 * time.Minute},
	sandbox.LocalDir{Path: projectDir}, sandbox.ToolOptions{})
if err != nil {
	return err // a failed step has already closed the sandbox
}
defer sb.Close(ctx)
```

The server, when you embed it rather than run `agentkit-gateway`:

```go
srv, err := gatewayd.New(gatewayd.Config{
	Backend:      backend,   // podman.New(...) in production
	Tokens:       tokens,    // each at least 32 printable characters
	TLS:          tlsConfig, // nil serves plaintext: loopback or behind a TLS proxy only
	NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone},
})
l, err := net.Listen("tcp", "127.0.0.1:8443")
go srv.Serve(l)
defer srv.Shutdown(ctx) // destroys every sandbox it holds
```

## Further reading

- [Examples index](../README.md); [`sandbox`](../sandbox) does the same work with the provider in your own process
- [User guide: sandboxes on another host](../../docs/guide.md#sandboxes-on-another-host-the-gateway)
- [`agentkit-gateway` command](../../docs/cli.md#agentkit-gateway): flags and the token file
- [Configuration: the gateway](../../docs/configuration.md#the-gateway-sandboxgateway-sandboxgatewayd)
- [Architecture: sandboxes](../../docs/architecture.md#sandboxes)
