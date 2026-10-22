---
spec_id: "19"
spec_name: "sandbox_gateway"
title: "Sandbox gateway: a remote provider, its gRPC service and the agentkit-gateway server"
status: "active"
created_at: "2026-10-13T15:46:48.669554Z"
updated_at: "2026-10-13T15:46:48.669554Z"
intent_hash: "35b0ae3deb883913feece22c8af7f633d6a77a1b586281311ce56469004252ce"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Let an embedder create and use sandboxes on another host through a gateway service running there. To the embedder and to the model, such a sandbox behaves the same as one created locally: the same `sandbox.Provider` and `sandbox.Sandbox` behaviour, the same proxy tools, the same errors. The gateway holds the backend provider and that provider's credentials. It authenticates every client with a bearer token and relays the agent protocol to each sandbox it manages. The embedder holds only the gateway's address and token, and the sandbox holds neither. The client sits beside the other providers in the sandbox module. The server is a separate nested module with its own command, so the library itself still listens on no socket.

## Goals

1. **Parity over a gateway, tested offline.** An in-process gateway server with the `local` backend, and a `gateway.Dial` client connected to it in the same test process, pass spec 13's first three acceptance criteria unchanged:
   - every proxy tool's `Name`, `Description`, `PromptGuidelines`, `Builtin`, `ExecutionMode` and `Label` equal those of `tools.All`, and both schemas marshal to byte-identical JSON;
   - the shared conformance table (`internal/toolconformance`) passes against the proxies;
   - a call cancelled from the host returns `aborted`, and the process it started is gone.

   In the same setup, a `LocalDir` `Sync` and `Export` and a `Tar` `Sync` and `Export` give the same workspace and destination trees as a direct sandbox does.
2. **Transport failures look the same as locally.** In-flight calls get `sandbox_unavailable`, and so does every later call on that sandbox, in each of three cases:
   - the client↔gateway connection is cut;
   - the backend agent's connection is cut (`localtest.Cut` on the backend sandbox);
   - the gateway destroys the sandbox.
3. **Authentication and ownership hold on every RPC.**
   - With no token or a wrong token, `Dial` fails and the error matches `gateway.ErrUnauthenticated`.
   - Every RPC of both services sent without a valid token is answered `Unauthenticated`, and the backend is never called.
   - A sandbox created with token A cannot be listed, relayed to or destroyed with token B. To token B it behaves like an id that does not exist.
4. **Nothing private reaches the agent.** An agent behind the gateway records the incoming metadata of every RPC it serves. It never sees the `authorization` header or the `agentkit-sandbox-id` key.
5. **Refusals keep their types.** Over the gateway, an unsupported spec field returns a `*sandbox.SpecError` that matches `sandbox.ErrUnsupported` and names the provider `gateway/<backend>` and the field. The same holds for each other kind of refusal:
   - a `GitRepo` on a backend that cannot clone matches `ErrUnsupported`;
   - a full gateway matches `gateway.ErrCapacity`;
   - a TTL above the gateway's maximum, or a network mode outside the gateway's ceiling, is refused before any RPC. It returns a `SpecError` naming `TTL` or `Network.Mode`.
6. **Nothing is left behind.** The backend sandbox (the `local` directory) is gone after each of these:
   - `Sandbox.Close`;
   - a failed handshake after `Create`;
   - a `Create` whose context ends mid-way;
   - `Provider.Close` with sandboxes still open;
   - `Server.Shutdown`;
   - expiry of a 2-second TTL.
7. **A real Podman backend works end to end (opt-in).** This needs a container engine and the locally built base image. Through the gateway:
   - `Create` under `NetworkNone`, a `LocalDir` `Sync`, proxy `read_file`, `write_file` and `execute`, and `Export` all work;
   - a `GitRepo` sync clones a public repository, and TCP from `execute` still fails afterwards;
   - after `Close` and after `Shutdown`, no container or volume with the label is left.
8. **The command works offline.** `agentkit-gateway` starts against a fake engine script and prints its listening line. A client can `Dial` it and sees capabilities named `gateway/podman`. On SIGTERM the command exits 0. Usage errors exit 2.
9. **The build stays as it is.**
   - No non-test file in the root or sandbox module calls a socket-listening API.
   - The `sandbox/gatewayd` module requires no module outside the allowlist, and none that the sandbox module's graph lacks.
   - `sandbox.ProtocolMinor` stays 2, and the exported APIs of `agentkit`, `core`, `tools` and `codemode` are unchanged.
   - `make check` needs no network, no engine and no `protoc`.

## Non-goals

- **Second-release work:** the Kubernetes gateway backend (`client-go`), the OpenShell provider, and code mode running inside the sandbox (PRD 11 D3 (c), the `RunScript` stream). Each gets its own PRD.
- **Other backends in the command.** `agentkit-gateway` drives Podman or Docker only. `local` would expose the gateway host unisolated, and `landlock` shares the host kernel, so neither is offered on a network service. The `gatewayd` library still accepts any first-party provider, which is how the tests use `local`.
- **Reattaching, leases and reconnecting.** There is no reattach to a sandbox from another process, no keep-alive lease that destroys a sandbox whose client went quiet, no persistent or shared sandboxes, and no transparent reconnect of a sandbox whose calls failed. A crashed embedder's sandboxes live until their TTL, unless it lists and destroys them.
- **Richer authentication and policy.** No mTLS client certificates, OIDC, token reload without a restart, per-token quotas, rate limiting or audit log beyond the structured stderr lines.
- **Egress control for the `GitRepo` init step on the gateway host.** It reaches whatever HTTPS URL the client names, exactly as on a local Podman host. The documentation says so.
- **Publishing the gateway.** No release automation, binaries, images or Helm chart. It is built from a checkout with `make sandbox-gateway`.
- **Relaying Agent RPCs the gateway's own build does not know.** A newer agent RPC answers `Unimplemented` until the gateway is upgraded (DD 4).
- **HA, load balancing or more than one backend per gateway.**
- **Changing the agent, the Agent service, the images or the existing providers' behaviour.** The only change to providers is an internal `Unwrap` method (Requirement 6).
- **A gateway flag in `examples/sandbox`.**
- **Windows parity for the server.** The module builds and vets on windows/amd64. Its behaviour there is best effort (steering).

