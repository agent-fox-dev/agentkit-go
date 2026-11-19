# ADR 09: Serve the tools from one command

- **Status:** Accepted
- **Date:** 2026-11-19

## Context

`agentkit-tools` (the nested module `cmd/agentkit-tools`) serves the
built-in tools, `code_search` and `code_mode` for one directory: as one MCP
session on stdio (`mcp`), as one tool call read from a file (`call`), and as
a listing of the served definitions (`tools`). It is the hands AgentKit
supplies inside a sandbox someone else runs
([ADR 08](08-leave-sandboxes-to-the-platform-that-runs-them.md)). Its spec
assumed that an MCP session lists the tools in serving order and that
`mcpserve.ServeStdio` returns promptly when its context ends. Neither holds
as written for a command run as a child process.

## Decision

**`tools` lists in serving order; an MCP session lists in the SDK's
order.** `agentkit-tools tools` writes the definitions in serving order:
the tools of `tools.For`, then `code_search`, then `code_mode`. In an `mcp`
session, `tools/list` answers with the same tools in the order the MCP Go SDK
keeps them, sorted by name (`go-sdk` v1.8.0, `mcp/features.go`). `mcpserve`
is not changed to reorder them.

**After SIGINT or SIGTERM, `mcp` waits at most 5 seconds for the session.**
When a signal arrives, `ServeStdio` cancels the running calls and closes the
session. Closing `os.Stdin` does not interrupt a read pending on a stdin
that is a blocking pipe, which it is when a parent process starts the
command, so `ServeStdio` can stay blocked after the calls have ended. The
command waits for it as long as `call` waits for a call past its timeout,
5 seconds, then exits 130 or 143.

**`call` always writes `text`.** It is the text an MCP session serves for
the same call: the result's `LLMText()`, then the text of each text block,
joined with a newline. `core.ToolResult.Text` does not marshal, so a relay
could not serve a session's text without it.

## Consequences

- A relay that answers `tools/list` from `agentkit-tools tools` lists the
  tools in serving order; a harness that connects over MCP sees them by
  name. The set is the same.
- A session ended by a signal leaves no running call, though `ServeStdio`
  may still be closing when the process exits.
- `mcpserve.ServeStdio` blocking on a blocking-pipe stdin after its context
  ends is a defect of `mcpserve`, tracked on its own; this command does not
  depend on it being fixed.
