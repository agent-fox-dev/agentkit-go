---
spec_id: "17"
spec_name: "sandbox_podman"
title: "Podman and Docker sandbox provider, GitRepo init step and guard guidance"
status: "active"
created_at: "2026-10-13T14:39:00.122631Z"
updated_at: "2026-10-13T14:39:00.122631Z"
intent_hash: "3ccd295ee950e00ece28e21a76680275745634f010eee51037625a7bf8aefefe"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Give AgentKit its first sandbox provider that actually isolates. The provider runs each sandbox's agent in a hardened, ephemeral container that holds exactly one workspace volume, and it drives the `podman` or `docker` command line. It covers the whole container lifecycle: creation with a hardening set the code can check offline, a workspace that can be filled from a public git repository without letting the network into the sandbox, a lifetime bounded on both the host and the container side, and cleanup of whatever a crashed host left behind. Once this exists, an embedder can let a model's shell commands run somewhere they cannot reach the host's files, environment, credentials or network. The project then also says when the guard's opt-out (`guard.AllowAll`) is a defensible choice.

## Goals

1. **The command line is pure and checkable offline.** `podman.Args` for the zero `Spec` (after `CheckSpec`, with an image set and engine `podman`) contains:
   - `--network none`, `--cap-drop ALL`, `--security-opt no-new-privileges`, `--read-only`, `--init` and `--log-driver none`;
   - a tmpfs for `/tmp`;
   - the sandbox's workspace volume mounted at `/workspace`;
   - the `agentkit.sandbox=<id>` label;
   - `--entrypoint /usr/local/libexec/agentkit-sandboxd`, followed by `sandbox.AgentArgs` of the spec.

   It contains no `-v`, `--volume`, bind mount, `--privileged`, `--cap-add` or host path. The `docker` variant differs only in leaving out `--unsetenv-all`. A `Spec` with `NetworkAllowlist` or `Resources.DiskBytes` is refused with a `*sandbox.SpecError` naming the provider and the field.
2. **A full round trip works.** On a host with `podman` (or `docker`) and the locally built base image:
   - `Create`, then `Sync` of a `LocalDir`, then proxy `read_file`, `write_file` and `execute` all work.
   - `Export` to a temporary directory returns the new file and nothing the source's `.gitignore` excludes.
   - After `Close`, neither `ps -a` nor `volume ls`, each filtered on the sandbox's label, shows anything.
3. **A `GitRepo` sync clones without opening the sandbox's network.** Under `NetworkNone`, a `GitRepo` sync of a public repository puts the clone in the workspace. Afterwards, `execute` of a TCP connect to any host still fails. A `GitRepo` sync after the first `Tools` call is refused.
4. **A missing agent is reported clearly.** `Create` with an image that has no agent at `sandbox.AgentPath` fails before any tool call. The error matches `sandbox.ErrNoAgent`, names the path, and includes the `COPY --from` line from `sandbox.OverlayCopyLine`. No container or volume with the sandbox's label is left behind.
5. **Resources are bounded.** In a sandbox with `MemoryBytes: 256 << 20`, a command that tries to allocate 1 GiB is killed, and the next proxy call on the same sandbox succeeds.
6. **Lifetime and Reap work.**
   - A sandbox created with `TTL: 5s` and never closed has no container and no volume left shortly after its TTL.
   - `Reap` removes a labelled volume whose expiry label is in the past, and returns its id.
   - `Reap` leaves alone a labelled volume whose expiry is in the future.
7. **Nothing from the host leaks in.** Inside the sandbox, `env` lists exactly the expected variables:
   - every entry of `Spec.Env`;
   - the provider's defaults for `PATH`, `HOME`, `LANG`, `TMPDIR` and `TERM` that `Spec.Env` did not set;
   - on Docker, also `HOSTNAME`.

   A marker variable set only in the engine's environment does not appear. No tool result from the run contains the host's working directory or home directory path.
8. **`make check` stays offline and the surfaces stay put.** It needs no container engine. The sandbox module gains no Go dependency. The exported surfaces of `agentkit`, `core`, `tools` and `codemode` are unchanged.

## Non-goals

- **The other scopes of this split:**
  - `sandbox_landlock_provider`: the confined child-process provider on Linux. It reuses this spec's `GitRepo` type and clone-argument helper.
  - `sandbox_gateway`: the gateway service, its client and its server. The server's Podman backend uses this provider unchanged.
- **Second-release work:** the Kubernetes gateway backend, the OpenShell provider, and D3 (c).
- **A network allowlist on Podman or Docker.** That needs an egress proxy. `NetworkAllowlist` is refused, not approximated.
- **Bounding disk.** `Resources.DiskBytes` is refused (DD 11).
- **Choosing defaults for unset `Resources`.** A zero field passes no flag, and the engine's own default applies (DD 12).
- **Private repositories, credentials, commit-SHA refs, submodules and Git LFS in `GitRepo`.** A private repository is cloned on the host and synced as a `LocalDir`.
- **Container-platform features:**
  - building images, beyond the test images the integration tests derive locally;
  - registry login or pull-policy options;
  - custom seccomp profiles;
  - choosing the container user;
  - extra engine flags;
  - device or GPU passthrough;
  - port publishing;
  - volumes beyond the workspace;
  - interactive attach.
- **The Docker or Podman Go API clients.** The provider drives the CLIs (PRD 11 D4).
- **Reattaching to a sandbox from another process**, and persistent or shared sandboxes.
- **Changing the agent, the protocol or the images.** There is no `ProtocolMinor` bump, no new agent flag and no Containerfile change.
- **CI changes.** The provider's integration tests are not added to the release workflow.
- **Windows hosts.** The package builds there and its behaviour is best effort (steering).

