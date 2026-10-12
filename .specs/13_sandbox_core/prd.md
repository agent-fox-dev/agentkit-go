---
spec_id: "13"
spec_name: "sandbox_core"
title: "Sandbox foundation: agent protocol, proxy tools, tar transfer and the local provider"
status: "active"
created_at: "2026-10-12T12:33:33.084695Z"
updated_at: "2026-10-12T12:33:33.084695Z"
intent_hash: "adf6f8d3af3eaec676be828a34aef4aecc1e49c6dcab4431d43c7749499160cf"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Let an embedder run AgentKit's built-in tools inside a sandbox: an ephemeral execution environment that holds exactly one workspace. The embedder gets a `[]core.Tool` that is a drop-in replacement for `tools.All`, and every call to one of those tools runs in an AgentKit agent process inside the sandbox, reached over an AgentKit gRPC protocol. Containment then comes from where the tools run, and the driver does not change. This spec is the foundation every sandbox provider builds on: the host-side `sandbox` API, the `agentkit.sandbox.v1` agent protocol, the `agentkit-sandboxd` agent, the proxy tools, tar transfer into and out of the workspace, and `local`, an in-process provider with no isolation that exists for tests.

## Goals

1. `local.Unisolated().Create`, then `Sync` of a tar, then `Tools`, returns one proxy tool for each tool `tools.All` returns, in the same order. Each has an equal `Name`, `Description`, `PromptGuidelines`, `Builtin`, `ExecutionMode` and `Label`, and `InputSchema` and `OutputSchema` marshal to byte-identical JSON.
2. The spec 06 conformance cases (each built-in tool's `Data` validated against its `OutputSchema` on success, and its documented error codes) pass against proxy tools over `local`, from the same case table the `tools` package runs.
3. A proxy call whose host context is cancelled returns `aborted`, and the process it started inside the sandbox is gone. A transport failure returns `sandbox_unavailable` for that call and for every later call on that sandbox.
4. A code-mode script over proxy tools, driven by `provider/faux` through `agentkit.New`, has one call blocked by `Config.Guard` and gets a tool error for it. Its other calls complete, and every nested call's execution events name the `code_mode` call as parent.
5. `agentkit.New` with proxy shell tools (`execute`, `run_command`) and a nil `Guard` fails with `core.ErrUnguardedExecute`, exactly as with local ones.
6. `Create` refuses a `Spec` that the provider's `Capabilities` do not cover, with an error naming the provider and the field. `local` refuses the zero `Spec`, because the zero value of `Network` is `NetworkNone`.
7. Tar extraction refuses entries named `../x`, absolute names, a symlink followed by an entry written through it, symlinks that point outside the target, hard links and device nodes. This holds both when tar goes into the sandbox and when the host validates an export.
8. `go build ./...` needs no `protoc`. A test fails when the checked-in generated code is older than the `.proto`. The root module's direct dependencies gain exactly `google.golang.org/grpc` and `google.golang.org/protobuf`, and `go list -m all` is unchanged. `make check` stays green, including the cross-target build gate and TS-12-14's 20,000-line budget.
9. The exported surfaces of `agentkit`, `core`, `tools` and `codemode` are unchanged.

## Non-goals

- **Any isolation.** `local` is not a sandbox and says so. The real boundaries arrive in later scopes of this split:
  - `sandbox_rsync_sync`: `LocalDir` sync and export over the rsync protocol.
  - `sandbox_landlock_agent`: the agent's own Landlock ruleset, `Spec.RequireLandlock`, and the ABI reported in `Hello`.
  - `sandbox_images`: the base and overlay images and their release workflow.
  - `sandbox_podman`: the Podman and Docker provider, the `GitRepo` source and its init step, the hardening set, and guidance on `guard.AllowAll`.
  - `sandbox_landlock_provider`: a confined child process on Linux.
  - `sandbox_gateway`: the gateway service, the client in the root module and the server in a nested module.
- **The second release.** The Kubernetes gateway backend, the OpenShell provider and D3 (c) (the script interpreter running in the sandbox, with authorization called back to the host) each get their own PRD when they are taken up. Only the reusable `Call` messages are designed for them here.
- **`LocalDir`, `GitRepo`, `.gitignore` filtering on transfer, and the rsync library.** All belong to later scopes. Tar is the only transfer in this spec, and it moves bytes verbatim.
- **Changing the driver, `core`, `tools` or `codemode`.** There is no `Config.Sandbox`. The driver does not know a tool is remote.
- **A network service.** `agentkit-sandboxd` serves on its stdio. Nothing in this spec listens on a socket.
- **Reconnection, persistent or shared sandboxes, more than one workspace per sandbox, interactive attach, and Windows parity.** Windows must build, and its behaviour is best effort (steering).
- **Scrubbing host paths from results.** `local` runs on the host, so its paths are host paths. That guarantee belongs to the container providers.
- **The `tools.Workspace` doc comment and the `guard` documentation.** They change with the scopes that actually contain `execute` (`sandbox_landlock_agent`, `sandbox_podman`).

## Background

What the code says today:

- `tools.Workspace` is the containment boundary for the file tools only. Its doc comment in `tools/path.go` says: "It does NOT constrain `execute`". The `guard` package doc calls `Restricted` "a floor for unattended safety, not a sandbox".
- `tools.All(opts Options)` (`tools/tools.go`) builds eleven built-ins over one `Workspace`: `read_file`, `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`, `execute` and `run_command`. Every description is a static string, so a definition built on the host equals one built in the agent from the same version.
  - `Options` carries `SpillDir`, `DisableSpill`, `Env`, `Ignore`, `Symbols` and `Index`.
  - A nil `Env` means `ReducedEnv(nil)`.
  - An empty `SpillDir` means a directory under `os.TempDir()`.
  - Two built-ins set `PrepareArguments`: `search_files` and `edit_file`.
  - No built-in sets `ToolResult.Blocks` or `Terminate`.
- `core.Tool` (`core/tool.go`) keeps definition separate from execution: `Name`, `Description`, `InputSchema`, `OutputSchema`, `PromptGuidelines`, `Builtin`, `ExecutionMode` and `PrepareArguments` on one side, `Execute` or `Handler` on the other. A proxy tool is the `tools` definition with a remote `Execute`.
- `core.ToolResult` carries `OK`, `Data map[string]any`, `Error`, `Detail`, `Metadata *ToolMetadata` and `Text`. `ToolMetadata` has stable JSON tags.
- The batch executor (`batch.go`) calls `t.Execute(ctx, p.Raw)`, where `Raw` is the model's own bytes unless the pipeline repaired them. A batch containing a `Sequential` tool runs serially. A cancelled batch gets `core.ErrResult("aborted", "Operation aborted")`, and the nested caller (`nested.go`) uses the same result.
- `checkShellGuard` (`execguard.go`) refuses unguarded shell tools by name, so proxy tools named `execute` are covered without any change.
- `codemode` converts `Data` to Starlark through JSON with `UseNumber` (`codemode/convert.go`). A `Data` map that was decoded from JSON therefore behaves the same in a script as the tool's original map.
- `tools.Run` and `RunArgv` (`tools/exec.go`) run each command in its own process group and kill the group when the context is cancelled. Through the service, cancellation reaches the agent as a cancelled RPC context.
- The symbol table behind `find_symbol` is not revalidated unless a write tool or a shell tool marks it (TS-02-39 in `tools/symbols_freshness_test.go`). Files written by a sync would go unseen if the agent kept its tool set.
- The project rule is that untrusted bytes go through `wire` (`docs/architecture.md`). `wire.Guard(data, Limits{})` bounds a message at 16 MiB (`wire.Defaults`), and limits depth and node count, and rejects duplicate keys.
- `internal/policy/deps_test.go` holds the root module's direct-dependency allowlist (`approvedDeps`, TS-09-16), and `docs/DEPS.md` holds the rulings behind it. `go.mod` already lists `google.golang.org/grpc v1.64.1` and `google.golang.org/protobuf v1.36.11` as indirect requirements.
- `internal/policy/crosstarget_test.go` builds and vets linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 with cgo off, and builds the host with cgo on.
- `core/size_test.go` (TS-12-14) fails when the root module exceeds 20,000 lines of non-test Go code (non-blank, non-comment lines). It skips nested modules but counts generated code.
- The spec 06 conformance suite lives in `tools/conformance_test.go` (package `tools_test`), over a small tree with `tools.NoGlobalExcludes()`.
- `mcp/transport.go` already runs a child process with exactly the configured environment, in its own process group, over pipes that the code owns. Later providers that start an agent process follow that pattern.
- There is no `cmd/` directory and no proto tooling. `docs/cli.md` was deleted by spec 12 because its commands went away.

PRD 11 (`docs/prd/11-run-tools-in-an-isolated-sandbox.md`) settled the overall design in its decisions D1–D8:

- D1: tools are bound to a sandbox through proxy tools, not through a virtual filesystem.
- D2: the wire is an AgentKit gRPC protocol, not MCP, so that schemas come from the `tools` package and are byte-identical, and so that `ToolMetadata` survives the trip.
- D3: a code-mode script runs on the host first.
- D5: the agent comes from the image rather than being injected.
- D6: the gateway client lives in the root module.

This spec implements the first part of PRD 11's step 1. The rest of that step and steps 2–5 are the later scopes listed in Non-goals.

## Requirements

### 1. The agent protocol and its generated code

Write the protocol `agentkit.sandbox.v1` in `sandbox/internal/sandboxpb/sandbox.proto`, with `go_package` pointing at that directory. Its one service has five RPCs:

```protobuf
service Agent {
  rpc Hello(HelloRequest) returns (HelloResponse);       // protocol and agent versions, workspace root, outline backend, git present
  rpc Tools(ToolsRequest) returns (ToolsResponse);       // configure the tool set; returns each tool's name and definition digest
  rpc Call(CallRequest) returns (CallResponse);          // tool name + input (JSON bytes) -> ok, data (JSON bytes), error, detail, metadata (JSON bytes), text
  rpc SyncTar(stream TarChunk) returns (SyncResponse);   // first chunk: target path and replace flag; then tar bytes
  rpc ExportTar(ExportRequest) returns (stream TarChunk); // a tar stream of a workspace subtree
}
```

- **JSON fields stay bytes.** Tool input, result data and result metadata travel as JSON `bytes` fields, never as proto structs.
- **Generated code is checked in.** The `protoc-gen-go` and `protoc-gen-go-grpc` output sits beside the `.proto`, so `go build ./...` and `go test ./...` need no generator.
- **`make proto` regenerates it.** It uses `protoc` with `protoc-gen-go` v1.36.11 (matching the protobuf runtime) and `protoc-gen-go-grpc` v1.5.1, both installed with `go install` into `build/bin`. When `protoc` is missing, `make proto` fails with a message that names it.
- **Staleness is checked offline.** `make proto` also writes the SHA-256 of the `.proto` into a checked-in file next to it. A test in the package fails when the current `.proto` hashes differently, and its message says to run `make proto`.
- **Fields are added, never renumbered.** `sandbox.ProtocolMajor = 1` and `sandbox.ProtocolMinor = 0`. A later scope that adds an RPC or a field bumps the minor version.
- **Message size is bounded.** `sandbox.MaxMessageBytes` is 16 MiB, set as the send and receive limit on both ends. Tar data chunks are at most 1 MiB each.
- **`Call` messages are self-contained.** `CallRequest` and `CallResponse` hold everything one call needs, so that a later script stream can reuse them as frames.

### 2. The host API and spec checking

Package `sandbox` exports:

- `AgentPath = "/usr/local/libexec/agentkit-sandboxd"`.
- `Spec`, with fields `Image`, `Workdir`, `Env map[string]string`, `Resources`, `Network`, `TTL`, `Labels` and `Provider map[string]any`.
- `Resources`, with fields `CPUs`, `MemoryBytes`, `PIDs` and `DiskBytes`.
- `Network`, with `Mode NetworkMode` and `Allow []Endpoint{Host, Port}`. The modes are `NetworkNone` (the zero value), `NetworkAllowlist` and `NetworkUnrestricted`.
- `Capabilities`, with these fields:
  - `Provider` (the provider's name).
  - `Isolation`: `IsolationNone`, `IsolationConfinement`, `IsolationContainer` or `IsolationMicroVM`.
  - `Network` (the modes it honours).
  - Which `Resources` fields it can bound.
  - `Image` and `Workdir` (whether it honours those fields).
  - `Notes`, a plain statement of what it does not do.
- `Provider`, with methods `Capabilities()`, `Create(ctx, Spec) (Sandbox, error)` and `Reap(ctx) ([]string, error)`.
- `Sandbox`, with methods `ID()`, `Spec()`, `Agent() AgentInfo`, `Sync(ctx, Source) (SyncResult, error)`, `Tools(ctx, ToolOptions) ([]core.Tool, error)`, `Export(ctx, path string, Destination) error` and `Close(ctx) error`.
- `AgentInfo`, with fields `Version`, `ProtocolMajor`, `ProtocolMinor`, `Workdir`, `OutlineBackend` and `Git`.
- `ToolOptions`, with fields `Symbols tools.SymbolOptions`, `SpillDir` and `DisableSpill`.
- `Source` and `Destination`, as sealed interfaces. This spec provides `Tar{R io.Reader; Path string; Replace bool}` and `TarOut{W io.Writer}`.
- `SyncResult{Files int; Bytes int64}`.
- The errors named below.

`sandbox.CheckSpec(caps Capabilities, spec Spec) (Spec, error)` is the single place where a spec is checked. Every provider calls it first in `Create`.

- **Defaults.** It fills in `TTL` of 0 as 1 hour, and `Workdir` as `/workspace` only when `caps.Workdir` is set.
- **Fields outside the capabilities.** It refuses every field that `caps` does not cover with a `*SpecError{Provider, Field, Reason}` that matches `ErrUnsupported` under `errors.Is`. Its message reads like `sandbox: provider "local" cannot honour Spec.Network.Mode = none: …`.
- **Values no provider accepts.** It refuses, whatever the provider:
  - a negative `TTL` or a negative resource value;
  - an environment name that is empty or contains `=` or NUL, and an environment value that contains NUL;
  - the reserved label key `agentkit.sandbox`;
  - an `Allow` list with a mode other than `NetworkAllowlist`;
  - an endpoint with an empty host or a port outside 1–65535.
- **Provider options.** Each provider validates the keys of `Spec.Provider` itself and refuses an unknown key, naming it.

### 3. The agent service

The agent side lives in `sandbox/internal/agent`. It is built from a workspace directory, an explicit environment (`[]string`), a TTL and version strings, and it serves the `Agent` service over a `net.Conn`.

**Startup.** The workspace directory must already exist and be a directory, or the agent fails to start.

**`Hello`** returns:
- the protocol version;
- the agent's module version from the build info (`(devel)` when there is none);
- the workspace root, symlinks resolved;
- the outline backend for non-Go files: `tree-sitter` in a cgo build, `none` otherwise;
- whether `git` resolves on the environment's `PATH`.

**`Tools`** builds `tools.All` over the workspace with:
- `Env` set to the explicit environment, never the process's own;
- `Ignore: tools.NoGlobalExcludes()`, so the workspace's own ignore files still apply;
- `Symbols` from the request;
- `SpillDir` from the request, which must be an absolute path. When the request leaves it empty, the default is `<TMPDIR of the explicit environment>/agentkit-spill`, not the process's `os.TempDir()`.
- `DisableSpill` from the request;
- no index.

It returns each tool's name and its definition digest, which is a SHA-256 over the tool's name, description, input and output schema JSON, prompt guidelines, `Builtin` flag and execution mode. Host and agent compute the digest with one shared function. A second `Tools` RPC with identical options returns the same answer; one with different options fails with `FailedPrecondition`.

**`Call`** runs each request on its own goroutine. It looks the tool up in the current set and passes the input bytes to `Execute` verbatim, with the RPC context as the tool's context. Cancelling the RPC therefore cancels the tool, and `tools.Run` kills the process group.

- A tool the agent does not have, or any call before `Tools`, gets a normal `CallResponse` with `ok=false`, error `unknown_tool` and a detail listing the available tools. It is not an RPC error.
- `Data` and `Metadata` are marshalled with `encoding/json`. A nil or empty value is sent as empty bytes.
- A marshal failure becomes `ok=false` with error `marshal_failed`.
- A panic in a tool becomes error `panic`, as `invokeHandler` does on the host.

**Tool-set rebuild.** After every successful `SyncTar`, if the tool set is configured, the agent rebuilds it with the same options, so that the symbol table and the reference cache see the synced files. Calls already in flight finish on the old set.

**TTL.** At the TTL, measured from start, the agent stops serving: in-flight calls are cancelled, and `Serve` returns an error that names the TTL.

### 4. The `agentkit-sandboxd` command

`sandbox/cmd/agentkit-sandboxd` is a thin `main` over the agent service.

- **Flags.** `--workdir` (default `/workspace`). `--ttl` (a duration, default `1h`; zero or negative is a usage error). `--version` (prints the agent version and protocol version, then exits 0).
- **Transport.** It serves the service as one HTTP/2 connection on a `net.Conn` made of stdin and stdout. It opens no listening socket.
- **Stdout protection.** Before serving, it points `os.Stdout` at stderr, so a stray write cannot corrupt the stream.
- **Environment.** The tool environment is its own `os.Environ()`, verbatim. The provider that started it set that environment.
- **Exit.** It exits 0 when the host closes the connection. It exits non-zero, with a one-line message on stderr, on a usage error, a startup failure or expiry of the TTL.
- **Builds.** It builds on all four cross targets with cgo off, and on the host with cgo on.

### 5. Attach, handshake and lifetime

An internal package under `sandbox/` implements `Sandbox` once, for every provider. It is written against a gRPC client connection plus per-call outgoing metadata, so that the gateway client in a later scope reuses it.

**Attach.** For a provider holding a `net.Conn` to an agent, it opens a gRPC client:
- with the `MaxMessageBytes` limits;
- with insecure transport credentials, because the channel is a private pipe;
- with a dialer that hands out the connection exactly once and never redials.

**Handshake.** `Create` calls `Hello` under its own context.
- A different protocol major is refused with `ErrProtocol`, naming both versions, and the sandbox is destroyed.
- Any minor of the same major is accepted.
- A later RPC that the agent does not implement (`Unimplemented`) returns an error naming the agent's protocol version. It is not a transport failure.

**Context.** The context passed to `Create` bounds creation only. It does not bound the sandbox's lifetime.

**Lifetime and Close.**
- At the TTL, a host-side timer destroys the sandbox.
- `Close` is idempotent. It cancels the timer, closes the connection, calls the provider's destroy step and returns the first error. Any later `Close` returns nil.
- `Spec()` returns the effective spec, with defaults applied and `Workdir` set to the workspace root the agent reported.
- `Agent()` returns the `Hello` facts.

**State.** A sandbox is live, then unavailable, then closed.
- It becomes unavailable on a transport failure, when the agent ends, or at the TTL.
- A **transport failure** is either of these, while the host context is still live:
  - an RPC error whose status is not one of the agent's deliberate refusals (`InvalidArgument`, `FailedPrecondition`, `NotFound`, `Unimplemented`);
  - a malformed response.
- Once unavailable or closed, `Tools`, `Sync` and `Export` return `ErrUnavailable` or `ErrClosed` without making an RPC.

### 6. Proxy tool definitions

`Sandbox.Tools(ctx, opts)` sends the `Tools` RPC. It then builds the host's own `tools.All` definitions with the same options, over a throwaway workspace.

- **Comparison.** It compares the agent's tool names (same set, same order) and per-tool digests with the host's. Any difference fails with `ErrToolMismatch`, naming each tool that differs and both the agent version and the host version.
- **What each proxy carries.** `Name`, `Description`, `InputSchema`, `OutputSchema`, `PromptGuidelines`, `Builtin`, `ExecutionMode`, `PrepareArguments` and `Label` are copied from the host's `tools.All` value. `Handler` and `ReachableTools` are nil, `Terminating` is false, and `Execute` is remote. No `Execute` closure from the host's `tools.All` is kept.
- **Order and repeats.** The tools come back in `tools.All` order. A second `Tools` call with equal options returns equivalent tools; one with different options fails with `ErrToolsConfigured`.
- **Concurrency.** The proxy tools are safe for concurrent use. The host's batch executor decides what runs in sequence, and the agent runs whatever it is sent.

### 7. Proxy tool execution

A proxy `Execute(ctx, input)`:

- **Input.** Sends `input` verbatim, which is the `PreparedArguments.Raw` the pipeline hands it. If the input would exceed `MaxMessageBytes`, it returns `arguments_too_large` without making an RPC, and the sandbox stays live.
- **Result.** Maps the response onto a `core.ToolResult`:
  - `OK`, `Error`, `Detail` and `Text` are copied verbatim.
  - `Data` must pass `wire.Guard` with the default limits and be a JSON object. It is decoded with `UseNumber`, so re-encoding it gives the agent's bytes.
  - `Metadata` is guarded the same way and decoded into a `*core.ToolMetadata`; empty means nil.
  - `Terminate` is false and `Blocks` is nil.
  - A response that fails these checks is a transport failure.
- **Cancellation.** If the host context ends before or during the call, it returns `aborted` / `Operation aborted`, the same result the batch executor and nested caller produce. The sandbox stays live, and the agent kills the process group through its cancelled context.
- **Transport failure.** It returns `sandbox_unavailable` with a detail naming the cause, and marks the sandbox unavailable. There is no retry and no reconnect.
- **After `Close`.** It returns `sandbox_unavailable` with detail `sandbox closed`.
- **Unknown tool.** An `unknown_tool` from the agent is passed through as a result.

### 8. Tar sync and export

**Paths.** A `Tar.Path` or an `Export` path is workspace-relative and slash-separated; `""` or `.` means the root. A path that is absolute, or that contains `..` after cleaning, is refused before any byte moves.

**Entry rules.** One function applies these rules on both sides:
- **Allowed:** regular files, directories, and symlinks whose target, resolved against the link's own directory, stays inside the target directory.
- **Refused:**
  - an absolute entry name;
  - an entry name with a `..` component;
  - hard links, character and block devices, FIFOs, and any other entry type;
  - a symlink whose target leaves the target directory;
  - an entry whose path resolves through an existing symlink to somewhere outside.
- **Modes:** setuid, setgid and sticky bits are cleared.
- **Write safety:** writes go through an `os.Root` opened at the target directory, so no write follows a symlink out of it.

**Sync.** `Sync(ctx, Tar{R, Path, Replace})` streams a start chunk, then `R` in chunks of at most 1 MiB. The agent extracts as the bytes arrive, creating missing parent directories.
- With `Replace`, the existing contents of `Path` are removed first. `Path` itself is kept, or created.
- The first refused or malformed entry ends the sync with `InvalidArgument`, naming the entry. The host returns it as `ErrUnsafeArchive`. Entries already extracted stay, and the documentation says so.
- A read error from `R`, or a cancelled context, fails the sync and cancels the RPC.
- `SyncResult` reports the files written and the bytes of file content.

**Export.** `Export(ctx, path, TarOut{W})` asks the agent to archive the subtree verbatim. That means hidden and ignored files too, symlinks stored as symlinks and never followed, entry types other than the allowed three skipped, and names relative to `path`.
- A path that does not exist is `NotFound`.
- A path naming a single file archives that file.
- The host treats the stream as untrusted. It reads it with `archive/tar`, applies the entry rules and writes each validated entry to `W`. The first refused entry stops the export with `ErrUnsafeArchive`.

**Failures.** A refusal never marks the sandbox unavailable; a transport failure does. A sync or export that runs at the same time as tool calls has no ordering guarantee with them.

### 9. The `local` provider

`local.Unisolated() sandbox.Provider` is for tests and development only.

**Capabilities.** `Provider: "local"`, `Isolation: IsolationNone`, `Network: [NetworkUnrestricted]`, no `Resources`, no `Image`, no `Workdir`, and `Notes` stating that tools run in the embedder's process with its privileges.

**Refusals.** Through `CheckSpec`, it refuses:
- `NetworkNone`, so the zero `Spec` is refused;
- `NetworkAllowlist`;
- any `Resources` field;
- `Image`;
- `Workdir`;
- any `Provider` option.

**Create.**
- It makes `<os.TempDir()>/agentkit-sandbox-local/<id>/` holding `workspace/`, `home/` and `tmp/`, each with mode 0700, plus a marker file recording the creating process's pid, the labels, the creation time and the TTL. The `<id>` is 16 random hex characters.
- The agent's environment is `Spec.Env`, plus `PATH`, `LANG` and `TERM` from the host when `Spec.Env` lacks them, plus `HOME` and `TMPDIR` pointing at the private directories.
- It runs the agent service in-process over two `os.Pipe` pairs, using the same `net.Conn` adapter as the command, so the production transport is exercised. It applies no confinement of any kind.

**Close** stops the in-process agent and removes the directory.

**Reap** removes every sandbox directory under its root whose marker names a pid that is not running, and returns the ids it removed. On Unix the check is signal 0. Where no liveness probe exists, Reap removes nothing.

**Test hook.** Tests have an unexported or internal way to cut the connection in the middle of a call.

### 10. Integration, dependencies, build gates and documentation

**Integration tests.** Tests under `sandbox/` show that:
- `agentkit.New` with proxy shell tools and a nil `Guard` fails with `core.ErrUnguardedExecute`;
- a `provider/faux`-driven code-mode script over proxy `read_file`, `search_files` and `execute` meets Goal 4;
- every proxy tool's effect lands in the sandbox's workspace and not in the host's working directory.

**Conformance suite.** The spec 06 case table moves from `tools/conformance_test.go` into a root `internal/toolconformance` helper. The `tools` tests run it against `tools.All` with unchanged behaviour, and the sandbox tests run it against proxy tools over `local`.

**Dependencies.** `google.golang.org/grpc` (v1.64.1) and `google.golang.org/protobuf` (v1.36.11) move from indirect to direct requirements at the versions already in `go.sum`.
- Both are added to `approvedDeps`, each with its reason.
- `docs/DEPS.md` gains a ruling for each.
- `golang.org/x/net` stays indirect, as TS-09-15 requires.

**Build gates.** The cross-target and host-cgo gates pass. TS-12-14 passes with generated code counted.

**Package layout.**
- All new production code lives under `sandbox/`.
- Outside that tree, only `examples/sandbox` and test helpers under `internal/` touch it.
- No exported identifier of `agentkit`, `core`, `tools` or `codemode` changes.

**Documentation.** Per steering, run `make doc`, then update:
- `README.md`: a Sandboxes section after Code mode, the packages table, the make targets table with `make proto`, and the "listens on no socket" sentence, rewritten as "the library listens on no socket; `agentkit-sandboxd` serves the agent service on its stdio".
- `docs/architecture.md`: package graph rows, a Sandboxes section covering the service, the proxy guarantees and the state machine, and the D3 record.
- `docs/configuration.md`: `sandbox.Spec`, `Resources`, `Network`, `ToolOptions`, `Tar`/`TarOut`, `local`, and a table of which provider honours which field.
- `docs/cli.md`: recreated, with the `agentkit-sandboxd` flags.
- `docs/DEPS.md`: the two rulings above.
- `examples/sandbox` with a README: the PRD 11 §2 program against `local` and `provider/faux`. It runs offline and exits 0, with a `main_test.go` like `examples/codemode`'s.

## Design Decisions

1. **This spec is the first of seven first-release scopes.** PRD 11's step 1 alone would exceed one spec. Tar stays in this spec because it needs only the standard library, and without it the foundation has no way to put a project into a sandbox.
2. **The split stops at the first release.** PRD 11 defers the Kubernetes backend, the OpenShell provider and D3 (c) to a second release, and OpenShell's gRPC surface is not yet verified. Specifying them now would mean writing against unknowns.
3. **Proxy tools, an AgentKit gRPC protocol, and the interpreter on the host (D1, D2, D3 (a)) are taken as decided.** The code confirms the premises: static tool descriptions, `Execute(ctx, Raw)` in the batch executor, and `codemode.New` accepting any `[]core.Tool`.
4. **All code lives under `sandbox/`, and the command moves from PRD 11's `cmd/agentkit-sandboxd` to `sandbox/cmd/agentkit-sandboxd`.** Go's `internal` rule lets the command import `sandbox/internal/agent`. If TS-12-14's budget or the allowlist ever requires it, the tree can become a nested module without changing a single import path.
5. **The sandbox stays in the root module, as D6 decided, and generated code counts toward TS-12-14.** This scope fits under the budget. Moving to a nested module, not raising the budget, is the remedy if a later scope does not fit.
6. **The generated code is checked in and produced with `protoc` and pinned plugins; staleness is caught by a recorded SHA-256 of the `.proto`.** `go build` stays plain, and the staleness test runs offline with no generator.
7. **grpc and protobuf are promoted at the versions already in `go.sum`.** That keeps `go list -m all` unchanged. Upgrading them is a separate ruling.
8. **The message limit is 16 MiB in both directions, matching `wire.Defaults`; tar chunks are at most 1 MiB.** One number for untrusted payloads in the whole module.
9. **Tool options travel in a `Tools` RPC and are fixed after the first call.** `ToolOptions` arrive after `Create`, and rebuilding silently on different options would discard caches the model relies on.
10. **A tool-definition digest mismatch is refused, not warned about.** The proxy promises the model the agent's behaviour under the host's schemas. Comparing digests still lets an older agent work across releases whenever the tools themselves did not change.
11. **`Data` and `Metadata` travel as JSON bytes and are checked with `wire.Guard`; `Blocks` and `Terminate` are not carried.** Agent output is untrusted under the project's `wire` invariant, JSON metadata picks up new `ToolMetadata` fields without a proto change, and no built-in tool sets `Blocks` or `Terminate`.
12. **Host cancellation returns `aborted` at once, and the sandbox stays live.** It is the batch executor's and nested caller's result for a cancelled call, and the agent kills the process group through the cancelled RPC context.
13. **Transport failures are sticky `sandbox_unavailable`, but an oversized request is a per-call `arguments_too_large` that leaves the sandbox live.** A sandbox whose agent died has lost its state. An oversized request, by contrast, is detected before it is sent, and killing the sandbox over one large `write_file` would end the run for nothing. This deviates from PRD 11's acceptance list.
14. **The agent uses `NoGlobalExcludes`, an explicit environment, and a spill directory under its environment's `TMPDIR`.** The in-process `local` agent must not read the host's global git excludes or fall back to the host's `os.TempDir()`.
15. **The agent rebuilds its tool set after each sync.** TS-02-39 shows that the symbol table never revalidates unless it is marked, and `tools` exposes no way to mark it from outside.
16. **Spec checking is one function driven by `Capabilities`.** What a provider advertises and what it refuses then cannot drift apart ("no provider silently widens a policy").
17. **`local` refuses `Image`, `Workdir` and the zero network, and creates private `home/` and `tmp/` directories.** A test should not be able to assert isolation it does not have, and keeping its files out of the developer's real home directory is basic hygiene.
18. **Tar export is verbatim, with no `.gitignore` filtering, and escaping symlinks are refused in both directions.** Tar is the raw channel for embedders with their own packaging; ignore semantics belong to `LocalDir`. Refusing in both directions keeps a single rule.
19. **`os.Root` performs the extraction writes.** The standard library's `openat`-based confinement closes the gap between checking a path and opening it, which per-entry path checks cannot close, and steering prefers the standard library.
20. **`Sandbox.Agent()` is added.** `Capabilities` describes a provider before any sandbox exists, so it cannot carry per-sandbox facts such as the agent version. Later scopes add the Landlock ABI there.
21. **A protocol major mismatch is refused, any minor is accepted, and a missing RPC reports the agent's version.** Proto3 ignores unknown fields, so the only incompatibility a minor version can introduce is a new RPC, and the project has no warning channel to send a softer signal through.
22. **The gateway server will need a nested module at `sandbox/gatewayd/`, not at `sandbox/gateway/` as PRD 11 says.** A nested module at that path would take the root module's `sandbox/gateway` client package out of the root module.
23. **The conformance cases move to `internal/toolconformance`.** The `tools` suite and the proxy suite then share one case table and cannot drift.
24. **The `guard` and `tools.Workspace` documentation are deferred.** Saying that `AllowAll` is defensible, or that `execute` is contained, is false while `local` is the only provider.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `08_code_mode` | depends on | `codemode.New` binds proxy tools unchanged; its smoke tests are the model for this spec's code-mode integration test. |
| `09_repository_cut` | modifies | Its direct-dependency allowlist (`internal/policy/deps_test.go`, TS-09-16) gains `google.golang.org/grpc` and `google.golang.org/protobuf`. |
| `11_minimal_driver` | depends on | The batch executor (`Execute(ctx, Raw)`, `Sequential` demotion, the `aborted` result) and `New`'s unguarded-shell refusal are relied on unchanged. |
| `12_core_schema_guard` | depends on | The `core.Tool`/`ToolResult` vocabulary, `guard.IsShellTool`, and `schema` marshalling in authored order for byte comparison; TS-12-14's 20,000-line budget is a constraint this spec must meet. |

## Verified External API

**In-repository symbols this work calls (read):**
- `tools.All(opts Options) ([]core.Tool, error)`
- `tools.Options{Workspace *Workspace; SpillDir string; DisableSpill bool; Env []string; Ignore IgnoreOptions; Symbols SymbolOptions; Index Index}`
- `tools.SymbolOptions{MaxFiles int; MaxDuration time.Duration}`
- `tools.NewWorkspace(root string) (*Workspace, error)`
- `tools.NoGlobalExcludes() IgnoreOptions`
- `(*tools.Workspace).CheckWriteTarget(abs string) error`
- `wire.Guard(data []byte, l Limits) error`
- `wire.Defaults() Limits`, with `MaxMessageBytes` of 16 MiB
- `core.ErrResult(code, detail string) ToolResult`
- `core.ToolMetadata`, with JSON tags
- `core.ErrUnguardedExecute`
- `guard.IsShellTool(name string) bool`
- `codemode.New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error)`
- `faux.New(turns ...Turn) *Provider`

**`google.golang.org/grpc` v1.64.1** (in `go.mod`, indirect today). **Unverified**: the module source is outside the repository. These signatures are the ones assumed:
- `grpc.NewServer(opt ...ServerOption) *Server`
- `grpc.MaxRecvMsgSize(m int) ServerOption`
- `grpc.MaxSendMsgSize(m int) ServerOption`
- `(*Server).Serve(lis net.Listener) error`
- `(*Server).Stop()`
- `(*Server).GracefulStop()`
- `grpc.NewClient(target string, opts ...DialOption) (*ClientConn, error)`, available since v1.63
- `grpc.WithContextDialer(func(context.Context, string) (net.Conn, error)) DialOption`
- `grpc.WithTransportCredentials(credentials.TransportCredentials) DialOption`
- `insecure.NewCredentials() credentials.TransportCredentials`, in `google.golang.org/grpc/credentials/insecure`
- `grpc.WithDefaultCallOptions(...CallOption) DialOption`
- `grpc.MaxCallRecvMsgSize(int) CallOption`
- `grpc.MaxCallSendMsgSize(int) CallOption`
- `(*ClientConn).Close() error`
- `status.Code(err error) codes.Code`
- `status.Error(c codes.Code, msg string) error`
- `codes.Canceled`, `codes.Unavailable`, `codes.ResourceExhausted`, `codes.InvalidArgument`, `codes.FailedPrecondition`, `codes.NotFound`, `codes.Unimplemented`
- `grpc.SupportPackageIsVersion9`, which `protoc-gen-go-grpc` v1.5.1 output references and which requires grpc v1.64.0 or later

**`google.golang.org/protobuf` v1.36.11** (in `go.mod`, indirect today). **Unverified**: generated code uses `reflect/protoreflect` and `runtime/protoimpl`, from `protoc-gen-go` v1.36.11.

**Standard library** (Go 1.27 per `go.mod`). **Unverified**: the source is outside the repository.
- `os.OpenRoot(name string) (*os.Root, error)`
- `(*os.Root).OpenFile`, `Mkdir` and `Lstat` (Go 1.24)
- `MkdirAll`, `Symlink`, `RemoveAll` and `Chmod` (Go 1.25)
- `archive/tar` `NewReader`, `NewWriter`, `Header` and its `Typeflag` constants
- `crypto/sha256`