## Background

**What PRD 11 decided for this scope (§5, D6).** A gateway implements the provider operations on a remote host and relays the agent service to each sandbox it started. The sandbox id travels in request metadata. The client is `gateway.Dial(url, token)`, beside the other providers. The server is a long-running network service in a nested module, with `cmd/agentkit-gateway`. It holds one backend provider, enforces TTL and Reap for everything it created, and authenticates with a bearer token per connection. TLS is the deployer's job: a reverse proxy, or the server's own certificate flags. Trust takes one hop: the gateway holds the backend's credentials, the host holds the gateway's token, and the sandbox holds neither.

**What the code provides today.**

*The module layout.* `sandbox/` is a nested module, `github.com/agent-fox-dev/agentkit-go/sandbox`, which requires the root through `replace ../` (`sandbox/go.mod`; `docs/errata/14_sandbox_rsync_sync.md` D-2). PRD 11's "client in the root module" therefore means the module that holds the sandbox API. `.specs/13_sandbox_core` DD 22 already settled that the server's nested module sits at `sandbox/gatewayd/`, not `sandbox/gateway/`, so that the client package keeps the `sandbox/gateway` path. The Makefile's `MODULES` lists `. codesearch sandbox examples/sandbox`. `internal/policy` checks three things for those modules:
- the build gates (`crosstarget_test.go`, `gateModules`);
- the allowlist (`deps_test.go`, `sandboxModules`);
- the generated protocol files (`proto_test.go`, `protoFiles`).

*attach was written for this scope.* `sandbox/internal/attach/attach.go` says it is "written against a gRPC client connection plus per-call outgoing metadata, so a provider holding a net.Conn to an agent (Dial) and a later gateway client (New over its own connection) share it".
- `attach.New(ctx, cc grpc.ClientConnInterface, closer io.Closer, Config{ID, Spec, Destroy, Metadata, Provider})` performs the handshake, the protocol-major check, the `RequireLandlock` check and the TTL timer.
- Every agent RPC goes through `s.outgoing(ctx)`, which attaches `Config.Metadata`.
- The connection watcher runs only when `cc` is a `*grpc.ClientConn`.
- `classify` (`state.go`) treats `InvalidArgument`, `FailedPrecondition`, `NotFound` and `Unimplemented` as deliberate refusals that leave the sandbox live. Any other RPC error is a transport failure and makes the sandbox unavailable.
- `attach.ClientConn(s)` returns the agent connection of a sandbox that attach created.

*Relaying already works.* `sandbox/internal/relaytest` is a typed relay. It embeds `sandboxpb.UnimplementedAgentServer` and forwards `Hello`, `Tools`, `Call`, `SyncTar`, `ExportTar` and `Rsync`. `TestRelayedTransfers_TS14_6` (`sandbox/relay_test.go`) shows that a relayed `LocalDir` transfer equals a direct one. `docs/DEPS.md` chose the in-process rsync library partly so that "a gateway can relay them like any other stream".

*The protocol.* `sandbox/internal/sandboxpb/sandbox.proto` holds package `agentkit.sandbox.v1` with one `Agent` service, at protocol 1.2.
- TS-13-1 asserts there is exactly one service in that file.
- `make proto` generates `sandbox.pb.go` and `sandbox_grpc.pb.go` with pinned plugins and records `sandbox.proto.sha256`, which TS-13-5 checks offline.
- `sandbox.MaxMessageBytes` is 16 MiB.

*Providers.*
- `local.Unisolated()` returns attach's sandbox directly and registers it with `localtest`, so `localtest.Cut` can sever its agent connection. Its capabilities are `NetworkUnrestricted` only, with no `Image`, `Workdir`, `Resources` or `Landlock`.
- `sandbox/podman` (spec 17) and `sandbox/landlock` (spec 18) are specified but not yet in the tree, and are implemented before this spec. 17 delivers:
  - `podman.New(podman.Options{Binary, Engine, Runtime, UserNS, EngineEnv})`;
  - `sandbox.GitRepo{URL, Ref, Depth}`, run as a provider-side init step through the provider's own wrapper sandbox type, with a "GitRepo slot";
  - `sandbox.ErrNoAgent`;
  - `sandbox/internal/gitclone.Check`;
  - the internal accessor `attach.Live(sandbox.Sandbox) error`.
- Both provider wrappers hide attach's sandbox, so `attach.ClientConn` returns nil for them today.

*Versions and images.* `sandbox/internal/buildver` reports a link-time stamp, else the build info's sandbox-module version, else `(devel)`. `sandbox.DefaultImage()` turns that into the base image tag. Spec 16 notes that "the Tools digest check needs the host and the agent at the same version".

*Documentation.* `README.md` and `docs/architecture.md` say that "the library listens on no socket; `agentkit-sandboxd` serves the agent service on its stdio". `docs/cli.md` says that `agentkit-sandboxd` is AgentKit's one command.

## Requirements

### 1. The gateway protocol

Add `sandbox/internal/sandboxpb/gateway.proto`, a second file of package `agentkit.sandbox.v1` with the same `go_package`. It holds one service, `Gateway`, with six unary RPCs:
- `Capabilities`;
- `Create`;
- `SyncGit`;
- `List`;
- `Destroy`;
- `Reap`.

**Messages.**
- **`SandboxSpec`** mirrors `sandbox.Spec` field for field:
  - `Env` and `Labels` are string maps;
  - `Resources` keeps its four fields;
  - the network mode is an enum numbered like `sandbox.NetworkMode` (none 0, allowlist 1, unrestricted 2), and `Allow` is a list of host and port;
  - the TTL is in nanoseconds;
  - `RequireLandlock` is a bool;
  - `Spec.Provider` travels as JSON bytes.
