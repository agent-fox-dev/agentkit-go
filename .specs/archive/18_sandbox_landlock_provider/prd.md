---
spec_id: "18"
spec_name: "sandbox_landlock_provider"
title: "Landlock sandbox provider: a confined child process on Linux"
status: "active"
created_at: "2026-10-13T15:11:36.496385Z"
updated_at: "2026-10-13T15:11:36.496385Z"
intent_hash: "5eb35357693e308e452ee4398c6072f2a80703f7f4c38c096401544d8cbccf87"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Give AgentKit a sandbox provider that needs no container engine, no image and no privileges. On a Linux host whose kernel offers Landlock ABI 4, the provider runs `agentkit-sandboxd` as a child process of the embedder, and the agent's own Landlock ruleset is the only boundary. The model's commands can then use the host's own toolchain, but cannot read or write the embedder's files or home directory, and under `NetworkNone` cannot open TCP connections. The provider is a confinement, not an isolation: it bounds no resources and shares the host kernel. It says so in its capabilities, refuses every spec field it cannot honour, cleans up everything it started, and never confines the embedder's own process.

## Goals

1. **Confinement works on a Landlock ABI 4 kernel.** On Linux with Landlock ABI 4 or later, take `landlock.New` with a test-built agent and a temporary `Root`, then call `Create` with the zero `Spec` (so `NetworkNone`), `Sync` of a `LocalDir`, `Tools`, and proxy `execute`. Then:
   - creating a file in the workspace succeeds;
   - creating a file in the test's real home directory fails with a permission error;
   - `cat` of a file in a sibling temporary directory outside the ruleset fails with a permission error;
   - a TCP connect to a loopback listener the test opened fails, and the listener accepts nothing;
   - with `NetworkUnrestricted` the same connect succeeds;
   - `Agent().Landlock` reports `ABI == 4`, `TCP == true` under `NetworkNone`, and the sandbox's resolved `workspace/`, `home/` and `tmp/` among `Writable`;
   - `env` prints exactly `Spec.Env` plus `HOME`, `TMPDIR` and `PATH`, with `HOME` and `TMPDIR` being the sandbox's own `home/` and `tmp/`.

   Below ABI 4 the test skips and names the ABI it found.
2. **The host toolchain is reachable only where a rule allows it.** `execute` of `go version` succeeds when the `go` on the sandbox's `PATH` lies beneath a system directory, or beneath a directory named in `Options.ExtraRO`. It fails with a permission error when that `go` lies only beneath a directory outside the ruleset.
3. **What cannot be honoured is refused before anything exists.** `Create` refuses each of these with a `*sandbox.SpecError` that names the provider `landlock` and the field, and creates no directory and starts no process: any `Resources` field, `NetworkAllowlist`, `Image`, `Workdir`, any `Spec.Provider` key, and a `Spec.Env` that sets `HOME` or `TMPDIR`.

   `New` fails, wrapping `landlock.ErrNoLandlock`, in these cases:
   - on a non-Linux `GOOS`, naming `sandbox/podman`;
   - for a probed kernel ABI of 0 to 3 (simulated through the probe seam), naming the ABI found, ABI 4 and Linux 6.7;
   - when the probe itself fails, naming the error.

   `New` also fails, without that sentinel:
   - when no agent binary is found, naming every place it looked and the `go install` line;
   - when an `ExtraRO` or `ExtraRW` entry is relative, or equals, contains or lies beneath `Root`.
4. **The lifecycle leaves nothing behind.** On Linux with ABI 4:
   - `execute` starts a background `sleep` with its output redirected, and then `chmod -R a-w` makes a directory tree read-only. `Close` then kills the agent and the `sleep` and removes the sandbox directory.
   - A sandbox created with `TTL: 5s` and never closed is destroyed by the host at its TTL in the same way.
   - A sandbox whose creating process was killed (a re-executed test child) is removed by `Reap`, which also kills the background process that outlived the agent and returns the sandbox's id.
   - `Reap` never removes a sandbox whose agent is alive, whichever process created it.
   - `Reap`'s decision rule is tested on every platform: owner alive, owner dead, pid reused (a different start time), and a different boot.
5. **`GitRepo` runs through a confined init child.** On Linux with ABI 4 and no network, the init command is replaced through its seam. The init child connects to a loopback listener and writes into the workspace, while proxy `execute` in the sandbox still cannot connect. These are refused:
   - a `GitRepo` after `Tools`;
   - a `GitRepo` after a successful `Sync`.

   A failed init leaves the workspace empty and the slot free. When `AGENTKIT_SANDBOX_GIT_URL` is set, a real public clone under `NetworkNone` puts the repository in the workspace, and a later TCP connect from proxy `execute` still fails.
6. **The host stays unconfined, and the build gains nothing new.** A re-executed test child runs `Create`, a `LocalDir` `Sync`, `Tools`, `execute` and `Close` over this provider. Afterwards it can still create a file outside the workspace and connect to a loopback listener. TS-15-39 still passes. The sandbox module's direct requirements are unchanged. The cross-target gate builds and vets `sandbox/landlock` on all four targets, and `make check` needs no network.

## Non-goals

