---
spec_id: "15"
spec_name: "sandbox_landlock_agent"
title: "Sandbox agent self-confinement with Landlock"
status: "active"
created_at: "2026-10-12T14:27:15.108012Z"
updated_at: "2026-10-12T14:27:15.108012Z"
intent_hash: "23392eb18106e15ff181bfa06f0e47254841a744619d17b1a6af04d57f6813ae"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Make the sandbox agent, `agentkit-sandboxd`, confine itself with Linux Landlock before it does any other work. Every tool call, and every process a tool starts, can then write only to the workspace and the agent's own private directories, can read only the system directories, and, when the sandbox has no network, can neither bind nor connect TCP. This is a second boundary underneath whatever boundary the provider supplies, never a replacement for it. The agent reports exactly what it applied, so an embedder can see it and can make a missing ruleset a creation failure. No host-side code path may ever confine the process that embeds AgentKit.

## Goals

1. **The ruleset works on Linux with Landlock ABI 4 or later.** Start `agentkit-sandboxd` with `--network none`, a workspace W, `HOME=H` and `TMPDIR=T`, attach to it, and call proxy `execute`. Then:
   - creating a file under W, H and T succeeds;
   - creating a file in a sibling directory outside all three fails with a permission error;
   - `cat` of a file outside the ruleset fails with a permission error;
   - a TCP connect to a listener the test opened on `127.0.0.1` fails;
   - `Sandbox.Agent().Landlock` reports `ABI == 4`, `TCP == true`, and W, H and T among `Writable`.

   With `--network unrestricted`, the same connect succeeds and `TCP` is false. The test skips, naming the ABI it found, when the kernel offers less than ABI 4. The filesystem half still runs at ABI 2 or 3.
2. **One pure decision function covers every case.** For every kernel ABI from 0 to 8, a probe error, and each of the three network modes, it returns the ABI to apply, the TCP flag and the note from the table in Requirement 2. The test runs on every platform.
3. **`local` stays unconfined and says so.** On a `local` sandbox, `Agent().Landlock.ABI` is 0 and carries a note. `Create` with `RequireLandlock` is refused with an error that names `RequireLandlock` and matches `ErrUnsupported`, and no sandbox directory is created.
4. **The host checks `RequireLandlock` against what the agent reports.** Over 13's `fakeagent`, a `Spec` with `RequireLandlock`:
   - is refused when the agent reports ABI 0, and the provider's destroy step runs exactly once;
   - is refused when the agent reports ABI 3 with `TCP` false under `NetworkNone`, and the destroy step runs exactly once;
   - is accepted when the agent reports ABI 3 under `NetworkUnrestricted`;
   - is accepted when the agent reports ABI 4 with `TCP` true under `NetworkNone`;
   - is refused, with an error naming the agent's protocol version, when the agent speaks protocol 1.1.
5. **Nothing on the host can confine itself.** Structural tests show that:
   - only `sandbox/cmd/agentkit-sandboxd` imports the confinement package;
   - only Linux-constrained files import `go-landlock`;
   - no non-test file in the root module calls `BestEffort`;
   - in the command's start-up path, confinement comes before `agent.New`.

   A re-executed test child runs a `local` sandbox through `Create`, `Sync`, `Tools`, `execute` and `Close`. Afterwards it can still create a file outside the workspace and connect to a loopback listener.
6. **The dependency change is exactly one promotion.** The root module's direct requirements gain exactly `github.com/landlock-lsm/go-landlock`, at the version `go.mod` already carries. The output of `go list -m all` is unchanged. The module is approved in `approvedDeps` and ruled in `docs/DEPS.md`. The cross-target gate, the host-cgo gate and TS-12-14 all pass.
7. **Public surfaces stay put.** The exported surfaces of `agentkit`, `core`, `tools` and `codemode` are unchanged. Only the doc comment of `tools.Workspace` changes.

## Non-goals

- **The other scopes of this split.** Each is its own spec:
  - `sandbox_images`: the published images, including the cgo base-image build that links psx's cgo variant.
  - `sandbox_podman`: passing `sandbox.AgentArgs` to the container, the seccomp profile's handling of the Landlock syscalls, the `--security-opt no-new-privileges` flag, and the `guard` documentation on when `AllowAll` is reasonable.
  - `sandbox_landlock_provider`:
    - the confined child-process provider;
    - its own floor of ABI 4, whatever `RequireLandlock` says;
    - `landlock.Options.ExtraRO` and `ExtraRW`, which it maps onto this spec's `--landlock-ro` and `--landlock-rw` flags;
    - host-specific defaults such as `/run/systemd/resolve`.
  - `sandbox_gateway`: relaying `Hello` and `Capabilities` through a gateway.