- **Times** are Unix nanoseconds. There are no well-known types.
- **`CapabilitiesResponse`** carries:
  - the gateway protocol version and the gateway's build version;
  - the backend's `sandbox.Capabilities` field for field, with `Network` already limited to the gateway's network ceiling;
  - the gateway's maximum TTL and maximum number of sandboxes.
- **`CreateRequest`** carries a `SandboxSpec`. **`CreateResponse`** carries either a refusal, or the id, the effective spec, the creation time and the expiry time.
- **`SyncGitRequest`** carries the id, URL, ref and depth. **`SyncGitResponse`** carries an optional refusal.
- **`ListResponse`** carries, per sandbox, the id, labels, creation time and expiry time.
- **`DestroyRequest`** carries the id.
- **`ReapResponse`** carries the removed ids and an error text, which is empty when every removal succeeded.
- **`Refusal`** has a kind (`UNSUPPORTED`, `NO_AGENT`, `NO_PUBLISHED_IMAGE`, `PROTOCOL`, `CAPACITY` or `REFUSED`), an optional spec field, a reason and a message.

**Metadata.**
- Every RPC of both services carries `authorization: Bearer <token>`.
- Every `Agent` RPC sent to a gateway carries `agentkit-sandbox-id: <id>`.
- Gateway RPCs carry the id in their message, never in metadata.

**Versions.**
- The Agent service and `sandbox.proto` do not change, and `sandbox.ProtocolMinor` stays 2.
- The gateway service has its own version: `gateway.ProtocolMajor = 1` and `gateway.ProtocolMinor = 0`.
- Fields are added and never renumbered, and any addition bumps the gateway minor version.

**Generated code.**
- `make proto` also generates `gateway.pb.go` and `gateway_grpc.pb.go`, using the same plugins and the same `(unknown)` header rewrite.
- It records `gateway.proto.sha256`.
- A test in the package fails, with "run make proto", when `gateway.proto` hashes differently.
- The policy proto tests list the new files.
- TS-13-1 keeps passing unchanged, because `sandbox.proto` still holds exactly one service.

### 2. The client: `gateway.Dial` and the provider

Package `gateway` (`sandbox/gateway`, in the sandbox module) exports these:
- `Dial(ctx context.Context, rawURL string, o Options) (*Provider, error)`;
- `Options{Token string; TLSConfig *tls.Config; Dialer func(context.Context, string) (net.Conn, error)}`;
- `Provider`, which implements `sandbox.Provider`, with the extra methods `Gateway() GatewayInfo`, `List(ctx) ([]Info, error)`, `Destroy(ctx, id string) error` and `Close() error`;
- `Info{ID string; Labels map[string]string; Created, Expires time.Time}`;
- `GatewayInfo{URL, Version, Backend string; ProtocolMajor, ProtocolMinor uint32; MaxTTL time.Duration; MaxSandboxes int}`;
- `ErrUnauthenticated`, `ErrCapacity` and `ErrClosed`;
- the protocol constants and `SandboxIDKey = "agentkit-sandbox-id"`.

**The URL.**
- `https://host[:port]` uses default port 443 and TLS.
- `http://host[:port]` uses default port 80 and plaintext. It is accepted only when the host is `localhost` or a loopback IP literal.
- A URL with user information, a query, a fragment, or a path other than empty or `/` is refused. So is any other scheme, and a non-loopback `http` host.
- Each refusal names the problem, and no connection is attempted.

**The token** is required. It must be printable ASCII without whitespace; otherwise `Dial` refuses it before connecting.

**The transport.**
- For `https`, the client uses a clone of `TLSConfig`. When that is nil, it uses the system roots with a minimum of TLS 1.2. `ServerName` defaults to the URL's host.
- The send and receive limits are `sandbox.MaxMessageBytes`.
- `Dialer`, when set, replaces TCP. The URL rules still apply.
- The token is sent as per-RPC credentials, which require transport security for `https`.

**The handshake.** `Dial` calls `Capabilities` under `ctx`. On any of these failures it closes the connection:
- `Unauthenticated` returns an error matching `ErrUnauthenticated`;
- a different gateway protocol major returns an error wrapping `sandbox.ErrProtocol` that names both versions;
- any other failure returns an error naming the URL.

**Capabilities.** `Capabilities()` returns the cached answer:
- `Provider` is `"gateway/" + <backend provider>`;
- `Network` is the offered modes;
- `Isolation`, `Resources`, `Image`, `Workdir` and `Landlock` are the backend's;
- `Notes` is the backend's notes, followed by one sentence naming the gateway's host, its maximum TTL and its maximum number of sandboxes.

**The other methods.**
- `Reap` calls `Gateway.Reap` and returns the sorted ids, plus an error carrying the server's error text when there was one.
- `List` returns the caller's sandboxes, sorted by creation time and then id.
- `Destroy` is idempotent. An unknown id returns nil.
- `Close` closes every sandbox created through this provider that is still open, which destroys each of them, and then closes the connection. It returns the joined errors and is idempotent. Afterwards `Create`, `Reap`, `List` and `Destroy` return `ErrClosed` without an RPC.
- An `Unauthenticated` answer to any RPC returns an error matching `ErrUnauthenticated`.

### 3. The client: `Create` and the sandbox it returns

`Create(ctx, spec)` runs these steps:

1. **Local checks, with no RPC.**
   - `sandbox.CheckSpec(p.Capabilities(), spec)` runs first.
   - An effective TTL above the gateway's maximum is refused with a `*sandbox.SpecError` naming `TTL`.
   - A `Spec.Provider` that does not encode as JSON is refused with a `SpecError` naming `Provider`.
   - When `Spec.Image` is empty and the capabilities honour `Image`, the client fills in its own `sandbox.DefaultImage()`. That function's `ErrNoPublishedImage` error is returned unchanged.