## Background

**What PRD 11 decided for this scope.**
- **D4:** `podman` and `docker` are driven through their command lines, and a pure function building the argument list is tested offline. The micro-VM variant is the same provider with a runtime flag.
- **§3.3:** the isolation list: no host mounts, a read-only root, an explicit environment, `--cap-drop ALL`, `no-new-privileges`, resource flags, a TTL enforced twice, labels, and `Reap`.
- **§3.5:** the `GitRepo` init step.
- **§2:** says that `guard.AllowAll` becomes defensible for a run whose every shell tool is bound to a sandbox. `.specs/13_sandbox_core` DD 24 and `.specs/15_sandbox_landlock_agent` DD 18 deferred that guidance to this scope.

**What the earlier scopes delivered, as the code stands.**
- **The module.** `sandbox/` is a nested module, `github.com/agent-fox-dev/agentkit-go/sandbox` (`sandbox/go.mod`; `docs/errata/14_sandbox_rsync_sync.md` D-2). Import paths are unchanged, and the Makefile's `test`, `vet`, `lint` and `tidy` targets loop over it.
- **The host API (`sandbox/sandbox.go`, `sandbox/spec.go`).**
  - `Provider` has `Capabilities`, `Create` and `Reap`.
  - `Sandbox` has `ID`, `Spec`, `Agent`, `Sync`, `Tools`, `Export` and `Close`.
  - `Source` and `Destination` are sealed by unexported methods, so a new source type must be declared in package `sandbox`. Today the sources are `Tar` and `LocalDir`; there is no `GitRepo`.
  - `Capabilities` has `Provider`, `Isolation` (`IsolationContainer`, `IsolationMicroVM`, …), `Network`, `Resources ResourceCaps`, `Image`, `Workdir`, `Notes` and `Landlock`.
  - `Resources`' doc says "Zero means no bound".
- **`CheckSpec` (`sandbox/check.go`).** It fills in a `TTL` of 0 as 1 hour, and an empty `Workdir` as `/workspace` when `caps.Workdir` is set. It refuses the reserved label key `agentkit.sandbox` and every field outside the capabilities, with a `*SpecError{Provider, Field, Reason}` matching `ErrUnsupported`. A `SpecError` built outside `CheckSpec` prints without a value (`docs/errata/15_sandbox_landlock_agent.md`).
- **`sandbox.AgentArgs(spec)` (`sandbox/agentargs.go`)** returns `--ttl`, `--network` and `--workdir`.
- **Image references (`sandbox/image.go`).** `sandbox.DefaultImage()` returns the base image at the build's exact `X.Y.Z` tag, or an error wrapping `ErrNoPublishedImage` that says to set `Spec.Image`. `OverlayImage()` and `OverlayCopyLine(ref)` give the `COPY --from` line. Their doc comments say providers always pass `--entrypoint AgentPath`.
- **`attach` (`sandbox/internal/attach/attach.go`)** implements `Sandbox` once, for every provider.
  - `attach.Dial(ctx, net.Conn, Config{ID, Spec, Destroy, Metadata, Provider})` opens a one-shot gRPC client and calls `Hello`.
  - It refuses a protocol major mismatch (`ErrProtocol`), and refuses `RequireLandlock` when the agent's report does not satisfy the network mode.
  - It starts the host-side TTL timer, which tears the sandbox down at the TTL.
  - It marks the sandbox unavailable when the connection ends.
  - On any failure it runs `Destroy`, with a 30-second context, before returning.
  - Its `Sync` (`attach/transfer.go`) answers any source other than `Tar` or `LocalDir` with a plain "unsupported source" error.
  - The state (`attach/state.go`: live, unavailable, closed) is unexported.
- **Transport and the agent.** `stdioconn.New(io.ReadCloser, io.WriteCloser) net.Conn` adapts a child's pipes. `agent.New(agent.Config{Workdir, Env, TTL, Landlock})` with `(*Agent).Serve(net.Conn)` is what `local` runs in-process.
- **`agentkit-sandboxd` (`sandbox/cmd/agentkit-sandboxd/main.go`)** behaves as follows:
  - it exits 0 when the host closes the connection, 2 on a usage error (including a `--workdir` that is a system directory), and 1 on a startup failure or at `--ttl`;
  - it hands its own environment to every tool verbatim;
  - on Linux it applies Landlock first: the workspace, `HOME` and the temporary directory are writable, the system directories are read and execute, and TCP is denied under `--network none`. A seccomp `EPERM` on the probe gives ABI 0 with a note.
- **The base image (16).** Debian trixie-slim with `git`, `bash` and `ca-certificates`, user uid 1000 with home `/home/agentkit`, and `/workspace` owned by 1000:1000. It declares no `ENV` or `VOLUME` beyond Debian's `PATH`. Its documentation says providers pass `--entrypoint sandbox.AgentPath` and `--init`.
- **The image tests.** `sandbox/images/harness_test.go` already starts an agent with `<engine> run --rm -i --name … --network none --env … --entrypoint <AgentPath> <image> <AgentArgs…>` and attaches with `stdioconn` and `attach.Dial`. It picks the engine from `AGENTKIT_CONTAINER`, then `podman`, then `docker`, and skips without `AGENTKIT_SANDBOX_BASE_IMAGE`. `docs/errata/16_sandbox_images.md` D-4 records that this ran with Podman 5 on podman-machine, where the agent reported Landlock ABI 4.
- **`local`** keeps a marker with the creating pid and reaps by pid liveness (`sandbox/local/`).