- **Bounding memory, CPU or process count.** Landlock cannot do it. That stays the container's job.
- **Landlock features beyond ABI 4**: ioctl on devices (ABI 5), scoping of abstract Unix sockets and signals (ABI 6), audit logging (ABI 7), thread synchronisation (ABI 8), and UDP or Unix-socket rules.
- **Restricting Multipath TCP.**
- **Host allowlists.** Landlock rules are by port, not by host. `NetworkAllowlist` gets no TCP rule from the agent.
- **A command-line switch that turns confinement off.** The unconfined path is `local`, and only `local`.
- **Making `RequireLandlock` default to true.** PRD 11 defers that until the published images' kernels are known to carry Landlock.
- **Confining anything on the host.** That covers the embedder, the `local` provider's in-process agent, `attach`, and the gateway client.
- **Landlock on macOS or Windows.** The agent builds there and reports ABI 0.

## Background

**What PRD 11 decided (D8).** The agent restricts itself with `github.com/landlock-lsm/go-landlock` before it serves its first RPC and before any cgo thread exists:

- read and write on the workspace, the spill directory and a private `HOME`;
- read and execute on `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc` and `/opt`;
- under `NetworkNone`, no TCP bind or connect (ABI 4, Linux 6.7);
- never `BestEffort()`, because a best-effort call succeeds silently on a kernel without Landlock;
- the applied ABI is reported in `Hello`;
- `Spec.RequireLandlock` makes a missing or lower ABI a `Create` failure, and is off by default;
- no host-side code path applies Landlock to its own process.

**What the code says today.**
- `tools/path.go`: the `Workspace` doc comment says "It does NOT constrain `execute`… The boundary for `execute` is the BeforeToolCall interceptor". `docs/configuration.md`'s `tools.Options` table says of `Workspace`: "File tools cannot leave it."
- `tools/exec.go`: a nil `ExecOptions.Stdin` "keeps the null device". `os/exec` therefore opens `/dev/null` for every `execute` and `run_command` child, so a ruleset that omits `/dev/null` breaks every shell tool.
- `ReducedEnv` keeps `PATH`, `HOME`, `LANG`, `TMPDIR` and `TERM`. The agent's tools see exactly the environment the provider gave the agent (13).
- `internal/policy/deps_test.go` holds `approvedDeps` (TS-09-16) and the TS-09-15 prune list. `internal/policy/crosstarget_test.go` builds and vets linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 with cgo off, and the host with cgo on. `core/size_test.go` (TS-12-14) caps the root module at 20,000 lines.
- `go.mod` does not mention `go-landlock` yet. `docs/DEPS.md` has one ruling per direct module and a "Rejected" table.

**What the earlier scopes deliver.** Both are written, not yet in the tree.
- `13_sandbox_core` provides:
  - `sandbox.Spec`, `Capabilities`, `CheckSpec` (the single place a spec is checked), `*SpecError` matching `ErrUnsupported`, and `AgentInfo` with its "Later scopes add the Landlock ABI there" (13 DD 20);
  - `sandbox/internal/agent` with `Config{Workdir, Env, TTL}`. Its default spill directory is `<TMPDIR of the explicit env, else TEMP, TMP, /tmp>/agentkit-spill`;
  - `sandbox/cmd/agentkit-sandboxd`, whose flags are `--workdir`, `--ttl` and `--version`. Usage errors exit 2, start-up failures exit 1;
  - `sandbox/internal/attach`, whose `New` runs `Hello`, refuses a different protocol major, and destroys the sandbox when the handshake fails;
  - `sandbox/internal/fakeagent`, and `local.Unisolated()`, which runs the agent in-process with no confinement.
- `14_sandbox_rsync_sync` provides:
  - the `Rsync` RPC, with `ProtocolMinor` set to 1;
  - `gokrazy/rsync` as a direct dependency. Its `rsyncd` package brings `go-landlock` and `kernel.org/pub/linux/libs/security/libcap/psx` into the graph as indirect requirements;
  - `rsyncd.DontRestrict()` on every server, pinned by an AST test and a re-executed child (TS-14-49);
  - TS-14-42, which asserts that `go-landlock` stays indirect, and TS-14-5, which pins minor 1.