2. **`Gateway.Create`.** A refusal becomes an error that carries the server's message and matches the sentinel for its kind:
   - `UNSUPPORTED` becomes a `*sandbox.SpecError{Provider: <client's provider name>, Field, Reason}`. When there is no field, it becomes an error wrapping `ErrUnsupported`.
   - `NO_AGENT` wraps `sandbox.ErrNoAgent`.
   - `NO_PUBLISHED_IMAGE` wraps `sandbox.ErrNoPublishedImage`.
   - `PROTOCOL` wraps `sandbox.ErrProtocol`.
   - `CAPACITY` wraps `ErrCapacity`.
   - `REFUSED` is a plain error.

   A gRPC error becomes an error naming the gateway.
3. **Attach.** The client calls `attach.New` over the provider's shared connection, wrapped so that it is not a `*grpc.ClientConn` and the connection watcher does not run, and with a no-op closer. The `Config` is:
   - `ID`: the gateway's id;
   - `Spec`: the returned effective spec;
   - `Metadata`: `{agentkit-sandbox-id: id}`;
   - `Provider`: the client's provider name;
   - `Destroy`: a `Gateway.Destroy(id)` call.

   When the handshake fails, attach runs that destroy step. When `ctx` ends after `Gateway.Create` succeeded but before `Create` returns, the client destroys the sandbox itself.

**The returned sandbox.**
- `ID()` is the gateway's id. `Spec()` is the effective spec, with `Workdir` as the agent reported it. `Agent()` is the relayed `Hello`.
- `Tools`, `Export`, `Sync` of a `Tar` or `LocalDir`, and `Close` are attach's, unchanged: the same proxies, the same state machine and the same errors.
- `Sync` of a `GitRepo` runs these steps:
  1. it checks `attach.Live`;
  2. it runs `gitclone.Check`, refusing an invalid repository before any RPC;
  3. it calls `SyncGit` under `ctx`, mapping a refusal as in step 2 above;
  4. on success it returns a zero `SyncResult`, as `podman` does.
- `Close` runs attach's teardown, which sends `Gateway.Destroy` with attach's fresh 30-second context. A failed `Destroy` is returned, and the sandbox then lives on the gateway until its expiry.

### 4. The server: library, authentication and ownership

The nested module `github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd` lives at `sandbox/gatewayd`. Its package `gatewayd` exports:
- `Config{Backend sandbox.Provider; Tokens []string; TLS *tls.Config; NetworkModes []sandbox.NetworkMode; MaxSandboxes int; MaxTTL, ReapInterval time.Duration; Logger *slog.Logger}`;
- `New(Config) (*Server, error)`;
- `(*Server).Serve(net.Listener) error`;
- `(*Server).Shutdown(context.Context) error`.

**`New` validates the config.**
- `Backend` must be set.
- There must be at least one token. Each token must be at least 32 characters of printable ASCII without whitespace, and no token may repeat.
- `NetworkModes` defaults to `[NetworkNone]`. Its intersection with the backend's modes must not be empty; otherwise `New` returns an error naming both lists.
- `MaxSandboxes` defaults to 16, `MaxTTL` to 24h and `ReapInterval` to 10m. Negative values are refused.
- `Logger` defaults to discarding.

**`Serve`** runs a gRPC server:
- with `sandbox.MaxMessageBytes` as the send and receive limits;
- with TLS credentials when `Config.TLS` is set, and plaintext otherwise;
- with the `Gateway` service and the Agent relay (Requirement 6) registered;
- with an authentication interceptor on every unary and streaming RPC.

It calls `Backend.Reap` once at start and then every `ReapInterval`; a failure is logged, never fatal. It is called once. It returns nil after `Shutdown`, and the listener's error otherwise.

**Authentication.**
- Every RPC must carry `authorization: Bearer <token>` matching a configured token, compared in constant time.
- Otherwise the answer is `Unauthenticated`, nothing else in the request is read, and the backend is not called.
- The server keeps only each token's SHA-256 as the **owner** identity. Logs show the first 8 hex characters of it.

**Ownership.**
- Every sandbox belongs to the owner that created it.
- Agent RPCs, `SyncGit`, `Destroy` and `List` see only the caller's sandboxes. Another owner's id behaves exactly like an unknown id.

**Logging.** The server logs one structured line for each of these:
- a create, with the id, the owner fingerprint, the TTL and the network mode;
- a destroy, with the id and the reason: `client`, `expired`, `unavailable` or `shutdown`;
- a refused authentication, with the peer address;
- each backend reap.

It never logs a token, an `Env` value, a tool input or a tool result.

### 5. The server: sandbox lifecycle

**Create** runs these steps in order:

1. **Decode.** A malformed request, or an unknown enum value, is answered `InvalidArgument`. The `Spec.Provider` JSON must pass `wire.Guard` with the default limits.
2. **The network ceiling.** A mode outside the ceiling is refused as `UNSUPPORTED`, field `Network.Mode`.
3. **The TTL.** A TTL above `MaxTTL` is refused as `UNSUPPORTED`, field `TTL`. A zero TTL counts as 1h.
4. **Capacity.** Entries whose backend sandbox is no longer live are pruned first. Then, when recorded sandboxes plus creates already in progress reach `MaxSandboxes`, the request is refused as `CAPACITY`.
5. **The backend.** It calls `Backend.Create(rpcCtx, spec)`. An error is mapped as follows:
   - a `*sandbox.SpecError` becomes `UNSUPPORTED` with its field and reason;
   - another error matching `ErrUnsupported` becomes `UNSUPPORTED`;
   - `ErrNoAgent` becomes `NO_AGENT`;
   - `ErrNoPublishedImage` becomes `NO_PUBLISHED_IMAGE`;
   - `ErrProtocol` becomes `PROTOCOL`;
   - anything else becomes status `Unavailable`, carrying its message.
