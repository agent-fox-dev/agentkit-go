# Command-line programs

AgentKit is a library. Its one command is `agentkit-sandboxd`, the agent that
runs inside a sandbox. The programs under `examples/` are runnable too; each
documents its own flags in its `README.md`.

## `agentkit-sandboxd`

The AgentKit agent inside a sandbox. It serves the `agentkit.sandbox.v1`
`Agent` service as one HTTP/2 connection on its stdin and stdout, and runs the
built-in tools (`tools.All`) against one workspace. It opens no listening
socket. A provider starts it, holding pipes to its stdin and stdout; a sandbox
image installs it at `sandbox.AgentPath`
(`/usr/local/libexec/agentkit-sandboxd`).

```bash
go build -o agentkit-sandboxd ./sandbox/cmd/agentkit-sandboxd
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

## Make targets

The targets are listed in [`README.md`](../README.md#make-targets). `make
proto` regenerates the agent protocol's Go code: it needs `protoc` on `PATH`,
installs `protoc-gen-go` v1.36.11 and `protoc-gen-go-grpc` v1.5.1 into
`build/bin`, and records the SHA-256 of `sandbox.proto` in
`sandbox/internal/sandboxpb/sandbox.proto.sha256`, which a test checks
offline.
