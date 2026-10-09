# Network API

AgentKit is a library and exposes no network surface unless the embedding
application starts one. The only inbound surface it can start is the **MCP
server**, in `mcp/`. This file documents it, and the outbound MCP client at the
end.

The protocol is implemented by the official Go SDK,
[`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
(v1.8). It speaks revision `2026-07-28` (no handshake; version and
capabilities in every request's `params._meta`) and **negotiates** down to
`2025-11-25`, `2025-06-18`, `2025-03-26` and `2024-11-05` (the `initialize`
handshake) for a peer that has not migrated, as server and as client. Method
semantics, error codes, pagination cursors and RFC 6570 resource templates are
the SDK's. `mcp.Server` embeds the SDK's `*mcp.Server`; what AgentKit adds is
described below.

## Starting it

Off by default. Nothing starts unless the host builds an `mcp.Server` and calls
one of:

| Call | Transport |
|---|---|
| `Server.ServeStdio(ctx)` | Newline-delimited JSON-RPC over this process's stdin/stdout. No authentication: relies on OS process isolation. Nothing but protocol may be written to stdout; log to stderr. |
| `Server.ListenAndServeHTTP(ctx, addr, HTTPOptions)` | Streamable HTTP, described below. Shuts down gracefully, waiting up to 5 s for in-flight requests. |
| `Server.HTTPHandler(HTTPOptions)` | The same as an `http.Handler`, to mount yourself. |
| `Server.Run(ctx, ServerModeConfig, lookupEnv)` | Whichever mode `[mcp_server]` selects ([`configuration.md`](configuration.md)). Disabled config returns nil without listening. HTTP mode binds `127.0.0.1:<port>`. |
| `Server.Connect(ctx, transport, nil)` | The SDK's own entry point, for any other transport (`mcp.NewPipeTransport` joins a client and server in one process). |

`examples/mcpserver` is a complete host ([`cli.md`](cli.md)).

## Registering content

- `RegisterTool(*mcp.Tool, ToolHandler)`: the handler receives the arguments
  decoded into a `map[string]any` (numbers as `json.Number`), and a returned
  error becomes a tool-error result (`isError: true`) rather than a JSON-RPC
  error. A nil input schema defaults to `{"type":"object"}`.
- The SDK's own `AddTool`, `AddResource`, `AddResourceTemplate`, `AddPrompt`
  and `Remove*` are available on the embedded server; a raw `AddTool` handler
  sees the whole request, including multi-round-trip `InputResponses`.

Registration may happen at any time; connected clients that subscribed are
notified. `tools.listChanged` is always advertised. List results default to
`cacheScope: private` (a tool list may be caller-specific).

Every `tools/call`, however the tool was registered:

- runs under a per-session bound of `mcp.MaxConcurrentHandlers` (64) concurrent
  handlers (`subscriptions/listen` is exempt);
- turns a panicking handler into a tool-error result;
- has its content capped at 50,000 characters across the whole result
  (`mcp.CapContent`; runes for text, encoded size for other blocks, which are
  never sliced);
- emits an audit event (`ServerOptions.Audit`) with the tool name and a hash of
  the arguments, except for an input-required (multi-round-trip) result.

On stdio, every inbound frame is bounded and checked strictly before the SDK
decodes it (REQ-SEC-11: size, depth, container and node bounds, duplicate keys
rejected); a malformed frame ends the session.

## HTTP endpoint

The SDK's Streamable HTTP handler in stateless mode, behind AgentKit's
middleware. A single endpoint; the handler does not inspect the path.

### Request checks, in order

| # | Check | Failure |
|---|---|---|
| 1 | API key in `Authorization` (`Bearer <key>` or the bare key) or `X-API-Key`; compared in constant time. `HTTPOptions.Headers` replaces the header list. | `401`, `WWW-Authenticate: Bearer realm="mcp"` |
| 2 | `Origin`: a request with no `Origin` passes; one with an `Origin` must match `HTTPOptions.AllowedOrigins` (`*` allows all). Default allowlist is empty. | `403` |
| 3 | `POST` body read, capped at the smaller of `HTTPOptions.MaxBodyBytes` and `ServerOptions.Limits.MaxMessageBytes` (16 MiB default). | `413`, JSON-RPC `-32600`, `id: null` |
| 4 | Body checked strictly (duplicate keys, depth and node bounds). | `400`, JSON-RPC `-32700`, `id: null` |
| 5 | Everything else — methods (`POST`; `GET`/`DELETE` are `405` in stateless mode), `Accept`, the `Mcp-Protocol-Version`/`Mcp-Method`/`Mcp-Name` routing headers, DNS-rebinding protection for localhost — is the SDK's. | SDK status and JSON-RPC error |

The key is mandatory: `HTTPHandler` returns `mcp.ErrNoAPIKey` for an empty one.
A request's handler is cancelled when its HTTP request ends.

## Programmatic Workspace reference search

Embedders can query references programmatically without model execution through the exported `Workspace.References` method:

```go
type ReferenceOptions struct {
	Path         string // Subdirectory filter ("" for entire workspace)
	IncludeTests bool   // Whether to include test files (default false in struct, tool defaults to true)
	MaxResults   int    // 0 or negative defaults to 30; capped at 100
}

type ReferenceSite struct {
	Path       string       // Slash-separated workspace-relative path
	Line       int          // 1-based line number
	Column     int          // 1-based byte column
	Confidence string       // "resolved", "lexical", or "text"
	Enclosing  outline.Decl // Enclosing declaration, or Kind "file" at top-level
	Source     string       // Trimmed source line, max 200 bytes, no control chars
}

type ReferenceResult struct {
	Target          outline.Decl
	Sites           []ReferenceSite
	Backend         string
	Partial         bool
	PackagesChecked int
	Errors          int
	Truncated       bool
}

func (ws *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)
```

`Workspace.References` enforces workspace containment, validates subdirectories, and executes exact Go type resolution and multi-language outline attribution with the same ranking and truncation as the `find_references` tool. Every site's `Enclosing` is the innermost declaration of the file's outline whose line range spans the site, or `Kind: "file"` at top level. The pass is bounded by the default `SymbolOptions` (50 000 files, 2 s); when a bound is reached it returns the sites found so far with `Partial` set and a nil error. Unlike the tool it keeps no cache between calls. `PackagesChecked` counts the Go package type-checks the pass used (a package with test files is checked a second time with them) and `Errors` counts the parse and type errors they collected, most of them from external imports, which are stubbed as empty packages; both are 0 when no Go code was checked.

## Client side

`mcp.Pool.Connect` consumes servers over stdio (`command`), Streamable HTTP
(`url`) or the 2024-11-05 HTTP+SSE transport (`url` with `transport = "sse"`),
negotiating the protocol version; tool names are qualified as
`<server name>__<tool>` unless `tool_prefix` says otherwise. `mcp.Connect` opens
a connection over any SDK transport, and `ServerConnection.Session` returns the
live SDK session for anything the pool does not wrap (resources, prompts).
Configuration keys and limits: [`configuration.md`](configuration.md).

What AgentKit adds on the client:

- a stdio server runs in its own process group with exactly the configured
  environment, and is killed as a group on close; its stderr is forwarded line
  by line to `ConnectionOptions.Warnf`;
- a dead server is re-spawned (or re-opened) at the next call, at most
  `per_session_reconnect_limit` times, and never by re-sending a call that was
  cut off;
- every inbound frame (stdio), JSON response body and SSE event (HTTP) is
  checked strictly before the SDK decodes it;
- the HTTP client sends the configured headers and does not follow redirects,
  because those headers may carry a bearer token;
- results are capped at 50,000 characters, calls are counted against
  `per_session_call_limit`, every call and every sampling request is audited,
  and sampling is advertised and answered only for a server with
  `allow_sampling`;
- a tool's `outputSchema` becomes `core.Tool.OutputSchema`. A non-object
  schema is wrapped as `{"value": <schema>}`. One that is not a JSON object
  is dropped, the tool is still imported, and `Pool.Diagnostics()` gains a
  `SeverityError` entry, for example
  `mcp: server "srv" tool "broken": output schema dropped: a schema must be a JSON object, got "not a valid schema object"`.

Constants: call limit 1000 per session, reconnect limit 3, timeout 30 s per
connect, list and call.