**Landlock ABI history** (kernel documentation):

| ABI | Linux | Adds |
|---|---|---|
| 1 | 5.13 | filesystem rules |
| 2 | 5.19 | refer: renaming or linking across directories. Under ABI 1 this is always denied, so git cannot move objects from `.git/objects/` into their fan-out directory. |
| 3 | 6.2 | truncate |
| 4 | 6.7 | TCP bind and connect |
| 5 and later | | not used here |

Landlock does not restrict `stat`, `readlink`, path lookup, already-open file descriptors, or pipes and sockets reached through `/proc` magic links. A ruleset is inherited across `fork` and `exec`, and cannot be lifted.

## Requirements

### 1. The ruleset

When the agent applies a ruleset, it grants exactly the access below and nothing else on the filesystem.

**Writable roots.** Every filesystem right the applied ABI defines, including execute, refer (ABI 2 and later) and truncate (ABI 3 and later), beneath:
- the workspace (`--workdir`, symlinks resolved);
- `HOME` from the agent's environment;
- the agent's temporary directory, resolved exactly as 13's agent resolves its default spill location;
- each `--landlock-rw` path.

**Read and execute** beneath `/usr`, `/bin`, `/sbin`, `/lib`, `/lib32`, `/lib64`, `/libx32`, `/etc`, `/opt`, and each `--landlock-ro` path.

**Read only** beneath `/proc`.

**Read and write** on the device files `/dev/null`, `/dev/zero`, `/dev/full`, `/dev/random` and `/dev/urandom`.

**Network.** Under `--network none`, at an applied ABI of 4, the ruleset handles TCP bind and connect and grants no port. Every TCP bind and connect is therefore denied, loopback included. Under `allowlist` and `unrestricted`, it handles no network right.

**Path handling.**
- A system or device path that does not exist is skipped silently. A `--landlock-ro` or `--landlock-rw` path that does not exist is skipped, and the skip is noted.
- `HOME` or the temporary directory is skipped, with a note naming it and the reason, when it is:
  - unset or relative;
  - missing;
  - `/`;
  - equal to or an ancestor of a system directory.
- These are usage errors (exit 2) that name the flag:
  - `--workdir` or a `--landlock-rw` path that is `/`, or equal to or an ancestor of a system directory;
  - a relative `--landlock-ro` or `--landlock-rw` path.
- Every root is resolved through symlinks before its rule is added. Duplicates collapse.

`Writable` and `ReadOnly` in the report list the resolved paths in the order the rules were added. The device files are listed under `Writable`, and `/proc` under `ReadOnly`.

### 2. Choosing and applying the ABI

The agent probes the kernel's Landlock ABI once. A pure function, `Plan(kernelABI, probeErr, mode)`, then decides what to apply. It has no build constraint and is tested on every platform:

| Kernel | ABI applied | TCP | Note |
|---|---|---|---|
| probe error | 0 | no | `Landlock unavailable: <error>` |
| 0 | 0 | no | `kernel reports no Landlock support` |
| 1 | 0 | no | `kernel Landlock ABI 1 cannot permit cross-directory renames (needs ABI 2, Linux 5.19); not applied` |
| 2 or 3 | the kernel's | no | under `none`: `kernel Landlock ABI k cannot restrict TCP (needs ABI 4, Linux 6.7)` |
| 4 or later | 4 | only under `none` | empty |

Under `allowlist`, at any applied ABI above 0, the note says that Landlock cannot express a host allowlist and that TCP is left to the provider.

**Applying.**
- The agent applies a configuration of exactly the chosen ABI version. It never calls `BestEffort`.
- `no_new_privs` is set on all threads as part of applying.
- On a non-Linux build, the agent applies nothing and reports ABI 0 with the note `Landlock is Linux-only (GOOS=<os>)`.

**Failures.**
- A probe failure, such as `ENOSYS`, `EOPNOTSUPP` or a seccomp `EPERM`, is not fatal. The agent continues with ABI 0 and the note.
- A failure to build or apply a ruleset after a successful probe is fatal. The command writes one line, `agentkit-sandboxd: landlock: <cause>`, to stderr and exits 1. It never serves unconfined after a failed apply.

