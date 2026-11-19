---
spec_id: "32"
spec_name: "tool_server_command"
title: "Serve the tools for one directory from one command"
status: "active"
created_at: "2026-11-19T15:39:09Z"
intent_hash: "1fb0ae07aeaf5d0c0e7de505759c1867ed47719258939f7b650a94d93564468c"
contract_hash:
  requirements.json: "efb5ab19151172ba34aa60a8b92cb654f6ccb35f5601f0232c0da02a4f6b2640"
  tasks.json: "f53f9fee913d4e1e8e2dac042a3c850682bc33a109f6cf8ac65535b73be47ad6"
schema_version: 3
source: "docs/prd/15-serve-the-tools-from-one-command-and-drop-the-sandbox.md"
---
# Serve the tools for one directory from one command

## Intent

AgentKit's tools, code search and code mode become usable inside any sandbox
that someone else runs, through one command that serves them for a directory:
as an MCP session for a harness that runs beside it, and as a single tool call
for a caller that can only start a process and read its output. Nobody has to
write Go to put AgentKit's hands to work.

## Goals

1. One command, `agentkit-tools`, serves the built-in tools, `code_search`
   and `code_mode` for one directory, in two modes: an MCP session on stdin
   and stdout (`mcp`), and a single tool call read from a file (`call`), with
   the served definitions listed by `tools`.
2. A harness that speaks MCP (Claude Code, Pi) running beside the directory
   can list and call every served tool with no other AgentKit code and no Go
   program of its own.
3. A caller that can only start a process with arguments and read its output
   (the Parley hub, whose runtime exec carries no stdin) can list the tools
   and make any tool call, and reads the same text a session would serve.
4. The root module's dependency graph does not grow: the command is its own
   nested module, `cmd/agentkit-tools`, and `make check` covers it.
5. An offline example starts the command on a sample directory and makes
   calls in both modes, and `docs/cli.md` documents both modes, every flag
   and the exit codes.

## Non-goals

- The index that outlives the process: `--index-dir`, `AGENTKIT_INDEX_DIR`,
  reuse across processes, the build lock, `find_symbol` from a stored index
  and the wall-time target. That is the third scope of PRD 15, its own
  package (`persistent_code_index`). Here the index lives in a temporary
  directory that is removed when the process ends, as for a library
  embedder today.
- Removing the sandbox module: spec 31 (`sandbox_removal`).
- Publishing the command as a container image or image layer. It lands in
  the CI/CD pipeline later.
- Serving MCP over the network from this command. A harness outside the
  sandbox reaches the tools through Parley's MCP route (a Parley PRD), which
  uses the single-call mode.
- Parley's side: the sandbox MCP route, `parley mcp`, and hub-side search.
- A model loop in the command. It serves tools; it never calls a model.
- Changes to `mcpserve`'s API (`New`, `ServeStdio`, `ServeLoopback`), to the
  tool definitions, or to code mode's rules.
- Indexing from a git tree instead of a directory.

## Background

AgentKit has three things an agent working on a checkout wants: the built-in
tools (`tools.For` over a `workspace.Workspace`: `read_file`, `write_file`,
`edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`,
`find_symbol`, `find_references`, `execute` and `run_command`, in that
order), the `code_search` index (`codesearch.New`, a nested module on zoekt,
plugged in through `tools.Options.Index`, whose tools `tools.For` appends
after the built-ins), and code mode (`codemode.New`, one tool that runs a
Starlark script over the tools it binds). `agentkit.NewRunner` validates a
`Toolset` (tools plus a guard, required when a shell tool is reachable) into
a `Runner`, whose `Call` returns every outcome as a `core.ToolResult` with an
error code (`unknown_tool`, `invalid_arguments`, `blocked_by_policy`,
`handler_error`, `panic`, `aborted`). `mcpserve` (its own module) serves a
`Runner`'s tools over MCP: `ServeStdio` holds every inbound frame to
`wire.Defaults()` (16 MiB a message), writes nothing but protocol frames to
stdout, and serves each result as `core.ToolResult.LLMText()` followed by
each text block of the result.

None of this is a program: an embedder writes Go to wire it up.
`examples/harness` was the only place that wiring existed, as an example, and
spec 31 removes it with the sandbox. `workspace.Open` does not fail on a root
that does not exist, so a command has to check its directory itself.
`codesearch.New` returns `codesearch.ErrUnsupported` on windows/amd64.
`guard.AllowAll` never blocks; `guard.Restricted(guard.Options{})` blocks
every shell call. `tools.ReducedEnv` strips credentials from an environment
and keeps `PATH`; it is the default subprocess environment of the shell
tools.