6. **Relayability.** When the backend sandbox exposes no agent connection through attach's accessor (Requirement 6), the server closes it and answers `Internal`, naming the backend.
7. **A caller who has gone.** When the RPC context is done by the time the backend returns, the server closes the sandbox and records nothing.
8. **Record and answer.** The server records the entry: owner, creation time, `expires = created + effective TTL`, and the effective spec. It answers with the backend sandbox's `ID()` and `Spec()`, the creation time and the expiry.

**Removal.** An entry is removed and its backend sandbox closed in each of these cases:
- at its expiry;
- when any access finds the backend sandbox no longer live;
- on `Destroy`;
- on `Shutdown`.

**Destroy, List and Reap.**
- **`Destroy`** of an unknown id, or of another owner's id, answers OK and does nothing. When the backend's `Close` fails, the entry is removed anyway and the answer is `Unavailable`, carrying the error.
- **`List`** returns the caller's entries.
- **`Reap`** runs `Backend.Reap`. It returns the ids, plus the joined error text when a removal failed. The backend's own rule decides what counts as abandoned; for `podman` that rule is "expired".

**`Shutdown(ctx)`** runs these steps:
1. it stops accepting new RPCs;
2. it closes every backend sandbox it holds, which ends the relayed calls in flight;
3. it waits for the handlers to return until `ctx` is done, then forces the server to stop.

It returns the joined errors and is idempotent.

### 6. The server: the Agent relay and the `GitRepo` slot

The server registers an `Agent` service implementation that relays `Hello`, `Tools`, `Call`, `SyncTar`, `ExportTar` and `Rsync` to the agent of the sandbox named by `agentkit-sandbox-id`.

**Routing.**
- In each of these cases the relay answers `Unavailable`, naming the id:
  - the id is missing or malformed;
  - the id is unknown;
  - the id belongs to another owner;
  - the backend sandbox is not live. In this case the server also closes the sandbox and removes it.
- The relay never answers a condition on the gateway's side with `InvalidArgument`, `FailedPrecondition`, `NotFound` or `Unimplemented`. Those codes stay reserved for the agent's own refusals.

**Forwarding.**
- The relay uses the backend sandbox's own agent connection.
- It forwards with the incoming RPC's context, so cancellation and deadlines propagate.
- It forwards none of the incoming metadata.
- Messages are forwarded unchanged, unknown fields included.
- Streams are copied frame by frame in both directions, and a half-close is propagated.

**Errors.**
- The agent's `InvalidArgument`, `FailedPrecondition`, `NotFound` and `Unimplemented` pass through with their messages.
- Any other upstream error becomes `Unavailable`, carrying the upstream message.
- An Agent RPC that the gateway's build does not know answers `Unimplemented`.

**The `GitRepo` slot**, per sandbox, follows spec 17's rules exactly.
- These take the slot:
  - a successful `SyncGit`;
  - the first relayed `Tools`, whatever its outcome;
  - a relayed `SyncTar` that succeeds;
  - a relayed `Rsync` whose start frame says `SYNC` and that ends OK.
- A `SyncGit` while the slot is taken is refused as `REFUSED`, with "GitRepo must be the first Sync and come before Tools".
- `SyncGit` and the slot-taking relays are serialised against each other.
- `SyncGit` calls `Sync(ctx, GitRepo)` on the backend sandbox. An error matching `ErrUnsupported` becomes `UNSUPPORTED`; any other error becomes `REFUSED`, carrying its message.
- A failed `SyncGit` does not take the slot.

**Reaching the agent connection.**
- `attach.ClientConn` and `attach.Live` follow a sandbox's `Unwrap() sandbox.Sandbox` chain.
- The wrapper types of `sandbox/podman` and `sandbox/landlock` gain an `Unwrap` method that returns the attached sandbox. `local` already returns it directly.
- No exported identifier of any package changes.

### 7. The `agentkit-gateway` command

The command lives at `sandbox/gatewayd/cmd/agentkit-gateway` and uses the standard `flag` package.

**Flags.**

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:8443` | The address to listen on. |
| `--token-file` | none; required | The file holding the accepted tokens. |
| `--tls-cert`, `--tls-key` | | The server's own certificate and key. Both or neither. |
| `--plaintext` | | Serve without TLS. Required when the TLS flags are absent, and refused together with them. |
| `--engine-binary` | `podman` | The engine command. |
| `--engine` | empty | `podman` or `docker`; empty means detected. |
| `--runtime` | empty | Empty, or `krun`. |
| `--userns` | | The user-namespace setting passed to the engine. |
| `--network` | `none` | A comma-separated list drawn from `none` and `unrestricted`. |
| `--max-sandboxes` | `16` | The most sandboxes held at once. |
| `--max-ttl` | `24h` | The longest TTL accepted. |
| `--reap-interval` | `10m` | How often the backend is reaped. |
| `--version` | | Print the version and exit. |

**The token file.**
- It holds one token per line. Blank lines and lines starting with `#` are ignored, and surrounding whitespace is trimmed.
- The token rules of Requirement 4 apply.
- Tokens are never accepted from a flag or the environment.

**The backend** is `podman.New(podman.Options{Binary, Engine, Runtime, UserNS})`, with the command's own environment as the engine's environment.

**Startup.** After binding, the command prints one line to stderr:

```
agentkit-gateway: listening on <bound address> (<tls|plaintext>), backend <name>, version <v>
```

With `--plaintext` on a non-loopback address, it also logs a warning line.

**Shutdown.** SIGINT or SIGTERM runs `Shutdown` with a 60-second budget. The command then exits 0, or 1 if destroying any sandbox failed.

**Exit codes.** Every failure is one line on stderr, `agentkit-gateway: <cause>`.

