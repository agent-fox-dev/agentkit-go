# Serve the tools from one command and drop the sandbox module

## Intent

AgentKit's tools, code search and code mode become usable inside any sandbox
that someone else runs, through one command that serves them for a directory.
AgentKit stops creating and managing sandboxes itself: isolation, lifetime,
credentials and file transfer belong to the platform that owns the sandbox
(Parley on OpenShell), and AgentKit supplies only the hands that work in it.

## Goals

1. One command, `agentkit-tools`, serves the built-in tools, `code_search`
   and `code_mode` for one directory, in two modes: an MCP session on stdin
   and stdout, and a single tool call read from a file.
2. A harness that speaks MCP (Claude Code, Pi) running in the same sandbox as
   the directory can list and call every served tool with no other AgentKit
   code and no Go program of its own.
3. A caller that can only start a process with arguments and read its output
   (the Parley hub, whose runtime exec carries no stdin) can list the tools
   and make any tool call.
4. A second process on the same directory answers `code_search` and
   `find_symbol` from the index the first one built, without building again,
   and sees every file change made since.
5. The repository holds no `sandbox/` tree, no gateway, no sandbox image and
   no gRPC, protobuf, rsync or go-landlock requirement in any `go.mod`.
   `make check` passes.
6. The last commit that still has the sandbox is reachable by a tag.

## Non-goals

- Creating, isolating, listing or destroying sandboxes, and moving files in
  or out of one. Parley does this (its PRD §13).
- Confining `execute` on a host without a sandbox. After this PRD the shell
  guard is again the only boundary there, as before PRD 11.
- Publishing the command as a container image or image layer. It lands in
  the CI/CD pipeline later.
- Serving MCP over the network from this command. A harness outside the
  sandbox reaches the tools through Parley's MCP route (a Parley PRD), which
  uses the single-call mode.
- Parley's side: the sandbox MCP route, `parley mcp`, and hub-side search
  over a materialised default branch. Separate PRDs in the Parley repository.
- A model loop in the command. It serves tools; it never calls a model.
- Changes to `mcpserve`'s existing API (`ServeStdio`, `ServeLoopback`), to
  the tool definitions, or to code mode's rules.
- Indexing from a git tree instead of a directory.

## Background

AgentKit today has three things an agent in a sandbox wants: the built-in
tools (`tools.For` over a `workspace.Workspace`), the `code_search` index
(`codesearch.New`, a nested module on zoekt, plugged in through
`tools.Options.Index`), and code mode (`codemode.New`, one tool that runs a
Starlark script over other tools). `mcpserve` serves a `Runner`'s tools over
MCP on stdio or a loopback port. None of this is a program: an embedder
writes Go to wire it up. `examples/harness` is the only place that wiring
exists, and it is an example.

AgentKit also has its own sandboxes (PRD 11, specs 13 to 19 and 25): the
`sandbox` module with the `agentkit-sandboxd` agent and its gRPC protocol,
proxy tools, an rsync-based sync and export, Podman, Landlock, local and
gateway providers, the `sandbox/gatewayd` server with `agentkit-gateway`,
and the `containers/agentkit-sandbox` image. It is about 15 000 lines of
production Go and as many of tests. Inside the repository only
`examples/sandbox`, `examples/gateway` and `examples/harness` import it; the
project owner confirmed nothing outside does.

Parley now owns sandboxes for the agent-fox projects. A Parley sandbox is an
OpenShell sandbox born with a fork checked out, with Parley's credentials,
network policy, quotas and checkpoints. The planned OpenShell provider of
PRD 11 was never built, and with Parley it has no purpose: the agent, or its
tools, run inside the sandbox Parley made. Two facts of that environment
shape this PRD:

- OpenShell's non-interactive exec takes no standard input (Parley's
  `internal/sandbox/runtime/openshell/client.go` refuses it). A caller
  outside the sandbox can pass arguments and files, and read output.
- The sandboxed tool set of today cannot carry an index
  (`sandbox.ToolOptions` has no `Index`), so `code_search` has never worked
  in a sandbox.

The index today lives for one process: `codesearch` writes its shards to a
run directory under `Options.TempDir` and `Index.Close` deletes it. The
first `code_search` call builds it (bounded by `MaxFiles`, `MaxBytes`,
`MaxBuildTime`, 60 s by default); later calls revalidate changed files by
size and modification time. `find_symbol` without an index builds its own
table per process (bounded at 2 s). A caller that starts one process per
tool call would pay both on every call.

The decision rule of PRD 10 applies: is this here because the model is bad
at judgment, or because we chose to own the whole stack? The tools are the
first kind. Sandboxes, once Parley exists, are the second.

## Requirements

### The command and its tool set

The command takes the directory to serve (`--dir`, default the working
directory) and opens it as the workspace. It serves, in this order: the
tools of `tools.For`, then `code_search`, then `code_mode` bound to all of
the others. Names, descriptions and schemas are the ones the packages
define.

