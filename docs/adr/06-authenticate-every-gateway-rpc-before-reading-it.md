# ADR 06: Authenticate every gateway RPC before reading it

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken between 2026-08 and 2026-10)

## Context

The sandbox gateway (`sandbox/gatewayd`, the `agentkit-gateway` command) and
its client (`sandbox/gateway`) let a program create and drive sandboxes on
another machine. Its spec required an unauthenticated RPC to be refused
before anything in the request was read, and an agent never to see the
caller's metadata; a later spec let an embedder authenticate tokens with a
callback. gRPC's own order of operations, and the case of a gateway in front
of another gateway, made parts of the text impossible as written. The
settlements were recorded in errata. The errata are gone; the rules the code
follows are here.

## Decision

**Authentication.**

- The server authenticates every RPC from its headers in gRPC's tap handle
  (`grpc.InTapHandle`), before gRPC reads a message, because a generated unary
  handler decodes its request before any interceptor runs. The tap also covers
  a method no handler serves: unauthenticated, it is `Unauthenticated`;
  authenticated, `Unimplemented`.
- In static mode (`Config.Tokens`) the unary and stream interceptors
  authenticate again. In callback mode (`Config.Authenticate`) the
  Authenticator runs once per RPC, in the tap, and the interceptors only check
  that the context carries a principal. `New` refuses a `Config` with both or
  neither.
- The owner of a sandbox is its principal: in static mode the lowercase hex
  SHA-256 of the token, exposed only by `gatewayd.PrincipalFromContext`; in
  callback mode the string the Authenticator returned (1 to 256 bytes), and no
  hash of the token is kept. A second token of the same principal reaches the
  first token's sandboxes.
- `sandbox created` logs `owner` as the principal's first 8 hex characters in
  static mode and as the whole principal in callback mode; a principal is not a
  secret. `authentication refused` carries `peer` and a `reason`: `no token`,
  `refused`, `invalid principal` or `panic`.

**Requests.** A request whose bytes do not decode never reaches a handler;
gRPC answers it `Internal`. Every request that decodes but is malformed (an
unknown network mode, provider bytes that are not a JSON object or that
`wire.Guard` rejects) is answered `InvalidArgument`. `SandboxSpec` carries its
durations and times as `*_nanos` fields, `ttl_nanos` among them.

**The relay.** The relay calls the backend sandbox's agent with that
sandbox's own outgoing metadata (`attach.Outgoing`), never the caller's. For a
Podman, Landlock or local sandbox that metadata is empty; for a backend that is
itself a gateway client it is the downstream sandbox id, which the next
gateway routes by. The agent behind the last gateway sees neither
`authorization` nor `agentkit-sandbox-id`.

**The client.**

- `Create` returns an unexported wrapper around attach's sandbox that turns a
  `GitRepo` Sync into the `SyncGit` RPC and forwards every other method; it
  unwraps to attach's sandbox.
- If the caller's context ends after the gateway created the sandbox and
  before `Create` returns, the client destroys it.
- The client trims trailing full stops from the backend's notes and joins its
  own sentence with `". "`.

**The command.** The plaintext warning names the configured `--listen`
address and the bound one, and decides "loopback" from the configured host
(macOS reports a listener on `0.0.0.0` as `[::]`).

**Listening.** Only the nested server modules, `sandbox/gatewayd` and
`mcpserve`, open a listening socket. The root and sandbox modules never do.

## Consequences

- An RPC that reaches a handler has always been authenticated, whatever the
  method and whatever the mode.
- Gateways chain: a gateway's backend may be another gateway's client, and
  each hop routes by its own sandbox id.
