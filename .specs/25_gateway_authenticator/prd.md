---
spec_id: "25"
spec_name: "gateway_authenticator"
title: "Gateway authentication by callback, the principal on the backend's context, and destroy from the embedder"
status: "active"
created_at: "2026-10-20T06:25:34.284579Z"
updated_at: "2026-10-20T06:25:34.284579Z"
intent_hash: "19fe1f9e81c805eb8488cc41de7836595131b82559103c8dcd50c90878885910"
schema_version: 2
source: "docs/prd/13-authenticate-gateway-tokens-with-a-callback.md"
---
## Intent

Let a program that embeds `gatewayd.Server` own authentication of the gateway. It supplies a function that turns a bearer token into a principal of its own, and the server keys every sandbox by that principal instead of by a hash of the token. The backend provider can read the principal of the RPC it is serving. The embedder can end any sandbox the server holds by its id, without holding a client token. The `agentkit-gateway` command, servers configured with a static token list, clients and both wire protocols behave as they do now.

## Goals

1. `gatewayd.New(gatewayd.Config{Backend: b, Authenticate: fn})` returns a server that accepts exactly the tokens `fn` accepts. It records every sandbox under the principal `fn` returns, and refuses every other RPC with `Unauthenticated` before it reads a request message.
2. Every ownership rule from spec 19 (List, Destroy, SyncGit, the Agent relay) holds with the principal in place of the token hash. A second token that maps to the same principal reaches the first token's sandboxes. A token that maps to another principal gets the answers it would get for an id that does not exist.
3. `gatewayd.PrincipalFromContext` returns the principal on the context the server passes to `Backend.Create`, to a sandbox's `Sync` from `SyncGit`, and to `Backend.Reap` from the `Gateway.Reap` RPC. In static mode the principal is the lowercase hex SHA-256 of the token. It returns no principal on the periodic reap, on any sandbox `Close`, and on any context the server did not make.
4. `(*Server).Destroy(ctx, id)` ends a held sandbox, whoever created it, with the same client-visible effects as expiry, and logs the destroy reason `embedder`. For an id the server does not hold it returns an error that matches `gatewayd.ErrNotHeld`.
5. A server built with `Config.Tokens`, and the `agentkit-gateway` command, keep their refusals, status codes, error texts and log messages. The one exception is a new `reason` attribute on the refused-authentication line. Every spec 19 test passes unchanged.
6. All new behaviour is tested in process over `local` through `gwtest`, with no container engine and no network. `make check` passes in every module.
7. `docs/generated/sandbox/gatewayd/gatewayd.txt` gains five exported identifiers and no others: `Authenticator`, `Config.Authenticate`, `PrincipalFromContext`, `(*Server).Destroy` and `ErrNotHeld`.

## Non-goals

- No change to the gateway protocol (1.0), the Agent protocol (1.2), `gateway.proto`, `sandbox.proto`, the client package `sandbox/gateway`, or the proxies. A client cannot tell which mode a gateway runs in.
- The server does not mint, store, expire or rotate tokens. The callback is the whole contract.
- The embedder gets no view of sandboxes by principal: no list by principal, no count by principal, no lookup by principal.
- No authorization beyond ownership: no quotas, no allowed profiles, no network modes per principal. An embedder refuses in its authenticator, or in its backend's `Create`.
- `agentkit-gateway` gets no dynamic authentication mode, and none of its flags or exit codes change.
- A revoked token does not end the RPCs already in flight under it, and the server enforces no timeout on the authenticator.
- Hub's own PRD lives in another repository and is not edited here.

## Background

The gateway server, from spec 19, lives in the nested module `sandbox/gatewayd`:

