# Erratum: spec 19 `sandbox_gateway`

These are the places where the delivered code and its tests differ from the
spec's text, and why.

## Whole requirements in a task's criteria (D-1)

Tasks 4 and 9 list `19-REQ-4` and `19-REQ-8` among their criteria. These
are requirement ids, not criterion ids, and were read as every criterion of
that requirement.

## The test commands are run one part at a time (D-2)

`tasks.json`'s `linter` (`make vet && make lint`) and `spec_tests` (three
`go test` runs joined by `&&`, two in subshells) use shell operators. Each
part ran as its own command, with the same effect.

## Spec 17 and 18's symbols are in the tree (D-3)

The PRD marks `podman.New`, `sandbox.GitRepo`, `sandbox.ErrNoAgent`,
`gitclone.Check` and `attach.Live` as not yet in the repository, and several
gRPC calls as unverified. All of them exist with the stated signatures (gRPC
v1.64.1). The podman wrapper type that gained `Unwrap` is `podSandbox`
(`sandbox/podman/sandbox.go`).

## A gateway sandbox is attach's sandbox behind a thin wrapper (D-4)

TS-19-25 says the sandbox `Create` returns has attach's concrete type. A
`GitRepo` Sync must become the `SyncGit` RPC, which attach's sandbox cannot
send, so `Create` returns an unexported `gwSandbox`
(`sandbox/gateway/sandbox.go`). It forwards every other method to attach's
sandbox unchanged and unwraps to it, as task 4's steps describe.
`TestCreatedSandbox_TS19_25` checks the type of the `Unwrap` target.

## The gateway's sentence follows the backend's notes after ". " (D-5)

`local`'s `Notes` has no final full stop. The client trims trailing full stops
from the backend's notes and joins its own sentence with ". "
(`joinNotes` in `sandbox/gateway/dial.go`).

## SandboxSpec carries the TTL as `ttl_nanos` (D-6)

TS-19-2's pseudocode looks each `sandbox.Spec` field up by its snake-case
name, which would make `TTL` the field `ttl`. Task 1's steps name the field
`ttl_nanos`, as do the other duration and time fields. The proto follows the
steps, and `TestGatewayMessages_TS19_2` maps `TTL` to `ttl_nanos`.

## A request gRPC cannot decode is answered by gRPC

19-REQ-6.1 asks for `InvalidArgument` for a malformed Create request. A
request whose bytes do not decode never reaches the handler: gRPC itself
answers it with `Internal` ("grpc: error unmarshalling request"). Replacing
that would need a custom codec, which no requirement asks for. Every request
that decodes but is malformed is answered `InvalidArgument`: an unknown
network mode, or provider bytes that are not a JSON object or that
`wire.Guard` rejects (TS-19-38).

## Authentication runs in gRPC's tap handle as well

19-REQ-5.4 says an unauthenticated RPC is refused before anything else in
the request is read. A generated unary handler decodes its request before
any interceptor runs. So the server authenticates every RPC from its headers
in a tap handle (`grpc.InTapHandle`), before gRPC reads a message, and then
again in the unary and stream interceptors 19-REQ-5.3 names
(`sandbox/gatewayd/auth.go`). The tap also covers a method no handler
serves: an unauthenticated call to one is answered `Unauthenticated`, an
authenticated one `Unimplemented` (TS-19-49).

## The plaintext warning names the --listen address

TS-19-54 expects the warning for `--plaintext --listen 0.0.0.0:0` to name
`0.0.0.0`. On macOS a listener on `0.0.0.0` reports its bound address as
`[::]:<port>`. The warning carries the configured address (`address`) and the
bound one (`bound`), and the loopback decision uses the configured host
(`sandbox/gatewayd/cmd/agentkit-gateway/run.go`).

## The client-side "context ended after Create" path

19-REQ-4.5 covers a context that ends after `Gateway.Create` succeeded but
before `Create` returns. `Provider.Create` checks the context after the
handshake and destroys the sandbox (`sandbox/gateway/create.go`). TS-19-26's
second case cancels the caller's context inside the backend's `Create`. The
cancellation then reaches the server before the response leaves it. So the
test exercises the server's own caller-gone path (19-REQ-6.5), which closes
the sandbox and records nothing, and asserts the outcome the spec names: no
sandbox, no entry, no directory. The backend waits for its RPC context to
end before returning, so the order does not depend on timing.

## The fake gateway buffers its writes

`net.Pipe` has no buffer. A TLS handshake whose client rejects the
certificate deadlocks on it: the server waits to finish writing its flight,
and the client waits to write its alert. Over TCP the kernel's buffers break
that cycle. The fake gateway in `sandbox/gateway/fakegw_test.go` gives its
server end buffered writes (`asyncConn`), so TS-19-11's system-roots case
fails on the untrusted certificate at once instead of after gRPC's 20-second
connect timeout.

## The stub backend creates local sandboxes with an unrestricted network

Several server tests use a stub backend that offers `none` and
`unrestricted` while creating `local` sandboxes, as TS-19-37 and TS-19-39
describe. `local` accepts only `NetworkUnrestricted`, so the stub's `Create`
asks `local` for that mode whatever the request said
(`sandbox/gatewayd/internal/gwtest/backends.go`). The gateway's own checks
(the ceiling, the TTL, capacity) run on the request as sent.

## TS-19-64 compares the policy testdata with the checked-out commit

TS-19-64 asks that `internal/policy/testdata` be unchanged by this spec. The
test fails when `git status` reports a change there. The spec's commits leave
the directory untouched; `git log` shows that, and no test can know the
spec's base commit.