Two facts of the target environment, Parley on OpenShell, shape this
package: OpenShell's non-interactive exec takes no standard input, so a
caller outside the sandbox passes arguments and files and reads output; and
the sandboxed tool set of today has never carried an index, so `code_search`
has never worked in a sandbox.

The repository's modules are listed in `MODULES` in the `Makefile`; after
spec 31 they are `.`, `codesearch`, `mcpserve` and `examples/indexedsearch`.
Tests use `internal/testutil` (file trees, `GoRun`, `Environ`, JSON helpers);
examples are their own modules, run with `go run .` and tested through
`testutil.GoRun`.

## Requirements

### The command and its tool set

`agentkit-tools <mcp|call|tools> [flags]`. Every subcommand takes `--dir`
(default the working directory), `--guard`, `--tools` and `--read-only`;
`call` also takes `--request` and `--timeout`. The command opens `--dir` as
the workspace and serves, in this order: the tools of `tools.For`, then
`code_search`, then `code_mode` bound to all of the others. Names,
descriptions and schemas are the ones the packages define.

- `--dir` that does not exist or is not a directory: one line on stderr
  naming the path, exit 2, nothing on stdout, nothing served.
- Where `codesearch.New` returns `ErrUnsupported`, the command serves the
  other tools, without `code_search`, and says so in one line on stderr.
- The subprocess environment of the shell tools is the command's own
  environment as `tools.ReducedEnv` reduces it, as for every embedder.
- `agentkit-tools --version` prints the version of the `cmd/agentkit-tools`
  module as the binary's build information records it, or `(devel)`, and
  exits 0. No subcommand, an unknown subcommand or an unknown flag prints
  usage to stderr and exits 2.

### The guard and the tool selection

- `--guard allow-all|restricted` chooses the guard of `execute` and
  `run_command`. The default is `allow-all` (`guard.AllowAll`): the command
  is meant for a sandbox, which is the boundary. `restricted` applies
  `guard.Restricted` with its default options; a call it refuses is a tool
  result, `ok: false`, `error: "blocked_by_policy"`. Any other value exits 2
  with a line naming the two values.
- `--tools a,b,c` serves only the named tools, in serving order whatever the
  order given. `--read-only` leaves out `write_file`, `edit_file`, `execute`
  and `run_command`. With both, a tool is served when both allow it.
- `code_mode` is bound to exactly the other tools that are served; it is
  served only when `--tools` is absent or names it, and at least one other
  tool is served.
- A name in `--tools` that is no tool exits 2 with a line naming it and
  listing the tool names. A selection that leaves no tool exits 2.

### MCP over stdio

`agentkit-tools mcp` serves one MCP session on stdin and stdout until the
client closes its end, then exits 0. It behaves as `mcpserve.ServeStdio`
does: newline-delimited JSON frames, each held to the wire limits, nothing
but protocol frames on stdout, logs on stderr. A harness in the same sandbox
starts it as a child process.

- The index stays open for the session: the first `code_search` builds it,
  and writes through `write_file`, `edit_file` and the shell tools
  invalidate it, so a later search sees them.
- A frame that breaks the limits ends the session; the command exits 1 with
  one line on stderr that names the broken rule and does not quote the
  frame.
- SIGINT or SIGTERM ends the session, cancels running calls, and the command
  exits with status 130 or 143.

### A single call

`agentkit-tools call --request <file>` makes one tool call and exits. The
request file is a JSON object `{"name": string, "input": object}`. The
command writes exactly one JSON object to stdout: the tool's result as
`core.ToolResult` marshals (`ok`, `data`, `error`, `detail`, `metadata`),
and `text`, the text a session would serve for the same call.

- Exit 0 when the call ran, whether the tool succeeded or failed: a failing
  tool is `ok: false` with its `error` code, not a non-zero exit.
- Exit 2, nothing on stdout, one line on stderr: the request file is
  missing, unreadable, larger than 16 MiB, not a JSON object, or lacks a
  non-empty string `name`.
- An `input` that is absent means `{}`; one that is present and not an
  object is the runner's `invalid_arguments` result, exit 0.
- An unknown tool name, or one the selection leaves out, is a result,
  `ok: false`, `error: "unknown_tool"`, exit 0, so the caller relays it like
  any tool error.
- `--timeout <duration>` (default 10 minutes) bounds the call. Past it the
  call is cancelled and the result is `ok: false`, `error: "timeout"`, exit
  0, written within 5 seconds of the deadline whether or not the tool has
  returned. A tool's own timeout argument still applies inside it. A
  `--timeout` that does not parse or is not positive exits 2.