- **Configuration.** `New` (`config.go`) validates `Config.Tokens` with `internal/token.Validate`: at least one token, each at least 32 bytes of printable ASCII, none repeated. It keeps each token's SHA-256 in `Server.owners`.
- **Identity.** `auth.go` defines `type owner [sha256.Size]byte`, the context key `ownerKey{}` and `ownerOf(ctx)`. The identity an RPC carries is the hash of its token.
- **Authentication.** `authenticateMD` reads exactly one `authorization` value with the prefix `Bearer `. It hashes the token and compares the hash with every configured hash in constant time. The gRPC tap handle `Server.tap` (`grpc.InTapHandle`) runs it before gRPC reads a message. `unaryAuth` and `streamAuth` then run it again, as 19-REQ-5.3 asks, and put the owner on the context. `refuse` logs `authentication refused` with `peer` and answers `errUnauthenticated`.
- **Ownership.** `registry.go` keeps `entry.owner`. `lookup`, `access` and `list` filter by owner. `gatewayService.Create` (`gateway.go`) records `ownerOf(ctx)` and calls `s.backend.Create(ctx, spec)` with the RPC's own context. `SyncGit` (`slot.go`) calls `e.sb.Sync(ctx, repo)` with the RPC's context. The `Gateway.Reap` RPC calls `s.backend.Reap(ctx)` with the RPC's context. The periodic `reapLoop` (`server.go`) uses a `context.Background()` timeout.
- **Answers for a foreign id.** These are 19-REQ-5.6, and the architecture document states them: List omits the sandbox, `Destroy` answers OK and does nothing, the relay and `SyncGit` answer `Unavailable` naming the id.
- **Removal.** `registry.remove(e, reason)` deletes the entry once and stops its timer. It then calls `e.sb.Close(context.Background())` and logs `sandbox destroyed` with `reason`, one of `client`, `expired`, `unavailable` or `shutdown`. `closeAll` serves `Shutdown`. A client whose sandbox was removed gets `sandbox_unavailable` on its next proxy call.
- **Logs.** `logCreate` (`log.go`) logs `owner` as the first 8 hex characters of the hash. `TestLogging_TS19_35` asserts that the log never contains the token or the full hex hash.
- **Test harness.** `internal/gwtest` provides `Start(t, cfg)`, which sets `Tokens` to `[Token, TokenB]` when nil, plus `Dial`, `Raw`, `Ctx` and `NewLogs`. It also has the backends `Counting`, `Agents` and `Slot`.

Hub embeds the same server with one token per lease. Its backend has to know at `Create` which lease is creating, and hub has to end a leased sandbox from its own side. The static list supports neither. Most of what is needed is already in place: the backend's `Create` already receives the authenticated context, and the registry already has a single removal path with a reason.

## Requirements

**R1. Configuration.** `gatewayd.Config` gains `Authenticate Authenticator`, where `type Authenticator func(ctx context.Context, token string) (principal string, ok bool)`.

- `New` refuses a `Config` in which both `Tokens` (non-empty) and `Authenticate` (non-nil) are set, or neither is. The error names both fields and contains no token.
- When `Tokens` is set, `New` validates it exactly as today, with the same errors.
- All other fields and their validation are unchanged.

**R2. Static mode.** With `Tokens` set, authentication, ownership, refusals and logs are today's. The tap authenticates and both interceptors authenticate again, comparing hashes in constant time. The principal of a token is the 64-character lowercase hex encoding of its SHA-256. Only `PrincipalFromContext` exposes it; no log line carries it in full.

**R3. Callback mode: when the authenticator runs.** With `Authenticate` set, the tap reads the `authorization` header with today's rules.

- A missing, repeated or non-`Bearer` header, or an empty token, is refused without calling the authenticator.
- Otherwise the tap calls the authenticator exactly once for the RPC. It passes the tap's context, which carries the peer address, and the token exactly as presented. The server applies no length or character rule of its own to the token.
- `ok == false` refuses the RPC with `Unauthenticated` before gRPC reads any message. This covers a method no handler serves.
- An accepted principal is placed on the RPC's context. The unary and stream interceptors do not call the authenticator again. They check that the context carries a principal and refuse with `Unauthenticated` if it does not.
- The authenticator is called once per unary RPC, once per stream, and never for a request without a valid-shaped header.

**R4. Callback mode: bad principals and panics.**

- A principal that is empty or longer than 256 bytes counts as `ok == false`.
- A panic in the authenticator is recovered. The RPC is refused with `Unauthenticated`, and the server keeps serving other RPCs and connections.
- The server places no other restriction on a principal. It is compared byte for byte and logged as given.

**R5. Ownership by principal.** The owner identity on the context, on `entry`, and in `lookup`, `access`, `list`, `relay.route`, `SyncGit` and `Destroy` is the principal string, in both modes. A sandbox created under principal P is visible to every later RPC whose token maps to P. To any other principal it answers exactly as spec 19 answers for an unknown id: omitted from List, OK and no effect on `Destroy`, `Unavailable` naming the id on the relay and on `SyncGit`.

**R6. Revocation.** Once the authenticator stops accepting a token, every later RPC with it is refused with `Unauthenticated`. RPCs already in flight run to their end. The sandboxes the principal holds stay registered until their TTL, a client `Destroy` under the same principal, `(*Server).Destroy`, or `Shutdown`. The server is not told about revocations.

**R7. Logs.**

