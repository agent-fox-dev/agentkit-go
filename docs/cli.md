# Command-line programs

AgentKit is a library, and it ships one program: `agentkit-tools`, which
serves its tools for one directory to a harness or a relay that runs beside
it. This page is for whoever runs or integrates that command. The runnable
programs under [`examples/`](../examples) document their own flags in their
`README.md`.

## Contents

- [`agentkit-tools`](#agentkit-tools)
  - [Installing](#installing)
  - [The served tools](#the-served-tools)
  - [`agentkit-tools mcp`](#agentkit-tools-mcp)
  - [`agentkit-tools call`](#agentkit-tools-call)
  - [`agentkit-tools tools`](#agentkit-tools-tools)
  - [Flags](#flags)
  - [Exit codes](#exit-codes)

## `agentkit-tools`

`agentkit-tools` serves AgentKit's built-in tools, `code_search` and
`code_mode` for one directory, in two modes: an MCP session on stdin and
stdout, for a harness in the same sandbox (Claude Code, Pi), and a single
tool call read from a file, for a relay that can only start a process and
read its output. It serves tools; it never calls a model. Isolation is the
sandbox's: the command is meant to run inside one someone else provides
([ADR 08](adr/08-leave-sandboxes-to-the-platform-that-runs-them.md)).

```
agentkit-tools mcp   [flags]
agentkit-tools call  [flags] --request <file>
agentkit-tools tools [flags]
agentkit-tools --version
```

### Installing

The command is the nested module `cmd/agentkit-tools`, released under tags
`cmd/agentkit-tools/vX.Y.Z`:

```bash
go install github.com/agent-fox-dev/agentkit-go/cmd/agentkit-tools@latest
```

`agentkit-tools --version` prints the module version the binary was built
from, or `(devel)`.

### The served tools

Every command opens `--dir` as the workspace and serves, in this order: the
tools of `tools.For` (`read_file`, `write_file`, `edit_file`, `list_files`,
`find_files`, `search_files`, `file_outline`, `find_symbol`,
`find_references`, `execute`, `run_command`), then `code_search`, then
`code_mode` bound to exactly the other tools that are served. Names,
descriptions and schemas are the ones the packages define.

- Where zoekt does not run (windows/amd64), `code_search` is left out and
  stderr says so in one line.
- `execute` and `run_command` run with the command's own environment, reduced
  by `tools.ReducedEnv`: credentials such as `ANTHROPIC_API_KEY` are removed
  and `PATH` is kept.
- The `code_search` index lives in a temporary directory that is removed when
  the process ends, unless `--index-dir` or `AGENTKIT_INDEX_DIR` names a
  directory to keep it in: then a later process on the same directory loads
  it, revalidates it against the tree and answers without building. Two
  processes at once build it once. `find_symbol` answers from a complete
  stored index too.

### `agentkit-tools mcp`

Serves one MCP session on stdin and stdout until the client closes its end,
then exits 0. Frames are newline-delimited JSON, each held to the wire limits
(16 MiB a message); nothing but protocol frames goes to stdout, and the
command's logs go to stderr. The server's name is `agentkit-tools` and its
version the command's. The index stays open for the session: the first
`code_search` builds it, and writes through `write_file`, `edit_file` and the
shell tools invalidate it, so a later search sees them. `tools/list` answers
in the MCP SDK's order, by name ([ADR 09](adr/09-serve-the-tools-from-one-command.md)).

A harness starts it as an MCP server, for example in Claude Code's
`--mcp-config`:

```json
{"mcpServers": {"agentkit": {"command": "agentkit-tools", "args": ["mcp", "--dir", "/work/checkout"]}}}
```

- A frame that breaks the wire limits ends the session: exit 1 and one line
  on stderr, `agentkit-tools: mcp session ended: frame rejected: <rule>`,
  which never quotes the frame.
- SIGINT or SIGTERM cancels the running calls and ends the session: exit 130
  or 143, at most 5 seconds after the signal.

### `agentkit-tools call`

Makes one tool call and exits. The request file is a JSON object of at most
16 MiB:

```json
{"name": "read_file", "input": {"path": "main.go"}}
```

`name` is required and is a non-empty string; an absent `input` means `{}`.
The command writes exactly one JSON object to stdout: the result as
`core.ToolResult` marshals it, and `text`, the text an MCP session would
serve for the same call:

```json
{"ok": true, "data": {"...": "..."}, "text": "1\tpackage main\n..."}
```

| Key | Holds |
|---|---|
| `ok` | Whether the tool succeeded. |
| `data` | The tool's structured result, or `null`. |
| `error` | The error code of a failed call (`unknown_tool`, `invalid_arguments`, `blocked_by_policy`, `timeout`, or the tool's own), absent on success. |
| `detail` | The error's message, absent on success. |
| `metadata` | The tool's metadata, when it has any. |
| `text` | The text the model reads, then the text of any extra content blocks, joined with a newline. |

- The command exits 0 whenever the call ran, whatever the tool returned. A
  name that is not served (no such tool, or one `--tools` or `--read-only`
  leaves out) is `ok: false`, `error: "unknown_tool"`; an `input` that is not
  an object is `invalid_arguments`.
- A request file that is missing, unreadable, larger than 16 MiB, not a JSON
  object or without a non-empty string `name` exits 2 with one line on stderr
  and nothing on stdout.
- `--timeout` (default `10m`) bounds the call. Past it the call is cancelled
  and the result is `ok: false`, `error: "timeout"`, with a `detail` that
  names the duration, written at most 5 seconds after the deadline. A tool's
  own timeout argument still applies inside it.
- SIGINT or SIGTERM cancels the call: exit 130 or 143, nothing on stdout.
- `code_mode` works as in a session: its script's nested calls run in the
  same process.

### `agentkit-tools tools`

Writes the served tool definitions to stdout as one JSON array in serving
order, and exits 0. It is what a relay answers `tools/list` from:

```json
[{"name": "read_file", "description": "...", "input_schema": {"type": "object", "...": "..."}, "output_schema": {"...": "..."}}]
```

`input_schema` is `{"type":"object"}` for a tool that declares none;
`output_schema` is `null` for a tool that declares none.

### Flags

Flags follow the command.

| Flag | Commands | Default | Meaning |
|---|---|---|---|
| `--dir <path>` | all | the working directory | The directory to serve. One that does not exist or is not a directory exits 2 with one line naming it. |
| `--guard allow-all\|restricted` | all | `allow-all` | The guard of `execute` and `run_command`. `allow-all` is `guard.AllowAll`, since the sandbox is the boundary; `restricted` is `guard.Restricted` with its default options, which blocks every shell call (`blocked_by_policy`). Any other value exits 2. |
| `--tools a,b,c` | all | every tool | Serve only the named tools, in serving order whatever the order given. `code_mode` is served only when named, and bound to the other named tools. A name that is no tool exits 2 with a line that lists the tool names. |
| `--read-only` | all | off | Leave out `write_file`, `edit_file`, `execute` and `run_command`. With `--tools`, a tool is served when both allow it. A selection that leaves nothing exits 2. |
| `--index-dir <path>` | all | `$AGENTKIT_INDEX_DIR`, else none | Keep the `code_search` index there, so a later process on the same directory loads it instead of building ([the stored index](codesearch.md#the-stored-index)). The command creates it when missing. One that is the served directory or lies inside it exits 2 with a line saying it must be outside; one that cannot be created or written is said once on stderr, and the process keeps a temporary index. An empty value means unset. |
| `--request <file>` | `call` | none | The request file. |
| `--timeout <duration>` | `call` | `10m` | The bound on the call, a Go duration (`30s`, `10m`). One that does not parse or is not positive exits 2. |
| `--version` | alone | | Print the version and exit 0. |

### Exit codes

| Code | When |
|---|---|
| 0 | `tools` listed the tools; `call` wrote a result, whether the tool succeeded or not; `mcp` ended because the client closed its end; `--version`. |
| 1 | The tool set could not be built, or an `mcp` session ended with an error, such as a rejected frame. |
| 2 | The command line or the request file is refused: usage, `--dir`, `--guard`, `--tools`, `--read-only`, `--index-dir`, `--request`, `--timeout`. Nothing is served and nothing is written to stdout. |
| 130, 143 | SIGINT or SIGTERM ended `mcp` or `call`. |
