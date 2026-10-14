# Run tools and scripts in an isolated sandbox

Status: **draft, decisions settled** (2026-10-12). Nothing here is
implemented. The seven decisions in §0 were open in the first draft and
are now recorded with their outcomes; the rest of the document is written
to them. Next step: this document becomes `.specs/13_sandbox/prd.md` and
the spec work (requirements, test spec, tasks) begins. The few details
left to settle at spec time are marked *spec time*.

Builds on PRD 08 (code mode, the nested-call pipeline) and PRD 10 (the
hands and one driver). Changes neither; §4 says what it adds beside them.

## Intent

Today every tool AgentKit ships runs in the embedder's process, on the
embedder's machine. The file tools are contained to a `tools.Workspace`
root; `execute` and `run_command` are not contained at all, and the code
says so (`Workspace`'s doc comment: "It does NOT constrain `execute`"). The
boundary for a shell is `Config.Guard`, and `guard.Restricted` is
documented as "a floor for unattended safety, not a sandbox".

This PRD adds the sandbox. A **sandbox** is an ephemeral, isolated
execution environment, a container or a micro-VM, that holds exactly one
workspace. When a workspace lives in a sandbox, every tool that operates
on it (`read_file`, `search_files`, `execute`, …) runs *inside* the
sandbox, and every code-mode script bound to those tools has its effects
there. The model, the driver and the embedder's process see only tool
results. The host's files, environment, credentials and network are not
reachable from inside, and nothing inside is reachable from the host
except through the sandbox's own API.

Sandboxes come from **providers**: Podman or Docker containers, Podman
micro-VMs, Kubernetes pods, NVIDIA OpenShell. A provider either runs on the
embedder's machine (embedded) or is reached through a **gateway** on a
remote host (cloud, build farm, another server), which creates, starts,
stops and controls sandboxes there.

The decision rule of PRD 10 still applies: AgentKit supplies mechanism the
model cannot be trusted to supply (containment), not a platform. The
sandbox is hands, not driver. `agentkit.Config`, `New`, the loop, the
batch executor and the nested-call pipeline do not change.

## 0. Decisions

Each was a question in the first draft. The outcome is recorded with the
reasoning that carried it, so it is not re-asked.

### D1. A workspace is bound to a sandbox through proxy tools, not a virtual filesystem

**Decided: proxy tools.** The sandbox runs an *agent* process inside it
that owns a real `tools.Workspace` at `/workspace` and the real
`tools.All` over it. The host gets `[]core.Tool` with the same names,
descriptions, schemas and guidelines as `tools.All`, whose `Execute`
forwards the call to the agent and returns its `core.ToolResult`. `tools`
does not change.

The alternative, an `fs`-like interface under `tools.Workspace`, was a
rewrite: every file tool calls `os.*` directly, and the shell tools, the
symbol table, the Go type-checker behind `find_references` and the ignore
engine all read the local disk. It also could not cover `execute`. Proxy
tools keep every conformance test valid for the in-sandbox copy and keep
the shell tools honest: the process they start is in the sandbox. The cost
is that one copy of the `tools` package, as a small binary, has to be in
the image (D5).

### D2. The wire between host and sandbox is an agentkit gRPC service, not MCP

**Decided: a small agentkit protocol, defined as gRPC.** Not MCP, because
the host would take the tools' schemas from the server's `tools/list`,
which loses the authored property order
(`docs/errata/12_core_schema_guard.md`), so code mode's generated
description and the provider's cached tool prefix could differ between a
local and a sandboxed run; because `CallToolResult` has no slot for
`ToolMetadata` (truncation, spill path, exit code, outcome); and because
PRD 10 deleted the MCP server from this module on purpose. With an
agentkit protocol the host builds the proxy tools from the `tools`
package's *own* definitions, byte-identical schemas included, and only
`Execute` is remote.

gRPC rather than hand-framed JSON on `wire`, because: the protocol has
three shapes that gRPC gives for free and JSON frames would re-implement
(unary calls with native cancellation, streams for sync and export, a
bidirectional stream for D3 (c)); OpenShell's gateway is gRPC, so the
OpenShell provider speaks one framework, not two; and `google.golang.org/grpc`
and `google.golang.org/protobuf` are already in the module graph as
indirect dependencies of the MCP SDK, so promoting them to direct adds
nothing to `go list -m all`. The proto package is `agentkit.sandbox.v1`,
one service for the agent and one for the gateway (§3.2, §5). Tool
arguments and `ToolResult.Data` travel as JSON `bytes` fields, never as
proto structs, so the model's own argument bytes reach the tool and the
result's `Data` comes back exactly as the tool built it. Message size
limits replace `wire`'s bounds. *Spec time:* generated code checked in
(so `go build` needs no `protoc`), and `buf` or `protoc` as the generator
in a `make proto` target.

An embedder that wants MCP can still serve a sandbox's tools over MCP
from its own process, as agent-fox's `af` server does today.

### D3. A code-mode script runs on the host now; in the sandbox with an authorization callback later

**Decided: (a) first, (c) as the follow-up, (b) never.**

What the sandbox adds for a script: a Starlark script already has no
file, network, environment, process or clock access; its only effects are
its tool calls, and with D1 those run inside the sandbox wherever the
interpreter is. What the interpreter's location changes is **memory**
(PRD 08 non-goal: "A memory limit on a script. The chosen runtime cannot
enforce one"), **CPU accounting**, **containment of an interpreter fault**,
and **latency**.

What moving it costs: PRD 08's load-bearing rule is that every nested
call runs the *host's* `Config.Guard`, `Config.After` and execution events
("a script is never a way around the embedder's policy"), and
`Config.Guard` is an arbitrary Go function that cannot be shipped.

- **(a) Interpreter on the host, tools in the sandbox.** `codemode.New`
  over the proxy tools, unchanged. Every nested call goes through the host
  pipeline exactly as today. **This is the first release.**
- **(c) Interpreter in the sandbox, authorization called back to the
  host.** The agent runs `codemode` over its local tools; before each
  nested call it asks the host over a bidirectional stream, and the host
  runs its real pipeline. Every PRD 08 invariant holds and the
  interpreter is isolated, for one round trip per nested call, the same
  round trip (a) pays. **This is the follow-up** (§4, §7 step 8), and (a)
  is built so that (c) reuses its pieces.
- **(b) Interpreter in the sandbox, `guard.Options` shipped as data,
  no callback.** Loses a custom `Guard` function, per-call `After` and
  live events for nothing (c) does not also give. Not built.

### D4. Container providers drive the CLIs; Kubernetes is reached only through a gateway

**Decided.** `podman` and `docker` are driven through their command lines
with `tools.RunArgv`, not through their Go client modules, which are
large dependency graphs for a module whose dependencies are an allowlist.
The CLIs are the stable, documented surface, `podman run` and `docker run`
take the whole hardening set as flags (§3.3), and a pure function
`podman.Args(Spec) []string` is testable offline the way `guard.Check`
is. The micro-VM variant is the same provider with a runtime flag
(`--runtime krun`, libkrun, or `crun-vm`; *spec time:* which the installed
Podman offers).

Kubernetes is a gateway backend only (§5). `client-go` stays out of the
root module, and a pod in a cluster is reached from outside the cluster
through something that is, in every respect that matters here, a gateway,
which is also how OpenShell works.

### D5. The sandbox agent comes from a published image; nothing is injected

**Decided: the image carries the agent.** The provider never copies a
binary into a running container. Two images are published per release:

- `quay.io/agent-fox-dev/agentkit-sandbox:<version>`, the **base image**:
  a small Debian-based image with the agent at
  `/usr/local/libexec/agentkit-sandboxd`, built with cgo so `outline` has
  its tree-sitter grammars, plus `git`, `bash`, `ca-certificates` and a
  non-root user. `Spec.Image` defaults to it. No `rsync`: the agent
  carries the rsync protocol itself (D7).
- `quay.io/agent-fox-dev/agentkit-sandboxd:<version>`, the **overlay
  image**: `FROM scratch`, holding only a statically linked agent
  (`CGO_ENABLED=0`, so `outline` is `go/ast` for Go and `none` for the
  rest) at the same path. An embedder with its own toolchain image adds
  one line to its Containerfile:

  ```dockerfile
  COPY --from=quay.io/agent-fox-dev/agentkit-sandboxd:0.6 /usr/local/libexec/agentkit-sandboxd /usr/local/libexec/
  ```

The agent's path is a constant of the protocol (`sandbox.AgentPath`). A
`Create` whose image has no agent there fails before any tool call, with
an error naming the path and the `COPY --from` line. The agent reports its
build (version, protocol version, outline backend, whether `git` is
present) in the `Hello` RPC, and `Capabilities` repeats it.

The injection alternative (copy a host binary in after create) would have
let any image work, but it put a host artifact inside the sandbox on
every create, needed a second copy of the agent on the host, and made
"which agent is running" a question with two answers. An image is one.

### D6. The gateway server is a nested module; the client is in the root module

**Decided.** The client (a `Provider` that talks to a gateway) is small
and belongs in the root module beside the other providers. The server is
a long-running network service with its own dependency and operational
story (authentication, TLS, the Kubernetes API), so it lives in the nested
module `sandbox/gateway/` with its binary in `cmd/agentkit-gateway`
inside that module, like `codesearch`. PRD 10's rule that "nothing in this
module listens on a socket" stays true of the root module.

### D7. Sync is `git clone` for public repositories and rsync for local folders, with rsync as a Go library

**Decided.** Two sources, both first-class operations on a `Sandbox`:

- **`GitRepo`**: a public repository, cloned *for* the sandbox in an
  **init step** with network access (§3.5), before the sandbox proper
  starts with its own network policy. No credential is involved, which is
  why it is public repositories only in the first release.
- **`LocalDir`**: a host directory, transferred with the rsync protocol,
  honouring `.gitignore`, incremental on every later `Sync` (the
  protocol's own quick check and delta transfer), and reversible:
  `Export` to a `LocalDir` is the same transfer the other way, so an
  embedder can pull the model's edits back into a working tree without a
  tar in between.

**The rsync is `github.com/gokrazy/rsync`, not an `rsync` binary.** It is
a native Go implementation of the rsync protocol (version 27,
wire-compatible with tridge rsync and openrsync) with a client package,
`rsyncclient`, whose `Client.Run(ctx, conn io.ReadWriteCloser, paths)`
runs one transfer over *any* connection, and a server package, `rsyncd`,
whose `NewConnection(r, w, name)` and `Server.HandleConnArgs(ctx, conn,
module, args)` serve a `Module{Path, Writable}` over any reader and
writer pair. So the two ends run in the two agentkit processes, the bytes
travel on a stream of the agent service (§3.2), nothing on the host or in
the image has to be installed, and a sync through a gateway is the same
code path as a local one with no remote-shell bridge. The host is always
the client: a sender for `Sync`, a receiver for `Export`. The agent is
always the server, with one module, the workspace, writable for `Sync`
and read-only for `Export`. Filter rules (`--filter`, `--exclude`,
`--include`) and `--delete` are parsed by the library's option table;
`--exclude-from` and `--include-from` are not, so `.gitignore` is applied
as a filter rule (`--filter=':- .gitignore'`, the per-directory merge
rule). *Spec time:* confirm the library's filter engine honours the `:`
dir-merge modifier and that the receiver acts on `--delete`; if either is
missing, the host expands the rules itself with the `tools` ignore
engine into explicit `--exclude` patterns, and the acceptance criteria
in §8 catch a gap either way. Known limits of the library, acceptable for
a source tree: no xattrs or ACLs (protocol 30 features), MD4 checksums,
and the project's own statement that it has fewer users than tridge
rsync. It becomes a direct dependency of the root module with a ruling
in `docs/DEPS.md`; what it pulls in is listed under Dependencies.

`Tar` stays as the third source and destination for an embedder with its
own packaging. A private repository in the first release is cloned on
the host and sent as a `LocalDir`.

### D8. Landlock: the agent confines itself, nothing else gets confined by accident, and a provider is built on it

**Decided**, in three parts, all in the first release.

**Part one is not optional.** `rsyncd.NewServer` in `gokrazy/rsync`
applies Landlock to the *whole calling process* at construction whenever
its module list is non-empty: `landlock.V3.BestEffort().RestrictPaths`
with a default set of system directories read-only, the module paths
read-write, and everything else denied, irrevocably, for the process and
every child it starts afterwards. Inside `agentkit-sandboxd` that would
confine every later tool call to the workspace and that default list,
so `execute` would lose `/tmp`, the Go build cache and anything else a
command needs. In the `local` provider, where the agent runs in the
embedder's process, it would landlock the embedder itself, `go test`
included. So every `rsyncd.Server` this module constructs is built with
`rsyncd.DontRestrict()`, and confinement is this module's own decision,
made once, in one place (part two). A test pins it: after a `Sync` over
`local`, the test process can still write outside the workspace.

**Part two: the agent applies its own ruleset at startup.** Before it
serves its first RPC, and before any cgo thread exists, the agent
restricts itself with `github.com/landlock-lsm/go-landlock`: read and
write on the workspace, the spill directory and a private `HOME`; read and
execute on `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc` and `/opt`;
nothing else on the filesystem; and, under `NetworkNone`, no TCP `bind` or
`connect` on any port (ABI v4, Linux 6.7). Every process a tool starts
inherits it. This closes a gap the code has always stated: "`Workspace`
does NOT constrain `execute`". Inside the sandbox it now does, at the
kernel, under the container boundary rather than instead of it, so a
provider option that weakened the container (a writable root, a host
network) still meets a second layer.

Rules that follow:

- **Never `BestEffort()` for a boundary.** A best-effort call succeeds
  with no error on a kernel without Landlock. The agent asks for the ABI
  it needs and reports the ABI it *applied* (or none) in `Hello`;
  `Capabilities` repeats it. `Spec.RequireLandlock` makes a missing or
  lower ABI a `Create` failure; off by default in the first release,
  because the container boundary is the primary one and a micro-VM or
  `podman machine` kernel may lack Landlock, and on by default once the
  published images' kernels are known to carry it.
- **Early, and before threads.** The base-image agent is a cgo build;
  below ABI v8 Landlock reaches other OS threads only through the psx
  trick go-landlock performs, and threads created before the call by C
  code are not covered. The ruleset is applied first thing in `main`.
- **Where it does not reach.** Landlock restricts filesystem and TCP; it
  does not bound memory, CPU or process count, cannot see UDP or Unix
  sockets below ABI v9 and v10, and cannot restrict Multipath TCP, which
  Go's `net.Listen` uses by default since Go 1.24. Those stay the
  container's job. The ruleset is defense in depth, and the
  documentation says so.
- **The host is never confined.** No code path in `sandbox`, the
  gateway client or `local` applies Landlock to the process it runs in.

**Part three: a provider with no container, `sandbox/landlock`.** On a
Linux host the agent runs as a child process of the embedder, over
pipes, with the part-two ruleset as its *only* boundary: a workspace
directory, a private `TMPDIR` and `HOME` the provider creates for that
sandbox; read and execute on the system directories, so the host's own
toolchain is available to `execute`; nothing else readable, so `~/.ssh`,
`~/.aws`, the embedder's source and the real home directory are not;
TCP denied under `NetworkNone`; `Spec.Env` as the whole environment. It
starts in milliseconds, needs no image and no daemon, and is the
provider a developer reaches for on their own machine and the one a
Linux CI job can use without privileges. What it cannot do, it refuses
rather than approximates: `Resources` (Landlock bounds none of them),
`NetworkAllowlist` (ports, not hosts), and any kernel below ABI v4,
because filesystem confinement with the network open is not what
`NetworkNone` promises. It is a *confinement*, not an isolation: no
kernel boundary, no PID namespace, no resource limits, and the gaps
listed above (UDP and Unix sockets below ABI v9 and v10, Multipath TCP).
Its `Capabilities` say exactly that, and the provider table in §6 ranks
it between `local` and `podman`. Details in §3.6.

## Goals

1. An embedder can create a sandbox from a provider, populate its one
   workspace from a local directory or a public git repository, and get a
   `[]core.Tool` that is a drop-in for `tools.All` over that workspace:
   same names, descriptions, input and output schemas and guidelines, so
   `Config.Policy`, `Config.Guard`, `guard.Restricted`, `prompt.Build`
   and `codemode.New` work on it unchanged.
2. A tool call on a sandboxed workspace runs inside the sandbox. The
   result the model and the embedder receive is a `core.ToolResult`;
   neither can read, write or address anything inside the sandbox except
   by making another tool call. The embedder can additionally sync in
   and export out, as explicit operations on the sandbox, never through
   a tool.
3. A sandbox isolates what runs inside it from the host: no host files,
   no host environment or credentials, no host network beyond what the
   spec allows, bounded CPU, memory and process count, and a lifetime
   after which it is destroyed whatever the embedder does.
4. A sandbox spec that a provider cannot honour is refused at `Create`.
   No provider silently widens a policy.
5. The same code drives a provider in the embedder's process and a
   provider on a remote host through a gateway. A sandbox created
   through a gateway behaves, to the embedder and to the model, exactly
   as one created locally.
6. Code mode over a sandboxed workspace keeps every PRD 08 invariant: a
   script's calls run inside the sandbox and through the host's
   interceptor, `After` and event pipeline (D3).
7. Everything but the containers themselves is testable offline: the
   proxy tools against an in-process agent, the provider's argument
   construction as a pure function, the gRPC service with both ends in
   one process over a pipe, the gateway client against an in-process
   server. Container integration tests run when `podman` or `docker` is
   installed, Landlock provider tests when the kernel has ABI v4, and
   both are skipped, not failed, otherwise.
8. `agentkit.Config`, `New`, `core`, `tools` and `codemode` are unchanged
   in their exported surface. A sandbox is a different source of
   `core.Tool`s, nothing else.

## Non-goals

- **A general container platform.** No image building beyond the two
  published images, no registries, no volumes beyond the workspace, no
  port publishing, no compose. An embedder that needs those uses the
  provider's own tooling and hands AgentKit the image name.
- **Persistent or shared sandboxes.** A sandbox holds one workspace, is
  created for a run or a pipeline phase, and is destroyed. No snapshot,
  restore, pause, migration or reuse across embedders. The durable state
  is what the embedder exports and parks in git, as PRD 10 §9 already
  assumes.
- **More than one workspace per sandbox**, or a workspace spanning
  sandboxes.
- **Private git repositories cloned inside the sandbox.** A credential
  would have to enter the sandbox. Clone on the host, sync as a
  `LocalDir`.
- **A network allowlist on the Docker and Podman providers in the first
  release.** They offer `none` and `unrestricted`. An allowlist needs an
  egress proxy; OpenShell has one, and a provider that has it declares
  it. A spec that asks for an allowlist from a provider without one is
  refused (goal 4), not approximated.
- **Implementing an OCI runtime, a hypervisor, a Kubernetes client or an
  rsync protocol.** The providers drive what is installed; the rsync
  protocol is a library (D7).
- **Browsers, displays, GPUs.** A provider may pass through what its
  backend offers via provider-specific options; nothing here depends on
  it.
- **Changing the driver.** No `Config.Sandbox`. The driver does not know
  a tool is remote, and must not: that is what makes goal 8 true.
- **Windows hosts.** The providers target Linux and macOS
  (`.specs/steering.md`). On macOS, Podman and Docker already run
  containers inside a Linux VM, which is a stronger boundary than the
  same container on Linux, not a weaker one.
- **Interactive attach** (a terminal into the sandbox). The provider CLIs
  do this already; AgentKit's surface is tool calls.

## Background

What exists and is reused:

- `tools.Workspace` resolves and contains paths; `tools.All(Options)`
  builds the eleven built-in tools over one; `tools.Run` and `RunArgv`
  run processes in their own process group, kill the group on timeout or
  cancel, interleave output, truncate and spill; `ReducedEnv` strips
  credentials. Inside the sandbox, all of this runs exactly as it does on
  a host today.
- `core.Tool` separates definition (name, description, schemas,
  guidelines, `ExecutionMode`) from execution (`Execute`). A proxy tool is
  the definition from `tools` with a remote `Execute`.
- `core.ToolResult` already carries everything a result needs to cross a
  wire: `OK`, `Data`, `Error`, `Detail`, `Metadata`, `Text`. The proto
  message mirrors it field for field, `Data` as JSON bytes.
- `guard.Check(argv, Options) Decision` is a pure function on data; with
  D3 (c) it is also the shape of the authorization answer the host sends
  back.
- `codemode.New(tools, opts)` binds any `[]core.Tool`; the proxy tools
  are one.
- `mcp` shows how this module already runs a child in its own process
  group with exactly the configured environment, kills it as a group and
  forwards its stderr. `podman exec` is such a child.
- `google.golang.org/grpc` and `google.golang.org/protobuf` are already
  in `go.mod` as indirect requirements of the MCP SDK.
- The `tools` ignore engine decides what a workspace is; the rsync filter
  rules of D7 say the same thing in rsync's vocabulary, and §8 checks
  that the two agree on `.gitignore`.

What does not exist: any notion of a tool running elsewhere, a provider
abstraction, a lifecycle, a file transfer, or a protocol between two
agentkit processes.

**OpenShell** (NVIDIA, github.com/NVIDIA/OpenShell), as of this writing: a
runtime for autonomous agents where a sandbox runs behind a *gateway*,
the control plane for lifecycle, policy, logs and exec sessions, exposed
over gRPC and through a CLI (`openshell sandbox create --name …`,
`openshell policy set`). Policy is YAML covering filesystem, network and
process rules; every outbound connection goes through a CONNECT proxy
checked against `network_policies` by host, port and calling binary, and
a connection no rule matches is denied; credentials are injected only
into requests to approved endpoints. Locally it runs on Docker, Podman or
host virtualization; in a cluster the gateway deploys with Helm and
relies on the CNI enforcing `NetworkPolicy`. It is the model for the
gateway shape in §5 and the first third-party provider (§6). Its gRPC
surface and policy keys are to be verified against the repository's proto
definitions when its provider is specified; nothing here depends on their
exact names.

## 1. Vocabulary

| Term | Meaning |
|---|---|
| **Sandbox** | One isolated execution environment with one workspace, created from a `Spec` by a `Provider`, destroyed on `Close` or at its TTL. |
| **Workspace** | The one directory inside a sandbox that tools operate on, `/workspace` unless the spec says otherwise. A volume the provider creates with the sandbox and destroys with it; never a mount of a host path. |
| **Provider** | What creates sandboxes: `podman`, `docker` (same code), `landlock` (a confined child process, Linux), `gateway` (a client to a remote gateway), `openshell`, and `local` (no isolation, tests only). |
| **Sandbox agent** (`agentkit-sandboxd`) | The AgentKit process inside the sandbox, at `/usr/local/libexec/agentkit-sandboxd`. It owns the workspace, runs `tools.All` over it, and serves the agent service on its stdio. |
| **Base image**, **overlay image** | The two published images (D5): one to run as is, one to `COPY --from` into an embedder's own image. |
| **Init step** | A short-lived run on the workspace volume, before the sandbox starts, that clones a `GitRepo`. The one place the provider allows network access that `Spec.Network` would not. |
| **Agent service**, **gateway service** | The two gRPC services of `agentkit.sandbox.v1`: hello, tool calls, the rsync stream and tar sync and export to an agent; lifecycle and relay on a gateway. |
| **Gateway** | A service on a remote host that implements the provider operations and relays the agent service to each sandbox it manages. |
| **Proxy tool** | A `core.Tool` on the host whose definition comes from `tools` and whose `Execute` runs the call in a sandbox. |

## 2. The host-side API

```go
package sandbox

const AgentPath = "/usr/local/libexec/agentkit-sandboxd"

type Spec struct {
    Image     string            // OCI image with the agent at AgentPath; default: the base image at this module's version
    Workdir   string            // the workspace path inside; default "/workspace"
    Env       map[string]string // the sandbox's whole environment; nothing is inherited
    Resources Resources         // zero fields take the provider's defaults
    Network   Network           // zero value: NetworkNone
    TTL       time.Duration     // destroyed at TTL whatever happens; default 1h, never unbounded
    RequireLandlock bool        // fail Create unless the agent applied its Landlock ruleset (D8)
    Labels    map[string]string // forwarded to the backend for the embedder's own bookkeeping
    Provider  map[string]any    // provider-specific options, validated by the provider
}

type Resources struct {
    CPUs        float64 // e.g. 2
    MemoryBytes int64
    PIDs        int
    DiskBytes   int64   // the workspace volume's size, where the backend can bound it
}

type Network struct {
    Mode  NetworkMode // NetworkNone (default), NetworkAllowlist, NetworkUnrestricted
    Allow []Endpoint  // host:port, with NetworkAllowlist
}

type Provider interface {
    // Capabilities says what this provider can honour, so an embedder can
    // decide before Create, and Create refuses a Spec outside them.
    Capabilities() Capabilities
    Create(ctx context.Context, spec Spec) (Sandbox, error)
    // Reap destroys sandboxes this provider created, by label, that no
    // live Sandbox value owns: what a crashed host left behind.
    Reap(ctx context.Context) ([]string, error)
}

type Sandbox interface {
    ID() string
    Spec() Spec
    // Sync populates or updates the workspace from src. It is the only way
    // bytes enter the sandbox other than through a tool call. A LocalDir
    // sync is incremental and SyncResult carries the transfer statistics
    // (files listed, transferred, bytes sent); a GitRepo sync is allowed
    // once, before the first Tools call, because it runs in the init step.
    Sync(ctx context.Context, src Source) (SyncResult, error)
    // Tools is the built-in tool set over the sandbox's workspace, with
    // tools.All's definitions and a remote Execute. opts selects the same
    // things tools.Options does where they apply (Symbols; SpillDir inside
    // the sandbox).
    Tools(ctx context.Context, opts ToolOptions) ([]core.Tool, error)
    // Export copies the workspace, or a subtree of it, out: to a LocalDir
    // by the rsync protocol, or to a Tar. It is the embedder's action,
    // never a tool's.
    Export(ctx context.Context, path string, dst Destination) error
    // Close destroys the sandbox and its volume. Idempotent. A sandbox is
    // also destroyed at its TTL and when the agent process ends.
    Close(ctx context.Context) error
}

// Sources and destinations.
type LocalDir struct {
    Path          string
    IncludeIgnored bool // send what .gitignore excludes too; default false
    ExcludeGit     bool // leave .git behind; default false, the model gets git history
    Delete         bool // on a re-sync, remove files in the sandbox the source no longer has
}
type GitRepo struct { URL, Ref string; Depth int } // public; cloned in the init step
type Tar struct { R io.Reader }                     // as a Source
type TarOut struct { W io.Writer }                  // as a Destination
```

How it composes with the rest of the module, which is the point:

```go
p, err := podman.New(podman.Options{})            // or landlock.New(landlock.Options{}), or gateway.Dial(url, token)
sb, err := p.Create(ctx, sandbox.Spec{
    Resources: sandbox.Resources{CPUs: 2, MemoryBytes: 4 << 30}, TTL: 30 * time.Minute})
defer sb.Close(ctx)
_, err = sb.Sync(ctx, sandbox.LocalDir{Path: "."})  // rsync protocol, .gitignore honoured
ts, err := sb.Tools(ctx, sandbox.ToolOptions{})     // a drop-in for tools.All
cm, _, err := codemode.New(subset(ts, "read_file", "search_files", "execute"), codemode.Options{})
agent, err := agentkit.New(agentkit.Config{
    Client: client, Model: "claude-opus-5-5",
    Tools:  append(ts, cm),
    Guard:  guard.Restricted(guard.Options{AllowedPrograms: []string{"go", "git", "make"}}),
})
res, err := agent.Run(ctx, "Make the tests pass.")
err = sb.Export(ctx, ".", sandbox.LocalDir{Path: "."}) // the edits come back the same way they went in
```

Nothing in `agentkit.Config` names the sandbox. `New` still refuses a
reachable shell tool with a nil `Guard`, sandboxed or not: the guard is
the embedder's statement of what the model may *ask for*, and that
statement is still worth making when the blast radius is a container.
`guard.AllowAll` becomes a defensible choice for a run whose every shell
tool is sandbox-bound, and the `guard` documentation will say so.

### What a proxy tool guarantees

- Its `Name`, `Description`, `InputSchema`, `OutputSchema`,
  `PromptGuidelines`, `Builtin`, `ExecutionMode` and `PrepareArguments`
  are those of the same tool from `tools.All`, taken from the package, not
  from the wire. A conformance test compares them field by field, and the
  JSON of the two schemas byte for byte.
- `Execute` sends the prepared arguments as the agent receives them from
  the pipeline (`PreparedArguments.Raw`, the model's bytes), and returns
  the agent's `core.ToolResult` with `OK`, `Data`, `Error`, `Detail`,
  `Metadata` and `Text` intact.
- Paths in results are workspace-relative or in-sandbox absolute
  (`/workspace/...`); no host path can appear, because the host has none
  to offer.
- The host's context cancels the call: gRPC propagates the cancellation,
  the agent kills the process group as `tools.Run` does today, and the
  result is the same `aborted` result a local call gets.
- A call whose connection dies gets `core.ErrResult("sandbox_unavailable",
  …)`, and every later call on that sandbox the same, until `Close`. There
  is no transparent reconnect: a sandbox whose agent died has lost
  whatever that process held, and pretending otherwise would hand the
  model a workspace that is not the one it was working in.
- Proxy tools carry the same Sequential/Parallel modes, so the batch
  executor serializes writes and shell calls as it does locally, and the
  agent still runs them in whatever order the host sends.

## 3. Inside the sandbox

### 3.1 `agentkit-sandboxd`

A command in this module, built into both images (D5). First thing in
`main`, it applies the Landlock ruleset of D8 (or records that the kernel
has none), then takes the workspace path and the tool options from its
flags, builds `tools.All` over `tools.NewWorkspace(workdir)` with `Env`
set to its own (explicit) environment, and serves the agent service over
gRPC on its stdin/stdout, one HTTP/2 connection on a `net.Conn` made of
the two pipes. It is the only process the provider starts; everything
else inside is a child of a tool call, and inherits the ruleset.

It has no model, no provider client, no driver: it is `tools`, the
generated proto package, the service, and the `rsyncd` server of
`gokrazy/rsync` serving the workspace as its one module, constructed
with `rsyncd.DontRestrict()` (D8), about the size of `mcp/client.go`. The overlay build is `CGO_ENABLED=0`; the base-image
build has cgo for tree-sitter.

### 3.2 The protocol: `agentkit.sandbox.v1`

```protobuf
service Agent {
  rpc Hello(HelloRequest) returns (HelloResponse);        // versions, workspace, tool names, outline backend, has git, Landlock ABI applied (0 = none)
  rpc Call(CallRequest) returns (CallResponse);           // tool, input (JSON bytes) -> ToolResult (ok, data JSON bytes, error, detail, metadata, text)
  rpc Rsync(stream RsyncFrame) returns (stream RsyncFrame); // first frame: direction and the server-side rsync args; then bytes both ways (§3.5)
  rpc SyncTar(stream TarChunk) returns (SyncResult);       // a tar stream into path, replace or merge
  rpc ExportTar(ExportRequest) returns (stream TarChunk);  // a tar stream out of path
  // D3 (c), later:
  // rpc RunScript(stream ScriptFrame) returns (stream ScriptFrame);
}
```

Rules:

- `Call` runs on its own goroutine per request; the host's batch
  executor, not the agent, decides what is sequential. Cancelling the
  host context cancels the RPC; the agent treats that as `tools.Run`
  treats a cancelled context.
- `input` and `data` are JSON bytes, untouched by the proto layer. The
  agent decodes `input` exactly as a local tool would; the host hands
  `data` to `core.ToolResult.Data` as the tool built it.
- A `Call` for a tool the agent does not serve is a response with
  `unknown_tool`, not an RPC error. RPC errors are for the transport
  (connection lost, message over the limit, agent gone), and become
  `sandbox_unavailable` on the host.
- Message size limits are set on both ends (*spec time:* the number; a
  tool result is at most 50 KB plus metadata, so the limit is for the
  tar chunks and the arguments). A tar is validated on extraction, both
  directions: no absolute names, no `..`, no entry that would write
  through a symlink out of the target, no device nodes. On the host the
  exported tar is treated as untrusted: a hostile repository's build step
  wrote it.
- `Rsync` is not a tool. Its stream is an `io.ReadWriteCloser` on each
  end: the host hands it to `rsyncclient.Client.Run`, the agent wraps it
  with `rsyncd.NewConnection` and hands it to `Server.HandleConnArgs`
  with the workspace module. The first frame carries the direction (which
  decides whether the module is writable) and the server-side options the
  client computes with `ServerCommandOptions`; the agent accepts only the
  options the library's own table knows and refuses a path outside the
  workspace. The model never sees it; nothing in `core` or `codemode` can
  address it.
- Versioning is the proto's: fields are added, never renumbered. `Hello`
  carries the agent's module version and the protocol's; a host refuses
  an agent whose protocol major differs and warns on an older minor. A
  published image therefore does not need rebuilding for every release.

### 3.3 Isolation, concretely

A provider must deliver all of these or refuse the `Create`. Each is a
requirement with a test; for the container providers the test is on the
argv `podman.Args(spec)` produces, offline.

- **Filesystem.** No bind mounts from the host. The root filesystem is
  read-only; the workspace volume and `/tmp` are writable, the volume
  bounded by `Resources.DiskBytes` where the backend can (volume size
  options on Podman with a supporting driver; refused otherwise, per goal
  4). The volume is created with the sandbox, labelled like it, and
  removed with it.
- **Environment.** `Spec.Env` is the entire environment of the agent and
  of every process it starts. The provider adds `PATH`, `HOME`, `LANG`,
  `TMPDIR` and `TERM` if the spec does not. Nothing from the host
  environment is inherited; `tools.ReducedEnv` is not involved because
  there is nothing to reduce.
- **Credentials.** The provider's own credentials (a registry login, a
  gateway token, a cloud identity) never enter the sandbox. No git
  credential exists in the first release (D7).
- **Network.** `NetworkNone` is `--network none`. `NetworkUnrestricted`
  is the backend's default network. `NetworkAllowlist` is refused by the
  container providers in the first release and honoured by OpenShell.
  The init step (§3.5) is the one documented exception.
- **Privilege.** `--cap-drop ALL`, `--security-opt no-new-privileges`,
  rootless where the backend supports it (`--userns` as configured), the
  image's non-root user, and the backend's default seccomp profile. The
  micro-VM runtime adds a kernel boundary on top.
- **Kernel confinement inside (D8).** The agent's own Landlock ruleset,
  inherited by every tool process: filesystem writes only in the
  workspace, the spill directory and its private `HOME`; reads and
  execution only from the system directories; no TCP under
  `NetworkNone`. The ABI applied is in `Hello`; `Spec.RequireLandlock`
  turns "none" into a `Create` failure. This layer is in addition to the
  container's, never a substitute, and does not bound resources.
- **Resources.** `--cpus`, `--memory` (with swap equal to memory, so
  swap cannot be used to exceed it), `--pids-limit`.
- **Lifetime.** `TTL` is enforced twice: the provider schedules a destroy
  at `TTL` on the host, and the agent exits at `TTL` on its own so the
  container ends if the host is gone. Every sandbox and its volume carry
  the label `agentkit.sandbox=<id>` plus the embedder's `Labels`, and
  `Provider.Reap` destroys labelled containers and volumes no live value
  owns.
- **No way in but the protocol, no way out but a result.** The agent
  serves on its stdio, an `exec` channel the provider owns. A tool result
  is capped as it is today (50 KB per result, spill inside the sandbox).
  `Sync` and `Export` move bytes only when the embedder calls them.

### 3.4 What the model experiences

Nothing new. The tools have the same names and descriptions. Paths are
workspace-relative. A spill path in `Metadata` names a file inside the
sandbox, which the model can reach with `execute` as it can a `/tmp`
spill file today, and cannot with `read_file`, as today. The system prompt
gains no sandbox text by default; an embedder that wants the model to
know it is in a container (so it does not try to open a browser) says so
in `Config.System`.

### 3.5 Sync and export

**`GitRepo`, in the init step.** The provider creates the workspace
volume, then runs the image once with the volume mounted, network
*unrestricted*, no `Spec.Env`, and the single command
`git clone --depth N --branch REF URL /workspace` (the base image has
`git`; an overlay image's embedder must provide it). That run ends, and
the sandbox proper starts on the populated volume with `Spec.Network`
applied. Nothing from the host is present during the clone, and nothing
of the clone's network reaches the sandbox: the two are different
processes on the same volume. On Kubernetes this is literally an init
container. A `GitRepo` sync after `Tools` has been called is refused: the
init step is before the sandbox has run anything.

**`LocalDir`, by the rsync protocol.** The host opens the `Rsync` stream
and runs `rsyncclient.New(args, rsyncclient.WithSender())` over it, with
`args` of `-a`, `--filter=':- .gitignore'` unless `IncludeIgnored`,
`--exclude=.git` only when `ExcludeGit`, and `--delete` when `Delete`;
the agent serves the workspace as a writable `rsyncd.Module` on a server
built with `DontRestrict()` (D8). Nothing is installed on either side: both ends are `gokrazy/rsync` in the two
agentkit binaries, and the bytes ride the gRPC stream, so a sync through
a gateway is the same code as a sync into a local container. A second
`Sync` of the same `LocalDir` sends only what changed, by the protocol's
quick check (size and mtime) and delta transfer, which is what makes a
local-folder workflow (edit on the host, re-sync, run the agent again)
cheap. `SyncResult` carries the library's `TransferStats`.

**`Export`** mirrors `Sync`: to a `LocalDir` by the same protocol
reversed, the host as receiver and the workspace served read-only (the
sandbox's `.gitignore` honoured the same way, so build output stays
behind unless asked), or to a `TarOut`. What comes back is treated as
untrusted on the host (§3.2): the receiver writes only under the
destination directory and never follows a symlink out of it.

### 3.6 The Landlock provider

`landlock.New(landlock.Options{})` returns a `Provider` on Linux and an
error naming `podman` as the alternative elsewhere. `New` probes the
kernel once: ABI v4 or later, else an error naming the ABI found and the
kernel version that carries v4. What `Create` does:

- **A sandbox directory** under `Options.Root` (default: a per-user
  directory under `os.UserCacheDir()`), holding `workspace/`, `home/` and
  `tmp/`, mode `0700`, plus a marker file with the agent's pid and the
  `Spec`. There is no volume and no image; the directory is the sandbox.
- **The agent as a child.** `agentkit-sandboxd` is started from
  `Options.AgentBinary` (default: next to the embedder's executable, then
  `PATH`) in its own process group, stdio as the gRPC connection,
  environment exactly `Spec.Env` plus `HOME`, `TMPDIR` and `PATH` set to
  the sandbox's `home/`, `tmp/` and the system directories. The agent
  applies the part-two ruleset with the workspace, `home/` and `tmp/`
  read-write, the system directories (`/usr`, `/bin`, `/sbin`, `/lib`,
  `/lib64`, `/etc`, `/opt`, `/usr/local`) read and execute,
  `Options.ExtraRO` and `Options.ExtraRW` as the embedder's deliberate
  widening (a shared Go build cache, a toolchain under the user's home),
  and TCP denied under `NetworkNone`. `Hello` reports the ABI; the
  provider refuses the sandbox if it is below v4, whatever
  `Spec.RequireLandlock` says, because here there is no other boundary.
- **The init step for `GitRepo`** is a second child, `git clone` into
  `workspace/`, under the same filesystem ruleset but with TCP allowed,
  and it ends before the agent starts. As on the container providers,
  nothing of its network reaches the sandbox: the agent is a different
  process with its own, stricter ruleset.
- **What is refused.** `Resources` with any field set; `NetworkAllowlist`;
  `Spec.Image` (there is no image); a kernel below ABI v4. Each refusal
  names the field and the provider.
- **Lifetime.** `TTL` kills the agent's process group from the host, and
  the agent exits on its own at `TTL` as everywhere. `Close` kills the
  group and removes the directory. `Reap` walks `Options.Root`, and
  removes every sandbox directory whose marker names a pid that is no
  longer alive or no longer the agent.
- **What it gives up**, said once more because it is the provider most
  likely to be mistaken for more than it is: the host kernel, memory and
  CPU are shared; a process inside can see the host's process list and
  signal processes it owns; UDP, Unix sockets below ABI v9 and v10, and
  Multipath TCP are not restricted; `chmod` and `stat` are not
  restricted. It keeps the model's commands out of the embedder's files,
  home directory and network, which is what a developer running an agent
  over their own checkout needs most, and nothing more.

## 4. Code mode and the sandbox

Under D3 (a), `codemode.New(proxyTools, opts)` is the whole story: the
script runs on the host, each tool function is a proxy tool, each call
goes through `core.CallNested` and the host pipeline, and executes inside
the sandbox. `codemode` does not change. The one check worth adding is a
smoke test: a script over sandboxed tools, against `provider/faux` and the
`local` provider, blocked by the guard on one call and completing on the
others, with the nested events carrying the parent id, as the existing
PRD 08 smoke tests do over local tools.

Under D3 (c), later: the agent gains `codemode`, `Sandbox.Tools` offers a
`code_mode` whose `Execute` opens a `RunScript` stream and sends the
script; before each nested call the agent sends an `authorize` frame
(tool, arguments) and waits for the host's `authorized` frame (run with
these arguments, or this blocked result), and after it sends the call's
result so the host can run `After` and emit the events. The host-side
`code_mode` proxy runs the real pipeline on each `authorize`, so `Guard`,
`After` and the events are the host's. `parallel` inside a script sends
several `authorize` frames at once. The service in §3.2 reserves the RPC
so (a) is built in a way that does not preclude it: the `Call` request
and response messages are the frames (c) reuses.

## 5. The gateway

For a sandbox on another host. The gateway implements `Provider` over
gRPC and relays the agent service to each agent it started.

```protobuf
service Gateway {
  rpc Capabilities(CapabilitiesRequest) returns (CapabilitiesResponse);
  rpc Create(CreateRequest) returns (CreateResponse);     // Spec -> id
  rpc List(ListRequest) returns (ListResponse);           // id, spec, created, expires
  rpc Destroy(DestroyRequest) returns (DestroyResponse);
  rpc Reap(ReapRequest) returns (ReapResponse);
}
// The gateway also serves the Agent service itself, proxied: the sandbox
// id travels in request metadata, and every Agent RPC is forwarded to that
// sandbox's agent over the gateway's own exec channel.
```

- **Client.** `gateway.Dial(url, token) Provider`, in the root module. A
  `Sandbox` from it holds the id and makes `Agent` RPCs against the
  gateway with the id in metadata; `Sync`, `Tools`, `Export` and `Close`
  are the same calls whoever is on the other end; the `Rsync` stream is
  proxied like any other, so a sync through a gateway needs no bridge.
  The gateway client is how a remote OpenShell deployment, a Kubernetes
  cluster and a plain remote Podman host all look the same to an embedder
  (goal 5).
- **Server.** `cmd/agentkit-gateway` in the nested `sandbox/gateway`
  module (D6). It holds one *backend* provider, the same `Provider`
  interface, driving Podman or Docker on its own host or pods in its
  cluster, and enforces `TTL` and `Reap` for everything it created.
  Authentication is a bearer token per connection in the first release;
  TLS is the deployer's (a reverse proxy, or the server's own certificate
  flags).
- **Kubernetes backend.** Lives in the gateway module, so `client-go`
  stays out of the root module (D4). A pod per sandbox: an init container
  for the `GitRepo` clone, one container from `Spec.Image` with the agent
  at `AgentPath`, resource requests and limits from `Resources`, a
  `NetworkPolicy` for `NetworkNone` (refused if the cluster's CNI does
  not enforce them, which the gateway checks once at start),
  `activeDeadlineSeconds` for `TTL`, the workspace as an ephemeral
  volume, and the agent reached through the API server's exec stream.
- **One hop of trust.** The gateway holds the backend's credentials and
  the host holds the gateway's token. The sandbox holds neither.

## 6. Providers

| Provider | Package | Backend | Isolation | Network modes | Status |
|---|---|---|---|---|---|
| `podman` | `sandbox/podman` | `podman` CLI; `docker` CLI with `Options.Binary: "docker"` | container; micro-VM with `Options.Runtime` (`krun` or `crun-vm`) | none, unrestricted | first release |
| `landlock` | `sandbox/landlock` | a child process on a Linux host, Landlock ABI v4 or later | filesystem and TCP confinement only; no resource limits, no kernel boundary (§3.6) | none (TCP), unrestricted | first release, Linux only |
| `gateway` | `sandbox/gateway` (client) | any gateway | the gateway's backend | the backend's | first release |
| `local` | `sandbox/local` | a temp dir and an in-process agent over a pipe | **none** | unrestricted | first release, tests and development only |
| Kubernetes | `sandbox/gateway` module (server backend) | pods via `client-go` | container; the cluster's runtime | none, unrestricted | second release |
| `openshell` | `sandbox/openshell`, a nested module for its gRPC client | an OpenShell gateway | OpenShell's | none, allowlist, unrestricted | second release |

`local` is the `provider/faux` of sandboxes: it makes every test in this
PRD runnable without a container, and it is **not a sandbox**. Its
`Capabilities` say so, its constructor is `local.Unisolated()`, and its
`Create` refuses a `Spec` with `Network` other than unrestricted or with
`Resources` set, so a test cannot accidentally assert isolation it does
not have. It runs the agent in-process over a pipe pair, so the gRPC
service is exercised end to end with no container and no socket. Because
it is in-process, it applies no Landlock ruleset and its `rsyncd` is
built with `DontRestrict()` (D8): nothing `local` does may confine the
process that embeds it.

`landlock` is the provider between those two: a real boundary for files
and TCP, no boundary for anything else, and `Capabilities` that say so
(§3.6). An embedder that wants a run bounded in memory or time, or that
runs code it does not trust with the host kernel, uses `podman`; one
that wants the model kept out of its files and network while using the
host's toolchain uses `landlock`.

The OpenShell provider maps `Spec` onto an OpenShell policy (network
allowlist to `network_policies`, resources to its sandbox options) and
`Sandbox.Tools` onto an exec session through its gateway that starts the
agent at `AgentPath` inside. What OpenShell enforces at the kernel and
proxy, this module does not re-check; what `Spec` asks for and OpenShell
cannot express is refused.

## 7. Order of work

Each step is one pull request with `make check` green, and each is
usable on its own.

1. **Proto, agent, proxy tools, `local`.** The `agentkit.sandbox.v1`
   proto and its generated code (`make proto`); `cmd/agentkit-sandboxd`
   serving over stdio; `sandbox` with `Spec`, `Provider`, `Sandbox`, the
   proxy tool construction and `local`. The spec 06 conformance suite
   runs against proxy tools over `local`; the proxy tools' definitions
   are checked against `tools.All`; cancellation, `unknown_tool`, a dead
   agent and an oversized message are pinned. Code mode over proxy tools
   against `provider/faux` (§4). `Sync` and `Export` for `LocalDir` over
   the `Rsync` stream with `gokrazy/rsync` on both ends, and for `Tar`;
   both run end to end over `local`'s pipe. The D8 ruleset in the agent,
   `DontRestrict()` on every `rsyncd`, the ABI in `Hello`, and
   `Spec.RequireLandlock`.
2. **Images.** The two Containerfiles, the release workflow that
   publishes them, and the `Create` failure that names `AgentPath` and
   the `COPY --from` line.
3. **`podman`.** `podman.Args(Spec)` as a pure function with golden argv
   for each hardening flag and each refusal; the workspace volume; the
   init step for `GitRepo`; TTL on both sides; labels and `Reap`;
   `docker` through `Options.Binary`;
   the micro-VM runtime flag. Integration tests gated on the binary being
   present.
4. **`landlock`.** The provider of §3.6: the sandbox directory and
   marker, the agent as a confined child, the init-step clone as a second
   child, the refusals, TTL, `Close` and `Reap`. Tests gated on Linux
   with ABI v4; the refusals and the directory layout are tested
   everywhere.
5. **Gateway.** The `Gateway` service and the proxied `Agent` service;
   the client in the root module; the server and its Podman backend in
   the nested module; tests run client and server in one process over
   `local`, the `Rsync` stream included.
6. **Documentation and tag** (below). This is the first release.
7. **Kubernetes backend** in the gateway module.
8. **OpenShell provider**, after verifying its gRPC surface and policy
   schema against the proto definitions and writing them into the spec's
   `external_apis`.
9. **D3 (c)**: `codemode` in the agent, the `RunScript` stream, the
   host-side `code_mode` proxy.

Steps 2, 3, 4 and 5 are independent once step 1 has landed.

## 8. Acceptance criteria

Offline, in `make check`:

- For every tool `tools.All` returns, `Sandbox.Tools` over `local`
  returns a tool with an equal `Name`, `Description`, `PromptGuidelines`,
  `Builtin`, `ExecutionMode`, and byte-equal JSON for `InputSchema` and
  `OutputSchema`.
- The spec 06 conformance suite (each built-in's `Data` against its
  `OutputSchema`, on success and on each documented error) passes against
  proxy tools over `local`.
- A call cancelled from the host returns `aborted`, and the process it
  started is gone (the `tools.Run` process-group test, through the
  service).
- A message over the configured size limit, or a connection the agent
  closes mid-call, yields `sandbox_unavailable` for the in-flight call
  and for every later call on that sandbox.
- `podman.Args` for the zero `Spec` contains `--network none`,
  `--cap-drop ALL`, `--security-opt no-new-privileges`, `--read-only`,
  a tmpfs for `/tmp`, the workspace volume and no other mount, the
  `agentkit.sandbox` label, and no `-v` or `--mount` of a host path;
  `NetworkAllowlist` is refused with an error naming the provider and
  the mode; the init-step argv for a `GitRepo` has the volume, no
  `--network none`, no `Spec.Env`, and `git clone` as its command.
- A `LocalDir` sync over `local` (both rsync ends in-process, no `rsync`
  binary on `PATH`, which the test asserts by emptying `PATH`) puts every
  file the `tools` ignore engine would list into the workspace and none it
  would skip, sends hidden files, follows no symlink out of the source
  directory, and leaves `.git` behind only with `ExcludeGit`. A second
  sync after changing one file reports one transferred file in
  `SyncResult`. With `Delete`, a file removed on the host is removed in
  the workspace; without it, it stays. An `Export` to a `LocalDir` brings
  back a file the sandbox created and writes nothing outside the
  destination directory. A `Tar` sync sends no ignored file and follows
  no symlink out; an exported tar with an entry named `../x`, an absolute
  name, or a symlink-then-file-through-it sequence is refused on the
  host.
- The agent refuses an `Rsync` stream whose first frame names a path
  outside the workspace or an option the library does not know.
- After a `LocalDir` sync over `local`, the test process can still create
  a file outside the workspace and read `/sys`: no `rsyncd` this module
  builds confines its caller (D8 part one).
- `Hello` from the in-process agent reports Landlock ABI 0, and a `Spec`
  with `RequireLandlock` is refused by `local` with an error naming the
  field.
- `landlock.New` on a non-Linux host returns an error naming `podman`;
  `Create` refuses a `Spec` with `Resources` set, with `NetworkAllowlist`,
  or with `Image`, each naming the field; the sandbox directory layout
  (`workspace/`, `home/`, `tmp/`, the marker) and `Reap`'s rule (a dead
  or foreign pid in the marker) are tested with a fake agent on every
  platform.

On Linux with Landlock ABI v4 or later, skipped otherwise:

- A `landlock` sandbox answers `execute` of a write into the workspace
  with success, a write into the test's real home directory with a
  permission error, a read of a file outside the ruleset with a
  permission error, and a TCP connect to a listener the test opened with
  a refusal; `Hello` reports the ABI applied; `env` inside shows exactly
  `Spec.Env` plus `HOME`, `TMPDIR` and `PATH`, and `HOME` is the
  sandbox's own.
- `execute` of `go version` succeeds when `go` is under a system
  directory or named in `Options.ExtraRO`, and fails with a permission
  error when it is only under the test's home.
- A `GitRepo` sync clones a public repository into the workspace, and
  the agent started afterwards still refuses TCP.
- A `landlock` sandbox created with `TTL: 5s` and no `Close` has its
  process group gone and its directory removed by `Reap`; `Close` on a
  live one kills the group, children of `execute` included, and removes
  the directory.
- On a kernel reporting ABI 1 to 3 (simulated by the probe's test seam),
  `New` fails naming the ABI found and the one required.
- A `GitRepo` sync after `Tools` has been called is refused.
- A code-mode script over proxy tools, driven by `provider/faux` through
  `agentkit.New`, has one call blocked by `Config.Guard` and receives an
  error value for it; the other calls complete; every nested call's
  execution events carry the `code_mode` call as parent.
- The gateway client against an in-process gateway server over `local`
  passes the first three criteria unchanged, with the sandbox id in
  request metadata.
- `local.Unisolated().Create` refuses a `Spec` with `Resources` set or a
  `Network` other than unrestricted.
- `agentkit.New` with proxy shell tools and a nil `Guard` fails with
  `core.ErrUnguardedExecute`, as with local ones.
- The root module's direct dependencies gain exactly
  `google.golang.org/grpc` and `google.golang.org/protobuf`, both already
  in the module graph, `github.com/gokrazy/rsync`, and
  `github.com/landlock-lsm/go-landlock`, which `gokrazy/rsync` already
  requires. The allowlist test in `internal/policy` is the check, and
  `docs/DEPS.md` carries the rulings.
- `go build ./...` needs no `protoc` or `buf`; `make proto` regenerates
  and a test fails when the checked-in code is stale.

With `podman` (or `docker`) installed, skipped otherwise:

- Create with the base image, sync this repository as a `LocalDir`,
  `read_file` a known file, `write_file` a new one, `execute` `go
  version`; `Export` to a temp dir returns the new file and nothing
  `.gitignore` excludes; `Close` removes the container and the volume,
  and `podman ps -a` and `podman volume ls` show nothing with the label.
- Create with a `GitRepo` of a public repository under `NetworkNone`: the
  clone is present, and `execute` of a `curl` to any host fails inside
  the sandbox. With the base image on a kernel with Landlock, `Hello`
  reports the ABI and a `Spec` with `RequireLandlock` succeeds.
- `execute` of a write to `/var/tmp` or `/home` inside the base image
  fails with a permission error even where the container has those paths
  writable: the Landlock layer, not the read-only root, is what refuses
  it.
- Create with an image lacking the agent at `AgentPath` fails before any
  tool call, and the error names the path and the `COPY --from` line.
- A sandbox with `MemoryBytes: 256 << 20` running a process that
  allocates 1 GiB sees that process killed, and the sandbox survives.
- A sandbox created with `TTL: 5s` and no `Close` is gone after `Reap`,
  and gone without `Reap` once the agent's own TTL fires.
- A second `LocalDir` sync into the container after editing one host file
  transfers that file and not the rest (`SyncResult`'s statistics), and
  `Export` back to the host directory returns the model's edits.
- No tool result from a sandboxed run contains the host's working
  directory or home path.

## 9. What is given up, stated plainly

- **A network allowlist on plain containers.** Until a provider brings an
  egress proxy, the choice on Podman and Docker is nothing or everything.
  Most coding runs want nothing plus a package registry; those use
  OpenShell, a gateway whose network does the filtering, an image with
  dependencies pre-fetched, or the init step's clone.
- **Private repositories inside the sandbox.** Clone on the host, sync
  the folder. The credential stays on the host, which is the point.
- **Any image.** The agent has to be in the image. One `COPY --from` line
  is the price, and the error on a missing agent prints it.
- **rsync's newer protocol features.** `gokrazy/rsync` speaks protocol
  27: no xattrs, no ACLs, MD4 checksums, and a smaller user base than
  tridge rsync. A source tree needs none of the first three; the last is
  why §8 tests the transfer semantics rather than trusting them.
- **A second layer on every kernel.** Landlock is applied where the
  kernel has it and reported where it does not. A kernel without it
  (an old host, a micro-VM image built without the LSM) gets the
  container boundary alone, visibly in `Hello`, and `RequireLandlock`
  is the embedder's way to refuse that.
- **Resource limits and a kernel boundary on `landlock`.** The provider
  refuses `Resources` rather than pretend, and shares the host kernel. A
  run that must be bounded in memory or time, or that runs code the
  embedder does not trust with its kernel, belongs on `podman`.
- **`landlock` on macOS.** There is no equivalent in scope; `New` says
  so and names `podman`, whose VM is the boundary there.
- **Reconnection.** A sandbox whose agent dies is dead. The run sees
  `sandbox_unavailable` results and ends on its own bounds; the embedder
  creates a new sandbox and re-syncs. PRD 10 §9 already chose "a run that
  dies is re-run".
- **Live nested-call interception when the interpreter is in the sandbox
  without a callback** (D3 (b)): not offered. (a) now and (c) later keep
  it; nothing in between is shipped.
- **Interactive use.** No attach, no shell, no port forwarding to the
  model's processes. The provider CLIs do this for a human who needs it.
- **Transparent locality.** The embedder chooses a sandbox explicitly, by
  constructing one and taking its tools. There is no flag that makes
  `tools.All` secretly remote, because the two differ in latency, in what
  `execute` can reach, and in what a result's path means.
- **A generator-free build.** The proto's generated code is checked in,
  so `go build` stays plain; regenerating needs the generator, and a test
  catches a stale checkout.

## Dependencies

- Runtime, not Go: `podman` or `docker` on the host for the container
  provider; a Podman with `krun` or `crun-vm` for micro-VMs; `git` in the
  image for `GitRepo` (in the base image). For `landlock`: a Linux kernel
  with Landlock ABI v4 (6.7 or later, with the LSM enabled), the
  `agentkit-sandboxd` binary on the host (`go install` or beside the
  embedder's executable), and `git` on the host for `GitRepo`. No `rsync`
  anywhere.
- Go, root module: `google.golang.org/grpc` and
  `google.golang.org/protobuf` promoted from indirect to direct;
  `github.com/gokrazy/rsync` (v0.3.8 at this writing, `go 1.25.0`,
  packages `rsyncclient` on the host side and `rsyncd` in the agent). Its
  own requirements are `BurntSushi/toml`, `coreos/go-systemd`,
  `google/renameio/v2`, `google/shlex`, `mmcloughlin/md4`,
  `landlock-lsm/go-landlock` and `golang.org/x/{crypto,sync,sys}`; *spec
  time:* check with `go mod why` which of these the two library packages
  actually import (the systemd one is expected to be the daemon
  command's) and record it in the ruling.
  `github.com/landlock-lsm/go-landlock` (v0.10.1 at this writing), used by
  `cmd/agentkit-sandboxd` for the D8 ruleset; it brings
  `kernel.org/pub/linux/libs/security/libcap/psx` for the pre-v8 thread
  handling in cgo builds, and it is already required by `gokrazy/rsync`.
  A ruling each in `docs/DEPS.md`; otherwise `encoding/json`,
  `archive/tar`, `os/exec`, and this module's `core`, `tools` and
  `guard`.
- Go, nested modules: `client-go` in `sandbox/gateway` (server side, step
  6); OpenShell's gRPC client in `sandbox/openshell` (step 7). Each gets a
  ruling before it lands; neither enters the root module's graph.
- Build: a proto generator (`buf` or `protoc` with the Go plugins) for
  `make proto` only.
- PRD 08 and spec 07: the nested-call pipeline, unchanged, is what §4
  relies on.

## Documentation

- `README.md`: a "Sandboxes" section after "Code mode", with the §2
  example trimmed to ten lines and the `COPY --from` line; the sentence
  "It serves no network API and listens on no socket" becomes "The
  library listens on no socket; `agentkit-sandboxd` serves the sandbox
  service on its stdio inside a sandbox, and `agentkit-gateway`, a
  separate module, is the one network service", and the package table
  gains `sandbox`.
- `docs/architecture.md`: the package graph rows for `sandbox`, its
  providers and the generated proto package; a "Sandboxes" section with
  the service, the proxy tool guarantees, the isolation list of §3.3 as
  invariants, the init step, the D3 record, and two new invariants: "no
  code in this module confines the process that embeds it" and "the
  agent's Landlock ruleset is applied before any other work".
- `tools.Workspace`'s doc comment and `docs/configuration.md`'s "File
  tools are contained to a workspace root" paragraph: still true on a
  host; inside a sandbox the agent's ruleset contains `execute` too, and
  the text says where that holds and where it does not.
- `docs/configuration.md`: `sandbox.Spec`, `Resources`, `Network`,
  `LocalDir`, `GitRepo`, `podman.Options`, `landlock.Options` (`Root`,
  `AgentBinary`, `ExtraRO`, `ExtraRW`), `gateway.Dial`, the
  `agentkit-sandboxd` and `agentkit-gateway` flags, the two images and
  their tags, and a table of which provider honours which `Spec` field.
- `docs/DEPS.md`: rulings for `grpc`, `protobuf`, `gokrazy/rsync` and
  `go-landlock` (the latter recording the `DontRestrict()` rule and why
  `BestEffort()` is never used); rulings for the nested modules'
  dependencies as they land; a "Rejected"
  row each for the Docker and Podman Go clients and for `client-go` in
  the root module, with the reasons in D4, and one for an external
  `rsync` binary, with the reason in D7.
- `guard` package documentation: that `Restricted` is still a floor, that
  a sandbox is the boundary it was never claiming to be, and when
  `AllowAll` is reasonable.
- `examples/sandbox`: the §2 program against `local` and
  `provider/faux`, offline, with `--podman` and `--landlock` flags that
  use the real providers when the binary or the kernel allows.
