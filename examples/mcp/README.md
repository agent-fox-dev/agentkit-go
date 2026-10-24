# mcp: using another program's tools over MCP

The Model Context Protocol lets an agent borrow tools from other programs: an
MCP server publishes tools, and a client connects, lists them and calls them.
AgentKit's client, `mcp.Pool`, turns every connected server's tools into
ordinary `core.Tool`s that the agent runs like its own. This example starts a
small "docs" MCP server inside the process (built on the official Go SDK),
connects to it, checks the tools work, and hands them to a model. The MCP part
needs nothing; the model part needs a provider and its credential.

(To go the other way and serve AgentKit's tools to an MCP client, see
[`harness`](../harness).)

## Run it

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/mcp "which topics do you know, and what do you say about qualified names?"

# also spawn and connect a real stdio MCP server
go run ./examples/mcp --external "npx -y @modelcontextprotocol/server-github"
```

| Flag | Meaning |
|---|---|
| `--external "<cmd> <args>"` | Also spawn and connect a stdio MCP server, named `github`, passing it `GITHUB_PERSONAL_ACCESS_TOKEN=${GITHUB_TOKEN}`. |

Everything after the flags is the prompt.

`AGENTKIT_PROVIDER` (`anthropic`, `openrouter` or `ollama`), `AGENTKIT_MODEL`
(a tier or a model id, default `STANDARD`) and `AGENTKIT_EFFORT` choose the
model. Only the examples read these variables. Each provider reads its own
credential: `ANTHROPIC_API_KEY` (or another Anthropic credential),
`OPENROUTER_API_KEY`, or `OLLAMA_HOST`. See
[choosing a provider](../../docs/guide.md#choosing-a-provider).

The MCP half runs before any model call, so even without a credential you see
it work:

```
connected servers: [docs]
tools discovered over MCP: [docs__list_topics docs__search_docs]
direct call to docs__list_topics: ok=true {"content":[{"type":"text","text":"environment, pipe transport, protocol version, qualified names"}],…
shadowing refused: mcp: tool name collides with an existing tool: server "helper" exposes "word_count" as "word_count", which is already a native tool
error: anthropic: missing credentials: set ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or ANTHROPIC_OAUTH_TOKEN, …
```

With a credential, the last line is replaced by the streamed answer, a
`[calling …]` / `[<tool>: ok, <n>ms]` pair per tool call, and a summary line on
stderr.

## What it shows

- MCP tools get **qualified names**, `<server>__<tool>`. Write allowlists and
  guards against the qualified name: a rule written against `search_docs`
  never matches, so it fails open.
- `pool.NativeTools`, set before connecting, makes the pool refuse a server
  tool that would take the name of one of your own tools.
- Calling one adapted tool directly checks the MCP side before you pay for a
  model turn.
- A spawned server gets a reduced environment, not `os.Environ()`; a credential
  travels as a `${VAR}` reference resolved at spawn, and an unset one stops the
  spawn with `*mcp.UnresolvedVariableError`.

## The code that matters

```go
opts := mcp.ConnectionOptions{
	ClientInfo: mcp.Implementation{Name: "my-app", Version: "0.1.0"},
	Limits:     wire.Defaults(),
}
pool := mcp.NewPool(opts)
defer pool.Close()
pool.NativeTools = []string{"word_count"} // your own tools' names

_, err := pool.Connect(ctx, mcp.ServerConfig{
	Name:    "github",
	Command: "npx",
	Args:    []string{"-y", "@modelcontextprotocol/server-github"},
	Env:     map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"},
	Timeout: 30 * time.Second,
}, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, os.Getenv)

mcpTools, err := pool.Tools(ctx, native) // github__… core.Tools
all := append(native, mcpTools...)
agent, err := agentkit.New(agentkit.Config{
	Provider: provider, Model: model, Effort: effort,
	Toolset:  agentkit.Toolset{Tools: all, Policy: core.ToolPolicy{ToolNames: toolNames(all)}},
	MaxTurns: 8, MaxCostUSD: 1.00,
})
```

A non-nil `ToolNames` is an allowlist over the whole set, so it names your own
tools as well as the MCP ones. For an in-process server, the example joins
`mcp.Connect` and the SDK server with `mcp.NewPipeTransport` and adds the
connection with `pool.Add`.

## Further reading

- [Examples index](../README.md)
- [User guide: using tools from MCP servers](../../docs/guide.md#using-tools-from-mcp-servers)
- [Configuration: `mcp.Config`, `mcp.ServerConfig`](../../docs/configuration.md#mcp-mcpconfig-mcpserverconfig)
- [Architecture: MCP client](../../docs/architecture.md#mcp-client)