- **The other scope of this split:** `sandbox_gateway`, which covers the gateway service, its client and its server. Nothing here is gateway-aware.
- **Second-release work:** the Kubernetes backend, the OpenShell provider, and code mode inside the sandbox (PRD 11 D3 (c)).
- **Bounding resources.** CPU, memory, process count and disk are refused, not approximated. That means no cgroups, no `prlimit` and no quota.
- **Any boundary other than Landlock.** That excludes namespaces (user, mount, PID, network), seccomp filters and chroot. For a kernel boundary, use `podman`.
- **Closing Landlock's own gaps at ABI 4.** UDP, pathname and abstract Unix sockets, Multipath TCP, signals to processes the user owns, `chmod` and `stat` stay unrestricted. Raising the agent's applied ABI cap belongs to `15_sandbox_landlock_agent` (DD 4 there), not to this spec. These gaps are documented, not mitigated (DD 19).
- **A host allowlist.** Landlock rules are by port, not by host.
- **Hiding host paths.** The workspace is a host directory, so tool results name host paths beneath `Root`.
- **Private repositories, credentials, proxies, submodules or Git LFS in a `GitRepo`.** Clone on the host and sync the result as a `LocalDir`.
- **Shipping the agent binary.** There are no prebuilt host binaries. The binary comes from `go install` or a checkout (16 assigned this to the present scope).
- **Changing the agent, the protocol or the images.** There is no new agent flag, no `ProtocolMinor` bump and no Containerfile change.
- **Sharing the `GitRepo` slot code with `sandbox/podman`.** Each provider keeps its own wrapper (DD 18).
- **Reattaching to a sandbox from another process,** and persistent or shared sandboxes.
- **macOS and Windows.** The package builds there, and `New` refuses with an error naming `podman`.
- **Changing the `guard` guidance.** Spec 17's text makes `AllowAll` reasonable only for `IsolationContainer` or `IsolationMicroVM`. This provider reports `IsolationConfinement`, so the guidance already excludes it.

## Background

**What PRD 11 decided for this scope (D8 part three, §3.6).** On a Linux host the agent runs as a child process of the embedder, over pipes, and the agent's Landlock ruleset is its only boundary. The ruleset gives:
- a workspace, a private `HOME` and a private `TMPDIR`;
- read and execute on the system directories;
- `ExtraRO` and `ExtraRW` as deliberate widening by the embedder;
- TCP denied under `NetworkNone`;
- `Spec.Env` as the whole environment.

The provider refuses `Resources`, `NetworkAllowlist`, `Image`, and any kernel below ABI 4, whatever `RequireLandlock` says. `New` probes the kernel once. `Reap` removes directories whose marker names a pid that is dead, or that is no longer the agent. The `GitRepo` init step is a second confined child with TCP allowed.

**What the earlier scopes delivered, as the code stands.**

`sandbox/` is a nested module (`sandbox/go.mod`). It contains:
- **Spec types (`sandbox/spec.go`).**
  - `Capabilities` has `Provider`, `Isolation`, `Network`, `Resources ResourceCaps`, `Image`, `Workdir`, `Notes` and `Landlock`.
  - `IsolationConfinement` ("a confined process on the host") already exists, and no provider uses it yet.
  - `Resources`' doc says "zero means no bound".
- **`sandbox.CheckSpec` (`sandbox/check.go`).**
  - It refuses every field outside the capabilities with a `*SpecError` that matches `ErrUnsupported`.
  - It fills in a `TTL` of 0 as one hour.
  - It fills in `Workdir` only when `caps.Workdir` is set.
  - It refuses `RequireLandlock` when `caps.Landlock` is false.
- **`sandbox.AgentArgs(spec)` (`sandbox/agentargs.go`)** returns `--ttl`, `--network`, and `--workdir` only when `Workdir` is non-empty. The agent's `--workdir` defaults to `/workspace`, so this provider must set it explicitly.
- **`sandbox.LandlockInfo` (`sandbox/landlock.go`)** has `ABI`, `KernelABI`, `TCP`, `Note`, `Writable` and `ReadOnly`. `LandlockTCPABI` is 4, and `Satisfies(mode)` requires only ABI 2.
- **`agentkit-sandboxd` (`sandbox/cmd/agentkit-sandboxd/main.go`).**
  - Its flags are `--workdir`, `--ttl`, `--network none|allowlist|unrestricted` (default `none`), and the repeatable `--landlock-ro` and `--landlock-rw`.
  - `--version` prints `agentkit-sandboxd <version> (protocol M.N)` and applies nothing.
  - On Linux it calls `confine.Setup` before anything else, and hands its environment to the tools verbatim.
  - It exits 0 when the host closes the connection. It exits 2 on a usage error, which includes a `--landlock-rw` that is `/` or contains a system directory, and a relative `--landlock-ro`/`--landlock-rw`. It exits 1 on a startup or apply failure, or at `--ttl`.
- **The ruleset (`sandbox/internal/confine/rules.go`).** It grants:
  - every right beneath the workspace, `HOME`, the temporary directory (`TMPDIR`, else `TEMP`, `TMP`, `/tmp`, through `agentenv.TempDir`) and each `--landlock-rw`;
  - read and execute beneath `/usr`, `/bin`, `/sbin`, `/lib`, `/lib32`, `/lib64`, `/libx32`, `/etc`, `/opt` and each `--landlock-ro`;
  - read beneath `/proc`;
  - five device files.

  Flag roots get directory rights, so a `--landlock-ro` must name a directory. `Plan` caps the applied ABI at 4.