**Test seam.** The probe is replaceable in tests through an unexported seam. Kernel ABI 1 to 3 can therefore be simulated.

### 3. The command and its flags

`agentkit-sandboxd` gains these flags:
- `--network none|allowlist|unrestricted`, default `none`. Any other value is a usage error.
- `--landlock-ro PATH`, repeatable.
- `--landlock-rw PATH`, repeatable.

**Order of work in `main`.**
1. Parse the flags.
2. Handle `--version`, which prints `(protocol 1.2)` and applies nothing.
3. Compute and apply the ruleset.
4. Do anything else, in particular redirecting stdout, calling `agent.New`, starting goroutines of its own, and serving.

An AST test over the command's source pins that confinement precedes `agent.New`.

**Hand-off to the agent.** The command passes the outcome to the agent as data, through a new `agent.Config.Landlock` field of type `sandbox.LandlockInfo`. Every process a tool starts inherits the ruleset.

**`sandbox.AgentArgs`.** `sandbox.AgentArgs(spec Spec) []string` returns the agent's flags for an effective spec:
- `--ttl <spec.TTL>`;
- `--network <spec.Network.Mode>`;
- `--workdir <spec.Workdir>`, only when `Workdir` is set.

Later providers start the agent with it.

### 4. Reporting: `Hello`, `AgentInfo` and the protocol version

**The proto.** `HelloResponse` gains `LandlockReport landlock = 7`. The new message `LandlockReport` has these fields:

```protobuf
message LandlockReport {
  uint32 abi = 1;
  uint32 kernel_abi = 2;
  bool tcp = 3;
  string note = 4;
  repeated string writable = 5;
  repeated string read_only = 6;
}
```

**Version.** `ProtocolMinor` becomes 2. `make proto` regenerates the checked-in code and the hash file. TS-14-5 and every test that pinned minor 1 now pin 2, and TS-13-24 expects `1.2`.

**The host API.** Package `sandbox` adds:

```go
type LandlockInfo struct {
    ABI       int      // ABI of the ruleset applied; 0 = none
    KernelABI int      // what the kernel offered; 0 = none or unknown
    TCP       bool     // TCP bind and connect are denied
    Note      string   // why nothing, or only part, was applied
    Writable  []string
    ReadOnly  []string
}
func (l LandlockInfo) Satisfies(mode NetworkMode) bool
```

`Satisfies` is true when `ABI >= LandlockMinABI` (2), and also `TCP` when `mode` is `NetworkNone`. `LandlockTCPABI` (4) is exported beside it.

`AgentInfo` gains `Landlock LandlockInfo`, filled from `Hello`. An agent of minor 1 or lower sends no report. `attach` then fills ABI 0 and the note `agent protocol 1.N does not report Landlock`.

**The in-process agent.** The agent that `local` runs in-process reports ABI 0 with the note `in-process agent (local provider): no confinement`.

### 5. The agent under its own ruleset

Every agent RPC works when the ruleset is applied: `Hello`, `Tools`, `Call`, `SyncTar`, `ExportTar` and 14's `Rsync`.
- They serve over the already-open stdio descriptors.
- They write only under the workspace and the temporary directory.

When the applied ABI is above 0, a `Tools` request whose explicit `SpillDir` is not beneath a writable directory root is refused with `InvalidArgument`. The message names the path and the writable roots. The default spill directory always qualifies.

On Linux, integration tests build the command and start it over pipes, as 13's command tests do. They:
- check every outcome in Goal 1, skipping with the ABI named when the kernel offers too little;
- show that `env` inside reports the agent's explicit environment unchanged;
- show that a `go version` or `/bin/sh -c true` under a system directory runs;
- show that a tar sync, a `LocalDir` sync and an export succeed against the confined agent.

### 6. `Spec.RequireLandlock`

`Spec` gains `RequireLandlock bool`, false by default. `Capabilities` gains `Landlock bool`: the provider starts `agentkit-sandboxd` in a way that lets it apply its ruleset. `local` reports false.

**Checked before anything is allocated.** `CheckSpec` refuses a spec with `RequireLandlock` when `caps.Landlock` is false. The refusal is a `*SpecError` with `Field: "RequireLandlock"`, checked after the fields 13 checks. 13's property test TS-13-13 and its oracle cover the new field.