**What the root module offers.**
- **`tools.RunArgv(ctx, argv, ExecOptions)` (`tools/exec.go`)** runs a program with no shell, in its own process group. It kills the group on timeout or cancellation, interleaves stdout and stderr into one tail-truncated `ExecResult.Output`, and resolves the program through `ExecOptions.Env`'s `PATH`. A nil `Env` means `ReducedEnv`, which strips `DOCKER_HOST`, `CONTAINER_HOST` and `XDG_RUNTIME_DIR`. RunArgv only captures output; it cannot hold a long-lived stdio connection.
- **`mcp/transport.go`** shows the pattern for a long-lived child: an explicit environment, `Setpgid`, private `os.Pipe` stdout, stderr drained on a goroutine, and a group kill on close. Its helpers are unexported in another module, so the provider carries its own small copies.
- **`guard/guard.go`.** The package doc says `Restricted` "is a floor for unattended safety, not a sandbox". `AllowAll`'s doc calls it "the explicit opt-out".

**Policy tests that constrain the work.**
- `internal/policy/landlock_policy_test.go` (TS-15-39): only `agentkit-sandboxd` may import `sandbox/internal/confine`.
- `internal/policy/deps_test.go`: the sandbox module's direct set is checked against the allowlist.
- The cross-target gate builds the sandbox module for windows/amd64 too.

## Requirements

### 1. `GitRepo`, `ErrNoAgent`, and refusing `GitRepo` where it is not supported

Package `sandbox` gains:

```go
// GitRepo is a public git repository cloned into the workspace by the
// provider's init step. It is a Source.
type GitRepo struct {
    URL   string // https only
    Ref   string // a branch or tag; empty means the remote's default branch
    Depth int    // > 0: shallow clone of that depth; 0: full history
}
var ErrNoAgent = errors.New("sandbox: the image has no executable agent at sandbox.AgentPath")
```

**Validation and clone arguments.** A new internal package, `sandbox/internal/gitclone`, holds:
- `Check(sandbox.GitRepo) error`;
- `Args(repo sandbox.GitRepo, dir string) []string`, the argument list that follows `git`.

`sandbox_landlock_provider` will reuse both.

`Check` refuses each of the following with an error naming the field:
- a `URL` whose scheme is not `https`, or that has no host, carries user information, has a fragment, or contains whitespace or control characters;
- a `Ref` that starts with `-` or `/`, contains `..`, or holds a character other than letters, digits, `.`, `_`, `-` and `/`;
- a negative `Depth`.

`Args` returns, in order:
1. `-c protocol.allow=never -c protocol.https.allow=always clone`;
2. `--depth N`, only when `Depth > 0`;
3. `--branch REF`, only when `Ref` is set;
4. `--`, then the URL, then `dir`.

**Where `GitRepo` is not supported.** `attach`'s `Sync` answers a `GitRepo` with an error that wraps `ErrUnsupported` and names the provider (`Config.Provider`), and it makes no RPC. As a result, `local` refuses `GitRepo` without any change of its own.

**A state accessor.** `attach` gains an internal accessor, `Live(sandbox.Sandbox) error`, which reports a sandbox's state the same way the existing gate does: nil, an error matching `ErrUnavailable`, or `ErrClosed`. A provider that wraps an attached sandbox uses it.

### 2. Package `sandbox/podman`: options, `New` and capabilities

```go
package podman
type Engine string
const (EnginePodman Engine = "podman"; EngineDocker Engine = "docker")
type Options struct {
    Binary    string   // the engine command; default "podman"
    Engine    Engine   // empty: detected from `<Binary> --version`
    Runtime   string   // "" (engine default) or "krun" (libkrun micro-VM; podman only)
    UserNS    string   // passed as --userns=<UserNS> when set
    EngineEnv []string // environment of every engine command; nil means os.Environ()
}
func New(o Options) (sandbox.Provider, error)
func Args(o Options, id string, created time.Time, spec sandbox.Spec) []string
func InitArgs(o Options, id string, created time.Time, spec sandbox.Spec, repo sandbox.GitRepo) []string
```

**What `New` does.**
- **Resolves the binary** through `EngineEnv`'s `PATH`. When it is not found, `New` fails with an error that names the binary and says to install podman or docker, or to set `Options.Binary`.
- **Detects the engine** when `Engine` is empty. It runs `<Binary> --version` once, with a 30-second timeout, and passes the output to a pure, unexported parser:
  - output beginning `podman version` means `EnginePodman`, which includes Docker shims that run podman;
  - output beginning `Docker version` means `EngineDocker`;
  - anything else is an error that quotes the output.
- **Validates the options.** An explicit `Engine` other than the two constants is refused. A `Runtime` other than `""` or `krun` is refused; the message names `crun-vm` as unsupported because it boots VM disk images, not OCI images with an entrypoint. `krun` with `EngineDocker` is refused.
- **Runs no other command.**