| Exit | When |
|---|---|
| 0 | A clean shutdown, or `--version`. |
| 1 | A startup failure: a token file that cannot be read or is invalid, invalid TLS files, an error from the backend's `New`, or a failure to listen. |
| 2 | A usage error: an unknown flag, no `--token-file`, half a TLS pair, `--plaintext` together with TLS, or neither of them, a bad engine, runtime or network value, a non-positive duration or count, or an extra argument. |

**`--version`** prints `agentkit-gateway <version> (gateway protocol M.N, agent protocol M.N)`. The version is `buildver.Version()`.

### 8. Module, build and policy wiring

**`sandbox/gatewayd/go.mod`** is a separate module with `go 1.27`.
- It requires the root module and the sandbox module through `replace ../../` and `replace ../`.
- Its direct requirements are only those two modules and gRPC, plus protobuf if it imports protobuf directly.
- Its `go list -m all` contains no module that the sandbox module's graph lacks.
- It imports the sandbox module's internal packages (`sandboxpb`, `attach`) under Go's import-path rule for `internal`.

**Makefile.**
- `MODULES` and the `doc` loop gain `sandbox/gatewayd`.
- `make sandbox-gateway` builds `build/bin/agentkit-gateway` with `-trimpath`. When `AGENTKIT_VERSION` is set, it also passes `-ldflags -X …/sandbox/internal/buildver.stamp=$(AGENTKIT_VERSION)`.
- `make sandbox-gateway-test` depends on `sandbox-images`. It runs the opt-in Podman tests in `sandbox/gatewayd` with the image and engine variables 16's target uses. It is not part of `make check`.

**Policy.**
- `gateModules` gains `sandbox/gatewayd`, so it is built and vetted on all four targets with cgo off, and on the host with cgo on.
- `sandboxModules` gains the gatewayd module path. A test checks that its direct set is on the allowlist and within the set stated above.
- A new test fails when any non-test Go file of the root or sandbox module refers to `net.Listen`, `net.ListenTCP`, `net.ListenUnix`, `net.ListenPacket`, `net.ListenConfig`, `tls.Listen` or `http.ListenAndServe*`. Only the gatewayd module may.

**What stays unchanged:**
- the root module's and the sandbox module's direct requirements and graphs;
- the exported-API golden;
- `sandbox.ProtocolMinor`.

### 9. Tests

**Offline, in `make check`.**

*In the gatewayd module.* These tests run the server in-process with an in-memory listener, and the client in the same process (`Options.Dialer` with an `http://127.0.0.1` URL).
- **Parity:** Goal 1 and Goal 2, over `local`, with `NetworkModes: [NetworkUnrestricted]`.
- **Refusals over `local`:**
  - `RequireLandlock`, `Image` and `NetworkNone` are refused on the client, naming `gateway/local`;
  - a `GitRepo` is refused with `ErrUnsupported`.
- **The slot rules,** with a test backend that wraps `local`, simulates the clone, and implements `Unwrap`.
- **Authentication:** a raw RPC without a token on each service, and a counting backend that shows `Create` was never called.
- **Ownership:** sandboxes created under two tokens.
- **Metadata hygiene:** a backend whose agent records incoming metadata.
- **Limits and lifecycle:**
  - capacity, including concurrent creates;
  - `MaxTTL` and the network ceiling;
  - a `Create` cancelled mid-way;
  - a handshake failure that destroys the sandbox;
  - `Provider.Close`, `Shutdown`, and expiry at a short TTL.
- **Error mapping:** an agent `NotFound` (export of a missing path) leaves the sandbox live.

*In the sandbox module.* Client unit tests run against a fake `Gateway` server built from the generated stubs. They cover:
- the URL and token rules;
- the version refusal;
- the refusal-to-sentinel mapping;
- `List`, `Destroy`, `Reap` and `Close`.

*The command* (not on Windows).
- Usage exit codes.
- Token-file failures.
- A fake engine script on `PATH` that prints `podman version 5.0.0`: the command starts, prints its listening line, accepts a `Dial`, reports `gateway/podman`, and exits 0 on SIGTERM.

**Opt-in, with an engine.** These tests skip under `-short`, without an engine, or without `AGENTKIT_SANDBOX_BASE_IMAGE`. They choose the engine as 16 and 17 do. They cover Goal 7, plus `Shutdown` destroying the sandboxes that are still live.

### 10. Documentation

Per steering, run `make doc` first, then update the following. No test checks the presence or content of the docs.

- **`README.md`:**
  - the Sandboxes section gains the gateway, with a short `gateway.Dial` example;
  - the socket sentence becomes "the library listens on no socket; `agentkit-sandboxd` serves the agent service on its stdio, and `agentkit-gateway`, a separate module, is the one network service";
  - the packages table gains `sandbox/gateway` and `sandbox/gatewayd`;
  - the make-targets table gains `sandbox-gateway` and `sandbox-gateway-test`.
- **`docs/architecture.md`:**
  - "Shape" lists four nested modules;
  - the network-API paragraph is updated;
  - the package graph gains rows for the new packages;
  - the Sandboxes section gains a "The gateway" part covering the topology, the typed relay, metadata hygiene, ownership, the slot, how transport failures map to the state machine, and the invariant "no code in the root or sandbox module listens on a socket".
- **`docs/configuration.md`:**
  - `gateway.Options`, `Dial` with its URL and TLS rules, and the `Provider` methods;
  - `gatewayd.Config`;
  - the network ceiling and `MaxTTL`;
  - a gateway column in "Which provider honours which field".
- **`docs/cli.md`:** an `agentkit-gateway` section with its flags, the token-file format and the exit codes.
- **`docs/DEPS.md`:** the gRPC and protobuf rulings name the gateway client and the gatewayd module as users. No module is added.
- **`docs/releasing.md`:** the gateway is not published, and `make sandbox-gateway AGENTKIT_VERSION=…` stamps the version.

## Design Decisions

