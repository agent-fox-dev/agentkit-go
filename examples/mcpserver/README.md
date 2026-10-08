# mcpserver — a standalone MCP server host

An MCP server publishes tools and resources that any MCP-speaking agent
(Claude Code, an IDE, another AgentKit program) can discover and call. AgentKit
ships the mechanism — `mcp.Server`, built on the official Go SDK — and this
program is the smallest complete host around it: construct a server, register
an inventory, and call `Run` with whichever transport the configuration
selects. A real host does exactly what `main` does, with its own tools in place
of `registerDemo`.

Two transports:

- **stdio** — newline-delimited JSON-RPC on stdin/stdout. No authentication;
  it relies on OS process isolation (the client spawned you). The default.
- **HTTP** — Streamable HTTP, bound to `127.0.0.1` only, with a **mandatory**
  API key. There is no anonymous mode to forget to turn off.

No model and no API key are involved.

## Run it

```bash
go run ./examples/mcpserver                                   # stdio (the default)
go run ./examples/mcpserver -config agentkit.toml             # whatever [mcp_server] selects
MCP_API_KEY=secret go run ./examples/mcpserver -transport http -port 8722 -api-key-env MCP_API_KEY
```

| Flag | Meaning |
|---|---|
| `-config FILE` | TOML file whose `[mcp_server]` table (`enabled`, `transport`, `port`, `api_key_env`) selects the mode. |
| `-transport stdio\|http` | Overrides the config (and enables server mode). |
| `-port N` | TCP port for HTTP mode. |
| `-api-key-env VAR` | Name of the environment variable holding the HTTP API key. The key itself never goes in a flag or file. |

Flags win over the file. With neither, it serves stdio. Exit 0 on a clean
shutdown (EOF on stdin, SIGINT/SIGTERM), 1 on a startup or transport failure.

## What you'll see

stdio, one `tools/call` at revision 2026-07-28 (keep stdin open briefly — the
server stops at end of input):

```
$ { echo '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}'; sleep 1; } | go run ./examples/mcpserver
{"jsonrpc":"2.0","id":1,"result":{"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"agentkit-mcp-server","version":"0.1.0"}},"content":[{"type":"text","text":"hello"}],"resultType":"complete"}}
```

HTTP: a request without the key is `401`; with `Authorization: Bearer <key>`
(or `X-API-Key`) the same body returns the result as an SSE `message` event.
HTTP without `-api-key-env` refuses to start:

```
mcp-server: mcp: http server mode needs api_key_env naming the variable that holds the key: ...
```

## Walkthrough

- **`main`**: flags are parsed, `loadConfig` reads the file through
  `mcp.ParseConfig` (diagnostics to stderr) and returns `cfg.Server`, flags
  override it, then `mcp.NewServer(mcp.ServerOptions{Info, Limits, Instructions})`,
  `registerDemo(srv)`, and `srv.Run(ctx, cfg, os.Getenv)` under a
  `signal.NotifyContext`. `Run` picks stdio or HTTP; the `os.Getenv` argument
  is how it reads the variable named by `api_key_env`.
- **`registerDemo`** is the part you replace:
  - `s.RegisterTool(&mcp.Tool{Name, Description, InputSchema}, handler)` —
    the handler gets decoded arguments as `map[string]any`; a returned error
    becomes a tool-error result, not a protocol error.
  - `s.AddResource` and `s.AddResourceTemplate` are the SDK's own API, which
    `*mcp.Server` embeds (imported as `sdk`). The template
    `agentkit://echo/{message}` shows a parameterised resource.
- **`main_test.go`** builds the binary and drives it as a subprocess with the
  shipped client (`mcp.NewPool` → `pool.Connect` → `ListTools`, `Call`,
  `ReadResource`). It is the pattern for testing your own host:
  `go test ./examples/mcpserver/`.

## Gotchas

- **Nothing but protocol may be written to stdout** in stdio mode. A stray
  log line corrupts the client's frame stream; log to stderr.
- HTTP binds `127.0.0.1` on purpose. Put a reverse proxy in front if it must be
  reachable from elsewhere.
- A named-but-unset or empty key variable refuses to start rather than serving
  unauthenticated.
- Inbound frames are size-, depth- and duplicate-key-checked before decoding;
  a malformed frame ends the session.

## Related

Package `mcp` (`Server`, `ServerOptions`, `ServerModeConfig`, `ParseConfig`,
`Run`, `ServeStdio`, `ListenAndServeHTTP`, `HTTPHandler`). The full wire
surface and HTTP request checks are in [`docs/api.md`](../../docs/api.md); the
`[mcp_server]` keys in [`docs/configuration.md`](../../docs/configuration.md).
To consume MCP servers from an agent, see [`mcp`](../mcp).