**`Capabilities()`** returns:
- `Provider`: the engine name, `podman` or `docker`;
- `Isolation`: `IsolationMicroVM` with `krun`, `IsolationContainer` otherwise;
- `Network`: `[NetworkNone, NetworkUnrestricted]`;
- `Resources`: `CPUs`, `MemoryBytes` and `PIDs` true, `DiskBytes` false;
- `Image`, `Workdir` and `Landlock`: true;
- `Notes`, stating plainly:
  - there is no host allowlist;
  - `DiskBytes` is not bounded;
  - a zero `Resources` field takes the engine's default;
  - `/tmp` is a memory-backed tmpfs that counts against `MemoryBytes`;
  - `Spec.Env` values appear on the engine's command line on the host;
  - the engine's default seccomp profile may withhold Landlock, which the agent then reports.

### 3. Spec checks beyond `CheckSpec`

`Create` calls `sandbox.CheckSpec(p.Capabilities(), spec)` first. It then refuses each of the following with a `*sandbox.SpecError{Provider, Field, Reason}` naming the field. All of these checks run before any engine command.
- **`Spec.Provider`:** any key. The provider defines none, and the error names the first key in sorted order.
- **`Labels`:**
  - a key with the prefix `agentkit.sandbox.`, which the provider reserves;
  - a key that is empty, or contains `=`, whitespace or control characters;
  - a value with control characters.
- **`Workdir`** (effective): one that is not absolute and clean, or is `/`, `/tmp` or beneath it, or is beneath `/proc`, `/sys` or `/dev`, or contains `,` or control characters. The agent itself refuses system directories with exit 2, which surfaces as a startup failure (Requirement 5).
- **`Image`:** one that starts with `-`, or contains whitespace or control characters.
- **`Env`:** a `HOME` or `TMPDIR` value that is not absolute, or is not `/tmp`, beneath `/tmp`, the workspace, or beneath the workspace. Under `--read-only`, nothing else is writable.

**The image.** When `Spec.Image` is empty, the image is `sandbox.DefaultImage()`. Its error, which wraps `ErrNoPublishedImage`, is returned as is. `Sandbox.Spec().Image` reports the image actually run.

### 4. `Args`: the sandbox container's command line

`Args` is a pure function of its inputs. It returns the argument list after the binary, for an effective spec with `Image` set. For the zero effective spec under `EnginePodman`, the list is exactly this, in this order:

```
run --rm -i --init
--name agentkit-<id>
--label agentkit.sandbox=<id>
--label agentkit.sandbox.expires=<created+TTL, RFC 3339, UTC, seconds>
--log-driver none
--network none
--cap-drop ALL
--security-opt no-new-privileges
--read-only
--tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777
--mount type=volume,source=agentkit-<id>,target=<Workdir>
--unsetenv-all
--env HOME=/tmp --env LANG=C.UTF-8 --env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin --env TERM=dumb --env TMPDIR=/tmp
--entrypoint /usr/local/libexec/agentkit-sandboxd
<Image>
<sandbox.AgentArgs(spec)...>
```

Variations on that list:
- **Labels.** The embedder's `Labels` follow the two reserved labels as `--label k=v`, sorted by key.
- **Network.** `NetworkUnrestricted` drops `--network none`, so the engine's default network applies.
- **Resources.** These follow `--mount`, in this order:
  - `CPUs` adds `--cpus <shortest decimal>`;
  - `MemoryBytes` adds `--memory <n> --memory-swap <n>`, equal so that swap cannot exceed the bound;
  - `PIDs` adds `--pids-limit <n>`.
- **Runtime and user namespace.** `--runtime krun`, then `--userns=<v>`, follow the resources.
- **Docker.** `EngineDocker` omits `--unsetenv-all`. Nothing else differs.
- **Environment.** It is `Spec.Env`, plus each of the five defaults that `Spec.Env` does not set. It is rendered as `--env NAME=value`, sorted by name.

No other flag is ever emitted. In particular, there is no `-v`/`--volume`, no `--mount type=bind`, no `--privileged`, no `--cap-add`, no `--security-opt seccomp=…` and no `--env NAME` without a value. The backend's default seccomp profile applies.

### 5. `Create`

`Create(ctx, spec)` runs these steps:

1. **Check.** It applies Requirement 3, then generates a 16-hex-character random id and takes `created = time.Now()`.
2. **Volume.** It runs `volume create` with the two reserved labels and the embedder's labels, naming the volume `agentkit-<id>`.
3. **The engine process.** It starts the binary with `Args`, using `EngineEnv`, in its own process group (a no-op on Windows). Its stdin is a pipe, its stdout a private `os.Pipe`, and its stderr is drained into a bounded tail buffer (the last 8 KiB), so the child never blocks.
4. **Attach.** It calls `attach.Dial(ctx, stdioconn.New(stdout, stdin), attach.Config{ID: id, Spec: eff, Destroy: <Requirement 6>, Provider: caps.Provider})`.
5. **Return.** It returns the sandbox wrapped by the provider's own type (Requirement 7), which forwards every method to `attach` except `Sync` of a `GitRepo` and the bookkeeping around `Tools` and `Sync`.

Every short engine command (`volume create`, `rm`, `volume rm`, listings, the diagnostic) runs through `tools.RunArgv` with `EngineEnv`, a 2-minute timeout and a bounded output tail. A missing container or volume during removal is not an error.

