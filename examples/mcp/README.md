# mcp — borrowing another program's tools over MCP

The Model Context Protocol lets an agent borrow tools from other programs. An
MCP **server** publishes tools; an MCP **client** connects to it, lists them
and calls them. AgentKit is a client: a `Pool` of client connections adapts
every server's tools into ordinary `core.Tool`s that the loop runs like any
other. AgentKit ships no server of its own.

This example starts a small "docs" MCP server *inside the process* — built
directly on the official Go SDK (`github.com/modelcontextprotocol/go-sdk`) —
connects to it over a pipe with the shipped client, and hands its tools to a
model.

## Run it

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/mcp "which topics do you know, and what do you say about qualified names?"
go run ./examples/mcp --external "npx -y @modelcontextprotocol/server-github"   # also attach a real server
```

| Flag | Meaning |
|---|---|
| `--external "<cmd> <args>"` | Also spawn and connect a stdio MCP server (named `github`, passed `GITHUB_PERSONAL_ACCESS_TOKEN=${GITHUB_TOKEN}`). |

Everything after the flags is the prompt. `AGENTKIT_MODEL` overrides the model.

## What you'll see

The MCP half runs before any model call, so even without a credential you see
it work:

```
connected servers: [docs]
tools discovered over MCP: [docs__list_topics docs__search_docs]
direct call to docs__list_topics: ok=true {"content":[{"type":"text","text":"environment, pipe transport, protocol version, qualified names"}],"text":"environment, pipe transport, protocol version, qual…
shadowing refused: mcp: tool name collides with an existing tool: server "helper" exposes "word_count" as "word_count", which is already a native tool
error: anthropic: missing credentials: set ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN, or select Vertex AI (CLAUDE_CODE_USE_VERTEX=1) or Bedrock (CLAUDE_CODE_USE_BEDROCK=1)
```

With a credential the last line is replaced by the streamed answer, one
`[calling …]` / `[<tool>: ok, <n>ms]` pair per tool call, and a usage summary.

## Walkthrough

In `clientMode`:

1. **`mcp.ConnectionOptions`** carries `ClientInfo`, `Limits`
   (`wire.Defaults()`) and a `Warnf` logger; pass the same value to the pool
   and to hand-made connections.
2. **`pool.NativeTools`** — set *before* connecting, to the names of your own
   tools. A server tool that would land on one of those names is refused
   (`demoShadowedNameIsRefused` shows it with `DisablePrefix: true`).
3. **Connect** — `connectOverPipe` joins the SDK server's `Connect` and
   `mcp.Connect` with `mcp.NewPipeTransport`; `pool.Add(conn)` adds it. For a
   subprocess use
   `pool.Connect(ctx, mcp.ServerConfig{Name, Command, Args, Env, Timeout}, env, secrets)`
   (`connectExternal`).
4. **`pool.Tools(ctx, native)`** returns `core.Tool`s with **qualified names**,
   `<server>__<tool>`. Write allowlists and `Config.Guard` checks against
   the qualified name.
5. **`smokeCall`** executes one adapted tool directly — a cheap way to prove
   the MCP side works before paying for a model turn.
6. From there it is an ordinary agent: `agentkit.New(agentkit.Config{…})`
   with `Tools` holding the native *and* MCP tools, and
   `Policy: core.ToolPolicy{ToolNames: …}` listing both (a non-nil allowlist
   covers the whole set). `MaxTurns: 8` and `MaxCostUSD: 1.00` bound the run.

The in-process server: `docsServer()` builds an `sdk.NewServer(…)` and
`addTool` registers each tool with a raw JSON Schema and a handler that gets
its arguments decoded.

## Gotchas

- **A gate written against an unqualified name fails open** — it never
  matches, so the call goes through.
- **Subprocess servers get a reduced environment** (`env` in
  `connectExternal`), not `os.Environ()`. Credentials travel as `${VAR}`
  references resolved at spawn; an unset one is an
  `*mcp.UnresolvedVariableError` and nothing is spawned.

## Related

Package `mcp` (`Pool`, `Connect`, `ServerConfig`, `ConnectionOptions`,
`NewPipeTransport`), `wire` (`Defaults`), `agentkit` (`Config`, `New`),
`core` (`ToolPolicy`). The client is
described in [`docs/architecture.md`](../../docs/architecture.md#mcp-client).