**Checked after `Hello`.** `attach.New` refuses the sandbox when the effective spec has `RequireLandlock` and `AgentInfo.Landlock.Satisfies(spec.Network.Mode)` is false. It then:
- closes the client;
- runs the provider's destroy step once;
- returns a nil `Sandbox` and a `*SpecError{Provider, Field: "RequireLandlock", Reason}`, matching `ErrUnsupported`. The reason quotes the agent's ABI, its kernel ABI, the TCP flag and the note.

`attach.Config` gains `Provider string` for that message.

### 7. The host is never confined

**Import rules for the root module's non-test code.** An AST test in `internal/policy` checks that:
- only files under `sandbox/internal/confine` import `github.com/landlock-lsm/go-landlock/...`, and each of those files is constrained to Linux;
- only `sandbox/cmd/agentkit-sandboxd` imports `sandbox/internal/confine`. In particular `sandbox/internal/agent`, `attach`, `local` and `sandbox` do not;
- no file contains a call to a method named `BestEffort`.

**Behaviour.** A test re-executes its own binary as a child, so that a regression confines only the child. The child:
1. creates a `local` sandbox;
2. syncs a tar;
3. gets the tools and runs `execute`;
4. closes the sandbox;
5. creates a file in a fresh directory outside the workspace;
6. connects to a TCP listener it opened on loopback.

Both of the last two must succeed.

### 8. Dependency, build constraint and gates

**`go.mod` and the allowlist.**
- `github.com/landlock-lsm/go-landlock` becomes a direct requirement, at the version already selected after 14. PRD 11 names v0.10.1.
- No module version changes, and the `go list -m all` snapshot is unchanged.
- psx stays indirect.
- `approvedDeps` gains the module, with a one-line reason.
- TS-14-42 and the direct-set literal of TS-13-65 now expect it as direct.

**Build constraint.** Every file that imports it carries `//go:build linux`. A `!linux` stub reports ABI 0.

**Gates.** The cross-target gate, the host-cgo gate (with no new environment variable) and TS-12-14 must pass.

**When the plan cannot go ahead,** do not work around it. Record the case in `docs/errata/sandbox_landlock_agent.md` and stop:
- the selected version lacks a symbol this spec assumes;
- psx's cgo variant does not link under the host-cgo gate without a new environment variable;
- the change would move a version.

### 9. Documentation

Per steering, run `make doc`, then update:
- **`tools.Workspace`'s doc comment.** On a host it is still true that `execute` is not contained. Inside a sandbox whose agent reports a Landlock ABI, the agent's kernel ruleset confines `execute` and every process it starts to the writable roots. Under `local`, or on a kernel without Landlock, it does not.
- **`docs/configuration.md`:**
  - the `Workspace` row, with the same qualification;
  - `Spec.RequireLandlock`, `Capabilities.Landlock`, `AgentInfo.Landlock` and `LandlockInfo.Satisfies`;
  - the ruleset;
  - the ABI table;
  - the provider table's `RequireLandlock` row.
