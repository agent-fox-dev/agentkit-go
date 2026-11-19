# toolserver: AgentKit's tools served by one command

`agentkit-tools` serves AgentKit's built-in tools, `code_search` and
`code_mode` for one directory, so a harness that speaks MCP (Claude Code, Pi)
or a relay that can only start a process can use them without writing Go.
This example builds the command and drives it both ways on a sample project.
It runs offline, with no API key.

## Run it

The example is its own Go module.

```bash
cd examples/toolserver && go run .
```

It prints:

```
agentkit-tools serves 13 tools: read_file, write_file, edit_file, list_files, find_files, search_files, file_outline, find_symbol, find_references, execute, run_command, code_search, code_mode
session: code_search for needle found a.go
session: after edit_file, code_search for fresh_marker found b.go
session: closed; agentkit-tools exited 0
call: code_search for needle found a.go
call: code_mode read 3 files
call: an unknown tool is a result too: ok=false error=unknown_tool
toolserver example completed successfully
```

## What it does

1. Builds `agentkit-tools`, which this module declares as a tool
   (`tool github.com/agent-fox-dev/agentkit-go/cmd/agentkit-tools` in
   `go.mod`), into a temporary directory, and writes the sample project
   beside it.
2. Lists the served tools with `agentkit-tools tools --dir <sample>`: the
   JSON a relay answers `tools/list` from.
3. Starts `agentkit-tools mcp --dir <sample>` and connects AgentKit's MCP
   client to its stdin and stdout, as a harness does with an MCP server it
   starts: it searches, edits a file with `edit_file`, searches again and
   finds the edit, then closes the session.
4. Makes single calls with `agentkit-tools call --dir <sample> --request
   <file>`, one process each: a `code_search`, a `code_mode` script that reads
   three files at once, and a tool that does not exist, which is a result
   (`unknown_tool`) and not a failed process.

On a platform where `code_search` is not available, the example says so and
runs the rest.

## Further reading

- [`docs/cli.md`](../../docs/cli.md): every subcommand, flag and exit code of
  `agentkit-tools`.
- [`docs/guide.md`](../../docs/guide.md): serving the tools to another harness.