- `--dir` that does not exist or is not a directory: the command writes one
  line to stderr naming the path and exits 2. Nothing is served.
- On a platform where `codesearch.New` returns `ErrUnsupported`, the command
  serves the other tools, without `code_search`, and says so in one line on
  stderr.
- `--guard allow-all|restricted` chooses the guard of `execute` and
  `run_command`. The default is `allow-all` (`guard.AllowAll`): the command
  is meant for a sandbox, which is the boundary. `restricted` applies
  `guard.Restricted` with its default options; a call it refuses is a tool
  result, `ok: false`, with the guard's error code. Any other value exits 2
  with a line naming the two values.
- `--tools a,b,c` serves only the named tools, in serving order whatever the
  order given. `--read-only` leaves out `write_file`, `edit_file`, `execute`
  and `run_command`. With both, a tool is served when both allow it.
  `code_mode` is bound to exactly the other tools that are served; it is
  served only when `--tools` is absent or names it, and at least one other
  tool is served. A name in `--tools` that is no tool exits 2 with a line
  naming it and listing the tool names. A selection that leaves no tool
  exits 2.
- The subprocess environment of the shell tools is the command's own
  environment as `tools.ReducedEnv` reduces it, as for every embedder.
- `--version` prints the module version and exits 0. An unknown flag or
  subcommand prints usage to stderr and exits 2.

### MCP over stdio

`agentkit-tools mcp` serves one MCP session on stdin and stdout until the client
closes its end, then exits 0. It behaves as `mcpserve.ServeStdio` does:
newline-delimited JSON frames, each held to the wire limits, nothing but
protocol frames on stdout, logs on stderr. A harness in the same sandbox
starts it as a child process.

- The index stays open for the session: the first `code_search` builds or
  loads it, and writes through `write_file`, `edit_file` and the shell tools
  invalidate it, as today.
- A frame that breaks the limits ends the session; the command exits 1 with
  one line on stderr that does not quote the frame.
- SIGINT or SIGTERM ends the session, cancels running calls, and the command
  exits with status 130 or 143.

### A single call

`agentkit-tools call --request <file>` makes one tool call and exits. The request
file is a JSON object `{"name": string, "input": object}`. The command
writes exactly one JSON object to stdout: the tool's result as
`core.ToolResult` marshals (`ok`, `data`, `error`, `detail`, `metadata`),
and for a result with extra content blocks, the text the model would read in
`text`.

- Exit 0 when the call ran, whether the tool succeeded or failed: a failing
  tool is `ok: false` with its `error` code, not a non-zero exit.
- Exit 2, nothing on stdout, one line on stderr: the request file is
  missing, unreadable, larger than 16 MiB, not a JSON object, or lacks
  `name`.
- An unknown tool name is a result, `ok: false`, `error: "unknown_tool"`,
  exit 0, so the caller relays it like any tool error.
- `--timeout <duration>` (default 10 minutes) bounds the call. Past it the
  call is cancelled and the result is `ok: false`, `error: "timeout"`, exit
  0. A tool's own timeout argument still applies inside it.
- `agentkit-tools tools` writes the served tool definitions to stdout as one JSON
  array of `{"name", "description", "input_schema", "output_schema"}` in
  serving order, and exits 0. This is what a relay answers `tools/list`
  from.
- `code_mode` works in a single call exactly as in a session; its script's
  nested calls run in the same process.

### An index that outlives the process

The command keeps the `code_search` index and the symbol data on disk, in a
directory outside the served one, so a later process on the same directory
reuses them.

- `--index-dir <path>`, or the environment variable `AGENTKIT_INDEX_DIR`
  when the flag is absent, names the index directory. There is no default
  location: without either, the index lives in a temporary directory that
  is removed when the process ends, as for a library embedder today. The
  command creates the directory when it is missing. One index directory
  holds the indexes of any number of served directories, kept apart.
- An index directory that is the served directory or lies inside it exits 2
  with a line saying it must be outside, so an index is never checkpointed
  or committed with the work.
- A process that finds a complete index loads it and revalidates: files
  whose size or modification time changed since they were indexed, new files
  and deleted files are picked up before the query is answered. The answer
  is the same as from a fresh build over the current tree.
- A process that finds no index, an index for another directory, one written
  by an incompatible version, or one it cannot read, builds a new one and
  replaces what was there. It reports nothing to the caller beyond the
  normal result.
- Two processes on the same directory at once: one builds, the other waits
  for it up to the build bound and then uses the result. Neither ever reads
  a half-written index: an index becomes visible only when complete.
- A build stopped by its bounds leaves a partial index, marked as such, and
  results carry the partial marker as today. A later process tries the
  build again.
- If the named index directory cannot be created or written, `code_search` still
  works for that process with an index in a temporary directory, and the
  command says so once on stderr.
- `find_symbol` answers from the stored index when it is complete, so a
  single call does not rebuild a symbol table.
