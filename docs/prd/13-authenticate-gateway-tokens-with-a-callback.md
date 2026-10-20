# Authenticate gateway tokens with a callback

Status: **proposed** (2026-10-19). Nothing here is implemented. Builds on
PRD 11 §5 (the gateway) as spec 19 delivered it and `docs/errata/19_sandbox_gateway.md`
records it. Changes nothing on either wire: the gateway protocol stays
1.0 and the Agent protocol 1.2. The consumer is hub's sandbox PRD
([hub `docs/prd/01`](https://github.com/agent-fox-dev/hub/blob/main/docs/prd/01-create-and-lease-sandboxes.md),
§8), which embeds `gatewayd.Server` and needs three things from it that
the static token list cannot give. Next step: this document becomes a
spec package; the few details left are marked *spec time*.

## Intent

`gatewayd.Server` authenticates every RPC against `Config.Tokens`, a list
fixed when `New` runs. Each token's SHA-256 is the caller's identity: a
sandbox belongs to the hash of the token that created it, and the backend
provider is told nothing about who is calling. That is the right shape for
the `agentkit-gateway` command, whose tokens come from a file and whose
operators are a handful of embedders.

Hub embeds the same server with a different population of callers: one
token per **lease**, minted when a caller asks hub for a sandbox, revoked
when the sandbox is destroyed, valid for one sandbox and no other. Hub's
backend provider has to know, at `Create`, which lease is creating so that
it can apply that lease's image, environment and secrets rather than the
caller's spec. And hub has to be able to destroy a sandbox it leased from
its own side, when a lease is deleted through its API, without holding a
client token.

This PRD adds the three seams and nothing else:

1. **A token callback** in place of the static list, so the embedder owns
   authentication and the identity an RPC carries is the embedder's
   principal, not a hash.
2. **The principal on the backend's context**, readable by the backend's
   `Create`, so a backend can act for the caller it is creating for.
3. **Destroy from the embedder's side**, by sandbox id, so the process that
   embeds the server can end a sandbox it holds.

The command does not change: `agentkit-gateway` keeps its token file, and
`Config.Tokens` keeps working as it does today.

## Goals

1. An embedder can construct a `gatewayd.Server` whose authentication is a
   function it supplies, called with the bearer token of each RPC, that
   returns a principal string; every ownership rule the server has today
   holds with the principal in place of the token's hash.
2. A backend's `Create` can read the principal of the RPC that caused it
   from its context, and a backend reached through the server in static
   mode reads a stable principal for each configured token.
3. An embedder can destroy a sandbox the server holds, by id, with the
   same effect on relayed calls and on the registry as expiry has.
4. Nothing observable changes for the `agentkit-gateway` command, for a
   client, or for a server constructed with `Config.Tokens`: the same
   refusals, the same log lines, the same errors.
5. The three seams are tested in process over `local`, as every gateway
   test is, with no engine and no network.

## Non-goals

- **Any change to the gateway or Agent protocols**, to the client
  (`sandbox/gateway`), or to the proxies. A client cannot tell which mode
  a gateway runs in.
- **Token minting, storage, expiry or rotation.** The callback is the
  whole contract; what stands behind it is the embedder's.
- **A per-principal view for the embedder** (list by principal, count by
  principal). An embedder that mints tokens keeps its own records; the
  server's registry stays the server's.
- **Authorization beyond ownership.** A principal sees the sandboxes it
  created, as a token does today. Quotas per principal, allowed profiles
  or network modes per principal are the embedder's, applied in its
  callback (refuse) or in its backend (`Create` refuses).
- **The command growing a dynamic mode.** `agentkit-gateway` has no
  source of principals but its file.

## Background: the server today

| Piece | Where | What it does |
|---|---|---|
| `Config.Tokens`, `token.Validate` | `config.go`, `internal/token` | at least one token of 32 printable ASCII characters, none repeated; `New` keeps each token's SHA-256 in `Server.owners` |
| `owner [32]byte`, `ownerKey`, `ownerOf` | `auth.go` | the identity an RPC carries in its context: the token's hash |
| `authenticateMD` | `auth.go` | compares the hash of the presented token against every configured hash in constant time |
| the tap, `unaryAuth`, `streamAuth` | `auth.go` | authenticate from the headers before gRPC reads a message, then again in the interceptors; put the owner in the context; refuse with `Unauthenticated` and log the peer |
| `entry.owner`, `registry.lookup`, `access`, `list` | `registry.go` | a sandbox belongs to one owner; another owner's id does not exist |
| `gatewayService.Create` | `gateway.go` | checks the spec, reserves capacity, calls `backend.Create(ctx, spec)` **with the RPC's context**, records the entry under `ownerOf(ctx)` |
| `relay.route` | `relay.go` | an Agent RPC names its sandbox in metadata and is served only for its owner |
| `logCreate`, `fingerprint` | `log.go` | logs carry the owner's first 8 hex characters, never a token or a full hash |
| `registry.remove`, `closeAll`, the expiry timer | `registry.go`, `server.go` | a sandbox leaves the registry on `Destroy`, at its TTL, when it becomes unavailable, and at `Shutdown`, each with a logged reason |
| `gwtest` | `internal/gwtest` | a server over an in-memory listener with `Token` and `TokenB`, clients, raw gRPC clients, a captured log, counting backends |

Two facts carry the design. The backend's `Create` already receives the
authenticated RPC's context, so the principal is one exported function
away. And the registry already removes entries for four reasons with one
code path, so an embedder-side destroy is a fifth reason, not a new
mechanism.

## 1. The callback

```go
package gatewayd

// Authenticator decides whether a bearer token is accepted and names the
// principal it belongs to. It is called for every RPC with the token as
// presented; ok false refuses the RPC with Unauthenticated. The principal
// is the identity the server keeps: a sandbox belongs to the principal
// that created it, and the backend reads it from its context. It must be
// non-empty, at most 256 bytes, and is logged as given, so it must not be
// a secret. The comparison the authenticator makes is its own; it is
// expected to be constant-time where a secret is compared.
type Authenticator func(ctx context.Context, token string) (principal string, ok bool)

type Config struct {
	// Tokens are the accepted bearer tokens, as today. Exactly one of
	// Tokens and Authenticate is set.
	Tokens []string
	// Authenticate, when set, replaces Tokens: the embedder decides which
	// tokens are accepted and what principal each one is.
	Authenticate Authenticator
	// … the other fields unchanged
}
```

Rules:

- `New` refuses a `Config` with both `Tokens` and `Authenticate` set, or
  neither, naming the two fields. With `Tokens` set, behaviour is today's,
  and the principal of each token is the hex of its SHA-256 (64
  characters), so an embedder reading the principal in static mode gets a
  stable name for a token without ever seeing the token.
- With `Authenticate` set, the tap calls it once per RPC with the token
  the `authorization: Bearer` header carries (missing, malformed or empty
  header: refused without a call, as today) and the RPC's context, which
  carries the peer and gRPC's deadline. The interceptors, which today
  authenticate a second time, verify in this mode that the tap placed a
  principal on the context and refuse otherwise; they do not call the
  authenticator again. *Spec time:* the erratum entry for 19-REQ-5.3
  ("again in the interceptors"), which this changes in substance for the
  callback mode only.
- A principal the authenticator returns that is empty or over 256 bytes is
  treated as `ok == false` and logged as a refused authentication with the
  reason `invalid principal`, so an embedder's bug cannot create an
  unowned sandbox. A panicking authenticator is recovered, logged, and the
  RPC refused.
- The `owner` type becomes the principal string. Ownership, `lookup`,
  `access`, `list` and `route` compare principals; a sandbox created under
  principal P is visible to every later RPC whose token maps to P, which
  is what lets an embedder rotate a token without losing the sandbox
  (same principal, new token) and lets one principal hold several
  sandboxes, as one token does today.
- Revocation is the embedder's: once its authenticator stops accepting a
  token, every later RPC with it is `Unauthenticated`, a relayed call in
  flight ends with the connection as it does for any refused RPC, and the
  sandboxes that principal holds stay in the registry until their TTL,
  `Reap`, `Shutdown`, or §3. The server does not learn of a revocation and
  does not need to.
- Logs: `logCreate`'s `owner` field is the principal in callback mode, and
  the first 8 hex characters of the hash in static mode, as today.
  `logAuthRefused` gains a `reason` (`no token`, `refused`, `invalid
  principal`, `panic`) and still never carries a token.

## 2. The principal on the backend's context

```go
// PrincipalFromContext is the principal of the authenticated RPC that a
// Backend method is serving, and ok false when the context carries none:
// a Reap, a Shutdown or a call the server did not make.
func PrincipalFromContext(ctx context.Context) (principal string, ok bool)
```

- Set on the context `gatewayService.Create` passes to `Backend.Create`,
  which is the RPC's context already, and on the context `SyncGit` passes
  to the sandbox's `Sync`. `Reap` runs under the server's own context and
  carries none; `Shutdown`'s closes carry none; a backend's sandbox
  methods the relay never calls (the relay forwards to the agent's
  connection directly) are not reached from an RPC and carry none.
- In static mode it is the hex hash, as §1 says, so a backend written
  against this function works behind the command as well as behind an
  embedder.
- The function reads the same context value the server keeps today
  (`ownerKey`); it exports a reader and changes no plumbing. The value
  stays unexported: an embedder cannot put a principal on a context, only
  read one.

With this, hub's backend wrapper does what its PRD describes: at `Create`
it reads the principal, finds the lease, refuses a lease in the wrong
state with `FailedPrecondition` (which the gateway returns to the client
as it does any backend error that is not a refusal), builds the spec the
lease decided, and creates on the real provider. A caller that names
another lease's id in a label changes nothing, because the lease is the
principal, not the label.

## 3. Destroy from the embedder's side

```go
// Destroy ends the sandbox id the server holds, whoever created it: it
// closes the backend sandbox, which ends the relayed calls in flight,
// removes the entry, and logs the destroy with reason "embedder". It
// returns ErrNotHeld for an id the server does not hold, and the backend's
// Close error otherwise; a sandbox the server no longer holds is not an
// error on a second call.
func (s *Server) Destroy(ctx context.Context, id string) error

var ErrNotHeld = errors.New("gatewayd: sandbox not held")
```

- The same path `registry.remove` runs for expiry and for a client's
  `Destroy`, with a new reason, `embedder`. A client whose sandbox was
  destroyed this way sees what it sees at expiry: `sandbox_unavailable`
  on the next proxy call, `NotFound` on the next Gateway RPC naming the
  id.
- `ctx` bounds the backend's `Close`; a cancelled `ctx` leaves the entry
  removed and the close error returned, as `Shutdown` does.
- No list, no lookup by principal: the embedder that minted a lease knows
  the id the client's `Create` produced, because its backend saw it
  (`Backend.Create` returns the sandbox, and hub records `ID()` on the
  lease). An embedder that does not keep ids has `Shutdown` and the TTL.

## 4. The command

Unchanged in behaviour: `--token-file` fills `Config.Tokens`, nothing fills
`Authenticate`. Its `--version` line, flags and exit codes stay as
`docs/cli.md` has them. The only visible difference is the `reason` field
on the refused-authentication log line.

## 5. Tests

In `gwtest`, a `Config` builder gains `WithAuthenticator(fn)`, and a
backend that records `PrincipalFromContext` on every `Create` joins the
counting backends. The spec's tests, all in process over `local`:

- `New` refuses both fields set and neither set, each naming the fields.
- Callback mode: a token the authenticator maps to `P` creates a sandbox;
  a second token mapped to `P` lists and reaches it; a token mapped to `Q`
  sees it as `NotFound` on `Destroy` and as `NotFound` on a relayed
  `Hello`; a token the authenticator refuses is `Unauthenticated` on
  every RPC, including a method no handler serves (the tap), and the
  refusal is logged with the peer and `reason: refused`.
- The authenticator is called once per unary RPC and once per stream (a
  counting authenticator), and never for a request with no
  `authorization` header.
- An empty principal and a 257-byte principal refuse with `reason: invalid
  principal`; a panicking authenticator refuses with `reason: panic` and
  the server keeps serving.
- The recording backend sees the principal of the creating RPC on
  `Create` in callback mode, the hex hash in static mode, and none on
  `Reap`.
- `Server.Destroy(id)` on a held sandbox makes a client's next proxy call
  `sandbox_unavailable` and its next `Destroy` `NotFound`, logs `reason:
  embedder`, and returns `ErrNotHeld` on an unknown id and nil on a second
  call for the same id.
- Static mode: every test of spec 19 passes unchanged (`TestOwnership_TS19_36`,
  `TestUnauthenticated_TS19_34`, the lifecycle, parity and shutdown
  tests), which is the proof of goal 4.
- The log never carries a token: the existing check extends to the
  callback mode with a token planted in the authenticator's input.

## 6. Acceptance criteria

1. `gatewayd.New(Config{Backend: b, Authenticate: fn})` returns a server
   that accepts exactly the tokens `fn` accepts, keys every sandbox by the
   principal `fn` returns, and refuses the rest with `Unauthenticated`
   before reading a request body.
2. `gatewayd.New(Config{Backend: b, Tokens: ts})` behaves as the server
   does today, test for test, and `PrincipalFromContext` in its backend's
   `Create` returns the hex SHA-256 of the creating token.
3. `PrincipalFromContext` returns `ok == true` on the context of
   `Backend.Create` and of a `SyncGit`'s `Sync`, and `ok == false` on the
   context of `Reap`, of `Shutdown`'s closes, and of a context the server
   did not make.
4. `(*Server).Destroy` ends a held sandbox with the effects of expiry and
   the reason `embedder`, and answers `ErrNotHeld` for an id it does not
   hold.
5. `agentkit-gateway`'s tests pass unchanged; `--version` prints the same
   protocol versions; `docs/cli.md`'s exit-code table is unchanged.
6. `make check` passes in every module, including the gatewayd module's
   and the gateway example's; `internal/policy`'s gates are unchanged.
7. `docs/generated/sandbox/gatewayd/gatewayd.txt` shows `Authenticator`,
   `Config.Authenticate`, `PrincipalFromContext`, `(*Server).Destroy` and
   `ErrNotHeld`, and nothing else new.

## 7. What is given up, stated plainly

- **The server trusts the authenticator for constant-time comparison.**
  In static mode the server compares hashes in constant time itself; in
  callback mode it cannot, and the documentation says so.
- **A principal is logged as given.** An embedder that returns something
  sensitive as a principal leaks it into its own logs; the contract says
  not to, and the 256-byte bound stops the worst of it.
- **Revocation is not immediate for in-flight streams.** A stream opened
  under a token that is revoked mid-way runs until it ends; the next RPC
  is refused. Ending it at once would mean asking the authenticator per
  frame, which this PRD does not do.
- **Two authentication modes to keep in step.** The interceptors behave
  differently in the two modes (re-authenticate versus verify presence).
  The static-mode tests stay the invariant that nothing moved.

## Documentation

| Change | Update |
|---|---|
| `Authenticator`, `Config.Authenticate`, `PrincipalFromContext`, `Destroy`, `ErrNotHeld` | `make doc`, `docs/generated/sandbox/gatewayd/gatewayd.txt` committed |
| the two modes and the principal | `docs/configuration.md` (the sandbox providers table's gateway column and a `gatewayd.Config` entry), `docs/api.md` (the `sandbox/gatewayd` row) |
| the refused-authentication log line | `docs/cli.md` (`agentkit-gateway`, "Output and exit codes") |
| the embedder's path when the gateway is embedded | `docs/architecture.md` ("Sandboxes": a paragraph on an embedded gateway and the principal) |
| 19-REQ-5.3's "again" in callback mode | `docs/errata/19_sandbox_gateway.md` |
| the consumer | hub PRD 01 §8 gains the third seam (`Destroy`), which it needs for its `DELETE` and its reaper and did not name |