- **`attach.Dial(ctx, net.Conn, Config{ID, Spec, Destroy, Metadata, Provider})` (`sandbox/internal/attach/attach.go`)** behaves as follows:
  - it runs `Hello`, refuses a protocol major mismatch, and refuses `RequireLandlock` when the report does not satisfy the network mode;
  - it starts a host-side TTL timer that tears the sandbox down;
  - it marks the sandbox unavailable when the connection ends;
  - it runs `Destroy` once, with a fresh 30-second context, on any failure and on `Close`;
  - it sets `Spec().Workdir` to the root the agent reported.
- **`stdioconn.New(io.ReadCloser, io.WriteCloser) net.Conn`** adapts a child's pipes. `agent.New(agent.Config{Workdir, Env, TTL, Landlock})` serves in-process, and its `Landlock` field is data.
- **`local` (`sandbox/local/`)** shows the directory shape this provider extends:
  - `<os.TempDir()>/agentkit-sandbox-local/<16 hex>/` holding `workspace/`, `home/` and `tmp/`, each 0700;
  - a JSON marker `agentkit-sandbox.json` with the creating pid;
  - a `Reap` that probes liveness with signal 0 and cannot tell a reused pid.
- **Policy (`internal/policy/landlock_policy_test.go`, TS-15-39).** Only Linux-only files in `sandbox/internal/confine` may import go-landlock, and only `agentkit-sandboxd` may import `confine`. The host-side probe therefore cannot reuse `confine`. The existing Linux tests probe with the standard library's `syscall.Syscall(444, 0, 0, 1)` (`sandbox/cmd/agentkit-sandboxd/landlock_linux_test.go`, `sandbox/internal/confine/reexec_linux_test.go`).
- **Spec 17 (`sandbox_podman`, written, not yet in the tree)** adds:
  - `sandbox.GitRepo{URL, Ref, Depth}`;
  - `sandbox/internal/gitclone` with `Check` and `Args`, documented as shared with this provider;
  - `attach.Live`;
  - `attach`'s refusal of `GitRepo`;
  - the `GitRepo` slot rule: a `GitRepo` is accepted only as the first `Sync` and before `Tools`, and it runs as an init step beside an agent that is running but idle.
- **Spec 16** assigned "getting the agent binary onto a host … `go install`, not an image" to this scope.

