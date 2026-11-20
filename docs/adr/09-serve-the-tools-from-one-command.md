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
`mcpserve.ServeStdio` returns promptly when its context ends. The first does
not hold as written; the second holds only when every running call honours
its cancellation.

## Decision

**`tools` lists in serving order; an MCP session lists in the SDK's
order.** `agentkit-tools tools` writes the definitions in serving order:
the tools of `tools.For`, then `code_search`, then `code_mode`. In an `mcp`
session, `tools/list` answers with the same tools in the order the MCP Go SDK
keeps them, sorted by name (`go-sdk` v1.8.0, `mcp/features.go`). `mcpserve`
is not changed to reorder them.

**After SIGINT or SIGTERM, `mcp` waits at most 5 seconds for the session.**
When a signal arrives, `ServeStdio` cancels the running calls and closes the
session, and closing it waits for those calls to return. It returns at once
when they honour the cancellation, even on the blocking-pipe stdin a parent
process gives the command
([ADR 07](07-give-mcp-serving-call-lifetimes-of-its-own.md)). For a tool
that ignores it, the command waits as long as `call` waits for a call past
its timeout, 5 seconds, then exits 130 or 143.

**`call` always writes `text`.** It is the text an MCP session serves for
the same call: the result's `LLMText()`, then the text of each text block,
joined with a newline. `core.ToolResult.Text` does not marshal, so a relay
could not serve a session's text without it.

## Consequences

- A relay that answers `tools/list` from `agentkit-tools tools` lists the
  tools in serving order; a harness that connects over MCP sees them by
  name. The set is the same.
- A session ended by a signal leaves no running call that honours
  cancellation; one that does not is cut off when the process exits.