1. **The client lives in the sandbox module.** PRD 11 wrote "root module" before spec 14 turned `sandbox/` into a nested module. The sandbox module is now where the API and every provider live.
2. **The server is the nested module `sandbox/gatewayd`, with its command at `sandbox/gatewayd/cmd/agentkit-gateway`.** Spec 13 DD 22 fixed the path so that `sandbox/gateway` stays the client package. Go's `internal` rule in module mode goes by import path, so the server can reuse `sandboxpb` and `attach` without exporting them.
3. **The `Gateway` service is a second `.proto` file in `agentkit.sandbox.v1`, with its own hash and its own version constants.** The Agent protocol and its minor version stay untouched, agents need no rebuild, and TS-13-1's "exactly one service in `sandbox.proto`" still holds.
4. **The relay is typed, not a transparent byte proxy.** The gateway has to read the `Rsync` direction and the RPC outcomes to keep the `GitRepo` slot, normalise status codes, and strip metadata. Typed forwarding still preserves unknown fields. relaytest and TS-14-6 already show that it works. The cost is that a new Agent RPC needs a gateway upgrade.
5. **Gateway-side conditions are always `Unavailable`, and only the agent's own refusals keep the four refusal codes.** attach's `classify` decides liveness by status code. An unknown or destroyed sandbox must therefore read as a transport failure, never as a refusal that leaves a dead sandbox "live".
6. **The id is the backend sandbox's id. It travels in metadata on Agent RPCs and in the message on Gateway RPCs.** PRD 11 puts the id in metadata for Agent RPCs, and `attach.Config.Metadata` already sends metadata on every RPC. Reusing the backend id keeps the labels on the gateway host meaningful.
7. **`GitRepo` becomes the Gateway RPC `SyncGit`, and the server keeps spec 17's slot rules itself.** The init step is a backend operation, not an agent RPC. Relayed RPCs bypass the backend wrapper's own bookkeeping, so the backend's slot would never be taken.
8. **The client is `Dial(ctx, url, Options) (*Provider, error)` rather than PRD 11's `Dial(url, token)`.** `Capabilities()` has neither a context nor an error, so the capabilities must be fetched, and the token checked, at `Dial`. A `Close` method is needed for the shared connection. Every other provider takes an `Options` struct.
9. **`https` is used everywhere, and `http` only on loopback; the token travels as per-RPC credentials.** A bearer token must never cross a network in clear text. Loopback plaintext covers tests and a co-located TLS proxy.
10. **One shared connection per provider, with no watcher; liveness is decided per RPC.** The agent's state lives on the gateway. A network blip with no call in flight loses nothing, while calls that fail in flight make the sandbox unavailable, as PRD 11's no-reconnect rule asks.
11. **A sandbox belongs to the token that created it, and another token sees it as nonexistent.** Several embedders can share a gateway without being able to reach or enumerate each other's sandboxes, and no existence oracle leaks. Only token hashes are kept.
12. **`Reap` delegates to the backend's rule, and a crashed embedder cleans up with `List` and `Destroy` or waits for the TTL.** The gateway cannot tell whether a quiet client process is still alive. Spec 17's expiry rule is the one that can never destroy a live sandbox. Leases are a non-goal.
13. **When `Spec.Image` is empty, the client fills in its own `DefaultImage()`, if the backend honours `Image`.** The client's `Tools` digest check compares the agent with the client's own `tools.All`. The image must therefore match the embedder's version, not the gateway's (spec 16's rationale).
14. **The capabilities are named `gateway/<backend>`, pass the backend's isolation through, and offer the network modes within the gateway's ceiling.** Errors and the guard guidance (which looks at `Isolation`) then stay truthful, and the spec checks on the client agree with the server's.
15. **The network ceiling defaults to `none`, in both the library and the command.** On a shared host, letting any token holder open unrestricted egress from the operator's network should be the operator's explicit choice. The tests opt in to `unrestricted` because `local` supports nothing else.
16. **`MaxSandboxes` defaults to 16 and `MaxTTL` to 24h.** A network service must bound what any token holder can consume. PRD 11 asks that a TTL is never unbounded.
17. **`Provider.Close` destroys the sandboxes it created.** Closing the connection first would strand them until their TTL. `mcp.Pool.Close` sets the same pattern.
18. **Expiry uses a per-entry timer, plus pruning on `Create` and a liveness check on every access, plus a periodic backend `Reap`.** That bounds capacity leaks without a sweeper goroutine per sandbox. The periodic reap catches what a crashed gateway left behind.
19. **`Shutdown` destroys every sandbox.** Persistent sandboxes are a PRD 11 non-goal, and a restarted gateway has no way to reattach to them.
20. **Refusals travel as a typed `Refusal` message in the response, not as gRPC status details.** That keeps the mapping to `ErrUnsupported`, `ErrNoAgent` and the other sentinels explicit and testable, without depending on `status.WithDetails` interoperability.
21. **The server checks everything again; the client's checks only give early feedback.** The server is the trust boundary, and a client may be older, newer or not AgentKit at all.
22. **The command serves Podman and Docker only. It needs TLS or an explicit `--plaintext`, takes tokens only from a file, and listens on loopback by default.** A network service must not expose an unisolated backend. Tokens on a command line or in the environment leak through process listings. The defaults should be safe without any flags.
23. **attach's accessors follow an `Unwrap()` chain, and the podman and landlock wrappers implement it.** The relay needs the raw agent connection behind a provider's wrapper. An unexported type's method changes no exported surface.
24. **A policy test pins "no socket listening outside gatewayd".** D6's promise that the library listens on no socket stays a checked invariant rather than prose.
25. **The end-to-end tests live in the gatewayd module, and the client unit tests in the sandbox module.** Only the gatewayd module can import both ends without a module cycle.
26. **The gateway's version comes from `buildver`, stamped by `make sandbox-gateway`.** This reuses the agent's mechanism. The version is informational, because the image is chosen by the client (DD 13).

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | depends on, modifies | Builds on `attach.New` and `Config.Metadata`, `classify`, the state machine, the proxies, `sandboxpb` and `make proto`, `CheckSpec` and `SpecError`, `local`, `localtest.Cut` and `internal/toolconformance`. It extends `attach.ClientConn` to unwrap, and `make proto` to a second file. |
| `14_sandbox_rsync_sync` | depends on | `LocalDir` `Sync` and `Export` ride the relayed `Rsync` stream. The slot reads `RsyncStart.Direction`. |
| `15_sandbox_landlock_agent` | depends on | `Capabilities.Landlock`, `RequireLandlock` and the `LandlockReport` in `Hello` pass through the gateway unchanged. |
| `16_sandbox_images` | depends on | `DefaultImage`, `buildver`'s stamp, `make sandbox-images`, and the image and engine variables the opt-in tests reuse. |
| `17_sandbox_podman` | depends on, modifies | `podman.New` is the command's backend. `sandbox.GitRepo`, `gitclone.Check`, `sandbox.ErrNoAgent`, `attach.Live` and the slot rules are reused. Its wrapper type gains `Unwrap`. |
| `18_sandbox_landlock_provider` | modifies | Its wrapper type gains `Unwrap`, so the `gatewayd` library can relay to any first-party provider. |
| `09_repository_cut` | modifies | The policy tests it owns gain the gatewayd module (`sandboxModules`, `gateModules`), its direct-set check, and the no-listen check. |

## Verified External API

**In-repository code (read):**
- `attach.New(ctx context.Context, cc grpc.ClientConnInterface, closer io.Closer, cfg attach.Config) (sandbox.Sandbox, error)`, with `attach.Config{ID string; Spec sandbox.Spec; Destroy func(context.Context) error; Metadata metadata.MD; Provider string}`. `Destroy` runs with a fresh 30-second context on any failure and on `Close`. The watcher starts only for a `*grpc.ClientConn`.
- `attach.ClientConn(s sandbox.Sandbox) grpc.ClientConnInterface`, which today returns nil for a sandbox attach did not create.
- `localtest.Cut(sb sandbox.Sandbox) error`.
- `local.Unisolated() sandbox.Provider`.
- `sandbox.CheckSpec(caps Capabilities, spec Spec) (Spec, error)`, `sandbox.SpecError{Provider, Field, Reason string}`, `sandbox.ErrUnsupported`, `ErrProtocol`, `ErrUnavailable`, `ErrClosed`, `ErrNoPublishedImage`, `sandbox.DefaultImage() (string, error)`, `sandbox.MaxMessageBytes`, `sandbox.ProtocolMajor` and `ProtocolMinor`.
- `buildver.Version() string`.
- `wire.Guard(data []byte, l wire.Limits) error`.
- Generated code in `sandboxpb`: `NewAgentClient`, `RegisterAgentServer(grpc.ServiceRegistrar, AgentServer)`, `UnimplementedAgentServer`, `Agent_RsyncServer = grpc.BidiStreamingServer[RsyncFrame, RsyncFrame]`, `Agent_SyncTarServer`, `Agent_ExportTarServer`, `(*RsyncFrame).GetStart()` and `RsyncStart_SYNC`.

**Specified by spec 17, not yet in the repository** (marked NOT FOUND today; this spec is implemented after 17 and 18):
- `podman.New(o podman.Options) (sandbox.Provider, error)`, with `podman.Options{Binary string; Engine podman.Engine; Runtime, UserNS string; EngineEnv []string}`;
- `sandbox.GitRepo{URL, Ref string; Depth int}`;
- `sandbox.ErrNoAgent`;
- `gitclone.Check(sandbox.GitRepo) error`;
- `attach.Live(sandbox.Sandbox) error`.

**`google.golang.org/grpc` v1.64.1.** The module source is outside the repository.

Already used at call sites in the repository (`attach.go`, `serve.go`, `relaytest.go`):
- `grpc.NewClient`, `grpc.WithContextDialer`, `grpc.WithTransportCredentials`, `insecure.NewCredentials`;
- `grpc.WithDefaultCallOptions`, `grpc.MaxCallRecvMsgSize`, `grpc.MaxCallSendMsgSize`;
- `grpc.NewServer`, `grpc.MaxRecvMsgSize`, `grpc.MaxSendMsgSize`, `grpc.WaitForHandlers`;
- `(*grpc.Server).Serve`, `(*grpc.Server).Stop`;
- `metadata.NewOutgoingContext`, `metadata.MD`;
- `status.Code`, `status.Convert`, `codes.*`;
- `grpc.ClientConnInterface`.

**Unverified** (assumed signatures):
- `grpc.Creds(credentials.TransportCredentials) grpc.ServerOption`;
- `grpc.ChainUnaryInterceptor(...grpc.UnaryServerInterceptor) grpc.ServerOption`;
- `grpc.ChainStreamInterceptor(...grpc.StreamServerInterceptor) grpc.ServerOption`;
- `(*grpc.Server).GracefulStop()`;
- `grpc.WithPerRPCCredentials(credentials.PerRPCCredentials) grpc.DialOption`;
- `credentials.PerRPCCredentials`, an interface with `GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error)` and `RequireTransportSecurity() bool`;
- `credentials.NewTLS(*tls.Config) credentials.TransportCredentials`;
- `metadata.FromIncomingContext(ctx) (metadata.MD, bool)`;
- `status.Error(codes.Code, string) error`;
- `peer.FromContext(ctx) (*peer.Peer, bool)`.

**`google.golang.org/protobuf` v1.36.11.** Generated code only, as in spec 13. **Unverified.**

**Standard library** (Go 1.27). **Unverified**: `crypto/tls`, `crypto/subtle.ConstantTimeCompare`, `crypto/sha256`, `log/slog`, `net/url`, `net/netip`, `os/signal`.