**What the root module offers.**
- **`tools.Run` and `RunArgv` (`tools/exec.go`)** put every command in its own process group. A background job with redirected output outlives the call (`execute`'s description says so). Killing the agent's process group therefore does not reach a tool's leftovers.
- **`run_command` (`tools/tools.go`)** takes `{"argv": [...]}` and runs it in the workspace root with no shell. On a non-zero exit it returns `Data{output, exit_code, outcome}`. When the program cannot start, it returns `exec_failed`.
- **`mcp/proc_unix.go`** shows the process-group pattern for a long-lived child.

**What Landlock at ABI 4 does not reach.** It does not reach UDP, Unix sockets (pathname or abstract), Multipath TCP, signals, `chmod`, `stat`, or file descriptors already open. Its ptrace scoping keeps processes outside the domain, such as the embedder, unreadable through `/proc`.

## Requirements

### 1. Package `sandbox/landlock`: `Options`, `New` and `ErrNoLandlock`

```go
package landlock

type Options struct {
    // Root holds one directory per sandbox. Empty means
    // <os.UserCacheDir()>/agentkit-sandbox-landlock.
    Root string
    // AgentBinary is the agentkit-sandboxd to run. Empty means the file
    // agentkit-sandboxd beside os.Executable(), then the one on PATH.
    AgentBinary string
    // ExtraRO are absolute directories that the agent, and every process a
    // tool starts, may read and execute beneath (--landlock-ro).
    ExtraRO []string
    // ExtraRW are absolute directories they may also write beneath
    // (--landlock-rw).
    ExtraRW []string
}

// ErrNoLandlock: this host cannot confine a sandbox with Landlock ABI 4.
var ErrNoLandlock = errors.New("landlock: this host cannot confine a sandbox with Landlock ABI 4")

func New(o Options) (sandbox.Provider, error)
```

`New` performs these steps in order. Apart from step 5, it runs nothing.

1. **Platform.** On any `GOOS` other than `linux`, it returns an error wrapping `ErrNoLandlock` that names the `GOOS` and `sandbox/podman` as the alternative.
2. **Kernel.** It probes the kernel once through an unexported seam. The default probe is the Landlock version query `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)`, issued as `syscall.Syscall(444, 0, 0, 1)`. Each failure wraps `ErrNoLandlock`:
   - **A probe error** names the errno and a hint: `ENOSYS` means the kernel has no Landlock; `EOPNOTSUPP` means Landlock is not enabled in `lsm=`; `EPERM` means the call is blocked, typically by a container's seccomp filter.
   - **An ABI below 4** names the ABI found, ABI 4 and Linux 6.7, and `sandbox/podman`.
3. **Root.** It takes the default when empty and makes the path absolute. It creates the directory with mode 0700 when missing, and resolves symlinks once. The result must be a directory.
4. **Extra paths.** Each `ExtraRO` and `ExtraRW` entry must be absolute and must not be `/`. Resolved where it exists and cleaned otherwise, it must not equal `Root`, contain it, or lie beneath it. A refusal names `Options.ExtraRO[i]` or `Options.ExtraRW[i]`.
5. **Agent binary.**
   - **Resolution.** It uses `AgentBinary`, made absolute, when set. Otherwise it tries `agentkit-sandboxd` beside `os.Executable()` (symlinks resolved), then `exec.LookPath("agentkit-sandboxd")` on the host's `PATH`. The result must be a regular, executable file.
   - **Not found.** The error lists every path tried and the install command. That command is `go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@<buildver.Version()>`, or `cd sandbox && go install ./cmd/agentkit-sandboxd` from a checkout when the version is `(devel)`.
   - **Version check.** It runs `<binary> --version` once, with a 10-second timeout and an empty environment, and parses `agentkit-sandboxd <version> (protocol M.N)`. `M` must equal `sandbox.ProtocolMajor` and `N` must be at least 2, because older agents do not report Landlock. Otherwise the error quotes the output and the install command.

The returned provider is safe for concurrent use, and its sandboxes are independent.

### 2. Capabilities and spec checks

`Capabilities()` returns:
- `Provider`: `"landlock"`;
- `Isolation`: `IsolationConfinement`;
- `Network`: `[NetworkNone, NetworkUnrestricted]`;
- `Resources`: all false;
- `Image` and `Workdir`: false;
- `Landlock`: true;
- `Notes`, which say plainly that:
  - this is a confinement, not an isolation: the host kernel, memory, CPU and process table are shared, and there are no resource limits;
  - `NetworkNone` denies TCP only, while UDP and Multipath TCP are not restricted;
  - a process inside can connect to any Unix socket the embedder's user can reach, such as a D-Bus session bus, an ssh-agent, or a podman or docker socket, and through those act outside the confinement;
  - a process inside can signal the user's processes;
  - a process that starts a new session can outlive `Close`;
  - tool results name host paths.

`Create(ctx, spec)` first calls `sandbox.CheckSpec(p.Capabilities(), spec)`. That call refuses every `Resources` field, `NetworkAllowlist`, `Image` and `Workdir`, and fills in the TTL. The provider then refuses, before any directory or process exists:
- any `Spec.Provider` key, with a `*sandbox.SpecError{Provider: "landlock", Field: "Provider"}` that names the first key in sorted order;
- a `Spec.Env` that sets `HOME` or `TMPDIR`, with a `*sandbox.SpecError{Field: "Env"}` whose reason names the variable and says the provider sets it to the sandbox's private directory.

A spec with `RequireLandlock` passes `CheckSpec`, and the provider's own floor (Requirement 4) always satisfies it. `Labels` are recorded in the marker only.

### 3. The sandbox directory and its marker

**Creation.** For each sandbox, `Create` takes 16 random lowercase hex characters as the id and creates `<Root>/<id>/` with mode 0700. It immediately writes the marker `agentkit-sandbox.json` (mode 0600). It then creates `workspace/`, `home/` and `tmp/`, each set to 0700 whatever the umask.

**The marker.** It records:
- the owning process: its pid, its start time (`/proc/<pid>/stat` field 22) and the boot id (`/proc/sys/kernel/random/boot_id`);
- the owner's role, `creator` or `agent`;
- the creation time, the TTL, the network mode and the labels.

It never records `Spec.Env`. It first names the creating process. Once the agent has started, it is replaced atomically (written to a temporary file, then renamed) so that it names the agent.

**Visibility and cleanup.** Neither the marker nor `<Root>/<id>/` itself lies inside the ruleset, so nothing inside the sandbox can read the marker. Every `Create` failure removes the directory before returning.

### 4. Starting the agent and checking what it applied

**Arguments.** The agent is started from the resolved binary with these arguments, in order:
1. `sandbox.AgentArgs` of the effective spec, with `Workdir` set to `<Root>/<id>/workspace` (giving `--ttl`, `--network`, `--workdir`);
2. `--landlock-ro P` for each `ExtraRO`, in order;
3. the resolver directory, when one applies;
4. `--landlock-rw P` for each `ExtraRW`, in order.

**The resolver directory.** It is added only under `NetworkUnrestricted`. It applies when `/etc/resolv.conf` resolves to a file whose directory lies beneath `/run` or `/var/run` (but is not `/run` itself), such as systemd-resolved's `/run/systemd/resolve`. That directory is then added as a `--landlock-ro`. The function that computes it takes the path to read, so tests can inject one.

**Environment.** It is exactly `Spec.Env`, plus `HOME=<dir>/home` and `TMPDIR=<dir>/tmp`, plus `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin` when `Spec.Env` sets no `PATH`. It is passed as sorted `NAME=value` strings. Nothing comes from the embedder's environment. The argument list and the environment are pure, unexported functions.

**The process.**
- Its working directory is the workspace.
- It starts a new session (`Setsid`), so it leads its own session and process group.
- Its stdin and stdout are pipes, adapted with `stdioconn.New`.
- Its stderr is drained into a tail buffer of the last 8 KiB.
- It is not tied to `Create`'s context.
- A goroutine waits on it and records its exit status.
- Starting it is an unexported seam. Offline tests substitute an in-process `agent.New` served over pipes, whose `Landlock` report the test sets.

**Attaching.** The provider calls `attach.Dial(ctx, conn, attach.Config{ID: id, Spec: eff, Destroy: <Requirement 5>, Provider: "landlock"})`. By the time `Dial` fails, it has already run the destroy step. The failure is classified as follows:
- `ErrProtocol` and `attach`'s `RequireLandlock` `*SpecError` pass through unchanged;
- a done `ctx` gives an error wrapping `ctx.Err()`;
- an agent that exited gives `landlock: agentkit-sandboxd exited with status N: <stderr tail>`. Status 2 is a refused flag value, such as an `ExtraRW` containing a system directory.

**Checking the report.** After `Hello`, whatever `RequireLandlock` says, the provider checks `Agent().Landlock`:
1. `ABI >= sandbox.LandlockTCPABI`;
2. under `NetworkNone`, `TCP` is true;
3. the resolved `workspace/`, `home/` and `tmp/` are all among `Writable`;
4. no other `Writable` or `ReadOnly` root equals or contains `Root`.

If check 1, 2 or 3 fails, the provider closes the sandbox (running the destroy step) and returns an error wrapping `ErrNoLandlock` that quotes `ABI`, `KernelABI`, `TCP` and `Note`. If check 4 fails, it closes the sandbox and returns an error naming `Options.Root` and the root that contains it, telling the caller to choose a `Root` outside the system directories, because otherwise one sandbox could read another.

On success, `Create` returns the provider's wrapper (Requirement 6). Its `Spec().Workdir` is the host path of the workspace.

### 5. The destroy step, `Close` and the TTL

The destroy step is passed as `attach.Config.Destroy`. It runs once: on `Close`, at the host-side TTL, and on every `Create` failure after the agent has started. It performs these actions in order. Each action runs even when an earlier one failed, and the errors are joined.

1. **Stop the agent.** By now `attach` has closed the connection, and the agent exits 0 on EOF. Wait up to 5 seconds for it to exit, then SIGKILL its process group.
2. **Kill the session.** Scan `/proc` for processes whose session id (`/proc/<pid>/stat` field 6) is the agent's pid, ignoring zombies, and SIGKILL each one. Repeat until a scan finds none, for up to 5 seconds. This reaches what a tool left running in its own process group. The process table is an unexported seam.
3. **Remove the sandbox directory.** Never follow a symlink. Restore owner read, write and execute on each directory before removing its entries, so that read-only trees do not stop the removal; examples are a Go module cache under `home/` and a `chmod -R a-w` by the model. A missing directory is not an error.

The whole step fits in `attach`'s 30-second destroy budget. `Close` is idempotent, through `attach`.

The TTL is enforced on both sides:
- `attach`'s host timer runs the destroy step at the TTL;
- the agent's own `--ttl` ends it even when the host is gone.

When the host process dies, the agent's stdin closes and the agent exits. The directory, and any process that outlived the agent, stay until `Reap`.

### 6. `Sync`, `Tools` and the `GitRepo` slot

The provider's sandbox wraps `attach`'s sandbox:
- `ID`, `Spec`, `Agent`, `Export` and `Close` are forwarded;
- `Tools` and every non-`GitRepo` `Sync` are forwarded under 17's slot rule.

**The slot rule.**
- A `GitRepo` is accepted only while the slot is free.
- These take the slot: a successful `GitRepo`, any other successful `Sync`, and the first `Tools` call whatever its outcome.
- A failed `GitRepo` does not take it.
- `Tools` and `Sync` are serialised against an in-flight `GitRepo` sync. The `GitRepo` holds the write lock; the others hold the read lock.
- A `GitRepo` while the slot is taken is refused with `landlock: GitRepo must be the first Sync and come before Tools`.

### 7. `GitRepo`: the init child

**Preconditions.** These are checked in order, before any process starts, while holding the write lock:
1. `attach.Live` passes;
2. `gitclone.Check(repo)` passes;
3. the slot is free;
4. `Agent().Git` is true. Otherwise the error is `landlock: git is not on the sandbox's PATH (<PATH>); a GitRepo needs it`.

**The init agent.** The provider starts a second agent from the same binary, as in Requirement 4, with these differences:
- **Arguments:**
  - `--ttl` is the time left until the sandbox's expiry, rounded up to a second;
  - `--network unrestricted`;
  - `--workdir` is the same workspace;
  - the same `--landlock-ro` and `--landlock-rw` flags, plus the resolver directory.
- **Environment:** exactly `GIT_CONFIG_NOSYSTEM=1`, `GIT_TERMINAL_PROMPT=0`, the sandbox's `HOME` and `TMPDIR`, and the `PATH` the sandbox's agent got. No `Spec.Env` value is included.
- **Process:** it runs in its own session.
- **Attaching:** `attach.Dial` is called with `Spec{Network: NetworkUnrestricted, TTL: <that>}`, `Provider: "landlock"`, and a destroy step consisting of Requirement 5's actions 1 and 2 for the init agent only. This step does not remove the directory.

**Checking and running.**
1. Its report must show `ABI >= 4` and the workspace among `Writable`. Otherwise the error wraps `ErrNoLandlock`.
2. The provider calls `Tools(ctx, sandbox.ToolOptions{DisableSpill: true})` on the init sandbox.
3. It calls the `run_command` proxy with argv `git` followed by `gitclone.Args(repo, <workspace>)`. The call's context is the `Sync` context, bounded by the sandbox's expiry.
4. It always closes the init sandbox before returning.

The init command is an unexported seam.

**Outcomes.**
- **OK:** the slot is taken, and `Sync` returns a zero `SyncResult`.
- **`exec_failed`, or exit status 126 or 127:** an error saying git could not be run under the sandbox's ruleset. It says to install git beneath a system directory, or to add git's directory to `Options.ExtraRO`, and it carries the output tail.
- **Any other non-zero exit:** an error naming the URL and carrying the last 2 KiB of output.
- **A done context:** an error wrapping `ctx.Err()`. Reaching the expiry gives an error wrapping `sandbox.ErrUnavailable`.

On every failure, once the init agent is gone, the provider removes the workspace's contents (keeping the directory itself) using Requirement 5's removal. A retry then starts from an empty directory, and the slot stays free. The sandbox's own agent runs throughout, idle and under its own stricter ruleset. None of the init child's network reaches it.

### 8. `Reap`

`Reap(ctx)` lists `Root`'s direct entries. It considers only directories (never symlinks) whose name is 16 lowercase hex characters and whose marker can be read. It leaves every other entry alone.

**The owner check.** A marker's owner counts as alive when all of these hold:
- `/proc/<pid>` exists;
- the process's start time equals the recorded one;
- the current boot id equals the recorded one.

**Live owners.** A sandbox whose owner is alive is skipped.

**Dead owners.** For any other sandbox, `Reap` does the following:
1. If all of these hold, it SIGKILLs every process in the agent's session, as action 2 of the destroy step does:
   - the marker names the agent;
   - the boot id matches;
   - no process with the agent's pid exists.
2. It removes the directory, as action 3 of the destroy step does.

**Result.** It returns the removed ids, sorted. When some removals fail, it returns the ids it did remove together with the joined errors.

The decision is a pure function of the marker and a process lookup, and is tested on every platform. `Reap` never removes a sandbox whose agent is alive, even one another process created.

### 9. The host stays unconfined; tests

**What the package may do.** `sandbox/landlock` imports neither go-landlock nor `sandbox/internal/confine`, which TS-15-39 already enforces. It calls no Landlock syscall other than the version query, and applies nothing to its own process. A policy test pins that `go list -deps` of `./landlock` in the sandbox module includes neither package. The sandbox module gains no Go dependency.

**Offline tests, on every platform, in `make check`.** Through the seams (probe, starter, process table, init command, resolver path) they cover:
- `Capabilities`;
- every refusal in Requirements 1 and 2, each leaving no directory;
- `New`'s probe outcomes for ABI 0–3 and for each errno;
- the directory layout and modes;
- the marker's contents and its atomic replacement;
- the agent's argument list and environment, including the resolver rule;
- each failing check of Requirement 4, which runs the destroy step exactly once;
- the slot rule and the emptying after a failed init;
- `Reap`'s decision table.

**Linux tests with Landlock ABI 4, in `make check`.** They skip, naming the ABI found, below ABI 4. `TestMain` builds `agentkit-sandboxd` with `CGO_ENABLED=0`. The tests cover:
- Goals 1, 2 and 4;
- the offline half of Goal 5;
- Goal 6's re-executed child;
- the destroy step and `Reap` against real processes.

**The opt-in network test.** The public clone of Goal 5 runs only when `AGENTKIT_SANDBOX_GIT_URL` is set. A new `make sandbox-landlock-test` target runs `go test -count=1 -v ./landlock/` in `sandbox/`, with that variable defaulting to `https://github.com/agent-fox-dev/agentkit-go`. That target is not part of `make check`.

### 10. Documentation and example

Per steering, run `make doc` first, then update:
- **`docs/configuration.md`:**
  - a `landlock.New` section covering:
    - the `Options` table;
    - `New`'s checks and errors, and `ErrNoLandlock`;
    - installing the agent with `go install`;
    - the environment rule;
    - the directory layout and the marker;
    - the resolver-directory rule;
    - `Reap`'s rule;
  - a `landlock` column in "Which provider honours which field".
- **`docs/architecture.md`:** a "The Landlock provider" part of the Sandboxes section. It covers:
  - the `Create` sequence;
  - the agent's session, and why a process group is not enough;
  - the init child beside the idle agent;
  - the two-sided TTL;
  - `Reap`;
  - what the provider gives up: Unix sockets, including D-Bus, ssh-agent and engine sockets; UDP; Multipath TCP; signals; host paths in results; and file descriptors the embedder opened without close-on-exec.
- **`README.md`:** the Sandboxes section names `sandbox/landlock` and its install line, and the packages table gains it.
- **`examples/sandbox`:** a `--landlock` flag, with `--agent PATH`, runs the same program on this provider under `NetworkNone`. The default offline run and its `main_test.go` are unchanged.

## Design Decisions

1. **The package builds on every target, works only on Linux, and `New` names `podman` elsewhere.** This follows the steering rule (Linux and macOS must build) and the cross-target gate. On macOS, Podman's VM is the boundary (PRD 11).
2. **The ABI 4 floor is enforced twice: by the probe in `New`, and on the agent's report after `Hello`, whatever `RequireLandlock` says.** Landlock is the only boundary here. `Satisfies` only asks for ABI 2, and a seccomp filter or a stale agent can withhold what the probe saw.
3. **The host-side probe is the raw version query (syscall 444), not `confine` or go-landlock.** TS-15-39 forbids both outside the agent, the query confines nothing, and the repository's Linux tests already use it.
4. **`ErrNoLandlock` covers the platform, kernel and report failures. A missing agent binary or a bad `Root` gives a plain error.** An embedder that falls back to `podman` needs one sentinel that means "this host cannot confine", and a misconfiguration is not that.
5. **`New` resolves the agent binary (`AgentBinary`, then beside the executable, then `PATH`) and checks `--version` for protocol `1.≥2`.** Failing in `New` rather than at the first `Create` is where an embedder decides on a fallback. Tool-definition drift is still caught by the `Tools` digest check, exactly as for images.
6. **The environment is `Spec.Env` plus `HOME`, `TMPDIR` and a default `PATH` only, with no `LANG` or `TERM`.** PRD 11 §3.6 and its acceptance criterion say exactly that for this provider.
7. **`HOME` or `TMPDIR` in `Spec.Env` is refused, not overridden.** Those two variables feed the ruleset's writable roots, so honouring them would widen the boundary. Silently replacing them would break goal 4's "no silent change".
8. **The default `Root` is `<UserCacheDir>/agentkit-sandbox-landlock` with mode 0700, and containment of `Root` is checked against the agent's reported roots.** A directory under the user's home is outside every ruleset. Checking the real report avoids duplicating `confine`'s unexported system-directory list.
9. **`ExtraRO` and `ExtraRW` may not overlap `Root`.** An overlap would let one sandbox read or write another's workspace and marker.
10. **The marker names its owner by pid, start time and boot id, first the creator and then the agent, and omits `Spec.Env`.** Signal 0, as `local` uses it, cannot tell a reused pid. The creator phase covers a crash before the agent starts, and environment values do not belong on disk.
11. **The agent runs in its own session, and `Close` and `Reap` kill the whole session.** `tools.Run` gives every command its own process group, so killing the agent's group misses a background job that the PRD requires `Close` to kill. A process that calls `setsid` escapes this, which is documented; cgroups need delegation that cannot be assumed.
12. **Removal restores directory write permission and never follows a symlink.** Go's module cache and the model's own `chmod` make read-only trees that `os.RemoveAll` cannot delete. Landlock does not restrict `chmod`.
13. **`GitRepo` is a `Sync` on a live sandbox, run beside the idle agent, rather than before the agent starts.** This follows 17 DD 1, so both providers give `Sandbox` the same semantics. The idle agent has run nothing, and its own ruleset denies TCP.
14. **The init child is a second `agentkit-sandboxd`, started with `--network unrestricted`, which runs git through its `run_command` tool.** Only the agent may apply Landlock, and this needs no new agent mode. The init child's ruleset is reported in `Hello` and checked, and cancellation and output capture come from the existing pipeline.
15. **The init environment is `GIT_CONFIG_NOSYSTEM=1`, `GIT_TERMINAL_PROMPT=0`, the private `HOME` and `TMPDIR`, and the agent's `PATH`, with no `Spec.Env`.** The host's system git config can carry credential helpers and URL rewrites, and the clone must involve no credential. The same `PATH` makes `Agent().Git` a true precondition.
16. **A failed init empties the workspace.** `git clone` needs an empty directory. The slot rule guarantees nothing successful was synced, so there is nothing to lose.
17. **Under `NetworkUnrestricted`, the directory of the resolved `/etc/resolv.conf` is readable when it lies beneath `/run` or `/var/run`.** On systemd-resolved hosts, the default on Ubuntu, `/etc/resolv.conf` is a symlink into `/run`, and without this rule neither DNS nor the clone works. A flag root must be a directory, and limiting the rule this way never grants `/run` itself.
18. **The slot rule is reimplemented in this package with 17's wording, not shared.** The two wrappers differ in how the clone runs, and sharing would change 17's package and tests.
19. **Unix-socket, UDP and Multipath TCP reach is documented in `Capabilities.Notes` and the docs, not mitigated.** At the agent's ABI cap of 4, Landlock cannot express any of them, and raising that cap is 15's decision. The honest statement is that this provider protects files and TCP, and that `podman` is the provider for untrusted code.
20. **The Linux tests run in `make check` when the kernel offers ABI 4, and only the network clone is opt-in.** PRD 11 goal 7 asks for kernel-gated tests, and `make check` must stay offline.
21. **The provider honours neither `Image` nor `Workdir` nor any `Resources` field, and reports `IsolationConfinement`.** There is no image, the workspace is a host directory, and Landlock bounds no resource. `IsolationConfinement` also keeps 17's `AllowAll` guidance from applying to this provider.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | depends on | Builds on `Provider`, `Sandbox`, `Spec`, `Capabilities` (with `IsolationConfinement`), `CheckSpec`, `SpecError`, `attach.Dial` (with its destroy-on-failure and host-side TTL timer), `stdioconn`, `agent.New` for the offline starter seam, `run_command` over proxy tools, and `local`'s directory and marker pattern. |
| `14_sandbox_rsync_sync` | depends on | `LocalDir` `Sync` and `Export` run unchanged over the child's stdio, under the agent's ruleset, and the Linux tests rely on them. |
| `15_sandbox_landlock_agent` | depends on | Uses the agent's ruleset; `--network`, `--landlock-ro` and `--landlock-rw`; `Hello`'s `LandlockInfo`; `Capabilities.Landlock` and `RequireLandlock`; `AgentArgs`; and the ABI cap. 15 assigned `ExtraRO`/`ExtraRW`, the ABI 4 floor and host-specific defaults such as `/run/systemd/resolve` to this scope. TS-15-39 constrains the provider's imports. |
| `16_sandbox_images` | depends on | Relies on `buildver.Version()` and the agent's `--version` format. 16 assigned getting the agent binary onto a host (`go install`) to this scope. |
| `17_sandbox_podman` | depends on | Uses `sandbox.GitRepo`, `gitclone.Check` and `gitclone.Args`, `attach.Live`, `attach`'s `GitRepo` refusal, the `GitRepo` slot rule it mirrors, and the `guard` guidance that already excludes `IsolationConfinement`. |

## Verified External API

**In-repository code (read):**
- `sandbox.CheckSpec(caps Capabilities, spec Spec) (Spec, error)`.
- `sandbox.SpecError{Provider, Field, Reason string}`. Its `Is` matches `ErrUnsupported`, and it prints without a value when built outside `CheckSpec`.
- `sandbox.AgentArgs(spec Spec) []string`, which returns `--ttl`, `--network`, and `--workdir` only when set.
- `sandbox.Capabilities{Provider string; Isolation Isolation; Network []NetworkMode; Resources ResourceCaps; Image, Workdir bool; Notes string; Landlock bool}` and `sandbox.IsolationConfinement`.
- `sandbox.LandlockInfo{ABI, KernelABI int; TCP bool; Note string; Writable, ReadOnly []string}`, `sandbox.LandlockTCPABI = 4`, and `(LandlockInfo).Satisfies(NetworkMode) bool`.
- `sandbox.ProtocolMajor = 1`, `sandbox.ProtocolMinor = 2`, and `sandbox.ErrProtocol`, `ErrUnavailable` and `ErrUnsupported`.
- `attach.Dial(ctx context.Context, conn net.Conn, cfg attach.Config) (sandbox.Sandbox, error)` with `attach.Config{ID string; Spec sandbox.Spec; Destroy func(context.Context) error; Metadata metadata.MD; Provider string}`. `Destroy` runs once, with a fresh 30-second context, on any failure inside `Dial` and on `Close`.
- `stdioconn.New(r io.ReadCloser, w io.WriteCloser) net.Conn`.
- `agent.New(agent.Config{Workdir string; Env []string; TTL time.Duration; Landlock sandbox.LandlockInfo}) (*agent.Agent, error)` and `(*agent.Agent).Serve(net.Conn) error`. `Hello.Git` comes from a `PATH` lookup in the agent's explicit environment.
- `buildver.Version() string`.
- `agentkit-sandboxd`:
  - flags `--workdir` (default `/workspace`), `--ttl` (positive, default 1h), `--network none|allowlist|unrestricted` (default `none`), and `--landlock-ro`/`--landlock-rw` (repeatable, absolute, directories);
  - `--version`, which prints `agentkit-sandboxd <version> (protocol M.N)`;
  - exit status 0 on host close, 2 on a usage error, and 1 on a startup or apply failure or at the TTL.
- `run_command`: input `{"argv": [string...], "timeout_s"?: int}`, run with no shell in the workspace root.
  - On a completed run, `Data` is `{output, exit_code, outcome}` and `Error` is `command_<outcome>` when not OK.
  - A program that cannot start gives `exec_failed`.

**From `17_sandbox_podman`** (specified, not yet in the tree):
- `sandbox.GitRepo{URL, Ref string; Depth int}`, a `Source`;
- `gitclone.Check(repo sandbox.GitRepo) error`;
- `gitclone.Args(repo sandbox.GitRepo, dir string) []string`, the arguments after `git`;
- `attach.Live(s sandbox.Sandbox) error`.

**Standard library (Go 1.27).** Unverified: the source is outside the repository. The existing Linux tests already use the first item.
- `syscall.Syscall(444, 0, 0, 1)` returns the Landlock ABI or an errno.
- `syscall.SysProcAttr{Setsid: true}`.
- `syscall.Kill(pid int, sig syscall.Signal) error`.
- `os.Executable`, `os.UserCacheDir`, `exec.LookPath` and `filepath.EvalSymlinks`.

**Linux kernel interfaces.** Unverified:
- In `/proc/<pid>/stat`, field 3 is the state (`Z` for a zombie), field 6 the session id, and field 22 the start time in clock ticks, all parsed after the last `)`.
- `/proc/sys/kernel/random/boot_id`.
- A pid number stays allocated while it is in use as a session id.
- Landlock ABI 4 arrived in Linux 6.7.

**git.** Unverified: `GIT_CONFIG_NOSYSTEM=1` skips the system configuration, and `GIT_TERMINAL_PROMPT=0` makes an authentication request fail instead of prompting.