**When `attach.Dial` fails.** `attach` has already run the destroy step, so nothing is left behind. The error is then classified:
- **`ErrProtocol` and the `RequireLandlock` `*SpecError`** pass through unchanged.
- **A done `ctx`** returns an error wrapping `ctx.Err()`.
- **The engine process exited 125** (an engine error, such as a failed pull or an unknown flag): the error names the engine and its exit status, and carries the stderr tail.
- **Any other non-zero exit:** the provider runs a diagnostic under a fresh 30-second context. The diagnostic is `run --rm` of the same image, with the reserved labels, `--log-driver none`, `--network none`, `--cap-drop ALL`, `--security-opt no-new-privileges`, `--read-only`, the runtime and userns flags, `--entrypoint <AgentPath>` and `--version`, and no volume.
  - If the diagnostic exits 126 or 127, `Create` returns an error wrapping `sandbox.ErrNoAgent`. The message names the image and `sandbox.AgentPath`, gives the fix as `sandbox.OverlayCopyLine(ref)`, and carries the engine's output. Here `ref` is `sandbox.OverlayImage()`, or, for a build with no published image, `sandbox.OverlayImageRepo + ":<version>"` together with the hint to build one with `make sandbox-images`.
  - Otherwise, the handshake error is returned with the original stderr tail.

### 6. Close, TTL and the agent's end

**The destroy step** runs these actions in order. Each runs even if an earlier one failed, and the errors are joined.
1. Wait up to 5 seconds for the engine process to exit; the connection is already closed, so the agent sees EOF and exits 0. If it has not exited, kill its process group.
2. `rm -f agentkit-<id>`, then `rm -f agentkit-<id>-init`.
3. `volume rm agentkit-<id>`, retried for up to 10 seconds while the engine reports the volume in use.

The whole step fits in `attach`'s 30-second destroy budget. `Close` stays idempotent, through `attach`.

**The TTL is enforced on both sides.**
- `attach`'s host timer destroys the sandbox at its TTL.
- The agent's own `--ttl` ends the container, which `--rm` then removes, even when the host is gone.
- The volume of a host that died carries its expiry label, so `Reap` can remove it.

**When the agent ends early,** `attach` marks the sandbox unavailable. Its volume stays until `Close`, the TTL or `Reap`.

### 7. `GitRepo` sync through the init step

The provider's `Sync` handles a `GitRepo` itself. Every other source goes to `attach`.

**The `GitRepo` slot.** A `GitRepo` is accepted only while the slot is free. Each of these takes the slot:
- a successful `GitRepo` sync;
- any other successful `Sync`;
- the first `Tools` call, whatever its outcome.

A `GitRepo` while the slot is taken is refused, with an error naming the rule ("GitRepo must be the first Sync and come before Tools"). A failed `GitRepo` sync does not take the slot. `Tools` and `Sync` on the same sandbox are serialised against an in-flight `GitRepo` sync.

**Order of checks.** Each of these runs before any engine command:
1. `attach.Live`: a closed or unavailable sandbox returns its error;
2. `gitclone.Check`;
3. `Agent().Git`: false means the error says the image needs `git` on its `PATH` for a `GitRepo`.

**`InitArgs`** is pure. For engine `podman`, it returns:

```
run --rm --init
--name agentkit-<id>-init
<the same reserved and embedder labels>
--log-driver none
--cap-drop ALL
--security-opt no-new-privileges
--read-only
--tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777
--mount type=volume,source=agentkit-<id>,target=<Workdir>
<the same resource, runtime and userns flags>
--unsetenv-all
--env GIT_TERMINAL_PROMPT=0 --env HOME=/tmp --env LANG=C.UTF-8 --env PATH=<the default PATH>
--entrypoint git
<Image>
<gitclone.Args(repo, Workdir)...>
```