- Target: on a generated tree of 10 000 source files, a `code_search`
  single call that finds a complete index and no changed file takes at most
  one fifth of the wall time of the call that built the index, on the same
  machine.
- The library keeps today's behaviour by default: an embedder that does not
  ask for a persistent directory gets a run directory that `Close` deletes.

### Removing the sandbox

After this PRD the repository has none of: the `sandbox` module and its
providers, agent, protocols and internal packages; `sandbox/gatewayd` and
`agentkit-gateway`; `containers/agentkit-sandbox` and its publishing
workflow; `examples/sandbox`, `examples/gateway` and `examples/harness`;
the sandbox, gateway, container and `proto` make targets; the sandbox
entries of `docs/DEPS.md`, `docs/cli.md`, `docs/api.md`,
`docs/architecture.md`, `docs/guide.md`, `docs/releasing.md`, the READMEs
and `docs/generated/`.

- Doc comments elsewhere that describe sandbox behaviour (`workspace`,
  `guard`, `mcpserve`, `tools`) say what is true afterwards.
- ADR 05 and ADR 06 are marked superseded by a new ADR that records this
  decision; they are not deleted.
- `docs/cli.md` documents the new command: both modes, every flag, exit
  codes.
- A new example replaces `examples/harness`: it starts the command on a
  sample directory and makes calls in both modes, offline.
- Before the removal commit, the commit it follows is tagged
  as an ordinary release: the next version, with the root, `codesearch/`,
  `sandbox/` and `mcpserve/` tags at that commit as `docs/releasing.md`
  describes. After the removal, `docs/releasing.md` names that version as
  the last one with sandboxes.

### End-to-end flows

1. **Harness in a sandbox.** Parley creates a sandbox with a fork checked
   out and starts Claude Code with an MCP configuration that runs
   `agentkit-tools mcp --dir <checkout>`. Claude Code lists the tools, calls
   `code_search` (the index is built and stored), edits a file with
   `edit_file`, and searches again: the edit is found.
2. **Relay from outside.** The Parley hub writes a request file into the
   sandbox, execs `agentkit-tools call --dir <checkout> --request <file>`, and
   reads the result from stdout. It does this twice with `code_search`; the
   second process loads the stored index and does not build.
3. **Script in one call.** A single call to `code_mode` runs a script that
   searches, reads three files concurrently and returns a summary; the
   result holds only what the script printed and returned.

## Design Decisions

1. **Delete the sandbox module whole, in one cut** (owner's decision).
   Nothing outside the repository uses it and confined local runs are not
   needed. No Landlock library is kept.
2. **The command lives in agentkit-go, not Parley** (owner's decision), so
   it works in any sandbox and Parley's build is not involved.
3. **One process per call for the outside relay** (decided after checking):
   OpenShell's exec carries no stdin, so a held-open session from outside is
   not possible. This is why the index must persist.
4. **MCP for the in-sandbox mode.** PRD 11's D2 chose gRPC over MCP to keep
   schema property order for AgentKit's own driver. That driver no longer
   sits outside a sandbox; the consumers are third-party harnesses, which
   speak MCP.
5. **Constraint: the root module's dependency graph does not grow.** The
   command needs `codesearch` (zoekt) and `mcpserve`; it is built from a
   nested module, as they are.
6. **The index directory has no default location** (owner's decision). The
   caller that wants reuse names it; Parley's route always does.
7. **The shell guard defaults to allow-all** (owner's decision), because
   the command's place is inside a sandbox. A bare-host user passes
   `--guard restricted`.
8. **Tool selection is the command's, by `--tools` and `--read-only`**
   (owner's decision), so a relay does not have to filter.
9. **The last build with sandboxes is an ordinary release, not a special
   tag**: Go resolves nested modules only from version tags, so a pinned
   consumer needs one.
10. **A split is expected**: the command with its two modes, the persistent
   index, and the removal are three scopes; the removal depends on neither.

## Dependencies

- PRD 11 (`docs/prd/11-run-tools-in-an-isolated-sandbox.md`) and PRD 13
  (`13-authenticate-gateway-tokens-with-a-callback.md`): superseded.
- Spec 25 `gateway_authenticator` (active, all tasks done): its code is
  removed. Archived specs 13 to 19: their code is removed.
- ADR 05 and ADR 06: superseded by the ADR this work writes.
- Archived specs 03 (indexed code search), 08 (code mode), 21 (toolset
  runner), 23 (MCP serve): built on, unchanged in contract.
- Parley (`github.com/agent-fox-dev/parley`, PRD §13): the consumer. Its
  MCP route and image are its own PRDs.

## Glossary

- **Tool server**: the command this PRD adds.
- **Session mode**: `agentkit-tools mcp`, one MCP session on stdio.
- **Single call**: `agentkit-tools call`, one tool call per process.
- **Served directory**: the directory given with `--dir`, opened as the
  workspace.
- **Index directory**: where the persistent `code_search` index and symbol
  data are kept, outside the served directory.
