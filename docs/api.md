# Network API

AgentKit is a library and exposes no network surface unless the embedding
application starts one. The only inbound surface it can start is the **MCP
server**, in `mcp/`. This file documents it. Outbound calls (to model vendors
and MCP servers) are documented in [`PROVIDERS.md`](PROVIDERS.md).

Protocol revision: **MCP `2026-07-28`, modern-only** (`mcp.ProtocolVersion`).
There is no `initialize` handshake, no protocol session, no `ping`, no GET
stream. Every request carries its protocol version and capabilities in
`params._meta`. A legacy `initialize` is answered with error `-32022` naming
the versions this server speaks.

## Starting it

Off by default. Nothing starts unless the host builds an `mcp.Server` and calls
one of:

| Call | Transport |
|---|---|
| `Server.ServeStdio(ctx)` | JSON-RPC over this process's stdin/stdout. No authentication: relies on OS process isolation. Nothing but protocol may be written to stdout; log to stderr (`ServerOptions.Warnf`). |
| `Server.ListenAndServeHTTP(ctx, addr, HTTPOptions)` | HTTP, described below. Shuts down gracefully, waiting up to 5 s for in-flight requests. |
| `Server.HTTPHandler(HTTPOptions)` | The same as an `http.Handler`, to mount yourself. |
| `Server.Run(ctx, ServerModeConfig, lookupEnv)` | Whichever mode `[mcp_server]` selects ([`configuration.md`](configuration.md)). Disabled config returns nil without listening. HTTP mode binds `127.0.0.1:<port>`. |

`examples/mcpserver` is a complete host ([`cli.md`](cli.md)).

## Registering content

`RegisterTool`, `UnregisterTool`, `RegisterResource`, `UnregisterResource` and
`RegisterResourceTemplate` may be called at any time. Registering or
withdrawing a tool notifies `subscriptions/listen` streams that asked for
`notifications/tools/list_changed`. Resource templates use `{var}` (one path
segment) and `{+var}` (may span `/`); an exact URI registration beats a
matching template. Lists are paginated when `ServerOptions.PageSize > 0`, with
cursors that name an entry.

## HTTP endpoint

A single endpoint; the handler does not inspect the path, so mount it wherever
you like. Only `POST` is accepted.

### Request checks, in order

| # | Check | Failure |
|---|---|---|
| 1 | API key in `Authorization` (`Bearer <key>` or the bare key) or `X-API-Key`; compared in constant time. `HTTPOptions.Headers` replaces the header list. | `401`, `WWW-Authenticate: Bearer realm="mcp"` |
| 2 | Method is `POST` | `405` |
| 3 | `Origin`: a request with no `Origin` passes; one with an `Origin` must match `HTTPOptions.AllowedOrigins` (`*` allows all). Default allowlist is empty. | `403` |
| 4 | Body read, capped at the smaller of `HTTPOptions.MaxBodyBytes` and the decoder's 16 MiB bound; decoded strictly (duplicate keys, depth and node bounds rejected). | JSON-RPC `-32700` / `-32600` |
| 5 | Routing headers agree with the body. | `400`, JSON-RPC `-32020` |

The key is mandatory: `HTTPHandler` returns `mcp.ErrNoAPIKey` for an empty one.

### Routing headers

| Header | Must equal |
|---|---|
| `MCP-Protocol-Version` | `params._meta["io.modelcontextprotocol/protocolVersion"]` (when present in the body) |
| `Mcp-Method` | the JSON-RPC `method` |
| `Mcp-Name` | `params.name` for `tools/call`, `params.uri` for `resources/read`; base64-sentinel-encoded when not header-safe ASCII |

### Responses

| Situation | Response |
|---|---|
| Notification (no `id`) | `202 Accepted`, empty body |
| Request | `200`, `application/json`, one JSON-RPC message |
| Unsupported protocol version | `400`, JSON-RPC error `-32022` with the supported versions |
| Header/body disagreement | `400`, JSON-RPC error `-32020` |
| `subscriptions/listen` | `200`, `text/event-stream`, held open |

Other JSON-RPC errors use the standard codes (`-32700` parse, `-32600` invalid
request, `-32601` method not found, `-32602` invalid params, `-32603`
internal) and `-32021` for a missing required client capability. The error
travels in the body with HTTP `200` unless a row above says otherwise.

## Methods

| Method | Notes |
|---|---|
| `server/discover` | Supported versions, capabilities, instructions, server info, cache hints. |
| `tools/list` | Paginated; result carries `ttlMs` and cache scope (`ServerOptions.ListTTLMs`, default 0 = immediately stale; `CacheScope`, default `private`). |
| `tools/call` | Handlers run concurrently, bounded. The result cap is spent across the whole result (counted in runes). Each call emits an audit event when `ServerOptions.Audit` is set. |
| `resources/list`, `resources/templates/list`, `resources/read` | As registered. |
| `subscriptions/listen` | Opt-in streaming of `notifications/tools/list_changed`, `notifications/resources/list_changed` and `notifications/resources/updated`. The request must name at least one type (else `-32602`); a connection with no subscription receives nothing. |
| `notifications/cancelled` | Cancels the handler's context and suppresses its reply; id matching is type-sensitive (`"5"` ≠ `5`). |
| `initialize` | Always rejected with `-32022`. |

`ping` and any other method return `-32601`.

## Client side

`mcp.Pool.Connect` consumes servers over stdio (`command`) or Streamable HTTP
(`url`); tool names are qualified as `<server name>__<tool>` unless
`tool_prefix` says otherwise. Configuration keys and limits:
[`configuration.md`](configuration.md). The default HTTP client does not follow
redirects, because the configured headers may carry a bearer token. Constants:
call limit 1000 per session, reconnect limit 3, timeout 30 s.