- **`docs/architecture.md`:**
  - a "Kernel confinement inside the agent" part of the Sandboxes section;
  - the invariant "the agent's Landlock ruleset is applied before any other work", beside 14's invariant "no code in this module confines the process that embeds it";
  - what Landlock does not reach: resources, UDP and Unix sockets, Multipath TCP (which Go's `net.Listen` uses by default), and host allowlists;
  - the fact that loopback TCP is denied under `none`.
- **`docs/cli.md`:** the three new flags, and exit status 1 on an apply failure.
- **`README.md`:** one sentence in the Sandboxes section on the agent's own confinement and `RequireLandlock`.
- **`docs/DEPS.md`:** the `go-landlock` ruling. It covers:
  - who uses it, which is `confine` directly and `rsyncd` indirectly, always with `DontRestrict()`;
  - the version;
  - why it is used, namely all-thread application through psx in cgo and non-cgo builds;
  - what it pulls in, namely psx and `x/sys`;
  - why `BestEffort` is never used.

  It also adds a "Rejected" row for raw Landlock syscalls through `x/sys/unix`.

## Design Decisions

1. **The ruleset code lives in `sandbox/internal/confine`, imported only by the command.** The agent package receives the outcome as data. `local` runs the agent package in the embedder's process, so the package that applies Landlock must be one that `local` cannot reach. A single import test can then pin that.
2. **The floor is ABI 2.** Under ABI 1 every cross-directory rename or link is denied, which breaks git's object writes and other ordinary tools. Applying it would hand the model a sandbox that fails in confusing ways.
3. **Instead of `BestEffort`, the agent applies a strict configuration whose version comes from the probe, and at ABI 2–3 it applies the filesystem part only and says so.** PRD 11's objection to `BestEffort` is the silent no-op. An explicit choice, reported field by field in `Hello` and enforceable through `RequireLandlock`, keeps the decision visible and in the embedder's hands, and partial confinement is strictly better than none.
4. **The applied ABI is capped at 4, and the kernel's own ABI is reported separately.** ABI 5 (ioctl on devices) and ABI 6 (scoping) are not in PRD 11's ruleset. They add breakage risk, for example with `/dev/null` ioctls and terminals, for no stated need.
5. **A probe failure gives ABI 0 and the agent continues; an apply failure after a good probe ends the agent.** "No Landlock here" is a state the container boundary already covers. A boundary the kernel advertised and then refused is exactly the silent downgrade D8 forbids.
6. **The ruleset adds `/proc` (read), five device nodes (read and write), and `/lib32` and `/libx32` to PRD 11's list.** `tools/exec.go` gives every child without stdin the null device, so without `/dev/null` every shell tool fails. Common runtimes read `/proc/self`, and Landlock's ptrace scoping keeps processes outside the domain unreadable through `/proc`.
7. **Under `none`, TCP is denied on loopback too.** Landlock rules are by port, and the rule cannot tell local from remote. PRD 11 chose this behaviour; this spec documents it rather than weakening it.
8. **The agent learns the network mode from `--network`, which defaults to `none`, and `sandbox.AgentArgs` builds its flags.** The agent cannot infer the mode. A missing flag narrows the boundary and never widens it, and one helper keeps the later providers from drifting.
9. **`--landlock-ro` and `--landlock-rw` are added here; the provider options that set them belong to `sandbox_landlock_provider`.** The ruleset's inputs are the agent's command-line contract, and testing the ruleset needs them.
10. **Dangerous flag values are refused, while dangerous environment-derived roots are skipped.** A flag is a programmer's intent, so a mistake there should be loud. `HOME` and `TMPDIR` come from images, and skipping one only narrows the boundary.
11. **When confined, a `SpillDir` outside the writable roots is refused at `Tools`.** That fails once and clearly, instead of on the first result that spills.
12. **There is no flag to turn confinement off.** No command-line path weakens the boundary. `local` is the one unconfined path, and it says so.
13. **`Hello` carries a nested `LandlockReport`, `ProtocolMinor` becomes 2, the per-sandbox facts go on `AgentInfo`, and `Capabilities` gets a bool.** Adding a field bumps the minor version (13). `Capabilities` exists before any sandbox does (13 DD 20), so PRD 11's "Capabilities repeats it" can only mean "this provider's agents can confine".
14. **`RequireLandlock` is checked twice.** It is checked statically in `CheckSpec` against `Capabilities.Landlock`, and dynamically after `Hello` with `Satisfies`, which requires TCP rules under `NetworkNone`. A refusal is a `SpecError`. This keeps 13's rule that all refusals flow through one shape, and it implements PRD 11's "a missing or lower ABI is a Create failure".
15. **`go-landlock` is promoted at the version 14 brings, and nothing else moves.** If an assumption fails, the work stops and the case goes into errata. This is the same discipline 13 and 14 apply, and the snapshot makes it checkable.
16. **Every `go-landlock` import sits behind `//go:build linux`, with a stub elsewhere.** Landlock exists only on Linux, steering requires macOS builds, and the cross-target gate builds windows/amd64.
17. **The start-up order is pinned by an AST test, not by trust.** "Before any other work" cannot be observed from outside. Threads that C code creates before `main` are outside the module's control, and the base image's cgo code (tree-sitter) creates none.
18. **The `tools.Workspace` doc comment changes in this scope.** 13 DD 24 deferred it to the first scope that contains `execute`. The `AllowAll` guidance stays with `sandbox_podman`, because the Landlock layer alone does not justify it.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | depends on, modifies | Builds on `Spec`, `Capabilities`, `CheckSpec`, `SpecError`, `AgentInfo`, `agent.Config`, the `agentkit-sandboxd` command, `attach.New`'s handshake, `fakeagent` and `local`. This spec makes these changes: adds `Spec.RequireLandlock`, `Capabilities.Landlock`, `AgentInfo.Landlock`, `LandlockInfo`, `AgentArgs`, `agent.Config.Landlock`, `attach.Config.Provider`, the `HelloResponse.landlock` field and the new flags. It also updates TS-13-13's oracle, TS-13-24's version string, TS-13-56's capabilities and TS-13-65's direct set. |
| `14_sandbox_rsync_sync` | depends on, modifies | `go-landlock` enters the module graph through `gokrazy/rsync`. `DontRestrict()` on every `rsyncd` server is what keeps confinement in this spec's single place. The `Rsync` RPC must work under the ruleset. This spec bumps `ProtocolMinor` from 1 to 2 (TS-14-5) and makes `go-landlock` direct (TS-14-42). |
| `09_repository_cut` | modifies | `approvedDeps` (TS-09-16) gains `github.com/landlock-lsm/go-landlock`, and TS-09-15 must stay green. |
| `12_core_schema_guard` | depends on | TS-12-14's 20,000-line budget constrains the new code. |

## Verified External API

**In-repository code (read):**
- `tools/exec.go`: `Run` and `RunArgv`. With a nil `ExecOptions.Stdin`, the child's stdin is the null device, which `os/exec` opens as `os.DevNull`.
- `tools/path.go`: the `Workspace` doc comment, quoted in Background.
- `internal/policy/deps_test.go`: `approvedDeps` and `validateAllowlist(deps, approved []string) error`.
- `internal/policy/crosstarget_test.go`: `crossTargets`, `TestCrossTargetBuildAndVet` and `TestHostCgoBuildAndVet`.
- `Makefile`: `doc`, `check`, `test` and `lint`.

**From `13_sandbox_core` and `14_sandbox_rsync_sync`** (specified, not yet in the tree):
- `sandbox.CheckSpec(caps Capabilities, spec Spec) (Spec, error)`, `*SpecError{Provider, Field, Reason}`, `ErrUnsupported`;
- `NetworkMode.String()`, which returns `none`, `allowlist` or `unrestricted`;
- `AgentInfo`, `ProtocolMajor` and `ProtocolMinor`;
- `agent.Config{Workdir string; Env []string; TTL time.Duration}` and `agent.New(cfg) (*Agent, error)`;
- the command's `parseFlags(args []string) (config, error)` and `redirectStdout()`;
- `attach.Config{ID, Spec, Destroy, Metadata}` and `attach.New` / `attach.Dial`;
- `fakeagent`, with a configurable `Hello`;
- `local.Unisolated()`;
- `localtest`.

**`github.com/landlock-lsm/go-landlock`** (expected v0.10.1). **Unverified**: the module source is outside the repository. The symbols assumed are:
- package `landlock`:
  - `type Config`, with the values `V2`, `V3` and `V4`;
  - `func (c Config) RestrictPaths(rules ...Rule) error`, which restricts the filesystem only;
  - `func (c Config) Restrict(rules ...Rule) error`, which restricts the filesystem and the network. With no network rule, every TCP bind and connect is denied;
  - `func (c Config) BestEffort() Config`, which is never called;
  - `func RODirs(paths ...string) FSRule`, `func RWDirs(paths ...string) FSRule`, `func ROFiles(paths ...string) FSRule` and `func RWFiles(paths ...string) FSRule`;
  - `func (r FSRule) WithRefer() FSRule`;
  - `func (r FSRule) IgnoreIfMissing() FSRule`.
- Behaviour assumed: applying a ruleset sets `no_new_privs` and reaches every thread through psx.
- package `landlock/syscall`: `func LandlockGetABIVersion() (int, error)`. If it is absent, the fallback is the standard library's `syscall.Syscall(444, 0, 0, 1)`, which is `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)`. Syscall number 444 is the same on amd64 and arm64.

**`kernel.org/pub/linux/libs/security/libcap/psx`**: indirect, not called. **Unverified**: its cgo variant links with `-Wl,-wrap,pthread_create`, and its non-cgo variant uses `syscall.AllThreadsSyscall`. Whether that link flag passes Go's cgo flag allowlist under the host-cgo gate without `CGO_LDFLAGS_ALLOW` is unknown.

**Standard library** (Go 1.27). **Unverified**: `syscall.Syscall(trap, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)` on Linux, `filepath.EvalSymlinks`, `net.Listen`, `net.Dial`, `go/parser`, `go/ast`.