It has no `--network none` (the engine's default network), no `Spec.Env` value, and no `-i`. The Docker variant omits `--unsetenv-all`.

**Running it.** The init step runs through `tools.RunArgv`, bounded by the `Sync` context and by the sandbox's expiry. The agent container runs beside it on the same volume, idle, with its own network policy, and none of the init step's network reaches it.
- **Exit 0:** `SyncResult` is all zeros. This is documented: the clone's files are visible to the tools.
- **Exit 126 or 127:** the image cannot run `git`, and the error says so.
- **Any other non-zero exit:** an error naming the URL and carrying the output tail.
- **Cancellation, failure or timeout:** the provider runs `rm -f agentkit-<id>-init` before returning.

### 8. `Reap`

`Reap(ctx)` lists the engine's containers (`ps -a`) and volumes (`volume ls`) that carry the label key `agentkit.sandbox`. It reads each one's labels from the engine's inspect output as JSON.

It removes every container (with `rm -f`) and every volume whose `agentkit.sandbox.expires` parses and is not after the current time. Containers go before volumes.

It leaves alone:
- every unexpired entry;
- every entry whose expiry label is missing or unparsable;
- every entry whose labels could not be read.

It returns the distinct sandbox ids it removed something for, sorted. If some removals failed, it returns the ids it did remove together with the joined errors.

### 9. Tests

**Offline, in `make check`.** The provider's command runner is an unexported seam: one method runs a short command, another starts the long-lived engine process. Tests substitute a fake that records argv and serves an in-process `agent.New` over pipes, as `local` does. The offline tests cover:
- **Golden argv.** `Args` and `InitArgs` for:
  - the zero spec under both engines;
  - `NetworkUnrestricted`;
  - every resource field;
  - `krun` and `UserNS`;
  - `Env` overriding a default;
  - sorted labels.

  Each golden also asserts the absence of every forbidden flag in Requirement 4.
- **Refusals.** Every refusal in Requirements 2, 3 and 7. `NetworkAllowlist` and `DiskBytes` are refused through `CheckSpec`, naming the provider.
- **The `--version` parser.**
- **Create.**
  - `volume create` precedes `run`, and `run` carries exactly `Args`.
  - Create succeeds through `Hello`.
  - The destroy step's commands run in order, and `Close` is idempotent.
  - Exit 125 gives an error carrying the stderr tail.
  - A diagnostic exit of 127 gives `ErrNoAgent` with `AgentPath` and the copy line.
  - With an empty `Spec.Image` in a `(devel)` test build, `ErrNoPublishedImage` comes back with no engine command run.
  - Every failure path removes the volume.
- **The `GitRepo` slot rules,** and cleanup after a cancelled init step.
- **`local` refusing `GitRepo`** with `ErrUnsupported`.
- **Reap's expiry rule** over fake listings.

**With an engine, opt-in.** The package's integration tests skip, and do not fail, under `-short`, without an engine, or without `AGENTKIT_SANDBOX_BASE_IMAGE`. They choose the engine as 16's harness does (`AGENTKIT_CONTAINER`, then `podman`, then `docker`). They cover Goals 2–7, plus:
- a second `LocalDir` sync after editing one host file reports `Files == 1`, and `Export` brings the model's edits back;
- on podman with an applied Landlock ABI of 2 or more, writing to `/var/tmp` fails with a permission error and not a read-only-filesystem error;
- a `RequireLandlock` spec succeeds when the kernel offers ABI 4.

Some cases have extra gates or inputs:
- The `GitRepo` case clones `AGENTKIT_SANDBOX_GIT_URL`, which defaults to `https://github.com/agent-fox-dev/agentkit-go`, at depth 1.
- The micro-VM case runs only with `AGENTKIT_SANDBOX_RUNTIME=krun`.
- The missing-agent image is derived locally as `FROM <base>`, `USER root` and `RUN rm <AgentPath>`, and is removed afterwards.

**The make target.** A new `make sandbox-podman-test` target depends on `sandbox-images`. It runs `go test -count=1 -v ./podman/` in `sandbox/`, with the image and engine variables 16's target sets. It is not part of `make check`.

### 10. Documentation

Per steering, run `make doc`, then update:
- **`guard`'s package doc comment and `AllowAll`'s doc comment** (doc only; no exported change):
  - `Restricted` remains a floor;
  - a sandbox is the boundary the guard never claimed to be;
  - `AllowAll` is reasonable only when every shell tool the agent can reach is a proxy from a sandbox whose provider reports `IsolationContainer` or `IsolationMicroVM`, under `NetworkNone` (or with egress the embedder accepts), with `Resources` set, and with exported files treated as untrusted;
  - it is never reasonable with `local` or with any local shell tool in the set;
  - `New` still requires a `Guard`.

  `docs/configuration.md`'s `guard.Options` section says the same.
- **`docs/configuration.md`:**
  - `podman.Options`, `New`, `Engine`, `Args` and `InitArgs`;
  - the hardening flags as a table;
  - the default environment and the `HOME`/`TMPDIR` rule;
  - the labels and the expiry rule of `Reap`;
  - `sandbox.GitRepo`, its validation and the `GitRepo` slot;
  - `sandbox.ErrNoAgent`;
  - a `podman`/`docker` column in "Which provider honours which field";
  - the test variables and `make sandbox-podman-test`.
- **`docs/architecture.md`:** a "The Podman and Docker provider" part of the Sandboxes section, covering the `Create` sequence, the init step beside the idle agent, the two-sided TTL, `Reap`, and the missing-agent diagnostic.
- **`README.md`:** the Sandboxes section names `sandbox/podman` as the first isolating provider and replaces "`local` is the only provider so far". The make-targets table gains `sandbox-podman-test`, and the packages table gains `sandbox/podman`.
- **`docs/DEPS.md`:** "Rejected" rows for the Docker Go client and the Podman Go bindings, with D4's reason (large dependency graphs, against the allowlist rule).
- **`examples/sandbox`:** a `--podman` flag (and `--image`) that runs the same program on the real provider under `NetworkNone`. The default run stays offline over `local`, and its `main_test.go` is unchanged.

## Design Decisions

1. **`GitRepo` is a `Sync` source, run as a separate init container beside an idle agent, rather than a field of `Spec` cloned before the agent starts.** PRD 11 puts `GitRepo` in `Sync` and only allows it "before the first Tools call". The agent has to be attached before `Create` returns, so "before the sandbox proper starts" can only mean "before the sandbox has run anything". An agent with no tool set runs nothing, and the clone container's network never reaches the agent's container.
2. **The `GitRepo` slot is taken by a successful `GitRepo`, by any successful `Sync`, and by the first `Tools` call.** `git clone` needs an empty directory. Before `Tools`, the agent has no symbol table to go stale.
3. **`https` only, with `protocol.allow=never` and `protocol.https.allow=always`, and `--` before the URL.** A public clone needs no other transport. `ext::`, `file://` and `ssh` must not be reachable even by redirect, and a URL must never be read as an option.
4. **`Args` and `InitArgs` take `(Options, id, created, Spec)` instead of PRD 11's `Args(Spec)`.** The container name, the volume and the expiry label depend on them. Taking them as parameters keeps the functions pure and goldenable.
5. **The engine flavour is detected once from `--version`, and only `--unsetenv-all` and `--runtime krun` depend on it.** A single argv for both engines is the goal, and a `docker` shim that runs podman is then treated as podman.
6. **One tmpfs at `/tmp`, with `exec`, and `HOME` defaulting to `/tmp`.** Under `--read-only`, only the workspace and `/tmp` are writable. A tmpfs at the image user's home cannot be given the right owner without inspecting the image, while `/tmp` (mode 1777) works for any user. The agent's Landlock ruleset collapses `HOME` and `TMPDIR` into one root. `go test` executes binaries from `TMPDIR`, which rules out `noexec`.
7. **`HOME` and `TMPDIR` in `Spec.Env` must lie under `/tmp` or the workspace.** Anything else is read-only in the container, and the result would be a confusing failure at the first spill or build instead of a clear refusal at `Create`.
8. **The environment is set with `--env NAME=value` on the command line, plus `--unsetenv-all` on podman.** `--env NAME` would read the engine client's own environment, where `DOCKER_HOST` or `PATH` collide. An env file adds a host file and cannot carry newlines. Values appear in the host's process list, so the `Notes` and the docs say so, and credentials never belong in `Spec.Env` (PRD 11 §3.3). Docker still adds `HOSTNAME`.
9. **`--log-driver none`.** The agent's stdout is the gRPC stream, carrying file contents and tool results. A log driver would copy all of it to journald or a host file. Attach does not depend on the log driver, and the integration tests confirm this on both engines.
10. **`--init`, `--rm` and `-i`, with the agent's stdio as the connection, and no `podman exec`.** The agent must be the container's own process, so that its `--ttl` ends the container when the host is gone (PRD 11 §3.3). 16 assigns zombie reaping to `--init`, and 16's harness has already shown `run --rm -i` attach working on Podman 5.
11. **`DiskBytes` is refused.** A volume size limit works only with some drivers and filesystems (a quota-capable local driver), and the engines do not report support up front. A size limit that is silently ignored is exactly the widening PRD 11's goal 4 forbids.
12. **A zero `Resources` field passes no flag, and the engine's default applies.** `Resources`' doc in `sandbox/spec.go` already says "zero means no bound" from AgentKit. Per-provider defaults would make the same `Spec` mean different things. Podman's own default of 2048 PIDs still applies, and the docs recommend setting `Resources`.
13. **Only `krun` is offered as a micro-VM runtime, and `crun-vm` is refused.** krun runs OCI images in a libkrun VM with the same entrypoint semantics. crun-vm boots VM disk images, so it cannot start the agent at `AgentPath` on stdio. The VM's kernel may lack Landlock, which the agent reports.
14. **A missing agent is diagnosed after a failed handshake by a `--version` run without `--init`.** With `--init`, the init process (catatonit or tini) decides the exit status, so 126 and 127 cannot be trusted from the main run. The diagnostic costs nothing on the success path, and it uses the runtime's own 126/127 convention.
15. **`Reap` removes only expired sandboxes, using an expiry label written by the host's clock.** A pid label cannot tell whether a sandbox belongs to a live process on another host (a gateway, or a second embedder). After its TTL a sandbox is dead whoever owns it, so the expiry rule can never break a live one. The cost is that a crashed host's volume lingers until its TTL; its container has already ended when stdin closed or the TTL passed. The engine's own `Created` time is not used, because a podman machine's clock can drift from the host's.
16. **The label prefix `agentkit.sandbox.` is reserved by this provider and enforced here, not in `CheckSpec`.** Only this provider writes such labels. Changing `CheckSpec` would alter 13's property oracle for no other provider's benefit.
17. **Provider refusals use `*sandbox.SpecError` throughout.** A caller then matches one shape (`ErrUnsupported`) for every reason a provider declines a spec, as `attach`'s `RequireLandlock` refusal already does.
18. **The workdir must exist in the image, owned by the image's user (or the image runs as root). The provider does not chown and does not choose a user.** The base image satisfies this. Only podman has a chown option, and selecting the user is a non-goal. With `--cap-drop ALL` and `no-new-privileges`, a root user has no capabilities, and Landlock still applies.
19. **The engine runs with the embedder's full environment by default (`EngineEnv` nil means `os.Environ()`), not `ReducedEnv`.** Rootless podman and remote engines need `XDG_RUNTIME_DIR`, `CONTAINER_HOST` or `DOCKER_HOST`. Nothing from that environment reaches the container, because only explicit `--env NAME=value` does.
20. **Short commands go through `tools.RunArgv`, and the long-lived agent process through `os/exec` with the provider's own process-group helpers.** D4 names `RunArgv`, and it gives group kill on cancel. RunArgv only captures output, though, so it cannot carry a stdio connection, and `mcp`'s helpers are unexported in another module.
21. **`ErrNoAgent` lives in package `sandbox`.** Later container backends (the gateway's Podman and Kubernetes backends) can return the same sentinel.
22. **The integration tests reuse 16's variables and harness conventions, through a new `make sandbox-podman-test`.** The provider must run the image that matches the host's tool definitions, and only a local build guarantees that. `make check` must stay offline.
23. **The `AllowAll` guidance is written here and in `guard`'s doc comments only, with no code helper.** This is the first scope where the claim is true. A helper that inspected `Capabilities` would give a policy judgement the appearance of a mechanism.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | depends on, modifies | Builds on `Provider`, `Sandbox`, `Spec`, `Capabilities`, `CheckSpec`, `SpecError`, `attach.Dial` with its destroy-on-failure and TTL timer, `stdioconn`, `agent.New` (for the offline seam) and `local`. Adds `sandbox.GitRepo`, `sandbox.ErrNoAgent`, `attach`'s `GitRepo` refusal and the `attach.Live` accessor. |
| `14_sandbox_rsync_sync` | depends on | `LocalDir` `Sync` and `Export` run unchanged over the engine's stdio, and the integration tests rely on them. |
| `15_sandbox_landlock_agent` | depends on | `sandbox.AgentArgs` (`--network`, `--ttl`, `--workdir`), `Capabilities.Landlock`, the post-`Hello` `RequireLandlock` check, and the ruleset's `HOME`/`TMPDIR` roots, which shape the `/tmp` decision. 15 assigned `no-new-privileges` and seccomp handling to this scope. |
| `16_sandbox_images` | depends on | `DefaultImage`, `OverlayImage`, `OverlayCopyLine`, `OverlayImageRepo`, `AgentPath`, the base image's layout (uid 1000, `/workspace` owned by it, `git`), `make sandbox-images`, and the harness conventions and variables. 16 assigned `--entrypoint`, `--init`, `--read-only`, the tmpfs, the volume and the default image to this scope. |
| `12_core_schema_guard` | modifies | Changes only the `guard` package and `AllowAll` doc comments (the guidance 13 DD 24 deferred). No exported identifier changes. |
| `11_minimal_driver` | depends on | `New`'s refusal of an unguarded shell tool (`checkShellGuard`) applies to proxy tools by name, and is what keeps a `Guard` mandatory for sandboxed runs. |

## Verified External API

**In-repository code (read):**
- `tools.RunArgv(ctx context.Context, argv []string, opts tools.ExecOptions) (tools.ExecResult, error)`. It returns an error only when the program cannot be resolved or started; the exit status is in the result.
- `tools.ExecOptions{Dir string; Timeout time.Duration; MaxBytes int; KeepHead bool; SpillDir string; LogPath string; Stdin io.Reader; Env []string; DrainIdle, DrainCeiling time.Duration}`. A nil `Env` means `ReducedEnv(nil)`; a non-nil one is used verbatim.
- `tools.ExecResult{Output string; Outcome tools.Outcome; ExitCode int; Truncated bool; TotalBytes int64; SpillPath string; Duration time.Duration; IOErr error}`.
- `sandbox.CheckSpec(caps Capabilities, spec Spec) (Spec, error)`; `sandbox.SpecError{Provider, Field, Reason string}`, whose `Is` matches `ErrUnsupported`; `sandbox.AgentArgs(spec Spec) []string`; `sandbox.AgentPath`.
- `sandbox.DefaultImage() (string, error)`, `sandbox.OverlayImage() (string, error)`, `sandbox.OverlayCopyLine(ref string) string`, `sandbox.OverlayImageRepo`, `sandbox.ErrNoPublishedImage`.
- `sandbox.Capabilities{Provider string; Isolation Isolation; Network []NetworkMode; Resources ResourceCaps; Image, Workdir bool; Notes string; Landlock bool}`; `sandbox.ResourceCaps{CPUs, MemoryBytes, PIDs, DiskBytes bool}`; `sandbox.Resources{CPUs float64; MemoryBytes, PIDs, DiskBytes int64}`.
- `attach.Dial(ctx context.Context, conn net.Conn, cfg attach.Config) (sandbox.Sandbox, error)`, with `attach.Config{ID string; Spec sandbox.Spec; Destroy func(context.Context) error; Metadata metadata.MD; Provider string}`. `Destroy` runs once, with a fresh 30-second context, on every failure inside `Dial` and on `Close`.
- `stdioconn.New(r io.ReadCloser, w io.WriteCloser) net.Conn`.
- `agent.New(agent.Config{Workdir string; Env []string; TTL time.Duration; Landlock sandbox.LandlockInfo}) (*agent.Agent, error)` and `(*agent.Agent).Serve(net.Conn) error`, as `sandbox/local/local.go` calls them.

**Podman CLI** (Podman 4 or later assumed; the integration tests are the check). **Unverified**: the program is outside the repository. The surface assumed:
- `--version` printing `podman version X.Y.Z`;
- `run` with `--rm`, `-i`, `--init`, `--name`, `--label k=v`, `--log-driver none`, `--network none`, `--cap-drop ALL`, `--security-opt no-new-privileges`, `--read-only`, `--tmpfs /tmp:rw,exec,nosuid,nodev,mode=1777`, `--mount type=volume,source=S,target=T`, `--cpus`, `--memory`, `--memory-swap`, `--pids-limit`, `--runtime krun`, `--userns=V`, `--unsetenv-all`, `--env K=V` and `--entrypoint`;
- `volume create --label k=v NAME`, `volume rm NAME`, `rm -f NAME`;
- `ps -a --filter label=KEY`, `volume ls --filter label=KEY`, and `inspect` / `volume inspect --format '{{json .Config.Labels}}'` / `'{{json .Labels}}'`;
- `--read-only` adding tmpfs mounts at `/run` and `/var/tmp` by default (`--read-only-tmpfs` defaults to true);
- named volumes copying up the image's content and ownership at the mount point.

**Docker CLI** (Docker 20.10 or later assumed). **Unverified**: the same surface without `--unsetenv-all` and `--runtime krun`, plus:
- `--version` printing `Docker version X.Y.Z, build …`;
- `run -i` closing the container's stdin when the client detaches;
- copy-up into named volumes.

**Exit status convention** (both engines). **Unverified**: 125 is an engine error, 126 a command that cannot be invoked, 127 a command that cannot be found, when the entrypoint is executed directly without `--init`.

**git.** **Unverified**:
- `git -c protocol.allow=never -c protocol.https.allow=always clone [--depth N] [--branch REF] -- URL DIR` into an existing empty directory;
- `GIT_TERMINAL_PROMPT=0` makes an authentication request fail instead of prompting.
