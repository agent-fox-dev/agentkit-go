# Command-line programs

AgentKit is a library. It has two commands: `agentkit-sandboxd`, the agent
that runs inside a sandbox, and `agentkit-gateway`, the sandbox gateway, the
one network service, built from its own nested module. The programs under
`examples/` are runnable too; each documents its own flags in its
`README.md`.

## `agentkit-sandboxd`

The AgentKit agent inside a sandbox. It serves the `agentkit.sandbox.v1`
`Agent` service as one HTTP/2 connection on its stdin and stdout, and runs the
built-in tools (`tools.All`) against one workspace. It opens no listening
socket. A provider starts it, holding pipes to its stdin and stdout; a sandbox
image installs it at `sandbox.AgentPath`
(`/usr/local/libexec/agentkit-sandboxd`).

```bash
(cd sandbox && go build -o ../agentkit-sandboxd ./cmd/agentkit-sandboxd)
agentkit-sandboxd --workdir /workspace --ttl 1h --network none
```

On Linux it confines itself with Landlock before any other work: before it
redirects stdout, builds the agent or serves. Every process a tool starts
inherits the ruleset, which cannot be lifted, and `Hello` reports what was
applied (`sandbox.AgentInfo.Landlock`). The ruleset and the ABI it chooses
are described in
[`configuration.md`](configuration.md#the-agents-landlock-ruleset). There is
no flag that turns it off; elsewhere the agent applies nothing and reports
ABI 0 with the note `Landlock is Linux-only (GOOS=<os>)`.

It builds with `CGO_ENABLED=0` for linux/amd64, linux/arm64, darwin/arm64 and
windows/amd64, and with cgo on the host; with cgo, non-Go files get
tree-sitter outlines (`Hello` reports `outline_backend` `tree-sitter`, else
`none`).

### Flags

The standard `flag` package: `-flag` and `--flag` are equivalent.

| Flag | Default | Meaning |
|---|---|---|
| `--workdir` | `/workspace` | The workspace directory. It must exist and be a directory, and may not be `/` or equal to or an ancestor of a system directory; a relative path is judged by the directory it names from the working directory, so `--workdir .` from `/` is refused. |
| `--ttl` | `1h` | How long to serve, as a Go duration. Zero or negative is a usage error. At the TTL the agent cancels every in-flight call, which kills the processes those calls started, and exits. |
| `--network` | `none` | The sandbox's network mode: exactly `none`, `allowlist` or `unrestricted`; anything else is a usage error. Under `none`, at Landlock ABI 4, the ruleset denies every TCP bind and connect, loopback included; under the other two it handles no network right. `sandbox.AgentArgs` passes the effective spec's mode. |
| `--landlock-ro PATH` | | An extra read-and-execute root, repeatable. It must be absolute; a missing one is skipped with a note. |
| `--landlock-rw PATH` | | An extra writable root, repeatable. It must be absolute, and may not be `/` or equal to or an ancestor of a system directory; a missing one is skipped with a note. |
| `--version` | | Print `agentkit-sandboxd <module version> (protocol <major>.<minor>)` to stdout (currently `(protocol 1.2)`) and exit 0 without serving or probing Landlock. The version is the one stamped at link time (`-ldflags -X …/sandbox/internal/buildver.stamp=vX.Y.Z`), which is the release version in the published images; otherwise it comes from the build info, or is `(devel)`, as in an unstamped image build. |

### Environment

The tools get the command's own environment, verbatim: whatever the provider
that started it set. The command adds and removes nothing.

### Output and exit codes

Before serving, the command points `os.Stdout` at stderr, so a stray write
cannot corrupt the protocol stream on the real stdout. Every failure is one
line on stderr, `agentkit-sandboxd: <cause>`.

| Exit | When |
|---|---|
| 0 | The host closed the connection, or `--version`. |
| 1 | A startup failure (for example, `--workdir` does not exist), a Landlock ruleset that cannot be built or applied after the kernel reported Landlock (`agentkit-sandboxd: landlock: <cause>`; the agent never serves unconfined then), or the TTL expired (`agent: TTL <d> expired`). A kernel without Landlock is not a failure: the agent serves and reports ABI 0. |
| 2 | A usage error: an unknown flag, a malformed or non-positive `--ttl`, a `--network` value other than `none`, `allowlist` or `unrestricted`, a `--workdir` or `--landlock-rw` that is `/` or contains a system directory, a relative `--landlock-ro` or `--landlock-rw`, or an extra argument. The line names the flag. |

## `agentkit-gateway`

The sandbox gateway: a gRPC server on TCP that lets clients
(`gateway.Dial`, package `sandbox/gateway`) create, use and destroy sandboxes
on this host with a bearer token. Its backend is the Podman or Docker
provider (`podman.New`), given the command's own environment as the engine's
environment. It is not published; build it from a checkout:

```bash
make sandbox-gateway                          # build/bin/agentkit-gateway
make sandbox-gateway AGENTKIT_VERSION=v0.19.0 # stamps the version it reports
build/bin/agentkit-gateway --token-file /etc/agentkit/tokens \
  --tls-cert server.pem --tls-key server-key.pem --listen 0.0.0.0:8443
```

It listens on loopback by default and needs TLS or an explicit
`--plaintext`; behind a TLS-terminating proxy on the same host, use
`--plaintext` on a loopback address, the only place a client accepts
`http://`. Each sandbox belongs to the token that created it, and no other
token can see or reach it. The gateway destroys every sandbox it holds when
it shuts down.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:8443` | The address to listen on. |
| `--token-file` | none; required | The file holding the accepted tokens (below). |
| `--tls-cert`, `--tls-key` | | The server's PEM certificate and key; both or neither. |
| `--plaintext` | | Serve without TLS. Required when the TLS flags are absent, and refused together with them. On a non-loopback address the command also logs a warning. |
| `--engine-binary` | `podman` | The engine command (`podman.Options.Binary`). |
| `--engine` | empty | `podman` or `docker`; empty means detected from `<engine-binary> --version`. |
| `--runtime` | empty | Empty (the engine's default OCI runtime), or `krun`. |
| `--userns` | | The user-namespace setting passed to the engine as `--userns`. |
| `--network` | `none` | The network ceiling: a comma-separated list drawn from `none` and `unrestricted`. Clients may request only these modes. |
| `--max-sandboxes` | `16` | The most sandboxes held at once; a `Create` beyond it is refused (`gateway.ErrCapacity`). |
| `--max-ttl` | `24h` | The longest TTL accepted; a longer one is refused, naming `TTL`. |
| `--reap-interval` | `10m` | How often the backend's `Reap` runs; it also runs once at start. |
| `--version` | | Print `agentkit-gateway <version> (gateway protocol 1.0, agent protocol 1.2)` and exit 0. The version is the link-time stamp, else the build info's sandbox-module version, else `(devel)`. |

### The token file

One token per line. Blank lines and lines starting with `#` are ignored, and
surrounding whitespace is trimmed. Each token must be at least 32 characters
of printable ASCII without whitespace, and no token may repeat. Tokens are
never accepted from a flag or the environment. The server keeps only each
token's SHA-256 and logs only its first 8 hex characters.

```
# one line per embedder
3f9c2b7e8a1d4c6f0b5e9a2d7c4f1e8b3a6d9c2f5e8b1a4d
```

### Output and exit codes

After binding, the command prints one line to stderr:

```
agentkit-gateway: listening on <bound address> (<tls|plaintext>), backend <name>, version <v>
```

It then logs structured lines (`log/slog` text) to stderr: one per create,
destroy (with the reason `client`, `expired`, `unavailable` or `shutdown`),
refused authentication (with the peer address) and backend reap. SIGINT or
SIGTERM runs a shutdown with a 60-second budget. A failure ends the run with
one line, `agentkit-gateway: <cause>`, that never contains a token; a startup
or usage failure prints nothing else.

| Exit | When |
|---|---|
| 0 | A clean shutdown, or `--version`. |
| 1 | A startup failure (a token file that cannot be read or holds an invalid token, invalid TLS files, an error from the backend's `podman.New`, a failure to listen), a serving failure, or a shutdown that failed to destroy a sandbox. |
| 2 | A usage error: an unknown flag, no `--token-file`, half a TLS pair, `--plaintext` together with TLS or neither of them, an `--engine` other than `podman` or `docker`, a `--runtime` other than `krun`, a `--network` entry other than `none` or `unrestricted`, a non-positive `--max-sandboxes`, `--max-ttl` or `--reap-interval`, or an extra argument. Nothing is read or run first. |

A `GitRepo` sync through the gateway runs the backend's init step on this
host, with the engine's default network: it reaches whatever HTTPS URL the
client names, as on a local Podman host.

## Make targets

The targets are listed in [`README.md`](../README.md#make-targets). `make
proto` regenerates the agent and gateway protocols' Go code: it needs `protoc`
on `PATH`, installs `protoc-gen-go` v1.36.11 and `protoc-gen-go-grpc` v1.5.1
into `build/bin`, and records the SHA-256 of `sandbox.proto` and
`gateway.proto` in `sandbox/internal/sandboxpb/sandbox.proto.sha256` and
`gateway.proto.sha256`, which tests check offline.