- SIGINT or SIGTERM cancels the call; the command exits 130 or 143 with
  nothing on stdout.
- `code_mode` works in a single call exactly as in a session; its script's
  nested calls run in the same process.

### The tool listing

`agentkit-tools tools` writes the served tool definitions to stdout as one
JSON array of `{"name", "description", "input_schema", "output_schema"}` in
serving order, and exits 0. `input_schema` is `{"type":"object"}` for a tool
that declares none, as `mcpserve` serves it; `output_schema` is `null` for a
tool that declares none. The selection flags apply. This is what a relay
answers `tools/list` from.

### The module, the example and the docs

- The command is the nested module `cmd/agentkit-tools`
  (`github.com/agent-fox-dev/agentkit-go/cmd/agentkit-tools`), with
  `replace` directives to the root, `codesearch` and `mcpserve` like the
  other nested modules. `MODULES` lists it, so `make check` builds, vets,
  lints and tests it. The root `go.mod` gains no requirement.
- A new example, `examples/toolserver`, its own module in `MODULES`, replaces
  `examples/harness`: it builds the command, starts it on a sample directory,
  lists the tools and makes calls in both modes, offline, with no API key,
  and exits 0. `examples/README.md` lists it.
- `docs/cli.md` documents the command: both modes, the listing, every flag,
  the request and result formats and the exit codes. `README.md`,
  `docs/guide.md`, `docs/architecture.md`, `docs/development.md`,
  `docs/DEPS.md` and `docs/releasing.md` say what changed for each
  (the command, how to serve the tools to a harness, the module layout, the
  modules list, the new module's dependencies, and its tag prefix
  `cmd/agentkit-tools/`). `make doc` documents the command's package.

### End-to-end flows

1. **Harness beside the directory.** A harness starts
   `agentkit-tools mcp --dir <checkout>` as its MCP server. It lists the
   tools, calls `code_search` (the index is built), edits a file with
   `edit_file`, and searches again: the edit is found. It closes stdin; the
   command exits 0.
2. **Relay from outside.** A relay writes a request file, runs
   `agentkit-tools call --dir <checkout> --request <file>`, and reads the
   result from stdout. It does this for `code_search` and for an unknown
   tool, and answers its own `tools/list` from `agentkit-tools tools`.
3. **Script in one call.** A single call to `code_mode` runs a script that
   searches, reads three files concurrently and returns a summary; the
   result holds only what the script printed and returned.
4. **A maintainer runs the example and the checks.** `go run .` in
   `examples/toolserver` prints the listing and both modes' results and
   exits 0; `make check` runs the command's and the example's tests.

## Design Decisions

1. **The command lives in agentkit-go, not Parley** (owner's decision, PRD 15
   D2), so it works in any sandbox and Parley's build is not involved.