- `sandbox created` carries `owner` as the first 8 hex characters of the principal in static mode (today's value) and as the principal itself in callback mode.
- `authentication refused` keeps its message and `peer`, and gains `reason`. The values are:
  - `no token`: missing, repeated, non-Bearer or empty header;
  - `refused`: no hash matches, the authenticator said no, or an interceptor found no principal;
  - `invalid principal`;
  - `panic`.
- No log line carries a token, the panic value, a full static-mode hash, an `Env` value or a tool payload.

**R8. The principal on the backend's context.** `func PrincipalFromContext(ctx context.Context) (principal string, ok bool)` reads the server's unexported context value.

- It returns `ok == true` on:
  - the context passed to `Backend.Create`;
  - the context `SyncGit` passes to the backend sandbox's `Sync`;
  - the context the `Gateway.Reap` RPC passes to `Backend.Reap`.
- It returns `ok == false` on:
  - the periodic reap at `Serve` start and every `ReapInterval`;
  - every backend sandbox `Close` the server makes on its own (client `Destroy`, expiry, unavailability, `Shutdown`, the error paths of `Create`);
  - any context the server did not make.
- No exported way exists to put a principal on a context.

**R9. Destroy from the embedder.** `func (s *Server) Destroy(ctx context.Context, id string) error` and `var ErrNotHeld = errors.New("gatewayd: sandbox not held")`.

- For a held id, whatever its principal, `Destroy` runs the registry's single removal path with the new reason `embedder`. The entry is removed and its timer stopped. The backend sandbox's `Close` is called with `ctx`, which ends the relayed calls in flight, and `sandbox destroyed` is logged with `reason: embedder`.
- It returns the `Close` error, wrapped with the id. The entry stays removed even if `Close` fails or `ctx` is cancelled.
- For an id not currently held it returns an error matching `ErrNotHeld` that names the id. This covers an unknown id, one already removed by any path (including a previous `Destroy`), and calls before `Serve` or after `Shutdown`.
- If a concurrent removal of the same entry wins after the lookup, `Destroy` returns nil.
- Afterwards the client sees what it sees at expiry: `sandbox_unavailable` on its next proxy call, the id absent from List, OK from a client `Destroy`, `Unavailable` from `SyncGit`.
- Existing callers of the removal path keep closing with `context.Background()`.

**R10. Tests, the command and documentation.**

- `gwtest.Start` sets `Tokens` to `[Token, TokenB]` only when both `Tokens` and `Authenticate` are unset.
- `gwtest.Counting` records the result of `PrincipalFromContext` for every `Create`, `Reap` and sandbox `Sync` it serves.
- The spec's tests run in process over `local`. They cover R1–R9 and the token-never-logged check in callback mode, with a planted token passed through the authenticator.
- `agentkit-gateway` keeps filling `Tokens` from `--token-file` and never sets `Authenticate`.
- The package documentation (`doc.go`) and the `Config`, `Authenticator`, `PrincipalFromContext`, `Destroy` and `ErrNotHeld` doc comments state:
  - the two modes;
  - that the authenticator must be safe for concurrent use, must return quickly without blocking, and is trusted to compare tokens in constant time;
  - that the principal must not be a secret.

## Design Decisions

1. **Exactly one mode, with `Tokens` set meaning non-empty.** A nil or empty `Tokens` together with a nil `Authenticate` is the "neither" error naming both fields. This replaces today's "no token configured" error for that one configuration, which never produced a working server. The command validates its token file before `New` and is not affected.
2. **The static-mode principal is the lowercase hex SHA-256.** This gives a backend a stable, non-secret name for each token in both modes. Logs keep the 8-character fingerprint, so `TestLogging_TS19_35`'s "never the full hash" check still holds.
3. **In callback mode the authenticator runs in the tap only.** Running it in the tap keeps 19-REQ-5.4: nothing is read before authentication. Not running it again in the interceptors halves the cost and stops a revocation between the two calls from giving inconsistent answers. Static mode keeps its double check so that nothing moves for existing deployments.
4. **The authenticator must not block, and the server enforces no timeout.** gRPC runs tap handles on the per-connection I/O goroutine, so a slow authenticator stalls every stream on that connection. A Go function cannot be pre-empted. The contract says to return quickly, and an embedder whose tokens live in a store keeps an in-memory view of them.
5. **The principal bound is 1 to 256 bytes, with no character rule.** The bound stops an embedder bug from creating an unowned sandbox or flooding logs. slog quotes or escapes any byte, so the server needs no charset rule.
6. **A panicking authenticator is recovered, and the panic value is not logged.** A panic in the tap would otherwise bring down the whole process, and the panic value could contain the token.
7. **The refused-authentication line gains `reason` in both modes.** This is additive: the message and `peer` stay as they are, and the existing tests check only those. It is the one visible change for the command, so goal 5 names it as the exception.
8. **Answers for a foreign or unknown id stay as spec 19 has them.** That means OK on `Destroy`, `Unavailable` on the relay and `SyncGit`, and omission from List. The input said `NotFound`, but the code and 19-REQ-5.6 answer as above, and changing them would break the no-change-for-clients goal.
9. **The `Gateway.Reap` RPC carries the principal; the periodic reap does not.** The input said a Reap carries none. But the RPC already passes its authenticated context, and the rule "present exactly when the backend call serves an authenticated RPC" is simpler, needs no change to the plumbing, and lets an embedder's backend refuse a backend-wide reap from a lease token.
10. **`Close` never carries a principal from the server's own removals.** `registry.remove` closes with `context.Background()` today, and that stays the same for every reason except `embedder`, which uses the embedder's `ctx`.
11. **`ErrNotHeld` is returned for any id not currently held, including on a second `Destroy`.** The input asked for both "`ErrNotHeld` for an unknown id" and "nil on a second call". Doing both would mean remembering every id ever removed, without bound. An embedder that wants idempotence treats `errors.Is(err, ErrNotHeld)` as already gone.
12. **`Destroy`'s `ctx` bounds the backend `Close`.** `registry.remove` gains a context parameter for this. The entry is deleted before `Close`, so a cancelled context still leaves it removed. The `Close` error is returned wrapped with the id, so `errors.Is` and `errors.As` still reach the backend's error.
13. **A create still in progress is not held.** An embedder's backend sees the id before `gatewayService.Create` records it. A `Destroy` in that window returns `ErrNotHeld`. The embedder then holds the backend sandbox and can close it directly, and the server removes the dead entry as `unavailable` on its next access or prune. This avoids a "doomed id" mechanism.
14. **The context key stays unexported.** An embedder can read a principal but cannot forge one into a context it passes to the server.
15. **`gwtest` gets no builder.** `Start` already takes a `gatewayd.Config`, so the harness only stops defaulting `Tokens` when `Authenticate` is set, and `Counting` records principals instead of a new backend type doing it.
16. **Spec 19's interceptor wording is handled in its erratum.** The callback-mode difference to 19-REQ-5.3 ("again in the interceptors") and to 19-REQ-5.5 and 5.7 (the owner fingerprint) is recorded in `docs/errata/19_sandbox_gateway.md`. Spec 19 is not amended.
17. **Documentation updates.** After `make doc`:
    - `docs/configuration.md`: the `gatewayd.Config` table gets an `Authenticate` row and the "exactly one" rule; a paragraph covers the principal, `PrincipalFromContext` and `Destroy`.
    - `docs/api.md`: the `sandbox/gatewayd` row.
    - `docs/cli.md`: "Output and exit codes" says the refused line carries `reason`: `no token` or `refused`.
    - `docs/architecture.md`: the gateway section's Ownership paragraph speaks of principals, and a new paragraph covers the embedded gateway.
    - `docs/errata/19_sandbox_gateway.md`.
    - The regenerated `docs/generated/sandbox/gatewayd/gatewayd.txt`.

## Dependencies

| Spec | Relation | Reason |
|---|---|---|
| `19_sandbox_gateway` | modifies | Changes `sandbox/gatewayd`'s authentication, owner identity, registry removal path, logs and `gwtest`. Every spec 19 test must still pass unchanged. |

## Verified External API

| Symbol | Signature assumed | Status |
|---|---|---|
| `google.golang.org/grpc.InTapHandle` | `func InTapHandle(h tap.ServerInHandle) ServerOption` | unverified (grpc v1.64.1 per `sandbox/gatewayd/go.mod`; module cache unreachable). Implied by the existing call in `config.go`. |
| `google.golang.org/grpc/tap.ServerInHandle` | `func(ctx context.Context, info *tap.Info) (context.Context, error)`; the returned context becomes the stream's context, and the handle runs on the per-connection I/O goroutine | unverified. The signature is implied by `Server.tap` in `auth.go`. That the context propagates is assumed: the callback-mode test (the recording backend sees the principal) proves it. |
| `google.golang.org/grpc/tap.Info` | `struct{ FullMethodName string; Header metadata.MD }` | unverified. `Header` is used by `Server.tap`. |
| `google.golang.org/grpc/peer.FromContext` | `func FromContext(ctx context.Context) (*peer.Peer, bool)` | unverified. Used by `refuse` on the tap context. |
| `google.golang.org/grpc/metadata.FromIncomingContext` | `func FromIncomingContext(ctx context.Context) (metadata.MD, bool)` | unverified. Used by `authenticate`. |
| `grpc.UnaryServerInterceptor` / `grpc.StreamServerInterceptor` shapes | as used by `unaryAuth` and `streamAuth` | unverified. Existing call sites. |
| `crypto/sha256.Sum256`, `encoding/hex.EncodeToString`, `crypto/subtle.ConstantTimeCompare`, `errors.New`, `log/slog` | standard library | stable |
