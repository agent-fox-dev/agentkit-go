# mcp — the Model Context Protocol, in both directions

The [Model Context Protocol](https://modelcontextprotocol.io) is how agents
borrow tools from other programs. An MCP **server** publishes tools; an MCP
**client** lists them and calls them. AgentKit's `mcp` package, built on the
official Go SDK (`github.com/modelcontextprotocol/go-sdk`), does both: a
`Pool` of client connections adapts every server's tools into ordinary
`core.Tool`s, and a `Server` exposes your own tools to somebody else's agent.
The SDK negotiates the protocol revision — it speaks 2026-07-28 to a migrated
peer and falls back to the earlier `initialize` handshake for one that has not.

This example is both. Client mode starts a small "docs" MCP server *inside the
process*, connects to it over a pipe, and hands its tools to a model. Server
mode publishes the same tools on stdin/stdout.

## Run it

**Client mode** — the MCP half needs no key; the final model run does:

```bash
go run ./examples/mcp                                   # MCP part runs, then stops at the credential check
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/mcp "which topics do you know, and what do you say about qualified names?"
go run ./examples/mcp --external "npx -y @modelcontextprotocol/server-github"   # also attach a real server
```

**Server mode** — no key. It reads JSON-RPC frames on stdin and answers on
stdout:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}' \
  | go run ./examples/mcp --serve
```

| Flag | Meaning |
|---|---|
| `--serve` | Expose the docs tools as an MCP server on stdin/stdout. |
| `--external "<cmd> <args>"` | In client mode, also spawn and connect a stdio MCP server (named `github`, passed `GITHUB_PERSONAL_ACCESS_TOKEN=${GITHUB_TOKEN}`). |

## What you'll see

Client mode with no key:

```
connected servers: [docs]
tools discovered over MCP: [docs__list_topics docs__search_docs]
[audit server] server=(local) tool=list_topics args=sha256:4413… error=false 0ms
[audit mcp] server=docs tool=list_topics args=sha256:4413… error=false 0ms
direct call to docs__list_topics: ok=true {"content":[{"type":"text","text":"audit, pipe transport, protocol version, qualified names"}]}
shadowing refused: mcp: tool name collides with an existing tool: server "helper" exposes "word_count" as "word_count", which is already a native tool
error: no credential for vendor "anthropic": set one of ANTHROPIC_API_KEY, ...
```

Server mode answers the request with the two tools:

```
[mcp-server] serving MCP on stdin/stdout
{"jsonrpc":"2.0","id":1,"result":{…,"tools":[{…"name":"list_topics"},{…"name":"search_docs"}]}}
```

## Walkthrough

Client side, in `clientMode`:

1. **`mcp.ConnectionOptions`** carries `ClientInfo`, `Limits`
   (`mcp.DefaultLimits()`), a `Warnf` logger and an `Audit` hook; pass the
   same value to the pool and to hand-made connections.
2. **`pool.NativeTools`** — set *before* connecting, to the names of your own
   tools. A server tool that would land on one of those names is refused
   (`demoShadowedNameIsRefused` shows it with `DisablePrefix: true`).
3. **Connect** — `connectOverPipe` joins `srv.Connect` and `mcp.Connect` with
   `mcp.NewPipeTransport`; `pool.Add(conn)` adds it. For a subprocess use
   `pool.Connect(ctx, mcp.ServerConfig{Name, Command, Args, Env, Timeout}, env, secrets)`
   (`connectExternal`).
4. **`pool.Tools(ctx, native)`** returns `core.Tool`s with **qualified names**,
   `<server>__<tool>`. Write allowlists, `BeforeToolCall` checks and plugin
   hooks against the qualified name.
5. **`smokeCall`** executes one adapted tool directly — a cheap way to prove
   the MCP side works before paying for a model turn.
6. From there it is an ordinary agent: `cfg.ToolPolicy.ToolNames` lists native
   *and* MCP tools (a non-nil allowlist covers the whole set).

Server side: `docsServer()` builds an `mcp.NewServer(mcp.ServerOptions{…})`
and calls `srv.RegisterTool(&mcp.Tool{…}, handler)` with a raw JSON Schema;
`serveMode` calls `srv.ServeStdio(ctx)`.

## Gotchas

- **A gate written against an unqualified name fails open** — it never
  matches, so the call goes through.
- **Subprocess servers get a reduced environment** (`env` in
  `connectExternal`), not `os.Environ()`. Credentials travel as `${VAR}`
  references resolved at spawn; an unset one is an
  `*mcp.UnresolvedVariableError` and nothing is spawned.
- **Audit events hash the arguments** (`ArgumentsHash`); arguments themselves
  never reach the audit trail.
- In server mode **only protocol frames may go to stdout**; log to stderr.
- The stdio server stops at end of input, after answering every request it
  has already read.
- For HTTP serving (API-key auth, bound to 127.0.0.1) see
  [`mcpserver`](../mcpserver).

## Related

Package `mcp` (`Pool`, `Connect`, `ServerConfig`, `Server`, `NewPipeTransport`,
`DefaultLimits`), `core` (`AuditEvent`, `ToolPolicy`). The server's wire
surface is documented in [`docs/api.md`](../../docs/api.md).
