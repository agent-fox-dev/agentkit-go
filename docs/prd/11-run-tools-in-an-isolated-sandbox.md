# Run tools and scripts in an isolated sandbox

Status: **draft, under discussion**. Nothing here is implemented. The
decisions in §0 are open; each names a recommendation so the discussion
starts from a position rather than a list. Once they are settled this
document becomes `.specs/13_sandbox/prd.md` and the spec work begins.

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
sandbox, and so does every code-mode script bound to those tools. The
model, the driver and the embedder's process see only tool results. The
host's files, environment, credentials and network are not reachable from
inside, and nothing inside is reachable from the host except through the
sandbox's own API.

Sandboxes come from **providers**: Podman or Docker containers, Podman
micro-VMs, Kubernetes pods, NVIDIA OpenShell. A provider either runs on the
embedder's machine (embedded) or is reached through a **gateway** on a
remote host (cloud, build farm, another server), which creates, starts,
stops and controls sandboxes there.

The decision rule of PRD 10 still applies: AgentKit supplies mechanism the
model cannot be trusted to supply (containment), not a platform. The
sandbox is hands, not driver. `agentkit.Config`, `New`, the loop, the
batch executor and the nested-call pipeline do not change.

## 0. Open decisions

These are the questions that change what gets built. Each has a
recommendation; the rest of the document is written as if the
recommendations hold, and says so where it matters.

### D1. How a workspace is "bound to a sandbox": proxy tools, not a virtual filesystem

**Options.**

- **(a) Proxy tools.** The sandbox runs an *agent* process inside it that
  owns a real `tools.Workspace` at `/workspace` and the real `tools.All`
  over it. The host gets `[]core.Tool` with the same names, descriptions,
  schemas and guidelines as `tools.All`, whose `Execute` forwards the call
  to the agent and returns its `core.ToolResult`. `tools` does not change.
- **(b) Abstract the filesystem.** Give `tools.Workspace` an `fs`-like
  interface, implement it over a remote protocol, and run the tool logic on
  the host against remote files.

**Recommendation: (a).** Every file tool calls `os.*` directly today; the
shell tools, the symbol table, the Go type-checker behind
`find_references` and the ignore engine all read the local disk. (b) is a
rewrite of `tools` and still cannot cover `execute`. (a) changes nothing in
`tools`, keeps every conformance test valid for the in-sandbox copy, and
keeps the shell tools honest: the process they start is in the sandbox.
The cost is that one copy of the `tools` package, as a small binary, has
to be inside the sandbox (D5).

### D2. The wire between host and sandbox: a small agentkit protocol, not MCP

**Options.**

- **(a) MCP.** The in-sandbox agent is an MCP server; the host imports its
  tools through `mcp.Pool`, which already yields `core.Tool`s.
- **(b) A small agentkit protocol**, JSON frames over the provider's exec
  channel (stdio) or over HTTP through a gateway, carrying
  `core.ToolResult` verbatim, plus sync, export, cancel and ping.

**Recommendation: (b).** MCP reuses the pool, but: the host would take the
tools' schemas from the server's `tools/list`, which loses the authored
property order (`docs/errata/12_core_schema_guard.md`), so code mode's
generated description and the provider's cached tool prefix could differ
between a local and a sandboxed run; `CallToolResult` has no slot for
`ToolMetadata` (truncation, spill path, exit code, outcome), which would
have to be smuggled through `structuredContent`; and PRD 10 deleted the MCP
server from this module on purpose. With (b) the host builds the proxy
tools from the `tools` package's *own* definitions, byte-identical
schemas included, and only `Execute` is remote. The `wire` package
(strict, bounded JSON frames) was kept "as `mcp`'s dependency and deleted
the day `mcp` stops needing it"; this is its second consumer. An embedder
that wants MCP can still serve the sandbox's tools over MCP from its own
process, as agent-fox's `af` server does today.

### D3. Where a code-mode script runs

