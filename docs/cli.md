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
agentkit-sandboxd --workdir /workspace --ttl 1h
```

It builds with `CGO_ENABLED=0` for linux/amd64, linux/arm64, darwin/arm64 and
windows/amd64, and with cgo on the host; with cgo, non-Go files get
tree-sitter outlines (`Hello` reports `outline_backend` `tree-sitter`, else
`none`).

### Flags

The standard `flag` package: `-flag` and `--flag` are equivalent.

| Flag | Default | Meaning |
|---|---|---|
| `--workdir` | `/workspace` | The workspace directory. It must exist and be a directory. |
| `--ttl` | `1h` | How long to serve, as a Go duration. Zero or negative is a usage error. At the TTL the agent cancels every in-flight call, which kills the processes those calls started, and exits. |
| `--version` | | Print `agentkit-sandboxd <module version> (protocol <major>.<minor>)` to stdout and exit 0 without serving. The module version comes from the build info, or is `(devel)`. |

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
| 1 | A startup failure (for example, `--workdir` does not exist), or the TTL expired (`agent: TTL <d> expired`). |
| 2 | A usage error: an unknown flag, a malformed or non-positive `--ttl`, or an extra argument. |

## Make targets

The targets are listed in [`README.md`](../README.md#make-targets). `make
proto` regenerates the agent protocol's Go code: it needs `protoc` on `PATH`,
installs `protoc-gen-go` v1.36.11 and `protoc-gen-go-grpc` v1.5.1 into
`build/bin`, and records the SHA-256 of `sandbox.proto` in
`sandbox/internal/sandboxpb/sandbox.proto.sha256`, which a test checks
offline.