2. **The module is `cmd/agentkit-tools`, a main package and nothing else**
   (owner's decision, this run): no new public API; installed with
   `go install github.com/agent-fox-dev/agentkit-go/cmd/agentkit-tools@<version>`;
   released under tags `cmd/agentkit-tools/vX.Y.Z`.
3. **`call` always prints `text`** (owner's decision, this run, refining the
   PRD's "for a result with extra content blocks"): `ToolResult.Text` does
   not marshal, so without it a relay could not serve what a session serves.
   `text` is `LLMText()` followed by the text of each text block, joined
   with `"\n"`; with no blocks it is `LLMText()` alone.
4. **MCP for the in-sandbox mode** (PRD 15 D4): the consumers are
   third-party harnesses, which speak MCP.
5. **The root module's dependency graph does not grow** (PRD 15 D5): the
   command needs `codesearch` (zoekt) and `mcpserve`, so it is built from a
   nested module, as they are.
6. **The shell guard defaults to allow-all** (owner's decision, PRD 15 D7),
   because the command's place is inside a sandbox. A bare-host user passes
   `--guard restricted`.
7. **Tool selection is the command's, by `--tools` and `--read-only`**
   (owner's decision, PRD 15 D8), so a relay does not have to filter.
8. **The index lives for one process in this package** (split of PRD 15,
   owner's choice of order): a temporary directory removed at exit. The
   `persistent_code_index` package adds the index directory.
9. **Flags follow the subcommand** (decided from the PRD's examples):
   `agentkit-tools call --dir <d> --request <f>`. `--request` and
   `--timeout` belong to `call` alone; elsewhere they are unknown flags.
10. **The version is the command module's** (decided from `mcpserve`'s
    `Options.Version` rule): `--version` and the MCP server's version are
    the `cmd/agentkit-tools` module's version from the build information, or
    `(devel)`. The MCP server's name is `agentkit-tools`.
11. **A session's error line names the rule, never the error's text**
    (decided from `mcpserve.ServeStdio`, whose error names the keys of the
    rejected frame): `agentkit-tools: mcp session ended: frame rejected:
    <rule>` from `wire.Error.Rule`, or `agentkit-tools: mcp session ended:
    session error`.
12. **A request's `name` must be a non-empty string; an absent `input` is
    `{}`** (decided from `Runner.Call`, which treats empty input as `{}` and
    reports a non-object input as `invalid_arguments`).
13. **The timeout result is written within 5 seconds of the deadline**
    (decided here): a tool that ignores cancellation must not hold a relay
    past its bound.
14. **Signals in `call` exit 130 or 143 with nothing on stdout** (decided
    here, as in `mcp`): a cancelled call has no result to relay.
15. **The example is `examples/toolserver`** (decided here), its own module,
    which builds the command into a temporary directory and drives it.
16. **Profile: full.** The command is verified end to end through a real
    MCP client, real processes and the example.
17. **Depends on spec 31**: the command lands after the removal, into the
    `MODULES`, `docs/cli.md` and `examples/README.md` it leaves.

## Dependencies

- Spec 31 `sandbox_removal` (active): removes `examples/harness` and the
  sandbox docs this package's example and docs replace.
- PRD 15 (`docs/prd/15-serve-the-tools-from-one-command-and-drop-the-sandbox.md`):
  the source; this package is its second scope.
- Archived specs 03 (indexed code search), 08 (code mode), 21 (toolset
  runner), 23 (MCP serve): built on, unchanged in contract.
- ADR 07 (`docs/adr/07-give-mcp-serving-call-lifetimes-of-its-own.md`): the
  call lifetimes `mcpserve` gives a stdio session.
- Parley (`github.com/agent-fox-dev/parley`, PRD §13): the consumer.

## Verified External API

Read in this repository's sources and in the module cache:

- `agentkit.NewRunner(ts Toolset) (*Runner, error)`;
  `(*Runner).Call(ctx, name string, input json.RawMessage) core.ToolResult`;
  `(*Runner).Tools() []core.Tool`; `Toolset{Tools, Policy, Guard, After, Events}`.
- `tools.For(ws *workspace.Workspace, opts tools.Options) ([]core.Tool, error)`;
  `tools.Options{SpillDir, DisableSpill, Env, Symbols, Index}`;
  `tools.ReducedEnv(base []string, extraPrefixes ...string) []string`;
  `tools.Index` (`Symbols`, `Tools`, `Invalidate`, `Close`).
- `workspace.Open(root string, opts workspace.Options) (*workspace.Workspace, error)`.
- `codesearch.New(ws *workspace.Workspace, opts codesearch.Options) (tools.Index, error)`;
  `codesearch.ErrUnsupported`; `codesearch.Options.TempDir`.
- `codemode.New(tools []core.Tool, opts codemode.Options) (core.Tool, codemode.BuildInfo, error)`;
  `codemode.FormatResultText(printed string, returnValue any) string`.
- `guard.AllowAll(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision`;
  `guard.Restricted(o guard.Options) core.BeforeToolCall`.
- `mcpserve.New(r *agentkit.Runner, opts mcpserve.Options) (*mcpserve.Server, error)`;
  `mcpserve.Options{Name, Version, Instructions, Logger}`;
  `(*mcpserve.Server).ServeStdio(ctx context.Context) error`.
- `wire.Error{Rule, Path, Msg, Err}`; `wire.Defaults()`.
- `core.ToolResult{OK, Data, Error, Detail, Terminate, Metadata, Blocks, Text}`
  with JSON keys `ok`, `data`, `error`, `detail`, `metadata`;
  `(core.ToolResult).LLMText() string`; `core.TextBlock`; `core.Tool{Name,
  Description, InputSchema, OutputSchema, ...}`.
- `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0 (for the end-to-end
  test): `NewClient(impl *Implementation, options *ClientOptions) *Client`;
  `(*Client).Connect(ctx, t Transport, opts *ClientSessionOptions) (*ClientSession, error)`;
  `CommandTransport{Command *exec.Cmd}`; `(*ClientSession).ListTools`,
  `CallTool`, `Close`.

## Glossary

- **Tool server**: the `agentkit-tools` command.
- **Session mode**: `agentkit-tools mcp`, one MCP session on stdio.
- **Single call**: `agentkit-tools call`, one tool call per process.
- **Served directory**: the directory given with `--dir`, opened as the
  workspace.
- **Serving order**: the tools of `tools.For`, then `code_search`, then
  `code_mode`.