This is the one place the requirement as stated ("the script must be
executed inside the sandbox") and the invariants of PRD 08 pull in
different directions, so it needs a decision rather than a default.

**What the sandbox adds for a script.** A Starlark script already has no
file, network, environment, process or clock access; its only effects are
its tool calls. With D1 those calls run inside the sandbox whether the
interpreter is on the host or not. What the interpreter's location changes
is: **memory** (PRD 08 non-goal: "A memory limit on a script. The chosen
runtime cannot enforce one"; a script building a huge list grows the host
process), **CPU accounting** (steps bound it, cgroups would bound it
better), **containment of an interpreter fault**, and **latency** (one
round trip per tool call when the interpreter is remote from the tools).

**What moving it costs.** PRD 08's load-bearing rule is that every nested
call runs the *host's* `Config.Guard`, `Config.After` and execution events
("a script is never a way around the embedder's policy"). `Config.Guard`
is an arbitrary Go function; it cannot be shipped into the sandbox.

**Options.**

- **(a) Interpreter on the host, tools in the sandbox.** `codemode.New`
  over the proxy tools, unchanged. Every nested call goes through the host
  pipeline exactly as today. Memory of the interpreter stays on the host.
- **(b) Interpreter in the sandbox, policy shipped as data.** The sandbox
  agent runs `codemode` over its local tools. The host sends a
  `guard.Options` with the script; the agent enforces it with
  `guard.Restricted` (which is why `guard.Check` was made a pure function
  on data). The host reconstructs the nested-call record from the result's
  `calls_completed` ledger after the fact. Lost: a custom `Guard` function,
  per-nested-call `After`, and live nested events.
- **(c) Interpreter in the sandbox, authorization called back to the
  host.** As (b), but before each nested call the agent asks the host
  (same connection, reverse direction) and the host runs its real
  pipeline: `Guard`, then tells the agent to run the call, then `After`
  and the events. Keeps every PRD 08 invariant and isolates the
  interpreter. Costs a bidirectional protocol and one round trip per
  nested call, the same round trip (a) pays.

**Recommendation: (a) for the first release, (c) as the follow-up, (b)
never.** (a) is zero new mechanism and keeps the invariants that spec 07
and spec 08 pin; what the sandbox is for, isolating *effects*, it
delivers in full. (c) is the right end state if interpreter memory on the
host turns out to matter, and it can be added without changing what a
script sees. (b) trades away the policy pipeline for nothing (c) does not
also give. **This needs your call**: if "the script runs in the sandbox"
is a requirement of the first release rather than the end state, (c) is
the first step and the work in §7 grows by one protocol direction.

### D4. Providers: drive the container CLIs, and reach Kubernetes only through a gateway

**Options** for the local container providers: the Docker Engine or
Podman REST API through their Go client modules, or the `podman` /
`docker` command line through `tools.RunArgv`.

**Recommendation: the command line.** The Go clients are large dependency
graphs for a module whose dependencies are an allowlist, and the two CLIs
are the stable, documented surface. `podman run` and `docker run` take the
whole hardening set as flags (§3.3), and a pure function
`podman.Args(Spec) []string` is testable offline the way `guard.Check`
is. The micro-VM variant is the same provider with a runtime flag
(`--runtime krun`, libkrun, or `crun-vm`; verify at spec time which the
installed Podman offers), not a second provider.

**Kubernetes: gateway only.** `client-go` is heavier still, and a pod in a
cluster is reached from outside the cluster through something that is, in
every respect that matters here, a gateway. The gateway (§5) deploys in the
cluster and creates pods; the host speaks only the gateway protocol. This
is also how OpenShell works (Helm-deployed gateway, gRPC control plane).

### D5. How the sandbox agent gets into the sandbox

The in-sandbox agent (`agentkit-sandboxd`, §3.1) must be present in the
image. **Options:** the image is built with it (a published base image,
`FROM` it); or the provider copies a statically linked binary in after
`create`, before `start`, so any image works.

**Recommendation: both, injection by default.** The provider injects
`Spec.AgentBinary` (default: a copy built with `CGO_ENABLED=0`, found on
`PATH` or next to the embedder's executable) into the container at
`/usr/local/libexec/agentkit-sandboxd`, read-only. An image that already
has one at that path, such as the published `agentkit-sandbox` image, is
used as is. Without cgo the in-sandbox `outline` falls back to `go/ast`
for Go and `none` for the rest; the published image is built with cgo and
carries the tree-sitter grammars. The injection is a `podman cp` or a tar
over `exec`; it is the same mechanism sync uses.

### D6. Where the gateway lives

**Options:** in this module under `cmd/`; a nested module like
`codesearch`; or a separate repository.

**Recommendation: a nested module, `sandbox/gateway/`, with its server in
`cmd/agentkit-gateway` inside that module.** The *client* (a `Provider`
that talks to a gateway) is small, standard library only, and belongs in
the root module beside the other providers. The server is a long-running
network service with its own dependency and operational story
(authentication, TLS, the Kubernetes API), and PRD 10's rule that "nothing
in this module listens on a socket" should stay true of the root module.
The nested module keeps the server versioned with the protocol it speaks
without putting its dependencies in the root module's graph.

### D7. Sync sources in the first release

**Recommendation:** a local directory (walked with the `tools` ignore
engine, hidden files included, ignored files excluded unless asked) and a
git repository *cloned on the host* and sent the same way. Cloning inside
the sandbox needs the git host on the network allowlist and, for a private
repository, a credential inside the sandbox; neither is a good default.
In-sandbox clone can be added as a `Source` later for public repositories.

## Goals

1. An embedder can create a sandbox from a provider, populate its one
   workspace from a local directory or a git repository, and get a
   `[]core.Tool` that is a drop-in for `tools.All` over that workspace:
   same names, descriptions, input and output schemas and guidelines, so
   `Config.Policy`, `Config.Guard`, `guard.Restricted`, `prompt.Build`
   and `codemode.New` work on it unchanged.
2. A tool call on a sandboxed workspace runs inside the sandbox. The
   result the model and the embedder receive is a `core.ToolResult`;
   neither can read, write or address anything inside the sandbox except
   by making another tool call. The embedder can additionally export the
   workspace, as an explicit action on the sandbox, never through a tool.
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
   construction as a pure function, the protocol with both ends in one
   test, the gateway client against an in-process server. Container
   integration tests run when `podman` or `docker` is installed and are
   skipped, not failed, otherwise.
8. `agentkit.Config`, `New`, `core`, `tools` and `codemode` are unchanged
   in their exported surface. A sandbox is a different source of
   `core.Tool`s, nothing else.

## Non-goals

- **A general container platform.** No image building, no registries, no
  volumes beyond the workspace, no port publishing, no compose. An
  embedder that needs those uses the provider's own tooling and hands
  AgentKit the image name.
- **Persistent or shared sandboxes.** A sandbox holds one workspace, is
  created for a run or a pipeline phase, and is destroyed. No snapshot,
  restore, pause, migration or reuse across embedders. The durable state
  is what the embedder exports (a patch, a tar) and parks in git, as PRD
  10 §9 already assumes.
- **More than one workspace per sandbox**, or a workspace spanning
  sandboxes.
- **A network allowlist on the Docker and Podman providers in the first
  release.** They offer `none` and `unrestricted`. An allowlist needs an
  egress proxy; OpenShell has one, and a provider that has it declares
  it. A spec that asks for an allowlist from a provider without one is
  refused (goal 4), not approximated.
- **Implementing an OCI runtime, a hypervisor or a Kubernetes client.**
  The providers drive what is installed.
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
  wire: `OK`, `Data`, `Error`, `Detail`, `Metadata`, `Text`. Only `Text`
  and `Blocks` lack JSON tags; the protocol defines its own frame.
- `guard.Check(argv, Options) Decision` is a pure function on data, so a
  policy can be evaluated wherever the data is (D3 (b) and (c)).
- `codemode.New(tools, opts)` binds any `[]core.Tool`; the proxy tools
  are one.
- `wire` parses untrusted JSON with bounds before allocation and rejects
  duplicate keys, and has frame readers; the protocol frames go through
  it on both ends.
- `mcp` shows how this module already runs a child in its own process
  group with exactly the configured environment, kills it as a group,
  respawns it and forwards its stderr. `podman exec` is such a child.
- The ignore engine and `tools.Walk` decide what a local-directory sync
  sends.

What does not exist: any notion of a tool running elsewhere, a provider
abstraction, a lifecycle, or a protocol between two agentkit processes.

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
definitions when the spec is written; nothing here depends on their
exact names.

## 1. Vocabulary

| Term | Meaning |
|---|---|
| **Sandbox** | One isolated execution environment with one workspace, created from a `Spec` by a `Provider`, destroyed on `Close` or at its TTL. |
| **Workspace** | The one directory inside a sandbox that tools operate on, `/workspace` unless the spec says otherwise. Populated by a sync; never a mount of a host path. |
| **Provider** | What creates sandboxes: `podman`, `docker` (same code), `gateway` (a client to a remote gateway), `openshell`, and `local` (no isolation, tests only). |
| **Sandbox agent** (`agentkit-sandboxd`) | The AgentKit process inside the sandbox. It owns the workspace, runs `tools.All` over it, and speaks the sandbox protocol on its stdio or on a port the provider forwards. |
| **Sandbox protocol** | The frames between the host and the agent: call a tool, cancel a call, sync a tar in, export a tar out, ping. |
| **Gateway** | A service on a remote host that implements the provider operations over HTTP and relays the sandbox protocol to the agent in each sandbox it manages. |
| **Proxy tool** | A `core.Tool` on the host whose definition comes from `tools` and whose `Execute` runs the call in a sandbox. |

## 2. The host-side API

```go
package sandbox

type Spec struct {
    Image     string            // OCI image; the provider's default when empty
    Workdir   string            // the workspace path inside; default "/workspace"
    Env       map[string]string // the sandbox's whole environment; nothing is inherited
    Resources Resources         // zero fields take the provider's defaults
    Network   Network           // zero value: NetworkNone
    TTL       time.Duration     // destroyed at TTL whatever happens; default 1h, never unbounded
    Labels    map[string]string // forwarded to the backend for the embedder's own bookkeeping
    AgentBinary string          // host path of agentkit-sandboxd to inject (D5); "" means find it
    Provider  map[string]any    // provider-specific options, validated by the provider
}

type Resources struct {
    CPUs        float64 // e.g. 2
    MemoryBytes int64
    PIDs        int
    DiskBytes   int64   // the workspace's writable size, where the backend can bound it
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
    // Sync populates (or replaces) the workspace from src. It is the only
    // way bytes enter the sandbox other than through a tool call.
    Sync(ctx context.Context, src Source) (SyncResult, error)
    // Tools is the built-in tool set over the sandbox's workspace, with
    // tools.All's definitions and a remote Execute. opts selects the
    // same things tools.Options does where they apply (Symbols, Index on
    // the agent side; SpillDir inside the sandbox).
    Tools(ctx context.Context, opts ToolOptions) ([]core.Tool, error)
    // Export writes the workspace, or a subtree of it, as a tar stream.
    // It is the embedder's action, never a tool's.
    Export(ctx context.Context, path string, w io.Writer) error
    // Close destroys the sandbox. Idempotent. A sandbox is also destroyed
    // at its TTL and when the agent process ends.
    Close(ctx context.Context) error
}

// Source is what Sync reads: LocalDir{Path, Ignore tools.IgnoreOptions,
// IncludeIgnored bool} or GitRepo{URL, Ref, Depth, Auth} (cloned on the host,
// D7), or Tar{io.Reader} for an embedder that has its own packaging.
type Source interface{ ... }
```

How it composes with the rest of the module, which is the point:

```go
p, err := podman.New(podman.Options{})            // or gateway.Dial(url, token)
sb, err := p.Create(ctx, sandbox.Spec{Image: "ghcr.io/agent-fox-dev/agentkit-sandbox:0.6",
    Resources: sandbox.Resources{CPUs: 2, MemoryBytes: 4 << 30}, TTL: 30 * time.Minute})
defer sb.Close(ctx)
_, err = sb.Sync(ctx, sandbox.LocalDir{Path: "."})
ts, err := sb.Tools(ctx, sandbox.ToolOptions{})     // a drop-in for tools.All
cm, _, err := codemode.New(subset(ts, "read_file", "search_files", "execute"), codemode.Options{})
agent, err := agentkit.New(agentkit.Config{
    Client: client, Model: "claude-opus-5-5",
    Tools:  append(ts, cm),
    Guard:  guard.Restricted(guard.Options{AllowedPrograms: []string{"go", "git", "make"}}),
})
res, err := agent.Run(ctx, "Make the tests pass.")
err = sb.Export(ctx, ".", patchWriter)              // the deliverable leaves through the embedder
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
- The host's context cancels the call: a `cancel` frame reaches the agent,
  which kills the process group as `tools.Run` does today, and the result
  is the same `aborted` result a local call gets.
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

A command in this module. It takes the workspace path and the tool
options as flags, builds `tools.All` over `tools.NewWorkspace(workdir)`
with `Env` set to its own (explicit) environment, and serves the protocol
on stdin/stdout, or on a TCP port when started by a gateway. It is the
only process the provider starts; everything else inside is a child of a
tool call.

It has no model, no provider client, no driver: it is `tools`, `wire`,
`guard` (for D3 (b)/(c) later) and the protocol, about the size of
`mcp/client.go`. It must build with `CGO_ENABLED=0` for injection (D5).

### 3.2 The protocol

JSON frames, each a single object, length-prefixed, read through `wire`
with `wire.Defaults()` bounds on both ends. One connection, concurrent
requests, correlated by `id`:

| Frame | Direction | Payload |
|---|---|---|
| `hello` | both | protocol version, agent version, workspace path, the tool names the agent serves |
| `call` | host → agent | `id`, `tool`, `input` (raw JSON), `deadline` |
| `result` | agent → host | `id`, the `ToolResult` fields (`ok`, `data`, `error`, `detail`, `metadata`, `text`) |
| `cancel` | host → agent | `id` |
| `sync` | host → agent | `id`, `path`, `replace bool`, then a tar stream of `size` bytes |
| `export` | host → agent | `id`, `path`; answered by a `result` and a tar stream |
| `ping` / `pong` | both | liveness; a gateway uses it to detect a dead agent |

Rules: a frame over the limit, a duplicate key or an unknown frame type
closes the connection. A `call` for a tool the agent does not serve is a
`result` with `unknown_tool`. The agent runs each `call` on its own
goroutine; the host's batch executor, not the agent, decides what is
sequential. A tar stream is validated on extraction, both directions: no
absolute names, no `..`, no entry that would write through a symlink out
of the target, no device nodes; on the host the exported tar is treated as
untrusted (a hostile repository's build step wrote it).

The version in `hello` is the compatibility rule: a host and an agent from
different minor versions of this module work together when the protocol
version matches, so a published image does not have to be rebuilt for
every release.

### 3.3 Isolation, concretely

A provider must deliver all of these or refuse the `Create`. Each is a
requirement with a test; for the container providers the test is on the
argv `podman.Args(spec)` produces, offline.

- **Filesystem.** No bind mounts from the host. The root filesystem is
  read-only; the workspace and `/tmp` are writable, the workspace bounded
  by `Resources.DiskBytes` where the backend can (`--storage-opt size=` on
  Podman with a supporting driver; refused otherwise, per goal 4).
- **Environment.** `Spec.Env` is the entire environment of the agent and
  of every process it starts. The provider adds `PATH`, `HOME`, `LANG`,
  `TMPDIR` and `TERM` if the spec does not. Nothing from the host
  environment is inherited; `tools.ReducedEnv` is not involved because
  there is nothing to reduce.
- **Credentials.** The provider's own credentials (a registry login, a
  gateway token, a cloud identity) never enter the sandbox. A git
  credential used by `GitRepo` is used on the host (D7).
- **Network.** `NetworkNone` is `--network none`. `NetworkUnrestricted`
  is the backend's default network. `NetworkAllowlist` is refused by the
  container providers in the first release and honoured by OpenShell.
- **Privilege.** `--cap-drop ALL`, `--security-opt no-new-privileges`,
  rootless where the backend supports it (`--userns` as configured), a
  non-root user inside, and the backend's default seccomp profile. The
  micro-VM runtime adds a kernel boundary on top.
- **Resources.** `--cpus`, `--memory` (with swap equal to memory, so
  swap cannot be used to exceed it), `--pids-limit`.
- **Lifetime.** `TTL` is enforced twice: the provider schedules a destroy
  at `TTL` on the host, and the agent exits at `TTL` on its own so the
  container ends if the host is gone. Every sandbox carries the label
  `agentkit.sandbox=<id>` plus the embedder's `Labels`, and
  `Provider.Reap` destroys labelled containers no live value owns.
- **No way in but the protocol, no way out but a result.** The agent
  listens on its stdio (an `exec` channel the provider owns) or, under a
  gateway, on a port only the gateway reaches. A tool result is capped as
  it is today (50 KB per result, spill inside the sandbox); the protocol
  carries that and `Metadata`, nothing else.

### 3.4 What the model experiences

Nothing new. The tools have the same names and descriptions. Paths are
workspace-relative. A spill path in `Metadata` names a file inside the
sandbox, which the model can reach with `execute` as it can a `/tmp`
spill file today, and cannot with `read_file`, as today. The system prompt
gains no sandbox text by default; an embedder that wants the model to
know it is in a container (so it does not try to open a browser) says so
in `Config.System`.

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
`code_mode` whose `Execute` ships the script, and the protocol gains two
frames, `authorize` (agent → host, before each nested call) and
`authorized` (host → agent, the decision and possibly rewritten
arguments). The host-side `code_mode` proxy runs the real pipeline on each
`authorize`, so `Guard`, `After` and the events are the host's. `parallel`
inside a script sends several `authorize` frames at once. That design is
recorded here so (a) is built in a way that does not preclude it: the
proxy tools' `Execute` and the protocol's `call` are the pieces (c)
reuses.

## 5. The gateway

For a sandbox on another host. The gateway is a service that implements
`Provider` over HTTP and relays the sandbox protocol to each agent it
started.

```
POST   /v1/sandboxes                 Spec  -> {id}            Create
GET    /v1/sandboxes                       -> [{id, spec, created, expires}]
DELETE /v1/sandboxes/{id}                                     Close
GET    /v1/sandboxes/{id}/protocol         WebSocket or HTTP/2 stream carrying the §3.2 frames
GET    /v1/capabilities                    -> Capabilities
```

- **Client.** `gateway.Dial(url, token) Provider`, standard library only,
  in the root module. A `Sandbox` from it is a connection to
  `/sandboxes/{id}/protocol`; `Sync`, `Tools`, `Export` and `Close` are
  the same frames and the same HTTP calls whoever is on the other end.
  The gateway client is how a remote OpenShell deployment, a Kubernetes
  cluster and a plain remote Podman host all look the same to an
  embedder (goal 5).
- **Server.** `cmd/agentkit-gateway` in the nested `sandbox/gateway`
  module (D6). It holds one *backend* provider, the same `Provider`
  interface, driving Podman or Docker on its own host or pods in its
  cluster, and enforces `TTL` and `Reap` for everything it created.
  Authentication is a bearer token in the first release; TLS is the
  deployer's (a reverse proxy or the server's own certificate flags).
- **Kubernetes backend.** Lives in the gateway module, so `client-go`
  stays out of the root module (D4). A pod per sandbox: one container
  from `Spec.Image` with the agent injected by an init container or
  present in the image, resource requests and limits from `Resources`, a
  `NetworkPolicy` for `NetworkNone` (refused if the cluster's CNI does not
  enforce them, which the gateway checks once at start), an
  `activeDeadlineSeconds` for `TTL`, and the agent reached through the
  API server's exec or port-forward stream.
- **One hop of trust.** The gateway holds the backend's credentials and
  the host holds the gateway's token. The sandbox holds neither.

## 6. Providers

| Provider | Package | Backend | Isolation | Network modes | Status |
|---|---|---|---|---|---|
| `podman` | `sandbox/podman` | `podman` CLI; `docker` CLI with `Options.Binary: "docker"` | container; micro-VM with `Options.Runtime` (`krun` or `crun-vm`) | none, unrestricted | first release |
| `gateway` | `sandbox/gateway` (client) | any gateway | the gateway's backend | the backend's | first release |
| `local` | `sandbox/local` | a temp dir and an in-process agent over a pipe | **none** | unrestricted | first release, tests and development only |
| Kubernetes | `sandbox/gateway` module (server backend) | pods via `client-go` | container; the cluster's runtime | none, unrestricted | second release |
| `openshell` | `sandbox/openshell`, a nested module for its gRPC client | an OpenShell gateway | OpenShell's | none, allowlist, unrestricted | second release |

`local` is the `provider/faux` of sandboxes: it makes every test in this
PRD runnable without a container, and it is **not a sandbox**. Its
`Capabilities` say so, its constructor is `local.Unisolated()`, and its
`Create` refuses a `Spec` with `Network` other than unrestricted or with
`Resources` set, so a test cannot accidentally assert isolation it does
not have.

The OpenShell provider maps `Spec` onto an OpenShell policy (network
allowlist to `network_policies`, resources to its sandbox options) and
`Sandbox.Tools` onto an exec session through its gateway that starts
`agentkit-sandboxd` inside. What OpenShell enforces at the kernel and
proxy, this module does not re-check; what `Spec` asks for and OpenShell
cannot express is refused.

## 7. Order of work

Each step is one pull request with `make check` green, and each is
usable on its own.

1. **Protocol, agent, proxy tools, `local`.** `sandbox/protocol` on
   `wire`; `cmd/agentkit-sandboxd`; `sandbox` with `Spec`, `Provider`,
   `Sandbox`, the proxy tool construction and `local`. The spec 06
   conformance suite runs against proxy tools over `local`; the proxy
   tools' definitions are checked against `tools.All`; cancellation,
   `unknown_tool`, a dead agent and a frame over the limit are pinned.
   Code mode over proxy tools against `provider/faux` (§4).
2. **Sync and export.** `LocalDir` with the ignore engine, `GitRepo` by
   host-side clone, `Tar`; strict extraction both ways; `Export`.
3. **`podman`.** `podman.Args(Spec)` as a pure function with golden
   argv for each hardening flag and each refusal; injection of the agent;
   TTL on both sides; labels and `Reap`; `docker` through `Options.Binary`;
   the micro-VM runtime flag. Integration tests gated on the binary being
   present.
4. **Gateway.** The HTTP API, the client in the root module, the server
   and its Podman backend in the nested module; tests run client and
   server in one process.
5. **Documentation and tag** (below). This is the first release.
6. **Kubernetes backend** in the gateway module.
7. **OpenShell provider**, after verifying its gRPC surface and policy
   schema against the proto definitions and writing them into the spec's
   `external_apis`.
8. **D3 (c)**, if and when wanted: `codemode` in the agent, the
   `authorize` frames, the host-side `code_mode` proxy.

Steps 2, 3 and 4 are independent once step 1 has landed.

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
  protocol).
- A frame larger than `wire.Defaults()` allows, or with a duplicate key,
  closes the connection, and the in-flight call returns
  `sandbox_unavailable`.
- `podman.Args` for the zero `Spec` contains `--network none`,
  `--cap-drop ALL`, `--security-opt no-new-privileges`, `--read-only`,
  a tmpfs for `/tmp`, a non-root `--user`, the `agentkit.sandbox` label,
  and no `-v` or `--mount` of a host path; `NetworkAllowlist` is refused
  with an error naming the provider and the mode.
- A `LocalDir` sync of a directory with a `.gitignore` sends no ignored
  file, follows no symlink out of the directory, and sends hidden files;
  an exported tar with an entry named `../x`, an absolute name, or a
  symlink-then-file-through-it sequence is refused on the host.
- A code-mode script over proxy tools, driven by `provider/faux` through
  `agentkit.New`, has one call blocked by `Config.Guard` and receives an
  error value for it; the other calls complete; every nested call's
  execution events carry the `code_mode` call as parent.
- The gateway client against an in-process gateway server over `local`
  passes the first three criteria unchanged.
- `local.Unisolated().Create` refuses a `Spec` with `Resources` set or a
  `Network` other than unrestricted.
- `agentkit.New` with proxy shell tools and a nil `Guard` fails with
  `core.ErrUnguardedExecute`, as with local ones.
- The root module's direct dependencies gain nothing: `sandbox`,
  `sandbox/podman`, `sandbox/gateway` (client) and `sandbox/local` are
  standard library plus this module. The allowlist test in
  `internal/policy` is the check.

With `podman` (or `docker`) installed, skipped otherwise:

- Create, sync this repository, `read_file` a known file, `write_file` a
  new one, `execute` `go version`; `Export` returns the new file; `Close`
  removes the container, and `podman ps -a` shows nothing with the label.
- Under `NetworkNone`, `execute` of a `curl` to any host fails inside the
  sandbox.
- A sandbox with `MemoryBytes: 256 << 20` running a process that
  allocates 1 GiB sees that process killed, and the sandbox survives.
- A sandbox created with `TTL: 5s` and no `Close` is gone after `Reap`,
  and gone without `Reap` once the agent's own TTL fires.
- No tool result from a sandboxed run contains the host's working
  directory or home path.

## 9. What is given up, stated plainly

- **A network allowlist on plain containers.** Until a provider brings an
  egress proxy, the choice on Podman and Docker is nothing or everything.
  Most coding runs want nothing plus a package registry; those use
  OpenShell, a gateway whose network does the filtering, or an image with
  dependencies pre-fetched.
- **Reconnection.** A sandbox whose agent dies is dead. The run sees
  `sandbox_unavailable` results and ends on its own bounds; the embedder
  creates a new sandbox and re-syncs. PRD 10 §9 already chose "a run that
  dies is re-run".
- **Live nested-call interception when the interpreter is in the sandbox**
  (D3 (b)): not offered. (a) now and (c) later keep it; nothing in between
  is shipped.
- **Interactive use.** No attach, no shell, no port forwarding to the
  model's processes. The provider CLIs do this for a human who needs it.
- **Transparent locality.** The embedder chooses a sandbox explicitly, by
  constructing one and taking its tools. There is no flag that makes
  `tools.All` secretly remote, because the two differ in latency, in what
  `execute` can reach, and in what a result's path means.

## Dependencies

- Runtime, not Go: `podman` or `docker` on the host for the container
  provider; a Podman with `krun` or `crun-vm` for micro-VMs; `git` on the
  host for `GitRepo`.
- Go, root module: none new. `sandbox/*` uses `encoding/json`,
  `archive/tar`, `net/http`, `os/exec`, and this module's `core`, `tools`,
  `wire` and `guard`.
- Go, nested modules: `client-go` in `sandbox/gateway` (server side, step
  6); OpenShell's gRPC client and `google.golang.org/grpc` in
  `sandbox/openshell` (step 7). Each gets a ruling in `docs/DEPS.md`
  before it lands; neither enters the root module's graph.
- PRD 08 and spec 07: the nested-call pipeline, unchanged, is what §4
  relies on.

## Documentation

- `README.md`: a "Sandboxes" section after "Code mode", with the §2
  example trimmed to ten lines; the sentence "It serves no network API
  and listens on no socket" becomes "The library listens on no socket;
  `agentkit-sandboxd` serves the sandbox protocol on its stdio inside a
  sandbox, and `agentkit-gateway`, a separate module, is the one network
  service", and the package table gains `sandbox`.
- `docs/architecture.md`: the package graph rows for `sandbox` and its
  providers; a "Sandboxes" section with the protocol, the proxy tool
  guarantees, the isolation list of §3.3 as invariants, and the D3
  record.
- `docs/configuration.md`: `sandbox.Spec`, `Resources`, `Network`,
  `podman.Options`, `gateway.Dial`, the `agentkit-sandboxd` and
  `agentkit-gateway` flags.
- `docs/DEPS.md`: rulings for the nested modules' dependencies as they
  land; a "Rejected" row each for the Docker and Podman Go clients and for
  `client-go` in the root module, with the reasons in D4.
- `guard` package documentation: that `Restricted` is still a floor, that
  a sandbox is the boundary it was never claiming to be, and when
  `AllowAll` is reasonable.
- `examples/sandbox`: the §2 program against `local` and
  `provider/faux`, offline, with a `--podman` flag that uses the real
  provider when the binary is present.
